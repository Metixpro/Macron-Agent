package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Metixpro/macron-agent/pkg/config"
	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/providers"
	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/telegram"
	"github.com/Metixpro/macron-agent/pkg/textutil"
	"github.com/Metixpro/macron-agent/pkg/tools"
)

// Speech to text in the admin panel (under ⚙️ settings): voice messages are
// written down before the model reads them, and transcribe_audio does the
// same for any recording. The service (OpenAI-compatible, or a Hugging Face
// model's inference address) is the panel's, else WAZE_STT_URL,
// else the OpenAI-compatible providers (each is asked once; one without
// speech to text is skipped for a few hours).
//
//	a:stt          status
//	a:stt:set      set a service (dialog: address, key, model; the message is deleted)
//	a:stt:test     send each service a second of silence
//	a:stt:del      remove the panel's service
//	a:stt:off      turn speech to text off or on

const (
	settingSTT    = "stt"     // the panel's service (JSON, encrypted)
	settingSTTOff = "stt_off" // "1" = off
)

type sttSetting struct {
	URL   string `json:"url"`
	Key   string `json:"key,omitempty"`
	Model string `json:"model,omitempty"`
}

func panelSTT(st *store.Store) (sttSetting, bool) {
	raw, err := st.SecretSetting(settingSTT)
	if err != nil || strings.TrimSpace(raw) == "" {
		return sttSetting{}, false
	}
	var s sttSetting
	if json.Unmarshal([]byte(raw), &s) != nil || s.URL == "" {
		return sttSetting{}, false
	}
	return s, true
}

// sttSources gives the registry its speech-to-text services; the
// providers are read again at most every half minute.
func sttSources(st *store.Store, cfg *config.Config) func() []tools.STTService {
	var (
		mu     sync.Mutex
		cached []tools.STTService
		at     time.Time
	)
	return func() []tools.STTService {
		if cfg.STTOff || st.SettingBool(settingSTTOff, false) {
			return nil
		}
		if s, ok := panelSTT(st); ok {
			return []tools.STTService{{Name: "panel", URL: s.URL, Key: s.Key, Model: s.Model}}
		}
		if cfg.STTURL != "" {
			return []tools.STTService{{Name: config.EnvSTTURL, URL: cfg.STTURL, Key: cfg.STTKey, Model: cfg.STTModel}}
		}
		mu.Lock()
		defer mu.Unlock()
		if time.Since(at) < 30*time.Second {
			return cached
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		list, err := st.Providers(ctx)
		if err != nil {
			return cached
		}
		cached, at = nil, time.Now()
		for _, p := range list { // in priority order
			if p.Enabled && providers.API(p) == llm.APIOpenAI {
				cached = append(cached, tools.STTService{Name: p.Name, URL: p.BaseURL, Key: p.APIKey, Model: cfg.STTModel, Auto: true})
			}
		}
		return cached
	}
}

func (b *Bot) sttSource(l lang) string {
	switch {
	case b.app.Config.STTOff:
		return l.t("⛔ خاموش (<code>WAZE_STT_URL=off</code>)")
	case b.st.SettingBool(settingSTTOff, false):
		return l.t("⛔ خاموش (از پنل)")
	}
	if _, ok := panelSTT(b.st); ok {
		return l.t("✅ سرویس پنل")
	}
	if b.app.Config.STTURL != "" {
		return l.t("✅ از <code>WAZE_STT_URL</code>")
	}
	return l.t("🔄 خودکار از پرووایدرهای OpenAI-compatible")
}

func (b *Bot) viewSTT(l lang) panelView {
	var sb strings.Builder
	sb.WriteString(l.t("🎙 <b>تبدیل صدا به متن</b>\n\n"))
	fmt.Fprint(&sb, l.f("منبع: %s\n", b.sttSource(l)))
	services := b.app.Registry.STTServices()
	for _, s := range services {
		model := s.Model
		if model == "" {
			model = tools.DefaultSTTModel(s.URL)
		}
		fmt.Fprintf(&sb, "• %s · <code>%s</code>\n", esc(s.Name), esc(model))
	}
	if len(services) == 0 && !b.app.Config.STTOff && !b.st.SettingBool(settingSTTOff, false) {
		sb.WriteString(l.t("❌ هیچ سرویسی نیست؛ یه پرووایدر OpenAI-compatible اضافه کن یا سرویس جدا تنظیم کن.\n"))
	}
	fmt.Fprint(&sb, l.f("وضعیت: %s\n", onOff(l, b.app.Registry.CanTranscribe())))
	sb.WriteString(l.t("\nویس‌ها و ویدیو‌مسیج‌ها قبل از این‌که مدل ببینه به متن تبدیل می‌شن، پس ربات مثل یه پیام تایپی جوابشون رو می‌ده؛ ") +
		l.t("ابزار <code>transcribe_audio</code> هم هر فایل صوتی یا ویدیویی (پادکست، کلاس، ویدیو) رو می‌نویسه. ") +
		l.t("هر سرویس OpenAI-compatible که <code>/audio/transcriptions</code> داره کار می‌کنه: Whisper اوپن‌اِی‌آی، Groq، سرویس‌های ایرانی، یا faster-whisper روی خود سرور."))
	rows := [][]telegram.InlineKeyboardButton{
		row(b.button(l.t("🔗 تنظیم سرویس"), "a:stt:set", telegram.StyleSuccess), b.button(l.t("🧪 تست"), "a:stt:test", telegram.StylePrimary)),
	}
	var last []telegram.InlineKeyboardButton
	if _, ok := panelSTT(b.st); ok {
		last = append(last, b.button(l.t("🗑 حذف سرویس پنل"), "a:stt:del", telegram.StyleDanger))
	}
	if b.st.SettingBool(settingSTTOff, false) {
		last = append(last, b.button(l.t("▶️ روشن کردن"), "a:stt:off", telegram.StyleSuccess))
	} else {
		last = append(last, b.button(l.t("⏸ خاموش کردن"), "a:stt:off", ""))
	}
	rows = append(rows, last, row(b.back(l, "a:ai")))
	return panelView{html: sb.String(), kb: keyboard(rows...)}
}

// testSTT sends each service a second of silence.
func (b *Bot) testSTT(ctx context.Context, l lang) string {
	services := b.app.Registry.STTServices()
	if len(services) == 0 {
		return l.t("❌ هیچ سرویس صدا به متنی تنظیم نیست.")
	}
	var sb strings.Builder
	sb.WriteString(l.t("🧪 <b>تست صدا به متن</b>\n"))
	for _, s := range services {
		if err := b.app.Registry.CheckSTT(ctx, s); err != nil {
			fmt.Fprintf(&sb, "❌ %s: %s\n", esc(s.Name), esc(textutil.TruncateRunes(err.Error(), 200, "…")))
		} else {
			fmt.Fprint(&sb, l.f("✅ %s: کار می‌کنه\n", esc(s.Name)))
		}
	}
	return strings.TrimSpace(sb.String())
}

// parseSTT reads the dialog's lines: address, key ("-" for none), model.
func parseSTT(text string) (sttSetting, bool) {
	var lines []string
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	if len(lines) == 0 {
		return sttSetting{}, false
	}
	if i := strings.Index(lines[0], "http"); i > 0 { // a stray character before the link
		lines[0] = lines[0][i:]
	}
	u, err := url.Parse(lines[0])
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return sttSetting{}, false
	}
	s := sttSetting{URL: strings.TrimSuffix(strings.TrimRight(lines[0], "/"), "/audio/transcriptions")}
	if len(lines) > 1 && lines[1] != "-" {
		s.Key = lines[1]
	}
	if len(lines) > 2 {
		s.Model = lines[2]
	}
	return s, true
}

func (b *Bot) sttCallback(ctx context.Context, l lang, action string, chatID int64, answer func(string, bool), ask func(*adminInput, string)) bool {
	switch action {
	case "set":
		answer("", false)
		ask(&adminInput{kind: "stt_set"}, l.t("🎙 سرویس صدا به متن رو در سه خط بفرست:\n1. آدرس OpenAI-compatible (مثلاً <code>https://api.openai.com/v1</code> یا <code>https://api.groq.com/openai/v1</code>)\n")+
			l.t("2. کلید API (یا <code>-</code> اگه کلید نمی‌خواد)\n3. مدل (اختیاری؛ پیش‌فرض <code>whisper-1</code>، برای Groq <code>whisper-large-v3-turbo</code>)\n\n")+
			l.t("مدل‌های Hugging Face هم کار می‌کنن: آدرس خود مدل (مثلاً <code>https://router.huggingface.co/hf-inference/models/openai/whisper-large-v3</code>) و توکن <code>hf_…</code>؛ مدل لازم نیست.\n\n")+
			l.t("پیامت بعد از خوندن پاک می‌شه و کلید رمزنگاری‌شده ذخیره می‌شه."))
		return true
	case "test":
		answer(l.t("دارم تست می‌کنم…"), false)
		b.sendHTML(ctx, chatID, b.testSTT(ctx, l), telegram.SendOptions{})
	case "del":
		_ = b.st.SetSecretSetting(ctx, settingSTT, "")
		answer(l.t("حذف شد"), false)
	case "off":
		off := !b.st.SettingBool(settingSTTOff, false)
		_ = b.st.SetSetting(ctx, settingSTTOff, map[bool]string{true: "1", false: "0"}[off])
		answer(map[bool]string{true: l.t("خاموش شد"), false: l.t("روشن شد")}[off], false)
	default:
		answer("", false)
	}
	return false
}

// sttInput saves the service of the dialog and tries it.
func (b *Bot) sttInput(ctx context.Context, l lang, m *telegram.Message) bool {
	_ = b.api.DeleteMessage(ctx, m.Chat.ID, m.MessageID) // it carries a key
	s, ok := parseSTT(m.Text)
	if !ok {
		b.reply(m, l.t("خط اول باید یه آدرس http(s) باشه؛ دوباره بفرست."))
		return false
	}
	raw, _ := json.Marshal(s)
	if err := b.st.SetSecretSetting(ctx, settingSTT, string(raw)); err != nil {
		b.reply(m, l.t(textError))
		return false
	}
	verdict := l.t("✅ کار می‌کنه")
	if err := b.app.Registry.CheckSTT(ctx, tools.STTService{Name: "panel", URL: s.URL, Key: s.Key, Model: s.Model}); err != nil {
		verdict = l.t("⚠️ تست جواب نداد: ") + esc(textutil.TruncateRunes(err.Error(), 200, "…"))
	}
	b.reply(m, l.t("🎙 سرویس صدا به متن ذخیره شد.")+"\n"+verdict)
	return true
}
