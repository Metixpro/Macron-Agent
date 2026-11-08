package tools

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/Metixpro/macron-agent/pkg/llm"
)

const (
	ToolBrowserAgent  = "browser_agent"
	agentStepsDefault = 20
	agentStepsMax     = 50
	agentRunsAtOnce   = 2
	bridgeBodyMax     = 24 << 20
	bridgeImagesKept  = 4
	bridgeImageMax    = 6 << 20
	bridgeTokensMax   = 1_500_000
)

var agentPythons = []string{"/usr/local/share/waze/browser-use/bin/python3", "/usr/share/waze/browser-use/bin/python3", "python3"}

func (r *Registry) findAgentPython() string {
	pys := agentPythons
	if env := os.Getenv("WAZE_BROWSER_USE_PYTHON"); env != "" {
		pys = append([]string{env}, pys...)
	}
	for _, py := range pys {
		if r.check(py, "-I", "-c", `import os,tempfile
with tempfile.TemporaryDirectory() as config:
 os.environ.update({"BROWSER_USE_CONFIG_DIR":config,"XDG_CONFIG_HOME":config,"XDG_CACHE_HOME":config,"ANONYMIZED_TELEMETRY":"false"})
 from browser_use import Agent, BrowserProfile, BrowserSession
 from browser_use.browser.profile import ProxySettings
 from browser_use.llm.openai.chat import ChatOpenAI
`) {
			return py
		}
	}
	return ""
}

func (r *Registry) registerBrowserAgent() {
	if r.browser == nil || r.LLM == nil {
		return
	}
	if r.browser.agentPython = r.findAgentPython(); r.browser.agentPython == "" {
		return
	}
	r.browser.agents = make(chan struct{}, agentRunsAtOnce)
	r.add(ToolBrowserAgent,
		"Autonomous browser agent (browser-use): give it a goal; it browses step by step on its own (opens, clicks, types, "+
			"scrolls, reads) and reports. For long work on a site; a few exact actions: browser. Shares cookies and the last page with browser.",
		map[string]any{
			"task":        prop("string", "The goal and what to report, with every value it needs"),
			"url":         prop("string", "Start page"),
			"max_steps":   prop("integer", "Default 20, max 50"),
			"vision":      prop("boolean", "Sees screenshots (default true)"),
			"domains":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Only these sites (a.com, *.a.com)"},
			"files":       map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Workspace files it may upload"},
			"new_session": prop("boolean", "Forget cookies first"),
		}, []string{"task"}, r.browserAgent)
}

var agentDomainRe = regexp.MustCompile(`^(?:https?://)?(?:\*\.)?[a-zA-Z0-9-]+(?:\.[a-zA-Z0-9-]+)+$`)

type agentReport struct {
	Done        bool     `json:"done"`
	Success     *bool    `json:"success"`
	Result      string   `json:"result"`
	Steps       int      `json:"steps"`
	URL         string   `json:"url"`
	Found       []string `json:"found"`
	Attachments []string `json:"attachments"`
	Files       []string `json:"files"`
	Error       string   `json:"error"`
	Log         []struct {
		Step    int      `json:"step"`
		URL     string   `json:"url"`
		Goal    string   `json:"goal"`
		Actions []string `json:"actions"`
		Error   string   `json:"error"`
	} `json:"log"`
}

func (r *Registry) browserAgent(ctx context.Context, sessionID string, args map[string]any) (string, error) {
	task := strings.TrimSpace(strArg(args, "task"))
	if task == "" {
		return "error: say in task what to do and what to report", nil
	}
	if utf8.RuneCountInString(task) > 6000 {
		return "error: the task is too long (at most 6000 characters); put long data in a workspace file and name it", nil
	}
	url := strArg(args, "url")
	if m := schemeRe.FindStringSubmatch(url); m != nil && !strings.EqualFold(m[1], "http") && !strings.EqualFold(m[1], "https") {
		return "error: only http(s) pages can be opened", nil
	}
	steps := intArg(args, "max_steps", agentStepsDefault, 1, agentStepsMax)
	var domains []string
	for _, d := range anyList(args["domains"]) {
		if d = strings.TrimSpace(d); d == "" {
			continue
		}
		if !agentDomainRe.MatchString(d) {
			return fmt.Sprintf("error: %q is not a site (example.com or *.example.com)", d), nil
		}
		domains = append(domains, d)
	}
	if len(domains) > 20 {
		return "error: at most 20 domains", nil
	}
	var files []string
	for _, f := range anyList(args["files"]) {
		if f = strings.TrimSpace(f); f != "" {
			files = append(files, f)
		}
	}
	vision := true
	if _, set := args["vision"]; set {
		vision = boolArg(args, "vision")
	}
	timeout := min(90*time.Second+time.Duration(steps)*40*time.Second, 20*time.Minute)
	bridge, err := newLLMBridge(ctx, r.LLM, steps)
	if err != nil {
		return "", err
	}
	cfg := map[string]any{"task": task, "url": url, "max_steps": steps, "vision": vision, "domains": domains, "files": files,
		"new_session": boolArg(args, "new_session"), "timeout_s": int(timeout.Seconds()), "screenshot": true}
	var rep agentReport
	run := browserRun{online: true, timeout: timeout + 2*time.Minute, bridge: bridge, slots: r.browser.agents, python: r.browser.agentPython}
	if err := r.runBrowserWith(ctx, sessionID, "browser_agent.py", cfg, run, &rep); err != nil {
		return "error: " + err.Error(), nil
	}
	return rep.text(bridge.spent()), nil
}

func (rep *agentReport) text(calls int, usage llm.Usage) string {
	var sb strings.Builder
	switch {
	case rep.Done && rep.Success != nil && !*rep.Success:
		fmt.Fprintf(&sb, "Browser agent: finished in %d steps, but says it did not succeed.\n", rep.Steps)
	case rep.Done:
		fmt.Fprintf(&sb, "Browser agent: done in %d steps.\n", rep.Steps)
	case rep.Steps == 0 && rep.Error != "":
		return "error: " + rep.Error
	default:
		fmt.Fprintf(&sb, "Browser agent: stopped after %d steps without finishing", rep.Steps)
		if rep.Error != "" {
			sb.WriteString(" (" + rep.Error + ")")
		}
		sb.WriteString(". What it did and found so far is below; run it again with a narrower task, or continue with browser.\n")
	}
	if rep.Result != "" {
		sb.WriteString("\nResult:\n" + strings.TrimSpace(rep.Result) + "\n")
	}
	if !rep.Done && len(rep.Found) > 0 {
		sb.WriteString("\nFound on the way:\n")
		for _, f := range rep.Found {
			sb.WriteString("- " + strings.TrimSpace(f) + "\n")
		}
	}
	if len(rep.Log) > 0 {
		sb.WriteString("\nSteps:\n")
		for _, l := range rep.Log {
			mark := "✓"
			if l.Error != "" {
				mark = "✗"
			}
			fmt.Fprintf(&sb, "%s %d. %s", mark, l.Step, l.Goal)
			if len(l.Actions) > 0 {
				sb.WriteString(" [" + strings.Join(l.Actions, ", ") + "]")
			}
			if l.Error != "" {
				sb.WriteString(" → " + l.Error)
			}
			sb.WriteString("\n")
		}
	}
	if files := append(rep.Files, rep.Attachments...); len(files) > 0 {
		fmt.Fprintf(&sb, "\nFiles: %s (look with view_image, deliver with send_file)\n", strings.Join(files, ", "))
	}
	if rep.URL != "" {
		fmt.Fprintf(&sb, "Last page: %s (browser continues from it; cookies are shared)\n", rep.URL)
	}
	fmt.Fprintf(&sb, "Model calls: %d (%d tokens)\n", calls, usage.TotalTokens)
	return strings.TrimSpace(sb.String())
}

type llmBridge struct {
	ctx      context.Context
	llm      llm.ChatClient
	token    string
	maxCalls int
	maxSteps int

	mu       sync.Mutex
	calls    int
	steps    int
	usage    llm.Usage
	noImages bool
	busy     chan struct{}
}

func newLLMBridge(ctx context.Context, client llm.ChatClient, steps int) (*llmBridge, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return nil, err
	}
	return &llmBridge{ctx: ctx, llm: client, token: hex.EncodeToString(b), maxCalls: steps*3 + 10, maxSteps: steps,
		busy: make(chan struct{}, 2)}, nil
}

func (b *llmBridge) spent() (int, llm.Usage) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls, b.usage
}

func (b *llmBridge) serve(path string) (stop func(), err error) {
	_ = os.Remove(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	ln, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	_ = os.Chmod(path, 0o600)
	srv := &http.Server{Handler: b, ReadHeaderTimeout: 30 * time.Second, ReadTimeout: 2 * time.Minute, IdleTimeout: 5 * time.Minute}
	go func() { _ = srv.Serve(ln) }()
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if srv.Shutdown(ctx) != nil {
			_ = srv.Close()
		}
		_ = os.Remove(path)
	}, nil
}

type bridgeMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

func bridgeFail(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": msg, "type": "bridge_error"}})
}

func (b *llmBridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || (r.URL.Path != "/v1/chat/completions" && r.URL.Path != "/chat/completions") {
		bridgeFail(w, http.StatusNotFound, "only chat completions")
		return
	}
	auth, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if subtle.ConstantTimeCompare([]byte(auth), []byte(b.token)) != 1 {
		bridgeFail(w, http.StatusUnauthorized, "wrong token")
		return
	}
	var req struct {
		Messages []bridgeMessage `json:"messages"`
		Stream   bool            `json:"stream"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, bridgeBodyMax)).Decode(&req); err != nil {
		bridgeFail(w, http.StatusBadRequest, "the request is not readable or too big")
		return
	}
	if req.Stream || len(req.Messages) == 0 || len(req.Messages) > 400 {
		bridgeFail(w, http.StatusBadRequest, "one non-streaming chat of at most 400 messages")
		return
	}
	select {
	case b.busy <- struct{}{}:
		defer func() { <-b.busy }()
	case <-r.Context().Done():
		return
	}
	ctx, cancel := context.WithCancel(b.ctx)
	defer cancel()
	defer context.AfterFunc(r.Context(), cancel)()

	text, finish, usage, status, msg := b.complete(ctx, req.Messages)
	if status != 0 {
		bridgeFail(w, status, msg)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id": "chatcmpl-macron", "object": "chat.completion", "created": time.Now().Unix(), "model": "macron",
		"choices": []map[string]any{{"index": 0, "message": map[string]any{"role": "assistant", "content": text}, "finish_reason": finish}},
		"usage":   map[string]any{"prompt_tokens": usage.PromptTokens, "completion_tokens": usage.CompletionTokens, "total_tokens": usage.TotalTokens},
	})
}

func (b *llmBridge) take() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.calls >= b.maxCalls || b.usage.Billable() >= min(40_000*b.maxSteps+100_000, bridgeTokensMax) {
		return false
	}
	b.calls++
	return true
}

func (b *llmBridge) add(u llm.Usage) {
	b.mu.Lock()
	b.usage = b.usage.Add(u)
	b.mu.Unlock()
}

func (b *llmBridge) complete(ctx context.Context, wire []bridgeMessage) (text, finish string, usage llm.Usage, status int, msg string) {
	b.mu.Lock()
	noImages := b.noImages
	b.mu.Unlock()
	msgs, images, wantJSON := bridgeMessages(wire, noImages)
	ask := func(msgs []llm.Message) (*llm.Response, error) {
		if !b.take() {
			return nil, errBridgeSpent
		}
		resp, err := b.llm.Complete(ctx, llm.Request{Messages: msgs})
		if err == nil {
			b.add(resp.Usage)
		}
		return resp, err
	}
	resp, err := ask(msgs)
	if err != nil && images && !errors.Is(err, errBridgeSpent) && refusedInput(err) {
		b.mu.Lock()
		b.noImages = true
		b.mu.Unlock()
		msgs, _, _ = bridgeMessages(wire, true)
		resp, err = ask(msgs)
	}
	switch {
	case errors.Is(err, errBridgeSpent):
		return "", "", usage, http.StatusBadRequest, "this task's model budget is spent: stop and report with done"
	case err != nil:
		if ctx.Err() == nil {
			log.Printf("browser_agent: model call failed: %v", err)
		}
		return "", "", usage, http.StatusBadGateway, "the model did not answer; try again"
	}
	usage, finish = resp.Usage, resp.FinishReason
	text = stripThinking(resp.Message.Text())
	if wantJSON {
		obj, ok := agentJSON(text)
		if !ok {

			fix := append(msgs[:len(msgs):len(msgs)], llm.Message{Role: "assistant", Content: firstRunes(text, 4000)},
				llm.Message{Role: "user", Content: "That was not the JSON object. Reply again with only the JSON object of <json_schema>: " +
					"start with { and end with }, every action inside its \"action\" list, no tool-call tags and no words around it."})
			if again, err := ask(fix); err == nil {
				usage = usage.Add(again.Usage)
				obj, ok = agentJSON(stripThinking(again.Message.Text()))
			}
		}
		if ok {
			text, finish = obj, "stop"
			b.progress(obj)
		}
	}
	if finish == "" {
		finish = "stop"
	}
	return text, finish, usage, 0, ""
}

var errBridgeSpent = errors.New("the run's model budget is spent")

func refusedInput(err error) bool {
	var api *llm.APIError
	return errors.As(err, &api) && (api.Status == 400 || api.Status == 413 || api.Status == 415 || api.Status == 422)
}

func (b *llmBridge) progress(obj string) {
	var out struct {
		NextGoal string `json:"next_goal"`
	}
	if json.Unmarshal([]byte(obj), &out) != nil {
		return
	}
	b.mu.Lock()
	b.steps++
	n := b.steps
	b.mu.Unlock()
	goal := strings.Join(strings.Fields(out.NextGoal), " ")
	ReportStatus(b.ctx, strings.TrimSpace(fmt.Sprintf("%d/%d · %s", min(n, b.maxSteps), b.maxSteps, firstRunes(goal, 70))))
}

func bridgeMessages(wire []bridgeMessage, noImages bool) (msgs []llm.Message, images, wantJSON bool) {
	type part struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}
	kept := 0
	keep := make([]bool, len(wire))
	for i := len(wire) - 1; i >= 0; i-- {
		var parts []part
		if json.Unmarshal(wire[i].Content, &parts) == nil {
			for _, p := range parts {
				if p.Type == "image_url" && kept < bridgeImagesKept {
					keep[i] = true
					kept++
				}
			}
		}
	}
	for i, m := range wire {
		role := m.Role
		switch role {
		case "system", "developer":
			role = "system"
		case "assistant":
		default:
			role = "user"
		}
		out := llm.Message{Role: role}
		var s string
		var parts []part
		switch {
		case json.Unmarshal(m.Content, &s) == nil:
			out.Content = s
		case json.Unmarshal(m.Content, &parts) == nil:
			var texts []string
			for _, p := range parts {
				switch {
				case p.Type == "text":
					texts = append(texts, p.Text)
					out.Parts = append(out.Parts, llm.TextPart(p.Text))
				case p.Type == "image_url" && !noImages && keep[i] && role == "user" && screenshotURL(p.ImageURL.URL):
					out.Parts = append(out.Parts, llm.ImagePart(p.ImageURL.URL))
					images = true
				case p.Type == "image_url":
					texts = append(texts, "[a screenshot]")
					out.Parts = append(out.Parts, llm.TextPart("[a screenshot]"))
				}
			}
			out.Content = strings.Join(texts, "\n")
			if !slicesHasImage(out.Parts) {
				out.Parts = nil
			}
		}
		if role == "system" && strings.Contains(out.Content, "<json_schema>") {
			wantJSON = true
		}
		msgs = append(msgs, out)
	}
	return msgs, images, wantJSON
}

func slicesHasImage(parts []llm.Part) bool {
	for _, p := range parts {
		if p.Type == "image_url" {
			return true
		}
	}
	return false
}

var screenshotRe = regexp.MustCompile(`^data:image/(?:png|jpeg|webp|gif);base64,`)

func screenshotURL(u string) bool { return len(u) <= bridgeImageMax && screenshotRe.MatchString(u) }

var thinkRe = regexp.MustCompile(`(?s)<(think|thinking|reasoning)>.*?</(?:think|thinking|reasoning)>`)

func stripThinking(s string) string { return strings.TrimSpace(thinkRe.ReplaceAllString(s, "")) }

func firstRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}

var (
	xmlFuncRe  = regexp.MustCompile(`(?s)<function=([A-Za-z_][\w.-]*)>(.*?)(?:</function>|$)`)
	xmlParamRe = regexp.MustCompile(`(?s)<parameter=([A-Za-z_]\w*)>\s*(.*?)\s*(?:</parameter>|$)`)
	agentTagRe = regexp.MustCompile(`(?s)<[^<>]{1,80}>`)
)

func agentJSON(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "{") && json.Valid([]byte(s)) {
		var v map[string]any
		if json.Unmarshal([]byte(s), &v) == nil {
			if _, ok := v["action"]; ok {
				return s, true
			}
			if obj, ok := actionsFrom([]map[string]any{v}, ""); ok {
				return obj, true
			}
			return s, true
		}
	}
	var objs []map[string]any
	var first string
	for i, tries := 0, 0; i < len(s) && tries < 60; i++ {
		if s[i] != '{' {
			continue
		}
		tries++
		dec := json.NewDecoder(strings.NewReader(s[i:]))
		var v map[string]any
		if dec.Decode(&v) != nil {
			continue
		}
		raw := s[i : i+int(dec.InputOffset())]
		if _, ok := v["action"]; ok {
			return raw, true
		}
		if first == "" {
			first = raw
		}
		objs = append(objs, v)
		i += int(dec.InputOffset()) - 1
	}
	prose := strings.TrimSpace(agentTagRe.ReplaceAllString(strings.SplitN(s, "<", 2)[0], ""))
	if obj, ok := actionsFrom(objs, prose); ok {
		return obj, true
	}
	if m := xmlFuncRe.FindAllStringSubmatch(s, 8); m != nil {
		var calls []map[string]any
		for _, f := range m {
			params := map[string]any{}
			for _, p := range xmlParamRe.FindAllStringSubmatch(f[2], 20) {
				params[p[1]] = paramValue(p[2])
			}
			calls = append(calls, map[string]any{"name": f[1], "arguments": params})
		}
		if obj, ok := actionsFrom(calls, prose); ok {
			return obj, true
		}
	}
	return first, first != ""
}

func actionsFrom(calls []map[string]any, prose string) (string, bool) {
	var actions []any
	for _, c := range calls {
		name, _ := c["name"].(string)
		if name == "" {
			continue
		}
		name = strings.TrimPrefix(name, "functions.")
		args := c["arguments"]
		if args == nil {
			args = c["parameters"]
		}
		if s, ok := args.(string); ok {
			var m map[string]any
			if json.Unmarshal([]byte(s), &m) != nil {
				continue
			}
			args = m
		}
		if args == nil {
			args = map[string]any{}
		}
		if _, ok := args.(map[string]any); !ok {
			continue
		}
		actions = append(actions, map[string]any{name: args})
	}
	if len(actions) == 0 {
		return "", false
	}
	note := firstRunes(strings.Join(strings.Fields(prose), " "), 300)
	out, err := json.Marshal(map[string]any{"evaluation_previous_goal": "", "memory": note, "next_goal": note, "action": actions})
	return string(out), err == nil
}

func paramValue(s string) any {
	if n, err := strconv.ParseFloat(s, 64); err == nil {
		return n
	}
	var v any
	if (strings.HasPrefix(s, "{") || strings.HasPrefix(s, "[") || s == "true" || s == "false") && json.Unmarshal([]byte(s), &v) == nil {
		return v
	}
	return s
}
