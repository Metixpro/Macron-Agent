package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/cmdgate"
	"github.com/Metixpro/macron-agent/pkg/llm"
)

func gatedRegistry(asked *[]string, answer cmdgate.Choice) (*Registry, context.Context) {
	g := cmdgate.New(cmdgate.Env{SelfNames: []string{"macron"}}, false)
	g.Policy = func() cmdgate.Policy { return cmdgate.Policy{Mode: cmdgate.ModeManual, Breaker: 0} }
	ctx := cmdgate.WithAsker(context.Background(), func(_ context.Context, q cmdgate.Question) cmdgate.Choice {
		*asked = append(*asked, q.Command)
		return answer
	})
	return &Registry{Gate: g}, ctx
}

func acts(list ...map[string]any) []any {
	out := make([]any, len(list))
	for i, a := range list {
		out[i] = a
	}
	return out
}

func TestGateBrowserReadingNeedsNothing(t *testing.T) {
	var asked []string
	r, ctx := gatedRegistry(&asked, cmdgate.Deny)
	for _, c := range [][]any{
		acts(map[string]any{"action": "goto", "url": "https://example.com"}, map[string]any{"action": "scroll", "to": "bottom"},
			map[string]any{"action": "extract", "selector": "main"}),
		// A search box: typing and pressing Enter, or its own button, only reads.
		acts(map[string]any{"action": "fill", "selector": "input[name=q]", "text": "go modules"},
			map[string]any{"action": "press", "key": "Enter"}),
		acts(map[string]any{"action": "type", "selector": "#search", "text": "x"}, map[string]any{"action": "click", "selector": "button[type=submit]"}),
		// Clicking a link or a tab, or pressing Tab in a form, is not sending anything.
		acts(map[string]any{"action": "click", "selector": "a.next-page"}),
		acts(map[string]any{"action": "fill", "selector": "#email", "text": "a@b.c"}, map[string]any{"action": "press", "key": "Tab"}),
		acts(map[string]any{"action": "eval", "script": "document.title"}),
	} {
		if refused := r.gateBrowser(ctx, "s", "https://example.com", c); refused != "" {
			t.Errorf("%v refused: %s", c, refused)
		}
	}
	if len(asked) != 0 {
		t.Fatalf("asked for reading: %q", asked)
	}
}

func TestGateBrowserAsksBeforeActing(t *testing.T) {
	for name, c := range map[string][]any{
		"form": acts(map[string]any{"action": "fill", "selector": "#email", "text": "me@x.com"},
			map[string]any{"action": "fill", "selector": "#password", "text": "hunter2"}, map[string]any{"action": "press"}),
		"buy":      acts(map[string]any{"action": "click", "selector": "button:has-text('Buy now')"}),
		"persian":  acts(map[string]any{"action": "click", "selector": "text=پرداخت"}),
		"submit":   acts(map[string]any{"action": "click", "selector": "input[type=submit]"}),
		"upload":   acts(map[string]any{"action": "upload", "selector": "#file", "files": []any{"report.pdf"}}),
		"script":   acts(map[string]any{"action": "eval", "script": "document.forms[0].submit()"}),
		"checkout": acts(map[string]any{"action": "check", "selector": "#terms"}, map[string]any{"action": "click", "selector": "#next"}),
	} {
		var asked []string
		r, ctx := gatedRegistry(&asked, cmdgate.Deny)
		refused := r.gateBrowser(ctx, "s", "https://shop.example.com/cart", c)
		if refused == "" || len(asked) != 1 {
			t.Errorf("%s: refused=%q asked=%d", name, refused, len(asked))
			continue
		}
		if !strings.Contains(asked[0], "shop.example.com") {
			t.Errorf("%s: the question does not name the site: %q", name, asked[0])
		}
		if strings.Contains(asked[0], "hunter2") {
			t.Errorf("%s: a typed password was shown: %q", name, asked[0])
		}
	}
}

func TestGateBrowserAsksOncePerSitePerCall(t *testing.T) {
	var asked []string
	r, ctx := gatedRegistry(&asked, cmdgate.Once)
	c := acts(map[string]any{"action": "fill", "selector": "#a", "text": "1"}, map[string]any{"action": "click", "selector": "#send"},
		map[string]any{"action": "fill", "selector": "#b", "text": "2"}, map[string]any{"action": "click", "selector": "#send"},
		map[string]any{"action": "goto", "url": "https://other.example.org"}, map[string]any{"action": "click", "selector": "text=Delete"})
	if refused := r.gateBrowser(ctx, "s", "https://a.example.com", c); refused != "" {
		t.Fatalf("approved, still refused: %s", refused)
	}
	if len(asked) != 2 || !strings.Contains(asked[1], "other.example.org") {
		t.Fatalf("asked %d times: %q", len(asked), asked)
	}
}

func TestGateBrowserNoGate(t *testing.T) {
	r := &Registry{}
	if refused := r.gateBrowser(context.Background(), "s", "x.com", acts(map[string]any{"action": "click", "selector": "#buy"})); refused != "" {
		t.Fatalf("no gate configured, still refused: %s", refused)
	}
}

type fakePlugins struct{ trusted map[string]bool }

func (fakePlugins) Tools() []llm.Tool           { return nil }
func (fakePlugins) Has(string) bool             { return true }
func (fakePlugins) Allowed(string, string) bool { return true }
func (f fakePlugins) Trusted(name string) bool  { return f.trusted[name] }
func (fakePlugins) Call(context.Context, string, string, map[string]any) (string, error) {
	return "", nil
}

func TestGatePlugin(t *testing.T) {
	var asked []string
	r, ctx := gatedRegistry(&asked, cmdgate.Session)
	p := fakePlugins{trusted: map[string]bool{"mcp_docs_search": true}}
	if refused := r.gatePlugin(ctx, "s", p, "mcp_docs_search", map[string]any{"q": "x"}); refused != "" || len(asked) != 0 {
		t.Fatalf("trusted tool: refused=%q asked=%d", refused, len(asked))
	}
	for i := 0; i < 2; i++ {
		if refused := r.gatePlugin(ctx, "s", p, "mcp_github_create_issue", map[string]any{"title": i}); refused != "" {
			t.Fatalf("approved for the session, still refused: %s", refused)
		}
	}
	if len(asked) != 1 || !strings.Contains(asked[0], `"title":0`) {
		t.Fatalf("asked %d times: %q", len(asked), asked)
	}
}

func TestSiteOf(t *testing.T) {
	for in, want := range map[string]string{"https://Shop.Example.com/a?b": "shop.example.com", "example.org/x": "example.org",
		"": "the open page", "file:///tmp/a.html": "a workspace page"} {
		if got := siteOf(in); got != want {
			t.Errorf("siteOf(%q) = %q, want %q", in, got, want)
		}
	}
}
