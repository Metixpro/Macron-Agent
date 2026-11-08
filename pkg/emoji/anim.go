package emoji

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"math"
	"sort"
)

// Animated is a premium emoji the bot draws itself: a Telegram animated
// sticker (.tgs, gzipped Lottie) for a custom emoji set, shown in place of
// Emoji.
type Animated struct {
	Key   string // the status it belongs to
	Emoji string // the plain emoji it stands for
	TGS   []byte
}

// What Telegram takes for an animated emoji: a 512×512 canvas, 60 fps, at
// most 3 seconds and 64 KB.
const (
	canvas   = 512
	fps      = 60
	loop     = 120 // frames: two seconds
	sampleBy = 4   // frames between sampled keyframes
)

// StatusArtVersion names the drawings: change one or add one, and this goes
// up, so the bot makes a new set instead of finding the old one.
const StatusArtVersion = 2

// StatusArt is the bot's avatar, the white puffy star with navy eyes and a
// blue glow, in its statuses: «listening» to a voice message (it leans
// toward the sound coming in and lights up as it hears) and listening to a
// recording for transcribe_audio (headphones on, nodding to the beat).
func StatusArt() []Animated {
	return []Animated{
		{Key: "listen", Emoji: "🎙", TGS: tgs("listening", hearing())},
		{Key: "transcribe", Emoji: "🎧", TGS: tgs("transcribing", headphones())},
	}
}

type obj map[string]any

// MarshalJSON writes "ty" first: rlottie, which Telegram draws with, reads
// an object's type before the rest of it and skips what it cannot place.
func (o obj) MarshalJSON() ([]byte, error) {
	keys := make([]string, 0, len(o))
	for k := range o {
		if k != "ty" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	if _, ok := o["ty"]; ok {
		keys = append([]string{"ty"}, keys...)
	}
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		v, err := json.Marshal(o[k])
		if err != nil {
			return nil, err
		}
		b.WriteString(`"` + k + `":`)
		b.Write(v)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func tgs(name string, layers []obj) []byte {
	for i, l := range layers {
		l["ind"] = i + 1
	}
	doc := obj{"tgs": 1, "v": "5.5.2", "nm": name, "fr": fps, "ip": 0, "op": loop, "w": canvas, "h": canvas,
		"ddd": 0, "assets": []any{}, "layers": layers}
	raw, _ := json.Marshal(doc)
	var buf bytes.Buffer
	zw, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
	_, _ = zw.Write(raw)
	_ = zw.Close()
	return buf.Bytes()
}

// The avatar's colors.
var (
	starLight = rgb(0xFBF9F6)
	starShade = rgb(0xC8D3F7)
	eyeNavy   = rgb(0x0D1655)
	glowBlue  = rgb(0x2F6BFF)
	gearBlue  = rgb(0x2C5BF0)
	gearDeep  = rgb(0x1A2F9E)
)

func rgb(c int) []float64 {
	return []float64{float64(c>>16&255) / 255, float64(c>>8&255) / 255, float64(c&255) / 255, 1}
}

func fixed(v any) obj { return obj{"a": 0, "k": v} }

// sampled animates a value over the loop from f(progress in [0, 1)), with
// linear keyframes; the last one equals the first, so the loop is seamless.
func sampled(f func(p float64) any) obj {
	var keys []obj
	for t := 0; t <= loop; t += sampleBy {
		k := obj{"t": t, "s": f(float64(t%loop) / loop)}
		if t < loop {
			k["i"] = obj{"x": []float64{1}, "y": []float64{1}}
			k["o"] = obj{"x": []float64{0}, "y": []float64{0}}
		}
		keys = append(keys, k)
	}
	return obj{"a": 1, "k": keys}
}

func num(v ...float64) []float64 {
	out := make([]float64, len(v))
	for i, x := range v {
		out[i] = math.Round(x*100) / 100
	}
	return out
}

func layer(name string, shapes ...obj) obj {
	return obj{"ddd": 0, "ty": 4, "nm": name, "sr": 1, "ao": 0, "bm": 0, "ip": 0, "op": loop, "st": 0,
		"ks": obj{"o": fixed(100), "r": fixed(0), "p": fixed([]float64{0, 0, 0}), "a": fixed([]float64{0, 0, 0}),
			"s": fixed([]float64{100, 100, 100})},
		"shapes": shapes}
}

// move is a group's transform; a nil field stays still. pos is where the
// anchor goes, scale is in percent, turn in degrees (clockwise).
type move struct {
	anchor                    []float64
	pos, scale, turn, opacity obj
}

func group(m move, items ...obj) obj {
	if m.anchor == nil {
		m.anchor = []float64{0, 0}
	}
	or := func(v, def obj) obj {
		if v == nil {
			return def
		}
		return v
	}
	tr := obj{"ty": "tr", "a": fixed(m.anchor), "p": or(m.pos, fixed(m.anchor)), "s": or(m.scale, fixed([]float64{100, 100})),
		"r": or(m.turn, fixed(0)), "o": or(m.opacity, fixed(100)), "sk": fixed(0), "sa": fixed(0)}
	return obj{"ty": "gr", "it": append(items, tr)}
}

func fill(c []float64) obj { return obj{"ty": "fl", "c": fixed(c), "o": fixed(100), "r": 1} }

func stroke(c []float64, width, opacity float64) obj {
	return obj{"ty": "st", "c": fixed(c), "o": fixed(opacity), "w": fixed(width), "lc": 2, "lj": 2, "ml": 4}
}

func strokeOf(c []float64, width obj) obj {
	return obj{"ty": "st", "c": fixed(c), "o": fixed(100), "w": width, "lc": 2, "lj": 2, "ml": 4}
}

func ellipse(cx, cy, w, h float64) obj {
	return obj{"ty": "el", "d": 1, "p": fixed(num(cx, cy)), "s": fixed(num(w, h))}
}

func rect(cx, cy, w, h, r float64) obj {
	return obj{"ty": "rc", "d": 1, "p": fixed(num(cx, cy)), "s": fixed(num(w, h)), "r": fixed(r)}
}

// path is a cubic Bézier path: points with their in and out tangents.
type path struct {
	v, i, o [][]float64
}

func (p *path) add(x, y, ix, iy, ox, oy float64) {
	p.v = append(p.v, num(x, y))
	p.i = append(p.i, num(ix, iy))
	p.o = append(p.o, num(ox, oy))
}

func (p *path) shape(closed bool) obj {
	return obj{"ty": "sh", "ks": fixed(obj{"c": closed, "v": p.v, "i": p.i, "o": p.o})}
}

// arc is an elliptical arc from a0 to a1 (degrees, clockwise from 3 o'clock).
func arc(cx, cy, rx, ry, a0, a1 float64) *path {
	n := int(math.Ceil(math.Abs(a1-a0) / 90))
	step := (a1 - a0) / float64(n) * math.Pi / 180
	k := 4.0 / 3 * math.Tan(step/4)
	var p path
	for s := 0; s <= n; s++ {
		a := a0*math.Pi/180 + float64(s)*step
		tx, ty := -math.Sin(a)*rx*k, math.Cos(a)*ry*k
		p.add(cx+rx*math.Cos(a), cy+ry*math.Sin(a), -tx, -ty, tx, ty)
	}
	return &p
}

// star is the avatar's outline, measured from its picture: the radius goes
// with the angle from the top as 1 - 0.12·(1 - cos 7θ), seven round points.
func star(cx, cy, r float64) obj {
	const n, depth = 56, 0.12
	step := 2 * math.Pi / n
	var p path
	for k := range n {
		t := float64(k) * step
		rad := r * (1 - depth*(1-math.Cos(7*t)))
		d := -r * depth * 7 * math.Sin(7*t) // how the radius changes with the angle
		dx, dy := (d*math.Sin(t)+rad*math.Cos(t))*step/3, (-d*math.Cos(t)+rad*math.Sin(t))*step/3
		p.add(cx+rad*math.Sin(t), cy-rad*math.Cos(t), -dx, -dy, dx, dy)
	}
	return p.shape(true)
}

// avatar draws the star at (cx, cy): the glow (glow animates its strength),
// the body with its shading, and the eyes, which look by look (an offset)
// and blink by blink (their height in percent).
func avatar(cx, cy, r float64, glow, look, blink obj) []obj {
	eye := func(dx float64) obj {
		return group(move{anchor: []float64{cx + dx, cy}, scale: blink},
			group(move{}, ellipse(cx+dx+0.03*r, cy-0.07*r, 0.05*r, 0.1*r), fill(rgb(0x4A5BBE))), // a glint
			group(move{}, ellipse(cx+dx, cy+0.03*r, 0.155*r, 0.37*r), fill(eyeNavy)))
	}
	eyes := group(move{pos: look}, eye(-0.21*r), eye(0.21*r))
	// The light falls from the top left: a white star over a shaded one.
	body := group(move{},
		group(move{anchor: []float64{cx, cy}, pos: fixed(num(cx-0.02*r, cy-0.035*r)), scale: fixed([]float64{95, 95})},
			star(cx, cy, r), fill(starLight)),
		group(move{}, star(cx, cy, r), fill(starShade)))
	halo := group(move{opacity: glow},
		group(move{}, star(cx, cy, r), stroke(rgb(0x8DB2FF), 0.12*r, 100)),
		group(move{}, star(cx, cy, r), stroke(rgb(0x3F7BFF), 0.24*r, 75)),
		group(move{}, star(cx, cy, r), stroke(glowBlue, 0.38*r, 32)),
		group(move{}, star(cx, cy, r), stroke(glowBlue, 0.54*r, 12)))
	return []obj{eyes, body, halo}
}

// blinking closes the eyes once a loop, around at (a progress).
func blinking(at float64) obj {
	return sampled(func(p float64) any {
		d := math.Abs(p - at)
		if d > 0.05 {
			return num(100, 100)
		}
		return num(100, 12+88*d/0.05)
	})
}

func wave(p, delay float64) float64 { return math.Mod(p*2+delay, 1) }

func hearing() []obj {
	const cx, cy, r = 222, 262, 156
	// It leans toward the sound on its right, a little more as each word
	// reaches it, and glows brighter while it hears.
	near := func(p float64) float64 { return math.Pow(math.Sin(math.Pi*wave(p, 0.15)), 2) }
	head := avatar(cx, cy, r,
		sampled(func(p float64) any { return num(78 + 22*near(p)) }),
		sampled(func(p float64) any { return num(0.1*r+0.03*r*near(p), -0.02*r) }),
		blinking(0.62))
	body := layer("avatar", group(move{anchor: []float64{cx, cy + 0.6*r},
		turn: sampled(func(p float64) any { return num(7 + 4*near(p)) })}, head...))
	layers := []obj{body}
	// Two waves, half a beat apart, come in from the right and fade as they
	// reach it; the line keeps its width as they shrink.
	for _, delay := range []float64{0, 0.5} {
		size := func(p float64) float64 { return 100 - 26*wave(p, delay) } // 245 to 180 from its center
		items := []obj{arc(cx, cy, 245, 245, -38, 38).shape(false),
			strokeOf(glowBlue, sampled(func(p float64) any { return num(30 * 100 / size(p)) }))}
		layers = append(layers, layer("sound", group(move{anchor: []float64{cx, cy},
			scale:   sampled(func(p float64) any { return num(size(p), size(p)) }),
			opacity: sampled(func(p float64) any { return num(100 * math.Pow(math.Sin(math.Pi*wave(p, delay)), 1.5)) })},
			items...)))
	}
	return layers
}

func headphones() []obj {
	const cx, cy, r = 256, 284, 150
	beat := func(p float64) float64 { return math.Abs(math.Sin(2 * math.Pi * p)) } // two beats a loop
	// The headphones sit on its side points; the band goes over the top one.
	side := 0.22 * r
	gear := group(move{},
		group(move{}, rect(cx-0.88*r, cy+side, 0.14*r, 0.46*r, 0.07*r), rect(cx+0.88*r, cy+side, 0.14*r, 0.46*r, 0.07*r),
			fill(gearDeep)),
		group(move{}, rect(cx-0.99*r, cy+side, 0.34*r, 0.62*r, 0.15*r), rect(cx+0.99*r, cy+side, 0.34*r, 0.62*r, 0.15*r),
			fill(gearBlue)),
		group(move{}, arc(cx, cy+side, 0.99*r, 1.3*r, 180, 360).shape(false), stroke(gearBlue, 0.15*r, 100)))
	head := append([]obj{gear}, avatar(cx, cy, r,
		sampled(func(p float64) any { return num(85 + 15*beat(p)) }),
		fixed(num(0, 0)),
		blinking(0.4))...)
	// It nods to the beat: down and a little to each side.
	return []obj{layer("avatar", group(move{anchor: []float64{cx, cy + r},
		pos:  sampled(func(p float64) any { return num(cx, cy+r+14*beat(p)-7) }),
		turn: sampled(func(p float64) any { return num(5 * math.Sin(2*math.Pi*p)) })}, head...))}
}
