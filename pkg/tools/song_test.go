package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/config"
)

const songTestKey = "song-key-for-tests"

const goodLyrics = "[verse]\nباران می‌زنه روی شیشه شهر\nدلم می‌خواد برگردی دوباره\n\n[chorus]\nبمون کنارم تا صبح بشه\nشب تموم می‌شه و عشق می‌مونه"

// fakeSongService plays the song service: one POST creates a task, its status
// page (behind a redirect, like the real one) says "processing" a few times and
// then lists the songs, the songs are mp3 files and the cover a JPEG.
type fakeSongService struct {
	*httptest.Server
	created   atomic.Int32
	polls     atomic.Int32
	mu        sync.Mutex
	lastBody  map[string]any
	processed int32  // polls that say "processing" first
	failWith  string // the task ends with this error
	same      bool   // the second version points at the first's file
	mp3       []byte
}

func newFakeSongService(t *testing.T) *fakeSongService {
	t.Helper()
	f := &fakeSongService{processed: 2}
	f.mp3 = []byte("ID3\x03\x00\x00\x00\x00\x00\x00" + strings.Repeat("\xff\xfb\x90\x00", 600))
	if ff, err := exec.LookPath("ffmpeg"); err == nil { // a real mp3, so the cover can really be put in
		out := t.TempDir() + "/x.mp3"
		if exec.Command(ff, "-nostdin", "-loglevel", "error", "-f", "lavfi", "-i", "sine=frequency=440:duration=2", "-c:a", "libmp3lame", "-y", out).Run() == nil {
			if b, err := os.ReadFile(out); err == nil {
				f.mp3 = b
			}
		}
	}
	var cover bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := range 64 {
		for x := range 64 {
			img.Set(x, y, color.RGBA{uint8(x * 4), uint8(y * 4), 120, 255})
		}
	}
	_ = jpeg.Encode(&cover, img, nil)

	mux := http.NewServeMux()
	auth := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("X-API-Key") != songTestKey {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"status":false,"error":"Invalid or missing API key"}`)
			return false
		}
		return true
	}
	mux.HandleFunc("/music/index.php", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !auth(w, r) {
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["user_id"] == nil || body["callback_url"] == nil {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"status":false,"error":"Missing user_id or callback_url"}`)
			return
		}
		f.created.Add(1)
		f.mu.Lock()
		f.lastBody = body
		f.mu.Unlock()
		fmt.Fprintf(w, `{"status":true,"task_id":"song_1","check_status_url":"http://%s/music?task_id=song_1","estimated_time":"60-120 seconds"}`, r.Host)
	})
	mux.HandleFunc("/music", func(w http.ResponseWriter, r *http.Request) { // the real one answers 301 to the slash
		http.Redirect(w, r, "/music/?"+r.URL.RawQuery, http.StatusMovedPermanently)
	})
	mux.HandleFunc("/music/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !auth(w, r) {
			return
		}
		f.polls.Add(1)
		if f.polls.Load() <= f.processed {
			fmt.Fprint(w, `{"status":true,"task":{"status":"processing","api_key":"song-ke..."}}`)
			return
		}
		if f.failWith != "" {
			fmt.Fprintf(w, `{"status":true,"task":{"status":"failed","error_message":%q}}`, f.failWith)
			return
		}
		second := "b"
		if f.same {
			second = "a"
		}
		fmt.Fprintf(w, `{"status":true,"task":{"status":"success","songs":[`+
			`{"status":"complete","audio_url":"http://%[1]s/music/generated/a.mp3","cover_url":"http://%[1]s/cover.jpg","duration":120.2},`+
			`{"status":"complete","audio_url":"http://%[1]s/music/generated/%[2]s.mp3","cover_url":"http://%[1]s/cover.jpg","duration":118.0}]}}`, r.Host, second)
	})
	mux.HandleFunc("/music/generated/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write(f.mp3)
	})
	mux.HandleFunc("/cover.jpg", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream") // as the real CDN serves it
		_, _ = w.Write(cover.Bytes())
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	old := songPollEvery
	songPollEvery = 0
	t.Cleanup(func() { songPollEvery = old })
	return f
}

func (f *fakeSongService) body() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastBody
}

func songRegistry(t *testing.T, f *fakeSongService) *Registry {
	t.Helper()
	r := newRegistry(t, true, nil)
	r.SetMusicAPI(f.URL + "/music/index.php")
	r.SetMusicKey(func() string { return songTestKey })
	r.registerSongTool()
	return r
}

func TestSongTwoVersionsWithTheirCover(t *testing.T) {
	f := newFakeSongService(t)
	r := songRegistry(t, f)
	out := call(t, r, "sess-42", ToolSong, map[string]any{"lyrics": goodLyrics, "title": "جنگ زرگرها", "style": "Persian pop ballad, piano", "gender": "زن"})
	for _, want := range []string{"Song created and queued for delivery", "File: Macron_AI_-_جنگ_زرگرها.mp3", "File (version 2): Macron_AI_-_جنگ_زرگرها_2.mp3",
		"Style: Persian pop ballad, piano", "Voice: female", "Length: 2:00", "Engine: AI song service"} {
		if !strings.Contains(out, want) {
			t.Errorf("the result misses %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "/music/generated") || strings.Contains(out, songTestKey) {
		t.Errorf("a link or the key reached the model:\n%s", out)
	}
	b := f.body()
	if b["lyrics"] != goodLyrics || b["style"] != "Persian pop ballad, piano" || b["gender"] != "female" || b["title"] != "جنگ زرگرها" {
		t.Errorf("the service was asked: %v", b)
	}
	if uid, _ := b["user_id"].(string); !strings.HasPrefix(uid, "waze-") || strings.Contains(uid, "sess-42") {
		t.Errorf("the chat is named to the service without giving its id away: %q", uid)
	}
	if b["callback_url"] != songCallback {
		t.Errorf("callback: %v", b["callback_url"])
	}
	for _, n := range []string{"Macron_AI_-_جنگ_زرگرها.mp3", "Macron_AI_-_جنگ_زرگرها_2.mp3"} {
		data, _, err := r.Machine.ReadFile("sess-42", n, 10<<20)
		if err != nil || len(data) < 1000 {
			t.Fatalf("%s: %d bytes, %v", n, len(data), err)
		}
		if _, err := exec.LookPath("ffmpeg"); err == nil && !bytes.Contains(data[:min(len(data), 100000)], []byte("APIC")) {
			t.Errorf("%s has no cover in its tags", n)
		}
	}
	if f.polls.Load() < 3 {
		t.Errorf("it waited for the songs: %d polls", f.polls.Load())
	}

	// The words are kept: a file beside the song, the song's tags, and the model is
	// told to put them in its answer (the conversation keeps answers, not tool calls).
	for _, want := range []string{"Lyrics file: Macron_AI_-_جنگ_زرگرها.txt", "put them in your answer", "never transcribe a song you made"} {
		if !strings.Contains(out, want) {
			t.Errorf("the result misses %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "lyrics are above") {
		t.Errorf("the model was told the lyrics were shown:\n%s", out)
	}
	txt, _, err := r.Machine.ReadFile("sess-42", "Macron_AI_-_جنگ_زرگرها.txt", 1<<20)
	if err != nil || !strings.HasPrefix(string(txt), "جنگ زرگرها\n\n") || !strings.Contains(string(txt), "بمون کنارم تا صبح بشه") || !strings.Contains(string(txt), "Voice: female") {
		t.Errorf("the lyrics file: %q, %v", txt, err)
	}
	if _, err := exec.LookPath("ffprobe"); err == nil {
		ws, _ := r.Machine.Workspace("sess-42")
		tags, _ := exec.Command("ffprobe", "-v", "error", "-show_entries", "format_tags=lyrics,title,artist", "-of", "default=nw=1", ws+"/Macron_AI_-_جنگ_زرگرها.mp3").Output()
		if !strings.Contains(string(tags), "TAG:lyrics=[verse]") || !strings.Contains(string(tags), "بمون کنارم تا صبح بشه") || !strings.Contains(string(tags), "TAG:artist=Macron AI") {
			t.Errorf("the mp3's tags carry the lyrics: %s", tags)
		}
	}
}

// An instrumental is asked for with the bare tag and the no-voice style tags,
// whatever words the model sent, with no voice and a title from the style.
func TestSongInstrumentalSendsNoWordsAndNoVoice(t *testing.T) {
	f := newFakeSongService(t)
	r := songRegistry(t, f)
	out := call(t, r, "s", ToolSong, map[string]any{"instrumental": true, "style": "lo-fi hip hop, mellow piano, vinyl crackle", "gender": "male",
		"lyrics": "la la la sing this"})
	b := f.body()
	if b["lyrics"] != instrumentalLyrics || b["gender"] != nil {
		t.Errorf("no words and no voice go to the service: %v", b)
	}
	if st, _ := b["style"].(string); !strings.HasPrefix(st, "instrumental, no vocals, no singing") || !strings.Contains(st, "mellow piano") {
		t.Errorf("the style says no voice and keeps the brief: %q", st)
	}
	if b["title"] != "Lo-fi Hip Hop" {
		t.Errorf("a title from the style: %v", b["title"])
	}
	for _, want := range []string{"Song created", "File: Macron_AI_-_Lo-fi_Hip_Hop.mp3", "Voice: none (instrumental)", "you cannot hear it"} {
		if !strings.Contains(out, want) {
			t.Errorf("the result misses %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "lyrics are above") || strings.Contains(out, "Voice: male") || strings.Contains(out, "Lyrics file") {
		t.Errorf("an instrumental has no lyrics and no voice:\n%s", out)
	}
	if _, err := r.Machine.Stat("s", "Macron_AI_-_Lo-fi_Hip_Hop.txt"); err == nil {
		t.Error("an instrumental has no lyrics file")
	}

	// no style at all: the tags alone; and the title the model gave wins
	call(t, r, "s", ToolSong, map[string]any{"instrumental": true, "title": "Rainy Window"})
	if b := f.body(); b["style"] != "instrumental, no vocals, no singing" || b["title"] != "Rainy Window" {
		t.Errorf("defaults: %v", b)
	}
	// a song still needs its words
	before := f.created.Load()
	if out := call(t, r, "s", ToolSong, map[string]any{"style": "Pop"}); !strings.HasPrefix(out, "error:") || !strings.Contains(out, "instrumental") || f.created.Load() != before {
		t.Errorf("no lyrics and not instrumental: %s", out)
	}
}

func TestSongTheSameLinkTwiceIsOneSong(t *testing.T) {
	f := newFakeSongService(t)
	f.same = true
	r := songRegistry(t, f)
	out := call(t, r, "s", ToolSong, map[string]any{"lyrics": goodLyrics, "title": "باران"})
	if !strings.Contains(out, "File: Macron_AI_-_باران.mp3") || strings.Contains(out, "version 2") || strings.Contains(out, "Two versions") {
		t.Errorf("a version that is the first's own file is not a second song:\n%s", out)
	}
}

func TestSongFailuresAreSaidPlainly(t *testing.T) {
	f := newFakeSongService(t)
	f.failWith = "lyrics were rejected by the moderation"
	r := songRegistry(t, f)
	out := call(t, r, "s", ToolSong, map[string]any{"lyrics": goodLyrics})
	if !strings.Contains(out, "song making failed") || !strings.Contains(out, "rejected by the moderation") || !strings.Contains(out, "do NOT offer") {
		t.Errorf("the service's reason, honestly:\n%s", out)
	}

	bad := songRegistry(t, f)
	bad.SetMusicKey(func() string { return "wrong" })
	out = call(t, bad, "s", ToolSong, map[string]any{"lyrics": goodLyrics})
	if !strings.Contains(out, "did not accept its key") || strings.Contains(out, "wrong") {
		t.Errorf("a refused key, without the key:\n%s", out)
	}
}

func TestSongNeedsRealLyricsAndAKey(t *testing.T) {
	f := newFakeSongService(t)
	r := songRegistry(t, f)
	for _, lyrics := range []string{"", "hi", "[verse]\none line\ntwo lines\n[chorus]"} {
		out := call(t, r, "s", ToolSong, map[string]any{"lyrics": lyrics})
		if !strings.HasPrefix(out, "error:") {
			t.Errorf("%q: %s", lyrics, out)
		}
	}
	if out := call(t, r, "s", ToolSong, map[string]any{"lyrics": strings.Repeat("یه خط آهنگ\n", 400)}); !strings.Contains(out, "too long") {
		t.Errorf("too long: %s", out)
	}
	if f.created.Load() != 0 {
		t.Fatal("nothing is made for lyrics that are not a song")
	}

	r.SetMusicKey(func() string { return "" })
	if out := call(t, r, "s", ToolSong, map[string]any{"lyrics": goodLyrics}); !strings.Contains(out, "not set up") || f.created.Load() != 0 {
		t.Errorf("without a key: %s", out)
	}
	if r.Usable(ToolSong, "s") || r.SongReady() {
		t.Error("without a key the tool is not offered")
	}
	r.SetMusicKey(func() string { return songTestKey })
	if !r.Usable(ToolSong, "s") {
		t.Error("with a key it is offered")
	}
}

func TestCheckMusicKeyMakesNoSong(t *testing.T) {
	f := newFakeSongService(t)
	r := songRegistry(t, f)
	if err := r.CheckMusicKey(context.Background(), songTestKey); err != nil {
		t.Fatalf("a good key: %v", err)
	}
	if err := r.CheckMusicKey(context.Background(), "nope"); !errors.Is(err, ErrMusicKey) {
		t.Fatalf("a wrong key: %v", err)
	}
	if f.created.Load() != 0 {
		t.Fatal("checking a key makes no song")
	}
	r.SetMusicAPI("")
	if err := r.CheckMusicKey(context.Background(), songTestKey); err == nil {
		t.Fatal("a service that is off cannot be checked")
	}
}

func TestSongHelpers(t *testing.T) {
	if lyricLines(goodLyrics) != 4 || lyricLines("[verse]\n\n[chorus]") != 0 {
		t.Error("lines are the sung ones")
	}
	if cleanTitle("  *My* \"song\"\n[x] a/b ") != "My song x a b" || cleanTitle(strings.Repeat("ا", 100)) == strings.Repeat("ا", 100) {
		t.Errorf("titles: %q", cleanTitle("  *My* \"song\"\n[x] a/b "))
	}
	if cleanStyle("") != "Pop" || cleanStyle("lo-fi\x00\nchill") != "lo-fi chill" || len([]rune(cleanStyle(strings.Repeat("x", 500)))) != 200 {
		t.Error("styles")
	}
	for in, want := range map[string]string{"Male": "male", "زنانه": "female", "": "", "robot": ""} {
		if songGender(in) != want {
			t.Errorf("gender %q → %q", in, songGender(in))
		}
	}
	if firstLyric("[verse]\n\nسلام دنیا\nبعدی") != "سلام دنیا" {
		t.Error("first lyric")
	}
	if instrumentalStyle("") != "instrumental, no vocals, no singing" ||
		instrumentalStyle(" Persian santur \n calm ") != "instrumental, no vocals, no singing, Persian santur calm" ||
		len([]rune(instrumentalStyle(strings.Repeat("x", 500)))) != 200 {
		t.Errorf("instrumental styles: %q", instrumentalStyle(" Persian santur \n calm "))
	}
	for in, want := range map[string]string{"lo-fi hip hop, piano": "Lo-fi Hip Hop", "instrumental, no vocals, epic cinematic orchestral strings": "Epic Cinematic Orchestral",
		"instrumental, no vocals, no singing": "Instrumental", "": "Instrumental", "پیانو": "پیانو"} {
		if styleTitle(in) != want {
			t.Errorf("title of %q: %q, want %q", in, styleTitle(in), want)
		}
	}
}

// TestLiveSong makes a real song when WAZE_TEST_MUSIC_KEY is set (the key
// comes from the environment only; it takes a couple of minutes).
func TestLiveSong(t *testing.T) {
	key := os.Getenv("WAZE_TEST_MUSIC_KEY")
	if key == "" {
		t.Skip("WAZE_TEST_MUSIC_KEY is not set")
	}
	r := newRegistry(t, true, nil)
	r.SetMusicAPI(config.DefaultMusicAPI)
	r.SetMusicKey(func() string { return key })
	r.registerSongTool()
	if err := r.CheckMusicKey(context.Background(), key); err != nil {
		t.Fatalf("the key: %v", err)
	}
	out := call(t, r, "live", ToolSong, map[string]any{"lyrics": goodLyrics, "title": "باران", "style": "Persian pop ballad, piano, sad", "gender": "female"})
	t.Log(out)
	if !strings.HasPrefix(out, "Song created") {
		t.Fatalf("no song: %s", out)
	}
	data, _, err := r.Machine.ReadFile("live", "Macron_AI_-_باران.mp3", 60<<20)
	if err != nil || len(data) < 200000 {
		t.Fatalf("the song: %d bytes, %v", len(data), err)
	}
	if _, e := exec.LookPath("ffprobe"); e == nil {
		_ = os.WriteFile(os.TempDir()+"/live_song.mp3", data, 0o600)
		o, _ := exec.Command("ffprobe", "-v", "error", "-show_entries", "format=duration:stream=codec_type", "-of", "default=nw=1", os.TempDir()+"/live_song.mp3").CombinedOutput()
		t.Logf("ffprobe: %s", strings.ReplaceAll(string(o), "\n", " "))
		_ = os.Remove(os.TempDir() + "/live_song.mp3")
	}
}

// TestLiveSongInstrumental makes a real track with no singer (same key rule as
// TestLiveSong). It checks the files and, when WAZE_TEST_KEEP_DIR is set, keeps
// the first one there to be listened to (speech recognition, by hand).
func TestLiveSongInstrumental(t *testing.T) {
	key := os.Getenv("WAZE_TEST_MUSIC_KEY")
	if key == "" {
		t.Skip("WAZE_TEST_MUSIC_KEY is not set")
	}
	r := newRegistry(t, true, nil)
	r.SetMusicAPI(config.DefaultMusicAPI)
	r.SetMusicKey(func() string { return key })
	r.registerSongTool()
	out := call(t, r, "live", ToolSong, map[string]any{"instrumental": true, "style": "solo piano, calm, cinematic"})
	t.Log(out)
	if !strings.HasPrefix(out, "Song created") || !strings.Contains(out, "Voice: none (instrumental)") {
		t.Fatalf("no track: %s", out)
	}
	data, _, err := r.Machine.ReadFile("live", "Macron_AI_-_Solo_Piano.mp3", 60<<20)
	if err != nil || len(data) < 200000 {
		t.Fatalf("the track: %d bytes, %v", len(data), err)
	}
	if dir := os.Getenv("WAZE_TEST_KEEP_DIR"); dir != "" {
		_ = os.WriteFile(dir+"/live_instrumental.mp3", data, 0o600)
	}
}
