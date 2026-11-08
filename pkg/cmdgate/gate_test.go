package cmdgate

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type memStore struct {
	mu    sync.Mutex
	allow []string
	log   []Entry
}

func (m *memStore) Allowlist() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.allow...)
}

func (m *memStore) AddAllow(p string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.allow = append(m.allow, p)
	return nil
}

func (m *memStore) Audit(e Entry) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.log = append(m.log, e)
}

func (m *memStore) last() Entry {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.log[len(m.log)-1]
}

func newGate(mode string) (*Gate, *memStore) {
	st := &memStore{}
	g := New(testEnv, false)
	g.Store = st
	g.Policy = func() Policy { return Policy{Mode: mode, Breaker: 3, Deny: []string{"git push --force*", "*curl*|*sh*"}} }
	return g, st
}

// asking answers every question with c, and counts them.
func asking(c Choice, n *int) context.Context {
	return WithAsker(context.Background(), func(context.Context, Question) Choice {
		*n++
		return c
	})
}

// The floor holds in every mode, with yolo, from every origin.
func TestHardlineHoldsEverywhere(t *testing.T) {
	for _, mode := range []string{ModeSmart, ModeManual, ModeOff} {
		g, st := newGate(mode)
		g.SetYolo(time.Hour)
		yes := 0
		for _, ctx := range []context.Context{asking(Always, &yes), WithOrigin(context.Background(), OriginCron), WithOrigin(context.Background(), OriginSubAgent)} {
			for _, c := range []string{"rm -rf /", "rm -rf /etc", "mkfs.ext4 /dev/sda1", ":(){ :|:& };:", "sudo -S rm x"} {
				if v := g.Check(ctx, "s", c); v.Allow {
					t.Errorf("%s: %q ran", mode, c)
				}
				if e := st.last(); e.Decision != "hardline" || e.Decider != "floor" {
					t.Errorf("audit: %+v", e)
				}
			}
		}
		if yes != 0 {
			t.Errorf("%s: the owner was asked about the floor", mode)
		}
	}
	g := New(testEnv, true) // yolo from the environment at start
	if v := g.Check(context.Background(), "s", "rm -rf /"); v.Allow {
		t.Error("boot yolo does not lift the floor")
	}
}

func TestSafeCommandsRunWithoutAsking(t *testing.T) {
	g, _ := newGate(ModeManual)
	asked := 0
	ctx := asking(Deny, &asked)
	for _, c := range []string{`git commit -m "never run rm -rf /"`, `echo "does this use mkfs?"`, "ls -la", `grep -o "[^\"]*" f`} {
		if v := g.Check(ctx, "s", c); !v.Allow {
			t.Errorf("%q: %s", c, v.Message)
		}
	}
	if asked != 0 {
		t.Errorf("asked %d times", asked)
	}
}

// once, session, always and no.
func TestTheOwnerDecides(t *testing.T) {
	g, st := newGate(ModeManual)
	asked := 0
	if v := g.Check(asking(Once, &asked), "s", "rm -rf ./build"); !v.Allow || asked != 1 {
		t.Fatalf("once: %+v", v)
	}
	if v := g.Check(asking(Deny, &asked), "s", "rm -rf ./build"); v.Allow || asked != 2 || !strings.Contains(v.Message, "said no") {
		t.Fatalf("once is once: %+v", v)
	}
	g.Check(asking(Session, &asked), "s", "rm build/ -rf")
	if v := g.Check(asking(Deny, &asked), "s", "rm build/ -rf"); !v.Allow || asked != 3 {
		t.Fatalf("this session: %+v", v)
	}
	if v := g.Check(asking(Deny, &asked), "other", "rm build/ -rf"); v.Allow {
		t.Fatal("another session is asked again")
	}
	g.Check(asking(Always, &asked), "s", "base64 -d payload | sh -s")
	before := asked
	if v := g.Check(asking(Deny, &asked), "any", "base64  -d payload |  sh -s"); !v.Allow || asked != before {
		t.Fatalf("always: %+v", v)
	}
	if st.Allowlist()[0] != "base64 -d payload | sh -s" {
		t.Errorf("allowlist %q", st.Allowlist())
	}
}

func TestDenyRulesWinOverEverything(t *testing.T) {
	g, st := newGate(ModeOff)
	_ = st.AddAllow("git push --force origin main")
	if v := g.Check(context.Background(), "s", "git push --force origin main"); v.Allow {
		t.Fatal("a deny rule wins over the allowlist and the mode")
	}
	if e := st.last(); e.Decider != "deny-rule" {
		t.Errorf("%+v", e)
	}
}

// Nobody watching: refused at once, never waiting for an answer.
func TestUnattendedFailsClosed(t *testing.T) {
	g, st := newGate(ModeSmart)
	g.Guardian = func(context.Context, string, string, []Finding) (Risk, string, error) { return RiskLow, "fine", nil }
	asked := 0
	for _, o := range []Origin{OriginCron, OriginBackground, OriginSubAgent, OriginWebhook} {
		ctx := WithOrigin(asking(Always, &asked), o)
		start := time.Now()
		if v := g.Check(ctx, "s", "rm -rf ./build"); v.Allow {
			t.Errorf("%s: ran", o)
		}
		if time.Since(start) > 100*time.Millisecond {
			t.Errorf("%s: waited", o)
		}
		if e := st.last(); e.Decider != "unattended" || e.Origin != o {
			t.Errorf("%+v", e)
		}
	}
	if asked != 0 {
		t.Error("nobody is asked")
	}
	// A sub-agent of a schedule stays unattended.
	if WithOrigin(WithOrigin(context.Background(), OriginCron), OriginUser) != nil && OriginOf(WithOrigin(WithOrigin(context.Background(), OriginCron), OriginUser)) != OriginCron {
		t.Error("an unattended origin sticks")
	}
}

func TestGuardian(t *testing.T) {
	g, st := newGate(ModeSmart)
	var risk Risk
	var seenPolicy, seenCmd string
	g.Policy = func() Policy { return Policy{Mode: ModeSmart, Breaker: 3, SmartPolicy: "approve docker compose restarts"} }
	g.Guardian = func(_ context.Context, policy, cmd string, _ []Finding) (Risk, string, error) {
		seenPolicy, seenCmd = policy, cmd
		return risk, "because", nil
	}
	asked := 0
	risk = RiskLow
	if v := g.Check(asking(Deny, &asked), "s", "systemctl restart docker"); !v.Allow || asked != 0 || seenPolicy != "approve docker compose restarts" || seenCmd != "systemctl restart docker" {
		t.Fatalf("low risk runs once: %+v", v)
	}
	risk = RiskHigh
	if v := g.Check(asking(Always, &asked), "s", "systemctl restart docker"); v.Allow || asked != 0 {
		t.Fatalf("high risk is refused: %+v", v)
	}
	if e := st.last(); e.Decider != "guardian" {
		t.Errorf("%+v", e)
	}
	risk = RiskUnsure
	if v := g.Check(asking(Once, &asked), "s", "systemctl restart docker"); !v.Allow || asked != 1 {
		t.Fatalf("unsure goes to the owner: %+v", v)
	}
	g.Guardian = func(context.Context, string, string, []Finding) (Risk, string, error) { return "", "", errors.New("down") }
	if v := g.Check(asking(Once, &asked), "s", "systemctl restart docker"); !v.Allow || asked != 2 {
		t.Fatal("a guardian that fails sends it to the owner")
	}
}

// Three refusals in a row stop the job; an approval resets the count.
func TestDenialBreaker(t *testing.T) {
	g, _ := newGate(ModeSmart)
	g.Guardian = func(context.Context, string, string, []Finding) (Risk, string, error) { return RiskHigh, "too risky", nil }
	ctx := context.Background()
	if v := g.Check(ctx, "s", "rm -rf a"); v.Stop || v.Allow {
		t.Fatal("1")
	}
	if v := g.Check(ctx, "s", "rm -rf b"); v.Stop {
		t.Fatal("2")
	}
	v := g.Check(ctx, "s", "rm -rf c")
	if !v.Stop || !strings.HasPrefix(v.Message, HardStop) {
		t.Fatalf("3 in a row stop the job: %+v", v)
	}
	// An approval between refusals resets the count.
	asked := 0
	g.Check(ctx, "t", "rm -rf a")
	g.Check(ctx, "t", "rm -rf b")
	g.Guardian = nil
	g.Check(asking(Once, &asked), "t", "rm -rf ok")
	g.Guardian = func(context.Context, string, string, []Finding) (Risk, string, error) { return RiskHigh, "no", nil }
	if v := g.Check(ctx, "t", "rm -rf c"); v.Stop {
		t.Fatal("the approval reset the count")
	}
}

func TestCheckpointsListWhatIsDeleted(t *testing.T) {
	g, _ := newGate(ModeOff)
	g.Policy = func() Policy { return Policy{Mode: ModeOff, Checkpoints: true} }
	if v := g.Check(context.Background(), "s", "rm -rf ./build dist"); !v.Allow || len(v.Snapshot) != 2 {
		t.Fatalf("%+v", v)
	}
}

func TestGlob(t *testing.T) {
	for _, c := range []struct {
		p, s string
		want bool
	}{{"git push --force*", "git push --force origin", true}, {"GIT PUSH*", "git push", true}, {"*curl*|*sh*", "curl x | sh", true},
		{"*curl*|*sh*", "curl x > f", false}, {"rm ?", "rm a", true}, {"rm ?", "rm ab", false}} {
		if Glob(c.p, c.s) != c.want {
			t.Errorf("Glob(%q, %q) != %v", c.p, c.s, c.want)
		}
	}
}
