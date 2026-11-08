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

// tools the agent's work leans on: name, why, how to get it.
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

	// ---- files
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

	// ---- Telegram
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

	// ---- database
	dbPath := getEnvDefault(env, "MACRON_DB", filepath.Join(home, config.FileDB))
	if _, err := os.Stat(dbPath); err != nil {
		r.line(warn, "no database yet ("+dbPath+")", "it is created the first time the agent starts")
	} else {
		reportDatabase(ctx, r, dbPath, env)
	}

	// ---- the model
	url, key, model := getEnv(env, keyAPIURL), getEnv(env, keyAPIKey), getEnv(env, keyModel)
	switch {
	case url == "":
		r.line(warn, "no model in the settings", "fine if you added a provider in Telegram (/admin → providers); otherwise run: macron setup")
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

	// ---- this machine
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

	// ---- the service
	if serviceSupported() {
		u, _ := currentUnit()
		ctl := systemctl(u)
		if runtime.GOOS == "linux" {
			if err := exec.Command(ctl[0], append(ctl[1:], "is-active", "--quiet", unitName)...).Run(); err != nil {
				r.line(warn, "the agent is not running as a service", "run: macron service install")
			} else {
				r.line(ok, "the agent runs as a service", "")
			}
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

// reportMoA checks the multi-model presets: each slot's provider resolves,
// the aggregator has a key, and no aggregator is itself a mixture.
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

// checkToolDependencies reports package imports, not merely an installed Python.
func checkToolDependencies(r *report) {
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
