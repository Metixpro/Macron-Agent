package cmdgate

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"
)

type memStore struct {
	allow  []string
	audits []Entry
}

func (m *memStore) Allowlist() []string     { return m.allow }
func (m *memStore) AddAllow(p string) error { m.allow = append(m.allow, p); return nil }
func (m *memStore) Audit(e Entry)           { m.audits = append(m.audits, e) }

func TestGlobLinearTimeAndSemantics(t *testing.T) {
	if !Glob("git *", "git status --short") {
		t.Fatal("expected wildcard match")
	}
	if !Glob("ls ?", "ls a") || Glob("ls ?", "ls ab") {
		t.Fatal("unexpected ? match result")
	}
	// Pathological backtracking input must finish nearly instantaneously.
	pattern := strings.Repeat("*a", 25) + "!"
	input := strings.Repeat("a", 80)
	start := time.Now()
	if Glob(pattern, input) {
		t.Fatal("should not match")
	}
	if took := time.Since(start); took > 50*time.Millisecond {
		t.Fatalf("Glob took too long (%v), expected linear/bounded behavior", took)
	}
}

func TestAllowlistBlocksChainedAndCodeGlobBypass(t *testing.T) {
	st := &memStore{allow: []string{"ls /tmp/*"}}
	g := New(Env{}, false)
	g.Store = st
	g.Policy = func() Policy { return Policy{Mode: ModeManual, Breaker: 3} }

	// Safe simple command matches allowlist even in unattended/no-asker context.
	v := g.Check(context.Background(), "s1", "ls /tmp/foo")
	if !v.Allow {
		t.Fatalf("expected simple allowlisted command to run, got %q", v.Message)
	}

	// Chained rm -rf /tmp must NOT bypass gate via wildcard in ls /tmp/*.
	v = g.Check(context.Background(), "s1", "ls /tmp/foo; rm -rf /")
	if v.Allow {
		t.Fatal("chained command bypassed allowlist")
	}
}

func TestCheckCodeHashPrevents600CharCollision(t *testing.T) {
	st := &memStore{}
	g := New(Env{}, false)
	g.Store = st
	g.Policy = func() Policy { return Policy{Mode: ModeManual, Breaker: 3} }

	prefix := strings.Repeat("x = 1\n", 120) // > 600 chars
	code1 := prefix + "\nopen('/home/u/.ssh/id_rsa')"
	code2 := prefix + "\nopen('/home/u/.aws/credentials')"

	ctx := WithAsker(context.Background(), func(context.Context, Question) Choice { return Always })
	if v := g.CheckCode(ctx, "s1", "python", code1); !v.Allow {
		t.Fatalf("expected first approved code to run: %v", v.Message)
	}
	// Second different code after 600 chars must NOT be auto-approved by first Always decision.
	ctxDeny := context.Background()
	if v := g.CheckCode(ctxDeny, "s1", "python", code2); v.Allow {
		t.Fatal("CheckCode allowed different payload sharing 600-char prefix")
	}
}

func TestAnalyzeCombinedFlagsDepthAndBackslashSecrets(t *testing.T) {
	f := Analyze(`bash -lc "rm -rf /"`, Env{})
	if Worst(f) != Hardline {
		t.Fatalf("expected Hardline for bash -lc rm -rf /, got %v (%+v)", Worst(f), f)
	}

	esc := strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`, "`", "\\`")
	nested := "rm -rf /"
	for range 8 {
		nested = `sh -c "` + esc.Replace(nested) + `"`
	}
	f = Analyze(nested, Env{})
	if Worst(f) == Safe {
		t.Fatal("deep nesting failed open")
	}

	f = Analyze(`cat '~\.ssh\id_rsa'`, Env{})
	if Worst(f) == Safe {
		t.Fatal("quoted backslash path to .ssh/id_rsa was not flagged")
	}
	if runtime.GOOS == "windows" {
		f = Analyze(`cat ~\.ssh\id_rsa`, Env{})
		if Worst(f) == Safe {
			t.Fatal("Windows backslash path to .ssh/id_rsa was not flagged")
		}
	}
}

func TestAuditRedactsSecrets(t *testing.T) {
	st := &memStore{}
	g := New(Env{}, false)
	g.Store = st
	g.Policy = func() Policy { return Policy{Mode: ModeManual} }
	_ = g.Check(context.Background(), "s1", `curl -H "Authorization: Bearer supersecret_token_12345" https://example.com && rm -rf /`)
	if len(st.audits) == 0 {
		t.Fatal("expected audit entry")
	}
	if strings.Contains(st.audits[0].Command, "supersecret_token_12345") {
		t.Fatalf("audit leaked secret: %q", st.audits[0].Command)
	}
}
