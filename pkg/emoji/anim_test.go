package emoji

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The drawings are what Telegram takes for animated emoji: 512×512 at
// 60 fps, at most three seconds and 64 KB, shape layers only.
func TestStatusArtIsWhatTelegramTakes(t *testing.T) {
	art := StatusArt()
	if len(art) != 2 || art[0].Key != "listen" || art[1].Key != "transcribe" {
		t.Fatalf("art = %+v", art)
	}
	for _, a := range art {
		if len(a.TGS) > 64<<10 {
			t.Errorf("%s: %d bytes", a.Key, len(a.TGS))
		}
		zr, err := gzip.NewReader(bytes.NewReader(a.TGS))
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := io.ReadAll(zr)
		var doc struct {
			TGS    int              `json:"tgs"`
			FR     float64          `json:"fr"`
			Layers []map[string]any `json:"layers"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatal(err)
		}
		var top map[string]any
		_ = json.Unmarshal(raw, &top)
		if doc.TGS != 1 || doc.FR != 60 || top["op"].(float64) > 180 || top["w"].(float64) != 512 || top["h"].(float64) != 512 {
			t.Errorf("%s: %s", a.Key, raw[:120])
		}
		for _, l := range doc.Layers {
			if l["ty"].(float64) != 4 {
				t.Errorf("%s: a layer of type %v", a.Key, l["ty"])
			}
		}
		if strings.Contains(string(raw), `,"ty":`) {
			t.Errorf("%s: a type that is not its object's first key", a.Key)
		}
		for _, banned := range []string{`"x":"`, `"ty":"mm"`, `"ty":"rp"`, `"ty":"gs"`, `"masksProperties"`, `"tt":`, `"refId"`} {
			if strings.Contains(string(raw), banned) {
				t.Errorf("%s uses %s, which Telegram refuses", a.Key, banned)
			}
		}
		if dir := os.Getenv("WAZE_EMOJI_DUMP"); dir != "" {
			_ = os.WriteFile(filepath.Join(dir, a.Key+".tgs"), a.TGS, 0o644)
		}
	}
}
