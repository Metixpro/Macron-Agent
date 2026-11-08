package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/moa"
	"github.com/Metixpro/macron-agent/pkg/providers"
	"github.com/Metixpro/macron-agent/pkg/telegram"
)

// Multi-model voting (pkg/moa): presets of advisors and an aggregator, kept
// in moa.presets (JSON, also «macron config set moa.presets …»). /moa runs
// one request with the default preset and goes back to the usual model;
// moa.active sends every request of the owner through a preset (the panel's
// «⭐ every request»). The whole run is billed to the aggregator's provider.

const (
	settingMoaPresets = "moa.presets"
	settingMoaPrivacy = "moa.privacy_filter" // the default of the presets that set none
	settingMoaActive  = "moa.active"         // a preset every owner request goes through ("" none)
	settingMoaDefault = "moa.default"        // the preset of /moa ("" the first)
)

func (b *Bot) moaPresets() []moa.Preset {
	raw, _ := b.st.Setting(settingMoaPresets)
	list, _ := moa.ParsePresets(raw)
	return list
}

func (b *Bot) saveMoaPresets(ctx context.Context, list []moa.Preset) error {
	raw, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return b.st.SetSetting(ctx, settingMoaPresets, string(raw))
}

// moaProvider resolves a slot's provider by its name or id.
func (b *Bot) moaProvider(name string) (providers.Snapshot, bool) {
	id, _ := strconv.ParseInt(name, 10, 64)
	for _, p := range b.prov.Snapshot() {
		if strings.EqualFold(p.Name, name) || id > 0 && p.ID == id {
			return p, true
		}
	}
	return providers.Snapshot{}, false
}

// moaProblems checks a preset against the providers (macron doctor and the panel).
func (b *Bot) moaProblems(p moa.Preset) []string {
	return moa.Problems(p, func(name string) (bool, bool, bool) {
		if strings.EqualFold(name, "moa") {
			return true, true, true
		}
		pr, ok := b.moaProvider(name)
		return ok && pr.Enabled, pr.APIKey != "" || strings.Contains(pr.BaseURL, "127.0.0.1") || strings.Contains(pr.BaseURL, "localhost"), false
	})
}

// moaStrict is a user's session memory of the destinations that need
// alternating roles.
func (b *Bot) moaStrict(uid int64) *moa.Strict {
	v, _ := b.moaStrictBy.LoadOrStore(uid, &moa.Strict{})
	return v.(*moa.Strict)
}

// moaFor is the preset a request goes through (nil: the usual model).
func (b *Bot) moaFor(uid int64, opt promptOptions) *moa.Preset {
	name := opt.moa
	if name == "" && b.isOwner(uid) {
		name, _ = b.st.Setting(settingMoaActive)
		if name == "" {
			return nil
		}
	}
	if name == "" {
		return nil
	}
	if name == moaDefaultName {
		name, _ = b.st.Setting(settingMoaDefault)
	}
	p, ok := moa.Find(b.moaPresets(), name)
	if !ok || len(b.moaProblems(p)) > 0 {
		return nil
	}
	if p.PrivacyFilter == "" {
		p.PrivacyFilter, _ = b.st.Setting(settingMoaPrivacy)
	}
	return &p
}

// moaDefaultName asks for the default preset (/moa).
const moaDefaultName = "\x00default"

// moaClient is a preset as the request's client, with its trace in the chat.
func (b *Bot) moaClient(uid int64, p moa.Preset, chatID int64, l lang) (*moa.Client, func()) {
	tr := &moaTrace{b: b, chatID: chatID, l: l, preset: p}
	c := &moa.Client{Preset: p, Strict: b.moaStrict(uid), OnAdvice: tr.advice,
		Dest: func(s moa.Slot) string {
			if pr, ok := b.moaProvider(s.Provider); ok {
				return pr.BaseURL + "|" + pr.Model
			}
			return s.Provider
		},
		Call: func(ctx context.Context, s moa.Slot, req llm.Request) (*llm.Response, error) {
			pr, ok := b.moaProvider(s.Provider)
			if !ok {
				return nil, fmt.Errorf("no provider %q", s.Provider)
			}
			ctx = providers.Only(ctx, pr.ID)
			if s.Effort != "" {
				ctx = llm.WithEffort(ctx, s.Effort)
			}
			return b.prov.Complete(ctx, req)
		}}
	return c, tr.done
}

// moaTrace shows the advisors and the aggregation as one temporary message,
// removed once the answer is out.
type moaTrace struct {
	b      *Bot
	chatID int64
	l      lang
	preset moa.Preset
	mu     sync.Mutex
	msgID  int64
}

func (t *moaTrace) advice(advice []moa.Advice) {
	var sb strings.Builder
	fmt.Fprintf(&sb, "⚖️ <b>%s</b>\n", esc(t.preset.Name))
	for _, a := range advice {
		if a.Err != nil {
			fmt.Fprintf(&sb, "<pre>[!] %s\n%s</pre>\n", esc(a.Slot.Provider), esc(shortText(a.Err.Error(), 200)))
			continue
		}
		text := a.Text
		if t.preset.PrivacyFilter != moa.PrivacyNone {
			text = moa.Scrub(text)
		}
		fmt.Fprintf(&sb, "<pre>[+] %s\n%s</pre>\n", esc(a.Slot.Provider), esc(shortText(text, 600)))
	}
	sb.WriteString(t.l.f("[*] جمع‌بندی با <b>%s</b>…", esc(t.preset.Aggregator.Provider)))
	ctx, cancel := t.b.ctx(15 * time.Second)
	defer cancel()
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.msgID != 0 {
		_ = t.b.api.DeleteMessage(ctx, t.chatID, t.msgID)
	}
	if sent := t.b.sendHTML(ctx, t.chatID, sb.String(), telegram.SendOptions{}); sent != nil {
		t.msgID = sent.MessageID
	}
}

func (t *moaTrace) done() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.msgID == 0 {
		return
	}
	ctx, cancel := t.b.ctx(15 * time.Second)
	defer cancel()
	_ = t.b.api.DeleteMessage(ctx, t.chatID, t.msgID)
	t.msgID = 0
}

// moaCommand: «/moa TASK» runs one request through the default preset; it
// reports whether the request goes on.
func (b *Bot) moaCommand(m *telegram.Message, l lang, args string) bool {
	if strings.TrimSpace(args) == "" {
		b.reply(m, l.t("⚖️ <b>رأی‌گیری چندمدلی</b>\n<code>/moa کار سخت</code> این یه درخواست رو با پریست پیش‌فرض انجام می‌ده: چند مدل نظر می‌دن و مدل عامل جمع‌بندی و کار رو انجام می‌ده؛ بعدش همون مدل همیشگی برمی‌گرده. پریست‌ها توی /settings ← 🦾 ← ⚖️ ساخته می‌شن."))
		return false
	}
	if b.moaFor(m.From.ID, promptOptions{moa: moaDefaultName}) == nil {
		b.reply(m, l.t("[!] هنوز پریست سالمی نیست؛ از /settings ← 🦾 ← ⚖️ رأی‌گیری چندمدلی یکی بساز."))
		return false
	}
	return true
}

// ---------------------------------------------------------------- the panel (a:moa)

var moaFanouts = []string{moa.FanoutUserTurn, moa.FanoutPerIteration, "every_n:3"}
var moaPrivacies = []string{moa.PrivacyNone, moa.PrivacyDisplay, moa.PrivacyFull}
var moaEfforts = []string{"", llm.EffortNone, llm.EffortLow, llm.EffortMedium, llm.EffortHigh, llm.EffortUltra}

func next[T comparable](list []T, cur T) T {
	for i, v := range list {
		if v == cur {
			return list[(i+1)%len(list)]
		}
	}
	return list[0]
}

func moaPrivacyName(l lang, p string) string {
	switch p {
	case moa.PrivacyDisplay:
		return l.t("نمایش")
	case moa.PrivacyFull:
		return l.t("کامل")
	}
	return l.t("خاموش")
}

func moaEffortName(l lang, e string) string {
	if e == "" {
		return l.t("پیش‌فرض پرووایدر")
	}
	return e
}

func (b *Bot) viewMoa(l lang) panelView {
	list := b.moaPresets()
	active, _ := b.st.Setting(settingMoaActive)
	def, _ := b.st.Setting(settingMoaDefault)
	var sb strings.Builder
	sb.WriteString(l.t("⚖️ <b>رأی‌گیری چندمدلی</b>\n\nچند مدل (مشاور) روی همون درخواست نظر می‌دن و یه مدل (عامل) با دیدن نظرهاشون جواب رو می‌نویسه و ابزارها رو می‌زنه. مشاورها نه ابزار دارن نه پرامپت سیستم؛ فقط متن گفتگو رو می‌بینن.\n"))
	if len(list) == 0 {
		sb.WriteString(l.t("\nهنوز پریستی نیست."))
	}
	var rows [][]telegram.InlineKeyboardButton
	for i, p := range list {
		mark := "▫️"
		if !p.On() {
			mark = "⏸"
		}
		if strings.EqualFold(p.Name, active) {
			mark = "⭐"
		}
		fmt.Fprintf(&sb, "\n%s <b>%s</b>: %s ← %s · %s", mark, esc(p.Name), esc(p.Aggregator.Provider), esc(moaRefNames(p)), esc(p.FanoutName()))
		if strings.EqualFold(p.Name, def) || def == "" && i == 0 {
			sb.WriteString(l.t(" · پیش‌فرض /moa"))
		}
		if probs := b.moaProblems(p); len(probs) > 0 {
			fmt.Fprintf(&sb, "\n   [!] %s", esc(probs[0]))
		}
		rows = append(rows, row(b.button(mark+" "+p.Name, fmt.Sprintf("a:moa:p:%d", i), "")))
	}
	rows = append(rows, row(b.button(l.t("➕ پریست جدید"), "a:moa:new", telegram.StyleSuccess)), row(b.back(l, "a:ai")))
	return panelView{html: sb.String(), kb: keyboard(rows...)}
}

func moaRefNames(p moa.Preset) string {
	var n []string
	for _, r := range p.References {
		n = append(n, r.Provider)
	}
	if len(n) == 0 {
		return "—"
	}
	return strings.Join(n, ", ")
}

func (b *Bot) viewMoaPreset(l lang, i int) panelView {
	list := b.moaPresets()
	if i < 0 || i >= len(list) {
		return b.viewMoa(l)
	}
	p := list[i]
	active, _ := b.st.Setting(settingMoaActive)
	var sb strings.Builder
	fmt.Fprintf(&sb, "⚖️ <b>%s</b>\n", esc(p.Name))
	sb.WriteString(l.f("\n🤖 مدل عامل: <b>%s</b> (عمق: %s) — صورتحساب روی پرووایدر همین مدله", esc(p.Aggregator.Provider), esc(moaEffortName(l, p.Aggregator.Effort))))
	for _, r := range p.References {
		sb.WriteString(l.f("\n🗣 مشاور: %s (عمق: %s)", esc(r.Provider), esc(moaEffortName(l, r.Effort))))
	}
	sb.WriteString(l.f("\n🔁 کادنس: <code>%s</code> · 🔒 حریم خصوصی: %s · %s", esc(p.FanoutName()), moaPrivacyName(l, p.PrivacyFilter), onOff(l, p.On())))
	for _, pr := range b.moaProblems(p) {
		fmt.Fprintf(&sb, "\n[!] %s", esc(pr))
	}
	sb.WriteString(l.t("\n\n⭐ = مدل عامل · ✓ = مشاور"))
	pfx := fmt.Sprintf("a:moa:%%s:%d", i)
	act := func(a string) string { return fmt.Sprintf(pfx, a) }
	var rows [][]telegram.InlineKeyboardButton
	for _, pr := range b.prov.Snapshot() {
		agg := "☆ "
		if strings.EqualFold(pr.Name, p.Aggregator.Provider) {
			agg = "⭐ "
		}
		ref := "▫️ "
		for _, r := range p.References {
			if strings.EqualFold(r.Provider, pr.Name) {
				ref = "✓ "
			}
		}
		id := strconv.FormatInt(pr.ID, 10)
		rows = append(rows, row(b.button(agg+pr.Name, act("agg")+":"+id, ""), b.button(ref+pr.Name, act("ref")+":"+id, "")))
	}
	effs := []telegram.InlineKeyboardButton{b.button(l.t("🧠 عمق عامل"), act("eff")+":-1", "")}
	for j, r := range p.References {
		effs = append(effs, b.button("🧠 "+r.Provider, act("eff")+":"+strconv.Itoa(j), ""))
	}
	star := l.t("⭐ برای همه درخواست‌ها")
	if strings.EqualFold(p.Name, active) {
		star = l.t("✅ برای همه درخواست‌ها (بزن تا خاموش بشه)")
	}
	rows = append(rows, effs,
		row(b.button(l.t("🔁 کادنس"), act("fan"), ""), b.button(l.t("🔒 حریم خصوصی"), act("priv"), ""), b.button(l.t("⏻ روشن/خاموش"), act("on"), "")),
		row(b.button(star, act("active"), ""), b.button(l.t("📌 پیش‌فرض /moa"), act("def"), "")),
		row(b.button(l.t("🗑 حذف"), act("del"), telegram.StyleDanger), b.back(l, "a:moa")))
	return panelView{html: sb.String(), kb: keyboard(rows...)}
}

func (b *Bot) handleMoaPanel(ctx context.Context, l lang, parts []string, answer func(string, bool), show func(panelView), ask func(*adminInput, string)) {
	arg := func(i int) string {
		if len(parts) > i {
			return parts[i]
		}
		return ""
	}
	if arg(2) == "new" {
		answer("", false)
		ask(&adminInput{kind: "moa_name"}, l.t("⚖️ اسم پریست جدید رو بفرست (مثلاً <code>duo</code>). مدل عاملش پرووایدر فعال فعلیه؛ بعد مشاورها رو انتخاب کن."))
		return
	}
	list := b.moaPresets()
	i, _ := strconv.Atoi(arg(3))
	if arg(2) == "" || i < 0 || i >= len(list) {
		answer("", false)
		show(b.viewMoa(l))
		return
	}
	p := &list[i]
	pid, _ := strconv.ParseInt(arg(4), 10, 64)
	name := ""
	for _, pr := range b.prov.Snapshot() {
		if pr.ID == pid {
			name = pr.Name
		}
	}
	switch arg(2) {
	case "p":
		answer("", false)
		show(b.viewMoaPreset(l, i))
		return
	case "agg":
		if name != "" {
			p.Aggregator.Provider = name
		}
	case "ref":
		if name == "" {
			break
		}
		kept := p.References[:0:0]
		found := false
		for _, r := range p.References {
			if strings.EqualFold(r.Provider, name) {
				found = true
				continue
			}
			kept = append(kept, r)
		}
		if !found {
			kept = append(kept, moa.Slot{Provider: name})
		}
		p.References = kept
	case "eff":
		j, _ := strconv.Atoi(arg(4))
		if j == -1 {
			p.Aggregator.Effort = next(moaEfforts, p.Aggregator.Effort)
		} else if j >= 0 && j < len(p.References) {
			p.References[j].Effort = next(moaEfforts, p.References[j].Effort)
		}
	case "fan":
		p.Fanout = next(moaFanouts, p.FanoutName())
	case "priv":
		p.PrivacyFilter = next(moaPrivacies, p.PrivacyFilter)
	case "on":
		on := !p.On()
		p.Enabled = &on
	case "active":
		cur, _ := b.st.Setting(settingMoaActive)
		if strings.EqualFold(cur, p.Name) {
			_ = b.st.DeleteSetting(ctx, settingMoaActive)
		} else {
			_ = b.st.SetSetting(ctx, settingMoaActive, p.Name)
		}
	case "def":
		_ = b.st.SetSetting(ctx, settingMoaDefault, p.Name)
	case "del":
		if cur, _ := b.st.Setting(settingMoaActive); strings.EqualFold(cur, p.Name) {
			_ = b.st.DeleteSetting(ctx, settingMoaActive)
		}
		list = append(list[:i], list[i+1:]...)
		_ = b.saveMoaPresets(ctx, list)
		answer(l.t("🗑 حذف شد"), false)
		show(b.viewMoa(l))
		return
	}
	_ = b.saveMoaPresets(ctx, list)
	answer("", false)
	show(b.viewMoaPreset(l, i))
}

// moaNameInput makes a preset of the name the owner sent.
func (b *Bot) moaNameInput(ctx context.Context, l lang, m *telegram.Message, done func(panelView)) {
	name := strings.TrimSpace(m.Text)
	if name == "" || len([]rune(name)) > 40 || strings.ContainsAny(name, "\n:") || strings.EqualFold(name, "moa") {
		b.reply(m, l.t("یه اسم کوتاه بفرست (بدون : و تا ۴۰ حرف)."))
		return
	}
	list := b.moaPresets()
	if _, ok := moa.Find(list, name); ok && name != "" {
		b.reply(m, l.t("پریستی با این اسم هست."))
		return
	}
	agg := ""
	if p, ok := b.prov.ActiveFor(ctx); ok {
		agg = p.Name
	}
	list = append(list, moa.Preset{Name: name, Aggregator: moa.Slot{Provider: agg}, Fanout: moa.FanoutUserTurn})
	_ = b.saveMoaPresets(ctx, list)
	done(b.viewMoaPreset(l, len(list)-1))
}
