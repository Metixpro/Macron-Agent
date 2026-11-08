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
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Metixpro/macron-agent/pkg/machine"
	"github.com/Metixpro/macron-agent/pkg/search"
)

// A headless Chromium driven by Playwright, here, with the network of the
// machine it runs on.

const (
	ToolBrowser    = "browser"
	browserTimeout = 3 * time.Minute
)

var (
	// chromeDirs are where Playwright keeps its browsers.
	chromeDirs = []string{"/usr/local/share/ms-playwright", "/ms-playwright", "/opt/pw-browsers", "/usr/lib/ms-playwright"}
	// chromeBinaries are Chromiums installed by the distribution: Debian's
	// chromium package (install.sh's fallback when Playwright cannot download).
	chromeBinaries = []string{"/usr/lib/chromium/chromium"}
	// webLibDirs hold JavaScript libraries served offline to rendered pages.
	webLibDirs = []string{"/usr/local/share/waze/weblibs", "/usr/share/waze/weblibs"}

	revisionRe = regexp.MustCompile(`-(\d+)/`)
	schemeRe   = regexp.MustCompile(`^([a-zA-Z][a-zA-Z0-9+.-]*):(?:[^\d]|$)`)
)

var checks sync.Map

// check runs a probe once per kind of machine (tests build many registries).
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
	chrome string
	libs   []string
	slots  chan struct{} // browsers running at once
	agents chan struct{} // browser agents running at once (registerBrowserAgent)
	// agentPython runs browser-use ("" when it is not installed).
	agentPython string
}

// findChrome picks the newest Chromium the machine can see; the lighter
// headless shell first.
func findChrome() string {
	dirs := append([]string(nil), chromeDirs...)
	if cache, err := os.UserCacheDir(); err == nil {
		dirs = append(dirs, filepath.Join(cache, "ms-playwright"))
	}
	if env := os.Getenv("PLAYWRIGHT_BROWSERS_PATH"); env != "" && env != "0" {
		dirs = append([]string{env}, dirs...)
	}
	revision := func(p string) int {
		if m := revisionRe.FindStringSubmatch(p); m != nil {
			n, _ := strconv.Atoi(m[1])
			return n
		}
		return 0
	}
	for _, patterns := range [][]string{
		{"chromium_headless_shell-*/chrome-linux/headless_shell", "chromium_headless_shell-*/chrome-headless-shell-linux64/chrome-headless-shell"},
		{"chromium-*/chrome-linux/chrome", "chromium-*/chrome-linux64/chrome"},
	} {
		var found []string
		for _, d := range dirs {
			for _, p := range patterns {
				m, _ := filepath.Glob(filepath.Join(d, p))
				found = append(found, m...)
			}
		}
		slices.SortFunc(found, func(a, b string) int { return revision(b) - revision(a) })
		for _, f := range found {
			return f
		}
	}
	for _, p := range chromeBinaries {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return p
		}
	}
	for _, name := range []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable"} {
		if p, err := exec.LookPath(name); err == nil && !strings.HasPrefix(p, "/snap/") {
			if real, err := filepath.EvalSymlinks(p); err == nil && !strings.HasPrefix(real, "/snap/") {
				return p
			}
		}
	}
	return ""
}

// detectBrowser finds Chromium and checks that Playwright imports here.
func (r *Registry) detectBrowser() *browserRuntime {
	chrome := findChrome()
	if chrome == "" {
		return nil
	}
	if !r.check("python3", "-I", "-c", "import playwright.sync_api") {
		return nil
	}
	b := &browserRuntime{chrome: chrome, slots: make(chan struct{}, 2)}
	for _, d := range webLibDirs {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() && true {
			b.libs = append(b.libs, d)
		}
	}
	return b
}

// webLibs lists the offline libraries by file name.
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

// runBrowser runs an embedded script with Chromium (offline when asked). The
// script's JSON answer is decoded into out.
func (r *Registry) runBrowser(ctx context.Context, sessionID, script string, cfg map[string]any, online bool, timeout time.Duration, out any) error {
	return r.runBrowserWith(ctx, sessionID, script, cfg, browserRun{online: online, timeout: timeout}, out)
}

// browserRun is how a browser script runs.
type browserRun struct {
	online  bool
	timeout time.Duration
	bridge  *llmBridge    // the model, for browser_agent.py: served next to the proxy
	slots   chan struct{} // nil: the browsers' own
	python  string        // "" = python3
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
	if py == "" {
		py = "python3"
	}
	opts := machine.RunOptions{HostExecutable: true, Args: []string{py, "-I", "-c", string(common) + "\n" + string(code)}, Timeout: run.timeout,
		Heavy: script == "motion.py"} // a frame-by-frame render keeps the CPU busy
	// The browser reaches the network directly, as any program on this machine
	// does; a page that only renders local files is kept offline.
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
		r.Search.Render = r.renderPage // the last way to read a page that blocks bots
	}
	r.add(ToolBrowser,
		"A real browser (headless Chromium): open, click, fill forms, log in, scroll, read, run JS, screenshot, PDF, upload, "+
			"download; logins stay. One call runs a list of actions and returns the page text, clickable elements with selectors "+
			"and a screenshot. Plain reading: web_fetch.",
		map[string]any{
			"url": prop("string", "https://… or a workspace .html; empty = the last page"),
			"actions": map[string]any{"type": "array", "description": `e.g. [{"action":"goto","url":"…"},{"action":"click","selector":"…"}]: ` +
				"goto{url} click{selector} fill{selector,text} type{selector?,text} " +
				"press{key} select{selector,value} check|uncheck|hover{selector} scroll{selector|to|y} wait{ms|selector|text} " +
				"screenshot{name?,full_page?,selector?} pdf{name?} extract{selector,attribute?,limit?} eval{script} back forward reload " +
				"upload{selector,files} download{selector,name?}. Selectors: CSS, text=\"…\", role=button[name=\"…\"], or ones listed before",
				"items": map[string]any{"type": "object"}},
			"mobile":      prop("boolean", "Phone viewport"),
			"full_page":   prop("boolean", "Whole page in the final screenshot"),
			"screenshot":  prop("boolean", "Final screenshot (default true)"),
			"locale":      prop("string", "e.g. fa-IR (default en-US)"),
			"new_session": prop("boolean", "Forget cookies and the last page"),
		}, nil, r.browse)
	r.registerBrowserAgent()
	r.registerMotionTool()
}

// renderPage reads a page with the headless browser, for web_fetch when the
// site blocks plain requests. It runs in a session of its own, with a fresh
// profile each time.
func (r *Registry) renderPage(ctx context.Context, url string) (*search.Page, error) {
	var res struct {
		Page  browserPage `json:"page"`
		Error string      `json:"error"`
	}
	cfg := map[string]any{"url": url, "screenshot": false, "new_session": true, "max_text": 60000}
	if err := r.runBrowser(ctx, renderSession, "browser.py", cfg, true, browserTimeout, &res); err != nil {
		return nil, err
	}
	if res.Error != "" {
		return nil, errors.New(res.Error)
	}
	return &search.Page{URL: res.Page.URL, Title: res.Page.Title, Text: res.Page.Text}, nil
}

// renderSession is the machine session of renderPage.
const renderSession = "waze-render"

type browserPage struct {
	Title string `json:"title"`
	URL   string `json:"url"`
	Text  string `json:"text"`
	More  int    `json:"more"`
	Items []struct {
		Tag     string   `json:"tag"`
		Sel     string   `json:"sel"`
		Text    string   `json:"text"`
		Type    string   `json:"type"`
		Href    string   `json:"href"`
		Options []string `json:"options"`
	} `json:"items"`
}

func (r *Registry) browse(ctx context.Context, sessionID string, args map[string]any) (string, error) {
	cfg := map[string]any{"url": strArg(args, "url"), "mobile": strArg(args, "mobile") == "true", "full_page": strArg(args, "full_page") == "true",
		"screenshot": strArg(args, "screenshot") != "false", "locale": strArg(args, "locale"), "new_session": strArg(args, "new_session") == "true"}
	if raw, ok := args["actions"].(string); ok && strings.TrimSpace(raw) != "" {
		var acts []any
		if json.Unmarshal([]byte(raw), &acts) != nil {
			return "error: actions must be a list of objects", nil
		}
		args["actions"] = acts
	}
	if acts, ok := args["actions"].([]any); ok {
		if len(acts) > 40 {
			return "error: at most 40 actions per call", nil
		}
		cfg["actions"] = normalizeActions(acts)
	}
	if m := schemeRe.FindStringSubmatch(cfg["url"].(string)); m != nil && !strings.EqualFold(m[1], "http") && !strings.EqualFold(m[1], "https") {
		return "error: only http(s) pages or workspace files can be opened", nil
	}
	var res struct {
		Page    browserPage `json:"page"`
		Error   string      `json:"error"`
		Files   []string    `json:"files"`
		Errors  []string    `json:"errors"`
		Dialogs []string    `json:"dialogs"`
		Log     []struct {
			Step   int    `json:"step"`
			What   string `json:"what"`
			OK     bool   `json:"ok"`
			Detail string `json:"detail"`
		} `json:"log"`
	}
	if err := r.runBrowser(ctx, sessionID, "browser.py", cfg, true, browserTimeout, &res); err != nil {
		return "error: " + err.Error(), nil
	}
	if res.Error != "" {
		return "error: " + res.Error, nil
	}
	var sb strings.Builder
	if res.Page.URL == "" {
		sb.WriteString("No page is open.")
		if cfg["url"] == "" {
			sb.WriteString(" Give a url.")
		}
		sb.WriteString("\n")
	} else {
		fmt.Fprintf(&sb, "Page: %s — %s\n", res.Page.Title, res.Page.URL)
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
		sb.WriteString("Dialogs (accepted): " + strings.Join(res.Dialogs, " | ") + "\n")
	}
	if len(res.Errors) > 0 {
		sb.WriteString("Page script errors: " + strings.Join(res.Errors, " | ") + "\n")
	}
	if len(res.Page.Items) > 0 {
		sb.WriteString("\nElements (selector ← what):\n")
		for _, it := range res.Page.Items {
			desc := it.Tag
			if it.Type != "" {
				desc += "[" + it.Type + "]"
			}
			if it.Text != "" {
				desc += " " + strconv.Quote(it.Text)
			}
			if len(it.Options) > 0 {
				desc += " options: " + strings.Join(it.Options, " / ")
			}
			fmt.Fprintf(&sb, "- %s ← %s\n", it.Sel, desc)
		}
	}
	if res.Page.Text != "" {
		sb.WriteString("\nText:\n" + res.Page.Text + "\n")
		if res.Page.More > 0 {
			fmt.Fprintf(&sb, "… %d more characters (scroll, or extract a selector)\n", res.Page.More)
		}
	}
	return strings.TrimSpace(sb.String()), nil
}

// browserActions are the actions browser.py knows, with the field a bare
// value of each goes to.
var browserActions = map[string]string{"goto": "url", "click": "selector", "fill": "text", "type": "text", "press": "key",
	"select": "value", "check": "selector", "uncheck": "selector", "hover": "selector", "scroll": "to", "wait": "ms",
	"screenshot": "name", "pdf": "name", "extract": "selector", "eval": "script", "back": "", "forward": "", "reload": "",
	"upload": "selector", "download": "selector"}

// normalizeActions reads the shapes models write actions in, besides
// {"action": "goto", "url": …}: {"goto": "https://…"}, {"click": {"selector": …}},
// {"url": …} alone (a goto), and "goto https://…" as text.
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
