package tools

import (
	"bytes"
	"fmt"
	"html"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func testJPEG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for x := 0; x < w; x++ {
		for y := 0; y < h; y++ {
			img.Set(x, y, color.RGBA{uint8(x), uint8(y), 120, 255})
		}
	}
	var buf bytes.Buffer
	_ = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80})
	return buf.Bytes()
}

func TestSearchImage(t *testing.T) {
	r := newRegistry(t, true, nil)
	small := new(bytes.Buffer)
	_ = png.Encode(small, image.NewRGBA(image.Rect(0, 0, 120, 90)))
	big := testJPEG(t, 1200, 800)
	var webp []byte
	if _, err := exec.LookPath("ffmpeg"); err == nil {
		dir := t.TempDir()
		_ = writeFileT(t, filepath.Join(dir, "a.jpg"), testJPEG(t, 900, 600))
		if out, err := exec.Command("ffmpeg", "-loglevel", "error", "-y", "-i", filepath.Join(dir, "a.jpg"), filepath.Join(dir, "a.webp")).CombinedOutput(); err == nil {
			webp = readFileT(t, filepath.Join(dir, "a.webp"))
		} else {
			t.Logf("no webp encoder: %s", out)
		}
	}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		card := func(murl, title, dims string) string {
			m := fmt.Sprintf(`{"murl":"%s/img/%s","purl":"https://site.example/%s","t":"%s"}`, srv.URL, murl, murl, title)
			return `<a class="iusc" m="` + html.EscapeString(m) + `"></a><div class="img_info"><span class="nowrap">` + dims + `</span></div>`
		}
		switch req.URL.Path {
		case "/bing":
			if req.URL.Query().Get("adlt") != "strict" {
				t.Error("safe search is on")
			}
			if strings.Contains(req.URL.Query().Get("q"), "nothing") {
				_, _ = w.Write([]byte("<html></html>"))
				return
			}
			_, _ = w.Write([]byte("<html>" + card("tiny.png", "Tiny Tesla", "120&#215;90") + card("error.jpg", "Broken Tesla", "3000&#215;2000") +
				card("pumpkin.jpg", "Pumpkin carving ideas", "4000&#215;3000") + card("nsfw.jpg", "Tesla model nude", "4000&#215;3000") +
				card("big.jpg", "Big \ue000Tesla\ue001 Model 3", "1200&#215;800") + card("pic.webp", "tesla-model-3 webp", "900&#215;600") + "</html>"))
		case "/openverse/":
			if req.URL.Query().Get("q") == "Milad Tower" { // the second search, under the article's English name
				_, _ = w.Write([]byte(fmt.Sprintf(`{"results":[{"url":"%[1]s/img/shanghai.jpg","title":"Shanghai Tower","width":1500,"height":1000,"license":"by"},
					{"url":"%[1]s/img/milad2.jpg","title":"Milad Tower at dusk","foreign_landing_url":"https://flickr.example/2","width":1500,"height":1000,"license":"by"}]}`, srv.URL)))
				return
			}
			if strings.Contains(req.URL.Query().Get("q"), "nothing") {
				_, _ = w.Write([]byte(`{"results":[]}`))
				return
			}
			_, _ = w.Write([]byte(fmt.Sprintf(`{"results":[{"url":"%s/img/big.jpg?ov","title":"Tesla Model 3 at night","foreign_landing_url":"https://flickr.example/1","width":1200,"height":800,"license":"by"}]}`, srv.URL)))
		case "/commons":
			_, _ = w.Write([]byte(`{"query":{"pages":{}}}`))
		case "/wiki/fa":
			// «برج میلاد»: the article itself and one that only mentions it.
			_, _ = w.Write([]byte(fmt.Sprintf(`{"query":{"pages":[
				{"index":2,"title":"برج میلاد","fullurl":"https://fa.wikipedia.org/wiki/milad","thumbnail":{"source":"%[1]s/img/milad.jpg","width":1500,"height":1000},"langlinks":[{"lang":"en","title":"Milad Tower"}]},
				{"index":1,"title":"فهرست برج‌های تهران","fullurl":"https://fa.wikipedia.org/wiki/list","thumbnail":{"source":"%[1]s/img/flag_of_x.svg.png","width":1500,"height":1000}}]}}`, srv.URL)))
		case "/wiki/en":
			_, _ = w.Write([]byte(`{"query":{"pages":[]}}`))
		case "/img/tiny.png":
			_, _ = w.Write(small.Bytes())
		case "/img/error.jpg":
			_, _ = w.Write([]byte("<html>" + strings.Repeat("Access denied ", 300) + "</html>"))
		case "/img/milad.jpg", "/img/milad2.jpg":
			_, _ = w.Write(testJPEG(t, 1500, 1000+len(req.URL.Path)))
		case "/img/big.jpg", "/img/pumpkin.jpg", "/img/nsfw.jpg":
			if req.URL.Path != "/img/big.jpg" {
				t.Errorf("an unrelated or adult result is never downloaded: %s", req.URL.Path)
			}
			_, _ = w.Write(big)
		case "/img/pic.webp":
			_, _ = w.Write(webp)
		default:
			http.NotFound(w, req)
		}
	}))
	defer srv.Close()
	oldB, oldO, oldC, oldW := bingImagesURL, openverseURL, commonsURL, wikipediaURL
	bingImagesURL, openverseURL, commonsURL = srv.URL+"/bing", srv.URL+"/openverse/", srv.URL+"/commons"
	wikipediaURL = func(lang string) string { return srv.URL + "/wiki/" + lang }
	defer func() { bingImagesURL, openverseURL, commonsURL, wikipediaURL = oldB, oldO, oldC, oldW }()

	out := call(t, r, "img", "search_image", map[string]any{"query": "tesla model 3", "count": 3})
	lines := strings.Split(out, "\n")
	if len(lines) < 2 || !strings.Contains(lines[1], "_1.jpg (1200×800") || !strings.Contains(lines[1], "Bing: Big Tesla Model 3 — https://site.example/big.jpg") {
		t.Fatalf("the sharp real picture first, the broken page skipped:\n%s", out)
	}
	if webp != nil && (len(lines) < 3 || !strings.Contains(lines[2], "_2.jpg (900×600")) {
		t.Errorf("a WebP picture comes as a JPEG:\n%s", out)
	}
	if strings.Contains(out, "120×90") || strings.Contains(out, ".webp ") || strings.Contains(out, ".png") {
		t.Errorf("a thumbnail-sized picture is not delivered:\n%s", out)
	}
	if strings.Contains(out, "Openverse") || !strings.Contains(out, "(2 of the 3 asked for)") {
		t.Errorf("the same picture from Openverse is not sent twice:\n%s", out)
	}
	name := strings.Fields(strings.TrimPrefix(lines[1], "- "))[0]
	if data, _, _ := r.Machine.ReadFile("img", name, 1<<22); !bytes.Equal(data, big) {
		t.Error("the picture itself is saved")
	}
	if out := call(t, r, "img", "search_image", map[string]any{"query": "nothing at all"}); !strings.Contains(out, "no image found") {
		t.Errorf("nothing found: %s", out)
	}
	// A Persian name: the Wikipedia article's own picture first, then the
	// photo libraries under its English name, all of whose words must match.
	out = call(t, r, "img", "search_image", map[string]any{"query": "برج میلاد", "count": 3})
	lines = strings.Split(out, "\n")
	if len(lines) < 3 || !strings.Contains(lines[1], "photo_milad_tower_") || !strings.Contains(lines[1], "Wikipedia: برج میلاد (Milad Tower)") ||
		!strings.Contains(lines[2], "Openverse (CC BY): Milad Tower at dusk") {
		t.Fatalf("a Persian query:\n%s", out)
	}
	if strings.Contains(out, "Shanghai") || strings.Contains(out, "flag_of") || strings.Contains(out, "Tesla") {
		t.Errorf("only matching photos:\n%s", out)
	}
	if !sameName("حافظ (شاعر)", "عکس حافظ") || sameName("فهرست برج‌های تهران", "برج میلاد") {
		t.Error("an article named as the query")
	}

	if terms := queryTerms("عکس برج آزادی تهران 4k wallpaper"); strings.Join(terms, ",") != "برج,آزادی,تهران" {
		t.Errorf("terms: %v", terms)
	}
	if !relevantHit(imageHit{title: "Azadi Tower", url: "https://x/%D8%A8%D8%B1%D8%AC-%D8%A2%D8%B2%D8%A7%D8%AF%DB%8C.jpg"}, queryTerms("برج آزادی تهران")) {
		t.Error("words in an escaped address count")
	}
}

func writeFileT(t *testing.T, name string, data []byte) error {
	t.Helper()
	return os.WriteFile(name, data, 0o644)
}

func readFileT(t *testing.T, name string) []byte {
	t.Helper()
	data, _ := os.ReadFile(name)
	return data
}
