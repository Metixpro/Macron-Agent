package main

import (
	"strings"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/telegram"
)

// Telegram's own servers hand bots files up to 20 MB; a local Bot API server
// up to 2000 MB. A file that is too big, or that the workspace has no room
// for, is turned away with the reason.
func TestBigFilesFollowTheBotAPIServer(t *testing.T) {
	h := newHarness(t)
	h.tg.files["big"] = []byte("some data")
	doc := func(uid int64, size int64) *telegram.Message {
		m := private(uid, "این فایل رو باز کن")
		m.Document = &telegram.Document{FileID: "big", FileName: "data.bin", MimeType: "application/octet-stream", FileSize: size}
		return m
	}
	h.send(doc(80, 300<<20))
	if all := h.tg.allText(); !strings.Contains(all, "۲۰ مگابایت") {
		t.Errorf("through api.telegram.org: up to 20 MB:\n%s", all)
	}

	h.bot.api.SetLocal(true)
	defer h.bot.api.SetLocal(false)
	h.tg.reset()
	h.send(doc(81, 300<<20))
	if all := h.tg.allText(); strings.Contains(all, "خیلی بزرگه") {
		t.Errorf("a local server takes 300 MB:\n%s", all)
	}
	if _, err := h.bot.app.Machine.Stat(sessionFor(81), "data.bin"); err != nil {
		t.Error("the file is in the workspace")
	}
	h.tg.reset()
	h.send(doc(82, 2100<<20))
	if all := h.tg.allText(); !strings.Contains(all, "۲ گیگابایت") {
		t.Errorf("over 2000 MB:\n%s", all)
	}

}
