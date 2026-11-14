package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/config"
	"github.com/Metixpro/macron-agent/pkg/telegram"
)

const (
	pidFile = "telegram.pid"
	logFile = "macron.log"
)

const telegramUsage = `usage: macron telegram [ACTION]
  (none)       connect the bot the first time, or show its state and what you can do
  setup        connect a bot, or change the token or the owner
  status       is it connected and running
  start | stop | restart
  logs [-f]    its log
  disconnect   stop it and forget the bot token
  run          run it in the foreground (what the service runs)
`

func telegramCmd(io *IO, args []string) int {
	sub := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	switch sub {
	case "":
		env, err := loadEnvFile(config.EnvFilePath())
		if err != nil {
			io.printf("❌ %v\n", err)
			return 1
		}
		if getEnv(env, keyToken) == "" {
			return telegramSetup(io, args)
		}
		return telegramPanel(io)
	case "setup", "connect", "configure":
		return telegramSetup(io, args)
	case "status":
		return telegramStatus(io)
	case "start", "stop", "restart":
		return telegramControl(io, sub)
	case "autostart":
		return windowsAutoStart(io, args)
	case "logs", "log":
		return telegramLogs(io, args)
	case "disconnect", "remove":
		return telegramDisconnect(io, args)
	case "help", "-h", "--help":
		io.print(telegramUsage)
		return 0
	}
	io.printf("macron telegram: unknown action %q\n\n%s", sub, telegramUsage)
	return 2
}

func telegramSetup(io *IO, args []string) int {
	fs := flag.NewFlagSet("telegram", flag.ContinueOnError)
	fs.SetOutput(io.Err)
	token := fs.String("token", "", "the bot token from @BotFather")
	owner := fs.String("owner", "", "your Telegram numeric id (comma separated for several owners)")
	yes := fs.Bool("yes", false, "ask nothing: use the flags and what config.env has already")
	noService := fs.Bool("no-service", false, "save the settings but do not start the bot in the background")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	return connectTelegram(io, *token, *owner, io.Interactive && !*yes, *noService)
}

func connectTelegram(io *IO, token, owner string, interactive, noService bool) int {
	ctx := context.Background()
	envPath := config.EnvFilePath()
	env, err := loadEnvFile(envPath)
	if err != nil {
		io.printf("❌ cannot read %s: %v\n", envPath, err)
		return 1
	}
	io.printf("\n✻ Connect Macron to Telegram\n\n")
	known := getEnv(env, keyToken)
	tok := strings.TrimSpace(token)
	if tok == "" && known != "" && (!interactive || io.yes("Keep the bot that is connected now?", true)) {
		tok = known
	}
	var me *telegram.User
	for attempt := 0; attempt < 3; attempt++ {
		if tok == "" {
			if !interactive {
				io.println("❌ no bot token: pass --token (create a bot with @BotFather in Telegram and copy its token)")
				return 1
			}
			io.println("1/3  The bot. In Telegram open @BotFather, send /newbot, choose a name, and paste the token it gives you.")
			t, err := io.Secret("     Bot token (hidden): ")
			if err != nil {
				return 1
			}
			tok = t
		}
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		u, err := telegram.NewBotWithBase(tok, telegramBase()).GetMe(cctx)
		cancel()
		if err == nil {
			me = u
			break
		}
		io.printf("     ✗ Telegram does not accept that token (%v)\n", shorten(err))
		if !interactive {
			return 1
		}
		tok = ""
	}
	if me == nil {
		return 1
	}
	io.printf("     ✓ bot @%s\n\n", me.Username)
	if tok != known && known != "" {
		stopDetached()
	}
	env.Set(keyToken, tok)

	owners := strings.TrimSpace(owner)
	if owners == "" {
		owners = getEnv(env, keyOwners)
		if owners != "" && interactive && !io.yes("Keep the owner "+owners+"?", true) {
			owners = ""
		}
	}
	if owners == "" {
		if !interactive {
			io.println("❌ no owner: pass --owner with your Telegram numeric id")
			return 1
		}
		io.println("2/3  You. The bot answers its owner and nobody else. Send any message to @" + me.Username + " now and I will see your id,")
		io.println("     or type your numeric id here (the number @userinfobot shows) and press Enter.")
		owners = askOwner(ctx, io, tok)
		if owners == "" {
			io.println("❌ no owner")
			return 1
		}
	}
	if _, err := config.ParseIDs(owners); err != nil {
		io.printf("❌ %v\n", err)
		return 1
	}
	io.printf("     ✓ owner %s\n\n", owners)
	env.Set(keyOwners, owners)
	if getEnv(env, keyTimezone) == "" {
		env.Set(keyTimezone, defaultZone())
	}
	if getEnv(env, keyAPIURL) == "" && interactive {
		io.println("     No model is set in config.env (fine if you added a provider in the bot's /settings).")
		if io.yes("     Set one now?", true) {
			if code := setupModel(ctx, io, env, true, "", "", "", false, true); code != 0 {
				return code
			}
		}
	}
	if err := env.Save(); err != nil {
		io.printf("❌ cannot save %s: %v\n", envPath, err)
		return 1
	}
	if noService {
		io.printf("✅ Saved. Start it with «macron telegram start» (or in the foreground: macron telegram run), then send /start to @%s.\n", me.Username)
		return 0
	}
	io.println("3/3  Running in the background")
	if code := startTelegram(io); code != 0 {
		return code
	}
	io.printf("\n✅ @%s is live: https://t.me/%s · send it /start\n", me.Username, me.Username)
	io.println("   Manage it: macron telegram   ·   its log: macron telegram logs -f")
	return 0
}

func selfBinary() (string, error) {
	bin, err := os.Executable()
	if err != nil {
		return "", err
	}
	if r, err := filepath.EvalSymlinks(bin); err == nil {
		bin = r
	}
	if runtime.GOOS == "windows" && strings.HasSuffix(strings.ToLower(bin), ".exe.old") {
		installed := bin[:len(bin)-4]
		if fileExists(installed) {
			bin = installed
		}
	}
	if runtime.GOOS == "windows" && strings.EqualFold(filepath.Base(bin), "macron-desktop.exe") {
		cli := filepath.Join(filepath.Dir(bin), "macron.exe")
		if fileExists(cli) {
			bin = cli
		}
	}
	return bin, nil
}

func trialRun(io *IO) bool {
	bin, err := selfBinary()
	if err != nil {
		io.printf("     ✗ %v\n", err)
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "telegram", "run", "--check").CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		lines := strings.Split(text, "\n")
		if len(lines) > 6 {
			lines = lines[len(lines)-6:]
		}
		io.printf("     ✗ the bot does not start here:\n       %s\n", strings.Join(lines, "\n       "))
		return false
	}
	last := text
	if i := strings.LastIndex(text, "\n"); i >= 0 {
		last = text[i+1:]
	}
	io.printf("     ✓ local settings and database checked (%s)\n", strings.TrimSpace(last))
	return true
}

func systemdUsable() bool {
	if runtime.GOOS != "linux" || !serviceSupported() {
		return false
	}
	if fi, err := os.Stat("/run/systemd/system"); err != nil || !fi.IsDir() {
		return false
	}
	if os.Geteuid() != 0 {
		if u, err := currentUnit(); err == nil && !u.System {
			return exec.Command("systemctl", "--user", "show-environment").Run() == nil
		}
	}
	return true
}

func startTelegram(io *IO) int {
	if !trialRun(io) {
		return 1
	}
	switch {
	case runtime.GOOS == "darwin":
		stopDetached()
		return serviceInstall(io)
	case systemdUsable():
		stopDetached()
		if code := serviceInstall(io); code != 0 {
			io.println("     ↷ the service did not start; running it as a background process instead")
			return startDetached(io)
		}
		return waitActive(io)
	}
	return startDetached(io)
}

func waitActive(io *IO) int {
	u, err := currentUnit()
	if err != nil {
		return 0
	}
	ctl := systemctl(u)
	time.Sleep(3 * time.Second)
	out, _ := exec.Command(ctl[0], append(ctl[1:], "is-active", unitName)...).Output()
	if state := strings.TrimSpace(string(out)); state != "active" {
		io.printf("     ✗ the service is %s; see: macron telegram logs\n", orWord(state, "not running"))
		return 1
	}
	io.println("     ✓ running as a service: it restarts by itself and starts at boot")
	return 0
}

func readPID() int {
	b, err := os.ReadFile(config.HomePath(pidFile))
	if err != nil {
		return 0
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	if !alive(pid) || !ownsBackgroundProcess(pid) {
		return 0
	}
	if cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid)); err == nil && !strings.Contains(string(cmdline), "telegram") {
		return 0
	}
	return pid
}

func stopDetached() bool {
	pid := readPID()
	if pid == 0 {
		_ = os.Remove(config.HomePath(pidFile))
		return false
	}
	if err := terminate(pid); err != nil {
		return false
	}
	for i := 0; i < 40 && alive(pid); i++ {
		time.Sleep(250 * time.Millisecond)
	}
	if alive(pid) {
		return false
	}
	_ = os.Remove(config.HomePath(pidFile))
	return true
}

func startDetached(io *IO) int {
	if readPID() > 0 && !stopDetached() {
		io.println("Cannot stop the previous bot process; no second copy was started.")
		return 1
	}
	bin, err := selfBinary()
	if err != nil {
		io.printf("❌ %v\n", err)
		return 1
	}
	logPath := config.HomePath(logFile)
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		io.printf("❌ cannot open %s: %v\n", logPath, err)
		return 1
	}
	defer f.Close()
	cmd := exec.Command(bin, "telegram", "run")
	cmd.Dir = config.Home()
	cmd.Stdout, cmd.Stderr = f, f
	cmd.SysProcAttr = detached()
	if err := cmd.Start(); err != nil {
		io.printf("❌ cannot start the bot: %v\n", err)
		return 1
	}
	pid := cmd.Process.Pid
	if err := os.WriteFile(config.HomePath(pidFile), []byte(strconv.Itoa(pid)+"\n"), 0o600); err != nil {
		_ = terminate(pid)
		_ = cmd.Wait()
		io.printf("❌ cannot save background process: %v\n", err)
		return 1
	}
	_ = cmd.Process.Release()
	time.Sleep(3 * time.Second)
	if !alive(pid) {
		io.printf("     ✗ the bot stopped right away; see: %s\n", logPath)
		return 1
	}
	io.printf("     ✓ running in the background (process %d, log %s)\n", pid, logPath)
	if runtime.GOOS == "windows" {
		io.println("       To start it when you sign in: macron service install")
	} else {
		io.println("       There is no system service here, so start it again after a reboot: macron telegram start")
	}
	return 0
}

type tgState struct {
	how     string
	running bool
}

func telegramState() tgState {
	if pid := readPID(); pid > 0 {
		return tgState{how: fmt.Sprintf("background process %d", pid), running: true}
	}
	switch runtime.GOOS {
	case "darwin":
		if _, err := os.Stat(launchdPath()); err == nil {
			return tgState{how: "launchd service", running: exec.Command("launchctl", "list", launchLabel).Run() == nil}
		}
	case "linux":
		if !serviceSupported() {
			break
		}
		u, err := currentUnit()
		if err != nil {
			break
		}
		if _, err := os.Stat(unitPath(u)); err != nil {
			break
		}
		ctl := systemctl(u)
		how := "systemd user service"
		if u.System {
			how = "systemd service"
		}
		return tgState{how: how, running: exec.Command(ctl[0], append(ctl[1:], "is-active", "--quiet", unitName)...).Run() == nil}
	}
	return tgState{}
}

func telegramStatus(io *IO) int {
	env, err := loadEnvFile(config.EnvFilePath())
	if err != nil {
		io.printf("❌ %v\n", err)
		return 1
	}
	tok := getEnv(env, keyToken)
	if tok == "" {
		io.println("Telegram is not connected. Connect it: macron telegram")
		return 3
	}
	bot := "the token is set"
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	me, err := telegram.NewBotWithBase(tok, telegramBase()).GetMe(ctx)
	cancel()
	if err != nil {
		bot = "✗ Telegram refuses the token: " + shorten(err)
	} else {
		bot = "@" + me.Username + "  https://t.me/" + me.Username
	}
	st := telegramState()
	run := "○ not running · start it: macron telegram start"
	switch {
	case st.running:
		run = "● running (" + st.how + ")"
	case st.how != "":
		run = "○ stopped (" + st.how + ") · start it: macron telegram start"
	}
	io.printf("  bot      %s\n  owner    %s\n  state    %s\n", bot, orWord(getEnv(env, keyOwners), "none"), run)
	if !st.running {
		return 3
	}
	return 0
}

func telegramPanel(io *IO) int {
	io.printf("\n✻ Telegram\n\n")
	code := telegramStatus(io)
	if !io.Interactive {
		return code
	}
	running := code == 0
	toggle := "Start"
	if running {
		toggle = "Stop"
	}
	io.printf("\n  1) Restart   2) %s   3) Show the log   4) Change the bot or the owner   5) Disconnect   0) Done\n", toggle)
	switch io.ask("  Choose", "0") {
	case "1":
		return telegramControl(io, "restart")
	case "2":
		if running {
			return telegramControl(io, "stop")
		}
		return telegramControl(io, "start")
	case "3":
		return telegramLogs(io, []string{"-n", "60"})
	case "4":
		return connectTelegram(io, "", "", true, false)
	case "5":
		return telegramDisconnect(io, nil)
	}
	return 0
}

func telegramControl(io *IO, action string) int {
	env, err := loadEnvFile(config.EnvFilePath())
	if err != nil || getEnv(env, keyToken) == "" {
		io.println("Telegram is not connected yet: macron telegram")
		return 1
	}
	st := telegramState()
	switch {
	case st.how == "" && action == "stop":
		io.println("It is not running.")
		return 0
	case strings.HasPrefix(st.how, "background") && action == "stop":
		if !stopDetached() {
			io.println("Cannot stop the background process.")
			return 1
		}
		io.println("✓ stopped")
		return 0
	case st.how == "" || strings.HasPrefix(st.how, "background"):
		return startTelegram(io)
	}
	if code := serviceControl(io, action); code != 0 {
		return code
	}
	if action == "stop" {
		io.println("✓ stopped")
		return 0
	}
	if runtime.GOOS == "linux" {
		return waitActive(io)
	}
	io.println("✓ " + action + "ed")
	return 0
}

func telegramLogs(io *IO, args []string) int {
	st := telegramState()
	if st.how == "" || strings.HasPrefix(st.how, "background") || runtime.GOOS == "darwin" || runtime.GOOS == "windows" {
		return fileLogs(io, args)
	}
	return logs(io, args)
}

func telegramDisconnect(io *IO, args []string) int {
	yes := len(args) > 0 && (args[0] == "--yes" || args[0] == "-y")
	if !yes && (!io.Interactive || !io.yes("Stop the bot and forget its token? Conversations and settings stay.", false)) {
		return 1
	}
	stopDetached()
	if st := telegramState(); st.how != "" {
		_ = serviceUninstall(io)
	}
	env, err := loadEnvFile(config.EnvFilePath())
	if err != nil {
		io.printf("❌ %v\n", err)
		return 1
	}
	env.Unset(keyToken)
	if err := env.Save(); err != nil {
		io.printf("❌ %v\n", err)
		return 1
	}
	io.println("✓ Telegram is disconnected. Connect it again any time: macron telegram")
	return 0
}

func orWord(s, alt string) string {
	if strings.TrimSpace(s) == "" {
		return alt
	}
	return s
}

func TelegramSummary() string {
	env, err := loadEnvFile(config.EnvFilePath())
	if err != nil || getEnv(env, keyToken) == "" {
		return "not connected · /telegram"
	}
	if st := telegramState(); st.running {
		return "connected · running"
	}
	return "connected · stopped · /telegram"
}
