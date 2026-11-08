package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeSTT is an OpenAI-compatible /audio/transcriptions service.
type fakeSTT struct {
	mu    sync.Mutex
	forms []map[string]string // fields, plus "file" (its name) and "auth"
}

func (f *fakeSTT) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/audio/transcriptions" || r.ParseMultipartForm(32<<20) != nil {
		http.NotFound(w, r)
		return
	}
	form := map[string]string{"auth": r.Header.Get("Authorization")}
	for k, v := range r.MultipartForm.Value {
		form[k] = v[0]
	}
	if fh := r.MultipartForm.File["file"]; len(fh) > 0 {
		form["file"] = fh[0].Filename
	}
	f.mu.Lock()
	f.forms = append(f.forms, form)
	f.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]string{"text": " سلام، فردا ساعت ده جلسه داریم "})
}

func (f *fakeSTT) last() map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.forms[len(f.forms)-1]
}

func TestTranscribeAudio(t *testing.T) {
	r := newRegistry(t, true, nil)
	if out := call(t, r, "s", ToolTranscribe, map[string]any{"filename": "x.ogg"}); !strings.Contains(out, "not set up") {
		t.Fatalf("without a service: %s", out)
	}
	good := &fakeSTT{}
	goodSrv := httptest.NewServer(good)
	defer goodSrv.Close()
	var badCalls atomic.Int32
	badSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		badCalls.Add(1)
		http.NotFound(w, r) // a chat-only relay
	}))
	defer badSrv.Close()
	r.SetTranscriber(func() []STTService {
		return []STTService{{Name: "relay", URL: badSrv.URL + "/v1", Key: "k1", Auto: true}, {Name: "whisper", URL: goodSrv.URL + "/v1/", Key: "k2", Model: "whisper-large-v3"}}
	})
	if !r.CanTranscribe() {
		t.Fatal("services are set")
	}
	_ = r.Machine.WriteFile("s", "voice_7.ogg", []byte("OggS fake opus"))
	out := call(t, r, "s", ToolTranscribe, map[string]any{"filename": "voice_7.ogg", "language": "fa-IR"})
	if !strings.Contains(out, "Transcript of voice_7.ogg") || !strings.Contains(out, "سلام، فردا ساعت ده جلسه داریم") {
		t.Fatalf("transcript: %s", out)
	}
	if f := good.last(); f["model"] != "whisper-large-v3" || f["language"] != "fa" || f["file"] != "voice_7.ogg" || f["auth"] != "Bearer k2" {
		t.Errorf("request: %v", f)
	}
	call(t, r, "s", ToolTranscribe, map[string]any{"filename": "voice_7.ogg"})
	if badCalls.Load() != 1 {
		t.Errorf("a service without speech to text is skipped afterwards: %d calls", badCalls.Load())
	}
	if f := good.last(); f["language"] != "" || f["model"] != "whisper-large-v3" {
		t.Errorf("no language given: %v", f)
	}

	// The check sends a second of silence and clears a service's failure.
	if err := r.CheckSTT(context.Background(), STTService{URL: goodSrv.URL + "/v1"}); err != nil || good.last()["file"] != "silence.wav" || good.last()["model"] != "whisper-1" {
		t.Errorf("check: %v %v", err, good.last())
	}
	if err := r.CheckSTT(context.Background(), STTService{URL: badSrv.URL + "/v1"}); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Errorf("a failing check: %v", err)
	}
	if out := call(t, r, "s", ToolTranscribe, map[string]any{"filename": "nope.ogg"}); !strings.Contains(out, "not found") {
		t.Errorf("missing file: %s", out)
	}

	// Only a chat provider without speech to text: as if none were set up.
	_ = r.Machine.WriteFile("s", "voice_8.ogg", []byte("OggS another voice"))
	r.SetTranscriber(func() []STTService { return []STTService{{Name: "relay", URL: badSrv.URL + "/v2", Auto: true}} })
	if out := call(t, r, "s", ToolTranscribe, map[string]any{"filename": "voice_8.ogg"}); !strings.Contains(out, "not set up") || r.CanTranscribe() {
		t.Errorf("auto service without speech to text: %s", out)
	}
	// A service the admin set is tried and its error told once; the same
	// recording is not sent again, and a service without speech to text rests.
	_ = r.Machine.WriteFile("s", "voice_9.ogg", []byte("OggS a third voice"))
	r.SetTranscriber(func() []STTService { return []STTService{{Name: "mine", URL: badSrv.URL + "/v3"}} })
	before := badCalls.Load()
	if out := call(t, r, "s", ToolTranscribe, map[string]any{"filename": "voice_9.ogg"}); !strings.Contains(out, "mine: HTTP 404") || !strings.Contains(out, "Do not call") {
		t.Errorf("explicit service: %s", out)
	}
	if out := call(t, r, "s", ToolTranscribe, map[string]any{"filename": "voice_9.ogg"}); !strings.Contains(out, "a moment ago") || badCalls.Load() != before+1 {
		t.Errorf("a failed recording is not sent again (%d calls): %s", badCalls.Load()-before, out)
	}
	if r.CanTranscribe() {
		t.Error("a service without speech to text rests")
	}
}

func TestTranscribeCachesRestsAndQueues(t *testing.T) {
	r := newRegistry(t, true, nil)
	var calls, inFlight, most, broken atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.HasPrefix(req.URL.Path, "/down/") {
			broken.Add(1)
			http.Error(w, "upstream crashed", http.StatusBadGateway)
			return
		}
		calls.Add(1)
		n := inFlight.Add(1)
		for m := most.Load(); n > m && !most.CompareAndSwap(m, n); m = most.Load() {
		}
		time.Sleep(150 * time.Millisecond)
		inFlight.Add(-1)
		fmt.Fprint(w, `{"text":"ok"}`)
	}))
	defer srv.Close()
	r.SetTranscriber(func() []STTService { return []STTService{{Name: "local", URL: srv.URL + "/v1"}} })
	for i := 0; i < 3; i++ {
		_ = r.Machine.WriteFile("q", fmt.Sprintf("v%d.ogg", i), []byte(fmt.Sprintf("OggS %d", i)))
	}
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			call(t, r, "q", ToolTranscribe, map[string]any{"filename": fmt.Sprintf("v%d.ogg", i)})
		}()
	}
	wg.Wait()
	if most.Load() != 1 || calls.Load() != 3 {
		t.Errorf("one recording at a time per service: at most %d at once, %d calls", most.Load(), calls.Load())
	}
	call(t, r, "q", ToolTranscribe, map[string]any{"filename": "v1.ogg"})
	if calls.Load() != 3 {
		t.Error("a recording already written down is not sent again")
	}

	// A 5xx rests the service for a while: the next recording does not wait for it.
	r.SetTranscriber(func() []STTService { return []STTService{{Name: "down", URL: srv.URL + "/down"}} })
	_ = r.Machine.WriteFile("q", "v3.ogg", []byte("OggS 3"))
	_ = r.Machine.WriteFile("q", "v4.ogg", []byte("OggS 4"))
	if out := call(t, r, "q", ToolTranscribe, map[string]any{"filename": "v3.ogg"}); !strings.Contains(out, "HTTP 502") {
		t.Errorf("5xx: %s", out)
	}
	if out := call(t, r, "q", ToolTranscribe, map[string]any{"filename": "v4.ogg"}); !strings.Contains(out, "unavailable for a few minutes") || broken.Load() != 1 || !r.CanTranscribe() {
		t.Errorf("rest after a 5xx (%d calls): %s", broken.Load(), out)
	}
}

func TestTranscribeConvertsOtherFormats(t *testing.T) {
	if exec.Command("ffmpeg", "-version").Run() != nil {
		t.Skip("ffmpeg is needed")
	}
	r := newRegistry(t, true, nil)
	fakeDownloader(t, r)
	good := &fakeSTT{}
	srv := httptest.NewServer(good)
	defer srv.Close()
	r.SetTranscriber(func() []STTService { return []STTService{{URL: srv.URL + "/v1"}} })
	ws, _ := r.Machine.Workspace("c")
	if out, err := exec.Command("ffmpeg", "-y", "-v", "error", "-f", "lavfi", "-i", "sine=frequency=300:duration=2", "-c:a", "aac",
		ws+"/lecture.aac").CombinedOutput(); err != nil {
		t.Skipf("cannot make a test recording: %v %s", err, out)
	}
	out := call(t, r, "c", ToolTranscribe, map[string]any{"filename": "lecture.aac"})
	if !strings.Contains(out, "جلسه") || good.last()["file"] != ".stt_lecture.mp3" {
		t.Fatalf("converted: %s %v", out, good.last())
	}
	if _, err := r.Machine.Stat("c", ".stt_lecture.mp3"); err == nil {
		t.Error("the converted copy is removed")
	}
}

func TestSTTHelpers(t *testing.T) {
	for in, want := range map[string]string{"fa": "fa", "Persian": "fa", "fa_IR": "fa", "EN-us": "en", "farsi": "fa", "123": "", "": ""} {
		if got := normalizeLang(in); got != want {
			t.Errorf("normalizeLang(%q) = %q", in, got)
		}
	}
	hf := "https://router.huggingface.co/hf-inference/models/openai/whisper-large-v3"
	if !hfInference(hf) || hfInference("https://router.huggingface.co/v1") || hfInference("https://api.openai.com/v1") || DefaultSTTModel(hf) != "openai/whisper-large-v3" {
		t.Error("Hugging Face inference addresses")
	}
	if DefaultSTTModel("https://api.groq.com/openai/v1") != "whisper-large-v3-turbo" || DefaultSTTModel("https://api.openai.com/v1") != "whisper-1" {
		t.Error("default models")
	}
	if w := silentWAV(time.Second); len(w) != 44+32000 || string(w[:4]) != "RIFF" || string(w[8:12]) != "WAVE" {
		t.Errorf("wav: %d bytes", len(w))
	}
	if (&sttHTTPError{status: 400, body: "model `x` not found"}).unsupported() != true || (&sttHTTPError{status: 400, body: "audio too short"}).unsupported() {
		t.Error("unsupported")
	}
}

// The same recording twice at once is written down once; a recording that
// waits for a busy local whisper says so; a cloud service takes a few at once.
func TestOneRecordingIsWrittenDownOnce(t *testing.T) {
	r := newRegistry(t, true, nil)
	var calls atomic.Int32
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls.Add(1)
		<-release
		fmt.Fprint(w, `{"text":"سلام"}`)
	}))
	defer srv.Close()
	r.SetTranscriber(func() []STTService { return []STTService{{Name: "local", URL: srv.URL + "/v1"}} })
	_ = r.Machine.WriteFile("o", "a.ogg", []byte("OggS same"))
	_ = r.Machine.WriteFile("o", "b.ogg", []byte("OggS same")) // the same voice, forwarded again
	_ = r.Machine.WriteFile("o", "c.ogg", []byte("OggS another"))
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		statuses []string
		texts    []string
	)
	for _, f := range []string{"a.ogg", "b.ogg", "c.ogg"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx := WithProgress(context.Background(), Progress{Status: func(s string) {
				mu.Lock()
				statuses = append(statuses, f+":"+s)
				mu.Unlock()
			}})
			text, _, err := r.Transcribe(ctx, "o", f, "")
			mu.Lock()
			texts = append(texts, fmt.Sprint(text, err))
			mu.Unlock()
		}()
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	close(release)
	wg.Wait()
	if calls.Load() != 2 || strings.Join(texts, ",") != "سلام<nil>,سلام<nil>,سلام<nil>" {
		t.Fatalf("%d calls for two recordings: %v", calls.Load(), texts)
	}
	if got := strings.Join(statuses, " "); !strings.Contains(got, "b.ogg:"+StatusQueued) || !strings.Contains(got, "c.ogg:"+StatusQueued) {
		t.Errorf("waiting is said: %s", got)
	}
	for svc, want := range map[string]int{"http://127.0.0.1:8443/v1": 1, "http://localhost:8443/v1": 1, "http://192.168.1.5/v1": 1,
		"https://api.groq.com/openai/v1": 4} {
		if got := sttSlots(STTService{URL: svc}); got != want {
			t.Errorf("%s: %d at once", svc, got)
		}
	}
}
