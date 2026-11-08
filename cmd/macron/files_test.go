package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"
)

// buttons returns "label → data" for each inline button of a call.
func buttons(c tgCall) []string {
	var out []string
	kb, _ := c.body["reply_markup"].(map[string]any)
	rows, _ := kb["inline_keyboard"].([]any)
	for _, r := range rows {
		for _, b := range r.([]any) {
			btn := b.(map[string]any)
			out = append(out, fmt.Sprint(btn["text"], " → ", btn["callback_data"]))
		}
	}
	return out
}

func lastOf(h *harness, method string) tgCall {
	h.t.Helper()
	calls := h.tg.of(method)
	if len(calls) == 0 {
		h.t.Fatalf("no %s call", method)
	}
	return calls[len(calls)-1]
}

func hasButton(list []string, sub string) bool {
	for _, b := range list {
		if strings.Contains(b, sub) {
			return true
		}
	}
	return false
}

func TestFilesPanel(t *testing.T) {
	h := newHarness(t)
	const uid = 61
	s := sessionFor(uid)
	sb := h.bot.app.Machine
	for i := 1; i <= 12; i++ {
		_ = sb.WriteFile(s, fmt.Sprintf("pic_%02d.png", i), testJPEG(20, 20))
	}
	_ = sb.WriteFile(s, "app.py", []byte("print(1)\n"))
	_ = sb.WriteFile(s, "data.json", []byte("{}"))
	_ = sb.WriteFile(s, "report.pdf", []byte("%PDF-1.4"))
	_ = sb.WriteFile(s, "work/frame_001.png", testJPEG(10, 10))
	_ = sb.WriteFile(s, ".pkgs/lib.py", []byte("x"))
	id := fmt.Sprint(uid)

	h.send(private(uid, "/files"))
	home := lastOf(h, "sendMessage")
	text := home.body["text"].(string)
	for _, want := range []string{"۱۵ فایل", "۱ فایل هم توی پوشه‌هاست"} {
		if !strings.Contains(text, want) {
			t.Errorf("home misses %q:\n%s", want, text)
		}
	}
	btns := buttons(home)
	for _, want := range []string{"🖼 تصویرها (۱۲) → f:c:" + id + ":img:0", "💻 کد و داده (۲) → f:c:" + id + ":code:0", "📄 اسناد (۱)",
		"f:c:" + id + ":all:0", "f:z:" + id, "f:w:" + id} {
		if !hasButton(btns, want) {
			t.Errorf("home has no button %q: %v", want, btns)
		}
	}
	if hasButton(btns, "سایر") || hasButton(btns, "فشرده") {
		t.Errorf("empty categories are hidden: %v", btns)
	}

	// A category, a page at a time.
	h.click(uid, uid, 900, "f:c:"+id+":img:0")
	page := lastOf(h, "editMessageText")
	btns = buttons(page)
	files := 0
	for _, b := range btns {
		if strings.Contains(b, "→ f:s:") {
			files++
		}
	}
	if files != filesPerPage || !hasButton(btns, "بعدی » → f:c:"+id+":img:1") || hasButton(btns, "قبلی") {
		t.Errorf("first page: %v", btns)
	}
	if !strings.Contains(page.body["text"].(string), "صفحه ۱ از ۲") {
		t.Errorf("page header: %s", page.body["text"])
	}
	h.click(uid, uid, 900, "f:c:"+id+":img:1")
	btns = buttons(lastOf(h, "editMessageText"))
	if !hasButton(btns, "« قبلی → f:c:"+id+":img:0") || hasButton(btns, "بعدی") {
		t.Errorf("second page: %v", btns)
	}

	// Someone else's panel does nothing.
	h.tg.reset()
	h.click(99, uid, 900, "f:s:"+id+":"+fileHash("pic_01.png")+":img:1")
	if len(h.tg.of("sendPhoto")) != 0 || !strings.Contains(fmt.Sprint(lastOf(h, "answerCallbackQuery").body["text"]), "مال کس دیگه‌ای است") {
		t.Error("only the owner of the panel can use it")
	}

	// Tapping a file sends that one file.
	h.click(uid, uid, 900, "f:s:"+id+":"+fileHash("pic_01.png")+":img:1")
	if p := h.tg.of("sendPhoto"); len(p) != 1 || p[0].body["photo_name"] != "pic_01.png" {
		t.Fatalf("one tap, one file: %v", p)
	}

	// The whole category, in albums, all of them.
	h.tg.reset()
	h.click(uid, uid, 900, "f:a:"+id+":img")
	albums := h.tg.of("sendMediaGroup")
	if len(albums) != 2 || albums[0].body["f0_name"] != "pic_01.png" || albums[1].body["f1_name"] != "pic_12.png" {
		t.Errorf("12 pictures: an album of 10 and one of 2, oldest first: %d albums", len(albums))
	}

	// Delete mode.
	h.click(uid, uid, 900, "f:d:"+id+":code:0")
	btns = buttons(lastOf(h, "editMessageText"))
	if !hasButton(btns, "🗑 app.py → f:r:"+id+":"+fileHash("app.py")+":code:0") || !hasButton(btns, "✅ تموم شد") {
		t.Errorf("delete mode: %v", btns)
	}
	h.click(uid, uid, 900, "f:r:"+id+":"+fileHash("app.py")+":code:0")
	if _, err := sb.Stat(s, "app.py"); err == nil {
		t.Error("app.py must be deleted")
	}
	if btns = buttons(lastOf(h, "editMessageText")); hasButton(btns, "app.py") || !hasButton(btns, "data.json") {
		t.Errorf("the page is refreshed: %v", btns)
	}

	// Everything as one zip, folders included, installed packages left out.
	h.tg.reset()
	h.click(uid, uid, 900, "f:z:"+id)
	doc := lastOf(h, "sendDocument")
	if name := fmt.Sprint(doc.body["document_name"]); !strings.HasPrefix(name, "workspace_") || !strings.HasSuffix(name, ".zip") {
		t.Errorf("zip name: %s", name)
	}
	if !strings.Contains(fmt.Sprint(doc.body["caption"]), "۱۵ فایل") {
		t.Errorf("zip caption: %v", doc.body["caption"])
	}

	// Emptying asks first, then keeps only the hidden packages.
	h.click(uid, uid, 900, "f:w:"+id)
	if !hasButton(buttons(lastOf(h, "editMessageText")), "f:W:"+id) {
		t.Error("emptying needs a confirmation")
	}
	h.click(uid, uid, 900, "f:W:"+id)
	left, _ := sb.List(s, 8, 100)
	var names []string
	for _, f := range left {
		names = append(names, f.Name)
	}
	sort.Strings(names)
	if strings.Join(names, ",") != ".pkgs,.pkgs/lib.py" {
		t.Errorf("left after emptying: %v", names)
	}
	h.tg.reset()
	h.send(private(uid, "/files"))
	h.wantText(textNoFiles)
}

func TestZipContents(t *testing.T) {
	h := newHarness(t)
	s := sessionFor(62)
	_ = h.bot.app.Machine.WriteFile(s, "a.txt", []byte(strings.Repeat("hello ", 100)))
	_ = h.bot.app.Machine.WriteFile(s, "work/b.png", testJPEG(10, 10))
	_ = h.bot.app.Machine.WriteFile(s, ".pkgs/x", []byte("x"))
	var buf bytes.Buffer
	files, _ := h.bot.app.Machine.List(s, 8, 100)
	var list = files[:0]
	for _, f := range files {
		if !f.IsDir && !hiddenPath(f.Name) {
			list = append(list, f)
		}
	}
	n, err := h.bot.writeZip(&buf, s, list)
	if err != nil || n != 2 {
		t.Fatalf("zip: %d files, %v", n, err)
	}
	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]uint16{}
	for _, f := range zr.File {
		got[f.Name] = f.Method
		rc, _ := f.Open()
		data, _ := io.ReadAll(rc)
		rc.Close()
		if f.Name == "a.txt" && string(data) != strings.Repeat("hello ", 100) {
			t.Error("a.txt content")
		}
	}
	if got["a.txt"] != zip.Deflate || got["work/b.png"] != zip.Store || len(got) != 2 {
		t.Errorf("zip entries: %v", got)
	}
}

func TestFileJobsDoNotOverlap(t *testing.T) {
	h := newHarness(t)
	const uid = 63
	_ = h.bot.app.Machine.WriteFile(sessionFor(uid), "a.pdf", []byte("%PDF"))
	// A slow Telegram keeps the first upload busy.
	release := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/sendDocument") {
			<-release
		}
		h.tg.ServeHTTP(w, r)
	}))
	defer slow.Close()
	h.bot.api.APIURL = slow.URL + "/bot" + h.bot.api.Token
	id := fmt.Sprint(uid)
	h.clickAsync(uid, uid, 900, "f:s:"+id+":"+fileHash("a.pdf")+":doc:0")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, busy := h.bot.fileJobs.Load(int64(uid)); busy || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	h.clickAsync(uid, uid, 900, "f:s:"+id+":"+fileHash("a.pdf")+":doc:0")
	close(release)
	h.bot.wg.Wait()
	if n := len(h.tg.of("sendDocument")); n != 1 {
		t.Errorf("a second tap while sending is refused: %d uploads", n)
	}
	if !strings.Contains(fmt.Sprint(lastOf(h, "answerCallbackQuery").body["text"]), "هنوز دارم") {
		t.Error("the user is told to wait")
	}
}
