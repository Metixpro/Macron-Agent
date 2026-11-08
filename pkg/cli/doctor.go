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
	"syscall"
	"time"

	"github.com/Metixpro/macron-agent/pkg/app"
	"github.com/Metixpro/macron-agent/pkg/config"
	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/moa"
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
	io    *IO
	fails int
	warns int
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

var doctorTools = []struct {
	name, why, apt string
	need           bool
}{
	{"python3", "scripts, data, documents", "python3 python3-pip", true},
	{"git", "projects and updates", "git", true},
	{"curl", "downloads", "curl", true},
	{"ssh", "other servers (the agent connects to them from the chat)", "openssh-client", false},
	{"sshpass", "servers that give a password instead of a key", "sshpass", false},
	{"rsync", "copying files to and from servers", "rsync", false},
	{"gh", "GitHub (pull requests, issues; git alone does the rest)", "gh", false},
	{"ffmpeg", "audio and video", "ffmpeg", false},
	{"node", "web projects, MCP servers", "nodejs npm", false},
	{"yt-dlp", "YouTube and social media downloads", "yt-dlp (pip install -U yt-dlp)", false},
	{"pdftotext", "reading PDFs", "poppler-utils", false},
}

func doctor(io *IO, args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(io.Err)
	toolsOnly := fs.Bool("tools-only", false, "check local tool dependencies without Telegram/model settings")
	offline := fs.Bool("offline", false, "do not call Telegram or the model")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	r := &report{io: io}
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
	} else if fi.Mode().Perm()&0o077 != 0 {
		r.line(warn, "the home folder can be read by other users on this machine", "chmod 700 "+home)
	} else {
		r.line(ok, "home folder "+home, "")
	}
	envPath := config.EnvFilePath()
	env, err := loadEnvFile(envPath)
	switch {
	case err != nil || len(env.lines) == 0:
		r.line(fail, "no settings in "+envPath, "run: macron setup")
		return 1
	default:
		if fi, err := os.Stat(envPath); err == nil && fi.Mode().Perm()&0o077 != 0 {
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

	token := getEnv(env, keyToken)
	owners := getEnv(env, keyOwners)
	if token == "" {
		r.line(fail, "no bot token", "run: macron setup")
	} else if !*offline {
		cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		me, err := telegram.NewBotWithBase(token, telegramAPI).GetMe(cctx)
		cancel()
		if err != nil {
			r.line(fail, "Telegram refuses the bot token: "+shorten(err), "get a new token from @BotFather and run: macron config set TELEGRAM_BOT_TOKEN <token>")
		} else {
			r.line(ok, "Telegram: the bot is @"+me.Username, "")
		}
	}
	if _, err := config.ParseIDs(owners); err != nil || owners == "" {
		r.line(fail, "no valid owner id", "run: macron config set MACRON_OWNERS <your numeric Telegram id>")
	} else {
		r.line(ok, "owner(s): "+owners, "")
	}

	dbPath := getEnvDefault(env, "MACRON_DB", filepath.Join(home, config.FileDB))
	if _, err := os.Stat(dbPath); err != nil {
		r.line(warn, "no database yet ("+dbPath+")", "it is created the first time the agent starts")
	} else {
		reportDatabase(ctx, r, dbPath, env)
	}

	url, key, model := getEnv(env, keyAPIURL), getEnv(env, keyAPIKey), getEnv(env, keyModel)
	switch {
	case url == "":
		r.line(warn, "no model in the settings", "fine if you added a provider in Telegram (/settings → providers); otherwise run: macron setup")
	case !*offline:
		base, api := llm.CleanBaseURL(url)
		c := llm.NewClient(base, key, model)
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
	if os.Geteuid() == 0 {
		r.line(warn, "the agent runs as root: it can do anything on this machine", "that is what a personal server is for; to limit it, run it as a normal user")
	} else {
		r.line(ok, "the agent runs commands as "+who+", with that user's full rights", "")
	}
	checkToolDependencies(r)
	var st syscall.Statfs_t
	if err := syscall.Statfs(home, &st); err == nil {
		if free := uint64(st.Bavail) * uint64(st.Bsize); free < 1<<30 {
			r.line(warn, "less than 1 GB of disk space is free", "files and downloads need room")
		}
	}

	if serviceSupported() {
		u, _ := currentUnit()
		ctl := systemctl(u)
		if runtime.GOOS == "linux" {
			if err := exec.Command(ctl[0], append(ctl[1:], "is-active", "--quiet", unitName)...).Run(); err != nil {
				r.line(warn, "the agent is not running as a service", "run: macron service install")
			} else {
				r.line(ok, "the agent runs as a service", "")
			}
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

func getEnvDefault(env *envFile, key, def string) string {
	if v := getEnv(env, key); v != "" {
		return v
	}
	return def
}

func installHint(pkg string) string {
	if runtime.GOOS == "darwin" {
		return "brew install " + strings.Fields(pkg)[0]
	}
	return "sudo apt install " + pkg
}

func reportDatabase(ctx context.Context, r *report, dbPath string, env *envFile) {
	st, err := store.Open(ctx, dbPath, store.Options{SecretKey: "doctor"})
	if err != nil {
		r.line(fail, "the database does not open: "+shorten(err), "restore it from a backup: macron backup makes them")
		return
	}
	defer st.Close()
	var verdict string
	if err := st.DB().QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&verdict); err != nil || verdict != "ok" {
		r.line(fail, "the database is damaged ("+verdict+")", "restore it from a backup")
		return
	}
	var turns int
	_ = st.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM chat_archive`).Scan(&turns)
	size, _ := st.DatabaseSize(ctx)
	r.line(ok, "database "+dbPath+" ("+humanBytes(size)+", "+itoa(turns)+" messages kept and searchable)", "")
	reportMoA(ctx, r, st)
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

func checkToolDependencies(r *report) {
	checkPython(r)
	for _, t := range doctorTools {
		if _, err := exec.LookPath(t.name); err == nil {
			r.line(ok, t.name+" ("+t.why+")", "")
			continue
		}
		l := warn
		if t.need {
			l = fail
		}
		r.line(l, t.name+" is missing ("+t.why+")", installHint(t.apt))
	}
	for _, dep := range []struct{ module, packageName string }{
		{"qrcode", "qrcode"}, {"matplotlib", "matplotlib"}, {"reportlab", "reportlab"},
		{"pypdf", "pypdf"}, {"pymupdf", "PyMuPDF"}, {"arabic_reshaper", "arabic-reshaper"},
		{"bidi.algorithm", "python-bidi"}, {"playwright.sync_api", "playwright"},
	} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := exec.CommandContext(ctx, "python3", "-I", "-c", "import importlib; importlib.import_module("+strconv.Quote(dep.module)+")").Run()
		cancel()
		if err != nil {
			r.line(fail, "Python package "+dep.packageName+" is missing or cannot import", "install requirements-tools.lock into the private venv")
		} else {
			r.line(ok, "Python package "+dep.packageName, "")
		}
	}
}

func checkPython(r *report) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "python3", "-I", "-c", "import sys; print('%d.%d' % sys.version_info[:2]); print(sys.prefix)").Output()
	if err != nil {
		r.line(fail, "python3 does not run", installHint("python3 python3-venv"))
		return
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	ver, prefix := lines[0], ""
	if len(lines) > 1 {
		prefix = lines[1]
	}
	if parts := versionNumbers(ver); len(parts) < 2 || parts[0] < 3 || parts[0] == 3 && parts[1] < 12 {
		r.line(fail, "Python "+ver+" is older than 3.12 (the pinned tools need 3.12+)", "install Python 3.12 (deadsnakes, uv python install 3.12, or brew), then run install.sh again")
	} else {
		r.line(ok, "Python "+ver, "")
	}
	venv := filepath.Join(config.Home(), "venv")
	switch {
	case strings.HasPrefix(prefix, venv):
		r.line(ok, "the tools' private venv "+venv, "")
	case fileExists(filepath.Join(venv, "bin", "python")):
		r.line(warn, "the private venv exists but python3 is not it", "it is used when the agent starts; nothing to fix if this is a manual check")
	default:
		r.line(warn, "no private venv for the tools ("+venv+")", "run install.sh again (it creates it from requirements-tools.lock)")
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
