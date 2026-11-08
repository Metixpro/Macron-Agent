package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/Metixpro/macron-agent/pkg/machine"
)

// Speech to text: transcribe_audio, and the voice messages the bot hears.
// Any OpenAI-compatible /audio/transcriptions service works (OpenAI's
// Whisper, Groq, Iranian relays, the local whisper.cpp of the installer),
// and so does a Hugging Face model's inference address; the bot gives the
// services in the order they are tried.
//
// A recording is written down once: transcripts are kept by the file's
// content (and language), and a failure is remembered for a few minutes, so
// a slow or broken service is never asked again and again for the same
// voice. Each service takes one recording at a time; a service that fails
// is rested (briefly after a timeout or a 5xx, for hours when it has no
// speech to text at all).

const ToolTranscribe = "transcribe_audio"

// STTService is one speech-to-text endpoint.
type STTService struct {
	Name  string // shown in errors ("" = the host)
	URL   string // the OpenAI-compatible base, e.g. https://api.openai.com/v1
	Key   string
	Model string
	// Auto marks a service found among the chat providers: one without
	// speech to text is as if it were not there.
	Auto bool
}

func (s STTService) key() string { return s.URL + "\x00" + s.Model }

const (
	sttMaxBytes       = 24 << 20 // the services take 25 MB
	sttDefaultTimeout = 5 * time.Minute
	sttBadFor         = 6 * time.Hour // a service without speech to text is not asked again for a while
	sttRestMin        = time.Minute   // after a timeout, a 5xx or a broken connection, doubling…
	sttRestMax        = 30 * time.Minute
	sttFailureTTL     = 10 * time.Minute // a recording that failed is not tried again before this
	sttMaxLength      = 100 * 60         // seconds kept of a longer recording (32 kb/s fits 24 MB)
	sttCacheSize      = 256
)

// sttFormats are the files the services take as they are.
var sttFormats = map[string]bool{".mp3": true, ".mp4": true, ".mpeg": true, ".mpga": true, ".m4a": true, ".wav": true,
	".webm": true, ".ogg": true, ".oga": true, ".flac": true}

type sttHealth struct {
	until       time.Time
	unsupported bool // no speech to text here (else a rest after a failure)
	fails       int
	last        string // the last error
}

type sttCached struct {
	text, err string
	at        time.Time
}

type sttState struct {
	mu       sync.Mutex
	health   map[string]*sttHealth
	slots    map[string]chan struct{} // recordings at a time per service (sttSlots)
	cache    map[string]sttCached     // by content and language
	order    []string                 // cache keys, oldest first
	inflight map[string]chan struct{} // recordings being written down, closed when done
}

// sttSlots is how many recordings a service writes down at once: a whisper
// on this server or the local network, one (it takes every core; the rest
// wait their turn instead of all slowing down to a timeout); a cloud
// service, a few.
func sttSlots(s STTService) int {
	u, err := url.Parse(s.URL)
	if err != nil {
		return 1
	}
	host := u.Hostname()
	if ip := net.ParseIP(host); host == "localhost" || ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast()) {
		return 1
	}
	return 4
}

// join makes one recording be written down once: the first caller does it
// (lead), the others wait for it to be done and take its result.
func (s *sttState) join(key string) (wait <-chan struct{}, done func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.inflight == nil {
		s.inflight = map[string]chan struct{}{}
	}
	if ch, ok := s.inflight[key]; ok {
		return ch, nil
	}
	ch := make(chan struct{})
	s.inflight[key] = ch
	return nil, func() {
		s.mu.Lock()
		delete(s.inflight, key)
		s.mu.Unlock()
		close(ch)
	}
}

func (s *sttState) status(key string, now time.Time) (resting, unsupported bool, last string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h := s.health[key]
	if h == nil || !now.Before(h.until) {
		return false, false, ""
	}
	return true, h.unsupported, h.last
}

// failed rests a service: for sttBadFor when it has no speech to text, else
// for a time that doubles with each failure in a row.
func (s *sttState) failed(key string, unsupported bool, err error, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.health == nil {
		s.health = map[string]*sttHealth{}
	}
	h := s.health[key]
	if h == nil {
		h = &sttHealth{}
		s.health[key] = h
	}
	h.fails++
	h.unsupported = unsupported
	h.last = tail(err.Error(), 160)
	if unsupported {
		h.until = now.Add(sttBadFor)
		return
	}
	rest := sttRestMin << min(h.fails-1, 5)
	h.until = now.Add(min(rest, sttRestMax))
}

func (s *sttState) ok(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.health, key)
}

// slot waits for the service's turn; while it waits, the live view says so.
func (s *sttState) slot(ctx context.Context, svc STTService) (func(), error) {
	s.mu.Lock()
	if s.slots == nil {
		s.slots = map[string]chan struct{}{}
	}
	ch := s.slots[svc.key()]
	if ch == nil {
		ch = make(chan struct{}, sttSlots(svc))
		s.slots[svc.key()] = ch
	}
	s.mu.Unlock()
	select {
	case ch <- struct{}{}:
		return func() { <-ch }, nil
	default:
	}
	ReportStatus(ctx, StatusQueued)
	select {
	case ch <- struct{}{}:
		ReportStatus(ctx, StatusListening)
		return func() { <-ch }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (s *sttState) lookup(key string, now time.Time) (sttCached, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.cache[key]
	if !ok || (c.err != "" && now.Sub(c.at) > sttFailureTTL) {
		return sttCached{}, false
	}
	return c, true
}

func (s *sttState) remember(key string, c sttCached) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cache == nil {
		s.cache = map[string]sttCached{}
	}
	if _, ok := s.cache[key]; !ok {
		s.order = append(s.order, key)
	}
	s.cache[key] = c
	for len(s.order) > sttCacheSize {
		delete(s.cache, s.order[0])
		s.order = s.order[1:]
	}
}

// SetTranscriber gives the speech-to-text services, in the order they are
// tried (nil or an empty list: none).
func (r *Registry) SetTranscriber(f func() []STTService) { r.stt = f }

// SetSTTTimeout bounds one recording's transcription (0 = 5 minutes); a
// local whisper on a small server needs more than a cloud service.
func (r *Registry) SetSTTTimeout(d time.Duration) { r.sttTimeout = d }

func (r *Registry) sttLimit() time.Duration {
	if r.sttTimeout > 0 {
		return r.sttTimeout
	}
	return sttDefaultTimeout
}

// STTServices lists the speech-to-text services in the order they are tried.
func (r *Registry) STTServices() []STTService {
	if r.stt == nil {
		return nil
	}
	return r.stt()
}

// CanTranscribe reports whether some speech-to-text service is set and not
// known to lack speech to text (a service resting after a failure counts:
// it is told as a failure, not as missing).
func (r *Registry) CanTranscribe() bool {
	now := time.Now()
	for _, s := range r.STTServices() {
		if _, unsupported, _ := r.sttState.status(s.key(), now); !unsupported {
			return true
		}
	}
	return false
}

// CheckSTT sends a second of silence to a service: nil means it takes
// speech to text with its key and model (and it is tried again at once).
func (r *Registry) CheckSTT(ctx context.Context, s STTService) error {
	wav := silentWAV(time.Second)
	_, err := r.transcribeWith(ctx, s, "silence.wav", func() (io.ReadCloser, int64, error) {
		return io.NopCloser(bytes.NewReader(wav)), int64(len(wav)), nil
	}, "")
	if err == nil {
		r.sttState.ok(s.key())
	}
	return err
}

// silentWAV is d of 16 kHz mono silence.
func silentWAV(d time.Duration) []byte {
	const rate = 16000
	n := int(d.Seconds() * rate * 2)
	var b bytes.Buffer
	le := func(v uint32, size int) {
		for i := 0; i < size; i++ {
			b.WriteByte(byte(v >> (8 * i)))
		}
	}
	b.WriteString("RIFF")
	le(uint32(36+n), 4)
	b.WriteString("WAVEfmt ")
	le(16, 4)
	le(1, 2) // PCM
	le(1, 2) // mono
	le(rate, 4)
	le(rate*2, 4)
	le(2, 2)
	le(16, 2)
	b.WriteString("data")
	le(uint32(n), 4)
	b.Write(make([]byte, n))
	return b.Bytes()
}

// ErrNoTranscriber means no speech-to-text service is set.
var ErrNoTranscriber = errors.New("speech to text is not set up")

func (r *Registry) registerTranscribe() {
	r.add(ToolTranscribe,
		"Speech to text of a workspace audio or video file (voice messages come transcribed already).",
		map[string]any{
			"filename": prop("string", ""),
			"language": prop("string", "ISO-639-1 (fa, en…) when known"),
		},
		[]string{"filename"}, r.transcribeTool)
}

func (r *Registry) transcribeTool(ctx context.Context, sessionID string, args map[string]any) (string, error) {
	name, _ := args["filename"].(string)
	lang, _ := args["language"].(string)
	ReportStatus(ctx, StatusListening)
	text, note, err := r.Transcribe(ctx, sessionID, name, lang)
	switch {
	case errors.Is(err, ErrNoTranscriber):
		return "error: speech to text is not set up on this bot. The bot's admin can turn it on in the admin panel (Settings → Speech to text). " +
			"Tell the user simply.", nil
	case err != nil:
		return "error: " + err.Error() + ". Do not call " + ToolTranscribe + " again for this file now; tell the user simply.", nil
	case strings.TrimSpace(text) == "":
		return "The recording has no speech that could be written down (silence, music or noise)." + note, nil
	}
	return fmt.Sprintf("Transcript of %s (speech to text; small mistakes are possible)%s:\n%s", name, note, text), nil
}

// The live status while a recording is written down, and while it waits for
// its turn (a local whisper writes down one at a time).
const (
	StatusListening = "🎧 listening"
	StatusQueued    = "⏳ in line"
)

// Transcribe writes down the speech of a workspace file. note tells what
// was left out of a long recording.
func (r *Registry) Transcribe(ctx context.Context, sessionID, name, lang string) (text, note string, err error) {
	services := r.STTServices()
	if len(services) == 0 {
		return "", "", ErrNoTranscriber
	}
	info, err := r.Machine.Stat(sessionID, name)
	if err != nil || info.IsDir() {
		return "", "", fmt.Errorf("file %q not found in the workspace", name)
	}
	lang = normalizeLang(lang)
	key, err := r.fileKey(sessionID, name, lang)
	if err != nil {
		return "", "", err
	}
	cached := func() (string, bool, error) {
		c, ok := r.sttState.lookup(key, time.Now())
		if !ok {
			return "", false, nil
		}
		if c.err != "" {
			return "", true, errors.New(c.err + " (it failed a moment ago)")
		}
		return c.text, true, nil
	}
	if text, ok, err := cached(); ok {
		return text, "", err
	}
	// The same recording twice at once (sent again, or replied to while it
	// is being written down): the second waits for the first.
	for {
		wait, done := r.sttState.join(key)
		if done != nil {
			defer done()
			break
		}
		ReportStatus(ctx, StatusQueued)
		select {
		case <-wait:
		case <-ctx.Done():
			return "", "", ctx.Err()
		}
		if text, ok, err := cached(); ok {
			return text, "", err
		}
	}
	text, note, err = r.transcribe(ctx, sessionID, name, info.Size(), lang, services)
	switch {
	case err == nil:
		r.sttState.remember(key, sttCached{text: text, at: time.Now()})
	case !errors.Is(err, ErrNoTranscriber) && ctx.Err() == nil:
		r.sttState.remember(key, sttCached{err: err.Error(), at: time.Now()})
	}
	return text, note, err
}

// fileKey names a recording by its content, so a file sent again (or a
// reply to an old voice message) is not written down twice.
func (r *Registry) fileKey(sessionID, name, lang string) (string, error) {
	f, _, err := r.Machine.Open(sessionID, name)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)) + ":" + lang, nil
}

func (r *Registry) transcribe(ctx context.Context, sessionID, name string, size int64, lang string, services []STTService) (string, string, error) {
	upload, note := name, ""
	if !sttFormats[strings.ToLower(path.Ext(name))] || size > sttMaxBytes {
		converted, long, err := r.sttAudio(ctx, sessionID, name)
		if err != nil {
			return "", "", err
		}
		defer func() { _ = r.Machine.Remove(sessionID, converted) }()
		upload = converted
		if long {
			note = fmt.Sprintf(" (only the first %d minutes)", sttMaxLength/60)
		}
	}
	if info, err := r.Machine.Stat(sessionID, upload); err != nil {
		return "", "", err
	} else if info.Size() > sttMaxBytes {
		return "", "", errors.New("the recording is too large for speech to text even as compressed audio")
	}
	open := func() (io.ReadCloser, int64, error) {
		f, info, err := r.Machine.Open(sessionID, upload)
		if err != nil {
			return nil, 0, err
		}
		return f, info.Size(), nil
	}
	var errs []string
	tried := false
	for _, s := range services {
		if resting, unsupported, last := r.sttState.status(s.key(), time.Now()); resting {
			if !unsupported || !s.Auto {
				errs = append(errs, s.label()+": resting after an error ("+last+")")
			}
			continue
		}
		tried = true
		release, err := r.sttState.slot(ctx, s)
		if err != nil {
			return "", "", err
		}
		text, err := r.transcribeWith(ctx, s, path.Base(upload), open, lang)
		release()
		if err == nil {
			r.sttState.ok(s.key())
			return text, note, nil
		}
		if ctx.Err() != nil {
			return "", "", ctx.Err()
		}
		var hs *sttHTTPError
		unsupported := errors.As(err, &hs) && hs.unsupported()
		r.sttState.failed(s.key(), unsupported, err, time.Now())
		if unsupported && s.Auto {
			continue // a chat provider without speech to text: as if it were not there
		}
		errs = append(errs, s.label()+": "+err.Error())
	}
	if len(errs) == 0 {
		return "", "", ErrNoTranscriber // only providers without speech to text
	}
	if !tried {
		return "", "", errors.New("speech to text is unavailable for a few minutes: " + strings.Join(errs, "; "))
	}
	return "", "", errors.New("speech to text failed: " + strings.Join(errs, "; "))
}

// sttAudio turns a recording into compact mono MP3 (an unsupported format,
// a video, or a file over the size limit).
func (r *Registry) sttAudio(ctx context.Context, sessionID, name string) (string, bool, error) {
	if d := r.downloaderInfo(); d == nil || !d.FFmpeg {
		return "", false, errors.New("this format needs ffmpeg to be converted, and ffmpeg is not installed")
	}
	out := ".stt_" + strings.TrimSuffix(path.Base(name), path.Ext(name)) + ".mp3"
	long := r.mediaLength(ctx, sessionID, name) > sttMaxLength
	res := r.Machine.Run(ctx, sessionID, machine.RunOptions{Timeout: 5 * time.Minute, Args: []string{
		"ffmpeg", "-y", "-v", "error", "-i", "./" + name, "-vn", "-ac", "1", "-ar", "16000", "-c:a", "libmp3lame", "-b:a", "32k",
		"-t", fmt.Sprint(sttMaxLength), "./" + out}})
	if res.ExitCode != 0 {
		_ = r.Machine.Remove(sessionID, out)
		return "", false, fmt.Errorf("could not read the audio: %s", tail(lastLine(res.Stderr+"\n"+res.Error), 200))
	}
	return out, long, nil
}

type sttHTTPError struct {
	status int
	body   string
}

func (e *sttHTTPError) Error() string { return fmt.Sprintf("HTTP %d: %s", e.status, e.body) }

// unsupported: the service has no speech to text (or not with this model or key).
func (e *sttHTTPError) unsupported() bool {
	switch e.status {
	case http.StatusBadRequest:
		return containsAny(e.body, "model", "not found", "not support", "unsupported", "invalid")
	case http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound, http.StatusMethodNotAllowed, http.StatusNotImplemented:
		return true
	}
	return false
}

func (s STTService) label() string {
	if s.Name != "" {
		return s.Name
	}
	if i := strings.Index(s.URL, "://"); i >= 0 {
		host, _, _ := strings.Cut(s.URL[i+3:], "/")
		return host
	}
	return s.URL
}

// audioBody opens the recording again for each request (a retry included).
type audioBody func() (io.ReadCloser, int64, error)

func (r *Registry) transcribeWith(ctx context.Context, s STTService, filename string, audio audioBody, lang string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, r.sttLimit())
	defer cancel()
	if hfInference(s.URL) {
		return r.transcribeHF(ctx, s, filename, audio)
	}
	model := s.Model
	if model == "" {
		model = DefaultSTTModel(s.URL)
	}
	fields := [][2]string{{"model", model}, {"response_format", "json"}}
	if lang != "" {
		fields = append(fields, [2]string{"language", lang})
	}
	body, size, ctype, err := multipartAudio(audio, filename, fields)
	if err != nil {
		return "", err
	}
	defer body.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(s.URL, "/")+"/audio/transcriptions", body)
	if err != nil {
		return "", err
	}
	req.ContentLength = size
	req.Header.Set("Content-Type", ctype)
	return r.sttSend(req, s.Key)
}

// multipartAudio streams a multipart form whose last part is the recording,
// with its exact length (some services refuse a chunked upload).
func multipartAudio(audio audioBody, filename string, fields [][2]string) (io.ReadCloser, int64, string, error) {
	var head bytes.Buffer
	w := multipart.NewWriter(&head)
	for _, f := range fields {
		if err := w.WriteField(f[0], f[1]); err != nil {
			return nil, 0, "", err
		}
	}
	if _, err := w.CreateFormFile("file", filename); err != nil {
		return nil, 0, "", err
	}
	headBytes := bytes.Clone(head.Bytes())
	head.Reset()
	if err := w.Close(); err != nil {
		return nil, 0, "", err
	}
	tailBytes := bytes.Clone(head.Bytes())
	f, size, err := audio()
	if err != nil {
		return nil, 0, "", err
	}
	body := struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(headBytes), io.LimitReader(f, size), bytes.NewReader(tailBytes)), f}
	return body, int64(len(headBytes)) + size + int64(len(tailBytes)), w.FormDataContentType(), nil
}

// hfInference reports a Hugging Face inference address (a model's URL that
// takes the audio itself as the body), not an OpenAI-compatible one.
func hfInference(rawURL string) bool {
	u := strings.ToLower(rawURL)
	return (strings.Contains(u, "huggingface.co/") && strings.Contains(u, "/models/")) || strings.Contains(u, ".endpoints.huggingface.cloud")
}

// transcribeHF posts the audio to a Hugging Face model; while the model is
// still loading (HTTP 503) it is asked once more.
func (r *Registry) transcribeHF(ctx context.Context, s STTService, filename string, audio audioBody) (string, error) {
	types := map[string]string{".wav": "audio/wav", ".mp3": "audio/mpeg", ".mpeg": "audio/mpeg", ".mpga": "audio/mpeg", ".ogg": "audio/ogg",
		".oga": "audio/ogg", ".flac": "audio/flac", ".webm": "audio/webm", ".m4a": "audio/mp4", ".mp4": "audio/mp4"}
	ctype := types[strings.ToLower(path.Ext(filename))]
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	for try := 0; ; try++ {
		f, size, err := audio()
		if err != nil {
			return "", err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.URL, io.LimitReader(f, size))
		if err != nil {
			f.Close()
			return "", err
		}
		req.ContentLength = size
		req.Header.Set("Content-Type", ctype)
		text, err := r.sttSend(req, s.Key)
		f.Close()
		var hs *sttHTTPError
		if try == 0 && errors.As(err, &hs) && hs.status == http.StatusServiceUnavailable {
			select {
			case <-time.After(10 * time.Second):
				continue
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		return text, err
	}
}

// sttSend sends a speech-to-text request and reads its {"text": …}.
func (r *Registry) sttSend(req *http.Request, key string) (string, error) {
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	resp, err := r.sttHTTP().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", &sttHTTPError{status: resp.StatusCode, body: tail(strings.TrimSpace(string(raw)), 200)}
	}
	var out struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		if t := strings.TrimSpace(string(raw)); strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[") {
			return "", fmt.Errorf("unexpected answer: %s", tail(t, 200))
		}
		return strings.TrimSpace(string(raw)), nil // a service that answers in plain text
	}
	return strings.TrimSpace(out.Text), nil
}

// sttHTTP reaches the speech-to-text services: set by the bot's admin, so a
// self-hosted one on the server itself is fine. Connections time out; the
// answer may take minutes (a local whisper), bounded by the request's time.
func (r *Registry) sttHTTP() *http.Client {
	if r.sttClient != nil {
		return r.sttClient
	}
	return defaultSTTClient
}

var defaultSTTClient = &http.Client{Transport: &http.Transport{
	Proxy:               http.ProxyFromEnvironment,
	DialContext:         (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
	TLSHandshakeTimeout: 15 * time.Second,
	IdleConnTimeout:     90 * time.Second,
	MaxIdleConnsPerHost: 2,
}}

// DefaultSTTModel is the model a service is asked for when none is set.
func DefaultSTTModel(baseURL string) string {
	if hfInference(baseURL) { // the model is in the address
		if _, m, ok := strings.Cut(baseURL, "/models/"); ok {
			return strings.Trim(m, "/")
		}
		return "huggingface"
	}
	if strings.Contains(strings.ToLower(baseURL), "groq.com") {
		return "whisper-large-v3-turbo"
	}
	return "whisper-1"
}

// normalizeLang keeps an ISO-639-1 code (the services refuse anything else).
func normalizeLang(lang string) string {
	lang = strings.ToLower(strings.TrimSpace(lang))
	lang, _, _ = strings.Cut(strings.ReplaceAll(lang, "_", "-"), "-") // fa-IR
	switch lang {
	case "persian", "farsi", "فارسی":
		return "fa"
	case "english":
		return "en"
	case "arabic":
		return "ar"
	}
	if len(lang) == 2 && lang[0] >= 'a' && lang[0] <= 'z' && lang[1] >= 'a' && lang[1] <= 'z' {
		return lang
	}
	return ""
}
