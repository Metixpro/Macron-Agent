package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/Metixpro/macron-agent/pkg/textutil"
)

const MaxRichChars = 32768

func (rm RichMessage) wire() RichMessage {
	if rm.Markdown != "" {
		rm.Markdown = HardBreaks(rm.Markdown)
	}
	return rm
}

func (b *Bot) SendRichMessage(ctx context.Context, chatID int64, rm RichMessage, opts SendOptions) (*Message, error) {
	p := map[string]any{"chat_id": chatID, "rich_message": rm.wire()}
	opts.apply(p)
	var m Message
	if err := b.call(ctx, "sendRichMessage", p, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

type RichFile struct {
	ID   string
	Type string
	File File
}

func RichLink(typ string) string {
	switch typ {
	case "photo", "audio", "document":
		return typ
	case "video", "animation":
		return "video"
	}
	return "document"
}

const MaxRichMedia = 50

func (b *Bot) SendRichMessageFiles(ctx context.Context, chatID int64, rm RichMessage, files []RichFile, opts SendOptions) error {
	if len(files) == 0 || len(files) > MaxRichMedia {
		return fmt.Errorf("a rich message holds 1 to %d files, got %d", MaxRichMedia, len(files))
	}
	rm.Media = nil
	parts := make([]namedFile, len(files))
	for i, f := range files {
		rm.Media = append(rm.Media, RichMediaSpec{ID: f.ID, Media: map[string]string{"type": f.Type, "media": "attach://" + f.ID}})
		parts[i] = namedFile{field: f.ID, file: f.File}
	}
	fields := map[string]any{"rich_message": rm.wire()}
	opts.apply(fields)
	return b.uploadMany(ctx, "sendRichMessage", chatID, fields, parts)
}

func (b *Bot) EditRichMessage(ctx context.Context, chatID, messageID int64, rm RichMessage, kb *InlineKeyboardMarkup) error {
	p := map[string]any{"chat_id": chatID, "message_id": messageID, "rich_message": rm.wire()}
	if kb != nil {
		p["reply_markup"] = kb
	}
	err := b.call(ctx, "editMessageText", p, nil)
	if IsNotModified(err) {
		return nil
	}
	return err
}

func (b *Bot) SendRichDraft(ctx context.Context, chatID, draftID int64, rm RichMessage, canStop bool) error {
	p := map[string]any{"chat_id": chatID, "draft_id": draftID, "rich_message": rm.wire()}
	if canStop {
		p["can_stop"] = true
	}
	return b.call(ctx, "sendRichMessageDraft", p, nil)
}

func (b *Bot) SendMessageDraft(ctx context.Context, chatID, draftID int64, text, parseMode string, canStop bool) error {
	p := map[string]any{"chat_id": chatID, "draft_id": draftID, "text": text}
	if parseMode != "" {
		p["parse_mode"] = parseMode
	}
	if canStop {
		p["can_stop"] = true
	}
	return b.call(ctx, "sendMessageDraft", p, nil)
}

func IsUnsupported(err error) bool {
	var api *APIError
	if !errors.As(err, &api) {
		return false
	}
	d := strings.ToLower(api.Description)
	return api.Code == http.StatusNotFound || strings.Contains(d, "method not found") || strings.Contains(d, "not supported") ||
		strings.Contains(d, "can't be used") || strings.Contains(d, "unsupported") || strings.Contains(d, "private chat")
}

func IsBadRequest(err error) bool {
	var api *APIError
	return errors.As(err, &api) && api.Code == http.StatusBadRequest
}

func (b *Bot) SetReaction(ctx context.Context, chatID, messageID int64, emoji string, big bool) error {
	reaction := []ReactionType{}
	if emoji != "" {
		reaction = append(reaction, ReactionType{Type: "emoji", Emoji: emoji})
	}
	p := map[string]any{"chat_id": chatID, "message_id": messageID, "reaction": reaction}
	if big {
		p["is_big"] = true
	}
	return b.call(ctx, "setMessageReaction", p, nil)
}

var ReactionEmoji = []string{"👍", "👎", "❤", "🔥", "🥰", "👏", "😁", "🤔", "🤯", "😱", "🤬", "😢", "🎉", "🤩", "🤮", "💩", "🙏", "👌",
	"🕊", "🤡", "🥱", "🥴", "😍", "🐳", "❤‍🔥", "🌚", "🌭", "💯", "🤣", "⚡", "🍌", "🏆", "💔", "🤨", "😐", "🍓", "🍾", "💋", "🖕", "😈",
	"😴", "😭", "🤓", "👻", "👨‍💻", "👀", "🎃", "🙈", "😇", "😨", "🤝", "✍", "🤗", "🫡", "🎅", "🎄", "☃", "💅", "🤪", "🗿", "🆒",
	"💘", "🙉", "🦄", "😘", "💊", "🙊", "😎", "👾", "🤷‍♂", "🤷", "🤷‍♀", "😡"}

func NormalizeReaction(e string) string {
	e = strings.TrimSpace(strings.ReplaceAll(e, "️", ""))
	for _, r := range ReactionEmoji {
		if r == e {
			return r
		}
	}
	return ""
}

func (b *Bot) GetChatMember(ctx context.Context, chatID, userID int64) (*ChatMember, error) {
	var m ChatMember
	if err := b.call(ctx, "getChatMember", map[string]any{"chat_id": chatID, "user_id": userID}, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func (b *Bot) GetChat(ctx context.Context, chat any) (*ChatFullInfo, error) {
	var c ChatFullInfo
	if err := b.call(ctx, "getChat", map[string]any{"chat_id": chat}, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

func (b *Bot) GetChatMemberCount(ctx context.Context, chatID int64) (int, error) {
	var n int
	err := b.call(ctx, "getChatMemberCount", map[string]any{"chat_id": chatID}, &n)
	return n, err
}

func (b *Bot) LeaveChat(ctx context.Context, chatID int64) error {
	return b.call(ctx, "leaveChat", map[string]any{"chat_id": chatID}, nil)
}

func (b *Bot) ExportChatInviteLink(ctx context.Context, chatID int64) (string, error) {
	var link string
	err := b.call(ctx, "exportChatInviteLink", map[string]any{"chat_id": chatID}, &link)
	return link, err
}

func (b *Bot) GetUserProfilePhotos(ctx context.Context, userID int64, limit int) (*UserProfilePhotos, error) {
	var p UserProfilePhotos
	if err := b.call(ctx, "getUserProfilePhotos", map[string]any{"user_id": userID, "limit": limit}, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

func (b *Bot) GetFile(ctx context.Context, fileID string) (*TelegramFile, error) {
	var f TelegramFile
	if err := b.call(ctx, "getFile", map[string]any{"file_id": fileID}, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

var ErrFileTooLarge = errors.New("file is too large")

func (b *Bot) Download(ctx context.Context, fileID string, w io.Writer, max int64) (int64, error) {
	f, err := b.GetFile(ctx, fileID)
	if err != nil {
		return 0, err
	}
	if f.FileSize > max {
		return 0, ErrFileTooLarge
	}
	if n, ok, err := b.readLocal(f.FilePath, w, max); ok {
		return n, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.fileBase+"/file/bot"+b.Token+"/"+strings.TrimPrefix(f.FilePath, "/"), nil)
	if err != nil {
		return 0, b.scrub(err)
	}
	resp, err := b.UploadClient.Do(req)
	if err != nil {
		return 0, b.scrub(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("download failed with status %d", resp.StatusCode)
	}
	n, err := io.Copy(w, io.LimitReader(resp.Body, max+1))
	if err != nil {
		return n, b.scrub(err)
	}
	if n > max {
		return n, ErrFileTooLarge
	}
	return n, nil
}

func (b *Bot) CopyMessage(ctx context.Context, toChat, fromChat, messageID int64, opts SendOptions) (int64, error) {
	p := map[string]any{"chat_id": toChat, "from_chat_id": fromChat, "message_id": messageID}
	opts.apply(p)
	var out struct {
		MessageID int64 `json:"message_id"`
	}
	err := b.call(ctx, "copyMessage", p, &out)
	return out.MessageID, err
}

func (b *Bot) ForwardMessage(ctx context.Context, toChat, fromChat, messageID int64, silent bool) (int64, error) {
	p := map[string]any{"chat_id": toChat, "from_chat_id": fromChat, "message_id": messageID}
	if silent {
		p["disable_notification"] = true
	}
	var m Message
	err := b.call(ctx, "forwardMessage", p, &m)
	return m.MessageID, err
}

func (b *Bot) EditMarkup(ctx context.Context, chatID, messageID int64, kb *InlineKeyboardMarkup) error {
	p := map[string]any{"chat_id": chatID, "message_id": messageID}
	if kb != nil {
		p["reply_markup"] = kb
	} else {
		p["reply_markup"] = InlineKeyboardMarkup{InlineKeyboard: [][]InlineKeyboardButton{}}
	}
	err := b.call(ctx, "editMessageReplyMarkup", p, nil)
	if IsNotModified(err) {
		return nil
	}
	return err
}

func (b *Bot) SendPoll(ctx context.Context, chatID int64, question string, options []string, multiple bool, opts SendOptions) (*Message, error) {
	opt := make([]map[string]string, 0, len(options))
	for _, o := range options {
		opt = append(opt, map[string]string{"text": o})
	}
	p := map[string]any{"chat_id": chatID, "question": question, "options": opt, "is_anonymous": false, "type": "regular"}
	if multiple {
		p["allows_multiple_answers"] = true
	}
	opts.apply(p)
	var m Message
	if err := b.call(ctx, "sendPoll", p, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

type Quiz struct {
	Correct     int
	Explanation string
}

func (b *Bot) SendQuiz(ctx context.Context, chatID int64, question string, options []string, q Quiz, opts SendOptions) (*Message, error) {
	opt := make([]map[string]string, 0, len(options))
	for _, o := range options {
		opt = append(opt, map[string]string{"text": o})
	}
	p := map[string]any{"chat_id": chatID, "question": question, "options": opt, "type": "quiz", "correct_option_id": q.Correct}
	if q.Explanation != "" {
		p["explanation"] = textutil.TruncateRunes(q.Explanation, 200, "…")
	}
	opts.apply(p)
	var m Message
	if err := b.call(ctx, "sendPoll", p, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func (b *Bot) SendOpenPoll(ctx context.Context, chatID int64, question string, options []string, multiple, anonymous bool, opts SendOptions) (*Message, error) {
	opt := make([]map[string]string, 0, len(options))
	for _, o := range options {
		opt = append(opt, map[string]string{"text": o})
	}
	p := map[string]any{"chat_id": chatID, "question": question, "options": opt, "is_anonymous": anonymous, "type": "regular"}
	if multiple {
		p["allows_multiple_answers"] = true
	}
	opts.apply(p)
	var m Message
	if err := b.call(ctx, "sendPoll", p, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func (b *Bot) StopPoll(ctx context.Context, chatID, messageID int64) error {
	return b.call(ctx, "stopPoll", map[string]any{"chat_id": chatID, "message_id": messageID}, nil)
}

type MediaItem struct {
	Type    string
	File    File
	Caption string
}

func (b *Bot) SendMediaGroup(ctx context.Context, chatID int64, items []MediaItem, opts SendOptions) error {
	if len(items) < 2 || len(items) > 10 {
		return fmt.Errorf("an album needs 2 to 10 items, got %d", len(items))
	}
	media := make([]map[string]string, 0, len(items))
	for i, it := range items {
		m := map[string]string{"type": it.Type, "media": fmt.Sprintf("attach://f%d", i)}
		if it.Caption != "" {
			m["caption"], m["parse_mode"] = textutil.TruncateRunes(it.Caption, 1000, "…"), "HTML"
		}
		media = append(media, m)
	}
	raw, _ := json.Marshal(media)
	fields := map[string]any{"media": string(raw)}
	opts.apply(fields)
	files := make([]namedFile, len(items))
	for i, it := range items {
		files[i] = namedFile{field: fmt.Sprintf("f%d", i), file: it.File}
	}
	return b.uploadMany(ctx, "sendMediaGroup", chatID, fields, files)
}

const MaxGuestTextChars = 4096

func (b *Bot) AnswerGuestQuery(ctx context.Context, queryID string, result any) (string, error) {
	var raw json.RawMessage
	if err := b.call(ctx, "answerGuestQuery", map[string]any{"guest_query_id": queryID, "result": result}, &raw); err != nil {
		return "", err
	}
	var sent struct {
		InlineMessageID string `json:"inline_message_id"`
	}
	if len(raw) > 0 && raw[0] == '{' {
		_ = json.Unmarshal(raw, &sent)
	}
	return sent.InlineMessageID, nil
}

func (b *Bot) EditInlineRich(ctx context.Context, inlineID string, rm RichMessage, kb *InlineKeyboardMarkup) error {
	p := map[string]any{"inline_message_id": inlineID, "rich_message": rm.wire()}
	if kb != nil {
		p["reply_markup"] = kb
	}
	err := b.call(ctx, "editMessageText", p, nil)
	if IsNotModified(err) {
		return nil
	}
	return err
}

func (b *Bot) EditInlineHTML(ctx context.Context, inlineID, html string, kb *InlineKeyboardMarkup) error {
	edit := func(text, mode string) error {
		p := map[string]any{"inline_message_id": inlineID, "text": text, "link_preview_options": map[string]any{"is_disabled": true}}
		if mode != "" {
			p["parse_mode"] = mode
		}
		if kb != nil {
			p["reply_markup"] = kb
		}
		return b.call(ctx, "editMessageText", p, nil)
	}
	err := edit(html, "HTML")
	if IsParseError(err) {
		err = edit(textutil.StripHTML(html), "")
	}
	if IsNotModified(err) {
		return nil
	}
	return err
}

func GuestRich(rm RichMessage, kb *InlineKeyboardMarkup) map[string]any {
	return guestArticle(map[string]any{"rich_message": rm.wire()}, kb)
}

func GuestHTML(html string, kb *InlineKeyboardMarkup) map[string]any {
	return guestArticle(map[string]any{"message_text": html, "parse_mode": "HTML", "link_preview_options": map[string]any{"is_disabled": true}}, kb)
}

func guestArticle(content map[string]any, kb *InlineKeyboardMarkup) map[string]any {
	r := map[string]any{"type": "article", "id": "answer", "title": "Macron Ai", "input_message_content": content}
	if kb != nil {
		r["reply_markup"] = kb
	}
	return r
}

var DiceEmoji = []string{"🎲", "🎯", "🏀", "⚽", "🎳", "🎰"}

func (b *Bot) SendDice(ctx context.Context, chatID int64, emoji string, opts SendOptions) (int, error) {
	p := map[string]any{"chat_id": chatID, "emoji": emoji}
	opts.apply(p)
	var m Message
	if err := b.call(ctx, "sendDice", p, &m); err != nil {
		return 0, err
	}
	if m.Dice == nil {
		return 0, nil
	}
	return m.Dice.Value, nil
}

func (b *Bot) SendLocation(ctx context.Context, chatID int64, lat, lon float64, opts SendOptions) error {
	p := map[string]any{"chat_id": chatID, "latitude": lat, "longitude": lon}
	opts.apply(p)
	return b.call(ctx, "sendLocation", p, nil)
}

func (b *Bot) SendVenue(ctx context.Context, chatID int64, lat, lon float64, title, address string, opts SendOptions) error {
	p := map[string]any{"chat_id": chatID, "latitude": lat, "longitude": lon, "title": title, "address": address}
	opts.apply(p)
	return b.call(ctx, "sendVenue", p, nil)
}

var MessageEffects = map[string]string{
	"🔥": "5104841245755180586",
	"👍": "5107584321108051014",
	"👎": "5104858069142078462",
	"❤": "5044134455711629726",
	"🎉": "5046509860389126442",
	"💩": "5046589136895476101",
}

var EffectNames = []string{"🎉", "🔥", "❤", "👍", "👎", "💩"}

type Invoice struct {
	Title       string
	Description string
	Payload     string
	Label       string
	Stars       int
	PhotoURL    string

	StartParameter string
}

func (b *Bot) SendInvoice(ctx context.Context, chatID int64, inv Invoice, opts SendOptions) (*Message, error) {
	p := map[string]any{
		"chat_id":     chatID,
		"title":       textutil.TruncateRunes(inv.Title, 32, ""),
		"description": textutil.TruncateRunes(inv.Description, 255, "…"),
		"payload":     inv.Payload,
		"currency":    "XTR",
		"prices":      []map[string]any{{"label": textutil.TruncateRunes(inv.Label, 32, ""), "amount": inv.Stars}},
	}
	if inv.PhotoURL != "" {
		p["photo_url"] = inv.PhotoURL
	}
	if inv.StartParameter != "" {
		p["start_parameter"] = inv.StartParameter
	}
	opts.apply(p)
	var m Message
	if err := b.call(ctx, "sendInvoice", p, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func (b *Bot) CreateInvoiceLink(ctx context.Context, inv Invoice) (string, error) {
	p := map[string]any{
		"title":       textutil.TruncateRunes(inv.Title, 32, ""),
		"description": textutil.TruncateRunes(inv.Description, 255, "…"),
		"payload":     inv.Payload,
		"currency":    "XTR",
		"prices":      []map[string]any{{"label": textutil.TruncateRunes(inv.Label, 32, ""), "amount": inv.Stars}},
	}
	if inv.PhotoURL != "" {
		p["photo_url"] = inv.PhotoURL
	}
	var link string
	err := b.call(ctx, "createInvoiceLink", p, &link)
	return link, err
}

func (b *Bot) AnswerPreCheckoutQuery(ctx context.Context, queryID, errorMessage string) error {
	p := map[string]any{"pre_checkout_query_id": queryID, "ok": errorMessage == ""}
	if errorMessage != "" {
		p["error_message"] = errorMessage
	}
	return b.call(ctx, "answerPreCheckoutQuery", p, nil)
}

func (b *Bot) RefundStarPayment(ctx context.Context, userID int64, chargeID string) error {
	return b.call(ctx, "refundStarPayment", map[string]any{"user_id": userID, "telegram_payment_charge_id": chargeID}, nil)
}

type StarAmount struct {
	Amount         int64 `json:"amount"`
	NanostarAmount int64 `json:"nanostar_amount,omitempty"`
}

func (b *Bot) GetMyStarBalance(ctx context.Context) (*StarAmount, error) {
	var s StarAmount
	if err := b.call(ctx, "getMyStarBalance", map[string]any{}, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

type StickerSet struct {
	Name        string    `json:"name"`
	Title       string    `json:"title"`
	StickerType string    `json:"sticker_type"`
	Stickers    []Sticker `json:"stickers"`
}

type AnimatedEmoji struct {
	Emoji string
	TGS   []byte
}

func emojiInputs(list []AnimatedEmoji) ([]map[string]any, []namedFile) {
	inputs := make([]map[string]any, len(list))
	files := make([]namedFile, len(list))
	for i, e := range list {
		field := fmt.Sprintf("emoji%d", i)
		inputs[i] = map[string]any{"sticker": "attach://" + field, "format": "animated", "emoji_list": []string{e.Emoji}}
		files[i] = namedFile{field: field, file: File{Name: field + ".tgs", Reader: bytes.NewReader(e.TGS)}}
	}
	return inputs, files
}

func (b *Bot) CreateEmojiSet(ctx context.Context, userID int64, name, title string, list []AnimatedEmoji, repaint bool) error {
	inputs, files := emojiInputs(list)
	return b.postForm(ctx, "createNewStickerSet", map[string]any{"user_id": fmt.Sprint(userID), "name": name, "title": title,
		"sticker_type": "custom_emoji", "needs_repainting": repaint, "stickers": inputs}, files, nil)
}

func (b *Bot) GetStickerSet(ctx context.Context, name string) (*StickerSet, error) {
	var s StickerSet
	if err := b.call(ctx, "getStickerSet", map[string]any{"name": name}, &s); err != nil {
		return nil, err
	}
	return &s, nil
}
