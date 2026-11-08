package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/telegram"
)

func TestProjectWorkflowThroughTheBot(t *testing.T) {
	h := newHarness(t)
	h.st.SetSetting(bg, store.SettingAgentSteps, "10")
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range map[string]string{"shop/app.py": "def total(items):\n    # TODO: tax\n    return sum(items)\n", "shop/README.md": "# Shop\n"} {
		w, _ := zw.Create(name)
		w.Write([]byte(body))
	}
	zw.Close()
	h.tg.mu.Lock()
	h.tg.files["projzip"] = buf.Bytes()
	h.tg.mu.Unlock()

	savedRe := regexp.MustCompile(`saved in the workspace as (\S+\.zip)`)
	results := map[int]string{}
	call := func(name string, args map[string]any) map[string]any {
		b, _ := json.Marshal(args)
		return toolCall(name, string(b))
	}
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		text := llmText(req)
		results[n] = text
		switch n {
		case 0:
			m := savedRe.FindStringSubmatch(text)
			if m == nil || !strings.Contains(text, "It is an archive") {
				t.Errorf("the model is told the upload is an archive:\n%s", text)
				return map[string]any{"role": "assistant", "content": "?"}, 0
			}
			return call("extract_archive", map[string]any{"filename": m[1], "dest": "shop"}), 0
		case 1:
			return call("search_code", map[string]any{"pattern": "TODO", "path": "shop"}), 0
		case 2:
			return call("edit_file", map[string]any{"filename": "shop/app.py", "old_text": "    # TODO: tax\n    return sum(items)",
				"new_text": "    return round(sum(items) * 1.09, 2)"}), 0
		case 3:
			return call("pack_files", map[string]any{"path": "shop", "filename": "shop_fixed.zip"}), 0
		case 4:
			return call(toolSendFile, map[string]any{"filename": "shop_fixed.zip", "caption": "پروژه درست‌شده"}), 0
		}
		return map[string]any{"role": "assistant", "content": "مالیات اضافه شد و پروژه رو فرستادم."}, 0
	}
	m := private(80, "مالیات ۹٪ رو به این پروژه اضافه کن")
	m.Document = &telegram.Document{FileID: "projzip", FileName: "shop.zip", MimeType: "application/zip", FileSize: int64(buf.Len())}
	h.send(m)

	if !strings.Contains(results[1], "Unpacked 2 files") || !strings.Contains(results[2], "shop/app.py") ||
		!strings.Contains(results[3], "Edited shop/app.py: 1 change(s)") || !strings.Contains(results[4], "Created shop_fixed.zip: 2 files") {
		t.Fatalf("the steps did not go through:\n1: %.300s\n2: %.300s\n3: %.300s\n4: %.300s", results[1], results[2], results[3], results[4])
	}
	data, _, _ := h.bot.app.Machine.ReadFile(sessionFor(80), "shop/app.py", 1<<20)
	if !strings.Contains(string(data), "1.09") || strings.Contains(string(data), "TODO") {
		t.Errorf("edited file:\n%s", data)
	}
	rich := richAnswers(h)
	if len(rich) == 0 || rich[len(rich)-1].body["m0_name"] != "shop_fixed.zip" {
		t.Fatalf("the packed project is delivered: %v", rich)
	}
	h.wantText("مالیات اضافه شد")
}
