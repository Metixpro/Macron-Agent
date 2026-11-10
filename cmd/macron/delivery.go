package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Metixpro/macron-agent/pkg/machine"
	"github.com/Metixpro/macron-agent/pkg/telegram"
	"github.com/Metixpro/macron-agent/pkg/tools"
)

const maxFilesPerReply = 10

var (
	audioExt = map[string]bool{".mp3": true, ".m4a": true, ".wav": true, ".ogg": true, ".flac": true}
	imageExt = map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".webp": true}
	videoExt = map[string]bool{".mp4": true, ".mov": true, ".mkv": true, ".webm": true}
)

type fileSig struct {
	size    int64
	modNano int64
}

const (
	trackDepth    = 3
	trackMaxFiles = 5000
)

func snapshot(sb *machine.Machine, sessionID string) map[string]fileSig {
	out := map[string]fileSig{}
	files, _ := sb.TrackedFiles(sessionID, trackDepth, trackMaxFiles)
	for _, f := range files {
		out[f.Name] = fileSig{f.Size, f.ModTime.UnixNano()}
	}
	return out
}

func hiddenFile(name string) bool {
	base := path.Base(name)
	return strings.HasPrefix(base, ".") || strings.HasSuffix(base, "~")
}

var deliverableTools = map[string]bool{
	"search_image": true, "text_to_speech": true, "generate_chart": true, "generate_qr": true,
	"download_file": true, "download_media_ytdlp": true, "media_convert": true, "pdf_tool": true, "deep_research": true, "split_video": true,
	"create_motion_graphic": true,
}

type fileTracker struct {
	sb      *machine.Machine
	session string
	mu      sync.Mutex
	calls   map[string][]*trackedCall
	auto    []string
	scratch []string
}

var workspaceCalls = struct {
	sync.Mutex
	started map[string]int
	running map[string]int
}{started: map[string]int{}, running: map[string]int{}}

type trackedCall struct {
	before map[string]fileSig
	alone  bool
	seq    int
}

func newFileTracker(sb *machine.Machine, session string) *fileTracker {
	return &fileTracker{sb: sb, session: session, calls: map[string][]*trackedCall{}}
}

func (t *fileTracker) start(tool string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	workspaceCalls.Lock()
	workspaceCalls.started[t.session]++
	c := &trackedCall{alone: workspaceCalls.running[t.session] == 0, seq: workspaceCalls.started[t.session]}
	workspaceCalls.running[t.session]++
	workspaceCalls.Unlock()
	c.before = snapshot(t.sb, t.session)
	t.calls[tool] = append(t.calls[tool], c)
}

func (t *fileTracker) end(tool, result string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	list := t.calls[tool]
	if len(list) == 0 {
		return
	}
	c := list[0]
	t.calls[tool] = list[1:]
	workspaceCalls.Lock()
	alone := c.alone && workspaceCalls.started[t.session] == c.seq
	if workspaceCalls.running[t.session]--; workspaceCalls.running[t.session] <= 0 {
		delete(workspaceCalls.running, t.session)
		delete(workspaceCalls.started, t.session)
	}
	workspaceCalls.Unlock()
	after := snapshot(t.sb, t.session)
	var changed []string
	for name, sig := range after {
		if old, ok := c.before[name]; ok && old == sig {
			continue
		}
		if !hiddenFile(name) {
			changed = append(changed, name)
		}
	}
	sort.Strings(changed)
	for _, name := range changed {
		if deliverableTools[tool] && (alone || names(result, name)) {
			t.auto = appendNew(t.auto, name)
		} else {
			t.scratch = appendNew(t.scratch, name)
		}
	}
}

func (t *fileTracker) adopt(sub *fileTracker, moved map[string]string) {
	sub.mu.Lock()
	auto, scratch := append([]string(nil), sub.auto...), append([]string(nil), sub.scratch...)
	sub.mu.Unlock()
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, name := range auto {
		if to, ok := moved[name]; ok {
			t.auto = appendNew(t.auto, to)
		}
	}
	for _, name := range scratch {
		if to, ok := moved[name]; ok {
			t.scratch = appendNew(t.scratch, to)
		}
	}
}

func names(result, name string) bool {
	for i := 0; ; {
		j := strings.Index(result[i:], name)
		if j < 0 {
			return false
		}
		at := i + j
		if at == 0 || !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-./", rune(result[at-1])) {
			return true
		}
		i = at + 1
	}
}

func appendNew(list []string, name string) []string {
	for _, n := range list {
		if n == name {
			return list
		}
	}
	return append(list, name)
}

func (t *fileTracker) deliveries(explicit []string) []string {
	var out []string
	for _, n := range explicit {
		if t.exists(n) {
			out = appendNew(out, n)
		}
	}
	return out
}

func (t *fileTracker) placed(answer string) []string {
	var out []string
	inFence := false
	for _, line := range strings.Split(answer, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
		}
		if inFence || !strings.Contains(line, "![") {
			continue
		}
		for _, g := range mediaRef.FindAllStringSubmatch(line, -1) {
			if name := workspaceName(g[2]); name != "" && !isURL(name) && t.exists(name) {
				out = appendNew(out, name)
			}
		}
	}
	return out
}

func (t *fileTracker) made(unsent ...string) []string {
	t.mu.Lock()
	list := append([]string(nil), t.auto...)
	t.mu.Unlock()
	skip := map[string]bool{}
	for _, n := range unsent {
		skip[n] = true
	}
	var out []string
	for _, n := range list {
		if !skip[n] && t.exists(n) {
			out = append(out, n)
		}
	}
	return out
}

func (t *fileTracker) exists(name string) bool {
	fi, err := t.sb.Stat(t.session, name)
	return err == nil && fi.Mode().IsRegular() && fi.Size() > 0
}

func (b *Bot) deliverFiles(ctx context.Context, m *telegram.Message, sessionID string, names []string, captions map[string]string) []string {
	extra := 0
	if len(names) > maxFilesPerReply {
		extra = len(names) - maxFilesPerReply
		names = names[:maxFilesPerReply]
	}
	opts := telegram.SendOptions{}.InTopic(m)
	if !m.Chat.Private() {
		opts.ReplyTo = m.MessageID
	}
	delivered, ok := b.sendBatch(ctx, m.Chat.ID, sessionID, names, captions, opts)
	if ok && extra > 0 {
		nctx, cancel := b.ctx(30 * time.Second)
		defer cancel()
		b.sendHTML(nctx, m.Chat.ID, b.langOf(m.From).f(textMoreFiles, extra), telegram.SendOptions{ThreadID: opts.ThreadID})
	}
	return delivered
}

func uploadBudget(size int64) time.Duration {
	return min(max(3*time.Minute, 2*time.Minute+time.Duration(size/(40<<20))*time.Minute), 60*time.Minute)
}

func (b *Bot) uploadCtx(parent context.Context, size int64) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), uploadBudget(size))
	stopParent := context.AfterFunc(parent, func() {
		if errors.Is(parent.Err(), context.Canceled) {
			cancel()
		}
	})
	stopJob := context.AfterFunc(b.jobCtx, cancel)
	return ctx, func() { stopParent(); stopJob(); cancel() }
}

func cancelled(ctx context.Context) bool { return errors.Is(ctx.Err(), context.Canceled) }

func (b *Bot) sendBatch(ctx context.Context, chatID int64, sessionID string, names []string, captions map[string]string, opts telegram.SendOptions) ([]string, bool) {
	var delivered []string
	one := func(name string) bool {
		var size int64
		if fi, err := b.app.Machine.Stat(sessionID, name); err == nil {
			size = fi.Size()
		}
		uctx, cancel := b.uploadCtx(ctx, size)
		defer cancel()
		err := b.sendFile(uctx, chatID, sessionID, name, captions[name], opts)
		switch {
		case err == nil:
			delivered = append(delivered, name)
			return true
		case errors.Is(err, errNotSent):
			return true
		case telegram.IsUnreachable(err) || cancelled(ctx):
			return false
		}
		log.Printf("[chat %d] sending %q failed: %v", chatID, name, err)
		nctx, ncancel := b.ctx(30 * time.Second)
		defer ncancel()
		b.sendHTML(nctx, chatID, b.chatLang(nctx, chatID).f(textSendFailed, esc(name)), telegram.SendOptions{ThreadID: opts.ThreadID})
		return true
	}
	for _, group := range b.albumGroups(sessionID, names) {
		if cancelled(ctx) {
			return delivered, false
		}
		if len(group.names) > 1 && b.sendAlbum(ctx, chatID, sessionID, group, captions, opts) {
			delivered = append(delivered, group.names...)
			continue
		}
		for _, name := range group.names {
			if !one(name) {
				return delivered, false
			}
		}
	}
	return delivered, true
}

type album struct {
	kind  string
	names []string
	types []string
}

func (b *Bot) albumGroups(sessionID string, names []string) []album {
	var groups []album
	index := map[string]int{}
	for _, name := range names {
		var size int64
		if fi, err := b.app.Machine.Stat(sessionID, name); err == nil {
			size = fi.Size()
		}
		ext := strings.ToLower(filepath.Ext(name))
		kind, typ := "document", "document"
		switch {
		case tools.NativeKind(name) != "":
			groups = append(groups, album{kind: "native", names: []string{name}, types: []string{tools.NativeKind(name)}})
			continue
		case imageExt[ext] && size <= telegram.MaxPhotoBytes:
			kind, typ = "visual", "photo"
		case videoExt[ext] && size <= b.api.UploadLimit():
			kind, typ = "visual", "video"
		case audioExt[ext]:
			kind, typ = "audio", "audio"
		}
		i, ok := index[kind]
		if !ok || len(groups[i].names) == 10 {
			groups = append(groups, album{kind: kind})
			i = len(groups) - 1
			index[kind] = i
		}
		groups[i].names = append(groups[i].names, name)
		groups[i].types = append(groups[i].types, typ)
	}
	return groups
}

func (b *Bot) sendAlbum(ctx context.Context, chatID int64, sessionID string, g album, captions map[string]string, opts telegram.SendOptions) bool {
	var items []telegram.MediaItem
	defer func() {
		for _, it := range items {
			if c, ok := it.File.Reader.(interface{ Close() error }); ok {
				c.Close()
			}
		}
	}()
	for i, name := range g.names {
		f, info, err := b.app.Machine.Open(sessionID, name)
		if err != nil || info.Size() == 0 || info.Size() > b.api.UploadLimit() {
			if f != nil {
				f.Close()
			}
			return false
		}
		items = append(items, telegram.MediaItem{Type: g.types[i], File: telegram.File{Name: filepath.Base(name), Reader: f}, Caption: captionHTML(captions[name])})
	}
	var total int64
	for _, name := range g.names {
		if fi, err := b.app.Machine.Stat(sessionID, name); err == nil {
			total += fi.Size()
		}
	}
	ctx, cancel := b.uploadCtx(ctx, total)
	defer cancel()
	action := map[string]string{"visual": "upload_photo", "audio": "upload_voice", "document": "upload_document"}[g.kind]
	_ = b.api.SendChatAction(ctx, chatID, action)
	if err := b.api.SendMediaGroup(ctx, chatID, items, opts); err != nil {
		if !telegram.IsUnreachable(err) && ctx.Err() == nil {
			log.Printf("[chat %d] album failed (%v), sending the files one by one", chatID, err)
		}
		return false
	}
	return true
}

func audioNames(base string) (title, performer string) {
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	stem = strings.TrimSpace(strings.TrimSuffix(stem, " (compressed)"))

	if a, t, ok := strings.Cut(stem, "_-_"); ok && strings.TrimSpace(a) != "" && strings.TrimSpace(t) != "" {
		return strings.TrimSpace(strings.ReplaceAll(t, "_", " ")), strings.TrimSpace(strings.ReplaceAll(a, "_", " "))
	}
	if a, t, ok := strings.Cut(stem, " - "); ok && strings.TrimSpace(a) != "" && strings.TrimSpace(t) != "" {
		return strings.TrimSpace(t), strings.TrimSpace(a)
	}
	return stem, "Macron"
}

func captionHTML(md string) string {
	if strings.TrimSpace(md) == "" {
		return ""
	}
	return telegram.MarkdownToHTML(md)
}

var errNotSent = errors.New("not sent: too big for the chat")

func (b *Bot) sendFile(ctx context.Context, chatID int64, sessionID, name, caption string, opts telegram.SendOptions) error {
	open := func() (telegram.File, func(), int64, error) {

		f, info, err := b.app.Machine.Open(sessionID, name)
		if err != nil {
			return telegram.File{}, nil, 0, err
		}
		return telegram.File{Name: filepath.Base(name), Reader: f}, func() { f.Close() }, info.Size(), nil
	}
	_, closeFn, size, err := open()
	if err != nil {
		return err
	}
	closeFn()
	switch {
	case size == 0:
		return fmt.Errorf("file is empty")
	case size > b.api.UploadLimit():
		l := b.chatLang(ctx, chatID)
		b.sendHTML(ctx, chatID, l.f(textFileOverLimit, esc(name), size>>20, sizeWord(l, b.api.UploadLimit())), telegram.SendOptions{ThreadID: opts.ThreadID})
		return errNotSent
	}

	base := filepath.Base(name)
	ext := strings.ToLower(filepath.Ext(base))
	label := esc(base)
	html := captionHTML(caption)
	attempt := func(send func(telegram.File) error) error {
		f, closeFn, _, err := open()
		if err != nil {
			return err
		}
		defer closeFn()
		return send(f)
	}
	asDocument := func() error {
		_ = b.api.SendChatAction(ctx, chatID, "upload_document")
		docCaption := "📄 " + label
		if html != "" {
			docCaption = html
		}
		return attempt(func(f telegram.File) error { return b.api.SendDocumentOpts(ctx, chatID, f, docCaption, opts) })
	}

	switch {
	case tools.NativeKind(name) == "voice":
		_ = b.api.SendChatAction(ctx, chatID, "upload_voice")
		err = attempt(func(f telegram.File) error { return b.api.SendVoiceOpts(ctx, chatID, f, html, opts) })
	case tools.NativeKind(name) == "video_note":
		_ = b.api.SendChatAction(ctx, chatID, "upload_video_note")
		err = attempt(func(f telegram.File) error { return b.api.SendVideoNoteOpts(ctx, chatID, f, 480, opts) })
		if err == nil && html != "" {
			b.sendHTML(ctx, chatID, html, opts)
		}
	case audioExt[ext]:
		_ = b.api.SendChatAction(ctx, chatID, "upload_voice")
		err = attempt(func(f telegram.File) error {
			title, performer := audioNames(base)
			return b.api.SendAudioOpts(ctx, chatID, f, title, performer, html, opts)
		})
	case imageExt[ext] && size <= telegram.MaxPhotoBytes:
		_ = b.api.SendChatAction(ctx, chatID, "upload_photo")
		err = attempt(func(f telegram.File) error { return b.api.SendPhotoOpts(ctx, chatID, f, html, opts) })
	case videoExt[ext]:
		_ = b.api.SendChatAction(ctx, chatID, "upload_video")
		err = attempt(func(f telegram.File) error { return b.api.SendVideoOpts(ctx, chatID, f, html, opts) })
	default:
		return asDocument()
	}
	if err != nil && ctx.Err() == nil && !telegram.IsUnreachable(err) {
		log.Printf("[chat %d] native send of %q failed (%v), sending as a document", chatID, name, err)
		return asDocument()
	}
	return err
}
