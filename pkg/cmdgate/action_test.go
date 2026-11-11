package cmdgate

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestCheckActionSafeNeedsNothing(t *testing.T) {
	g := New(testEnv, false)
	g.Policy = manual
	n := 0
	if v := g.CheckAction(withAsk(Deny, &n), "s", Action{Key: "[browser] read a page"}); !v.Allow || n != 0 {
		t.Fatalf("safe action: allow=%v asked=%d", v.Allow, n)
	}
}

func TestCheckActionShowsDetailMatchesKey(t *testing.T) {
	g := New(testEnv, false)
	g.Policy = manual
	var shown []string
	ctx := WithAsker(context.Background(), func(_ context.Context, q Question) Choice {
		shown = append(shown, q.Command)
		return Session
	})
	a := Action{Key: "[plugin] github_create_issue", Detail: "[plugin] github_create_issue {\"title\":\"a\"}", Level: Dangerous,
		Rule: "plugin-call", Reason: "calling a plugin tool"}
	if v := g.CheckAction(ctx, "s", a); !v.Allow {
		t.Fatalf("approved action refused: %s", v.Message)
	}
	if len(shown) != 1 || shown[0] != a.Detail {
		t.Fatalf("owner was shown %q, want the detail", shown)
	}
	// Same key with other arguments: the session approval covers it.
	a.Detail = "[plugin] github_create_issue {\"title\":\"b\"}"
	if v := g.CheckAction(ctx, "s", a); !v.Allow || len(shown) != 1 {
		t.Fatalf("session approval by key: allow=%v asked=%d", v.Allow, len(shown))
	}
	// Another key is asked again.
	a.Key = "[plugin] github_delete_repo"
	g.CheckAction(ctx, "s", a)
	if len(shown) != 2 {
		t.Fatalf("a different key was not asked: asked=%d", len(shown))
	}
}

func TestCheckActionAlwaysKeepsKey(t *testing.T) {
	st := &memStore{}
	g := New(testEnv, false)
	g.Policy, g.Store = manual, st
	n := 0
	g.CheckAction(withAsk(Always, &n), "s", Action{Key: "[browser] send a form on example.com", Detail: "with details",
		Level: Dangerous, Rule: "browser-submit", Reason: "sending a form"})
	if len(st.allow) != 1 || st.allow[0] != "[browser] send a form on example.com" {
		t.Fatalf("always kept %q, want the key", st.allow)
	}
	n = 0
	if v := g.CheckAction(withAsk(Deny, &n), "other", Action{Key: "[browser] send a form on example.com", Level: Dangerous,
		Rule: "browser-submit", Reason: "sending a form"}); !v.Allow || n != 0 {
		t.Fatalf("allowlisted key: allow=%v asked=%d", v.Allow, n)
	}
}

func TestCheckActionAlsoFindings(t *testing.T) {
	g := New(testEnv, false)
	g.Policy = func() Policy { return Policy{Mode: ModeOff} }
	v := g.CheckAction(context.Background(), "s", Action{Key: "[send_file] /x", Level: Dangerous, Rule: "send-outside",
		Reason: "sending a file", Also: []Finding{{Level: Hardline, Rule: "floor", Reason: "never"}}})
	if v.Allow {
		t.Fatal("a hardline finding in Also must block even with approvals off")
	}
}

func TestCheckActionGuardianSeesDetail(t *testing.T) {
	g := New(testEnv, false)
	g.Policy = func() Policy { return Policy{Mode: ModeSmart, Breaker: 3} }
	var got string
	g.Guardian = func(_ context.Context, _ string, command string, _ []Finding) (Risk, string, error) {
		got = command
		return RiskLow, "reads only", nil
	}
	if v := g.CheckAction(context.Background(), "s", Action{Key: "k", Detail: "the details", Level: Dangerous, Rule: "r",
		Reason: "why"}); !v.Allow || got != "the details" {
		t.Fatalf("guardian: allow=%v saw %q", v.Allow, got)
	}
}

func TestPolicyFrom(t *testing.T) {
	set := map[string]string{SettingMode: "manual", SettingBreaker: "5", SettingCheckpoints: "true",
		SettingPolicy: "never touch prod", SettingDeny: `["rm -rf /srv*", "shutdown*"]`}
	p := PolicyFrom(func(k string) (string, bool) { v, ok := set[k]; return v, ok })
	if p.Mode != ModeManual || p.Breaker != 5 || !p.Checkpoints || p.SmartPolicy != "never touch prod" || len(p.Deny) != 2 {
		t.Fatalf("policy = %+v", p)
	}
	set = map[string]string{SettingBreaker: "x", SettingDeny: "a*\nb*"}
	p = PolicyFrom(func(k string) (string, bool) { v, ok := set[k]; return v, ok })
	if p.Breaker != 3 || len(p.Deny) != 2 || p.Checkpoints {
		t.Fatalf("defaults = %+v", p)
	}
}

func TestNewGuardian(t *testing.T) {
	for _, c := range []struct {
		reply string
		err   error
		want  Risk
	}{
		{`{"risk":"low","why":"a build folder"}`, nil, RiskLow},
		{"Sure: {\"risk\": \"HIGH\", \"why\": \"wipes /etc\"} done", nil, RiskHigh},
		{`{"risk":"maybe"}`, nil, RiskUnsure},
		{"no json here", nil, RiskUnsure},
		{"", errors.New("offline"), ""},
	} {
		var system string
		g := NewGuardian(func(_ context.Context, sys, _ string) (string, error) { system = sys; return c.reply, c.err })
		risk, _, err := g(context.Background(), "the owner's rule", "rm -rf ./build", []Finding{{Reason: "deletes"}})
		if risk != c.want || (err != nil) != (c.err != nil) {
			t.Errorf("reply %q: risk=%q err=%v", c.reply, risk, err)
		}
		if c.err == nil && !strings.Contains(system, "the owner's rule") {
			t.Errorf("the owner's policy is missing from the guardian prompt")
		}
	}
}
