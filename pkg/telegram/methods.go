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

// ---------------------------------------------------------------- rich messages (Bot API 10.1+)

// MaxRichChars is the size limit of one rich message.
const MaxRichChars = 32768

// wire is a rich message as it is sent: Markdown keeps its lines (HardBreaks).
func (rm RichMessage) wire() RichMessage {
	if rm.Markdown != "" {
		rm.Markdown = HardBreaks(rm.Markdown)
	}
	return rm
}

// SendRichMessage sends a rich (Markdown/HTML, blocks, tables…) message.
func (b *Bot) SendRichMessage(ctx context.Context, chatID int64, rm RichMessage, opts SendOptions) (*Message, error) {
	p := map[string]any{"chat_id": chatID, "rich_message": rm.wire()}
	opts.apply(p)
	var m Message
	if err := b.call(ctx, "sendRichMessage", p, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// RichFile is a file uploaded together with a rich message. Type is photo,
// video, animation, audio or document; the text shows it with a
// tg://<Link>?id=<ID> link (Link is photo, video, audio or document).
type RichFile struct {
	ID   string // 1–64 characters: A-Z, a-z, 0-9, _ and -
	Type string
	File File
}

// RichLink is the tg:// link scheme of a media type.
func RichLink(typ string) string {
	switch typ {
	case "photo", "audio", "document":
		return typ
	case "video", "animation":
		return "video"
	}
	return "document"
}

// MaxRichMedia is the number of media a rich message can hold.
const MaxRichMedia = 50

// SendRichMessageFiles sends a rich message whose text shows uploaded files
// (images, a swipeable <tg-slideshow>, videos, songs, documents).
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

// EditRichMessage replaces the content of a rich message.
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

// SendRichDraft streams a temporary preview of a message being generated
// (private chats only). Reusing draftID animates the change. With canStop the
// user sees a stop button and the bot receives Update.StoppedMessageGeneration.
// The preview disappears when the bot sends a message or after ~30 seconds.
func (b *Bot) SendRichDraft(ctx context.Context, chatID, draftID int64, rm RichMessage, canStop bool) error {
	p := map[string]any{"chat_id": chatID, "draft_id": draftID, "rich_message": rm.wire()}
	if canStop {
		p["can_stop"] = true
	}
	return b.call(ctx, "sendRichMessageDraft", p, nil)
}

// SendMessageDraft streams a plain-text draft (empty text shows "Thinking…").
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

// IsUnsupported reports an error caused by a method or parameter the chat or
// Bot API server does not support (e.g. drafts in groups, rich messages on an
// old local Bot API server).
func IsUnsupported(err error) bool {
	var api *APIError
	if !errors.As(err, &api) {
		return false
	}
	d := strings.ToLower(api.Description)
	return api.Code == http.StatusNotFound || strings.Contains(d, "method not found") || strings.Contains(d, "not supported") ||
		strings.Contains(d, "can't be used") || strings.Contains(d, "unsupported") || strings.Contains(d, "private chat")
}

// IsBadRequest reports a 400 answer (invalid content, unknown entity…).
func IsBadRequest(err error) bool {
	var api *APIError
	return errors.As(err, &api) && api.Code == http.StatusBadRequest
}

// ---------------------------------------------------------------- reactions

// SetReaction sets one emoji reaction on a message ("" removes the bot's reaction).
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

// ReactionEmoji is the set of emoji Telegram accepts as reactions.
var ReactionEmoji = []string{"👍", "👎", "❤", "🔥", "🥰", "👏", "😁", "🤔", "🤯", "😱", "🤬", "😢", "🎉", "🤩", "🤮", "💩", "🙏", "👌",
	"🕊", "🤡", "🥱", "🥴", "😍", "🐳", "❤‍🔥", "🌚", "🌭", "💯", "🤣", "⚡", "🍌", "🏆", "💔", "🤨", "😐", "🍓", "🍾", "💋", "🖕", "😈",
	"😴", "😭", "🤓", "👻", "👨‍💻", "👀", "🎃", "🙈", "😇", "😨", "🤝", "✍", "🤗", "🫡", "🎅", "🎄", "☃", "💅", "🤪", "🗿", "🆒",
	"💘", "🙉", "🦄", "😘", "💊", "🙊", "😎", "👾", "🤷‍♂", "🤷", "🤷‍♀", "😡"}

// NormalizeReaction maps an emoji to an allowed reaction ("" if impossible).
func NormalizeReaction(e string) string {
	e = strings.TrimSpace(strings.ReplaceAll(e, "️", ""))
	for _, r := range ReactionEmoji {
		if r == e {
			return r
		}
	}
	return ""
}

// ---------------------------------------------------------------- chats and members

// GetChatMember returns the membership of a user (the bot must be an admin of channels).
func (b *Bot) GetChatMember(ctx context.Context, chatID, userID int64) (*ChatMember, error) {
	var m ChatMember
	if err := b.call(ctx, "getChatMember", map[string]any{"chat_id": chatID, "user_id": userID}, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// GetChat returns full information about a chat; chat is an id or "@username".
func (b *Bot) GetChat(ctx context.Context, chat any) (*ChatFullInfo, error) {
	var c ChatFullInfo
	if err := b.call(ctx, "getChat", map[string]any{"chat_id": chat}, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// GetChatMemberCount returns the number of members of a chat.
func (b *Bot) GetChatMemberCount(ctx context.Context, chatID int64) (int, error) {
	var n int
	err := b.call(ctx, "getChatMemberCount", map[string]any{"chat_id": chatID}, &n)
	return n, err
}

// LeaveChat makes the bot leave a group or channel.
func (b *Bot) LeaveChat(ctx context.Context, chatID int64) error {
	return b.call(ctx, "leaveChat", map[string]any{"chat_id": chatID}, nil)
}

// ExportChatInviteLink creates the primary invite link (the bot must be an admin).
func (b *Bot) ExportChatInviteLink(ctx context.Context, chatID int64) (string, error) {
	var link string
	err := b.call(ctx, "exportChatInviteLink", map[string]any{"chat_id": chatID}, &link)
	return link, err
}

// GetUserProfilePhotos lists a user's profile pictures, newest first.
func (b *Bot) GetUserProfilePhotos(ctx context.Context, userID int64, limit int) (*UserProfilePhotos, error) {
	var p UserProfilePhotos
	if err := b.call(ctx, "getUserProfilePhotos", map[string]any{"user_id": userID, "limit": limit}, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// ---------------------------------------------------------------- files

// GetFile resolves a file id to a downloadable path (files up to 20 MB, or
// any size through a local server, which names the file on its disk).
func (b *Bot) GetFile(ctx context.Context, fileID string) (*TelegramFile, error) {
	var f TelegramFile
	if err := b.call(ctx, "getFile", map[string]any{"file_id": fileID}, &f); err != nil {
		return nil, err
	}
	return &f, nil
}

// ErrFileTooLarge is returned when a download exceeds the limit.
var ErrFileTooLarge = errors.New("file is too large")

// Download streams a file from Telegram into w, at most max bytes.
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

// ---------------------------------------------------------------- copy / forward

// CopyMessage sends a copy of a message (no "forwarded from" header) and returns the new id.
func (b *Bot) CopyMessage(ctx context.Context, toChat, fromChat, messageID int64, opts SendOptions) (int64, error) {
	p := map[string]any{"chat_id": toChat, "from_chat_id": fromChat, "message_id": messageID}
	opts.apply(p)
	var out struct {
		MessageID int64 `json:"message_id"`
	}
	err := b.call(ctx, "copyMessage", p, &out)
	return out.MessageID, err
}

// ForwardMessage forwards a message with its original sender shown.
func (b *Bot) ForwardMessage(ctx context.Context, toChat, fromChat, messageID int64, silent bool) (int64, error) {
	p := map[string]any{"chat_id": toChat, "from_chat_id": fromChat, "message_id": messageID}
	if silent {
		p["disable_notification"] = true
	}
	var m Message
	err := b.call(ctx, "forwardMessage", p, &m)
	return m.MessageID, err
}

// EditMarkup replaces only the inline keyboard of a message.
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

// ---------------------------------------------------------------- polls

// SendPoll sends a regular, non-anonymous poll, so the bot learns who voted
// (Update.PollAnswer).
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

// Quiz is a poll with one right answer, shown after the user votes.
type Quiz struct {
	Correct     int    // index of the right option
	Explanation string // shown after answering (0–200 characters)
}

// SendQuiz sends a quiz (anonymous unless the chat is private; the bot does not need the votes).
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

// SendOpenPoll sends a regular poll the bot does not follow (a group vote).
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

// StopPoll closes a poll the bot sent.
func (b *Bot) StopPoll(ctx context.Context, chatID, messageID int64) error {
	return b.call(ctx, "stopPoll", map[string]any{"chat_id": chatID, "message_id": messageID}, nil)
}

// ---------------------------------------------------------------- albums

// MediaItem is one file of an album: Type is photo, video, audio or document
// (photos and videos can share an album; audio and documents only with their own kind).
type MediaItem struct {
	Type    string
	File    File
	Caption string
}

// SendMediaGroup sends 2–10 files as one album.
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

// ---------------------------------------------------------------- guest mode (Bot API 10.0)

// MaxGuestTextChars is the limit of a text guest answer (InputTextMessageContent).
const MaxGuestTextChars = 4096

// AnswerGuestQuery sends the single reply to a guest message. result is an
// InlineQueryResult; the reply appears in the chat where the bot was summoned.
// It returns the reply's inline_message_id (SentGuestMessage), with which the
// reply can be edited later; "" when the server did not give one.
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

// EditInlineRich replaces a message sent through the bot (a guest or inline
// answer, known by its inline_message_id) with a rich message.
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

// EditInlineHTML replaces the text of a message sent through the bot with
// HTML (plain text when the HTML is refused).
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

// GuestRich answers with a rich message (InputRichMessageContent, Bot API 10.1).
func GuestRich(rm RichMessage, kb *InlineKeyboardMarkup) map[string]any {
	return guestArticle(map[string]any{"rich_message": rm.wire()}, kb)
}

// GuestHTML answers with an HTML text message (at most 4096 characters).
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

// ---------------------------------------------------------------- dice, places

// DiceEmoji are the animated dice: 🎲 and 🎯 and 🎳 give 1–6, 🏀 and ⚽ 1–5, 🎰 1–64.
var DiceEmoji = []string{"🎲", "🎯", "🏀", "⚽", "🎳", "🎰"}

// SendDice sends an animated die and returns the value it landed on.
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

// SendLocation sends a point on the map.
func (b *Bot) SendLocation(ctx context.Context, chatID int64, lat, lon float64, opts SendOptions) error {
	p := map[string]any{"chat_id": chatID, "latitude": lat, "longitude": lon}
	opts.apply(p)
	return b.call(ctx, "sendLocation", p, nil)
}

// SendVenue sends a place with its name and address.
func (b *Bot) SendVenue(ctx context.Context, chatID int64, lat, lon float64, title, address string, opts SendOptions) error {
	p := map[string]any{"chat_id": chatID, "latitude": lat, "longitude": lon, "title": title, "address": address}
	opts.apply(p)
	return b.call(ctx, "sendVenue", p, nil)
}

// Message effects (private chats only). Telegram does not publish the list;
// these are the long-standing default ones.
var MessageEffects = map[string]string{
	"🔥": "5104841245755180586",
	"👍": "5107584321108051014",
	"👎": "5104858069142078462",
	"❤": "5044134455711629726",
	"🎉": "5046509860389126442",
	"💩": "5046589136895476101",
}

// EffectNames lists the effect emoji in a stable order.
var EffectNames = []string{"🎉", "🔥", "❤", "👍", "👎", "💩"}

// ---------------------------------------------------------------- payments in Telegram Stars

// Invoice is a purchase paid in Telegram Stars (currency XTR).
type Invoice struct {
	Title       string // 1–32 characters
	Description string // 1–255 characters
	Payload     string // 1–128 bytes, never shown to the user
	Label       string // the price line
	Stars       int
	PhotoURL    string
	// StartParameter makes forwarded copies open the bot instead of paying,
	// so an invoice is only paid in the chat it was sent to.
	StartParameter string
}

// SendInvoice sends an invoice in Telegram Stars.
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

// CreateInvoiceLink makes a link that opens the payment form of an invoice
// in Telegram Stars, for a URL button: paying needs no invoice message in
// the chat (the pre-checkout query and successful_payment come as usual).
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

// AnswerPreCheckoutQuery confirms (errorMessage "") or refuses a purchase.
func (b *Bot) AnswerPreCheckoutQuery(ctx context.Context, queryID, errorMessage string) error {
	p := map[string]any{"pre_checkout_query_id": queryID, "ok": errorMessage == ""}
	if errorMessage != "" {
		p["error_message"] = errorMessage
	}
	return b.call(ctx, "answerPreCheckoutQuery", p, nil)
}

// RefundStarPayment gives the Stars of a payment back to the user.
func (b *Bot) RefundStarPayment(ctx context.Context, userID int64, chargeID string) error {
	return b.call(ctx, "refundStarPayment", map[string]any{"user_id": userID, "telegram_payment_charge_id": chargeID}, nil)
}

// StarAmount is an amount of Telegram Stars.
type StarAmount struct {
	Amount         int64 `json:"amount"`
	NanostarAmount int64 `json:"nanostar_amount,omitempty"`
}

// GetMyStarBalance returns the bot's balance of Telegram Stars.
func (b *Bot) GetMyStarBalance(ctx context.Context) (*StarAmount, error) {
	var s StarAmount
	if err := b.call(ctx, "getMyStarBalance", map[string]any{}, &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// StickerSet is a sticker or custom emoji set.
type StickerSet struct {
	Name        string    `json:"name"`
	Title       string    `json:"title"`
	StickerType string    `json:"sticker_type"`
	Stickers    []Sticker `json:"stickers"`
}

// AnimatedEmoji is an animated (.tgs) custom emoji to upload, with the plain
// emoji it stands for.
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

// CreateEmojiSet creates a custom emoji set owned by userID (someone who has
// started the bot). With repaint, Telegram draws the emoji in the color of
// the text around them.
func (b *Bot) CreateEmojiSet(ctx context.Context, userID int64, name, title string, list []AnimatedEmoji, repaint bool) error {
	inputs, files := emojiInputs(list)
	return b.postForm(ctx, "createNewStickerSet", map[string]any{"user_id": fmt.Sprint(userID), "name": name, "title": title,
		"sticker_type": "custom_emoji", "needs_repainting": repaint, "stickers": inputs}, files, nil)
}

// GetStickerSet returns a set by its name.
func (b *Bot) GetStickerSet(ctx context.Context, name string) (*StickerSet, error) {
	var s StickerSet
	if err := b.call(ctx, "getStickerSet", map[string]any{"name": name}, &s); err != nil {
		return nil, err
	}
	return &s, nil
}
