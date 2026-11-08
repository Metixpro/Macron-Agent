package main

import (
	"context"
	"encoding/json"
	"log"
	"os"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/app"
	"github.com/Metixpro/macron-agent/pkg/telegram"
	"github.com/Metixpro/macron-agent/pkg/textutil"
	"github.com/Metixpro/macron-agent/pkg/updater"
)

// /update in the chat: the newest release is looked up, its changelog shown
// with [+] update; once approved it is downloaded, checked against
// SHA256SUMS, tried in a staging folder, put in place atomically, and the
// service restarted from outside this process (so the answer is sent
// first). The new process says it is back, with its version and uptime. A
// failed step leaves the installed program as it was and says why.
//
// Once a day the newest release is looked up, and each new version is told
// to the owners once («[!] vX is out — /update»); update_notice turns it off.

const (
	settingUpdateNotice   = "update_notice"     // the daily notice (on)
	settingUpdateNotified = "update_notified"   // the last version told
	settingUpdateChecked  = "update_checked_at" // when it was last looked up (RFC3339)
	settingUpdatePending  = "update_pending"    // the update in flight, for the report after the restart
	updateCheckEvery      = 24 * time.Hour
)

// restartLater restarts the service from outside (a variable for the tests).
var restartLater = updater.RestartLater

type updatePending struct {
	Chat    int64  `json:"chat"`
	Version string `json:"version"`
	From    string `json:"from"`
}

// updateView looks the newest release up and says what is there.
func (b *Bot) updateView(ctx context.Context, l lang, back string) panelView {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	rel, err := updater.Latest(cctx, "")
	kb := keyboard(row(b.back(l, back)))
	if err != nil {
		return panelView{html: l.f("[-] نسخه جدید چک نشد: %s", esc(err.Error())), kb: kb}
	}
	if !updater.Newer(rel.Version(), app.Version) {
		return panelView{html: l.f("[+] تو آخرین نسخه‌ای (<code>v%s</code>).", esc(app.Version)), kb: kb}
	}
	notes := strings.TrimSpace(rel.Body)
	if notes == "" {
		notes = l.t("(بدون توضیح)")
	}
	html := l.f("[!] نسخه جدید: <b>v%s</b> (الان <code>v%s</code>)\n\n<pre>%s</pre>", esc(rel.Version()), esc(app.Version),
		esc(textutil.TruncateRunes(notes, 2500, "…")))
	return panelView{html: html, kb: keyboard(row(b.button(l.t("[+] آپدیت کن"), "up:go:"+rel.TagName, telegram.StyleSuccess), b.back(l, back)))}
}

// handleUpdateCallback runs the update the owner approved: up:go:TAG.
func (b *Bot) handleUpdateCallback(ctx context.Context, q *telegram.CallbackQuery, answer func(string, bool)) {
	l := b.langOf(&q.From)
	if !b.isOwner(q.From.ID) || q.Message == nil {
		answer("", false)
		return
	}
	tag := strings.TrimPrefix(q.Data, "up:go:")
	if !b.updating.CompareAndSwap(false, true) {
		answer(l.t("یه آپدیت در جریانه"), true)
		return
	}
	answer("", false)
	chatID, msgID := q.Message.Chat.ID, q.Message.MessageID
	_ = b.api.EditMarkup(ctx, chatID, msgID, nil)
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		defer b.updating.Store(false)
		uctx, cancel := b.ctx(10 * time.Minute)
		defer cancel()
		installed := false
		var steps []string
		show := func(s string) {
			steps = append(steps, "[*] "+s)
			_ = b.api.EditMessageText(uctx, chatID, msgID, "<pre>"+esc(strings.Join(steps, "\n"))+"</pre>", nil)
		}
		fail := func(err error) {
			log.Printf("update: %v", err)
			if installed {
				b.sendHTML(uctx, chatID, l.f("[!] نسخه نصب شد؛ راه‌اندازی مجدد ناموفق بود: %s\n<code>macron service restart</code>", esc(err.Error())), telegram.SendOptions{})
			} else {
				b.sendHTML(uctx, chatID, l.f("[-] آپدیت نشد: %s\nبرنامه نصب‌شده دست نخورد.", esc(err.Error())), telegram.SendOptions{})
			}
		}
		rel, err := updater.Latest(uctx, tag)
		if err != nil {
			fail(err)
			return
		}
		if !updater.Newer(rel.Version(), app.Version) {
			show(l.f("[+] تو آخرین نسخه‌ای (<code>v%s</code>).", app.Version))
			return
		}
		p, _ := json.Marshal(updatePending{Chat: chatID, Version: rel.Version(), From: app.Version})
		if err := b.st.SetSetting(uctx, settingUpdatePending, string(p)); err != nil {
			fail(err)
			return
		}
		if err := updater.Install(uctx, rel, show); err != nil {
			_ = b.st.DeleteSetting(uctx, settingUpdatePending)
			fail(err)
			return
		}
		installed = true
		self, _ := updater.SelfPath()
		how, err := restartLater(self, os.Geteuid() == 0)
		if err != nil {
			fail(err)
			return
		}
		log.Printf("update: v%s installed; the service restarts (%s)", rel.Version(), how)
		show(l.f("نصب شد؛ سرویس ری‌استارت می‌شه… (%s)", how))
	}()
}

// reportUpdate is said by the new process: the update it came from.
func (b *Bot) reportUpdate(ctx context.Context) {
	raw, ok := b.st.Setting(settingUpdatePending)
	if !ok || raw == "" {
		return
	}
	_ = b.st.DeleteSetting(ctx, settingUpdatePending)
	var p updatePending
	if json.Unmarshal([]byte(raw), &p) != nil || p.Chat == 0 {
		return
	}
	l := b.lang(p.Chat)
	if p.Version != app.Version {
		b.sendHTML(ctx, p.Chat, l.f("[!] بعد از ری‌استارت هنوز نسخه <code>v%s</code> اجرا می‌شه، نه <code>v%s</code>. <code>macron service status</code> رو ببین.", esc(app.Version), esc(p.Version)), telegram.SendOptions{})
		return
	}
	b.sendHTML(ctx, p.Chat, l.f("[+] آپدیت شد به <b>v%s</b> · سرویس فعال · uptime %ss", esc(app.Version), l.num(int(time.Since(b.started).Seconds()))), telegram.SendOptions{})
}

// runUpdateNotices looks for a new release once a day and tells the owners
// once per version.
func (b *Bot) runUpdateNotices(ctx context.Context) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		b.checkUpdateNotice(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (b *Bot) checkUpdateNotice(ctx context.Context) {
	if !b.st.SettingBool(settingUpdateNotice, true) {
		return
	}
	if last, ok := b.st.Setting(settingUpdateChecked); ok {
		if at, err := time.Parse(time.RFC3339, last); err == nil && time.Since(at) < updateCheckEvery {
			return
		}
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	rel, err := updater.Latest(cctx, "")
	_ = b.st.SetSetting(ctx, settingUpdateChecked, time.Now().UTC().Format(time.RFC3339))
	if err != nil || !updater.Newer(rel.Version(), app.Version) {
		return
	}
	if told, _ := b.st.Setting(settingUpdateNotified); told == rel.Version() {
		return
	}
	_ = b.st.SetSetting(ctx, settingUpdateNotified, rel.Version())
	for owner := range b.owner {
		l := b.lang(owner)
		b.sendHTML(ctx, owner, l.f("[!] نسخه <b>v%s</b> موجوده — /update", esc(rel.Version())), telegram.SendOptions{})
	}
}
