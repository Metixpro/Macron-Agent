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

// The decision, in this order:
//
//  1. HARDLINE (and the agent's own floor): never runs, in any mode, from
//     anywhere (the owner's chat, a schedule, a sub-agent, yolo).
//  2. The owner's deny rules (globs, case-insensitive): never run either.
//  3. The permanent allowlist: runs without asking.
//  4. A command no rule flags runs.
//  5. A dangerous one: with the gate off (or /yolo) it runs; approved for
//     this session it runs; in a job nobody watches (a schedule, a
//     background task, a sub-agent, a webhook) it is refused at once;
//     otherwise the guardian model judges it (smart mode): low risk runs
//     this once, high risk is refused, unsure goes to the owner, who
//     chooses once, this session, always, or no.
//  6. After a few refusals in a row the job is stopped (the denial
//     breaker): it reports what was blocked and asks the owner to run it by
//     hand. Any approval resets the count.

// Modes.
const (
	ModeSmart  = "smart"  // the guardian judges; unsure goes to the owner
	ModeManual = "manual" // the owner is asked for every dangerous command
	ModeOff    = "off"    // dangerous commands run; the floor still holds
)

// HardStop starts a tool result that must end the job (agent.runTool).
const HardStop = "[hard stop] "

// Origin is who a command runs for.
type Origin string

const (
	OriginUser       Origin = "user"
	OriginCron       Origin = "cron"
	OriginBackground Origin = "background"
	OriginSubAgent   Origin = "subagent"
	OriginWebhook    Origin = "webhook"
)

// Unattended: nobody can answer a question there.
func (o Origin) Unattended() bool { return o != OriginUser && o != "" }

type originKey struct{}

// WithOrigin marks who the commands of a context run for. An origin that
// is already unattended is never made attended again (a sub-agent of a
// schedule is still a schedule).
func WithOrigin(ctx context.Context, o Origin) context.Context {
	if cur := OriginOf(ctx); cur.Unattended() {
		return ctx
	}
	return context.WithValue(ctx, originKey{}, o)
}

// OriginOf is who the commands of a context run for (the user by default).
func OriginOf(ctx context.Context) Origin {
	if o, ok := ctx.Value(originKey{}).(Origin); ok {
		return o
	}
	return OriginUser
}

// Choice is the owner's answer.
type Choice string

const (
	Once    Choice = "once"
	Session Choice = "session"
	Always  Choice = "always"
	Deny    Choice = "deny"
)

// Question is what the owner is asked.
type Question struct {
	Command  string
	Findings []Finding
	Guardian string // what the guardian said, if it was asked
}

// Asker asks the owner and waits for the answer (Deny when it times out).
type Asker func(ctx context.Context, q Question) Choice

type askerKey struct{}

// WithAsker gives a context someone to ask: the owner's chat.
func WithAsker(ctx context.Context, a Asker) context.Context {
	return context.WithValue(ctx, askerKey{}, a)
}

func askerOf(ctx context.Context) Asker {
	a, _ := ctx.Value(askerKey{}).(Asker)
	return a
}

// Risk is the guardian's judgment.
type Risk string

const (
	RiskLow    Risk = "low"
	RiskHigh   Risk = "high"
	RiskUnsure Risk = "unsure"
)

// Guardian judges a dangerous command with a helper model. policy is the
// owner's own text, for the guardian's system prompt only: never beside the
// command, which is untrusted.
type Guardian func(ctx context.Context, policy, command string, findings []Finding) (Risk, string, error)

// Policy is the owner's configuration, read at each check.
type Policy struct {
	Mode        string
	Breaker     int      // refusals in a row that stop the job (0: never)
	Deny        []string // globs that never run
	SmartPolicy string
	Checkpoints bool // snapshot what a destructive command deletes
}

// Entry is one decision, for the audit log.
type Entry struct {
	At       time.Time
	Session  string
	Origin   Origin
	Command  string
	Decision string // allowed, denied, hardline
	Decider  string // floor, deny-rule, allowlist, mode-off, yolo, session, unattended, guardian, owner, nobody
	Rule     string
}

// Store keeps the permanent allowlist and the audit log.
type Store interface {
	Allowlist() []string
	AddAllow(pattern string) error
	Audit(e Entry)
}

// Verdict is the gate's answer.
type Verdict struct {
	Allow    bool
	Message  string   // for the model when it does not run
	Stop     bool     // the breaker tripped: the job must stop
	Snapshot []string // paths to keep a checkpoint of before it runs
	Findings []Finding
}

// Gate decides. Its Env and its yolo are fixed when it is made: reading the
// environment at each call would let any tool in the process turn the gate
// off (YOLO_MODE=1) from inside a job.
type Gate struct {
	env      Env
	yoloBoot bool
	Policy   func() Policy
	Store    Store
	Guardian Guardian

	yoloUntil atomic.Int64 // unix nano: /yolo from the owner's chat
	mu        sync.Mutex
	session   map[string]map[string]bool
	denials   map[string]int
}

// New makes a gate. yolo comes from the environment at start only.
func New(env Env, yolo bool) *Gate {
	return &Gate{env: env, yoloBoot: yolo, session: map[string]map[string]bool{}, denials: map[string]int{}}
}

// Env is what the gate was made with.
func (g *Gate) Env() Env { return g.env }

// SetYolo turns the gate off for a while (the owner's /yolo); 0 turns it back on.
func (g *Gate) SetYolo(d time.Duration) {
	if d <= 0 {
		g.yoloUntil.Store(0)
		return
	}
	g.yoloUntil.Store(time.Now().Add(d).UnixNano())
}

// Yolo reports whether dangerous commands run without asking (the floor holds).
func (g *Gate) Yolo() (bool, time.Time) {
	if g.yoloBoot {
		return true, time.Time{}
	}
	if u := g.yoloUntil.Load(); u > 0 && time.Now().UnixNano() < u {
		return true, time.Unix(0, u)
	}
	return false, time.Time{}
}

// Normal is a command as the allowlist and the deny rules see it.
func Normal(cmd string) string { return strings.Join(strings.Fields(Clean(cmd)), " ") }

// Glob matches * (anything, / included) and ?, case-insensitive.
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

// Check decides whether a command may run.
func (g *Gate) Check(ctx context.Context, sessionID, command string) Verdict {
	norm := Normal(command)
	origin := OriginOf(ctx)
	findings := Analyze(command, g.env)
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
		// Nobody can answer: refused, never «no answer means yes».
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

// refuse counts a refusal; at the breaker's threshold the job stops.
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

// ForgetSession drops a session's approvals and refusals (a new conversation).
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
