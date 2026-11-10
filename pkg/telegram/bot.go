package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Metixpro/macron-agent/pkg/textutil"
)

const (
	MaxMessageChars = 4096

	MaxUploadBytes = 50 << 20

	MaxPhotoBytes = 10 << 20

	defaultBase = "https://api.telegram.org"
)

type Bot struct {
	Token        string
	APIURL       string
	fileBase     string
	HTTPClient   *http.Client
	UploadClient *http.Client

	local     bool
	localWarn sync.Once

	OnSent func(ctx context.Context, chatID, messageID int64)
}

type APIError struct {
	Code        int
	Description string
	RetryAfter  int
}

func (e *APIError) Error() string {
	return fmt.Sprintf("telegram api error %d: %s", e.Code, e.Description)
}

func IsParseError(err error) bool {
	var api *APIError
	return errors.As(err, &api) && api.Code == http.StatusBadRequest && strings.Contains(api.Description, "can't parse entities")
}

func IsNotModified(err error) bool {
	var api *APIError
	return errors.As(err, &api) && strings.Contains(api.Description, "message is not modified")
}

func IsUnreachable(err error) bool {
	var api *APIError
	return errors.As(err, &api) && (api.Code == http.StatusForbidden ||
		strings.Contains(api.Description, "chat not found") || strings.Contains(api.Description, "message to edit not found"))
}

func NewBot(token string) *Bot { return NewBotWithBase(token, defaultBase) }

func NewBotWithBase(token, base string) *Bot {
	base = strings.TrimRight(base, "/")
	return &Bot{
		Token:      token,
		APIURL:     base + "/bot" + token,
		fileBase:   base,
		HTTPClient: &http.Client{Timeout: 60 * time.Second},

		UploadClient: &http.Client{Transport: &http.Transport{
			Proxy:                 http.ProxyFromEnvironment,
			DialContext:           (&net.Dialer{Timeout: 20 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout:   20 * time.Second,
			ResponseHeaderTimeout: 2 * time.Minute,
			IdleConnTimeout:       90 * time.Second,
		}},
	}
}

func (b *Bot) scrub(err error) error {
	if err == nil {
		return nil
	}
	var api *APIError
	if errors.As(err, &api) {
		return err
	}
	if msg := err.Error(); strings.Contains(msg, b.Token) {
		return &scrubbedError{msg: strings.ReplaceAll(msg, b.Token, "<token>"), err: err}
	}
	return err
}

type scrubbedError struct {
	msg string
	err error
}

func (e *scrubbedError) Error() string { return e.msg }
func (e *scrubbedError) Unwrap() error { return e.err }

type apiResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	ErrorCode   int             `json:"error_code"`
	Description string          `json:"description"`
	Parameters  struct {
		RetryAfter int `json:"retry_after"`
	} `json:"parameters"`
}

func (b *Bot) decode(resp *http.Response, out any) error {
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	var ar apiResponse
	if err := json.Unmarshal(body, &ar); err != nil {
		return fmt.Errorf("telegram: unexpected response (status %d): %s", resp.StatusCode, textutil.TruncateRunes(string(body), 200, "…"))
	}
	if !ar.OK {
		code := ar.ErrorCode
		if code == 0 {
			code = resp.StatusCode
		}
		return &APIError{Code: code, Description: ar.Description, RetryAfter: ar.Parameters.RetryAfter}
	}
	if b.OnSent != nil && len(ar.Result) > 0 && (ar.Result[0] == '{' || ar.Result[0] == '[') && bytes.Contains(ar.Result, []byte(`"message_id"`)) {
		type sentMsg struct {
			MessageID int64 `json:"message_id"`
			Chat      *Chat `json:"chat"`
		}
		var sent []sentMsg
		if ar.Result[0] == '{' {
			var one sentMsg
			if json.Unmarshal(ar.Result, &one) == nil {
				sent = append(sent, one)
			}
		} else {
			_ = json.Unmarshal(ar.Result, &sent)
		}
		ctx := context.Background()
		if resp.Request != nil {
			ctx = resp.Request.Context()
		}
		for _, s := range sent {
			if s.MessageID != 0 && s.Chat != nil {
				b.OnSent(ctx, s.Chat.ID, s.MessageID)
			}
		}
	}
	if out != nil && len(ar.Result) > 0 {
		return json.Unmarshal(ar.Result, out)
	}
	return nil
}

func (b *Bot) call(ctx context.Context, method string, payload, out any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.APIURL+"/"+method, bytes.NewReader(data))
		if err != nil {
			return b.scrub(err)
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := b.HTTPClient.Do(req)
		if err != nil {
			return b.scrub(err)
		}
		err = b.decode(resp, out)
		resp.Body.Close()

		var api *APIError
		if errors.As(err, &api) && api.Code == http.StatusTooManyRequests && attempt == 0 && api.RetryAfter > 0 && api.RetryAfter <= 30 {
			select {
			case <-time.After(time.Duration(api.RetryAfter) * time.Second):
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return b.scrub(err)
	}
}

func (b *Bot) GetMe(ctx context.Context) (*User, error) {
	var u User
	if err := b.call(ctx, "getMe", map[string]any{}, &u); err != nil {
		return nil, err
	}
	return &u, nil
}

func (b *Bot) GetUpdates(ctx context.Context, offset int64, timeout int) ([]Update, error) {
	var raw []json.RawMessage
	err := b.call(ctx, "getUpdates", map[string]any{
		"offset": offset, "timeout": timeout, "allowed_updates": AllowedUpdates,
	}, &raw)
	if err != nil {
		return nil, err
	}
	return decodeUpdates(raw), nil
}

var AllowedUpdates = []string{"message", "edited_message", "callback_query", "my_chat_member", "stopped_message_generation", "poll_answer",
	"guest_message", "pre_checkout_query", "message_reaction", "widget_query", "shared_widget_query"}

func decodeUpdates(raw []json.RawMessage) []Update {
	updates := make([]Update, 0, len(raw))
	for _, r := range raw {
		var u Update
		if err := json.Unmarshal(r, &u); err != nil {
			var id struct {
				UpdateID int64 `json:"update_id"`
			}
			if json.Unmarshal(r, &id) != nil {
				continue
			}

			fixed, dropped, lerr := lenientUpdate(r)
			if lerr != nil {
				log.Printf("telegram: skipping update %d: %v", id.UpdateID, err)
				fixed = Update{UpdateID: id.UpdateID}
			} else {
				log.Printf("telegram: update %d read without %v (%v)", id.UpdateID, dropped, err)
			}
			u = fixed
		}
		updates = append(updates, u)
	}
	return updates
}

func lenientUpdate(r json.RawMessage) (Update, []string, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(r, &top); err != nil {
		return Update{}, nil, err
	}
	var dropped []string
	for _, k := range []string{"message", "edited_message", "guest_message"} {
		if v, ok := top[k]; ok {
			if clean, ok := cleanMessage(v, k, &dropped); ok {
				top[k] = clean
			} else {
				delete(top, k)
			}
		}
	}
	if v, ok := top["callback_query"]; ok {
		var cq map[string]json.RawMessage
		if json.Unmarshal(v, &cq) == nil {
			if m, ok := cq["message"]; ok {
				if clean, ok := cleanMessage(m, "callback_query.message", &dropped); ok {
					cq["message"] = clean
				} else {
					delete(cq, "message")
				}
			}
			top["callback_query"], _ = json.Marshal(cq)
		}
	}
	for k, v := range top {
		var probe Update
		one, _ := json.Marshal(map[string]json.RawMessage{k: v})
		if json.Unmarshal(one, &probe) != nil {
			delete(top, k)
			dropped = append(dropped, k)
		}
	}
	fixed, _ := json.Marshal(top)
	var u Update
	err := json.Unmarshal(fixed, &u)
	return u, dropped, err
}

func cleanMessage(raw json.RawMessage, path string, dropped *[]string) (json.RawMessage, bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		*dropped = append(*dropped, path)
		return nil, false
	}
	for k, v := range fields {
		if k == "reply_to_message" || k == "pinned_message" {
			if clean, ok := cleanMessage(v, path+"."+k, dropped); ok {
				fields[k] = clean
			} else {
				delete(fields, k)
			}
			continue
		}
		var probe Message
		one, _ := json.Marshal(map[string]json.RawMessage{k: v})
		if json.Unmarshal(one, &probe) != nil {
			if k == "message_id" || k == "chat" {
				*dropped = append(*dropped, path)
				return nil, false
			}
			delete(fields, k)
			*dropped = append(*dropped, path+"."+k)
		}
	}
	out, _ := json.Marshal(fields)
	return out, true
}

func (b *Bot) DeleteWebhook(ctx context.Context) error {
	return b.call(ctx, "deleteWebhook", map[string]any{"drop_pending_updates": false}, nil)
}

func (b *Bot) SetMyCommands(ctx context.Context, cmds []BotCommand) error {
	return b.SetMyCommandsScoped(ctx, cmds, nil)
}

func (b *Bot) SetMyCommandsScoped(ctx context.Context, cmds []BotCommand, scope *BotCommandScope) error {
	return b.SetMyCommandsFor(ctx, cmds, scope, "")
}

func (b *Bot) SetMyCommandsFor(ctx context.Context, cmds []BotCommand, scope *BotCommandScope, languageCode string) error {
	p := map[string]any{"commands": cmds}
	if scope != nil {
		p["scope"] = scope
	}
	if languageCode != "" {
		p["language_code"] = languageCode
	}
	return b.call(ctx, "setMyCommands", p, nil)
}

func (b *Bot) send(ctx context.Context, chatID int64, text, parseMode string, opts SendOptions) (*Message, error) {
	payload := map[string]any{"chat_id": chatID, "text": text, "link_preview_options": map[string]any{"is_disabled": true}}
	if parseMode != "" {
		payload["parse_mode"] = parseMode
	}
	opts.apply(payload)
	var m Message
	if err := b.call(ctx, "sendMessage", payload, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func (b *Bot) SendHTML(ctx context.Context, chatID int64, html string, opts SendOptions) (*Message, error) {
	m, err := b.send(ctx, chatID, html, "HTML", opts)
	if IsParseError(err) {
		return b.send(ctx, chatID, textutil.StripHTML(html), "", opts)
	}
	return m, err
}

func (b *Bot) SendMessage(ctx context.Context, chatID int64, html string, kb *InlineKeyboardMarkup) (*Message, error) {
	return b.SendHTML(ctx, chatID, html, SendOptions{Markup: kb})
}

func (b *Bot) SendText(ctx context.Context, chatID int64, text string) (*Message, error) {
	return b.send(ctx, chatID, text, "", SendOptions{})
}

func (b *Bot) SendHTMLChunks(ctx context.Context, chatID int64, html string, kb *InlineKeyboardMarkup) error {
	_, err := b.SendHTMLParts(ctx, chatID, html, SendOptions{Markup: kb})
	return err
}

func (b *Bot) SendHTMLParts(ctx context.Context, chatID int64, html string, opts SendOptions) ([]*Message, error) {
	if !ValidHTML(html) {
		html = EscapeHTML(textutil.StripHTML(html))
	}
	chunks := SplitHTML(html, 3800)
	var sent []*Message
	for i, ch := range chunks {
		o := opts
		if i > 0 {
			o.ReplyTo = 0
		}
		if i != len(chunks)-1 {
			o.Markup = nil
		}
		m, err := b.SendHTML(ctx, chatID, ch, o)
		if err != nil {
			return sent, err
		}
		sent = append(sent, m)
	}
	return sent, nil
}

func (b *Bot) EditMessageText(ctx context.Context, chatID, messageID int64, text string, kb *InlineKeyboardMarkup) error {
	edit := func(t, mode string) error {
		payload := map[string]any{"chat_id": chatID, "message_id": messageID, "text": t,
			"link_preview_options": map[string]any{"is_disabled": true}}
		if mode != "" {
			payload["parse_mode"] = mode
		}
		if kb != nil {
			payload["reply_markup"] = kb
		}
		return b.call(ctx, "editMessageText", payload, nil)
	}
	err := edit(text, "HTML")
	if IsParseError(err) {
		err = edit(textutil.StripHTML(text), "")
	}
	if IsNotModified(err) {
		return nil
	}
	return err
}

func (b *Bot) SendChatAction(ctx context.Context, chatID int64, action string) error {
	return b.call(ctx, "sendChatAction", map[string]any{"chat_id": chatID, "action": action}, nil)
}

func (b *Bot) AnswerCallbackQuery(ctx context.Context, callbackQueryID, text string, showAlert bool) error {
	return b.call(ctx, "answerCallbackQuery", map[string]any{
		"callback_query_id": callbackQueryID, "text": text, "show_alert": showAlert,
	}, nil)
}

func (b *Bot) DeleteMessage(ctx context.Context, chatID, messageID int64) error {
	return b.call(ctx, "deleteMessage", map[string]any{"chat_id": chatID, "message_id": messageID}, nil)
}

type File struct {
	Name   string
	Reader io.Reader
}

func (b *Bot) SendAudio(ctx context.Context, chatID int64, f File, title, performer, caption string) error {
	return b.SendAudioOpts(ctx, chatID, f, title, performer, caption, SendOptions{})
}

func (b *Bot) SendAudioOpts(ctx context.Context, chatID int64, f File, title, performer, caption string, opts SendOptions) error {
	fields := map[string]string{}
	if title != "" {
		fields["title"] = title
	}
	if performer != "" {
		fields["performer"] = performer
	}
	return b.upload(ctx, "sendAudio", "audio", chatID, f, caption, fields, opts)
}

func (b *Bot) SendPhoto(ctx context.Context, chatID int64, f File, caption string) error {
	return b.SendPhotoOpts(ctx, chatID, f, caption, SendOptions{})
}

func (b *Bot) SendPhotoOpts(ctx context.Context, chatID int64, f File, caption string, opts SendOptions) error {
	return b.upload(ctx, "sendPhoto", "photo", chatID, f, caption, nil, opts)
}

func (b *Bot) SendVideo(ctx context.Context, chatID int64, f File, caption string) error {
	return b.SendVideoOpts(ctx, chatID, f, caption, SendOptions{})
}

func (b *Bot) SendVideoOpts(ctx context.Context, chatID int64, f File, caption string, opts SendOptions) error {
	return b.upload(ctx, "sendVideo", "video", chatID, f, caption, map[string]string{"supports_streaming": "true"}, opts)
}

func (b *Bot) SendVoiceOpts(ctx context.Context, chatID int64, f File, caption string, opts SendOptions) error {
	return b.upload(ctx, "sendVoice", "voice", chatID, f, caption, nil, opts)
}

func (b *Bot) SendVideoNoteOpts(ctx context.Context, chatID int64, f File, length int, opts SendOptions) error {
	extra := map[string]string{}
	if length > 0 {
		extra["length"] = fmt.Sprint(length)
	}
	return b.upload(ctx, "sendVideoNote", "video_note", chatID, f, "", extra, opts)
}

func (b *Bot) SendDocument(ctx context.Context, chatID int64, f File, caption string) error {
	return b.SendDocumentOpts(ctx, chatID, f, caption, SendOptions{})
}

func (b *Bot) SendDocumentOpts(ctx context.Context, chatID int64, f File, caption string, opts SendOptions) error {
	return b.upload(ctx, "sendDocument", "document", chatID, f, caption, nil, opts)
}

var quoteEscaper = strings.NewReplacer("\\", "\\\\", `"`, "\\\"")

type namedFile struct {
	field string
	file  File
}

func (b *Bot) uploadMany(ctx context.Context, method string, chatID int64, fields map[string]any, files []namedFile) error {
	all := map[string]any{"chat_id": fmt.Sprint(chatID)}
	for k, v := range fields {
		all[k] = v
	}
	return b.postForm(ctx, method, all, files, nil)
}

func (b *Bot) postForm(ctx context.Context, method string, fields map[string]any, files []namedFile, out any) error {
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		err := func() error {
			for k, v := range fields {
				if s, ok := v.(string); ok {
					_ = mw.WriteField(k, s)
					continue
				}
				raw, _ := json.Marshal(v)
				_ = mw.WriteField(k, string(raw))
			}
			for _, f := range files {
				h := make(textproto.MIMEHeader)
				h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, f.field, quoteEscaper.Replace(filepath.Base(f.file.Name))))
				h.Set("Content-Type", "application/octet-stream")
				part, err := mw.CreatePart(h)
				if err != nil {
					return err
				}
				if _, err := io.Copy(part, f.file.Reader); err != nil {
					return err
				}
			}
			return mw.Close()
		}()
		_ = pw.CloseWithError(err)
	}()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.APIURL+"/"+method, pr)
	if err != nil {
		_ = pr.Close()
		return b.scrub(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := b.UploadClient.Do(req)
	if err != nil {
		_ = pr.CloseWithError(err)
		return b.scrub(err)
	}
	defer resp.Body.Close()
	return b.scrub(b.decode(resp, out))
}

func (b *Bot) upload(ctx context.Context, method, field string, chatID int64, f File, caption string, extra map[string]string, opts SendOptions) error {
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	name := filepath.Base(f.Name)

	jsonFields := map[string]any{}
	opts.apply(jsonFields)

	go func() {
		err := func() error {
			_ = mw.WriteField("chat_id", fmt.Sprint(chatID))
			if caption != "" {
				_ = mw.WriteField("caption", textutil.TruncateRunes(caption, 1000, "…"))
				_ = mw.WriteField("parse_mode", "HTML")
			}
			for k, v := range extra {
				_ = mw.WriteField(k, v)
			}
			for k, v := range jsonFields {
				if s, ok := v.(string); ok {
					_ = mw.WriteField(k, s)
					continue
				}
				raw, _ := json.Marshal(v)
				_ = mw.WriteField(k, string(raw))
			}
			h := make(textproto.MIMEHeader)
			h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, field, quoteEscaper.Replace(name)))
			h.Set("Content-Type", "application/octet-stream")
			part, err := mw.CreatePart(h)
			if err != nil {
				return err
			}
			if _, err := io.Copy(part, f.Reader); err != nil {
				return err
			}
			return mw.Close()
		}()
		_ = pw.CloseWithError(err)
	}()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.APIURL+"/"+method, pr)
	if err != nil {
		_ = pr.Close()
		return b.scrub(err)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := b.UploadClient.Do(req)
	if err != nil {
		_ = pr.CloseWithError(err)
		return b.scrub(err)
	}
	defer resp.Body.Close()
	return b.scrub(b.decode(resp, nil))
}
