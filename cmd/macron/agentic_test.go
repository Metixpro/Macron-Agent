package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Metixpro/macron-agent/pkg/app"
	"github.com/Metixpro/macron-agent/pkg/machine"
	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/telegram"
)

// ---------------------------------------------------------------- only final files reach the chat

func TestFileTrackerSendsOnlyFinalResults(t *testing.T) {
	sb, err := machine.New(machine.Config{BaseDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	ft := newFileTracker(sb, "s")
	step := func(tool string, files ...string) {
		ft.start(tool)
		for _, f := range files {
			_ = sb.WriteFile("s", f, []byte("data "+f))
		}
		ft.end(tool, "")
	}
	// Code cuts ten frames, then makes the final GIF: nothing is sent on its own…
	var frames []string
	for i := 0; i < 10; i++ {
		frames = append(frames, fmt.Sprintf("frame_%02d.png", i))
	}
	step("run_code", frames...)
	step("run_code", "final.gif")
	if got := ft.deliveries(nil); len(got) != 0 || len(ft.made()) != 0 {
		t.Errorf("intermediate files must not flood the chat: %v", got)
	}
	// …until the model hands over the result, or shows it in its answer.
	if got := ft.deliveries([]string{"final.gif", "gone.png"}); strings.Join(got, ",") != "final.gif" {
		t.Errorf("explicit result: %v", got)
	}
	if got := ft.placed("Here it is:\n![the animation](final.gif)\n```\n![not this](frame_01.png)\n```\n![web](https://x.io/a.png)"); strings.Join(got, ",") != "final.gif" {
		t.Errorf("placed: %v", got)
	}
	// A picture tool's output is not sent by itself either: it is only
	// pointed out to the model when it sends nothing.
	step("generate_image", "sunset.png")
	if got := ft.deliveries(nil); len(got) != 0 {
		t.Errorf("nothing is sent unless chosen: %v", got)
	}
	if got := ft.made(); strings.Join(got, ",") != "sunset.png" {
		t.Errorf("made: %v", got)
	}
	if got := ft.made("sunset.png"); len(got) != 0 {
		t.Errorf("made leaves out what was chosen: %v", got)
	}
	// Overwriting a picture counts as one change.
	ft.start("edit_image")
	time.Sleep(10 * time.Millisecond)
	_ = sb.WriteFile("s", "sunset.png", []byte("better"))
	ft.end("edit_image", "")
	if got := ft.made(); len(got) != 1 {
		t.Errorf("a changed file is tracked once: %v", got)
	}
}

func TestFileTrackerLooksInSubfolders(t *testing.T) {
	sb, err := machine.New(machine.Config{BaseDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	ws, _ := sb.Workspace("s")
	for _, d := range []string{"out/charts/2026", ".git/objects", "node_modules/x", "build/.cache", "src/__pycache__"} {
		if err := os.MkdirAll(filepath.Join(ws, filepath.FromSlash(d)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	ft := newFileTracker(sb, "s")
	ft.start("generate_chart")
	for _, f := range []string{"out/report.pdf", "out/charts/sales.png", "out/charts/2026/too_deep.png", ".git/objects/ab",
		"node_modules/x/index.js", "build/.cache/blob", "src/__pycache__/m.pyc", "out/.hidden.png"} {
		if err := sb.WriteFile("s", f, []byte("data")); err != nil {
			t.Fatal(err)
		}
	}
	ft.end("generate_chart", "")
	if got := strings.Join(ft.made(), ","); got != "out/charts/sales.png,out/report.pdf" {
		t.Fatalf("outputs in out/ and out/charts/ are found; VCS, caches, dependencies, dot files and depth 4 are not: %s", got)
	}
}

func TestCodeWorkDoesNotSpamTheChat(t *testing.T) {
	h := newHarness(t)
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		if n < 5 {
			return toolCall("write_file", fmt.Sprintf(`{"filename":"frame_%d.png","content":"x"}`, n)), 0
		}
		return map[string]any{"role": "assistant", "content": "تمومه."}, 0
	}
	h.send(private(50, "این گیف رو بررسی کن"))
	for _, m := range []string{"sendPhoto", "sendDocument", "sendMediaGroup"} {
		if n := len(h.tg.of(m)); n != 0 {
			t.Errorf("%d × %s: work files must stay in the workspace", n, m)
		}
	}
}

func TestResultsArriveInsideTheAnswer(t *testing.T) {
	h := newHarness(t)
	for _, f := range []string{"a.png", "b.png", "c.jpg"} {
		_ = h.bot.app.Machine.WriteFile(sessionFor(51), f, testJPEG(40, 40))
	}
	_ = h.bot.app.Machine.WriteFile(sessionFor(51), "report.pdf", []byte("%PDF-1.4"))
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		files := []string{"a.png", "b.png", "c.jpg", "report.pdf"}
		if n < len(files) {
			return toolCall(toolSendFile, fmt.Sprintf(`{"filename":%q}`, files[n])), 0
		}
		return map[string]any{"role": "assistant", "content": "سه تا نسخه ساختم."}, 0
	}
	h.send(private(51, "سه نسخه بساز"))
	rich := h.tg.of("sendRichMessage")
	if len(rich) != 1 {
		t.Fatalf("one message carries the text and the files: %d\n%s", len(rich), h.tg.allText())
	}
	body := rich[0].body
	md := body["rich_message"].(map[string]any)["markdown"].(string)
	want := "<tg-slideshow>\n![](tg://photo?id=m0)\n![](tg://photo?id=m1)\n![](tg://photo?id=m2)\n</tg-slideshow>\n\nسه تا نسخه ساختم.\n\n![](tg://document?id=m3)"
	if md != want {
		t.Errorf("pictures swipe on top, the document is at the end:\n%s", md)
	}
	media := fmt.Sprint(body["rich_message"].(map[string]any)["media"])
	if !strings.Contains(media, "attach://m3") || !strings.Contains(media, "type:document") || body["m2_name"] != "c.jpg" || body["m3_name"] != "report.pdf" {
		t.Errorf("the files are uploaded with the message: %v %v", media, body)
	}
	if rp, ok := body["reply_parameters"].(map[string]any); !ok || rp["message_id"] == nil {
		t.Error("the answer replies to the request")
	}
	for _, m := range []string{"sendPhoto", "sendDocument", "sendMediaGroup"} {
		if n := len(h.tg.of(m)); n != 0 {
			t.Errorf("%d × %s: nothing is sent separately", n, m)
		}
	}
	if mem := llmText(h.llm.last()); strings.Contains(mem, "tg://") {
		t.Error("internal links never reach the model")
	}
}

// sendFiles is one model turn handing over several files at once.
func sendFiles(names ...string) map[string]any {
	var calls []any
	for i, n := range names {
		calls = append(calls, map[string]any{"id": fmt.Sprintf("c%d", i), "type": "function",
			"function": map[string]any{"name": toolSendFile, "arguments": fmt.Sprintf(`{"filename":%q}`, n)}})
	}
	return map[string]any{"role": "assistant", "tool_calls": calls}
}

func lastRole(req map[string]any) string {
	msgs, _ := req["messages"].([]any)
	if len(msgs) == 0 {
		return ""
	}
	role, _ := msgs[len(msgs)-1].(map[string]any)["role"].(string)
	return role
}

func TestModelPlacesFilesInItsText(t *testing.T) {
	h := newHarness(t)
	s := sessionFor(53)
	_ = h.bot.app.Machine.WriteFile(s, "before.jpg", testJPEG(40, 40))
	_ = h.bot.app.Machine.WriteFile(s, "after.png", testJPEG(40, 40))
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		if n == 0 {
			return toolCall(toolSendFile, `{"filename":"after.png"}`), 0
		}
		return map[string]any{"role": "assistant", "content": "قبل:\n![عکس اصلی](before.jpg)\n\nبعد:\n![](./after.png \"بدون \\\"واترمارک\")\n\n![x](missing.png)\n\n```md\n![a](after.png)\n```"}, 0
	}
	h.send(private(53, "واترمارک رو پاک کن"))
	rich := h.tg.of("sendRichMessage")
	if len(rich) != 1 {
		t.Fatalf("messages: %d", len(rich))
	}
	md := rich[0].body["rich_message"].(map[string]any)["markdown"].(string)
	for _, want := range []string{`![](tg://photo?id=m0 "عکس اصلی")`, `![](tg://photo?id=m1 "بدون`, "*x*", "```md\n![a](after.png)\n```"} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown misses %q:\n%s", want, md)
		}
	}
	if strings.Contains(md, "<tg-slideshow>") || rich[0].body["m0_name"] != "before.jpg" || rich[0].body["m1_name"] != "after.png" {
		t.Errorf("placed files are not repeated on top: %s %v", md, rich[0].body)
	}
}

func TestFilesFollowAsAlbumsWhenTheMessageCannotCarryThem(t *testing.T) {
	h := newHarness(t)
	h.tg.refuseFiles(true)
	s := sessionFor(54)
	for _, f := range []string{"a.png", "b.png", "c.jpg"} {
		_ = h.bot.app.Machine.WriteFile(s, f, testJPEG(40, 40))
	}
	_ = h.bot.app.Machine.WriteFile(s, "report.pdf", []byte("%PDF-1.4"))
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		if lastRole(req) == "user" {
			return sendFiles("a.png", "b.png", "c.jpg", "report.pdf"), 0
		}
		return map[string]any{"role": "assistant", "content": "سه تا نسخه:\n![اولی](a.png)"}, 0
	}
	for i := 0; i < maxMediaStrikes+1; i++ {
		h.tg.reset()
		h.send(private(54, "سه نسخه بساز"))
		rich := h.tg.of("sendRichMessage")
		text := rich[len(rich)-1].body["rich_message"].(map[string]any)["markdown"]
		if text != "سه تا نسخه:  \n*اولی*" { // its line break kept (telegram.HardBreaks)
			t.Errorf("round %d: the text still arrives, without file links: %q", i, text)
		}
		if tries := len(rich) - 1; i < maxMediaStrikes && tries != 1 || i == maxMediaStrikes && tries != 0 {
			t.Errorf("round %d: %d tries with files", i, tries)
		}
		albums := h.tg.of("sendMediaGroup")
		if len(albums) != 1 || !strings.Contains(fmt.Sprint(albums[0].body["media"]), `"type":"photo"`) || albums[0].body["f0_name"] != "a.png" {
			t.Fatalf("round %d: the photos follow as one album: %v", i, albums)
		}
		if docs := h.tg.of("sendDocument"); len(docs) != 1 {
			t.Errorf("round %d: the PDF follows on its own: %d", i, len(docs))
		}
	}
}

func TestAlbumGroups(t *testing.T) {
	h := newHarness(t)
	s := sessionFor(52)
	names := []string{"1.png", "2.mp4", "3.mp3", "4.pdf", "5.jpg", "6.ogg", "7.zip"}
	for _, n := range names {
		_ = h.bot.app.Machine.WriteFile(s, n, []byte("x"))
	}
	var got []string
	for _, g := range h.bot.albumGroups(s, names) {
		got = append(got, g.kind+":"+strings.Join(g.types, "+"))
	}
	if strings.Join(got, " ") != "visual:photo+video+photo audio:audio+audio document:document+document" {
		t.Errorf("groups: %v", got)
	}

	// A voice message and a round video message travel alone, with their own methods.
	native := []string{"a.jpg", "hi.voice.ogg", "me.round.mp4", "b.jpg"}
	for _, n := range native {
		_ = h.bot.app.Machine.WriteFile(s, n, []byte("x"))
	}
	got = nil
	for _, g := range h.bot.albumGroups(s, native) {
		got = append(got, g.kind+":"+strings.Join(g.types, "+"))
	}
	if strings.Join(got, " ") != "visual:photo+photo native:voice native:video_note" {
		t.Errorf("native groups: %v", got)
	}
	h.tg.reset()
	h.bot.sendBatch(t.Context(), 52, s, []string{"hi.voice.ogg", "me.round.mp4"}, map[string]string{"me.round.mp4": "**hello**"}, telegram.SendOptions{})
	if len(h.tg.of("sendVoice")) != 1 || len(h.tg.of("sendVideoNote")) != 1 || len(h.tg.of("sendAudio")) != 0 || len(h.tg.of("sendVideo")) != 0 {
		t.Errorf("sent with sendVoice and sendVideoNote: %v", h.tg.allText())
	}
	if !strings.Contains(h.tg.allText(), "hello") {
		t.Error("a round video's caption follows it as a message")
	}
}

// ---------------------------------------------------------------- GIFs and stickers are seen, not cut up

func TestGIFArrivesWithAFrameToLookAt(t *testing.T) {
	h := newHarness(t)
	h.tg.files["gif"] = []byte("fake mp4")
	h.tg.files["thumb"] = testJPEG(90, 90)
	m := private(53, "")
	// Telegram also fills document for animations.
	m.Animation = &telegram.Video{FileID: "gif", Duration: 3, Thumbnail: &telegram.PhotoSize{FileID: "thumb"}}
	m.Document = &telegram.Document{FileID: "gif", FileName: "funny.gif.mp4", MimeType: "video/mp4"}
	h.send(m)
	req := llmText(h.llm.last())
	for _, want := range []string{"GIF animation, saved in the workspace as animation_77.mp4", "A still frame of it is attached below", "data:image/jpeg;base64,", `"(یه گیف فرستاد)"`} {
		if !strings.Contains(req, want) {
			t.Errorf("model request misses %q", want)
		}
	}
	if strings.Contains(req, "funny.gif.mp4") {
		t.Error("an animation must not be handled as a plain document")
	}
}

func TestStickerIsSavedAndSeen(t *testing.T) {
	h := newHarness(t)
	h.tg.files["st"] = []byte("RIFF\x00\x00\x00\x00WEBPVP8 fake")
	m := private(54, "")
	m.Sticker = &telegram.Sticker{FileID: "st", Emoji: "😂", Type: "regular"}
	h.send(m)
	req := llmText(h.llm.last())
	if !strings.Contains(req, "a sticker 😂 (saved in the workspace as sticker_77.webp)") || !strings.Contains(req, "data:image/webp;base64,") {
		t.Errorf("the sticker is saved and shown to the model:\n%s", req)
	}
}

func TestFramePreviewMakesAGrid(t *testing.T) {
	sb, err := machine.New(machine.Config{BaseDir: t.TempDir()})
	if err != nil {
		t.Fatalf("machine: %v", err)
	}
	if !sb.Available("ffmpeg")["ffmpeg"] {
		t.Skip("ffmpeg is not available here")
	}
	res := sb.Run(context.Background(), "v", machine.RunOptions{Args: []string{"ffmpeg", "-v", "error", "-f", "lavfi", "-i",
		"testsrc=duration=4:size=640x360:rate=10", "-y", "clip.mp4"}, Timeout: 30 * time.Second})
	if res.ExitCode != 0 {
		t.Skipf("ffmpeg cannot make a test clip: %s", res.Stderr)
	}
	b := &Bot{app: &app.App{Machine: sb}}
	url, desc := b.framePreview(context.Background(), "v", "clip.mp4", 4, nil, false)
	if url == "" || !strings.Contains(desc, "2×2 grid") {
		t.Fatalf("preview: %q %q", url, desc)
	}
	data, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(url, "data:image/jpeg;base64,"))
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width < 600 || cfg.Width > 700 {
		t.Errorf("a 2×2 grid of 320 px frames: %+v %v", cfg, err)
	}
	if files, _ := sb.List("v", 3, 50); len(files) != 1 {
		t.Errorf("the preview leaves nothing behind: %v", files)
	}
}

// ---------------------------------------------------------------- asking the user with a poll

func TestParsePollArgs(t *testing.T) {
	q, err := parsePollArgs(map[string]any{"question": " کدوم سبک؟ ", "options": []any{"مینیمال", "رنگی", "مینیمال", " ", "کلاسیک"}})
	if err != nil || q.Question != "کدوم سبک؟" || strings.Join(q.Options, "|") != "مینیمال|رنگی|کلاسیک" {
		t.Errorf("parse: %+v %v", q, err)
	}
	for _, bad := range []map[string]any{{"question": "x"}, {"question": "", "options": []any{"a", "b"}}, {"question": "x", "options": []any{"a", "A"}}} {
		if _, err := parsePollArgs(bad); err == nil {
			t.Errorf("%v must be refused", bad)
		}
	}
}

func TestAskUserSendsAPollAndTheVoteContinuesTheTask(t *testing.T) {
	h := newHarness(t)
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		if n == 0 {
			msg := toolCall(toolAskUser, `{"question":"لوگو با چه سبکی باشه؟","options":["مینیمال","رنگی","کلاسیک"]}`)
			msg["content"] = "قبل از ساختنش یه سؤال دارم 👇"
			return msg, 0
		}
		return map[string]any{"role": "assistant", "content": "حله، سبک رنگی انتخاب شد 🎨"}, 0 // not a claim of work no tool did
	}
	h.send(private(55, "یه لوگو برام بساز"))

	if h.llm.count() != 1 {
		t.Fatalf("the turn must end with the question; model calls: %d", h.llm.count())
	}
	polls := h.tg.of("sendPoll")
	if len(polls) != 1 || polls[0].body["is_anonymous"] != false || !strings.Contains(fmt.Sprint(polls[0].body["options"]), "رنگی") {
		t.Fatalf("a non-anonymous poll is sent: %v", polls)
	}
	h.wantText("قبل از ساختنش یه سؤال دارم")
	h.noText("انجام شد ✅")
	mem, _ := h.bot.mem.Load(bg, 55)
	if last := mem.History[len(mem.History)-1].Content; !strings.Contains(last, "[I asked with a poll: «لوگو با چه سبکی باشه؟»") {
		t.Errorf("the question is remembered: %q", last)
	}
	used, _ := h.st.UsedToday(bg, 55)
	p := latestPoll(t, h, 55)

	// Someone else votes (in a group anyone can): ignored.
	h.bot.handlePollAnswer(&telegram.PollAnswer{PollID: p.PollID, User: &telegram.User{ID: 999, FirstName: "x"}, OptionIDs: []int{0}})
	h.bot.wg.Wait()
	if h.llm.count() != 1 {
		t.Fatal("only the person who was asked continues the task")
	}

	h.tg.reset()
	h.bot.handlePollAnswer(&telegram.PollAnswer{PollID: p.PollID, User: &telegram.User{ID: 55, FirstName: "Ali"}, OptionIDs: []int{1}})
	h.bot.wg.Wait()
	req := llmText(h.llm.last())
	if !strings.Contains(req, "The user answered your poll «لوگو با چه سبکی باشه؟» by choosing: رنگی") || !strings.Contains(req, "انتخاب کردم: رنگی") {
		t.Errorf("the vote reaches the model:\n%s", req)
	}
	// The job that asked goes on as it stopped: its own call to ask_user and
	// what it answered are still there, not a summary of them.
	if !strings.Contains(req, `"name":"ask_user"`) || !strings.Contains(req, "The user answered your question. The job goes on") {
		t.Errorf("the vote resumes the job itself:\n%s", req)
	}
	if len(h.tg.of("stopPoll")) != 1 {
		t.Error("the poll is closed after the vote")
	}
	rich := h.tg.of("sendRichMessage")
	if len(rich) != 1 || rich[0].body["reply_parameters"].(map[string]any)["message_id"].(float64) != float64(p.MessageID) {
		t.Errorf("the answer replies to the poll: %v", rich)
	}
	if after, _ := h.st.UsedToday(bg, 55); after != used {
		t.Errorf("a vote continues a request and is not charged: %d → %d", used, after)
	}

	// A second vote on the same poll does nothing.
	h.bot.handlePollAnswer(&telegram.PollAnswer{PollID: p.PollID, User: &telegram.User{ID: 55}, OptionIDs: []int{2}})
	h.bot.wg.Wait()
	if h.llm.count() != 2 {
		t.Error("a poll is answered once")
	}
}

func TestPollFallsBackToText(t *testing.T) {
	h := newHarness(t)
	h.tg.fail["sendPoll"], h.tg.desc["sendPoll"] = 400, "Bad Request: poll can't be sent"
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		return toolCall(toolAskUser, `{"question":"کدوم؟","options":["اولی","دومی"]}`), 0
	}
	h.send(private(56, "یکی رو برام انتخاب کن"))
	h.wantText("1. اولی")
	h.wantText("شماره یا اسم گزینه‌ات رو بفرست")
}

// latestPoll finds the recorded poll of a user (the fake names polls
// "poll-<message id>").
func latestPoll(t *testing.T, h *harness, userID int64) *store.Poll {
	t.Helper()
	for id := int64(500); id < 700; id++ {
		if p, err := h.st.Poll(bg, fmt.Sprintf("poll-%d", id)); err == nil && p.UserID == userID {
			return p
		}
	}
	t.Fatal("no poll was recorded")
	return nil
}

func TestFilesCarryTheModelsCaption(t *testing.T) {
	h := newHarness(t)
	s := sessionFor(55)
	_ = h.bot.app.Machine.WriteFile(s, "rose.mp3", []byte("ID3fake"))
	_ = h.bot.app.Machine.WriteFile(s, "notes.txt", []byte("hello"))
	_ = h.bot.app.Machine.WriteFile(s, "a.png", testJPEG(20, 20))
	_ = h.bot.app.Machine.WriteFile(s, "b.png", testJPEG(20, 20))
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		if lastRole(req) == "user" {
			call := func(id, args string) map[string]any {
				return map[string]any{"id": id, "type": "function", "function": map[string]any{"name": toolSendFile, "arguments": args}}
			}
			return map[string]any{"role": "assistant", "tool_calls": []any{
				call("c1", `{"filename":"rose.mp3","caption":"🎵 **رز سفید** — حامیم"}`),
				call("c2", `{"filename":"notes.txt"}`),
				call("c3", `{"filename":"a.png","caption":"نسخه \"اول\""}`),
				call("c4", `{"filename":"b.png"}`),
			}}, 0
		}
		return map[string]any{"role": "assistant", "content": "بفرما"}, 0
	}

	// Inside the answer: the caption sits under the file.
	h.send(private(55, "آهنگ رو بفرست"))
	md := lastOf(h, "sendRichMessage").body["rich_message"].(map[string]any)["markdown"].(string)
	for _, want := range []string{`![](tg://audio?id=`, `"🎵 رز سفید — حامیم")`, `"نسخه 'اول'")`, `![](tg://document?id=`} {
		if !strings.Contains(md, want) {
			t.Errorf("markdown misses %q:\n%s", want, md)
		}
	}

	// Sent separately: every kind of file keeps its caption.
	h.tg.refuseFiles(true)
	h.tg.reset()
	h.send(private(55, "آهنگ رو بفرست"))
	audio := lastOf(h, "sendAudio")
	if audio.body["caption"] != "🎵 <b>رز سفید</b> — حامیم" || audio.body["parse_mode"] != "HTML" {
		t.Errorf("audio caption: %v", audio.body)
	}
	if doc := lastOf(h, "sendDocument"); doc.body["caption"] != "📄 notes.txt" {
		t.Errorf("a document without a caption keeps its name: %v", doc.body["caption"])
	}
	album := fmt.Sprint(lastOf(h, "sendMediaGroup").body["media"])
	if !strings.Contains(album, `"caption":"نسخه \"اول\""`) {
		t.Errorf("album item caption: %s", album)
	}
}

// A vote that comes while the user has another job going waits for it.
func TestAVoteWhileBusyIsNotLost(t *testing.T) {
	h := newHarness(t)
	old := pollWait
	pollWait = 10 * time.Second
	t.Cleanup(func() { pollWait = old })
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		if n == 0 {
			return toolCall(toolAskUser, `{"question":"رنگ؟","options":["آبی","سبز"]}`), 0
		}
		return map[string]any{"role": "assistant", "content": "باشه " + fmt.Sprint(n)}, 0
	}
	h.send(private(58, "یه طرح بزن"))
	p := latestPoll(t, h, 58)
	release, _ := h.bot.gate.TryAcquire("58") // another job of the user, in another chat
	go func() {
		time.Sleep(4500 * time.Millisecond) // longer than a request's own wait for a busy user
		release()
	}()
	h.bot.handlePollAnswer(&telegram.PollAnswer{PollID: p.PollID, User: &telegram.User{ID: 58, FirstName: "Ali"}, OptionIDs: []int{1}})
	h.bot.wg.Wait()
	if !strings.Contains(llmText(h.llm.last()), "انتخاب کردم: سبز") {
		t.Error("the vote runs once the other job is done")
	}
	h.noText(textBusy)
}
