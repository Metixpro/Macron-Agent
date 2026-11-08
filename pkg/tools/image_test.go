package tools

import (
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
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/config"
)

const studioTestKey = "k3y-for-tests"

// fakeImageServices plays the picture studio (/studio: one POST with JSON,
// styles, aspects and generate, two JPEGs per picture) and Flux (/flux/<prompt>,
// a JPEG of the asked size).
type fakeImageServices struct {
	*httptest.Server
	nest, flux atomic.Int32 // generate calls, Flux calls
	calls      atomic.Int32 // every call to the studio
	fail       atomic.Bool  // the studio fails a generate
	mu         sync.Mutex
	lastBody   map[string]any
	fluxPrompt string
}

func jpegOf(w, h int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 90, 255})
		}
	}
	var b strings.Builder
	_ = jpeg.Encode(&b, img, &jpeg.Options{Quality: 80})
	return []byte(b.String())
}

func newFakeImageServices(t *testing.T) *fakeImageServices {
	t.Helper()
	f := &fakeImageServices{}
	mux := http.NewServeMux()
	mux.HandleFunc("/studio", func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		var body map[string]any
		if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&body) != nil {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":"POST JSON"}`)
			return
		}
		if body["api_key"] != studioTestKey {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"success":false,"error":"کلید API نامعتبر","received_key":"zzz..."}`)
			return
		}
		switch body["action"] {
		case "styles":
			fmt.Fprint(w, `{"success":true,"total":8,"data":[{"id":"anime","name":"انیمه"},{"id":"realistic","name":"واقع‌گرا"},`+
				`{"id":"cyberpunk","name":"سایبرپانک"},{"id":"3d_render","name":"رندر سه‌بعدی"},{"id":"oil_painting","name":"نقاشی رنگ روغن"},`+
				`{"id":"macro","name":"ماکرو"},{"id":"macro_insect","name":"حشرات"},{"id":"neon","name":"نئون"}]}`)
		case "aspects":
			fmt.Fprint(w, `{"success":true,"data":["1:1","4:5","16:9","9:16","4:3","3:4","2:1","21:9","32:9","5:4"],"total":10}`)
		case "generate":
			f.nest.Add(1)
			f.mu.Lock()
			f.lastBody = body
			f.mu.Unlock()
			if f.fail.Load() {
				fmt.Fprint(w, `{"success":false,"error":"The request timed out after 12000ms."}`)
				return
			}
			prompt, _ := body["prompt"].(string)
			if len([]rune(prompt)) < 3 {
				w.WriteHeader(http.StatusBadRequest)
				fmt.Fprint(w, `{"error":"متن توضیحات باید حداقل ۳ حرف باشد"}`)
				return
			}
			aspect, _ := body["aspect"].(string)
			_ = json.NewEncoder(w).Encode(map[string]any{"success": true, "data": map[string]any{
				"images":       []string{"http://" + r.Host + "/storage/a.jpg?aspect=" + aspect, "http://" + r.Host + "/storage/b.jpg?aspect=" + aspect},
				"aspect_ratio": aspect, "number_of_images": 2}})
		default:
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":"unknown action"}`)
		}
	})
	mux.HandleFunc("/storage/", func(w http.ResponseWriter, r *http.Request) {
		var a, b int
		fmt.Sscanf(r.URL.Query().Get("aspect"), "%d:%d", &a, &b)
		wd, ht := 1024, 1024
		switch {
		case a > b:
			ht = 1024 * b / a
		case b > a:
			wd = 1024 * a / b
		}
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(jpegOf(wd, ht))
	})
	mux.HandleFunc("/flux/", func(w http.ResponseWriter, r *http.Request) {
		f.flux.Add(1)
		f.mu.Lock()
		f.fluxPrompt = strings.TrimPrefix(r.URL.Path, "/flux/")
		f.mu.Unlock()
		var wd, ht int
		fmt.Sscan(r.URL.Query().Get("width"), &wd)
		fmt.Sscan(r.URL.Query().Get("height"), &ht)
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(jpegOf(wd, ht))
	})
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	oldFlux, oldWait := fluxEndpoint, imageRetryWait
	fluxEndpoint, imageRetryWait = f.URL+"/flux/", 0
	t.Cleanup(func() { fluxEndpoint, imageRetryWait = oldFlux, oldWait })
	return f
}

func (f *fakeImageServices) body() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastBody
}

// studioRegistry is a registry whose first engine is the fake studio.
func studioRegistry(t *testing.T, f *fakeImageServices) *Registry {
	t.Helper()
	r := newRegistry(t, true, nil)
	r.imageAPI = f.URL + "/studio"
	r.SetImageKey(func() string { return studioTestKey })
	return r
}

func imageSize(t *testing.T, r *Registry, session, name string) (int, int, int64) {
	t.Helper()
	f, fi, err := r.Machine.Open(session, name)
	if err != nil {
		t.Fatalf("open %s: %v", name, err)
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
	return cfg.Width, cfg.Height, fi.Size()
}

func TestGenerateImageStudioMakesTheStyleAndShape(t *testing.T) {
	f := newFakeImageServices(t)
	r := studioRegistry(t, f)

	for _, c := range []struct {
		ratio, file string
		w, h        int
	}{{"16:9", "wide.jpg", 1024, 576}, {"9:16", "tall.jpg", 576, 1024}, {"", "square.jpg", 1024, 1024},
		{"4:3", "photo.jpg", 1024, 768}, {"21:9", "ultra.jpg", 1024, 438}, {"5:3", "odd.jpg", 1024, 576}} {
		out := call(t, r, "img", "generate_image", map[string]any{"prompt": "a red bicycle on a beach", "aspect_ratio": c.ratio, "filename": c.file, "style": "Cyberpunk"})
		if !strings.Contains(out, "Engine: AI image service") || !strings.Contains(out, "Style: cyberpunk") || strings.Contains(out, "storage/") {
			t.Fatalf("%s: %q", c.ratio, out)
		}
		w, h, size := imageSize(t, r, "img", c.file)
		if w != c.w || h != c.h {
			t.Errorf("%q: %dx%d, want %dx%d", c.ratio, w, h, c.w, c.h)
		}
		if !strings.Contains(out, "("+humanSize(size)+")") {
			t.Errorf("%s: the reported size must be the file's (%s): %q", c.ratio, humanSize(size), out)
		}
	}
	body := f.body()
	if body["action"] != "generate" || body["prompt"] != "a red bicycle on a beach" || body["style"] != "cyberpunk" || body["aspect"] != "16:9" {
		t.Fatalf("the studio was asked: %v", body) // the last call: 5:3 → 16:9
	}
	if f.flux.Load() != 0 {
		t.Fatal("Flux is not called when the studio works")
	}
}

func TestGenerateImageNameAndFormat(t *testing.T) {
	f := newFakeImageServices(t)
	r := studioRegistry(t, f)
	out := call(t, r, "img", "generate_image", map[string]any{"prompt": "a quiet harbour"})
	if !strings.Contains(out, "File: ai_image_a_quiet_harbour_") || !strings.Contains(out, ".jpg (") {
		t.Fatalf("the studio answers JPEG, so an unnamed picture is a .jpg: %q", out)
	}
	if b := f.body(); b["style"] != nil {
		t.Fatalf("without a style the studio's own default applies: %v", b)
	}

	call(t, r, "img", "generate_image", map[string]any{"prompt": "a quiet harbour", "aspect_ratio": "16:9", "filename": "harbour.png"})
	file, _, err := r.Machine.Open("img", "harbour.png")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if cfg, kind, err := image.DecodeConfig(file); err != nil || kind != "png" || cfg.Width != 1024 || cfg.Height != 576 {
		t.Fatalf("a .png name gets a PNG, cut to the shape: %q %dx%d %v", kind, cfg.Width, cfg.Height, err)
	}
}

func TestGenerateImageTwoOptions(t *testing.T) {
	f := newFakeImageServices(t)
	r := studioRegistry(t, f)
	out := call(t, r, "img", "generate_image", map[string]any{"prompt": "a lighthouse at dawn", "count": 2, "filename": "light.jpg"})
	if !strings.Contains(out, "File: light.jpg") || !strings.Contains(out, "File (option 2): light_2.jpg") {
		t.Fatalf("two options: %q", out)
	}
	for _, n := range []string{"light.jpg", "light_2.jpg"} {
		if _, _, err := r.Machine.Open("img", n); err != nil {
			t.Fatalf("%s: %v", n, err)
		}
	}
	if f.nest.Load() != 1 {
		t.Fatalf("one generate gives both options, got %d calls", f.nest.Load())
	}

	// The request's allowance: one picture left means one option.
	ctx := WithAllowance(context.Background(), &Allowance{Images: 1})
	out, err := r.Execute(ctx, "img", "generate_image", `{"prompt":"a lighthouse at dawn","count":2,"filename":"one.jpg"}`)
	if err != nil || strings.Contains(out, "option 2") || !strings.Contains(out, "File: one.jpg") {
		t.Fatalf("the allowance caps the options: %q %v", out, err)
	}
	out, _ = r.Execute(ctx, "img", "generate_image", `{"prompt":"another lighthouse"}`)
	if !strings.HasPrefix(out, "error: Limit") {
		t.Fatalf("the allowance is used up: %q", out)
	}
}

func TestMatchStyle(t *testing.T) {
	list := []StudioStyle{{ID: "anime", Name: "انیمه"}, {ID: "realistic", Name: "واقع‌گرا"}, {ID: "cyberpunk"}, {ID: "3d_render"}, {ID: "oil_painting"},
		{ID: "macro"}, {ID: "macro_insect"}, {ID: "pixel"}, {ID: "low_poly"}, {ID: "neon"}, {ID: "sketch"}}
	for in, want := range map[string]string{
		"": "", "anime": "anime", "Anime": "anime", " ANIME ": "anime", "انیمه": "anime", "واقع‌گرا": "realistic", "oil painting": "oil_painting",
		"oil-painting": "oil_painting", "3D": "3d_render", "photo": "realistic", "Photorealistic": "realistic", "pixel art": "pixel", "low poly": "low_poly",
		"cyberpunk city": "cyberpunk", "neon lights": "neon", "macro insect photo": "macro_insect", "cyber": "cyberpunk", "anime style": "anime",
		"line art": "sketch",
	} {
		got, ok := matchStyle(in, list)
		if !ok || got != want {
			t.Errorf("%q → %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"zzz", "gouache", "an", "anime neon"} {
		if got, ok := matchStyle(in, list); ok {
			t.Errorf("%q must match no style, got %q", in, got)
		}
	}
}

func TestGenerateImageUnknownStyleListsTheStyles(t *testing.T) {
	f := newFakeImageServices(t)
	r := studioRegistry(t, f)
	out := call(t, r, "img", "generate_image", map[string]any{"prompt": "a fox", "style": "gouache"})
	if !strings.HasPrefix(out, "error: unknown style") || !strings.Contains(out, "cyberpunk") || !strings.Contains(out, "macro_insect") {
		t.Fatalf("the model learns the styles: %q", out)
	}
	if f.nest.Load() != 0 || f.flux.Load() != 0 {
		t.Fatal("nothing is drawn for a style that does not exist")
	}
	if out := call(t, r, "img", "generate_image", map[string]any{"prompt": "x"}); !strings.Contains(out, "too short") || f.nest.Load() != 0 {
		t.Fatalf("a prompt under 3 characters: %q", out)
	}
}

func TestShapes(t *testing.T) {
	all := studioShapes
	for in, want := range map[string]string{"": "1:1", "16:9": "16:9", "16/9": "16:9", "16x9": "16:9", "5:3": "16:9", "2:3": "3:4", "1:2": "9:16",
		"3:1": "32:9", "bogus": "1:1", "0:5": "1:1", "9:16": "9:16", "21:9": "21:9", "6:5": "5:4"} {
		if got := nearestShape(in, all); got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
	for ratio, want := range map[string][2]int{"1:1": {768, 768}, "16:9": {1024, 576}, "9:16": {576, 1024}, "4:3": {1024, 768}, "21:9": {1024, 440}, "32:9": {1024, 288}} {
		if w, h := fluxSize(ratio); w != want[0] || h != want[1] {
			t.Errorf("flux %s: %dx%d, want %dx%d", ratio, w, h, want[0], want[1])
		}
	}
}

func TestGenerateImageFallsBackToFlux(t *testing.T) {
	f := newFakeImageServices(t)
	f.fail.Store(true)
	r := studioRegistry(t, f)
	out := call(t, r, "img", "generate_image", map[string]any{"prompt": "a cat", "aspect_ratio": "16:9", "filename": "cat.png", "style": "anime"})
	if strings.Contains(out, "AI image service") || !strings.HasPrefix(out, "Image created") {
		t.Fatalf("fallback: %q", out)
	}
	if f.nest.Load() != imageAttempts || f.flux.Load() != 1 {
		t.Fatalf("the studio tried %d times, Flux %d", f.nest.Load(), f.flux.Load())
	}
	if w, h, _ := imageSize(t, r, "img", "cat.png"); w != 1024 || h != 576 {
		t.Fatalf("flux size %dx%d", w, h)
	}
	f.mu.Lock()
	fp := f.fluxPrompt
	f.mu.Unlock()
	if !strings.Contains(fp, "anime") || !strings.Contains(fp, "style") {
		t.Fatalf("Flux has no styles: the style goes into the words: %q", fp)
	}

	// No address at all (WAZE_IMAGE_API_URL=off): straight to Flux.
	r.imageAPI = ""
	f.nest.Store(0)
	out = call(t, r, "img", "generate_image", map[string]any{"prompt": "a dog"})
	if f.nest.Load() != 0 || f.flux.Load() != 2 {
		t.Fatal("WAZE_IMAGE_API_URL=off goes straight to Flux")
	}
	if !strings.Contains(out, "File: ai_image_a_dog_") || !strings.Contains(out, ".jpg (") {
		t.Fatalf("an unnamed Flux picture is a .jpg, like its content: %q", out)
	}
}

func TestGenerateImageWithoutAKeySkipsTheStudio(t *testing.T) {
	f := newFakeImageServices(t)
	r := newRegistry(t, true, nil)
	r.imageAPI = f.URL + "/studio"
	out := call(t, r, "img", "generate_image", map[string]any{"prompt": "a quiet harbour"})
	if f.calls.Load() != 0 || f.flux.Load() != 1 || !strings.HasPrefix(out, "Image created") {
		t.Fatalf("no key: the fallbacks make it, and the studio is never asked: %q (studio %d, flux %d)", out, f.calls.Load(), f.flux.Load())
	}
	if r.StudioReady() {
		t.Fatal("not ready without a key")
	}
}

func TestGenerateImageAKeyTheStudioRefusesIsNotRetried(t *testing.T) {
	f := newFakeImageServices(t)
	fluxEndpoint = f.URL + "/missing/"
	r := newRegistry(t, true, nil)
	r.imageAPI = f.URL + "/studio"
	r.SetImageKey(func() string { return "wrong-key" })
	out := call(t, r, "img", "generate_image", map[string]any{"prompt": "a cat"})
	if f.calls.Load() != 2 { // the styles are asked once, the picture once: a refused key is not tried again
		t.Fatalf("a refused key is not tried again: %d calls", f.calls.Load())
	}
	if !strings.Contains(out, "did not accept its key") || strings.Contains(out, "wrong-key") || !strings.Contains(out, "do NOT substitute") {
		t.Fatalf("honest failure, without the key: %q", out)
	}
}

func TestCheckImageKey(t *testing.T) {
	f := newFakeImageServices(t)
	r := studioRegistry(t, f)
	n, err := r.CheckImageKey(context.Background(), studioTestKey)
	if err != nil || n != 8 {
		t.Fatalf("a good key: %d %v", n, err)
	}
	if _, err := r.CheckImageKey(context.Background(), "nope"); !errors.Is(err, ErrImageKey) {
		t.Fatalf("a wrong key: %v", err)
	}
	if r.studioKey() != studioTestKey {
		t.Fatal("checking a key changes nothing")
	}
	if f.nest.Load() != 0 {
		t.Fatal("checking a key draws nothing")
	}
}

func TestGenerateImageBothFail(t *testing.T) {
	f := newFakeImageServices(t)
	f.fail.Store(true)
	fluxEndpoint = f.URL + "/missing/"
	r := studioRegistry(t, f)
	out := call(t, r, "img", "generate_image", map[string]any{"prompt": "a cat"})
	if !strings.Contains(out, "do NOT substitute") || !strings.Contains(out, "timed out") {
		t.Fatalf("honest failure: %q", out)
	}
}

func TestGenerateImageStopsWhenCancelled(t *testing.T) {
	f := newFakeImageServices(t)
	f.fail.Store(true)
	r := studioRegistry(t, f)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, err := r.Execute(ctx, "img", "generate_image", `{"prompt":"a cat"}`)
	if err != nil || f.flux.Load() != 0 || f.nest.Load() > 1 {
		t.Fatalf("a stopped job makes no more calls: %q %v (studio %d, flux %d)", out, err, f.nest.Load(), f.flux.Load())
	}
}

func TestTheStyleListIsReadOnceAndNeverHoldsAPictureBack(t *testing.T) {
	f := newFakeImageServices(t)
	r := studioRegistry(t, f)
	for range 3 {
		call(t, r, "img", "generate_image", map[string]any{"prompt": "a quiet harbour", "style": "neon"})
	}
	if f.calls.Load() != 2+3 { // styles and aspects once, a picture each time
		t.Fatalf("the lists are cached: %d calls", f.calls.Load())
	}

	// A service whose lists cannot be read: the built-in lists stand in, and
	// it is not asked again for a while.
	bad := newRegistry(t, true, nil)
	bad.imageAPI = f.URL + "/studio"
	bad.SetImageKey(func() string { return "wrong-key" })
	before := f.calls.Load()
	for range 3 {
		bad.studioCatalog(context.Background())
	}
	if got := f.calls.Load() - before; got != 1 {
		t.Fatalf("a failed read is not repeated at once: %d calls", got)
	}
	if id, ok := matchStyle("cyberpunk", func() []StudioStyle { l, _ := bad.studioCatalog(context.Background()); return l }()); !ok || id != "cyberpunk" {
		t.Fatal("the built-in styles stand in")
	}
}

// TestLiveStudio draws with the real service when WAZE_TEST_IMAGE_KEY is set
// (the key comes from the environment only; nothing is stored).
func TestLiveStudio(t *testing.T) {
	key := os.Getenv("WAZE_TEST_IMAGE_KEY")
	if key == "" {
		t.Skip("WAZE_TEST_IMAGE_KEY is not set")
	}
	r := newRegistry(t, true, nil)
	r.imageAPI = config.DefaultImageAPI
	r.SetImageKey(func() string { return key })
	if n, err := r.CheckImageKey(context.Background(), key); err != nil || n < 50 {
		t.Fatalf("the key: %d styles, %v", n, err)
	}
	styles, shapes := r.studioCatalog(context.Background())
	t.Logf("%d styles, shapes %v", len(styles), shapes)
	out := call(t, r, "live", "generate_image", map[string]any{"prompt": "a red fox in a snowy forest at golden hour", "style": "oil painting", "aspect_ratio": "16:9", "count": 2, "filename": "fox.jpg"})
	t.Log(out)
	for _, n := range []string{"fox.jpg", "fox_2.jpg"} {
		w, h, size := imageSize(t, r, "live", n)
		t.Logf("%s %dx%d %d bytes", n, w, h, size)
		if w*9 != h*16 && (w*9-h*16 > 20 || h*16-w*9 > 20) {
			t.Errorf("%s is not 16:9: %dx%d", n, w, h)
		}
	}
	if !strings.Contains(out, "AI image service") {
		t.Fatalf("the first engine drew it: %q", out)
	}
}
