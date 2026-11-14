package cli

import (
	"context"
	"flag"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/app"
	"github.com/Metixpro/macron-agent/pkg/config"
	"github.com/Metixpro/macron-agent/pkg/diskfree"
	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/moa"
	"github.com/Metixpro/macron-agent/pkg/providers"
	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/telegram"
	"github.com/Metixpro/macron-agent/pkg/updater"
)

type level int

const (
	ok level = iota
	warn
	fail
)

type report struct {
	io           *IO
	fails        int
	warns        int
	requireTools bool
}

func (r *report) line(l level, what, hint string) {
	mark := map[level]string{ok: "✅", warn: "⚠️ ", fail: "❌"}[l]
	r.io.printf("%s %s\n", mark, what)
	if hint != "" && l != ok {
		r.io.printf("     → %s\n", hint)
	}
	switch l {
	case fail:
		r.fails++
	case warn:
		r.warns++
	}
}

type doctorTool struct {
	name, why, apt, brew, winget string
	unixOnly                     bool
}

var doctorTools = []doctorTool{
	{name: "git", why: "projects", apt: "git", brew: "git", winget: "Git.Git"},
	{name: "curl", why: "command-line downloads (Macron also has a built-in HTTP client)", apt: "curl", brew: "curl", winget: "cURL.cURL"},
	{name: "ssh", why: "external SSH commands (Macron also has built-in SSH)", apt: "openssh-client", brew: "openssh"},
	{name: "sshpass", why: "external password-based SSH commands", apt: "sshpass", brew: "sshpass", unixOnly: true},
	{name: "rsync", why: "external file synchronization", apt: "rsync", brew: "rsync", unixOnly: true},
	{name: "gh", why: "GitHub pull requests and issues", apt: "gh", brew: "gh", winget: "GitHub.cli"},
	{name: "ffmpeg", why: "audio and video", apt: "ffmpeg", brew: "ffmpeg", winget: "Gyan.FFmpeg"},
	{name: "node", why: "web projects and MCP servers", apt: "nodejs npm", brew: "node", winget: "OpenJS.NodeJS.LTS"},
	{name: "yt-dlp", why: "media downloads", apt: "yt-dlp", brew: "yt-dlp", winget: "yt-dlp.yt-dlp"},
	{name: "pdftotext", why: "PDF text extraction", apt: "poppler-utils", brew: "poppler"},
	{name: "7z", why: "7z and RAR archives", apt: "p7zip-full", brew: "sevenzip", winget: "7zip.7zip"},
}

func toolInstallHint(t doctorTool, goos string) string {
	switch goos {
	case "windows":
		if t.winget != "" {
			return "winget install --exact --id " + t.winget
		}
		if t.name == "ssh" {
			return "enable OpenSSH Client in Windows Settings > System > Optional features"
		}
		return "install Poppler for Windows and add its bin folder to Path, or use Macron's Python PDF tools"
	case "darwin":
		return "brew install " + t.brew
	default:
		return "install with your distribution's package manager (Debian/Ubuntu: sudo apt install " + t.apt + ")"
	}
}

func doctor(io *IO, args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(io.Err)
	toolsOnly := fs.Bool("tools-only", false, "check local tool dependencies without Telegram/model settings")
	requireTools := fs.Bool("require-tools", false, "treat missing Python/document dependencies as errors")
	offline := fs.Bool("offline", false, "do not call Telegram or the model")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	r := &report{io: io, requireTools: *requireTools}
	if *toolsOnly {
		checkToolDependencies(r)
		if r.fails > 0 {
			return 1
		}
		return 0
	}
	ctx := context.Background()
	home := config.Home()
	io.printf("Macron Agent %s · %s/%s · home %s\n\n", app.Version, runtime.GOOS, runtime.GOARCH, home)

	if fi, err := os.Stat(home); err != nil || !fi.IsDir() {
		r.line(fail, "the home folder does not exist", "run: macron setup")
		return 1
	} else if runtime.GOOS != "windows" && fi.Mode().Perm()&0o077 != 0 {
		r.line(warn, "the home folder can be read by other users on this machine", "chmod 700 "+home)
	} else {
		r.line(ok, "home folder "+home, "")
	}
	envPath := config.EnvFilePath()
	env, err := loadEnvFile(envPath)
	switch {
	case err != nil:
		r.line(fail, "cannot read settings: "+shorten(err), "check access to "+envPath)
		env = &envFile{path: envPath}
	case len(env.lines) == 0:
		r.line(ok, "no config.env overrides (desktop settings are stored in macron.db)", "")
	default:
		if fi, err := os.Stat(envPath); err == nil && runtime.GOOS != "windows" && fi.Mode().Perm()&0o077 != 0 {
			r.line(warn, envPath+" holds keys and other users can read it", "chmod 600 "+envPath)
		} else {
			r.line(ok, "settings "+envPath, "")
		}
	}
	for _, f := range []string{config.FileSoul, config.FileUser, config.FileMemory} {
		if _, err := os.Stat(filepath.Join(home, f)); err != nil {
			r.line(warn, f+" is not there yet", "it is written the first time the agent starts")
		}
	}

	token := doctorValue(env, keyToken)
	owners := doctorValue(env, keyOwners)
	if token == "" {
		r.line(ok, "Telegram is not connected (optional)", "to reach Macron from your phone: macron telegram")
	} else if !*offline {
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		me, err := telegram.NewBotWithBase(token, telegramBase()).GetMe(cctx)
		cancel()
		if err != nil {
			r.line(fail, "Telegram refuses the bot token: "+shorten(err), "get a new token from @BotFather and run: macron config set TELEGRAM_BOT_TOKEN <token>")
		} else {
			r.line(ok, "Telegram: the bot is @"+me.Username, "")
		}
	}
	if _, err := config.ParseIDs(owners); token != "" && (err != nil || owners == "") {
		r.line(fail, "no valid owner id for the bot", "run: macron telegram setup")
	} else if token != "" {
		r.line(ok, "owner(s): "+owners, "")
	}

	var savedModel *store.Provider
	dbPath := getEnvDefault(env, "MACRON_DB", filepath.Join(home, config.FileDB))
	if _, err := os.Stat(dbPath); err != nil {
		r.line(warn, "no database yet ("+dbPath+")", "it is created the first time the agent starts")
	} else {
		savedModel = reportDatabase(ctx, r, dbPath, env)
	}

	url, key, model := doctorValue(env, keyAPIURL), doctorValue(env, keyAPIKey), doctorValue(env, keyModel)
	if savedModel != nil {
		url, key, model = savedModel.BaseURL, savedModel.APIKey, savedModel.Model
		r.line(ok, "saved provider "+savedModel.Name+" · "+model, "")
	}
	switch {
	case url == "":
		r.line(warn, "no model in the settings", "open Settings > Providers in the desktop app, or run: macron setup")
	case !*offline:
		base, api := llm.CleanBaseURL(url)
		c := llm.NewClient(base, key, model)
		if savedModel != nil {
			c.API, c.NoStream = providers.API(*savedModel), savedModel.NoStream
		}
		if api != "" {
			c.API = api
		}
		cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		reply, err := c.Chat(cctx, []llm.Message{{Role: "user", Content: "Reply with the single word: ok"}}, nil)
		cancel()
		if err != nil {
			r.line(fail, "the model "+model+" does not answer: "+shorten(err), "check the address, the key and the model name; see: macron config list")
		} else {
			r.line(ok, "the model "+model+" answers ("+strings.TrimSpace(reply.Content)+")", "")
		}
	}

	who := currentUser()
	if runtime.GOOS != "windows" && os.Geteuid() == 0 {
		r.line(warn, "the agent runs as root: it can do anything on this machine", "that is what a personal server is for; to limit it, run it as a normal user")
	} else {
		r.line(ok, "the agent runs commands as "+who+", with that user's full rights", "")
	}
	checkToolDependencies(r)
	if _, free, ok := diskfree.Space(home); ok && free < 1<<30 {
		r.line(warn, "less than 1 GB of disk space is free", "files and downloads need room")
	}

	if st := telegramState(); token != "" && st.running {
		r.line(ok, "the Telegram bot is running ("+st.how+")", "")
	} else if token != "" {
		r.line(warn, "the Telegram bot is not running", "run: macron telegram start")
	}
	if serviceSupported() && token != "" {
		u, _ := currentUnit()
		if runtime.GOOS == "linux" {
			if !u.System {
				out, _ := exec.Command("loginctl", "show-user", currentUser(), "-p", "Linger", "--value").Output()
				if strings.TrimSpace(string(out)) == "yes" {
					r.line(ok, "lingering is on: the service keeps running after you log out", "")
				} else {
					r.line(warn, "lingering is off: the service stops when your last SSH session ends", "sudo loginctl enable-linger "+currentUser())
				}
			}
		}
	}

	if !*offline {
		cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
		rel, err := updater.Latest(cctx, "")
		cancel()
		switch {
		case err != nil:
			r.line(warn, "could not look for a newer release: "+shorten(err), "")
		case updater.Newer(rel.Version(), app.Version):
			r.line(warn, "a newer release is out: "+rel.Version()+" (you have "+app.Version+")", "run: macron update (or /update in Telegram)")
		default:
			r.line(ok, "the newest release ("+app.Version+")", "")
		}
	}
	io.println()
	switch {
	case r.fails > 0:
		io.printf("%d problem(s) to fix.\n", r.fails)
		return 1
	case r.warns > 0:
		io.printf("Works, with %d thing(s) worth a look.\n", r.warns)
	default:
		io.println("All good.")
	}
	return 0
}

func doctorValue(env *envFile, key string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return getEnv(env, key)
}

func getEnvDefault(env *envFile, key, def string) string {
	if v := doctorValue(env, key); v != "" {
		return v
	}
	return def
}

func reportDatabase(ctx context.Context, r *report, dbPath string, env *envFile) *store.Provider {
	secret := strings.TrimSpace(os.Getenv(config.EnvSecretKey))
	if secret == "" {
		secret = getEnv(env, config.EnvSecretKey)
	}
	if secret == "" {
		data, _ := os.ReadFile(config.HomePath(config.FileSecret))
		secret = strings.TrimSpace(string(data))
	}
	st, err := store.Open(ctx, dbPath, store.Options{SecretKey: secret})
	if err != nil {
		r.line(fail, "the database does not open: "+shorten(err), "restore it from a backup: macron backup makes them")
		return nil
	}
	defer st.Close()
	var verdict string
	if err := st.DB().QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&verdict); err != nil || verdict != "ok" {
		r.line(fail, "the database is damaged ("+verdict+")", "restore it from a backup")
		return nil
	}
	var turns int
	_ = st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM chat_archive`).Scan(&turns)
	size, _ := st.DatabaseSize(ctx)
	r.line(ok, "database "+dbPath+" ("+humanBytes(size)+", "+itoa(turns)+" messages kept and searchable)", "")
	reportMoA(ctx, r, st)
	manager, err := providers.New(ctx, st, nil, nil)
	if err != nil {
		r.line(fail, "cannot read saved providers: "+shorten(err), "check Settings > Providers")
		return nil
	}
	if p, ok := manager.Active(); ok {
		return &p
	}
	return nil
}

func reportMoA(ctx context.Context, r *report, st *store.Store) {
	raw, _ := st.Setting("moa.presets")
	list, err := moa.ParsePresets(raw)
	if err != nil {
		r.line(fail, shorten(err), "fix it: macron config set moa.presets '[...]' (or the panel: 🦾 → ⚖️)")
		return
	}
	if len(list) == 0 {
		return
	}
	provs, err := st.Providers(ctx)
	if err != nil {
		return
	}
	resolve := func(name string) (bool, bool, bool) {
		if strings.EqualFold(name, "moa") {
			return true, true, true
		}
		id, _ := strconv.ParseInt(name, 10, 64)
		for _, p := range provs {
			if strings.EqualFold(p.Name, name) || id > 0 && p.ID == id {
				local := strings.Contains(p.BaseURL, "127.0.0.1") || strings.Contains(p.BaseURL, "localhost")
				return p.Enabled, p.APIKey != "" || local, false
			}
		}
		return false, false, false
	}
	bad := 0
	for _, p := range list {
		for _, pr := range moa.Problems(p, resolve) {
			r.line(fail, "multi-model preset: "+pr, "fix it in the panel: 🦾 → ⚖️")
			bad++
		}
	}
	if bad == 0 {
		r.line(ok, itoa(len(list))+" multi-model preset(s) resolve", "")
	}
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return itoa(int(n>>30)) + " GB"
	case n >= 1<<20:
		return itoa(int(n>>20)) + " MB"
	}
	return itoa(int(n>>10)) + " KB"
}

func itoa(n int) string { return strconv.Itoa(n) }

func currentUser() string {
	if u, err := user.Current(); err == nil {
		return u.Username
	}
	return "this user"
}

func (r *report) toolIssue(what, hint string) {
	l := warn
	if r.requireTools {
		l = fail
	}
	r.line(l, what, hint)
}

func checkToolDependencies(r *report) {
	py := config.ToolPython()
	checkPython(r, py)
	if runtime.GOOS == "windows" {
		if _, err := exec.LookPath("pwsh"); err == nil {
			r.line(ok, "PowerShell 7", "")
		} else if _, err := exec.LookPath("powershell.exe"); err == nil {
			r.line(ok, "Windows PowerShell", "")
		} else {
			r.line(fail, "PowerShell is missing", "install PowerShell: winget install --exact --id Microsoft.PowerShell")
		}
	}
	for _, t := range doctorTools {
		if runtime.GOOS == "windows" && t.unixOnly {
			continue
		}
		_, err := exec.LookPath(t.name)
		if t.name == "7z" && err != nil {
			_, err = exec.LookPath("7zz")
		}
		if err == nil {
			r.line(ok, t.name+" ("+t.why+")", "")
			continue
		}
		r.line(warn, t.name+" is optional and not installed ("+t.why+")", toolInstallHint(t, runtime.GOOS))
	}
	if len(py) == 0 {
		return
	}
	for _, dep := range []struct{ module, packageName string }{
		{"qrcode", "qrcode"}, {"matplotlib", "matplotlib"}, {"reportlab", "reportlab"},
		{"pypdf", "pypdf"}, {"pymupdf", "PyMuPDF"}, {"arabic_reshaper", "arabic-reshaper"},
		{"bidi.algorithm", "python-bidi"}, {"playwright.sync_api", "playwright"},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := exec.CommandContext(ctx, py[0], append(append([]string{}, py[1:]...), "-I", "-c", "import importlib; importlib.import_module("+strconv.Quote(dep.module)+")")...).Run()
		cancel()
		if err != nil {
			r.toolIssue("optional Python package "+dep.packageName+" is missing or cannot import", toolRequirementsHint())
		} else {
			r.line(ok, "Python package "+dep.packageName, "")
		}
	}
}

func pythonInstallHint(goos string) string {
	switch goos {
	case "windows":
		return "winget install --exact --id Python.Python.3.12"
	case "darwin":
		return "brew install python@3.12"
	default:
		return "install Python 3.12+ with your distribution's package manager, or: uv python install 3.12"
	}
}

func toolRequirementsHint() string {
	py := filepath.Join(config.Home(), "venv", "bin", "python")
	create := "python3"
	if runtime.GOOS == "windows" {
		py = filepath.Join(config.Home(), "venv", "Scripts", "python.exe")
		create = "py -3"
		if _, err := exec.LookPath("py"); err != nil {
			if _, err := exec.LookPath("python"); err == nil {
				create = "python"
			}
		}
	}
	quote := func(s string) string {
		if runtime.GOOS == "windows" {
			return "'" + strings.ReplaceAll(s, "'", "''") + "'"
		}
		return shellQuote(s)
	}
	run := quote(py)
	if runtime.GOOS == "windows" {
		run = "& " + run
	}
	return "from the Macron release folder, run:\n       " + create + " -m venv " + quote(config.HomePath("venv")) + "\n       " + run + " -m pip install --require-hashes -r requirements-tools.lock"
}

func checkPython(r *report, py []string) {
	if len(py) == 0 {
		r.toolIssue("Python is optional and not installed (scripts and document tools)", pythonInstallHint(runtime.GOOS))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, py[0], append(append([]string{}, py[1:]...), "-I", "-c", "import sys; print('%d.%d' % sys.version_info[:2]); print(sys.prefix)")...).Output()
	if err != nil {
		r.toolIssue("Python does not run", pythonInstallHint(runtime.GOOS))
		return
	}
	lines := strings.Split(strings.ReplaceAll(strings.TrimSpace(string(out)), "\r\n", "\n"), "\n")
	ver := lines[0]
	if parts := versionNumbers(ver); len(parts) < 2 || parts[0] < 3 || parts[0] == 3 && parts[1] < 12 {
		r.toolIssue("Python "+ver+" is older than 3.12 (the pinned optional tools need 3.12+)", pythonInstallHint(runtime.GOOS))
	} else {
		r.line(ok, "Python "+ver+" ("+py[0]+")", "")
	}
	if len(lines) > 1 && strings.EqualFold(filepath.Clean(lines[1]), config.HomePath("venv")) {
		r.line(ok, "the tools' private venv "+config.HomePath("venv"), "")
	} else {
		r.line(warn, "no active private venv for optional tools", toolRequirementsHint())
	}
}

func versionNumbers(v string) []int {
	var out []int
	for _, p := range strings.Split(v, ".") {
		n, err := strconv.Atoi(p)
		if err != nil {
			break
		}
		out = append(out, n)
	}
	return out
}
