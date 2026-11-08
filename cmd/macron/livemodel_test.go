package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/netclient"
	"github.com/Metixpro/macron-agent/pkg/search"
	"github.com/Metixpro/macron-agent/pkg/store"
)

var leakRe = regexp.MustCompile(`(?i)<\s*/?\s*(?:[a-z0-9_-]+:)?(?:tool_call|tool_calls|function_calls?|invoke|parameter|tool_use|arg_key|arg_value)\b|` +
	`<function=|<\|[a-z_]+\|>|\[TOOL_CALLS\]|\bto=functions\.|(?m:^\s*Action:\s)|` +
	`(?m:^[\s>*•-]*(?:bash_execute|run_code|write_file|read_file|web_search|web_fetch|send_file|list_files|update_plan|generate_chart|generate_image)\s*\()|` +
	`"name"\s*:\s*"(?:bash_execute|run_code|write_file|read_file|web_search|send_file|update_plan|generate_chart)"`)

type labProxy struct {
	target, key string
	mu          sync.Mutex
	replies     []string
	raw         []string
}

func (p *labProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	req, _ := http.NewRequestWithContext(r.Context(), r.Method, p.target+strings.TrimPrefix(r.URL.Path, "/v1"), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.key)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	w.WriteHeader(resp.StatusCode)
	var raw bytes.Buffer
	flusher, _ := w.(http.Flusher)
	sc := bufio.NewReader(resp.Body)
	for {
		line, err := sc.ReadBytes('\n')
		if len(line) > 0 {
			raw.Write(line)
			_, _ = w.Write(line)
			if flusher != nil {
				flusher.Flush()
			}
		}
		if err != nil {
			break
		}
	}
	p.mu.Lock()
	p.replies = append(p.replies, assembled(raw.String()))
	p.raw = append(p.raw, string(body)+"\n\n>>> REPLY\n"+raw.String())
	p.mu.Unlock()
}

func assembled(raw string) string {
	var text, calls strings.Builder
	if !strings.Contains(raw, "data:") {
		var r struct {
			Choices []struct {
				Message struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Function struct{ Name, Arguments string } `json:"function"`
					} `json:"tool_calls"`
				} `json:"message"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(raw), &r) == nil && len(r.Choices) > 0 {
			for _, c := range r.Choices[0].Message.ToolCalls {
				fmt.Fprintf(&calls, "[native] %s %s\n", c.Function.Name, c.Function.Arguments)
			}
			return calls.String() + r.Choices[0].Message.Content
		}
		return raw
	}
	for _, line := range strings.Split(raw, "\n") {
		data, ok := strings.CutPrefix(strings.TrimSpace(line), "data:")
		if !ok {
			continue
		}
		var c struct {
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						Function struct{ Name, Arguments string } `json:"function"`
					} `json:"tool_calls"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(strings.TrimSpace(data)), &c) != nil || len(c.Choices) == 0 {
			continue
		}
		text.WriteString(c.Choices[0].Delta.Content)
		for _, tc := range c.Choices[0].Delta.ToolCalls {
			if tc.Function.Name != "" {
				fmt.Fprintf(&calls, "\n[native] %s ", tc.Function.Name)
			}
			calls.WriteString(tc.Function.Arguments)
		}
	}
	return strings.TrimSpace(calls.String() + "\n" + text.String())
}

func shown(h *harness) []string {
	h.tg.mu.Lock()
	defer h.tg.mu.Unlock()
	var out []string
	for _, c := range h.tg.calls {
		if t, ok := c.body["text"].(string); ok && t != "" {
			out = append(out, c.method+": "+t)
		}
		if rm, ok := c.body["rich_message"].(map[string]any); ok {
			out = append(out, c.method+": "+fmt.Sprint(rm["markdown"], rm["html"]))
		}
	}
	return out
}

func TestLiveModelNeverShowsItsCalls(t *testing.T) {
	url, key, model := os.Getenv("WAZE_TEST_LLM_URL"), os.Getenv("WAZE_TEST_LLM_KEY"), os.Getenv("WAZE_TEST_LLM_MODEL")
	if url == "" || key == "" || model == "" {
		t.Skip("WAZE_TEST_LLM_URL, WAZE_TEST_LLM_KEY and WAZE_TEST_LLM_MODEL are not set")
	}
	h := newHarness(t)
	proxy := &labProxy{target: strings.TrimRight(url, "/"), key: key}
	srv := httptest.NewServer(proxy)
	t.Cleanup(srv.Close)
	ps, _ := h.st.Providers(bg)
	p := ps[0]
	p.BaseURL, p.APIKey, p.Model, p.Name = srv.URL+"/v1", "sk-lab-proxy", model, "lab"
	if err := h.st.UpdateProvider(bg, p); err != nil {
		t.Fatal(err)
	}
	_ = h.bot.prov.Reload(bg)
	h.bot.runTimeout = 4 * time.Minute
	if os.Getenv("WAZE_TEST_LLM_PLUGINS") != "" {
		h.bot.app.Registry.SetPlugins(labPlugins{})
	}
	if os.Getenv("WAZE_TEST_LLM_FOCUS") != "" {
		h.bot.saveToolSkill(bg, p.ID, toolSkill{Mode: toolModeFocus})
	}
	if os.Getenv("WAZE_TEST_LLM_WEB") != "" {
		web := netclient.NewClient(netclient.Options{UseEnvProxy: true, Timeout: 25 * time.Second})
		engine := search.NewEngine(web)
		h.bot.app.Search, h.bot.app.Registry.Search, h.bot.app.Registry.HTTP = engine, engine, web
		h.bot.runTimeout = 8 * time.Minute
		_ = h.st.SetSetting(bg, store.SettingAgentSteps, "80")
	}

	jobs := strings.Split(os.Getenv("WAZE_TEST_LLM_JOBS"), "|")
	if os.Getenv("WAZE_TEST_LLM_JOBS") == "" {
		jobs = []string{
			"یه فایل hello.py بساز که Hello Macron چاپ کنه، اجراش کن و خروجی رو بگو",
			"یه نمودار میله‌ای از فروش چهار ماه بکش: فروردین ۱۲، اردیبهشت ۱۸، خرداد ۹، تیر ۲۴",
			"توی فایل hello.py یه تابع جمع دو عدد اضافه کن، با ۲ و ۳ صداش بزن و نتیجه رو نشون بده",
			"یه برنامه کار سه‌مرحله‌ای بنویس و قدم‌به‌قدم انجامش بده: یه فایل notes.txt با سه خط بساز، تعداد خط‌هاشو بشمار، بعد خلاصه‌اش رو بگو",
			"مرسی، عالی بود",
		}
	}
	logDir := os.Getenv("WAZE_TEST_LLM_LOG")
	leaks := 0
	for i, job := range jobs {
		h.tg.reset()
		proxy.mu.Lock()
		proxy.replies, proxy.raw = nil, nil
		proxy.mu.Unlock()
		start := time.Now()
		h.send(private(int64(700+i), job))
		texts := shown(h)
		var bad []string
		for _, s := range texts {
			if loc := leakRe.FindStringIndex(s); loc != nil {
				bad = append(bad, s[max(loc[0]-80, 0):min(loc[1]+120, len(s))])
			}
		}
		final := ""
		for _, s := range texts {
			if strings.HasPrefix(s, "sendRichMessage:") || strings.HasPrefix(s, "sendMessage:") {
				final = s
			}
		}
		proxy.mu.Lock()
		var used []string
		for _, r := range proxy.replies {
			for _, l := range strings.Split(r, "\n") {
				if name, ok := strings.CutPrefix(strings.TrimSpace(l), "[native] "); ok {
					used = append(used, strings.Fields(name + " ")[0])
				}
			}
		}
		proxy.mu.Unlock()
		t.Logf("job %d (%s): %d model calls, tools %v, %d messages\n  final: %s", i+1, time.Since(start).Round(time.Second), len(proxy.replies), used, len(texts),
			strings.ReplaceAll(final[:min(len(final), 700)], "\n", " ⏎ "))
		if logDir != "" {
			proxy.mu.Lock()
			raw := strings.Join(proxy.replies, "\n\n======== next call ========\n\n")
			proxy.mu.Unlock()
			_ = os.WriteFile(filepath.Join(logDir, fmt.Sprintf("job%d-model.txt", i+1)), []byte(job+"\n\n"+raw), 0o600)
			_ = os.WriteFile(filepath.Join(logDir, fmt.Sprintf("job%d-shown.txt", i+1)), []byte(strings.Join(texts, "\n\n-----\n\n")), 0o600)
			proxy.mu.Lock()
			_ = os.WriteFile(filepath.Join(logDir, fmt.Sprintf("job%d-raw.txt", i+1)), []byte(strings.Join(proxy.raw, "\n\n######## next call ########\n\n")), 0o600)
			proxy.mu.Unlock()
		}
		for _, b := range bad {
			leaks++
			t.Errorf("job %d showed a call or its markup: …%s…", i+1, b)
		}
	}
	if leaks == 0 {
		t.Logf("no call and no markup reached the user in %d jobs", len(jobs))
	}
}

type labPlugins struct{}

func (labPlugins) Tools() []llm.Tool {
	out := []llm.Tool{{Type: "function", Function: llm.ToolDef{Name: "sqlite_query", Description: "Run a read-only SQL query on the users database.",
		Parameters: map[string]any{"type": "object", "properties": map[string]any{"sql": map[string]any{"type": "string"}}, "required": []string{"sql"}}}}}
	for i := 0; i < 45; i++ {
		out = append(out, llm.Tool{Type: "function", Function: llm.ToolDef{Name: fmt.Sprintf("seclens_check_%02d", i), Description: "A network security check.",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{}}}})
	}
	return out
}
func (p labPlugins) Has(name string) bool {
	for _, t := range p.Tools() {
		if t.Function.Name == name {
			return true
		}
	}
	return false
}
func (labPlugins) Allowed(string, string) bool { return true }
func (labPlugins) Call(_ context.Context, _, name string, _ map[string]any) (string, error) {
	if name == "sqlite_query" {
		return "count\n1342", nil
	}
	return "ok", nil
}
