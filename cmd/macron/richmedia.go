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

// Files inside the answer. A rich message can show uploaded files in its
// text: pictures and videos (several of them as one swipeable slideshow),
// songs and documents. The model can place a file itself by writing
// ![caption](file.png) on its own line; files it does not place go above the
// text (pictures, videos) or below it (songs, documents). When Telegram
// refuses a rich message with files, the text goes alone and the files follow
// as albums.

const (
	maxEmbedFiles   = 20                      // per message (Telegram allows 50)
	maxEmbedBytes   = telegram.MaxUploadBytes // per message
	maxMediaStrikes = 3                       // failures in a row before files go separately
	mediaRetryAfter = 6 * time.Hour           // then try again once in a while
	embedTimeout    = 4 * time.Minute         // one upload
)

// urlSchemes mark media targets that are not workspace files.
var urlSchemes = []string{"http://", "https://", "tg://"}

// mediaRef is a Markdown media block: ![alt](target "title").
var mediaRef = regexp.MustCompile(`!\[([^\]\n]*)\]\(\s*<?([^()<>"\s][^()<>"\n]*?)>?(?:\s+"((?:[^"\\\n]|\\.)*)")?\s*\)`)

// mediaFile is a workspace file shown in a message.
type mediaFile struct {
	id      string
	name    string
	typ     string // photo, video, animation, audio, document
	size    int64
	caption string
}

// answerPart is one message of the answer.
type answerPart struct {
	md    string      // the text with its tg:// media links
	plain string      // the same text without files (the fallback)
	files []mediaFile // uploaded with the message
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
		return false // a voice or round video message is its own message
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

// mediaKind decides how a workspace file is shown; ok is false for a file
// that cannot be uploaded (missing, empty, too big, outside the workspace).
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

// planAnswer splits the answer into messages and decides which files go
// inside them (embed) and which are sent separately (rest). Placed files
// are shown even when the model did not hand them over with send_file.
func (b *Bot) planAnswer(l lang, session, answer string, deliver []string, captions map[string]string, embed bool) (parts []answerPart, rest []string) {
	chunks := []string{""}
	if strings.TrimSpace(answer) != "" {
		chunks = b.answerChunks(l, answer) // a long answer folds into fewer messages
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
					return ref // Telegram fetches web media itself
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
					return text // not a workspace file: keep only its caption
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

	// Files the model did not place: pictures and videos on top of the first
	// message (a slideshow when there are several), songs and documents at
	// the end of the last one.
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

// stripMarkdown removes emphasis marks (a media caption is plain text).
func stripMarkdown(s string) string {
	return strings.NewReplacer("**", "", "__", "", "~~", "", "||", "", "`", "").Replace(s)
}

// withoutMediaRefs replaces the model's references to workspace files by
// their captions (guest answers and the live draft cannot carry files).
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

// sendAnswerWithFiles sends the answer with the deliverable files inside it;
// effect (a message effect id) plays with its first message. It returns the
// files that were shown and those still to be sent separately.
// With inPlace, a first part without files replaces the group's live message
// instead of being sent as a new one. Answers carry no buttons.
func (b *Bot) sendAnswerWithFiles(ctx context.Context, m *telegram.Message, session, answer, head string, deliver []string, captions map[string]string,
	effect string, inPlace func(context.Context, string) error) (shown, rest []string) {
	embed := !b.richOff.Load() && (b.mediaStrikes.Load() < maxMediaStrikes ||
		time.Since(time.Unix(b.mediaFailedAt.Load(), 0)) > mediaRetryAfter)
	parts, rest := b.planAnswer(b.langOf(m.From), session, answer, deliver, captions, embed)
	if head != "" && len(parts) > 0 { // above the first part (after the answer was folded and split)
		parts[0].md, parts[0].plain = head+"\n\n"+parts[0].md, head+"\n\n"+parts[0].plain
	}
	// An effect Telegram does not know must not cost the message.
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
		opts := telegram.SendOptions{}.InTopic(m) // every part stays in the question's topic
		if i == 0 {
			opts.ReplyTo, opts.EffectID = m.MessageID, effect
		}
		if i > 0 {
			time.Sleep(350 * time.Millisecond) // keeps the order and reads naturally
		}
		if len(p.files) > 0 {
			var total int64
			for _, f := range p.files {
				if fi, err := b.app.Machine.Stat(session, f.name); err == nil {
					total += fi.Size()
				}
			}
			uctx, ucancel := b.uploadCtx(ctx, total) // big files get their own time
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
			// Last resort: the words without any formatting, so the answer is never lost.
			if _, err := b.api.SendHTMLParts(ctx, m.Chat.ID, telegram.EscapeHTML(p.md), telegram.SendOptions{}.InTopic(m)); err != nil {
				log.Printf("[chat %d] answer part %d is lost: %v", m.Chat.ID, i, err)
			}
		}
	}
	return shown, rest
}

// sendRichFiles uploads one message with its files.
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
