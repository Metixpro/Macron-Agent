package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Metixpro/macron-agent/pkg/cmdgate"
	"github.com/Metixpro/macron-agent/pkg/config"
	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/telegram"
)

const (
	settingGateMode        = cmdgate.SettingMode
	settingGateBreaker     = cmdgate.SettingBreaker
	settingGateCheckpoints = cmdgate.SettingCheckpoints

	gateYoloFor    = 30 * time.Minute
	gateAuditShown = 8
)

var gateAskTimeout = 10 * time.Minute

func (b *Bot) newCommandGate() *cmdgate.Gate { return b.commandGate(b.app.Machine.BaseDir) }

func (b *Bot) commandGate(workDir string) *cmdgate.Gate {
	env := cmdgate.Env{
		SudoPassword: os.Getenv("SUDO_PASSWORD") != "",
		SelfNames:    []string{"macron", config.ServiceName()},
		SelfDB:       []string{config.HomePath(config.FileDB)},
		SelfPID:      os.Getpid(),
		SelfHome:     config.Home(),
		SelfSecrets:  config.SecretFiles(),
		WorkDir:      workDir,
	}
	env.UserHome, _ = os.UserHomeDir()
	g := cmdgate.New(env, os.Getenv("MACRON_YOLO") == "1")
	g.Policy = b.gatePolicy
	g.Store = &gateStore{b: b}
	g.Guardian = cmdgate.NewGuardian(b.guardianCall)
	return g
}

// gateSendFile asks before send_file sends the chat a file from outside the session's workspace (a key, a config
// file...); workspace files need nothing. It returns why the file may not go, or "".
func (b *Bot) gateSendFile(ctx context.Context, session, name string) string {
	g := b.app.Registry.Gate
	if g == nil || b.app.Machine.Inside(session, name) {
		return ""
	}
	abs, _, err := b.app.Machine.HostPath(session, name)
	if err != nil {
		return "[!] Not sent: " + err.Error()
	}
	if real, err := filepath.EvalSymlinks(abs); err == nil {
		abs = real
	}
	also := cmdgate.Analyze("cat -- '"+strings.ReplaceAll(abs, "'", `'\''`)+"'", g.Env())
	v := g.CheckAction(ctx, session, cmdgate.Action{Key: "[send_file] " + abs, Level: cmdgate.Dangerous, Rule: "send-outside",
		Reason: "sending the chat a file from outside the workspace", Also: also})
	if !v.Allow {
		return v.Message
	}
	return ""
}

func (b *Bot) gatePolicy() cmdgate.Policy { return cmdgate.PolicyFrom(b.st.Setting) }

func (b *Bot) gateMode() string {
	switch v, _ := b.st.Setting(settingGateMode); v {
	case cmdgate.ModeManual, cmdgate.ModeOff:
		return v
	}
	return cmdgate.ModeSmart
}

type gateStore struct {
	b     *Bot
	mu    sync.Mutex
	cache []string
	at    time.Time
}

func (s *gateStore) Allowlist() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cache == nil || time.Since(s.at) > time.Minute {
		ctx, cancel := s.b.ctx(5 * time.Second)
		defer cancel()
		list, err := s.b.st.CmdAllowlist(ctx)
		if err != nil {
			return s.cache
		}
		s.cache, s.at = append([]string{}, list...), time.Now()
	}
	return s.cache
}

func (s *gateStore) AddAllow(p string) error {
	ctx, cancel := s.b.ctx(5 * time.Second)
	defer cancel()
	err := s.b.st.AddCmdAllow(ctx, p)
	s.forget()
	return err
}

func (s *gateStore) forget() {
	s.mu.Lock()
	s.cache = nil
	s.mu.Unlock()
}

func (s *gateStore) Audit(e cmdgate.Entry) {
	ctx, cancel := s.b.ctx(5 * time.Second)
	defer cancel()
	if err := s.b.st.AddCmdAudit(ctx, store.CmdAudit{At: e.At, Session: e.Session, Origin: string(e.Origin), Command: e.Command,
		Decision: e.Decision, Decider: e.Decider, Rule: e.Rule}); err != nil {
		log.Printf("gate audit: %v", err)
	}
}

// guardianCall asks the configured model, as the smart-mode guardian.
func (b *Bot) guardianCall(ctx context.Context, system, user string) (string, error) {
	resp, err := b.prov.Complete(ctx, llm.Request{Messages: []llm.Message{{Role: "system", Content: system},
		{Role: "user", Content: user}}, MaxTokens: 200})
	if err != nil {
		return "", err
	}
	return resp.Message.Content, nil
}

type gateAsk struct {
	id     int64
	chatID int64
	msgID  int64
	owner  int64
	answer chan cmdgate.Choice
	once   sync.Once
}

var gateAskSeq atomic.Int64

func (b *Bot) gateAsker(uid, chatID int64) cmdgate.Asker {
	if !b.isOwner(uid) {
		return nil
	}
	return func(ctx context.Context, q cmdgate.Question) cmdgate.Choice {
		l := b.lang(uid)
		a := &gateAsk{id: gateAskSeq.Add(1), chatID: chatID, owner: uid, answer: make(chan cmdgate.Choice, 1)}
		b.gateAsks.Store(a.id, a)
		defer b.gateAsks.Delete(a.id)
		var why []string
		for _, f := range q.Findings {
			why = append(why, f.Reason)
		}
		html := l.f("[!] <b>تأیید دستور</b> — %s\n<pre>%s</pre>", esc(strings.Join(why, l.t("، "))), esc(shortText(q.Command, 1500)))
		if q.Guardian != "" {
			html += l.f("\n🛡 گاردین: <i>%s</i>", esc(q.Guardian))
		}
		id := strconv.FormatInt(a.id, 10)
		kb := keyboard(row(b.button(l.t("[+] یکبار"), "cg:"+id+":once", telegram.StyleSuccess), b.button(l.t("[+] این سشن"), "cg:"+id+":session", "")),
			row(b.button(l.t("[+] همیشه"), "cg:"+id+":always", ""), b.button(l.t("[-] رد"), "cg:"+id+":deny", telegram.StyleDanger)))
		sctx, cancel := b.ctx(15 * time.Second)
		sent := b.sendHTML(sctx, chatID, html, telegram.SendOptions{Markup: kb})
		cancel()
		if sent == nil {
			return cmdgate.Deny
		}
		a.msgID = sent.MessageID
		choice := cmdgate.Deny
		t := time.NewTimer(gateAskTimeout)
		defer t.Stop()
		select {
		case choice = <-a.answer:
		case <-t.C:
		case <-ctx.Done():
		}

		dctx, dcancel := b.ctx(15 * time.Second)
		defer dcancel()
		_ = b.api.DeleteMessage(dctx, chatID, a.msgID)
		b.sendHTML(dctx, chatID, l.f("[!] %s: <code>%s</code>", gateChoiceName(l, choice), esc(shortText(q.Command, 120))), telegram.SendOptions{})
		return choice
	}
}

func gateChoiceName(l lang, c cmdgate.Choice) string {
	switch c {
	case cmdgate.Once:
		return l.t("یکبار تأیید شد")
	case cmdgate.Session:
		return l.t("برای این سشن تأیید شد")
	case cmdgate.Always:
		return l.t("برای همیشه تأیید شد")
	}
	return l.t("رد شد")
}

func (b *Bot) handleGateCallback(q *telegram.CallbackQuery, answer func(string, bool)) {
	parts := strings.Split(q.Data, ":")
	if len(parts) != 3 {
		answer("", false)
		return
	}
	id, _ := strconv.ParseInt(parts[1], 10, 64)
	v, ok := b.gateAsks.Load(id)
	if !ok {
		answer(b.langOf(&q.From).t("این سؤال دیگه منتظر جواب نیست"), true)
		return
	}
	a := v.(*gateAsk)
	if q.From.ID != a.owner {
		answer("", false)
		return
	}
	b.resolveGate(a, cmdgate.Choice(parts[2]))
	answer("", false)
}

func (b *Bot) resolveGate(a *gateAsk, c cmdgate.Choice) {
	switch c {
	case cmdgate.Once, cmdgate.Session, cmdgate.Always:
	default:
		c = cmdgate.Deny
	}
	a.once.Do(func() { a.answer <- c })
}

func (b *Bot) approveLatest(uid int64) bool {
	var latest *gateAsk
	b.gateAsks.Range(func(_, v any) bool {
		if a := v.(*gateAsk); a.owner == uid && (latest == nil || a.id > latest.id) {
			latest = a
		}
		return true
	})
	if latest == nil {
		return false
	}
	b.resolveGate(latest, cmdgate.Once)
	return true
}

func (b *Bot) yoloCommand(m *telegram.Message, l lang, args string) {
	g := b.app.Registry.Gate
	if g == nil {
		b.reply(m, l.t(textUnknownCommand))
		return
	}
	if strings.TrimSpace(args) == "off" {
		g.SetYolo(0)
		b.reply(m, l.t("[+] گیت دستورات دوباره روشنه."))
		return
	}
	if on, until := g.Yolo(); on && until.IsZero() {
		b.reply(m, l.t("[!] گیت با MACRON_YOLO=1 از شروع خاموشه؛ از اینجا عوض نمی‌شه."))
		return
	}
	g.SetYolo(gateYoloFor)
	b.reply(m, l.f("[!] <b>هشدار:</b> گیت دستورات تا %s خاموشه: دستورهای پرخطر بدون پرسیدن اجرا می‌شن. هاردلاین (مثل <code>rm -rf /</code>) و قواعد deny همچنان بلاکن. <code>/yolo off</code> برش می‌گردونه.",
		time.Now().Add(gateYoloFor).In(b.loc).Format("15:04")))
}

func gateModeName(l lang, mode string) string {
	switch mode {
	case cmdgate.ModeManual:
		return l.t("دستی (همیشه بپرس)")
	case cmdgate.ModeOff:
		return l.t("خاموش (فقط هاردلاین)")
	}
	return l.t("هوشمند (گاردین)")
}

var gateBreakerSteps = map[int]int{3: 5, 5: 0, 0: 3}

func (b *Bot) viewGate(ctx context.Context, l lang) panelView {
	var sb strings.Builder
	sb.WriteString(l.t("🛡 <b>گیت دستورات</b>\n\nهر دستوری که ایجنت روی همین سرور اجرا می‌کنه اول از گیت رد می‌شه. هاردلاین (مثل <code>rm -rf /</code>، <code>mkfs</code>، خاموش کردن سرور یا خود مکرون) هیچ‌وقت اجرا نمی‌شه، حتی با حالت خاموش یا /yolo. کارهای زمان‌بندی‌شده، پس‌زمینه و زیرایجنت‌ها کسی رو ندارن که ازش بپرسن، پس دستور پرخطرشون رد می‌شه.\n"))
	fmt.Fprint(&sb, l.f("\n⚙️ حالت: <b>%s</b>", gateModeName(l, b.gateMode())))
	if g := b.app.Registry.Gate; g != nil {
		if on, until := g.Yolo(); on {
			if until.IsZero() {
				sb.WriteString(l.t("\n[!] yolo از متغیر محیطی"))
			} else {
				fmt.Fprint(&sb, l.f("\n[!] yolo تا %s", until.In(b.loc).Format("15:04")))
			}
		}
	}
	if n := b.gatePolicy().Breaker; n > 0 {
		fmt.Fprint(&sb, l.f("\n⛔ توقف بعد از %s رد پشت‌سرهم", l.num(n)))
	} else {
		sb.WriteString(l.t("\n⛔ توقف بعد از ردهای پشت‌سرهم: خاموش"))
	}
	fmt.Fprint(&sb, l.f("\n📸 چک‌پوینت قبل از حذف: %s", onOff(l, b.st.SettingBool(settingGateCheckpoints, false))))
	if p := b.gatePolicy(); len(p.Deny) > 0 || p.SmartPolicy != "" {
		fmt.Fprint(&sb, l.f("\n🚫 قواعد deny: %s · 📜 سیاست گاردین: %s", l.num(len(nonEmpty(p.Deny))), onOff(l, p.SmartPolicy != "")))
	}
	sb.WriteString(l.t("\n<i>قواعد deny و سیاست گاردین: <code>macron config set approvals.deny …</code> و <code>approvals.smart_policy</code></i>"))
	allow, _ := b.st.CmdAllowlist(ctx)
	var rows [][]telegram.InlineKeyboardButton
	if len(allow) > 0 {
		sb.WriteString(l.t("\n\n<b>همیشه مجاز</b> (برای ابطال بزن)"))
		for i, a := range allow {
			if i >= 10 {
				break
			}
			fmt.Fprintf(&sb, "\n%d. <code>%s</code>", i+1, esc(shortText(a, 80)))
			rows = append(rows, row(b.button(fmt.Sprintf("❌ %d. %s", i+1, shortText(a, 30)), fmt.Sprintf("a:gate:rm:%d", i), "")))
		}
	}
	if log, err := b.st.CmdAudits(ctx, gateAuditShown); err == nil && len(log) > 0 {
		sb.WriteString(l.t("\n\n<b>آخرین تصمیم‌ها</b>\n<pre>"))
		for _, e := range log {
			mark := map[string]string{"allowed": "[+]", "denied": "[-]", "hardline": "[!]"}[e.Decision]
			fmt.Fprintf(&sb, "%s %s %-8s %-10s %s\n", mark, e.At.In(b.loc).Format("01-02 15:04"), e.Origin, e.Decider, esc(shortText(e.Command, 50)))
		}
		sb.WriteString("</pre>")
	}
	var modes []telegram.InlineKeyboardButton
	for _, m := range []string{cmdgate.ModeSmart, cmdgate.ModeManual, cmdgate.ModeOff} {
		label := gateModeName(l, m)
		if m == b.gateMode() {
			label = "✅ " + label
		}
		modes = append(modes, b.button(label, "a:gate:mode:"+m, ""))
	}
	rows = append([][]telegram.InlineKeyboardButton{modes[:2], modes[2:],
		row(b.button(l.t("⛔ آستانه توقف"), "a:gate:breaker", ""), b.button(l.t("📸 چک‌پوینت"), "a:gate:ckpt", ""))}, rows...)
	rows = append(rows, row(b.back(l, "a:ai")))
	return panelView{html: sb.String(), kb: keyboard(rows...)}
}

func nonEmpty(xs []string) []string {
	var out []string
	for _, x := range xs {
		if strings.TrimSpace(x) != "" {
			out = append(out, x)
		}
	}
	return out
}

func (b *Bot) handleGatePanel(ctx context.Context, l lang, parts []string, answer func(string, bool), show func(panelView)) {
	arg := func(i int) string {
		if len(parts) > i {
			return parts[i]
		}
		return ""
	}
	switch arg(2) {
	case "mode":
		if m := arg(3); m == cmdgate.ModeSmart || m == cmdgate.ModeManual || m == cmdgate.ModeOff {
			_ = b.st.SetSetting(ctx, settingGateMode, m)
		}
	case "breaker":
		next, ok := gateBreakerSteps[b.gatePolicy().Breaker]
		if !ok {
			next = 3
		}
		_ = b.st.SetSetting(ctx, settingGateBreaker, strconv.Itoa(next))
	case "ckpt":
		_ = b.st.SetSetting(ctx, settingGateCheckpoints, strconv.FormatBool(!b.st.SettingBool(settingGateCheckpoints, false)))
	case "rm":
		i, _ := strconv.Atoi(arg(3))
		if allow, err := b.st.CmdAllowlist(ctx); err == nil && i >= 0 && i < len(allow) {
			_ = b.st.RemoveCmdAllow(ctx, allow[i])
			if g := b.app.Registry.Gate; g != nil {
				if s, ok := g.Store.(*gateStore); ok {
					s.forget()
				}
			}
		}
	}
	answer("", false)
	show(b.viewGate(ctx, l))
}
