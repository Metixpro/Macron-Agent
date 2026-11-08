package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/agent"
	"github.com/Metixpro/macron-agent/pkg/emoji"
	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/providers"
	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/telegram"
)

type (
	showFunc = func(panelView)
	askFunc  = func(*adminInput, string)
)

// handleSectionInput finishes the dialogs of the provider, channel, emoji and broadcast sections.
func (b *Bot) handleSectionInput(ctx context.Context, m *telegram.Message, in *adminInput, done func(panelView)) bool {
	l := b.langOf(m.From)
	text := strings.TrimSpace(m.Text)
	switch in.kind {
	// ----- provider wizard
	case "pv_name":
		if text == "" || len([]rune(text)) > 40 {
			b.reply(m, l.t("یه اسم کوتاه بفرست (حداکثر ۴۰ کاراکتر)."))
			return true
		}
		in.draft.Name = text
		in.kind = "pv_url"
		b.expect(m.From.ID, in)
		b.reply(m, l.t("🌐 آدرس API را بفرست:\n<code>https://api.openai.com/v1</code>\n<code>https://api.anthropic.com</code>\nبرای Responses، آدرس کاملِ <code>/responses</code> را بفرست."))
	case "pv_url":
		u, api := llm.CleanBaseURL(text) // a whole endpoint pasted (".../v1/responses") names its wire format
		u = strings.TrimRight(u, "/")
		if err := providers.ValidateBaseURL(u); err != nil {
			b.reply(m, l.t("آدرس معتبر نیست؛ باید شبیه <code>https://host/v1</code> باشه."))
			return true
		}
		in.draft.BaseURL = u
		if api != "" {
			in.draft.API = api
		}
		in.kind = "pv_key"
		b.expect(m.From.ID, in)
		b.reply(m, l.t("🔑 کلید API رو بفرست (اگه کلید نداره بنویس <code>-</code>).\nپیامت رو بلافاصله پاک می‌کنم که جایی نمونه."))
	case "pv_key":
		_ = b.api.DeleteMessage(ctx, m.Chat.ID, m.MessageID)
		if text != "-" {
			in.draft.APIKey = text
		}
		in.kind = "pv_model"
		b.expect(m.From.ID, in)
		b.reply(m, l.t("🧠 اسم مدل رو بفرست، مثلاً <code>gpt-4o</code> یا <code>claude-sonnet-5</code>:"))
	case "pv_model":
		if text == "" || len(text) > 120 {
			b.reply(m, l.t("اسم مدل معتبر نیست."))
			return true
		}
		in.draft.Model = text
		in.draft.Enabled, in.draft.Vision, in.draft.Priority = true, true, b.nextPriority(ctx)
		b.inputs.Delete(m.From.ID)
		b.reply(m, l.t("🧪 دارم اتصال رو تست می‌کنم…"))
		res, err := b.prov.Test(ctx, in.draft)
		if err != nil {
			b.expect(m.From.ID, &adminInput{kind: "pv_pending", draft: in.draft})
			b.showNew(ctx, m.Chat.ID, panelView{
				html: l.t("❌ تست ناموفق بود:\n<code>") + esc(shortText(err.Error(), 300)) + l.t("</code>\n\nبازم ذخیره کنم؟"),
				kb: keyboard(row(b.button(l.t("💾 ذخیره در هر صورت"), "a:pv:savepending", telegram.StylePrimary),
					b.button(l.t("❌ لغو"), "a:pv", telegram.StyleDanger))),
			})
			return true
		}
		id, err := b.saveProvider(ctx, l, in.draft)
		if err != nil {
			b.reply(m, l.t("ذخیره نشد: ")+esc(err.Error()))
			return true
		}
		b.reply(m, l.f("✅ وصل شد! جواب مدل: «%s» در %s", esc(shortText(res.Reply, 60)), res.Latency.Round(time.Millisecond)))
		done(b.viewProvider(ctx, l, id))
	case "pv_model_edit", "pv_key_edit", "pv_window", "pv_temp":
		p, err := b.st.Provider(ctx, in.target)
		if err != nil {
			done(b.viewProviders(ctx, l))
			return true
		}
		if in.kind == "pv_temp" {
			t, def, ok := parseTemperature(text)
			max := 2.0
			if providers.API(*p) == llm.APIAnthropic {
				max = 1 // Claude takes 0–1
			}
			if !ok || t > max {
				b.reply(m, l.f("یه عدد بین ۰ و %s بفرست (مثلاً <code>0.7</code>)، یا <code>-</code> برای پیش‌فرض.", l.num(int(max))))
				return true
			}
			p.Temperature = nil
			if !def {
				p.Temperature = &t
			}
		} else if in.kind == "pv_window" {
			n, ok := parseTokens(text)
			if !ok {
				b.reply(m, l.t("یه عدد بین ۸۰۰۰ و ۱۰ میلیون بفرست (مثلاً <code>256k</code>)، یا <code>0</code> برای خودکار."))
				return true
			}
			p.ContextWindow = n
		} else if in.kind == "pv_key_edit" {
			_ = b.api.DeleteMessage(ctx, m.Chat.ID, m.MessageID)
			p.APIKey = map[bool]string{true: "", false: text}[text == "-"]
		} else {
			p.Model = text
		}
		_ = b.st.UpdateProvider(ctx, *p)
		_ = b.prov.Reload(ctx)
		done(b.viewProvider(ctx, l, p.ID))

	// ----- premium emoji
	case "em_add":
		var ents []emoji.Entity
		for _, e := range m.Entities {
			if e.Type == "custom_emoji" {
				ents = append(ents, emoji.Entity{Offset: e.Offset, Length: e.Length, CustomEmojiID: e.CustomEmojiID})
			}
		}
		pairs, problems := emoji.ParseMappings(m.Text, ents)
		existing, err := b.st.PremiumEmoji(ctx)
		if err != nil {
			b.reply(m, l.t("ذخیره نشد: ")+esc(err.Error()))
			return true
		}
		// Extra mappings are not added; the others are.
		add, rejected := emoji.Filter(existing, pairs)
		saved := 0
		for _, p := range add {
			if err := b.st.SetPremiumEmoji(ctx, p.Emoji, p.ID); err == nil {
				saved++
			}
		}
		b.reloadEmoji(ctx)
		msg := l.f("✅ %d تا ایموجی ذخیره شد.", saved)
		if len(rejected) > 0 {
			lines := make([]string, len(rejected))
			for i, r := range rejected {
				switch {
				case r.Twice:
					lines[i] = l.f("«%s»: توی همین پیام تکراری بود؛ اولی ثبت شد", r.Emoji)
				case r.Taken:
					lines[i] = l.f("«%s»: از قبل ایموجی پریمیوم داره (برای عوض کردنش اول حذفش کن)", r.Emoji)
				default:
					lines[i] = l.f("«%s»: این ایموجی پریمیوم قبلاً برای %s ثبت شده", r.Emoji, r.UsedFor)
				}
			}
			msg += l.f("\n\n♻️ %d تا تکراری بود و ثبت نشد:\n• ", len(rejected)) + esc(strings.Join(lines, "\n• "))
		}
		if len(problems) > 0 {
			lines := make([]string, len(problems))
			for i, p := range problems {
				if p.MissingID {
					lines[i] = l.f("«%s»: ایموجی پریمیوم یا آیدی عددی پیدا نشد", p.Line)
				} else {
					lines[i] = l.f("«%s»: اولش باید یه ایموجی معمولی باشه", p.Line)
				}
			}
			msg += l.t("\n\n⚠️ این خط‌ها رو نفهمیدم:\n• ") + esc(strings.Join(lines, "\n• "))
		}
		if saved == 0 && len(rejected) == 0 {
			b.reply(m, msg+l.t("\n\nدوباره بفرست یا /cancel"))
			return true
		}
		b.reply(m, msg)
		done(b.viewEmoji(ctx, l))
	case "em_del":
		removed := 0
		for _, f := range strings.Fields(text) {
			if _, ok := b.emojis.Load().ID(f); ok {
				_ = b.st.DeletePremiumEmoji(ctx, f)
				_ = b.st.DeletePremiumEmoji(ctx, f+"️")
				_ = b.st.DeletePremiumEmoji(ctx, strings.ReplaceAll(f, "️", ""))
				removed++
			}
		}
		b.reloadEmoji(ctx)
		b.reply(m, l.f("🗑 %d تا حذف شد.", removed))
		done(b.viewEmoji(ctx, l))

	case "ie_key":
		return b.handleImageEditInput(ctx, m, done)
	case "ig_key":
		return b.handleImageGenInput(ctx, m, done)
	case "mu_key":
		return b.handleMusicInput(ctx, m, done)
	case "em_pack":
		return b.handleEmojiPackInput(ctx, m, done)
	default:
		b.inputs.Delete(m.From.ID)
		return false
	}
	return true
}

func shortText(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// ---------------------------------------------------------------- providers

func maskKey(l lang, k string) string {
	switch {
	case k == "":
		return l.t("بدون کلید")
	case len(k) <= 8:
		return "••••"
	}
	return k[:3] + "…" + k[len(k)-4:]
}

func (b *Bot) nextPriority(ctx context.Context) int {
	list, _ := b.st.Providers(ctx)
	p := 10
	for _, x := range list {
		p = max(p, x.Priority+10)
	}
	return p
}

func (b *Bot) saveProvider(ctx context.Context, l lang, p store.Provider) (int64, error) {
	id, err := b.st.AddProvider(ctx, p)
	if errors.Is(err, store.ErrDuplicate) {
		return 0, errors.New(l.t("پروایدری با این اسم از قبل هست"))
	}
	if err != nil {
		return 0, err
	}
	if !b.prov.Configured() {
		_ = b.st.SetSetting(ctx, store.SettingActiveProvider, strconv.FormatInt(id, 10))
	}
	return id, b.prov.Reload(ctx)
}

func (b *Bot) viewProviders(ctx context.Context, l lang) panelView {
	list := b.prov.Snapshot()
	var sb strings.Builder
	sb.WriteString(l.t("🤖 <b>پروایدرها</b>\n⭐ فعال (پیش‌فرض) · ▫️ روشن · ⏸ خاموش · 🧊 موقتاً کنار گذاشته شده\n\n"))
	var rows [][]telegram.InlineKeyboardButton
	if len(list) == 0 {
		sb.WriteString(l.t("هنوز پروایدری اضافه نشده. بدون پروایدر ربات نمی‌تونه جواب بده!\n"))
	}
	for i, p := range list {
		mark := "▫️"
		switch {
		case p.Active:
			mark = "⭐"
		case !p.Enabled:
			mark = "⏸"
		case p.Cooling:
			mark = "🧊"
		}
		health := "✅"
		if p.LastErrorAt != nil && (p.LastOKAt == nil || p.LastErrorAt.After(*p.LastOKAt)) {
			health = "❌ " + esc(shortText(p.LastError, 40))
		}
		lat := ""
		if p.AvgLatencyMS > 0 {
			lat = fmt.Sprintf(" · %dms", p.AvgLatencyMS)
		}
		fmt.Fprintf(&sb, "%d. %s <b>%s</b> — <code>%s</code>%s%s\n    %s\n", i+1, mark, esc(p.Name), esc(p.Model), effortBadge(p.Effort), lat, health)
		rows = append(rows, row(b.button(mark+" "+p.Name, fmt.Sprintf("a:pv:%d", p.ID), "")))
	}
	failover := b.st.SettingBool(store.SettingFailover, true)
	fmt.Fprint(&sb, l.f("\n🔁 سوییچ خودکار وقتی پروایدر فعال خطا بده: %s", onOff(l, failover)))
	rows = append(rows,
		row(b.button(l.t("➕ افزودن پروایدر"), "a:pv:add", telegram.StyleSuccess), b.button(l.t("🧭 مسیریابی هوشمند"), "a:rt", telegram.StylePrimary)),
		row(b.button(l.t("🔁 سوییچ خودکار"), "a:st:failover:pv", ""), b.back(l, "a:home")))
	return panelView{html: sb.String(), kb: keyboard(rows...)}
}

// maxModelButtons: a long model list (OpenRouter has hundreds) shows its start.
const maxModelButtons = 40

// viewModels lets the admin pick the provider's model from the endpoint's list.
func (b *Bot) viewModels(l lang, p *store.Provider, models []string) panelView {
	html := l.f("📋 <b>مدل‌های %s</b>\nیکی رو انتخاب کن:", esc(p.Name))
	var rows [][]telegram.InlineKeyboardButton
	for i, m := range models {
		if i == maxModelButtons {
			html += l.f("\n\n(%d مدل دیگه هم هست؛ برای اونا با ✏️ مدل اسمش رو بفرست)", len(models)-maxModelButtons)
			break
		}
		label := m
		if m == p.Model {
			label = "✓ " + m
		}
		btn := b.button(shortText(label, 40), fmt.Sprintf("a:pv:%d:pick:%d", p.ID, i), "")
		if n := len(rows); n > 0 && len(rows[n-1]) < 2 {
			rows[n-1] = append(rows[n-1], btn)
		} else {
			rows = append(rows, row(btn))
		}
	}
	if len(models) == 0 {
		html += l.t("\n\n(این سرویس هیچ مدلی برنگردوند)")
	}
	rows = append(rows, row(b.back(l, fmt.Sprintf("a:pv:%d", p.ID))))
	return panelView{html: html, kb: keyboard(rows...)}
}

// apiName is how the admin panel calls a wire format.
func apiName(api string) string {
	switch api {
	case llm.APIAnthropic:
		return "Anthropic Messages"
	case llm.APIResponses:
		return "OpenAI Responses (/responses)"
	}
	return "OpenAI-compatible (chat completions)"
}

// nextAPI is the wire format after cur, going round: chat completions →
// Responses → Anthropic → chat completions.
func nextAPI(cur string) string {
	switch cur {
	case llm.APIOpenAI:
		return llm.APIResponses
	case llm.APIResponses:
		return llm.APIAnthropic
	}
	return llm.APIOpenAI
}

func (b *Bot) viewProvider(ctx context.Context, l lang, id int64) panelView {
	p, err := b.st.Provider(ctx, id)
	if err != nil {
		return b.viewProviders(ctx, l)
	}
	active := b.prov.ActiveID() == id
	var sb strings.Builder
	fmt.Fprintf(&sb, "🤖 <b>%s</b>", esc(p.Name))
	if active {
		sb.WriteString(l.t(" ⭐ فعال"))
	}
	if !p.Enabled {
		sb.WriteString(l.t(" ⏸ خاموش"))
	}
	host := p.BaseURL
	if u, err := url.Parse(p.BaseURL); err == nil {
		host = u.Scheme + "://" + u.Host + u.Path
	}
	fmt.Fprint(&sb, l.f("\n\n🌐 آدرس: <code>%s</code>\n🔑 کلید: <code>%s</code>\n🧠 مدل: <code>%s</code>\n🖼 تصویر: %s\n↕️ اولویت: %d",
		esc(host), esc(maskKey(l, p.APIKey)), esc(p.Model), onOff(l, p.Vision), p.Priority))
	api := providers.API(*p)
	fmt.Fprint(&sb, l.f("\n🔌 نوع API: <b>%s</b>", apiName(api)))
	if p.API == "" {
		sb.WriteString(l.t(" (خودکار از روی آدرس)"))
	}
	fmt.Fprint(&sb, l.f("\n🌊 استریم: %s", onOff(l, !p.NoStream)))
	if p.ContextWindow > 0 {
		fmt.Fprint(&sb, l.f("\n🪟 پنجره context: <b>%s</b> توکن", agent.HumanTokens(p.ContextWindow)))
	} else {
		fmt.Fprint(&sb, l.f("\n🪟 پنجره context: خودکار (%s از اسم مدل)", agent.HumanTokens(agent.ContextWindow(p.Model))))
		if !agent.ContextWindowKnown(p.Model) {
			sb.WriteString(l.t("\n⚠️ این اسم مدل رو نمی‌شناسم، پس پنجره بزرگ فرض شده. اگه پشتش یه روتر یا کامبو (چند مدل پشت‌سرهم) هست، پنجره کوچک‌ترین مدلش رو دستی بذار؛ وگرنه توی چت‌های سنگین از پنجره اون مدل رد می‌شه."))
		}
	}
	if p.Temperature != nil {
		fmt.Fprint(&sb, l.f("\n🌡 temperature: <b>%s</b>", strconv.FormatFloat(*p.Temperature, 'f', -1, 64)))
	} else {
		fmt.Fprint(&sb, l.t("\n🌡 temperature: پیش‌فرض"))
	}
	fmt.Fprint(&sb, l.f("\n⚡ effort: <b>%s</b>", effortName(l, p.Effort)))
	sb.WriteString(b.toolModeLine(l, *p))
	sb.WriteString(b.cacheLine(l, p.ID))
	fmt.Fprint(&sb, l.f("\n\n📊 درخواست: %d · خطا: %d", p.Requests, p.Failures))
	if p.AvgLatencyMS > 0 {
		fmt.Fprint(&sb, l.f(" · میانگین %dms", p.AvgLatencyMS))
	}
	fmt.Fprint(&sb, l.f("\n✅ آخرین موفق: %s", ago(l, p.LastOKAt)))
	if p.LastErrorAt != nil {
		fmt.Fprint(&sb, l.f("\n❌ آخرین خطا (%s): <code>%s</code>", ago(l, p.LastErrorAt), esc(shortText(p.LastError, 200))))
	}
	pid := func(action string) string { return fmt.Sprintf("a:pv:%d:%s", id, action) }
	toggle := b.button(l.t("⏸ خاموش کن"), pid("toggle"), "")
	if !p.Enabled {
		toggle = b.button(l.t("▶️ روشن کن"), pid("toggle"), telegram.StyleSuccess)
	}
	first := row(toggle)
	if !active && p.Enabled {
		first = row(b.button(l.t("⭐ فعال کن"), pid("activate"), telegram.StyleSuccess), toggle)
	}
	return panelView{html: sb.String(), kb: keyboard(first,
		row(b.button(l.t("🧪 تست اتصال"), pid("test"), telegram.StylePrimary), b.button(l.t("🖼 تصویر"), pid("vision"), "")),
		row(b.button(l.t("🔌 نوع API"), pid("api"), ""), b.button(l.t("📋 انتخاب مدل"), pid("models"), "")),
		row(b.button(l.t("✏️ مدل"), pid("model"), ""), b.button(l.t("🔑 کلید"), pid("key"), "")),
		row(b.button(l.t("🪟 پنجره context"), pid("window"), "")),
		row(b.button(l.t("🌡 Temperature"), pid("temp"), ""), b.button(l.t("⚡ Effort")+" "+effortOf(p.Effort).icon, pid("eff"), "")),
		row(b.button(l.t("🧰 ابزارها برای این مدل"), pid("tools"), ""), b.button(l.f("🌊 استریم: %s", onOff(l, !p.NoStream)), pid("stream"), "")),
		row(b.button(l.t("⬆️ اولویت"), pid("up"), ""), b.button(l.t("⬇️ اولویت"), pid("down"), "")),
		row(b.button(l.t("🗑 حذف"), pid("delete"), telegram.StyleDanger), b.back(l, "a:pv")),
	)}
}

func (b *Bot) handleProviderPanel(ctx context.Context, q *telegram.CallbackQuery, parts []string, answer func(string, bool), show showFunc, ask askFunc) {
	l := b.langOf(&q.From)
	if len(parts) < 3 {
		answer("", false)
		show(b.viewProviders(ctx, l))
		return
	}
	switch parts[2] {
	case "add":
		answer("", false)
		ask(&adminInput{kind: "pv_name"}, l.t("➕ <b>پروایدر جدید</b>\nیه اسم کوتاه براش بفرست (مثلاً <code>main</code>):"))
		return
	case "savepending":
		in := b.pending(q.From.ID)
		if in == nil || in.kind != "pv_pending" {
			answer(l.t("زمانش گذشته؛ دوباره اضافه کن."), true)
			return
		}
		b.inputs.Delete(q.From.ID)
		id, err := b.saveProvider(ctx, l, in.draft)
		if err != nil {
			answer(err.Error(), true)
			return
		}
		answer(l.t("ذخیره شد"), false)
		show(b.viewProvider(ctx, l, id))
		return
	}
	id, _ := strconv.ParseInt(parts[2], 10, 64)
	action := ""
	if len(parts) > 3 {
		action = parts[3]
	}
	p, err := b.st.Provider(ctx, id)
	if err != nil {
		answer(l.t("پیدا نشد"), false)
		show(b.viewProviders(ctx, l))
		return
	}
	switch action {
	case "activate":
		_ = b.st.SetSetting(ctx, store.SettingActiveProvider, strconv.FormatInt(id, 10))
		answer(l.t("⭐ از الان همه درخواست‌ها می‌رن روی ")+p.Name, false)
	case "toggle":
		p.Enabled = !p.Enabled
		_ = b.st.UpdateProvider(ctx, *p)
		answer(map[bool]string{true: l.t("روشن شد"), false: l.t("خاموش شد")}[p.Enabled], false)
	case "vision":
		p.Vision = !p.Vision
		_ = b.st.UpdateProvider(ctx, *p)
		answer(l.t("انجام شد"), false)
	case "api":
		p.API = nextAPI(providers.API(*p))
		_ = b.st.UpdateProvider(ctx, *p)
		answer(l.f("نوع API: %s", apiName(p.API)), false)
	case "stream":
		p.NoStream = !p.NoStream
		_ = b.st.UpdateProvider(ctx, *p)
		answer(l.f("🌊 استریم: %s", onOff(l, !p.NoStream)), false)
	case "up", "down":
		delta := -15
		if action == "down" {
			delta = 15
		}
		p.Priority = max(p.Priority+delta, 0)
		_ = b.st.UpdateProvider(ctx, *p)
		answer(l.t("اولویت تغییر کرد"), false)
	case "test":
		answer(l.t("دارم تست می‌کنم…"), false)
		res, err := b.prov.Test(ctx, *p)
		if err != nil {
			b.sendHTML(ctx, q.Message.Chat.ID, "❌ <b>"+esc(p.Name)+"</b>: <code>"+esc(shortText(err.Error(), 300))+"</code>", telegram.SendOptions{})
		} else {
			b.sendHTML(ctx, q.Message.Chat.ID, l.f("✅ <b>%s</b> جواب داد: «%s» در %s", esc(p.Name), esc(shortText(res.Reply, 60)), res.Latency.Round(time.Millisecond)), telegram.SendOptions{})
		}
	case "model":
		answer("", false)
		ask(&adminInput{kind: "pv_model_edit", target: id}, l.t("🧠 اسم مدل جدید رو بفرست:"))
		return
	case "models", "pick":
		models, err := b.prov.Models(ctx, *p)
		if err != nil {
			answer(l.f("فهرست مدل‌ها گرفته نشد: %s", shortText(err.Error(), 150)), true)
			return
		}
		if action == "pick" && len(parts) > 4 {
			if n, err := strconv.Atoi(parts[4]); err == nil && n >= 0 && n < len(models) {
				p.Model = models[n]
				_ = b.st.UpdateProvider(ctx, *p)
				answer(l.f("مدل: %s", p.Model), false)
				break
			}
		}
		answer("", false)
		show(b.viewModels(l, p, models))
		return
	case "temp":
		answer("", false)
		ask(&adminInput{kind: "pv_temp", target: id}, l.t("🌡 <b>Temperature</b> این پرووایدر رو بفرست: عدد کم (مثلاً <code>0.2</code>) جواب‌های دقیق‌تر و ثابت‌تر، عدد زیاد (مثلاً <code>1</code>) جواب‌های خلاق‌تر و متنوع‌تر می‌ده. ")+
			l.t("OpenAI-compatible از ۰ تا ۲، Claude از ۰ تا ۱. <code>-</code> = پیش‌فرض (۰٫۳؛ برای Claude و مدل‌های استدلالی، پیش‌فرض خود مدل).\n\nمدلی که temperature قبول نکنه، یه بار یاد گرفته می‌شه و بعدش بدون اون می‌ره."))
		return
	case "tools":
		s := b.toolSkill(id)
		s.Mode = nextToolMode(s.Mode)
		b.saveToolSkill(ctx, id, s)
		answer(map[string]string{toolModeAuto: l.t("🧰 خودکار: همه ابزارها، با آمار فراخوانی‌ها"), toolModeFull: l.t("🧰 همیشه همه ابزارها"),
			toolModeFocus: l.t("🧰 همیشه ابزارهای متمرکز")}[s.Mode], false)
	case "eff":
		if len(parts) > 4 {
			key, ok := effortFromCode(parts[4])
			if !ok {
				answer("", false)
				return
			}
			p.Effort = key
			_ = b.st.UpdateProvider(ctx, *p)
			_ = b.prov.Reload(ctx)
			answer("⚡ Effort: "+effortName(l, key), false)
		} else {
			answer("", false)
		}
		show(b.viewEffort(l, *p, func(code string) string { return fmt.Sprintf("a:pv:%d:eff:%s", id, code) }, b.back(l, fmt.Sprintf("a:pv:%d", id))))
		return
	case "window":
		answer("", false)
		ask(&adminInput{kind: "pv_window", target: id}, l.t("🪟 پنجره context همین پرووایدر رو به توکن بفرست، مثلاً <code>128k</code> یا <code>1m</code>؛ ")+
			l.t("برای مدل‌هایی که از اسمشون درست حدس زده نمی‌شه. <code>0</code> = خودکار."))
		return
	case "key":
		answer("", false)
		ask(&adminInput{kind: "pv_key_edit", target: id}, l.t("🔑 کلید جدید رو بفرست (<code>-</code> برای بدون کلید). پیامت رو پاک می‌کنم."))
		return
	case "delete":
		answer("", false)
		show(panelView{html: l.f("🗑 پروایدر <b>%s</b> حذف بشه؟", esc(p.Name)), kb: keyboard(
			row(b.button(l.t("✅ آره، حذف کن"), fmt.Sprintf("a:pv:%d:deleteok", id), telegram.StyleDanger), b.button(l.t("❌ نه"), fmt.Sprintf("a:pv:%d", id), "")))})
		return
	case "deleteok":
		_ = b.st.DeleteProvider(ctx, id)
		_ = b.st.DeleteSetting(ctx, fmt.Sprint(settingToolSkill, id))
		if b.prov.ActiveID() == id {
			_ = b.st.DeleteSetting(ctx, store.SettingActiveProvider)
		}
		_ = b.prov.Reload(ctx)
		answer(l.t("حذف شد"), false)
		show(b.viewProviders(ctx, l))
		return
	default:
		answer("", false)
	}
	_ = b.prov.Reload(ctx)
	show(b.viewProvider(ctx, l, id))
}

// ---------------------------------------------------------------- premium emoji

func (b *Bot) viewEmoji(ctx context.Context, l lang) panelView {
	m, _ := b.st.PremiumEmoji(ctx)
	enabled := b.st.SettingBool(store.SettingPremiumEmoji, false)
	html := l.f(`🎨 <b>ایموجی پریمیوم</b>: %s
تعداد: <b>%d</b>

وقتی روشن باشه، هر ایموجی معمولی که توی متن‌ها و دکمه‌های ربات بیاد و نسخه پریمیومش ثبت شده باشه، خودکار پریمیوم نشون داده می‌شه.

⚠️ تلگرام فقط وقتی اجازه می‌ده که صاحب ربات <b>تلگرام پریمیوم</b> داشته باشه (یا ربات یوزرنیم Fragment خریده باشه).`, onOff(l, enabled), len(m))
	toggleStyle := telegram.StyleSuccess
	if enabled {
		toggleStyle = ""
	}
	return panelView{html: html, kb: keyboard(
		row(b.button(map[bool]string{true: l.t("🔴 خاموش کن"), false: l.t("🟢 روشن کن")}[enabled], "a:st:premium:em", toggleStyle),
			b.button(l.t("➕ افزودن"), "a:em:add", telegram.StylePrimary)),
		row(b.button(l.t("📦 وارد کردن پک"), "a:em:pack", telegram.StylePrimary)),
		row(b.button(l.t("📋 لیست"), "a:em:list", ""), b.button(l.t("🧪 تست"), "a:em:test", "")),
		row(b.button(l.t("🗑 حذف"), "a:em:del", ""), b.button(l.t("🧹 حذف همه"), "a:em:clear", telegram.StyleDanger)),
		row(b.back(l, "a:ws")),
	)}
}

func (b *Bot) handleEmojiPanel(ctx context.Context, q *telegram.CallbackQuery, parts []string, answer func(string, bool), show showFunc, ask askFunc) {
	l := b.langOf(&q.From)
	action := ""
	if len(parts) > 2 {
		action = parts[2]
	}
	chatID := q.Message.Chat.ID
	switch action {
	case "add":
		answer("", false)
		ask(&adminInput{kind: "em_add"}, l.t("➕ هر خط: <b>ایموجی معمولی</b> + فاصله + <b>ایموجی پریمیوم</b> (یا آیدی عددی‌اش). چند خط هم با هم می‌شه:\n\n<code>😀 ⭐️</code>  ← دومی رو از پنل ایموجی‌های پریمیوم بفرست\n<code>🔥 5368324170671202286</code>"))
		return
	case "pack":
		answer("", false)
		ask(&adminInput{kind: "em_pack"}, l.t("📦 لینک پک ایموجی پریمیوم رو بفرست، مثلاً:\n<code>https://t.me/addemoji/vector_icons_by_fStikBot</code>\n\nهر ایموجی پک جای همون ایموجی معمولی‌ای می‌شینه که سازنده پک بهش داده (نگاشت قبلی همون ایموجی‌ها عوض می‌شه)."))
		return
	case "icons":
		answer("", false)
		show(b.viewEmoji(ctx, l))
		return
	case "del":
		answer("", false)
		ask(&adminInput{kind: "em_del"}, l.t("🗑 ایموجی‌هایی که می‌خوای نگاشتشون حذف بشه رو بفرست (با فاصله جدا کن):"))
		return
	case "list":
		answer("", false)
		m, _ := b.st.PremiumEmoji(ctx)
		if len(m) == 0 {
			b.sendHTML(ctx, chatID, l.t("هنوز چیزی ثبت نشده."), telegram.SendOptions{})
			return
		}
		list, more := emoji.Preview(m, 80)
		if more > 0 {
			list += l.f("… و %s تای دیگه\n", l.num(more))
		}
		b.sendHTML(ctx, chatID, l.t("📋 <b>نگاشت‌ها</b>\n\n")+list, telegram.SendOptions{})
		return
	case "test":
		answer("", false)
		m, _ := b.st.PremiumEmoji(ctx)
		var keys []string
		for k := range m {
			keys = append(keys, k)
			if len(keys) == 30 {
				break
			}
		}
		sample := l.t("🧪 تست ایموجی پریمیوم:\n") + strings.Join(keys, " ")
		set := emoji.New(m)
		if _, err := b.api.SendHTML(ctx, chatID, set.HTML(sample), telegram.SendOptions{}); err != nil {
			b.sendHTML(ctx, chatID, l.t("❌ تلگرام قبول نکرد: <code>")+esc(err.Error())+l.t("</code>\nاحتمالاً صاحب ربات پریمیوم نداره."), telegram.SendOptions{})
		}
		return
	case "clear":
		answer("", false)
		show(panelView{html: l.t("🧹 همه نگاشت‌های ایموجی پاک بشن؟"), kb: keyboard(row(
			b.button(l.t("✅ آره"), "a:em:clearok", telegram.StyleDanger), b.button(l.t("❌ نه"), "a:em", "")))})
		return
	case "clearok":
		_ = b.st.ClearPremiumEmoji(ctx)
		b.reloadEmoji(ctx)
		answer(l.t("پاک شد"), false)
	default:
		answer("", false)
	}
	show(b.viewEmoji(ctx, l))
}

// parseTemperature reads a temperature ("0.7", "۰٫۷", "1"); def: "-", the default.
func parseTemperature(text string) (t float64, def, ok bool) {
	text = strings.TrimSpace(text)
	switch strings.ToLower(text) {
	case "-", "default", "پیش‌فرض", "پیشفرض":
		return 0, true, true
	}
	t, err := strconv.ParseFloat(strings.NewReplacer("۰", "0", "۱", "1", "۲", "2", "۳", "3", "۴", "4", "۵", "5", "۶", "6", "۷", "7", "۸", "8", "۹", "9",
		"٫", ".", ",", ".").Replace(text), 64)
	return t, false, err == nil && t >= 0 && t <= 2
}

// cacheLine says how much of its prompts a provider reads from its cache, as
// measured since the bot started ("" before anything was measured). A
// provider that caches little costs the full prompt on every step.
func (b *Bot) cacheLine(l lang, id int64) string {
	r, n, known := b.providerCaches(id)
	if n == 0 {
		return ""
	}
	line := l.f("\n📦 کش پرامپت: %s٪ از %s درخواست", l.num(int(r*100+0.5)), l.num(n))
	if known && r < triageCacheLow {
		line += l.t(" (کمه؛ هر قدم کار پرامپت کامل حساب می‌شه. اگه پشتش روتر یا کامبو هست، چک کن کش رو نگه می‌داره)")
	}
	return line
}
