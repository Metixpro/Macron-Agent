package cli

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/Metixpro/macron-agent/pkg/config"
)

var (
	unitName    = config.ServiceName() + ".service"
	launchLabel = "app.macron.agent" + strings.TrimPrefix(config.ServiceName(), "macron")
)

type unit struct {
	Binary string
	Home   string
	User   string
	System bool
}

func systemdPath(p string) string {
	return strings.NewReplacer("%", "%%", "\n", "", "\r", "").Replace(p)
}

func systemdWord(s string, execValue bool) string {
	s = systemdPath(s)
	if execValue {
		s = strings.ReplaceAll(s, "$", "$$")
	}
	if !strings.ContainsAny(s, " \t\"'\\;") && s != "" {
		return s
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

func renderSystemd(u unit) string {
	var sb strings.Builder
	sb.WriteString("[Unit]\nDescription=Macron Agent (Telegram bot)\nAfter=network-online.target\nWants=network-online.target\n\n[Service]\nType=simple\n")
	if u.System && u.User != "" {
		fmt.Fprintf(&sb, "User=%s\n", u.User)
	}
	fmt.Fprintf(&sb, "Environment=%s\n", systemdWord("MACRON_HOME="+u.Home, false))
	fmt.Fprintf(&sb, "WorkingDirectory=%s\n", systemdPath(u.Home))
	fmt.Fprintf(&sb, "ExecStart=%s telegram run\n", systemdWord(u.Binary, true))
	sb.WriteString("Restart=always\nRestartSec=5\nTimeoutStopSec=45\nKillMode=mixed\nLimitNOFILE=65536\nUMask=0077\n\n[Install]\n")
	if u.System {
		sb.WriteString("WantedBy=multi-user.target\n")
	} else {
		sb.WriteString("WantedBy=default.target\n")
	}
	return sb.String()
}

func verifyUnit(u unit, path string) (out string, checked bool, err error) {
	if _, lerr := exec.LookPath("systemd-analyze"); lerr != nil {
		return "", false, nil
	}
	args := []string{"verify", path}
	if !u.System && os.Getenv("XDG_RUNTIME_DIR") != "" {
		args = []string{"--user", "verify", path}
	}
	b, err := exec.Command("systemd-analyze", args...).CombinedOutput()
	out = string(b)
	if err != nil && strings.Contains(out, "Failed to initialize manager") {

		b, err = exec.Command("systemd-analyze", "verify", path).CombinedOutput()
		out = string(b)
		if err != nil && strings.Contains(out, "Failed to initialize manager") {
			return out, false, nil
		}
	}
	if err != nil && (strings.Contains(out, "Read-only file system") || strings.Contains(out, "Failed to connect to bus")) {
		return out, false, nil
	}
	if err == nil {
		return out, true, nil
	}

	base := filepath.Base(path)
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, base) {
			return out, true, err
		}
	}
	return out, true, err
}

func renderLaunchd(u unit) string {
	escape := func(s string) string {
		var b bytes.Buffer
		_ = xml.EscapeText(&b, []byte(s))
		return b.String()
	}
	u.Binary, u.Home = escape(u.Binary), escape(u.Home)
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>%s</string>
	<key>ProgramArguments</key><array><string>%s</string><string>telegram</string><string>run</string></array>
	<key>EnvironmentVariables</key><dict><key>MACRON_HOME</key><string>%s</string></dict>
	<key>WorkingDirectory</key><string>%s</string>
	<key>RunAtLoad</key><true/>
	<key>KeepAlive</key><true/>
	<key>StandardOutPath</key><string>%s/macron.log</string>
	<key>StandardErrorPath</key><string>%s/macron.log</string>
</dict>
</plist>
`, launchLabel, u.Binary, u.Home, u.Home, u.Home, u.Home)
}

func serviceSupported() bool {
	switch runtime.GOOS {
	case "linux":
		_, err := exec.LookPath("systemctl")
		return err == nil
	case "darwin":
		return true
	}
	return false
}

func service(io *IO, args []string) int {
	if len(args) == 0 {
		io.println("usage: macron service install | uninstall | start | stop | restart | status")
		return 2
	}
	if !serviceSupported() {
		io.printf("❌ %v\n", errNoService)
		return 1
	}
	action := args[0]
	switch action {
	case "install":
		return serviceInstall(io)
	case "uninstall":
		return serviceUninstall(io)
	case "start", "stop", "restart", "status":
		return serviceControl(io, action)
	}
	io.printf("unknown action %q\n", action)
	return 2
}

func currentUnit() (unit, error) {
	bin, err := os.Executable()
	if err != nil {
		return unit{}, err
	}
	if r, err := filepath.EvalSymlinks(bin); err == nil {
		bin = r
	}
	home, err := filepath.Abs(config.Home())
	if err != nil {
		return unit{}, err
	}
	u := unit{Binary: bin, Home: home, System: os.Geteuid() == 0 && runtime.GOOS == "linux"}
	if cu, err := user.Current(); err == nil {
		u.User = cu.Username
	}
	record, recordErr := config.ReadInstallation(bin)
	if recordErr == nil && record.UID == os.Geteuid() {
		u.System = record.Scope == "system"
	}
	if u.System && recordErr != nil {

		if su := os.Getenv("SUDO_USER"); su != "" && su != "root" {
			if tu, err := user.Lookup(su); err == nil {
				u.User = tu.Username
				if os.Getenv(config.EnvHome) == "" {
					u.Home = filepath.Join(tu.HomeDir, ".macron")
				}
			}
		}
	}
	return u, nil
}

func unitPath(u unit) string {
	if u.System {
		return "/etc/systemd/system/" + unitName
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".config", "systemd", "user", unitName)
}

func run(io *IO, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout, cmd.Stderr = io.Out, io.Err
	return cmd.Run()
}

func serviceInstall(io *IO) int {
	u, err := currentUnit()
	if err != nil {
		io.printf("❌ %v\n", err)
		return 1
	}
	env, err := loadEnvFile(filepath.Join(u.Home, config.FileConfig))
	if err != nil || getEnv(env, keyToken) == "" {
		io.println("❌ Telegram is not connected yet: run «macron telegram» (it sets up the bot and this service)")
		return 1
	}
	if runtime.GOOS == "darwin" {
		return launchdInstall(io, u)
	}
	path := unitPath(u)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		io.printf("❌ %v\n", err)
		return 1
	}
	content := renderSystemd(u)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		io.printf("❌ cannot write %s: %v\n", path, err)
		return 1
	}
	if out, checked, err := verifyUnit(u, path); err != nil {
		io.printf("❌ the generated unit is invalid: %v\n%s\n--- %s ---\n%s", err, strings.TrimSpace(out), path, content)
		return 1
	} else if !checked {
		io.println("⚠️  systemd-analyze could not check the unit here; enabling it anyway.")
	}
	if u.System && u.User != "" && u.User != "root" {
		if tu, err := user.Lookup(u.User); err == nil {
			uid, _ := strconv.Atoi(tu.Uid)
			gid, _ := strconv.Atoi(tu.Gid)
			_ = filepath.Walk(u.Home, func(p string, _ os.FileInfo, err error) error {
				if err == nil {
					_ = os.Lchown(p, uid, gid)
				}
				return nil
			})
		}
	}
	ctl := systemctl(u)

	for _, a := range [][]string{{"daemon-reload"}, {"enable", unitName}, {"restart", unitName}} {
		if err := run(io, ctl[0], append(ctl[1:], a...)...); err != nil {
			io.printf("❌ systemctl %s: %v\n", strings.Join(a, " "), err)
			return 1
		}
	}
	if !u.System {
		if cu, err := user.Current(); err == nil {
			if err := exec.Command("loginctl", "enable-linger", cu.Username).Run(); err != nil {
				io.println("⚠️  could not enable lingering: the agent will stop when you log out. Fix: sudo loginctl enable-linger " + cu.Username)
			}
		}
	}
	io.printf("✅ The agent runs as a service now (%s).\n   Status: macron service status   ·   Log: macron logs -f\n", path)
	return 0
}

func systemctl(u unit) []string {
	if u.System {
		return []string{"systemctl"}
	}
	return []string{"systemctl", "--user"}
}

func serviceUninstall(io *IO) int {
	u, err := currentUnit()
	if err != nil {
		io.printf("❌ %v\n", err)
		return 1
	}
	if runtime.GOOS == "darwin" {
		return launchdUninstall(io)
	}
	if _, err := os.Stat(unitPath(u)); err != nil {
		io.println("There is no service to remove. Your data in " + u.Home + " is untouched.")
		return 0
	}
	ctl := systemctl(u)
	_ = run(io, ctl[0], append(ctl[1:], "disable", "--now", unitName)...)
	_ = os.Remove(unitPath(u))
	_ = run(io, ctl[0], append(ctl[1:], "daemon-reload")...)
	io.println("✅ The service is removed. Your data in " + u.Home + " is untouched.")
	return 0
}

func serviceControl(io *IO, action string) int {
	u, err := currentUnit()
	if err != nil {
		io.printf("❌ %v\n", err)
		return 1
	}
	if runtime.GOOS == "darwin" {
		return launchdControl(io, action)
	}
	ctl := systemctl(u)
	args := append(ctl[1:], action, unitName)
	if action == "status" {
		args = append(args, "--no-pager")
	}
	if err := run(io, ctl[0], args...); err != nil {
		if action == "status" {
			return 3
		}
		io.printf("❌ systemctl %s: %v\n", action, err)
		return 1
	}
	return 0
}

func launchdPath() string {
	h, _ := os.UserHomeDir()
	return filepath.Join(h, "Library", "LaunchAgents", launchLabel+".plist")
}

func launchdInstall(io *IO, u unit) int {
	path := launchdPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		io.printf("❌ %v\n", err)
		return 1
	}
	if err := os.WriteFile(path, []byte(renderLaunchd(u)), 0o644); err != nil {
		io.printf("❌ %v\n", err)
		return 1
	}
	_ = exec.Command("launchctl", "unload", path).Run()
	if err := run(io, "launchctl", "load", "-w", path); err != nil {
		io.printf("❌ launchctl: %v\n", err)
		return 1
	}
	io.printf("✅ The agent runs as a launchd service now (%s). Log: %s\n", path, filepath.Join(u.Home, "macron.log"))
	return 0
}

func launchdUninstall(io *IO) int {
	path := launchdPath()
	_ = run(io, "launchctl", "unload", "-w", path)
	_ = os.Remove(path)
	io.println("✅ The service is removed.")
	return 0
}

func launchdControl(io *IO, action string) int {
	path := launchdPath()
	switch action {
	case "start":
		return exitCode(run(io, "launchctl", "load", "-w", path))
	case "stop":
		return exitCode(run(io, "launchctl", "unload", path))
	case "restart":
		_ = exec.Command("launchctl", "unload", path).Run()
		return exitCode(run(io, "launchctl", "load", "-w", path))
	default:
		return exitCode(run(io, "launchctl", "list", launchLabel))
	}
}

func exitCode(err error) int {
	if err != nil {
		return 1
	}
	return 0
}

func logs(io *IO, args []string) int {
	follow := false
	lines := "100"
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-f", "--follow":
			follow = true
		case "-n":
			if i+1 < len(args) {
				lines = args[i+1]
				i++
			}
		}
	}
	u, err := currentUnit()
	if err != nil {
		io.printf("❌ %v\n", err)
		return 1
	}
	var cmd *exec.Cmd
	switch {
	case runtime.GOOS == "darwin":
		a := []string{"-n", lines}
		if follow {
			a = append(a, "-f")
		}
		cmd = exec.Command("tail", append(a, filepath.Join(u.Home, "macron.log"))...)
	default:
		a := []string{"-n", lines, "--no-pager", "-o", "cat"}
		if u.System {
			a = append(a, "-u", unitName)
		} else {
			a = append(a, "--user-unit", unitName)
		}
		if follow {
			a = append(a, "-f")
		}
		cmd = exec.Command("journalctl", a...)
	}
	cmd.Stdout, cmd.Stderr = io.Out, io.Err
	if err := cmd.Run(); err != nil {
		io.printf("❌ %v\n", err)
		return 1
	}
	return 0
}
