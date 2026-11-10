package cmdgate

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	ModeSmart  = "smart"
	ModeManual = "manual"
	ModeOff    = "off"
)

const HardStop = "[hard stop] "

type Origin string

const (
	OriginUser       Origin = "user"
	OriginCron       Origin = "cron"
	OriginBackground Origin = "background"
	OriginSubAgent   Origin = "subagent"
	OriginWebhook    Origin = "webhook"
)

func (o Origin) Unattended() bool { return o != OriginUser && o != "" }

type originKey struct{}

func WithOrigin(ctx context.Context, o Origin) context.Context {
	if cur := OriginOf(ctx); cur.Unattended() {
		return ctx
	}
	return context.WithValue(ctx, originKey{}, o)
}

func OriginOf(ctx context.Context) Origin {
	if o, ok := ctx.Value(originKey{}).(Origin); ok {
		return o
	}
	return OriginUser
}

type Choice string

const (
	Once    Choice = "once"
	Session Choice = "session"
	Always  Choice = "always"
	Deny    Choice = "deny"
)

type Question struct {
	Command  string
	Findings []Finding
	Guardian string
}

type Asker func(ctx context.Context, q Question) Choice

type askerKey struct{}

func WithAsker(ctx context.Context, a Asker) context.Context {
	return context.WithValue(ctx, askerKey{}, a)
}

func askerOf(ctx context.Context) Asker {
	a, _ := ctx.Value(askerKey{}).(Asker)
	return a
}

type Risk string

const (
	RiskLow    Risk = "low"
	RiskHigh   Risk = "high"
	RiskUnsure Risk = "unsure"
)

type Guardian func(ctx context.Context, policy, command string, findings []Finding) (Risk, string, error)

type Policy struct {
	Mode        string
	Breaker     int
	Deny        []string
	SmartPolicy string
	Checkpoints bool
}

type Entry struct {
	At       time.Time
	Session  string
	Origin   Origin
	Command  string
	Decision string
	Decider  string
	Rule     string
}

type Store interface {
	Allowlist() []string
	AddAllow(pattern string) error
	Audit(e Entry)
}

type Verdict struct {
	Allow    bool
	Message  string
	Stop     bool
	Snapshot []string
	Findings []Finding
}

type Gate struct {
	env      Env
	yoloBoot bool
	Policy   func() Policy
	Store    Store
	Guardian Guardian

	yoloUntil atomic.Int64
	mu        sync.Mutex
	session   map[string]map[string]bool
	denials   map[string]int
}

func New(env Env, yolo bool) *Gate {
	return &Gate{env: env, yoloBoot: yolo, session: map[string]map[string]bool{}, denials: map[string]int{}}
}

func (g *Gate) Env() Env { return g.env }

func (g *Gate) SetYolo(d time.Duration) {
	if d <= 0 {
		g.yoloUntil.Store(0)
		return
	}
	g.yoloUntil.Store(time.Now().Add(d).UnixNano())
}

func (g *Gate) Yolo() (bool, time.Time) {
	if g.yoloBoot {
		return true, time.Time{}
	}
	if u := g.yoloUntil.Load(); u > 0 && time.Now().UnixNano() < u {
		return true, time.Unix(0, u)
	}
	return false, time.Time{}
}

func Normal(cmd string) string { return strings.Join(strings.Fields(Clean(cmd)), " ") }

func Glob(pattern, s string) bool {
	p, t := []rune(strings.ToLower(pattern)), []rune(strings.ToLower(s))
	var match func(i, j int) bool
	match = func(i, j int) bool {
		for i < len(p) {
			switch p[i] {
			case '*':
				for k := j; k <= len(t); k++ {
					if match(i+1, k) {
						return true
					}
				}
				return false
			case '?':
				if j >= len(t) {
					return false
				}
			default:
				if j >= len(t) || p[i] != t[j] {
					return false
				}
			}
			i++
			j++
		}
		return j == len(t)
	}
	return match(0, 0)
}

func (g *Gate) policy() Policy {
	p := Policy{Mode: ModeSmart, Breaker: 3}
	if g.Policy != nil {
		p = g.Policy()
	}
	switch p.Mode {
	case ModeSmart, ModeManual, ModeOff:
	default:
		p.Mode = ModeSmart
	}
	return p
}

func (g *Gate) audit(sessionID string, o Origin, cmd, decision, decider, rule string) {
	if g.Store != nil {
		g.Store.Audit(Entry{At: time.Now(), Session: sessionID, Origin: o, Command: cmd, Decision: decision, Decider: decider, Rule: rule})
	}
	log.Printf("🛡 gate: %s by %s (%s, %s): %s", decision, decider, o, rule, shorten(cmd, 160))
}

func (g *Gate) Check(ctx context.Context, sessionID, command string) Verdict {
	return g.decide(ctx, sessionID, command, Analyze(command, g.env))
}

// CheckCode is Check for a program in another language (run_code): only what
// AnalyzeCode can see is judged, and the owner is shown a short piece of the code.
func (g *Gate) CheckCode(ctx context.Context, sessionID, language, code string) Verdict {
	findings := AnalyzeCode(code, g.env)
	if len(findings) == 0 {
		return Verdict{Allow: true}
	}
	return g.decide(ctx, sessionID, "["+language+" code] "+shorten(code, 600), findings)
}

func (g *Gate) decide(ctx context.Context, sessionID, command string, findings []Finding) Verdict {
	norm := Normal(command)
	origin := OriginOf(ctx)
	worst := Worst(findings)
	p := g.policy()
	rule := ""
	if len(findings) > 0 {
		rule = findings[0].Rule
	}

	if worst == Hardline {
		g.audit(sessionID, origin, norm, "hardline", "floor", rule)
		return g.refuse(sessionID, p, findings, fmt.Sprintf("[!] Blocked, and it never runs, in any mode: %s. Do not try another way to do "+
			"the same; if it is really needed, tell the owner what and why, and they can run it by hand.", findings[0].Reason))
	}
	for _, d := range p.Deny {
		if d = strings.TrimSpace(d); d != "" && Glob(d, norm) {
			g.audit(sessionID, origin, norm, "denied", "deny-rule", d)
			return g.refuse(sessionID, p, findings, "[!] Blocked by the owner's rule «"+d+"». Do not run it another way; tell the owner if it is needed.")
		}
	}
	if g.Store != nil {
		for _, a := range g.Store.Allowlist() {
			if a == norm || strings.ContainsAny(a, "*?") && Glob(a, norm) {
				if worst > Safe {
					g.audit(sessionID, origin, norm, "allowed", "allowlist", rule)
				}
				return g.allow(sessionID, p, findings)
			}
		}
	}
	if worst == Safe {
		return Verdict{Allow: true}
	}

	if yolo, _ := g.Yolo(); yolo || p.Mode == ModeOff {
		decider := "mode-off"
		if yolo {
			decider = "yolo"
		}
		g.audit(sessionID, origin, norm, "allowed", decider, rule)
		return g.allow(sessionID, p, findings)
	}
	g.mu.Lock()
	approved := g.session[sessionID][norm]
	g.mu.Unlock()
	if approved {
		g.audit(sessionID, origin, norm, "allowed", "session", rule)
		return g.allow(sessionID, p, findings)
	}
	if origin.Unattended() {

		g.audit(sessionID, origin, norm, "denied", "unattended", rule)
		return g.refuse(sessionID, p, findings, fmt.Sprintf("[!] Not run: %s, and this job runs without anyone to approve it (%s). "+
			"Report it in your answer so the owner can run it or approve it later.", findings[0].Reason, origin))
	}

	guardianSaid := ""
	if p.Mode == ModeSmart && g.Guardian != nil {
		risk, why, err := g.Guardian(ctx, p.SmartPolicy, command, findings)
		switch {
		case err != nil:
			guardianSaid = "the guardian could not judge it: " + shorten(err.Error(), 120)
		case risk == RiskLow:
			g.audit(sessionID, origin, norm, "allowed", "guardian", rule+": "+shorten(why, 200))
			return g.allow(sessionID, p, findings)
		case risk == RiskHigh:
			g.audit(sessionID, origin, norm, "denied", "guardian", rule+": "+shorten(why, 200))
			return g.refuse(sessionID, p, findings, "[!] Refused by the guardian: "+why+". Find a safer way, or tell the owner what you need and why.")
		default:
			guardianSaid = why
		}
	}
	ask := askerOf(ctx)
	if ask == nil {
		g.audit(sessionID, origin, norm, "denied", "nobody", rule)
		return g.refuse(sessionID, p, findings, fmt.Sprintf("[!] Not run: %s, and there is nobody to approve it here.", findings[0].Reason))
	}
	switch ask(ctx, Question{Command: command, Findings: findings, Guardian: guardianSaid}) {
	case Once:
		g.audit(sessionID, origin, norm, "allowed", "owner:once", rule)
	case Session:
		g.mu.Lock()
		if g.session[sessionID] == nil {
			g.session[sessionID] = map[string]bool{}
		}
		g.session[sessionID][norm] = true
		g.mu.Unlock()
		g.audit(sessionID, origin, norm, "allowed", "owner:session", rule)
	case Always:
		if g.Store != nil {
			if err := g.Store.AddAllow(norm); err != nil {
				log.Printf("gate: keeping %q: %v", norm, err)
			}
		}
		g.audit(sessionID, origin, norm, "allowed", "owner:always", rule)
	default:
		g.audit(sessionID, origin, norm, "denied", "owner", rule)
		return g.refuse(sessionID, p, findings, "[!] The owner said no. Do not run it another way; carry on without it or ask them what they prefer.")
	}
	return g.allow(sessionID, p, findings)
}

func (g *Gate) allow(sessionID string, p Policy, f []Finding) Verdict {
	g.mu.Lock()
	delete(g.denials, sessionID)
	g.mu.Unlock()
	v := Verdict{Allow: true, Findings: f}
	if p.Checkpoints {
		for _, x := range f {
			if x.Rule == "rm-recursive" || x.Rule == "rm-root-path" {
				v.Snapshot = append(v.Snapshot, x.Paths...)
			}
		}
	}
	return v
}

func (g *Gate) refuse(sessionID string, p Policy, f []Finding, msg string) Verdict {
	g.mu.Lock()
	g.denials[sessionID]++
	n := g.denials[sessionID]
	if p.Breaker > 0 && n >= p.Breaker {
		delete(g.denials, sessionID)
	}
	g.mu.Unlock()
	v := Verdict{Message: msg, Findings: f}
	if p.Breaker > 0 && n >= p.Breaker {
		v.Stop = true
		v.Message = HardStop + fmt.Sprintf("%d commands in a row were refused, so this job stops here. %s Tell the owner exactly "+
			"which commands were blocked and why they were needed, so they can run them by hand; do not try again.", n, msg)
	}
	return v
}

func (g *Gate) ForgetSession(sessionID string) {
	g.mu.Lock()
	delete(g.session, sessionID)
	delete(g.denials, sessionID)
	g.mu.Unlock()
}

func shorten(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}
