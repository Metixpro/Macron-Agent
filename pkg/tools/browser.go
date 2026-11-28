package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Metixpro/macron-agent/pkg/machine"
	"github.com/Metixpro/macron-agent/pkg/netclient"
	"github.com/Metixpro/macron-agent/pkg/search"
)

const (
	ToolBrowser    = "browser"
	browserTimeout = 3 * time.Minute
)

var (
	chromeDirs = []string{"/usr/local/share/ms-playwright", "/ms-playwright", "/opt/pw-browsers", "/usr/lib/ms-playwright"}

	chromeBinaries = []string{"/usr/lib/chromium/chromium"}

	webLibDirs = []string{"/usr/local/share/macron/weblibs", "/usr/share/macron/weblibs"}

	revisionRe = regexp.MustCompile(`-(\d+)[/\\]`)
	schemeRe   = regexp.MustCompile(`^([a-zA-Z][a-zA-Z0-9+.-]*):(?:[^\d]|$)`)
	renderSeq  atomic.Uint64
)

var checks sync.Map

func (r *Registry) check(argv ...string) bool {
	key := fmt.Sprint(argv)
	if v, ok := checks.Load(key); ok {
		return v.(bool)
	}
	ok := r.Machine.Check(argv...)
	checks.Store(key, ok)
	return ok
}

type browserRuntime struct {
	chrome   string
	python   string
	isolated bool
	libs     []string
	slots    chan struct{}
	agents   chan struct{}

	agentPython string
}

func isRunnableBinary(p string) bool {
	fi, err := os.Stat(p)
	if err != nil || fi.IsDir() {
		return false
	}
	if runtime.GOOS == "windows" {
		return true
	}
	return fi.Mode()&0o111 != 0
}

func findChrome() string {
	var dirs []string
	if env := os.Getenv("PLAYWRIGHT_BROWSERS_PATH"); env != "" && env != "0" {
		dirs = append(dirs, env)
	}
	if cache, err := os.UserCacheDir(); err == nil {
		dirs = append(dirs, filepath.Join(cache, "ms-playwright"))
	}
	if runtime.GOOS == "windows" {
		if la := os.Getenv("LOCALAPPDATA"); la != "" {
			dirs = append(dirs, filepath.Join(la, "ms-playwright"))
		}
	} else {
		dirs = append(dirs, chromeDirs...)
	}

	revision := func(p string) int {
		if m := revisionRe.FindStringSubmatch(p); m != nil {
			n, _ := strconv.Atoi(m[1])
			return n
		}
		return 0
	}

	var patternGroups [][]string
	switch runtime.GOOS {
	case "windows":
		patternGroups = [][]string{
			{
				filepath.Join("chromium_headless_shell-*", "chrome-win", "headless_shell.exe"),
				filepath.Join("chromium_headless_shell-*", "chrome-headless-shell-win64", "chrome-headless-shell.exe"),
			},
			{
				filepath.Join("chromium-*", "chrome-win", "chrome.exe"),
				filepath.Join("chromium-*", "chrome-win64", "chrome.exe"),
			},
		}
	case "darwin":
		patternGroups = [][]string{
			{
				"chromium_headless_shell-*/chrome-mac/headless_shell",
				"chromium_headless_shell-*/chrome-headless-shell-mac-*/chrome-headless-shell",
			},
			{
				"chromium-*/chrome-mac/Chromium.app/Contents/MacOS/Chromium",
				"chromium-*/chrome-mac-*/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing",
			},
		}
	default:
		patternGroups = [][]string{
			{"chromium_headless_shell-*/chrome-linux/headless_shell", "chromium_headless_shell-*/chrome-headless-shell-linux64/chrome-headless-shell"},
			{"chromium-*/chrome-linux/chrome", "chromium-*/chrome-linux64/chrome"},
		}
	}

	for _, patterns := range patternGroups {
		var found []string
		for _, d := range dirs {
			for _, p := range patterns {
				m, _ := filepath.Glob(filepath.Join(d, p))
				found = append(found, m...)
			}
		}
		slices.SortFunc(found, func(a, b string) int { return revision(b) - revision(a) })
		for _, f := range found {
			if isRunnableBinary(f) {
				return f
			}
		}
	}

	var fixed []string
	var names []string
	switch runtime.GOOS {
	case "windows":
		for _, base := range []string{os.Getenv("PROGRAMFILES"), os.Getenv("PROGRAMFILES(X86)"), os.Getenv("LOCALAPPDATA")} {
			if base == "" {
				continue
			}
			fixed = append(fixed,
				filepath.Join(base, "Google", "Chrome", "Application", "chrome.exe"),
				filepath.Join(base, "Microsoft", "Edge", "Application", "msedge.exe"),
				filepath.Join(base, "BraveSoftware", "Brave-Browser", "Application", "brave.exe"),
				filepath.Join(base, "Chromium", "Application", "chrome.exe"),
			)
		}
		names = []string{"chrome.exe", "msedge.exe", "chromium.exe", "brave.exe"}
	case "darwin":
		fixed = []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
			"/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
			"/Applications/Brave Browser.app/Contents/MacOS/Brave Browser",
		}
		names = []string{"chromium", "google-chrome"}
	default:
		fixed = chromeBinaries
		names = []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable", "microsoft-edge"}
	}

	for _, p := range fixed {
		if isRunnableBinary(p) {
			return p
		}
	}
	for _, name := range names {
		if p, err := exec.LookPath(name); err == nil && !strings.HasPrefix(p, "/snap/") {
			if real, err := filepath.EvalSymlinks(p); err == nil && !strings.HasPrefix(real, "/snap/") {
				return p
			}
			if runtime.GOOS == "windows" {
				return p
			}
		}
	}
	return ""
}

func (r *Registry) detectBrowser() *browserRuntime {
	chrome := findChrome()
	if chrome == "" {
		return nil
	}
	candidates := []struct {
		bin      string
		isolated bool
	}{
		{"python3", true},
		{"python3", false},
		{"python", false},
	}
	var py string
	var isolated bool
	for _, c := range candidates {
		argv := []string{c.bin, "-c", "import playwright.sync_api"}
		if c.isolated {
			argv = []string{c.bin, "-I", "-c", "import playwright.sync_api"}
		}
		if r.check(argv...) {
			py, isolated = c.bin, c.isolated
			break
		}
	}
	if py == "" {
		return nil
	}
	b := &browserRuntime{chrome: chrome, python: py, isolated: isolated, slots: make(chan struct{}, 2)}
	for _, d := range webLibDirs {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			b.libs = append(b.libs, d)
		}
	}
	return b
}

func (b *browserRuntime) webLibs() []string {
	var names []string
	for _, d := range b.libs {
		_ = filepath.WalkDir(d, func(p string, e os.DirEntry, err error) error {
			if err == nil && !e.IsDir() && (strings.HasSuffix(p, ".js") || strings.HasSuffix(p, ".mjs")) {
				names = append(names, e.Name())
			}
			return nil
		})
	}
	slices.Sort(names)
	return slices.Compact(names)
}

func (r *Registry) runBrowser(ctx context.Context, sessionID, script string, cfg map[string]any, online bool, timeout time.Duration, out any) error {
	return r.runBrowserWith(ctx, sessionID, script, cfg, browserRun{online: online, timeout: timeout}, out)
}

type browserRun struct {
	online  bool
	timeout time.Duration
	bridge  *llmBridge
	slots   chan struct{}
	python  string
}

func (r *Registry) runBrowserWith(ctx context.Context, sessionID, script string, cfg map[string]any, run browserRun, out any) error {
	b := r.browser
	slots := run.slots
	if slots == nil {
		slots = b.slots
	}
	select {
	case slots <- struct{}{}:
		defer func() { <-slots }()
	case <-ctx.Done():
		return ctx.Err()
	}
	common, err := scriptFS.ReadFile("scripts/browser_common.py")
	if err != nil {
		return err
	}
	code, err := scriptFS.ReadFile("scripts/" + script)
	if err != nil {
		return err
	}
	cfg["chrome"], cfg["libs"] = b.chrome, b.libs
	py := run.python
	useIsolated := true
	if py == "" {
		py = b.python
		useIsolated = b.isolated
		if py == "" {
			py = "python3"
		}
	}
	args := []string{py, "-c", string(common) + "\n" + string(code)}
	if useIsolated {
		args = []string{py, "-I", "-c", string(common) + "\n" + string(code)}
	}
	opts := machine.RunOptions{HostExecutable: true, Args: args, Timeout: run.timeout,
		Heavy: script == "motion.py"}

	cfg["offline"] = !(run.online || run.bridge != nil)
	if run.bridge != nil {
		dir, err := os.MkdirTemp("", "macron-llm-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		stopLLM, err := run.bridge.serve(filepath.Join(dir, "llm.sock"))
		if err != nil {
			return err
		}
		defer stopLLM()
		cfg["llm"], cfg["token"] = filepath.Join(dir, "llm.sock"), run.bridge.token
	}
	if opts.Stdin, err = json.Marshal(cfg); err != nil {
		return err
	}
	res := r.Machine.Run(ctx, sessionID, opts)
	if err := json.Unmarshal([]byte(lastLine(res.Stdout)), out); err != nil {
		return fmt.Errorf("the browser failed: %s", tail(strings.TrimSpace(res.Error+" "+res.Stderr), 600))
	}
	return nil
}

func (r *Registry) registerBrowserTools() {
	if r.browser = r.detectBrowser(); r.browser == nil {
		return
	}
	if r.Search != nil {
		r.Search.Render = r.renderPage
	}
	r.add(ToolBrowser,
		"A real live browser (persistent Chromium with revisioned semantic snapshots, smart SPA settling, cookie-banner auto-refusal, profiles, and live user Takeover). "+
			"Each snapshot returns revisioned refs (`rev:frame:idx`, e.g. `3:main:12`) with ARIA roles and states — pass those refs to `click`, `fill`, `fill_fields`, `select`, etc. "+
			"Prefer `fill_fields` for multi-field non-sensitive forms and `script` (`run_script`) for multi-step loops/filtering to save turns. "+
			"If a page shows a CAPTCHA/verification challenge or asks for sensitive credentials (password/OTP/card), call `request_takeover` and let the user handle it. "+
			"Screenshots are opt-in (`screenshot: true`) to save tokens; live preview stays active for the user. Plain reading: web_fetch.",
		map[string]any{
			"url": prop("string", "https://… or a workspace .html; empty = current page"),
			"actions": map[string]any{"type": "array", "description": `e.g. [{"action":"goto","url":"…"},{"action":"click","selector":"3:main:12"}]: ` +
				"goto{url,until?} click{selector,frame?,button?,count?,until?} click_at{x,y,button?,count?} move_at{x,y,steps?} " +
				"drag{selector|x,y, to_selector|to_x,to_y, steps?} hold{selector|x,y,ms?} " +
				"fill{selector,text,mode?,submit?,frame?} fill_fields{fields:[{selector,value,mode?}]} type{selector?,text,clear?,delay_ms?,submit?,frame?} press{key,frame?} " +
				"select{selector,value,frame?} check|uncheck|hover{selector,frame?} scroll{selector|to|y,frame?} wait{ms|selector|text,frame?} " +
				"handle_dialog{accept?,prompt_text?} request_takeover{reason} save_site_memory{host?,text} delete_site_memory{id} " +
				"tabs new_tab{url?} switch_tab{index|url|title} close_tab " +
				"screenshot{name?,full_page?,selector?} pdf{name?} extract{selector,attribute?,limit?,frame?} eval{script,frame?} back forward reload close " +
				"upload{selector,files,frame?} download{selector,name?,frame?}. Targets: revisioned ref (`3:main:12`), CSS, `text=\"…\"`, or `role=button[name=\"…\"]`.",
				"items": map[string]any{"type": "object"}},
			"script":          prop("string", "Sandboxed JS multi-step script (`run_script`) with click(), fill(), fillFields(), waitFor(), evaluate(), snapshot(), navigate(), press(), select(), scroll(), sleep(), log(). Returns summary + 1 final snapshot."),
			"until":           prop("string", "Settle mode: dom-quiet (default, SPA/skeleton-aware), load, or none"),
			"profile":         prop("string", "Browser profile ID (default: active session profile, e.g. workspace, shared, or a named profile)"),
			"mobile":          prop("boolean", "Phone viewport"),
			"full_page":       prop("boolean", "Whole page in the final screenshot"),
			"screenshot":      prop("boolean", "Save final PNG screenshot to workspace (default false to save tokens; set true when visual inspection is needed)"),
			"close_after":     prop("boolean", "Close the live browser preview when this call finishes (use when task is done)"),
			"locale":          prop("string", "e.g. fa-IR (default en-US)"),
			"new_session":     prop("boolean", "Forget cookies and the last page"),
			"timeout_seconds": prop("integer", "Page/action timeout in seconds (default 60, max 180)"),
		}, nil, r.browse)
	r.registerBrowserAgent()
	r.registerMotionTool()
}

func validateBrowserTarget(raw string) error {
	u := strings.TrimSpace(raw)
	if u == "" {
		return nil
	}
	if schemeRe.FindStringSubmatch(u) != nil || strings.HasPrefix(u, "//") {
		if _, err := netclient.ValidateURL(u); err != nil {
			return err
		}
		return nil
	}
	if _, err := netclient.ValidateURL("https://" + u); err != nil {
		return err
	}
	return nil
}

func (r *Registry) renderPage(ctx context.Context, url string) (*search.Page, error) {
	if err := validateBrowserTarget(url); err != nil {
		return nil, err
	}
	var res struct {
		Page  browserPage `json:"page"`
		Error string      `json:"error"`
	}
	sess := fmt.Sprintf("%s-%d", renderSession, renderSeq.Add(1))
	defer func() { _ = r.Machine.RemoveAll(sess, ".browser") }()
	cfg := map[string]any{"url": url, "screenshot": false, "new_session": true, "max_text": 60000, "nav_timeout_ms": 35000}
	if err := r.runBrowser(ctx, sess, "browser.py", cfg, true, browserTimeout, &res); err != nil {
		return nil, err
	}
	if res.Error != "" {
		return nil, errors.New(res.Error)
	}
	return &search.Page{URL: res.Page.URL, Title: res.Page.Title, Text: res.Page.Text}, nil
}

const renderSession = "macron-render"

type browserPageItem struct {
	Ref     string   `json:"ref"`
	Tag     string   `json:"tag"`
	Role    string   `json:"role"`
	Sel     string   `json:"sel"`
	Text    string   `json:"text"`
	Type    string   `json:"type"`
	Value   string   `json:"value"`
	States  []string `json:"states"`
	Href    string   `json:"href"`
	Options []string `json:"options"`
	Frame   int      `json:"frame"`
}

type browserPage struct {
	Rev       int               `json:"rev"`
	Title     string            `json:"title"`
	URL       string            `json:"url"`
	Text      string            `json:"text"`
	More      int               `json:"more"`
	Challenge *BrowserChallenge `json:"challenge"`
	Dialog    *BrowserDialog    `json:"dialog"`
	Items     []browserPageItem `json:"items"`
}

func (r *Registry) HasBrowser() bool {
	return r != nil && r.browser != nil
}

func (r *Registry) Browse(ctx context.Context, sessionID string, args map[string]any) (string, error) {
	if r == nil || r.browser == nil {
		return "", errors.New("browser runtime is not available")
	}
	return r.browse(ctx, sessionID, args)
}

func (r *Registry) browse(ctx context.Context, sessionID string, rawArgs map[string]any) (string, error) {
	args, _ := repairBrowserToolArgs(rawArgs)

	timeoutSec := intArg(args, "timeout_seconds", 60, 5, 180)
	navTimeoutMS := min(timeoutSec*1000, 90000)
	actionTimeoutMS := min(max(timeoutSec*500, 5000), 30000)

	// Opt-in screenshots by default when using live daemon (saves tokens!).
	wantShot := strArg(args, "screenshot") == "true"

	cfg := map[string]any{
		"url":            strArg(args, "url"),
		"until":          normalizeWaitUntil(strArg(args, "until")),
		"profile":        strArg(args, "profile"),
		"mobile":         strArg(args, "mobile") == "true",
		"full_page":      strArg(args, "full_page") == "true",
		"screenshot":     wantShot,
		"close_after":    strArg(args, "close_after") == "true",
		"locale":         strArg(args, "locale"),
		"new_session":    strArg(args, "new_session") == "true",
		"timeout_ms":     actionTimeoutMS,
		"nav_timeout_ms": navTimeoutMS,
	}

	if raw, ok := args["actions"].(string); ok && strings.TrimSpace(raw) != "" {
		var acts []any
		if json.Unmarshal([]byte(raw), &acts) != nil {
			return "error: actions must be a list of objects", nil
		}
		// Re-run repair on parsed JSON action list.
		repaired := make([]any, 0, len(acts))
		for _, it := range acts {
			if m, ok := it.(map[string]any); ok {
				rm, _ := repairBrowserAction(m)
				repaired = append(repaired, rm)
			} else {
				repaired = append(repaired, it)
			}
		}
		args["actions"] = repaired
	}

	// Check for top-level `script` or a `run_script` action.
	scriptCode := strings.TrimSpace(strArg(args, "script"))
	if acts, ok := args["actions"].([]any); ok {
		if len(acts) > 40 {
			return "error: at most 40 actions per call", nil
		}
		norm := normalizeActions(acts)
		// Intercept Go-side site memory actions and run_script cleanly.
		var filtered []any
		var memLogs []string
		for _, item := range norm {
			m, ok := item.(map[string]any)
			if !ok {
				filtered = append(filtered, item)
				continue
			}
			verb := strings.ToLower(strings.TrimSpace(fmt.Sprint(m["action"])))
			switch verb {
			case "save_site_memory":
				host := strings.TrimSpace(fmt.Sprint(m["host"]))
				if host == "" || host == "<nil>" {
					host = cfg["url"].(string)
					if host == "" {
						host = r.BrowserState(sessionID).URL
					}
				}
				txt := strings.TrimSpace(fmt.Sprint(m["text"]))
				if txt == "<nil>" {
					txt = ""
				}
				entry, err := r.AddSiteMemory(host, txt)
				if err != nil {
					memLogs = append(memLogs, "✗ save_site_memory → "+err.Error())
				} else {
					memLogs = append(memLogs, fmt.Sprintf("✓ saved site memory #%d for %s", entry.ID, entry.Host))
				}
			case "delete_site_memory":
				idVal := 0
				switch x := m["id"].(type) {
				case float64:
					idVal = int(x)
				case int:
					idVal = x
				case string:
					idVal, _ = strconv.Atoi(x)
				}
				if err := r.DeleteSiteMemory(idVal); err != nil {
					memLogs = append(memLogs, "✗ delete_site_memory → "+err.Error())
				} else {
					memLogs = append(memLogs, fmt.Sprintf("✓ deleted site memory #%d", idVal))
				}
			case "run_script":
				if sc, ok := m["script"].(string); ok && strings.TrimSpace(sc) != "" {
					scriptCode = strings.TrimSpace(sc)
				}
			default:
				filtered = append(filtered, m)
			}
		}
		if len(memLogs) > 0 && len(filtered) == 0 && scriptCode == "" && cfg["url"].(string) == "" {
			return strings.Join(memLogs, "\n"), nil
		}
		cfg["actions"] = filtered
	}

	if err := validateBrowserTarget(cfg["url"].(string)); err != nil {
		return "error: " + err.Error(), nil
	}
	acts, _ := cfg["actions"].([]any)
	for _, a := range acts {
		if m, ok := a.(map[string]any); ok {
			if kind, _ := m["action"].(string); strings.EqualFold(kind, "goto") || strings.EqualFold(kind, "new_tab") {
				if u, _ := m["url"].(string); u != "" {
					if err := validateBrowserTarget(u); err != nil {
						return "error: " + err.Error(), nil
					}
				}
			}
		}
	}
	if refused := r.gateBrowser(ctx, sessionID, cfg["url"].(string), acts); refused != "" {
		return refused, nil
	}

	// Sandboxed JS run_script execution path.
	if scriptCode != "" {
		if cfg["url"].(string) != "" {
			if _, err := r.BrowserNav(ctx, sessionID, "goto", map[string]any{"url": cfg["url"].(string)}); err != nil {
				return "error: " + err.Error(), nil
			}
		}
		if prof := cfg["profile"].(string); prof != "" {
			_, _ = r.SetSessionBrowserProfile(sessionID, prof)
		}
		scriptTimeout := time.Duration(timeoutSec) * time.Second
		sres, err := r.BrowserRunScript(ctx, sessionID, scriptCode, scriptTimeout)
		if err != nil {
			return "error: " + err.Error(), nil
		}
		return r.formatScriptOutput(sessionID, sres), nil
	}

	var res browserToolOutput
	runTimeout := time.Duration(timeoutSec)*time.Second + 15*time.Second
	if runTimeout > browserTimeout {
		runTimeout = browserTimeout
	}
	res, err := r.browseViaDaemon(ctx, sessionID, cfg, runTimeout)
	if err != nil {
		if errors.Is(err, errDaemonUnsupported) {
			if err2 := r.runBrowser(ctx, sessionID, "browser.py", cfg, true, runTimeout, &res); err2 != nil {
				return "error: " + err2.Error(), nil
			}
		} else {
			return "error: " + err.Error(), nil
		}
	}
	var siteNotes []SiteMemory
	if res.Page.URL != "" {
		siteNotes = r.siteMem().claimForTurn(browserSessionKey(sessionID), res.Page.URL)
	}
	return formatBrowserToolOutput(cfg, &res, siteNotes), nil
}

func (r *Registry) formatScriptOutput(sessionID string, sres browserScriptResult) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Script completed (%d steps).\n", sres.Steps)
	if sres.Interrupted != "" {
		fmt.Fprintf(&sb, "Interrupted: %s\n", sres.Interrupted)
	}
	if len(sres.Logs) > 0 {
		sb.WriteString("Logs:\n")
		for _, ln := range sres.Logs {
			sb.WriteString("  " + ln + "\n")
		}
	}
	if sres.Value != nil {
		if raw, err := json.MarshalIndent(sres.Value, "", "  "); err == nil {
			sb.WriteString("Result:\n" + string(raw) + "\n")
		}
	}
	if len(sres.Page) > 0 {
		raw, err := json.Marshal(sres.Page)
		if err == nil {
			var page browserPage
			if json.Unmarshal(raw, &page) == nil {
				out := browserToolOutput{Page: page}
				var siteNotes []SiteMemory
				if page.URL != "" {
					siteNotes = r.siteMem().claimForTurn(browserSessionKey(sessionID), page.URL)
				}
				sb.WriteString("\n" + formatBrowserToolOutput(map[string]any{}, &out, siteNotes))
			}
		}
	}
	return strings.TrimSpace(sb.String())
}

type browserToolOutput struct {
	Page      browserPage       `json:"page"`
	Error     string            `json:"error"`
	Files     []string          `json:"files"`
	Errors    []string          `json:"errors"`
	Dialogs   []string          `json:"dialogs"`
	Challenge *BrowserChallenge `json:"challenge"`
	Log       []struct {
		Step   int    `json:"step"`
		What   string `json:"what"`
		OK     bool   `json:"ok"`
		Detail string `json:"detail"`
	} `json:"log"`
}

func formatBrowserToolOutput(cfg map[string]any, res *browserToolOutput, siteNotes []SiteMemory) string {
	if res.Error != "" {
		return "error: " + res.Error
	}
	var sb strings.Builder
	if res.Page.URL == "" {
		sb.WriteString("No page is open.")
		if cfg["url"] == "" {
			sb.WriteString(" Give a url.")
		}
		sb.WriteString("\n")
	} else {
		if res.Page.Rev > 0 {
			fmt.Fprintf(&sb, "Page [rev:%d]: %s — %s\n", res.Page.Rev, res.Page.Title, res.Page.URL)
		} else {
			fmt.Fprintf(&sb, "Page: %s — %s\n", res.Page.Title, res.Page.URL)
		}
	}

	ch := res.Challenge
	if ch == nil {
		ch = res.Page.Challenge
	}
	if ch != nil && ch.Status != "" && ch.Status != "none" {
		switch ch.Status {
		case "needs-user":
			fmt.Fprintf(&sb, "⚠ Challenge [needs-user]: %s — User Takeover has been triggered. Wait for the user to complete verification before continuing.\n", ch.Reason)
		case "blocked":
			fmt.Fprintf(&sb, "⚠ Challenge [blocked]: %s — Access blocked by site security policy.\n", ch.Reason)
		default:
			fmt.Fprintf(&sb, "⚠ Challenge [%s]: %s\n", ch.Status, ch.Reason)
		}
	}

	if res.Page.Dialog != nil {
		fmt.Fprintf(&sb, "Open JS Dialog [%s]: %q (respond with handle_dialog or let the user answer in Live View)\n", res.Page.Dialog.Type, res.Page.Dialog.Message)
	}

	if len(siteNotes) > 0 {
		sb.WriteString("\nSite Memories (auto-loaded for this domain):\n")
		for _, m := range siteNotes {
			fmt.Fprintf(&sb, "- [%s] %s\n", m.Host, m.Text)
		}
	}

	for _, l := range res.Log {
		mark := "✓"
		if !l.OK {
			mark = "✗"
		}
		fmt.Fprintf(&sb, "%s %d. %s", mark, l.Step, l.What)
		if l.Detail != "" {
			sb.WriteString(" → " + tail(l.Detail, 1500))
		}
		sb.WriteString("\n")
	}
	if len(res.Files) > 0 {
		fmt.Fprintf(&sb, "Files: %s (look with view_image, deliver with send_file)\n", strings.Join(res.Files, ", "))
	}
	if len(res.Dialogs) > 0 {
		sb.WriteString("Dialogs: " + strings.Join(res.Dialogs, " | ") + "\n")
	}
	if len(res.Errors) > 0 {
		sb.WriteString("Page script errors: " + strings.Join(res.Errors, " | ") + "\n")
	}
	if len(res.Page.Items) > 0 {
		sb.WriteString("\nInteractive Elements (ref ← role \"label\" [states]):\n")
		for _, it := range res.Page.Items {
			id := it.Ref
			if id == "" {
				id = it.Sel
			}
			desc := it.Role
			if desc == "" {
				desc = it.Tag
			}
			if it.Type != "" && it.Type != "text" {
				desc += "[" + it.Type + "]"
			}
			if it.Text != "" {
				desc += " " + strconv.Quote(it.Text)
			}
			if it.Value != "" {
				desc += " value=" + strconv.Quote(it.Value)
			}
			if len(it.States) > 0 {
				desc += " (" + strings.Join(it.States, ", ") + ")"
			}
			if len(it.Options) > 0 {
				desc += " options: " + strings.Join(it.Options, " / ")
			}
			if it.Frame > 0 {
				desc += fmt.Sprintf(" [frame:%d]", it.Frame)
			}
			fmt.Fprintf(&sb, "- %s ← %s\n", id, desc)
		}
	}
	if res.Page.Text != "" {
		sb.WriteString("\nText:\n" + res.Page.Text + "\n")
		if res.Page.More > 0 {
			fmt.Fprintf(&sb, "… %d more characters (scroll, or extract a selector)\n", res.Page.More)
		}
	}
	return strings.TrimSpace(sb.String())
}

var browserActions = map[string]string{
	"goto": "url", "new_tab": "url", "click": "selector", "click_at": "x", "move_at": "x",
	"drag": "selector", "drag_at": "x", "hold": "selector",
	"fill": "text", "fill_fields": "fields", "type": "text", "press": "key", "select": "value",
	"check": "selector", "uncheck": "selector", "hover": "selector", "scroll": "to",
	"wait": "ms", "tabs": "", "switch_tab": "index", "close_tab": "",
	"screenshot": "name", "pdf": "name", "extract": "selector", "eval": "script",
	"run_script": "script", "handle_dialog": "accept", "request_takeover": "reason",
	"save_site_memory": "text", "delete_site_memory": "id",
	"back": "", "forward": "", "reload": "", "close": "", "upload": "selector", "download": "selector",
}

func normalizeActions(acts []any) []any {
	out := make([]any, 0, len(acts))
	for _, a := range acts {
		switch v := a.(type) {
		case string:
			name, rest, _ := strings.Cut(strings.TrimSpace(v), " ")
			name = strings.ToLower(name)
			field, ok := browserActions[name]
			if !ok {
				out = append(out, a)
				continue
			}
			act := map[string]any{"action": name}
			if rest = strings.TrimSpace(rest); rest != "" && field != "" {
				act[field] = rest
			}
			out = append(out, act)
		case map[string]any:
			if _, ok := v["action"]; ok {
				out = append(out, v)
				continue
			}
			act := map[string]any{}
			for k, val := range v {
				act[k] = val
			}
		named:
			for name, field := range browserActions {
				val, ok := act[name]
				if !ok {
					continue
				}
				delete(act, name)
				act["action"] = name
				switch x := val.(type) {
				case map[string]any:
					for k, y := range x {
						act[k] = y
					}
				case string, float64:
					if field != "" {
						act[field] = x
					}
				}
				break named
			}
			if _, ok := act["action"]; !ok {
				switch {
				case act["url"] != nil:
					act["action"] = "goto"
				case act["script"] != nil:
					act["action"] = "eval"
				case act["key"] != nil:
					act["action"] = "press"
				case act["fields"] != nil:
					act["action"] = "fill_fields"
				case act["selector"] != nil && act["text"] != nil:
					act["action"] = "fill"
				case act["selector"] != nil:
					act["action"] = "click"
				}
			}
			out = append(out, act)
		default:
			out = append(out, a)
		}
	}
	return out
}
