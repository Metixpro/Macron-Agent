package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/telegram"
	"github.com/Metixpro/macron-agent/pkg/textutil"
	"github.com/Metixpro/macron-agent/pkg/tools"
)

const (
	maxEmbedFiles   = 20
	maxEmbedBytes   = telegram.MaxUploadBytes
	maxMediaStrikes = 3
	mediaRetryAfter = 6 * time.Hour
	embedTimeout    = 4 * time.Minute
)

var urlSchemes = []string{"http://", "https://", "tg://"}

var mediaRef = regexp.MustCompile(`!\[([^\]\n]*)\]\(\s*<?([^()<>"\s][^()<>"\n]*?)>?(?:\s+"((?:[^"\\\n]|\\.)*)")?\s*\)`)

type mediaFile struct {
	id      string
	name    string
	typ     string
	size    int64
	caption string
}

type answerPart struct {
	md    string
	plain string
	files []mediaFile
	bytes int64
}

func (p *answerPart) add(f mediaFile) mediaFile {
	for _, have := range p.files {
		if have.name == f.name {
			return have
		}
	}
	f.id = fmt.Sprintf("m%d", len(p.files))
	p.files = append(p.files, f)
	p.bytes += f.size
	return f
}

func (p *answerPart) fits(f mediaFile) bool {
	if tools.NativeKind(f.name) != "" {
		return false
	}
	for _, have := range p.files {
		if have.name == f.name {
			return true
		}
	}
	return len(p.files) < maxEmbedFiles && p.bytes+f.size <= maxEmbedBytes
}

func (f mediaFile) link() string {
	s := "![](tg://" + telegram.RichLink(f.typ) + "?id=" + f.id
	if f.caption != "" {
		s += ` "` + f.caption + `"`
	}
	return s + ")"
}

func isURL(target string) bool {
	for _, scheme := range urlSchemes {
		if strings.HasPrefix(strings.ToLower(target), scheme) {
			return true
		}
	}
	return false
}

func cleanCaption(s string) string {
	s = strings.NewReplacer(`\"`, "'", `"`, "'", `\`, "").Replace(s)
	s = strings.Join(strings.Fields(s), " ")
	return textutil.TruncateRunes(s, 200, "…")
}

func (b *Bot) mediaKind(session, name string) (mediaFile, bool) {
	name = workspaceName(name)
	if name == "" {
		return mediaFile{}, false
	}
	fi, err := b.app.Machine.Stat(session, name)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() == 0 || fi.Size() > telegram.MaxUploadBytes {
		return mediaFile{}, false
	}
	f := mediaFile{name: name, size: fi.Size(), typ: "document"}
	switch ext := strings.ToLower(filepath.Ext(name)); {
	case imageExt[ext] && fi.Size() <= telegram.MaxPhotoBytes:
		f.typ = "photo"
	case ext == ".gif":
		f.typ = "animation"
	case videoExt[ext]:
		f.typ = "video"
	case audioExt[ext]:
		f.typ = "audio"
	}
	return f, true
}

func (f mediaFile) visual() bool { return f.typ == "photo" || f.typ == "video" || f.typ == "animation" }

func (b *Bot) planAnswer(l lang, session, answer string, deliver []string, captions map[string]string, embed bool) (parts []answerPart, rest []string) {
	chunks := []string{""}
	if strings.TrimSpace(answer) != "" {
		chunks = b.answerChunks(l, answer)
	}
	shown := map[string]bool{}
	addRest := func(name string) {
		if !shown[name] {
			rest = appendNew(rest, name)
		}
	}
	for _, chunk := range chunks {
		p := answerPart{}
		var md, plain []string
		inFence := false
		for _, line := range strings.Split(chunk, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "```") {
				inFence = !inFence
			}
			if inFence || !strings.Contains(line, "![") {
				md, plain = append(md, line), append(plain, line)
				continue
			}
			mdLine := mediaRef.ReplaceAllStringFunc(line, func(ref string) string {
				g := mediaRef.FindStringSubmatch(ref)
				alt, target, title := g[1], strings.TrimSpace(g[2]), g[3]
				if isURL(target) {
					return ref
				}
				caption := cleanCaption(title)
				if caption == "" {
					caption = cleanCaption(alt)
				}
				text := ""
				if caption != "" {
					text = "*" + caption + "*"
				}
				f, ok := b.mediaKind(session, target)
				switch {
				case !ok:
					return text
				case embed && p.fits(f):
					if caption == "" {
						caption = cleanCaption(stripMarkdown(captions[f.name]))
					}
					f.caption = caption
					shown[f.name] = true
					rest = removeName(rest, f.name)
					return p.add(f).link()
				default:
					addRest(f.name)
					return text
				}
			})
			md, plain = append(md, mdLine), append(plain, withoutMediaRefs(line))
		}
		p.md, p.plain = strings.TrimSpace(strings.Join(md, "\n")), strings.TrimSpace(strings.Join(plain, "\n"))
		parts = append(parts, p)
	}

	var top, bottom []mediaFile
	first, last := &parts[0], &parts[len(parts)-1]
	for _, name := range deliver {
		name = workspaceName(name)
		if shown[name] {
			continue
		}
		f, ok := b.mediaKind(session, name)
		if !ok || !embed {
			addRest(name)
			continue
		}
		target := last
		if f.visual() {
			target = first
		}
		if !target.fits(f) {
			addRest(name)
			continue
		}
		f.caption = cleanCaption(stripMarkdown(captions[name]))
		f = target.add(f)
		shown[name] = true
		if f.visual() {
			top = append(top, f)
		} else {
			bottom = append(bottom, f)
		}
	}
	if len(top) > 0 {
		var lines []string
		for _, f := range top {
			lines = append(lines, f.link())
		}
		block := lines[0]
		if len(lines) > 1 {
			block = "<tg-slideshow>\n" + strings.Join(lines, "\n") + "\n</tg-slideshow>"
		}
		first.md = strings.TrimSpace(block + "\n\n" + first.md)
	}
	if len(bottom) > 0 {
		var lines []string
		for _, f := range bottom {
			lines = append(lines, f.link())
		}
		last.md = strings.TrimSpace(last.md + "\n\n" + strings.Join(lines, "\n\n"))
	}
	return parts, rest
}

func removeName(list []string, name string) []string {
	out := list[:0]
	for _, n := range list {
		if n != name {
			out = append(out, n)
		}
	}
	return out
}

func stripMarkdown(s string) string {
	return strings.NewReplacer("**", "", "__", "", "~~", "", "||", "", "`", "").Replace(s)
}

func withoutMediaRefs(md string) string {
	if !strings.Contains(md, "![") {
		return md
	}
	return mediaRef.ReplaceAllStringFunc(md, func(ref string) string {
		g := mediaRef.FindStringSubmatch(ref)
		if isURL(strings.TrimSpace(g[2])) {
			return ref
		}
		if c := cleanCaption(g[3]); c != "" {
			return "*" + c + "*"
		}
		if c := cleanCaption(g[1]); c != "" {
			return "*" + c + "*"
		}
		return ""
	})
}

func (b *Bot) sendAnswerWithFiles(ctx context.Context, m *telegram.Message, session, answer, head string, deliver []string, captions map[string]string,
	effect string, inPlace func(context.Context, string) error) (shown, rest []string) {
	embed := !b.richOff.Load() && (b.mediaStrikes.Load() < maxMediaStrikes ||
		time.Since(time.Unix(b.mediaFailedAt.Load(), 0)) > mediaRetryAfter)
	parts, rest := b.planAnswer(b.langOf(m.From), session, answer, deliver, captions, embed)
	if head != "" && len(parts) > 0 {
		parts[0].md, parts[0].plain = head+"\n\n"+parts[0].md, head+"\n\n"+parts[0].plain
	}

	withEffect := func(opts telegram.SendOptions, send func(telegram.SendOptions) error) error {
		err := send(opts)
		if err != nil && opts.EffectID != "" && !telegram.IsUnreachable(err) && ctx.Err() == nil {
			log.Printf("[chat %d] message effect refused (%v); sending without it", m.Chat.ID, err)
			opts.EffectID = ""
			err = send(opts)
		}
		return err
	}
	for i, p := range parts {
		opts := telegram.SendOptions{}.InTopic(m)
		if i == 0 {
			opts.ReplyTo, opts.EffectID = m.MessageID, effect
		}
		if i > 0 {
			time.Sleep(350 * time.Millisecond)
		}
		if len(p.files) > 0 {
			var total int64
			for _, f := range p.files {
				if fi, err := b.app.Machine.Stat(session, f.name); err == nil {
					total += fi.Size()
				}
			}
			uctx, ucancel := b.uploadCtx(ctx, total)
			err := withEffect(opts, func(o telegram.SendOptions) error { return b.sendRichFiles(uctx, m.Chat.ID, session, p, o) })
			ucancel()
			if err == nil {
				b.mediaStrikes.Store(0)
				for _, f := range p.files {
					shown = appendNew(shown, f.name)
				}
				continue
			}
			if telegram.IsUnreachable(err) || ctx.Err() != nil {
				return shown, rest
			}
			b.mediaFailedAt.Store(time.Now().Unix())
			if n := b.mediaStrikes.Add(1); n == maxMediaStrikes {
				log.Printf("files inside rich messages failed %d times in a row (%v); sending them separately from now on", n, err)
			} else {
				log.Printf("[chat %d] answer with %d files failed (%v); sending the files separately", m.Chat.ID, len(p.files), err)
			}
			for _, f := range p.files {
				rest = appendNew(rest, f.name)
			}
			p.md = p.plain
		}
		if strings.TrimSpace(p.md) == "" {
			continue
		}
		if i == 0 && inPlace != nil && effect == "" {
			if err := inPlace(ctx, p.md); err == nil {
				continue
			} else if ctx.Err() == nil && !errors.Is(err, errNoLiveMessage) {
				log.Printf("[chat %d] the live message could not become the answer (%v); sending it", m.Chat.ID, err)
			}
		}
		if err := withEffect(opts, func(o telegram.SendOptions) error { return b.sendRich(ctx, m.Chat.ID, p.md, o) }); err != nil {
			if telegram.IsUnreachable(err) {
				return shown, rest
			}
			log.Printf("[chat %d] answer part %d failed: %v; sending it as plain text", m.Chat.ID, i, err)

			if _, err := b.api.SendHTMLParts(ctx, m.Chat.ID, telegram.EscapeHTML(p.md), telegram.SendOptions{}.InTopic(m)); err != nil {
				log.Printf("[chat %d] answer part %d is lost: %v", m.Chat.ID, i, err)
			}
		}
	}
	return shown, rest
}

func (b *Bot) sendRichFiles(ctx context.Context, chatID int64, session string, p answerPart, opts telegram.SendOptions) error {
	ctx, cancel := context.WithTimeout(ctx, embedTimeout)
	defer cancel()
	action := "upload_document"
	for _, f := range p.files {
		if f.visual() {
			action = "upload_photo"
			break
		}
	}
	_ = b.api.SendChatAction(ctx, chatID, action)
	rtl := telegram.IsMostlyRTL(p.plain)
	send := func(md string) error {
		files := make([]telegram.RichFile, 0, len(p.files))
		defer func() {
			for _, f := range files {
				if c, ok := f.File.Reader.(interface{ Close() error }); ok {
					c.Close()
				}
			}
		}()
		for _, f := range p.files {
			r, _, err := b.app.Machine.Open(session, f.name)
			if err != nil {
				return err
			}
			files = append(files, telegram.RichFile{ID: f.id, Type: f.typ, File: telegram.File{Name: filepath.Base(f.name), Reader: r}})
		}
		return b.api.SendRichMessageFiles(ctx, chatID, telegram.RichMessage{Markdown: md, IsRTL: rtl}, files, opts)
	}
	if set := b.premium(); set != nil {
		err := send(set.Markdown(p.md))
		if err == nil || telegram.IsUnreachable(err) || ctx.Err() != nil {
			return err
		}
	}
	return send(p.md)
}
