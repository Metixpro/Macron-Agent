package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/machine"
	"github.com/Metixpro/macron-agent/pkg/telegram"
	"github.com/Metixpro/macron-agent/pkg/tools"
)

// The song tool in the bot: offered only with a key, made through the service,
// and delivered as an audio message with its title and singer.
func TestASongIsMadeAndDeliveredAsAudio(t *testing.T) {
	h := newHarness(t)
	var created atomic.Int32
	svc := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, ".mp3") { // the files are public: the key is never sent to them
			if r.Header.Get("X-API-Key") != "" {
				t.Error("the key went to the file host")
			}
			w.Header().Set("Content-Type", "audio/mpeg")
			_, _ = w.Write([]byte("ID3\x03\x00\x00\x00\x00\x00\x00" + strings.Repeat("\xff\xfb\x90\x00", 900)))
			return
		}
		if r.Header.Get("X-API-Key") != "panel-song-key" {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"status":false,"error":"Invalid or missing API key"}`)
			return
		}
		switch {
		case r.Method == http.MethodPost:
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["lyrics"] == nil {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, `{"status":false,"error":"Missing user_id or callback_url"}`)
				return
			}
			created.Add(1)
			fmt.Fprintf(w, `{"status":true,"task_id":"s1","check_status_url":"http://%s/music/?task_id=s1"}`, r.Host)
		case strings.HasSuffix(r.URL.Path, ".mp3"):
			w.Header().Set("Content-Type", "audio/mpeg")
			_, _ = w.Write([]byte("ID3\x03\x00\x00\x00\x00\x00\x00" + strings.Repeat("\xff\xfb\x90\x00", 900)))
		default:
			fmt.Fprintf(w, `{"status":true,"task":{"status":"success","songs":[{"status":"complete","audio_url":"http://%s/g/a.mp3","duration":120}]}}`, r.Host)
		}
	}))
	defer svc.Close()
	reg := h.bot.app.Registry
	reg.HTTP = &http.Client{} // a plain client
	reg.SetMusicAPI(svc.URL + "/music/index.php")
	h.bot.app.Config.MusicAPI = svc.URL + "/music/index.php"

	// Without a key the model is not offered the tool, and the panel says so.
	h.llm.reply = say("سلام")
	h.send(private(61, "یه آهنگ با صدای خواننده برام بساز"))
	if names := toolNamesOf(h.llm.last()); strings.Contains(names, "generate_song") {
		t.Fatalf("a tool that cannot work is not offered: %s", names)
	}

	// The admin sets the key in the panel: it is checked without making a song.
	h.click(ownerID, ownerID, 900, "a:mu")
	if p := lastOf(h, "editMessageText"); !strings.Contains(fmt.Sprint(p.body["text"]), "تنظیم نشده") || !hasButton(buttons(p), "a:mu:key") {
		t.Fatalf("the panel without a key: %v", p.body["text"])
	}
	h.click(ownerID, ownerID, 900, "a:mu:key")
	h.tg.reset()
	bad := private(ownerID, "wrong-key")
	bad.MessageID = 5300
	h.send(bad)
	h.wantText("قبول نکرد")
	good := private(ownerID, "panel-song-key")
	good.MessageID = 5301
	h.click(ownerID, ownerID, 900, "a:mu:key")
	h.send(good)
	h.wantText("ذخیره شد")
	if created.Load() != 0 || !reg.SongReady() {
		t.Fatalf("checking a key makes no song (%d made); the key works at once: %v", created.Load(), reg.SongReady())
	}

	// Now the model has it, makes the song, and the user gets an audio message.
	h.tg.reset()
	h.llm.mu.Lock()
	h.llm.requests = nil
	h.llm.mu.Unlock()
	h.llm.reply = func(req map[string]any, n int) (map[string]any, int) {
		if n == 0 {
			return toolCall("generate_song", `{"lyrics":"[verse]\nباران می‌زنه روی شیشه شهر\nدلم می‌خواد برگردی دوباره\n\n[chorus]\nبمون کنارم تا صبح بشه\nشب تموم می‌شه و عشق می‌مونه","title":"باران","style":"Persian pop ballad, piano","gender":"female"}`), 0
		}
		if n == 1 { // the model chooses what is sent, as it always does
			return toolCall("send_file", `{"filename":"Macron_AI_-_باران.mp3","caption":"🎤 **باران**"}`), 0
		}
		return map[string]any{"role": "assistant", "content": "آهنگ آماده شد 🎤"}, 0
	}
	h.send(private(62, "یه آهنگ عاشقانه با صدای زن برام بساز"))
	if names := toolNamesOf(h.llm.requests[0]); !strings.Contains(names, "generate_song") {
		t.Fatalf("with a key the model is offered it: %s", names)
	}
	if created.Load() != 1 {
		t.Fatalf("one song task: %d", created.Load())
	}
	all := h.tg.allText()
	if !strings.Contains(all, "tg://audio") || !strings.Contains(all, `"🎤 باران"`) {
		t.Errorf("the song arrives as audio, with the caption the model chose:\n%s", all)
	}
	for _, s := range h.tg.texts() {
		if strings.Contains(s, "panel-song-key") || strings.Contains(s, "/g/a.mp3") {
			t.Errorf("a key or a link reached the chat: %s", s)
		}
	}

	// The words were kept: the lyrics file is beside the song but never sent by
	// itself, and the next request names it, so «متنش رو بده» needs no transcribing.
	if docs := h.tg.of("sendDocument"); len(docs) != 0 {
		t.Errorf("the lyrics file is not sent unasked: %d documents", len(docs))
	}
	if txt, _, err := h.bot.app.Machine.ReadFile(sessionFor(62), "Macron_AI_-_باران.txt", 1<<20); err != nil || !strings.Contains(string(txt), "بمون کنارم تا صبح بشه") {
		t.Fatalf("the lyrics file: %q, %v", txt, err)
	}
	h.tg.reset()
	h.llm.reply = say("این هم متنش.")
	ask := private(62, "متن آهنگ رو بده")
	ask.MessageID = 5400
	ask.ReplyToMessage = &telegram.Message{MessageID: 5399, Chat: ask.Chat, From: &telegram.User{ID: h.bot.me.ID},
		Audio: &telegram.Audio{FileID: "song-audio", FileName: "Macron_AI_-_باران.mp3", Title: "باران", Performer: "Macron AI"}}
	h.send(ask)
	req := llmText(h.llm.last())
	for _, want := range []string{"Macron_AI_-_باران.txt", "It is a song you made", "never transcribe it with a speech tool"} {
		if !strings.Contains(req, want) {
			t.Errorf("the request after «give me the lyrics» misses %q:\n%s", want, req)
		}
	}
}

func toolNamesOf(req map[string]any) string {
	var names []string
	tools, _ := req["tools"].([]any)
	for _, t := range tools {
		if f, ok := t.(map[string]any)["function"].(map[string]any); ok {
			names = append(names, fmt.Sprint(f["name"]))
		}
	}
	return strings.Join(names, ",")
}

// A song's lyrics file is made by the song tool but is never offered as a
// result to send: the song is.
func TestASongsLyricsFileIsNotAnAutoDeliverable(t *testing.T) {
	sb, err := machine.New(machine.Config{BaseDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	ft := newFileTracker(sb, "s")
	ft.start(tools.ToolSong)
	_ = sb.WriteFile("s", "Macron_AI_-_باران.mp3", []byte("mp3"))
	_ = sb.WriteFile("s", "Macron_AI_-_باران.txt", []byte("words"))
	ft.end(tools.ToolSong, "- File: Macron_AI_-_باران.mp3\n- Lyrics file: Macron_AI_-_باران.txt")
	if got := ft.made(); len(got) != 1 || got[0] != "Macron_AI_-_باران.mp3" {
		t.Errorf("only the song is a result: %v", got)
	}
	// another tool's text file is still a result
	ft2 := newFileTracker(sb, "s2")
	ft2.start("download_file")
	_ = sb.WriteFile("s2", "notes.txt", []byte("text"))
	ft2.end("download_file", "")
	if got := ft2.made(); len(got) != 1 {
		t.Errorf("a downloaded text file is a result: %v", got)
	}
}
