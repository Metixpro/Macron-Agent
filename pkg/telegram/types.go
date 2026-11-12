package telegram

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

type User struct {
	ID           int64  `json:"id"`
	IsBot        bool   `json:"is_bot"`
	FirstName    string `json:"first_name"`
	LastName     string `json:"last_name,omitempty"`
	Username     string `json:"username,omitempty"`
	LanguageCode string `json:"language_code,omitempty"`
	IsPremium    bool   `json:"is_premium,omitempty"`

	SupportsGuestQueries bool `json:"supports_guest_queries,omitempty"`

	CanReadAllGroupMessages bool `json:"can_read_all_group_messages,omitempty"`
}

func (u User) FullName() string {
	n := strings.TrimSpace(u.FirstName + " " + u.LastName)
	if n == "" && u.Username != "" {
		return "@" + u.Username
	}
	return n
}

type Chat struct {
	ID        int64  `json:"id"`
	Type      string `json:"type"`
	Title     string `json:"title,omitempty"`
	Username  string `json:"username,omitempty"`
	FirstName string `json:"first_name,omitempty"`
	LastName  string `json:"last_name,omitempty"`
}

func (c Chat) Private() bool { return c.Type == "private" }

type MessageEntity struct {
	Type          string `json:"type"`
	Offset        int    `json:"offset"`
	Length        int    `json:"length"`
	URL           string `json:"url,omitempty"`
	User          *User  `json:"user,omitempty"`
	Language      string `json:"language,omitempty"`
	CustomEmojiID string `json:"custom_emoji_id,omitempty"`
}

type PhotoSize struct {
	FileID       string `json:"file_id"`
	FileUniqueID string `json:"file_unique_id"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	FileSize     int64  `json:"file_size,omitempty"`
}

type Document struct {
	FileID   string `json:"file_id"`
	FileName string `json:"file_name,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
	FileSize int64  `json:"file_size,omitempty"`
}

type Sticker struct {
	FileID        string     `json:"file_id"`
	Type          string     `json:"type"`
	Emoji         string     `json:"emoji,omitempty"`
	SetName       string     `json:"set_name,omitempty"`
	CustomEmojiID string     `json:"custom_emoji_id,omitempty"`
	IsAnimated    bool       `json:"is_animated"`
	IsVideo       bool       `json:"is_video"`
	FileSize      int64      `json:"file_size,omitempty"`
	Thumbnail     *PhotoSize `json:"thumbnail,omitempty"`
}

type Video struct {
	FileID    string     `json:"file_id"`
	FileName  string     `json:"file_name,omitempty"`
	MimeType  string     `json:"mime_type,omitempty"`
	Duration  int        `json:"duration"`
	Width     int        `json:"width"`
	Height    int        `json:"height"`
	FileSize  int64      `json:"file_size,omitempty"`
	Thumbnail *PhotoSize `json:"thumbnail,omitempty"`
}

type Poll struct {
	ID                    string       `json:"id"`
	Question              string       `json:"question"`
	Options               []PollOption `json:"options"`
	IsClosed              bool         `json:"is_closed"`
	IsAnonymous           bool         `json:"is_anonymous"`
	AllowsMultipleAnswers bool         `json:"allows_multiple_answers"`
}

type PollOption struct {
	Text       string `json:"text"`
	VoterCount int    `json:"voter_count"`
}

type PollAnswer struct {
	PollID    string `json:"poll_id"`
	VoterChat *Chat  `json:"voter_chat,omitempty"`
	User      *User  `json:"user,omitempty"`
	OptionIDs []int  `json:"option_ids"`
}

type Voice struct {
	FileID   string `json:"file_id"`
	Duration int    `json:"duration"`
	MimeType string `json:"mime_type,omitempty"`
	FileSize int64  `json:"file_size,omitempty"`
}

type Audio struct {
	FileID    string `json:"file_id"`
	Duration  int    `json:"duration"`
	FileName  string `json:"file_name,omitempty"`
	MimeType  string `json:"mime_type,omitempty"`
	Title     string `json:"title,omitempty"`
	Performer string `json:"performer,omitempty"`
	FileSize  int64  `json:"file_size,omitempty"`
}

type Dice struct {
	Emoji string `json:"emoji"`
	Value int    `json:"value"`
}

type Location struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

type Contact struct {
	PhoneNumber string `json:"phone_number"`
	FirstName   string `json:"first_name"`
	LastName    string `json:"last_name,omitempty"`
	UserID      int64  `json:"user_id,omitempty"`
}

type TextQuote struct {
	Text     string `json:"text"`
	Position int    `json:"position"`
	IsManual bool   `json:"is_manual,omitempty"`
}

type MessageOrigin struct {
	Type            string `json:"type"`
	Date            int64  `json:"date"`
	SenderUser      *User  `json:"sender_user,omitempty"`
	SenderUserName  string `json:"sender_user_name,omitempty"`
	SenderChat      *Chat  `json:"sender_chat,omitempty"`
	Chat            *Chat  `json:"chat,omitempty"`
	MessageID       int64  `json:"message_id,omitempty"`
	AuthorSignature string `json:"author_signature,omitempty"`
}

func (o *MessageOrigin) Name() string {
	switch {
	case o == nil:
		return ""
	case o.SenderUser != nil:
		return o.SenderUser.FullName()
	case o.SenderUserName != "":
		return o.SenderUserName
	case o.Chat != nil:
		return chatName(o.Chat)
	case o.SenderChat != nil:
		return chatName(o.SenderChat)
	}
	return "unknown"
}

func (o *MessageOrigin) ChatID() int64 {
	switch {
	case o == nil:
		return 0
	case o.Chat != nil:
		return o.Chat.ID
	case o.SenderChat != nil:
		return o.SenderChat.ID
	}
	return 0
}

func (o *MessageOrigin) UserID() int64 {
	if o == nil || o.SenderUser == nil {
		return 0
	}
	return o.SenderUser.ID
}

func (o *MessageOrigin) Describe() string {
	if o == nil {
		return ""
	}
	var parts []string
	switch {
	case o.SenderUser != nil:
		u := o.SenderUser
		parts = append(parts, u.FullName())
		if u.Username != "" && !strings.HasPrefix(parts[0], "@") {
			parts = append(parts, "@"+u.Username)
		}
		if u.IsBot {
			parts = append(parts, "bot")
		}
		parts = append(parts, fmt.Sprintf("id: %d", u.ID))
	case o.SenderUserName != "":
		return o.SenderUserName + " (hidden user, ID not provided by Telegram)"
	case o.Chat != nil || o.SenderChat != nil:
		c := o.Chat
		if c == nil {
			c = o.SenderChat
		}
		parts = append(parts, chatName(c))
		if c.Username != "" && c.Title != "" {
			parts = append(parts, "@"+c.Username)
		}
		parts = append(parts, fmt.Sprintf("id: %d", c.ID))
		if o.MessageID != 0 {
			parts = append(parts, fmt.Sprintf("msg_id: %d", o.MessageID))
		}
		if o.Type == "chat" {
			parts = append(parts, "sent anonymously on behalf of the group")
		}
	default:
		return "an unknown sender"
	}
	if o.AuthorSignature != "" {
		parts = append(parts, "signed: "+o.AuthorSignature)
	}
	return strings.Join(parts, " · ")
}

func chatName(c *Chat) string {
	switch {
	case c.Title != "":
		return c.Title
	case c.Username != "":
		return "@" + c.Username
	}
	return strings.TrimSpace(c.FirstName + " " + c.LastName)
}

type Message struct {
	MessageID       int64           `json:"message_id"`
	From            *User           `json:"from,omitempty"`
	SenderChat      *Chat           `json:"sender_chat,omitempty"`
	Chat            Chat            `json:"chat"`
	Date            int64           `json:"date"`
	Text            string          `json:"text,omitempty"`
	Entities        []MessageEntity `json:"entities,omitempty"`
	Caption         string          `json:"caption,omitempty"`
	CaptionEntities []MessageEntity `json:"caption_entities,omitempty"`
	Photo           []PhotoSize     `json:"photo,omitempty"`
	Document        *Document       `json:"document,omitempty"`
	Sticker         *Sticker        `json:"sticker,omitempty"`
	Video           *Video          `json:"video,omitempty"`
	Animation       *Video          `json:"animation,omitempty"`
	Voice           *Voice          `json:"voice,omitempty"`
	Audio           *Audio          `json:"audio,omitempty"`
	VideoNote       *Video          `json:"video_note,omitempty"`
	Location        *Location       `json:"location,omitempty"`
	Contact         *Contact        `json:"contact,omitempty"`
	ReplyToMessage  *Message        `json:"reply_to_message,omitempty"`
	Quote           *TextQuote      `json:"quote,omitempty"`

	ExternalReply *ExternalReplyInfo `json:"external_reply,omitempty"`
	ReplyToStory  *Story             `json:"reply_to_story,omitempty"`

	ForumTopicCreated json.RawMessage `json:"forum_topic_created,omitempty"`
	IsTopicMessage    bool            `json:"is_topic_message,omitempty"`

	IsAutomaticForward bool           `json:"is_automatic_forward,omitempty"`
	MessageThreadID    int64          `json:"message_thread_id,omitempty"`
	ForwardOrigin      *MessageOrigin `json:"forward_origin,omitempty"`

	ForwardFrom          *User           `json:"forward_from,omitempty"`
	ForwardFromChat      *Chat           `json:"forward_from_chat,omitempty"`
	ForwardFromMessageID int64           `json:"forward_from_message_id,omitempty"`
	ForwardSignature     string          `json:"forward_signature,omitempty"`
	ForwardSenderName    string          `json:"forward_sender_name,omitempty"`
	ForwardDate          int64           `json:"forward_date,omitempty"`
	MediaGroupID         string          `json:"media_group_id,omitempty"`
	RichMessage          json.RawMessage `json:"rich_message,omitempty"`
	ViaBot               *User           `json:"via_bot,omitempty"`
	Poll                 *Poll           `json:"poll,omitempty"`
	Widget               *Widget         `json:"widget,omitempty"`
	Dice                 *Dice           `json:"dice,omitempty"`
	EditDate             int64           `json:"edit_date,omitempty"`

	SuccessfulPayment *SuccessfulPayment `json:"successful_payment,omitempty"`
	RefundedPayment   *RefundedPayment   `json:"refunded_payment,omitempty"`

	Album     []*Message `json:"-"`
	AlbumDone bool       `json:"-"`
	SplitDone bool       `json:"-"`

	GuestQueryID string `json:"guest_query_id,omitempty"`
}

func (m *Message) Origin() *MessageOrigin {
	switch {
	case m == nil:
		return nil
	case m.ForwardOrigin != nil:
		return m.ForwardOrigin
	case m.ForwardFromChat != nil:
		o := &MessageOrigin{Type: "chat", Date: m.ForwardDate, AuthorSignature: m.ForwardSignature, SenderChat: m.ForwardFromChat}
		if m.ForwardFromChat.Type == "channel" {
			o.Type, o.Chat, o.SenderChat, o.MessageID = "channel", m.ForwardFromChat, nil, m.ForwardFromMessageID
		}
		return o
	case m.ForwardFrom != nil:
		return &MessageOrigin{Type: "user", Date: m.ForwardDate, SenderUser: m.ForwardFrom}
	case m.ForwardSenderName != "":
		return &MessageOrigin{Type: "hidden_user", Date: m.ForwardDate, SenderUserName: m.ForwardSenderName}
	}
	return nil
}

func (m *Message) HasMedia() bool {
	return len(m.Photo) > 0 || m.Document != nil || m.Voice != nil || m.Audio != nil || m.Video != nil ||
		m.VideoNote != nil || m.Animation != nil || m.Sticker != nil || m.Location != nil || m.Contact != nil
}

func (m *Message) Body() string {
	switch {
	case m.Text != "":
		return m.Text
	case m.Caption != "":
		return m.Caption
	case len(m.RichMessage) > 0:
		return m.RichText()
	}
	return ""
}

func (m *Message) BodyEntities() []MessageEntity {
	if m.Text != "" {
		return m.Entities
	}
	return m.CaptionEntities
}

func (m *Message) LargestPhoto(maxSide int) *PhotoSize {
	var best *PhotoSize
	for i := range m.Photo {
		p := &m.Photo[i]
		if max(p.Width, p.Height) > maxSide {
			if best == nil {
				best = p
			}
			continue
		}
		if best == nil || max(best.Width, best.Height) > maxSide || p.Width*p.Height > best.Width*best.Height {
			best = p
		}
	}
	return best
}

type ChatMember struct {
	Status   string `json:"status"`
	User     User   `json:"user"`
	IsMember bool   `json:"is_member,omitempty"`

	CanBeEdited         bool   `json:"can_be_edited,omitempty"`
	IsAnonymous         bool   `json:"is_anonymous,omitempty"`
	CanManageChat       bool   `json:"can_manage_chat,omitempty"`
	CanDeleteMessages   bool   `json:"can_delete_messages,omitempty"`
	CanManageVideoChats bool   `json:"can_manage_video_chats,omitempty"`
	CanRestrictMembers  bool   `json:"can_restrict_members,omitempty"`
	CanPromoteMembers   bool   `json:"can_promote_members,omitempty"`
	CanPostStories      bool   `json:"can_post_stories,omitempty"`
	CanEditStories      bool   `json:"can_edit_stories,omitempty"`
	CanDeleteStories    bool   `json:"can_delete_stories,omitempty"`
	CustomTitle         string `json:"custom_title,omitempty"`

	CanChangeInfo   bool `json:"can_change_info,omitempty"`
	CanInviteUsers  bool `json:"can_invite_users,omitempty"`
	CanPinMessages  bool `json:"can_pin_messages,omitempty"`
	CanManageTopics bool `json:"can_manage_topics,omitempty"`

	UntilDate             int64 `json:"until_date,omitempty"`
	CanSendMessage        bool  `json:"can_send_messages,omitempty"`
	CanSendAudios         bool  `json:"can_send_audios,omitempty"`
	CanSendDocuments      bool  `json:"can_send_documents,omitempty"`
	CanSendPhotos         bool  `json:"can_send_photos,omitempty"`
	CanSendVideos         bool  `json:"can_send_videos,omitempty"`
	CanSendVideoNotes     bool  `json:"can_send_video_notes,omitempty"`
	CanSendVoiceNotes     bool  `json:"can_send_voice_notes,omitempty"`
	CanSendPolls          bool  `json:"can_send_polls,omitempty"`
	CanSendOtherMessages  bool  `json:"can_send_other_messages,omitempty"`
	CanAddWebPagePreviews bool  `json:"can_add_web_page_previews,omitempty"`
}

func (m *ChatMember) Joined() bool {
	switch m.Status {
	case "creator", "administrator", "member":
		return true
	case "restricted":
		return m.IsMember
	}
	return false
}

type ChatMemberUpdated struct {
	Chat          Chat       `json:"chat"`
	From          User       `json:"from"`
	Date          int64      `json:"date"`
	OldChatMember ChatMember `json:"old_chat_member"`
	NewChatMember ChatMember `json:"new_chat_member"`
}

type MessageGenerationStopped struct {
	Chat            Chat      `json:"chat"`
	MessageThreadID int64     `json:"message_thread_id,omitempty"`
	DraftID         FlexInt64 `json:"draft_id"`
}

type FlexInt64 int64

func (f *FlexInt64) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" || s == "" {
		*f = 0
		return nil
	}
	if len(s) >= 2 && s[0] == '"' {
		var str string
		if err := json.Unmarshal(b, &str); err != nil {
			return err
		}
		s = strings.TrimSpace(str)
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		*f = 0
		return nil
	}
	*f = FlexInt64(n)
	return nil
}

type CallbackQuery struct {
	ID      string   `json:"id"`
	From    User     `json:"from"`
	Message *Message `json:"message,omitempty"`
	Data    string   `json:"data"`
}

type Update struct {
	UpdateID                 int64                     `json:"update_id"`
	Message                  *Message                  `json:"message,omitempty"`
	CallbackQuery            *CallbackQuery            `json:"callback_query,omitempty"`
	MyChatMember             *ChatMemberUpdated        `json:"my_chat_member,omitempty"`
	StoppedMessageGeneration *MessageGenerationStopped `json:"stopped_message_generation,omitempty"`
	PollAnswer               *PollAnswer               `json:"poll_answer,omitempty"`
	GuestMessage             *Message                  `json:"guest_message,omitempty"`
	EditedMessage            *Message                  `json:"edited_message,omitempty"`
	PreCheckoutQuery         *PreCheckoutQuery         `json:"pre_checkout_query,omitempty"`
	MessageReaction          *MessageReactionUpdated   `json:"message_reaction,omitempty"`
	WidgetQuery              *WidgetQuery              `json:"widget_query,omitempty"`
	SharedWidgetQuery        *SharedWidgetQuery        `json:"shared_widget_query,omitempty"`
}

type Widget struct {
	ID         string `json:"id,omitempty"`
	Width      int    `json:"width,omitempty"`
	Height     int    `json:"height,omitempty"`
	HTML       string `json:"html"`
	PublicData string `json:"public_data,omitempty"`
	ChatData   string `json:"chat_data,omitempty"`
}

type WidgetQueryType struct {
	Type string `json:"type"`
}

type WidgetQuery struct {
	ID              string          `json:"id"`
	From            User            `json:"from"`
	Chat            *Chat           `json:"chat,omitempty"`
	MessageID       int64           `json:"message_id,omitempty"`
	InlineMessageID string          `json:"inline_message_id,omitempty"`
	ChatInstance    string          `json:"chat_instance"`
	ChatType        string          `json:"chat_type,omitempty"`
	Type            WidgetQueryType `json:"type"`
	Data            string          `json:"data"`
	WidgetID        string          `json:"widget_id,omitempty"`
}

type SharedWidgetQuery struct {
	ID       string          `json:"id"`
	From     User            `json:"from"`
	Type     WidgetQueryType `json:"type"`
	Data     string          `json:"data"`
	WidgetID string          `json:"widget_id"`
}

type MessageReactionUpdated struct {
	Chat        Chat           `json:"chat"`
	MessageID   int64          `json:"message_id"`
	User        *User          `json:"user,omitempty"`
	Date        int64          `json:"date"`
	OldReaction []ReactionType `json:"old_reaction"`
	NewReaction []ReactionType `json:"new_reaction"`
}

func (r *MessageReactionUpdated) Added() []string {
	had := map[string]bool{}
	for _, x := range r.OldReaction {
		had[x.Emoji] = true
	}
	var out []string
	for _, x := range r.NewReaction {
		if x.Type == "emoji" && !had[x.Emoji] {
			out = append(out, x.Emoji)
		}
	}
	return out
}

type PreCheckoutQuery struct {
	ID             string `json:"id"`
	From           User   `json:"from"`
	Currency       string `json:"currency"`
	TotalAmount    int    `json:"total_amount"`
	InvoicePayload string `json:"invoice_payload"`
}

type SuccessfulPayment struct {
	Currency                string `json:"currency"`
	TotalAmount             int    `json:"total_amount"`
	InvoicePayload          string `json:"invoice_payload"`
	TelegramPaymentChargeID string `json:"telegram_payment_charge_id"`
	ProviderPaymentChargeID string `json:"provider_payment_charge_id,omitempty"`
}

type RefundedPayment struct {
	Currency                string `json:"currency"`
	TotalAmount             int    `json:"total_amount"`
	InvoicePayload          string `json:"invoice_payload"`
	TelegramPaymentChargeID string `json:"telegram_payment_charge_id"`
}

type Birthdate struct {
	Day   int `json:"day"`
	Month int `json:"month"`
	Year  int `json:"year,omitempty"`
}

type ChatPhoto struct {
	SmallFileID string `json:"small_file_id"`
	BigFileID   string `json:"big_file_id"`
}

type ChatFullInfo struct {
	ID           int64            `json:"id"`
	Type         string           `json:"type"`
	Title        string           `json:"title,omitempty"`
	Username     string           `json:"username,omitempty"`
	FirstName    string           `json:"first_name,omitempty"`
	LastName     string           `json:"last_name,omitempty"`
	Bio          string           `json:"bio,omitempty"`
	Description  string           `json:"description,omitempty"`
	InviteLink   string           `json:"invite_link,omitempty"`
	Birthdate    *Birthdate       `json:"birthdate,omitempty"`
	PersonalChat *Chat            `json:"personal_chat,omitempty"`
	Photo        *ChatPhoto       `json:"photo,omitempty"`
	Permissions  *ChatPermissions `json:"permissions,omitempty"`
}

type UserProfilePhotos struct {
	TotalCount int           `json:"total_count"`
	Photos     [][]PhotoSize `json:"photos"`
}

type TelegramFile struct {
	FileID   string `json:"file_id"`
	FilePath string `json:"file_path"`
	FileSize int64  `json:"file_size"`
}

const (
	StyleDanger  = "danger"
	StyleSuccess = "success"
	StylePrimary = "primary"
)

type InlineKeyboardButton struct {
	Text              string `json:"text"`
	CallbackData      string `json:"callback_data,omitempty"`
	URL               string `json:"url,omitempty"`
	Style             string `json:"style,omitempty"`
	IconCustomEmojiID string `json:"icon_custom_emoji_id,omitempty"`
}

type InlineKeyboardMarkup struct {
	InlineKeyboard [][]InlineKeyboardButton `json:"inline_keyboard"`
}

type BotCommand struct {
	Command     string `json:"command"`
	Description string `json:"description"`
}

type BotCommandScope struct {
	Type   string `json:"type"`
	ChatID int64  `json:"chat_id,omitempty"`
}

type InputRichBlock struct {
	Type     string              `json:"type"`
	Text     any                 `json:"text,omitempty"`
	Size     int                 `json:"size,omitempty"`
	Language string              `json:"language,omitempty"`
	Summary  any                 `json:"summary,omitempty"`
	Blocks   []InputRichBlock    `json:"blocks,omitempty"`
	Items    []InputRichListItem `json:"items,omitempty"`
	IsOpen   bool                `json:"is_open,omitempty"`
}

type InputRichListItem struct {
	Blocks      []InputRichBlock `json:"blocks"`
	HasCheckbox bool             `json:"has_checkbox,omitempty"`
	IsChecked   bool             `json:"is_checked,omitempty"`
}

type RichTextSpan struct {
	Type            string `json:"type"`
	Text            any    `json:"text,omitempty"`
	CustomEmojiID   string `json:"custom_emoji_id,omitempty"`
	AlternativeText string `json:"alternative_text,omitempty"`
}

type RichMessage struct {
	Blocks              []InputRichBlock `json:"blocks,omitempty"`
	Markdown            string           `json:"markdown,omitempty"`
	HTML                string           `json:"html,omitempty"`
	IsRTL               bool             `json:"is_rtl,omitempty"`
	SkipEntityDetection bool             `json:"skip_entity_detection,omitempty"`
	Media               []RichMediaSpec  `json:"media,omitempty"`
}

type RichMediaSpec struct {
	ID    string            `json:"id"`
	Media map[string]string `json:"media"`
}

type ReactionType struct {
	Type  string `json:"type"`
	Emoji string `json:"emoji"`
}

type ReplyParameters struct {
	MessageID                int64 `json:"message_id"`
	AllowSendingWithoutReply bool  `json:"allow_sending_without_reply,omitempty"`
}

type SendOptions struct {
	ReplyTo        int64
	Markup         *InlineKeyboardMarkup
	Silent         bool
	ProtectContent bool
	EffectID       string

	ThreadID int64
}

func (o SendOptions) InTopic(m *Message) SendOptions {
	if m != nil && m.IsTopicMessage && m.MessageThreadID != 0 {
		o.ThreadID = m.MessageThreadID
	}
	return o
}

func (o SendOptions) apply(p map[string]any) {
	if o.ThreadID != 0 {
		p["message_thread_id"] = o.ThreadID
	}
	if o.ReplyTo != 0 {
		p["reply_parameters"] = ReplyParameters{MessageID: o.ReplyTo, AllowSendingWithoutReply: true}
	}
	if o.Markup != nil {
		p["reply_markup"] = o.Markup
	}
	if o.Silent {
		p["disable_notification"] = true
	}
	if o.ProtectContent {
		p["protect_content"] = true
	}
	if o.EffectID != "" {
		p["message_effect_id"] = o.EffectID
	}
}

const TelegramServiceID = 777000

func (m *Message) FromChannel() bool {
	return m.IsAutomaticForward || (m.From != nil && m.From.ID == TelegramServiceID) || (m.SenderChat != nil && m.SenderChat.Type == "channel")
}

type ChatPermissions struct {
	CanSendMessages       bool `json:"can_send_messages"`
	CanSendAudios         bool `json:"can_send_audios"`
	CanSendDocuments      bool `json:"can_send_documents"`
	CanSendPhotos         bool `json:"can_send_photos"`
	CanSendVideos         bool `json:"can_send_videos"`
	CanSendVideoNotes     bool `json:"can_send_video_notes"`
	CanSendVoiceNotes     bool `json:"can_send_voice_notes"`
	CanSendPolls          bool `json:"can_send_polls"`
	CanSendOtherMessages  bool `json:"can_send_other_messages"`
	CanAddWebPagePreviews bool `json:"can_add_web_page_previews"`
	CanChangeInfo         bool `json:"can_change_info"`
	CanInviteUsers        bool `json:"can_invite_users"`
	CanPinMessages        bool `json:"can_pin_messages"`
	CanManageTopics       bool `json:"can_manage_topics"`
}
