package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/agent"
	"github.com/Metixpro/macron-agent/pkg/machine"
	"github.com/Metixpro/macron-agent/pkg/telegram"
)

const (
	visionMaxSide   = 1568
	visionMaxPixels = 40_000_000
)

var errNotImage = errors.New("not an image")

func visionImage(data []byte) (url, desc string, err error) {
	mime := http.DetectContentType(data)
	if !strings.HasPrefix(mime, "image/") {
		return "", "", errNotImage
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		if mime == "image/webp" && len(data) <= maxVisionImage {
			return dataURL(mime, data), "WebP image", nil
		}
		return "", "", fmt.Errorf("%w (%s cannot be opened; convert it to PNG or JPEG first)", errNotImage, mime)
	}
	desc = fmt.Sprintf("%d×%d %s", cfg.Width, cfg.Height, strings.ToUpper(format))
	if cfg.Width*cfg.Height > visionMaxPixels {
		return "", desc, fmt.Errorf("the image is too large to look at (%s)", desc)
	}
	if format == "jpeg" && max(cfg.Width, cfg.Height) <= visionMaxSide && len(data) <= maxVisionImage {
		return dataURL("image/jpeg", data), desc, nil
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return "", desc, fmt.Errorf("could not decode the image: %w", err)
	}
	img = shrink(img, visionMaxSide)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, flatten(img), &jpeg.Options{Quality: 85}); err != nil {
		return "", desc, err
	}
	return dataURL("image/jpeg", buf.Bytes()), desc, nil
}

func shrink(img image.Image, maxSide int) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= maxSide && h <= maxSide {
		return img
	}
	scale := float64(maxSide) / float64(max(w, h))
	nw, nh := max(1, int(math.Round(float64(w)*scale))), max(1, int(math.Round(float64(h)*scale)))
	dst := image.NewRGBA(image.Rect(0, 0, nw, nh))
	for y := 0; y < nh; y++ {
		y0, y1 := b.Min.Y+y*h/nh, b.Min.Y+(y+1)*h/nh
		y1 = max(y1, y0+1)
		for x := 0; x < nw; x++ {
			x0, x1 := b.Min.X+x*w/nw, b.Min.X+(x+1)*w/nw
			x1 = max(x1, x0+1)
			var r, g, bl, a, n uint64
			for sy := y0; sy < y1; sy++ {
				for sx := x0; sx < x1; sx++ {
					cr, cg, cb, ca := img.At(sx, sy).RGBA()
					r, g, bl, a, n = r+uint64(cr), g+uint64(cg), bl+uint64(cb), a+uint64(ca), n+1
				}
			}
			dst.SetRGBA64(x, y, color.RGBA64{R: uint16(r / n), G: uint16(g / n), B: uint16(bl / n), A: uint16(a / n)})
		}
	}
	return dst
}

func flatten(img image.Image) image.Image {
	b := img.Bounds()
	out := image.NewRGBA(b)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bl, a := img.At(x, y).RGBA()
			inv := 0xffff - a
			out.SetRGBA64(x, y, color.RGBA64{R: uint16(r + inv), G: uint16(g + inv), B: uint16(bl + inv), A: 0xffff})
		}
	}
	return out
}

func (b *Bot) viewImages(session string, args map[string]any) agent.ToolOutput {
	var names []string
	switch v := args["filenames"].(type) {
	case []any:
		for _, x := range v {
			if s, ok := x.(string); ok && strings.TrimSpace(s) != "" {
				names = append(names, s)
			}
		}
	case string:
		names = append(names, v)
	}
	if n, ok := args["filename"].(string); ok && strings.TrimSpace(n) != "" {
		names = append(names, n)
	}
	if len(names) == 0 {
		return agent.ToolOutput{Text: "error: give at least one image file name"}
	}
	if len(names) > 4 {
		names = names[:4]
	}
	var (
		res   agent.ToolOutput
		lines []string
	)
	for _, name := range names {
		name = workspaceName(name)
		data, truncated, err := b.app.Machine.ReadFile(session, name, 30<<20)
		switch {
		case err != nil:
			lines = append(lines, name+": cannot be opened (missing or not a regular file)")
			continue
		case truncated:
			lines = append(lines, name+": larger than 30 MB, too big to look at")
			continue
		}
		url, desc, err := visionImage(data)
		if err != nil {
			lines = append(lines, name+": "+err.Error())
			continue
		}
		res.Images = append(res.Images, url)
		lines = append(lines, fmt.Sprintf("image %d = %s (%s, %s)", len(res.Images), name, desc, sizeText(int64(len(data)))))
	}
	res.Text = strings.Join(lines, "\n")
	if len(res.Images) > 0 {
		res.Text += "\nThe images follow in this order."
	}
	return res
}

func (b *Bot) hasFFmpeg() bool {
	b.ffmpegOnce.Do(func() { b.ffmpeg = b.app.Machine.Available("ffmpeg")["ffmpeg"] })
	return b.ffmpeg
}

func (b *Bot) framePreview(ctx context.Context, session, name string, seconds int, thumb *telegram.PhotoSize, still bool) (string, string) {
	if still && name != "" {
		if data, truncated, err := b.app.Machine.ReadFile(session, name, maxVisionImage); err == nil && !truncated {
			if url, _, err := visionImage(data); err == nil {
				return url, "It is attached below."
			}
		}
	}
	if name != "" && b.hasFFmpeg() {
		out := ".preview_" + strings.NewReplacer("/", "_", ".", "_").Replace(name) + ".jpg"
		vf := "scale=640:-2"
		if !still {
			rate := "2"
			if seconds > 0 {
				rate = fmt.Sprintf("4/%d", seconds)
			}
			vf = "fps=" + rate + ",scale=320:-2,tile=2x2:padding=4:color=white"
		}
		res := b.app.Machine.Run(ctx, session, machine.RunOptions{
			Args:    []string{"ffmpeg", "-v", "error", "-nostdin", "-i", "./" + name, "-vf", vf, "-frames:v", "1", "-y", out},
			Timeout: 30 * time.Second,
		})
		data, _, err := b.app.Machine.ReadFile(session, out, maxVisionImage)
		_ = b.app.Machine.Remove(session, out)
		if res.ExitCode == 0 && err == nil {
			if url, _, err := visionImage(data); err == nil {
				if still {
					return url, "It is attached below."
				}
				return url, "Four moments from it are attached below as a 2×2 grid (left to right, top to bottom, in time order)."
			}
		}
	}
	if thumb != nil {
		if data, err := b.fetch(ctx, thumb.FileID, maxVisionImage); err == nil {
			if url, _, err := visionImage(data); err == nil {
				return url, "A still frame of it is attached below."
			}
		}
	}
	return "", ""
}
