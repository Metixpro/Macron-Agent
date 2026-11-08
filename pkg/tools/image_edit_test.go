package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

// fakeEditService plays the Nano Banana task service and a file host.
type fakeEditService struct {
	*httptest.Server
	mu         sync.Mutex
	created    []map[string][]string
	polls      atomic.Int32
	failWith   string // the task ends with this error
	uploads    atomic.Int32
	readyAfter int32
}

func newFakeEditService(t *testing.T) *fakeEditService {
	t.Helper()
	f := &fakeEditService{readyAfter: 2}
	img := image.NewRGBA(image.Rect(0, 0, 640, 480))
	for y := range 480 {
		for x := range 640 {
			img.Set(x, y, color.RGBA{20, 20, uint8(x), 255})
		}
	}
	var pngData bytes.Buffer
	_ = png.Encode(&pngData, img)
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/index.php", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Header.Get("X-API-Key") != "good-key" {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"status":false,"error":"Invalid or missing API key"}`)
			return
		}
		_ = r.ParseMultipartForm(1 << 20)
		if r.MultipartForm == nil || r.FormValue("user_id") == "" || r.FormValue("callback_url") == "" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"status":false,"error":"Missing user_id or callback_url"}`)
			return
		}
		f.mu.Lock()
		f.created = append(f.created, r.MultipartForm.Value)
		f.mu.Unlock()
		op := "generate"
		if len(r.MultipartForm.Value["image_urls[]"]) > 0 {
			op = "edit"
		}
		fmt.Fprintf(w, `{"status":true,"task_id":"task_1","operation":%q,"check_status_url":"http://%s/v2/task.php?task_id=task_1"}`, op, r.Host)
	})
	mux.HandleFunc("/v2/task.php", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		n := f.polls.Add(1)
		switch {
		case n == 1:
			w.WriteHeader(http.StatusNotFound) // not visible yet, right after creation
			fmt.Fprint(w, `{"status":"error","error":"Task not found"}`)
		case n <= f.readyAfter:
			fmt.Fprint(w, `{"status":"processing","processed_image_url":null}`)
		case f.failWith != "":
			fmt.Fprintf(w, `{"status":"failed","error_message":%q}`, f.failWith)
		default:
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "success",
				"processed_image_url": "http://" + r.Host + "/v2/imgs/out.png", "image_urls": []string{"http://" + r.Host + "/v2/imgs/out.png"}})
		}
	})
	mux.HandleFunc("/v2/imgs/out.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pngData.Bytes())
	})
	mux.HandleFunc("/upload", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(32 << 20); err != nil || r.MultipartForm.File["files[]"] == nil {
			http.Error(w, "no file", http.StatusBadRequest)
			return
		}
		f.uploads.Add(1)
		fmt.Fprint(w, `{"success":true,"files":[{"url":"https://files.example/abc.png"}]}`)
	})
	mux.HandleFunc("/broken-host", func(w http.ResponseWriter, r *http.Request) { http.Error(w, "down", http.StatusBadGateway) })
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	oldHosts, oldPoll := uploadHosts, editPollEvery
	uploadHosts = []uploadHost{
		{name: "broken", url: f.URL + "/broken-host", field: "file", parse: parsePlainURL},
		{name: "uguu", url: f.URL + "/upload", field: "files[]", parse: parseUguu},
	}
	editPollEvery = 0
	t.Cleanup(func() { uploadHosts, editPollEvery = oldHosts, oldPoll })
	return f
}

func editRegistry(t *testing.T, f *fakeEditService, key string) *Registry {
	t.Helper()
	r := newRegistry(t, true, nil)
	r.imageEditAPI = f.URL + "/v2/index.php"
	r.SetImageEditKey(func() string { return key })
	r.registerImageEditTool()
	return r
}

func putPNG(t *testing.T, r *Registry, session, name string) {
	t.Helper()
	var b bytes.Buffer
	_ = png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 32, 32)))
	if err := r.Machine.WriteFile(session, name, b.Bytes()); err != nil {
		t.Fatal(err)
	}
}

func TestEditImageWorkspacePicture(t *testing.T) {
	f := newFakeEditService(t)
	r := editRegistry(t, f, "good-key")
	putPNG(t, r, "tg-77", "photo.png")
	out := call(t, r, "tg-77", ToolEditImage, map[string]any{"prompt": "make it night, keep the bicycle", "images": []any{"photo.png"}, "filename": "night.png"})
	if !strings.HasPrefix(out, "Image edited and queued for delivery.") || !strings.Contains(out, "Engine: Nano Banana") || strings.Contains(out, "http") {
		t.Fatalf("result (no links ever): %q", out)
	}
	if w, h, size := imageSize(t, r, "tg-77", "night.png"); w != 640 || h != 480 || !strings.Contains(out, humanSize(size)) {
		t.Fatalf("saved %dx%d, %d bytes: %q", w, h, size, out)
	}
	if f.uploads.Load() != 1 {
		t.Fatal("a workspace picture goes to the next file host when one fails")
	}
	form := f.created[0]
	if got := form["image_urls[]"]; len(got) != 1 || got[0] != "https://files.example/abc.png" {
		t.Fatalf("image_urls[]: %v", got)
	}
	if uid := form["user_id"][0]; !strings.HasPrefix(uid, "waze-") || strings.Contains(uid, "77") {
		t.Fatalf("the chat id never leaves the server: %q", uid)
	}
	if form["prompt"][0] != "make it night, keep the bicycle" || form["callback_url"][0] == "" {
		t.Fatalf("form: %v", form)
	}
}

func TestEditImageLinksAndErrors(t *testing.T) {
	f := newFakeEditService(t)
	r := editRegistry(t, f, "good-key")
	out := call(t, r, "s", ToolEditImage, map[string]any{"prompt": "combine them", "images": []any{"https://a.example/1.jpg", "https://a.example/2.jpg"}})
	if !strings.HasPrefix(out, "Image edited") || f.uploads.Load() != 0 || len(f.created[0]["image_urls[]"]) != 2 {
		t.Fatalf("links go straight to the service: %q %v", out, f.created)
	}

	for _, c := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{"prompt": "x"}, "error: images is required"},
		{map[string]any{"images": []any{"a.png"}}, "error: prompt is required"},
		{map[string]any{"prompt": "x", "images": []any{"missing.png"}}, "not found in the workspace"},
		{map[string]any{"prompt": "x", "images": []any{"1", "2", "3", "4", "5"}}, "at most 4"},
	} {
		if out := call(t, r, "s", ToolEditImage, c.args); !strings.Contains(out, c.want) {
			t.Errorf("%v: %q", c.args, out)
		}
	}
	_ = r.Machine.WriteFile("s", "notes.png", []byte("just text, not a picture"))
	if out := call(t, r, "s", ToolEditImage, map[string]any{"prompt": "x", "images": []any{"notes.png"}}); !strings.Contains(out, "is not a picture") {
		t.Fatalf("not a picture: %q", out)
	}

	f.failWith, f.polls = "content policy violation", atomic.Int32{}
	out = call(t, r, "s", ToolEditImage, map[string]any{"prompt": "x", "images": []any{"https://a.example/1.jpg"}})
	if !strings.Contains(out, "image editing failed: content policy violation") || !strings.Contains(out, "Never send the original") {
		t.Fatalf("an honest failure: %q", out)
	}
}

func TestEditImageNeedsAKey(t *testing.T) {
	f := newFakeEditService(t)
	r := editRegistry(t, f, "")
	if r.Usable(ToolEditImage, "s") {
		t.Fatal("without a key edit_image is not offered")
	}
	if out := call(t, r, "s", ToolEditImage, map[string]any{"prompt": "x", "images": []any{"a.png"}}); !strings.Contains(out, "not set up") {
		t.Fatalf("no key: %q", out)
	}
	if err := r.CheckImageEditKey(context.Background(), "bad"); err != ErrImageEditKey {
		t.Fatalf("a bad key: %v", err)
	}
	if err := r.CheckImageEditKey(context.Background(), "good-key"); err != nil {
		t.Fatalf("a good key: %v", err)
	}
	if len(f.created) != 0 {
		t.Fatal("checking a key creates no task")
	}
	r2 := editRegistry(t, f, "good-key")
	if !r2.Usable(ToolEditImage, "s") {
		t.Fatal("with a key it is offered")
	}
}

func TestGenerateImageTriesTheTaskServiceBeforeFlux(t *testing.T) {
	imgs := newFakeImageServices(t)
	imgs.fail.Store(true)
	f := newFakeEditService(t)
	r := editRegistry(t, f, "good-key")
	r.imageAPI = imgs.URL + "/studio"
	r.SetImageKey(func() string { return studioTestKey })
	out := call(t, r, "img", "generate_image", map[string]any{"prompt": "a fox", "aspect_ratio": "16:9", "filename": "fox.png"})
	if !strings.Contains(out, "Engine: Nano Banana") || imgs.flux.Load() != 0 {
		t.Fatalf("the task service answers before Flux: %q (flux %d)", out, imgs.flux.Load())
	}
	if w, h, _ := imageSize(t, r, "img", "fox.png"); w*9 != h*16 {
		t.Fatalf("cropped to 16:9: %dx%d", w, h)
	}
	if op := f.created[0]; len(op["image_urls[]"]) != 0 || op["aspect"][0] != "16:9" {
		t.Fatalf("a new picture, not an edit: %v", op)
	}

	f.failWith, f.polls = "busy", atomic.Int32{}
	out = call(t, r, "img", "generate_image", map[string]any{"prompt": "a fox"})
	if imgs.flux.Load() != 1 || !strings.HasPrefix(out, "Image created") {
		t.Fatalf("then Flux: %q", out)
	}
}
