package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var ErrImageKey = errors.New("the image service did not accept its key")

var errStudioRefused = errors.New("the image service refused the request")

func (r *Registry) SetImageKey(key func() string) { r.imageKey = key }

func (r *Registry) SetImageAPI(url string) { r.imageAPI = url }

func (r *Registry) ImageKey() string { return r.studioKey() }

func (r *Registry) studioKey() string {
	if r.imageKey == nil {
		return ""
	}
	return strings.TrimSpace(r.imageKey())
}

func (r *Registry) StudioReady() bool { return r.imageAPI != "" && r.studioKey() != "" }

func (r *Registry) StudioStyles(ctx context.Context) []StudioStyle {
	list, _ := r.studioCatalog(ctx)
	return list
}

type StudioStyle struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

var studioShapes = []string{"1:1", "4:5", "16:9", "9:16", "4:3", "3:4", "2:1", "21:9", "32:9", "5:4"}

var studioStyleIDs = strings.Fields(`anime realistic oil_painting watercolor sketch pixel 3d_render cyberpunk steampunk fantasy gothic
cartoon comic glitch vaporwave impressionist expressionist cubist surrealist popart minimalist baroque rococo renaissance abstract
graffiti vector isometric low_poly chalk pastel charcoal mosaic origami lego collage mandala calligraphy henna polaroid infrared
thermal underwater microscopic telescopic night_vision xray sonar radar hologram neon chromatic monochrome sepia cyanotype lomography
tilt_shift macro bokeh long_exp double_exp hdr night_mode portrait landscape street architecture food fashion product automotive
sports wildlife macro_insect astro aerial`)

var styleAliases = map[string]string{
	"photo": "realistic", "photograph": "realistic", "photorealistic": "realistic", "photo_realistic": "realistic", "realism": "realistic",
	"real": "realistic", "natural": "realistic", "cinematic": "realistic",
	"3d": "3d_render", "3d_art": "3d_render", "render": "3d_render", "cgi": "3d_render", "3d_model": "3d_render", "3d_rendering": "3d_render",
	"oil": "oil_painting", "oil_paint": "oil_painting", "painting": "oil_painting", "oilpainting": "oil_painting",
	"water_color": "watercolor", "water_colour": "watercolor", "watercolour": "watercolor",
	"line_art": "sketch", "lineart": "sketch", "pencil": "sketch", "drawing": "sketch", "pencil_sketch": "sketch", "line_drawing": "sketch",
	"pixel_art": "pixel", "pixelart": "pixel", "8bit": "pixel", "8_bit": "pixel", "retro_game": "pixel",
	"manga": "anime", "japanese_animation": "anime", "anime_style": "anime",
	"comic_book": "comic", "comics": "comic", "toon": "cartoon", "disney": "cartoon", "pixar": "cartoon", "animated": "cartoon",
	"synthwave": "vaporwave", "retrowave": "vaporwave", "retro_wave": "vaporwave", "outrun": "vaporwave",
	"lowpoly": "low_poly", "low_polygon": "low_poly", "pop_art": "popart",
	"cyber_punk": "cyberpunk", "steam_punk": "steampunk", "sci_fi": "cyberpunk", "scifi": "cyberpunk",
	"vector_art": "vector", "flat": "vector", "flat_design": "vector", "illustration": "vector",
	"neon_lights": "neon", "black_and_white": "monochrome", "bw": "monochrome", "b&w": "monochrome", "black_white": "monochrome",
	"vintage": "sepia", "old_photo": "sepia", "film": "lomography", "analog": "lomography", "tiltshift": "tilt_shift", "miniature": "tilt_shift",
	"long_exposure": "long_exp", "double_exposure": "double_exp", "night": "night_mode", "space": "astro", "astrophotography": "astro",
	"drone": "aerial", "aerial_view": "aerial", "product_photo": "product", "car": "automotive", "cars": "automotive",
	"animal": "wildlife", "animals": "wildlife", "nature": "landscape", "city": "street", "building": "architecture",
	"fashion_photo": "fashion", "food_photo": "food", "sport": "sports", "magical": "fantasy", "dark_fantasy": "gothic", "dark": "gothic",
	"geometric": "cubist", "dreamlike": "surrealist", "surreal": "surrealist", "surrealism": "surrealist", "impressionism": "impressionist",
	"expressionism": "expressionist", "cubism": "cubist", "minimal": "minimalist", "minimalism": "minimalist",
}

type styleCache struct {
	mu         sync.Mutex
	list       []StudioStyle
	shapes     []string
	fetched    time.Time
	retryAfter time.Time
}

const (
	styleCacheFor   = 6 * time.Hour
	styleRetryAfter = 2 * time.Minute
	styleReadFor    = 8 * time.Second
)

func (r *Registry) studioCatalog(ctx context.Context) ([]StudioStyle, []string) {
	c := &r.styles
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.list) > 0 && time.Since(c.fetched) < styleCacheFor {
		return c.list, c.shapes
	}
	list, shapes := builtinStyles(), studioShapes
	if !r.StudioReady() || time.Now().Before(c.retryAfter) {
		return list, shapes
	}
	rctx, cancel := context.WithTimeout(ctx, styleReadFor)
	defer cancel()
	raw, err := r.studioCall(rctx, "styles", nil)
	var got []StudioStyle
	if err != nil || json.Unmarshal(raw, &got) != nil || len(got) == 0 {
		if ctx.Err() == nil {
			c.retryAfter = time.Now().Add(styleRetryAfter)
		}
		return list, shapes
	}
	list = got
	if raw, err := r.studioCall(rctx, "aspects", nil); err == nil {
		var got []string
		if json.Unmarshal(raw, &got) == nil && len(got) > 0 {
			shapes = got
		}
	}
	c.list, c.shapes, c.fetched = list, shapes, time.Now()
	return list, shapes
}

func builtinStyles() []StudioStyle {
	out := make([]StudioStyle, len(studioStyleIDs))
	for i, id := range studioStyleIDs {
		out[i] = StudioStyle{ID: id}
	}
	return out
}

func normStyle(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.NewReplacer(" ", "_", "-", "_", "/", "_", ".", "").Replace(s)
	s = strings.TrimSuffix(s, "_style")
	return strings.Trim(s, "_")
}

func matchStyle(raw string, list []StudioStyle) (id string, ok bool) {
	if strings.TrimSpace(raw) == "" {
		return "", true
	}
	n := normStyle(raw)
	for _, s := range list {
		if s.ID == n || (s.Name != "" && strings.TrimSpace(s.Name) == strings.TrimSpace(raw)) {
			return s.ID, true
		}
	}
	if a, found := styleAliases[n]; found {
		for _, s := range list {
			if s.ID == a {
				return a, true
			}
		}
	}

	var hits []string
	for _, s := range list {
		if len(s.ID) >= 4 && strings.Contains(n, s.ID) {
			hits = append(hits, s.ID)
		}
	}
	if len(hits) > 0 {
		sort.Slice(hits, func(i, j int) bool { return len(hits[i]) > len(hits[j]) })
		clear := true
		for _, h := range hits[1:] {
			if !strings.Contains(hits[0], h) {
				clear = false
			}
		}
		if clear {
			return hits[0], true
		}
	}

	if len(n) >= 4 {
		var pre []string
		for _, s := range list {
			if strings.HasPrefix(s.ID, n) {
				pre = append(pre, s.ID)
			}
		}
		if len(pre) == 1 {
			return pre[0], true
		}
	}
	return "", false
}

func styleHelp(list []StudioStyle) string {
	ids := make([]string, len(list))
	for i, s := range list {
		ids[i] = s.ID
	}
	return strings.Join(ids, ", ")
}

func normShape(s string) (w, h int, ok bool) {
	s = strings.NewReplacer("/", ":", "x", ":", "X", ":", "×", ":", "؛", ":", " ", "").Replace(strings.TrimSpace(s))
	a, b, found := strings.Cut(s, ":")
	if !found {
		return 0, 0, false
	}
	w, err1 := strconv.Atoi(a)
	h, err2 := strconv.Atoi(b)
	if err1 != nil || err2 != nil || w <= 0 || h <= 0 || w > 1000 || h > 1000 {
		return 0, 0, false
	}
	return w, h, true
}

func nearestShape(want string, shapes []string) string {
	w, h, ok := normShape(want)
	if !ok {
		return "1:1"
	}
	target := math.Log(float64(w) / float64(h))
	best, bestDist := "1:1", math.MaxFloat64
	for _, sh := range shapes {
		sw, sh2, ok := normShape(sh)
		if !ok {
			continue
		}
		if sw == w && sh2 == h {
			return fmt.Sprintf("%d:%d", w, h)
		}
		if d := math.Abs(math.Log(float64(sw)/float64(sh2)) - target); d < bestDist {
			best, bestDist = fmt.Sprintf("%d:%d", sw, sh2), d
		}
	}
	return best
}

func (r *Registry) studioCall(ctx context.Context, action string, extra map[string]any) (json.RawMessage, error) {
	body := map[string]any{"action": action, "api_key": r.studioKey()}
	for k, v := range extra {
		body[k] = v
	}
	payload, _ := json.Marshal(body)
	cctx, cancel := context.WithTimeout(ctx, nestCallTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, r.imageAPI, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("the image service is unreachable: %w", err)
	}
	data, rerr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	resp.Body.Close()
	if rerr != nil {
		return nil, fmt.Errorf("reading the answer: %w", rerr)
	}
	var out struct {
		Success *bool           `json:"success"`
		Data    json.RawMessage `json:"data"`
		Error   string          `json:"error"`
		Message string          `json:"message"`
	}
	if json.Unmarshal(data, &out) != nil {
		return nil, fmt.Errorf("the image service answered HTTP %d with an unexpected body", resp.StatusCode)
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, ErrImageKey
	case out.Success != nil && *out.Success && len(out.Data) > 0:
		return out.Data, nil
	}
	msg := strings.TrimSpace(out.Error)
	if msg == "" {
		msg = strings.TrimSpace(out.Message)
	}
	if msg == "" {
		msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
	}
	if len([]rune(msg)) > 300 {
		msg = string([]rune(msg)[:300]) + "…"
	}
	if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusRequestTimeout && resp.StatusCode != http.StatusTooManyRequests {
		return nil, fmt.Errorf("%w: %s", errStudioRefused, msg)
	}
	return nil, errors.New(msg)
}

func (r *Registry) CheckImageKey(ctx context.Context, key string) (styles int, err error) {
	saved := r.imageKey
	r.imageKey = func() string { return key }
	defer func() { r.imageKey = saved }()
	if r.imageAPI == "" {
		return 0, errors.New("the image service is turned off (WAZE_IMAGE_API_URL=off)")
	}
	raw, err := r.studioCall(ctx, "styles", nil)
	if err != nil {
		return 0, err
	}
	var got []StudioStyle
	if json.Unmarshal(raw, &got) != nil {
		return 0, errors.New("the image service answered something unexpected")
	}
	return len(got), nil
}

func (r *Registry) imageViaStudio(ctx context.Context, sessionID, prompt, style, ratio, name string, n int) ([]savedImage, error) {
	extra := map[string]any{"prompt": prompt, "aspect": ratio}
	if style != "" {
		extra["style"] = style
	}
	raw, err := r.studioCall(ctx, "generate", extra)
	if err != nil {
		return nil, err
	}
	var out struct {
		Images []string `json:"images"`
	}
	if json.Unmarshal(raw, &out) != nil || len(out.Images) == 0 {
		return nil, errors.New("the image service gave no picture")
	}
	if n < 1 {
		n = 1
	}
	var saved []savedImage
	var firstErr error
	for _, link := range out.Images {
		if len(saved) >= n {
			break
		}
		file := name
		if len(saved) > 0 {
			file = secondName(name, len(saved)+1)
		}
		size, err := r.downloadStudioImage(ctx, sessionID, link, file, ratio)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		saved = append(saved, savedImage{name: file, size: size})
	}
	if len(saved) == 0 {
		return nil, firstErr
	}
	return saved, nil
}

type savedImage struct {
	name string
	size int64
}

func secondName(name string, n int) string {
	ext := path.Ext(name)
	return fmt.Sprintf("%s_%d%s", strings.TrimSuffix(name, ext), n, ext)
}

func (r *Registry) downloadStudioImage(ctx context.Context, sessionID, link, name, ratio string) (int64, error) {
	dctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(dctx, http.MethodGet, link, nil)
	if err != nil {
		return 0, errors.New("the image service gave an invalid image link")
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return 0, fmt.Errorf("downloading the image: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("downloading the image: HTTP %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "image/") {
		return 0, fmt.Errorf("downloading the image: unexpected content type %q", ct)
	}
	n, err := r.saveStream(sessionID, name, resp.Body, 20<<20)
	if err != nil {
		return 0, err
	}
	r.fitImage(sessionID, name, ratio)
	if _, fi, err := r.Machine.Open(sessionID, name); err == nil {
		n = fi.Size()
	}
	return n, nil
}

func (r *Registry) fitImage(sessionID, name, ratio string) {
	if strings.EqualFold(path.Ext(name), ".png") {
		if f, _, err := r.Machine.Open(sessionID, name); err == nil {
			head := make([]byte, 3)
			_, _ = io.ReadFull(f, head)
			f.Close()
			if bytes.Equal(head, []byte{0xff, 0xd8, 0xff}) {
				r.reencode(sessionID, name)
			}
		}
	}
	r.cropToRatio(sessionID, name, ratio)
}

func (r *Registry) reencode(sessionID, name string) {
	f, _, err := r.Machine.Open(sessionID, name)
	if err != nil {
		return
	}
	img, _, err := image.Decode(f)
	f.Close()
	if err != nil {
		return
	}
	var buf bytes.Buffer
	switch strings.ToLower(path.Ext(name)) {
	case ".png":
		err = png.Encode(&buf, img)
	default:
		err = jpeg.Encode(&buf, img, &jpeg.Options{Quality: 92})
	}
	if err != nil {
		return
	}
	w, err := r.Machine.Create(sessionID, name)
	if err != nil {
		return
	}
	_, err = w.Write(buf.Bytes())
	if cerr := w.Close(); err == nil {
		err = cerr
	}
	_ = err
}

func styledPrompt(prompt, style string) string {
	if style == "" || style == "realistic" {
		return prompt
	}
	return prompt + ", " + strings.ReplaceAll(style, "_", " ") + " style"
}
