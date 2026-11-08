package telegram

import (
	"encoding/json"
	"strings"
)

// What a message replies to. Telegram shows it in several ways:
//
//   - reply_to_message: a message of the same chat and thread. The bot's own
//     answers are rich messages, which have no text: what they say is in
//     their blocks (read by RichText), and their pictures and files too.
//   - external_reply: a message of another chat or topic, or one the bot
//     cannot see (guest mode). It carries who wrote it and its media, never
//     its text; the part the user quoted (quote) is all there is of that.
//   - In a forum, every message of a topic "replies" to the topic's first
//     message (a service message) unless it replies to something else: that
//     is not a reply.

// ExternalReplyInfo is a message replied to from another chat or topic.
type ExternalReplyInfo struct {
	Origin    *MessageOrigin `json:"origin,omitempty"`
	Chat      *Chat          `json:"chat,omitempty"`
	MessageID int64          `json:"message_id,omitempty"`
	Photo     []PhotoSize    `json:"photo,omitempty"`
	Document  *Document      `json:"document,omitempty"`
	Video     *Video         `json:"video,omitempty"`
	Animation *Video         `json:"animation,omitempty"`
	Audio     *Audio         `json:"audio,omitempty"`
	Voice     *Voice         `json:"voice,omitempty"`
	VideoNote *Video         `json:"video_note,omitempty"`
	Sticker   *Sticker       `json:"sticker,omitempty"`
	Location  *Location      `json:"location,omitempty"`
	Contact   *Contact       `json:"contact,omitempty"`
	Poll      *Poll          `json:"poll,omitempty"`
}

// Story is a story a message replies to.
type Story struct {
	Chat Chat  `json:"chat"`
	ID   int64 `json:"id"`
}

// RepliedInChat is the message of this chat that m replies to: nil when it
// replies to nothing, or only belongs to a forum topic (its first message).
func (m *Message) RepliedInChat() *Message {
	if m == nil || m.ReplyToMessage == nil || m.ReplyToMessage.ForumTopicCreated != nil {
		return nil
	}
	return m.ReplyToMessage
}

// Replied is the message m replies to, as far as the update shows it: a copy
// of reply_to_message with a rich message's media where the other messages
// keep theirs, or external_reply with the quoted part as its text. nil when
// m replies to nothing.
func (m *Message) Replied() *Message {
	if r := m.RepliedInChat(); r != nil {
		cp := *r
		cp.fillRichMedia()
		return &cp
	}
	if m == nil || m.ExternalReply == nil {
		return nil
	}
	e := m.ExternalReply
	r := &Message{MessageID: e.MessageID, Photo: e.Photo, Document: e.Document, Video: e.Video, Animation: e.Animation,
		Audio: e.Audio, Voice: e.Voice, VideoNote: e.VideoNote, Sticker: e.Sticker, Location: e.Location, Contact: e.Contact, Poll: e.Poll}
	if e.Chat != nil {
		r.Chat = *e.Chat
	}
	if r.MessageID == 0 {
		r.MessageID = -m.MessageID // its own id is unknown: one that names its files apart from the reply's
	}
	switch o := e.Origin; {
	case o == nil:
	case o.SenderUser != nil:
		r.From = o.SenderUser
	case o.Chat != nil:
		r.SenderChat = o.Chat
	case o.SenderChat != nil:
		r.SenderChat = o.SenderChat
	case o.SenderUserName != "":
		r.From = &User{FirstName: o.SenderUserName} // a hidden account: a name, no id
	}
	if m.Quote != nil {
		r.Text = m.Quote.Text
	}
	return r
}

// ---------------------------------------------------------------- received rich messages

// richBlock is one block of a received rich message (RichBlock); only the
// fields that hold text or media are read.
type richBlock struct {
	Type       string            `json:"type"`
	Text       json.RawMessage   `json:"text,omitempty"`
	Size       int               `json:"size,omitempty"`
	Language   string            `json:"language,omitempty"`
	Expression string            `json:"expression,omitempty"`
	Blocks     []json.RawMessage `json:"blocks,omitempty"`
	Credit     json.RawMessage   `json:"credit,omitempty"`
	Caption    json.RawMessage   `json:"caption,omitempty"`
	Summary    json.RawMessage   `json:"summary,omitempty"`
	Items      []struct {
		Label       string            `json:"label,omitempty"`
		Blocks      []json.RawMessage `json:"blocks,omitempty"`
		HasCheckbox bool              `json:"has_checkbox,omitempty"`
		IsChecked   bool              `json:"is_checked,omitempty"`
	} `json:"items,omitempty"`
	Cells [][]struct {
		Text json.RawMessage `json:"text,omitempty"`
	} `json:"cells,omitempty"`
	Buttons []struct {
		Text json.RawMessage `json:"text,omitempty"`
	} `json:"buttons,omitempty"`
	Location  *Location   `json:"location,omitempty"`
	Animation *Video      `json:"animation,omitempty"`
	Audio     *Audio      `json:"audio,omitempty"`
	Document  *Document   `json:"document,omitempty"`
	Photo     []PhotoSize `json:"photo,omitempty"`
	Video     *Video      `json:"video,omitempty"`
	VoiceNote *Voice      `json:"voice_note,omitempty"`
}

// richBlocks decodes the blocks of a received rich message (nil when there is none).
func richBlocks(raw json.RawMessage) []richBlock {
	if len(raw) == 0 {
		return nil
	}
	var doc struct {
		Blocks []json.RawMessage `json:"blocks"`
	}
	if json.Unmarshal(raw, &doc) != nil {
		return nil
	}
	return decodeBlocks(doc.Blocks)
}

func decodeBlocks(raws []json.RawMessage) []richBlock {
	out := make([]richBlock, 0, len(raws))
	for _, r := range raws {
		var b richBlock
		if json.Unmarshal(r, &b) == nil {
			out = append(out, b)
		}
	}
	return out
}

// maxRichDepth bounds nested blocks (quotes in lists in details…).
const maxRichDepth = 6

// RichText is what a received rich message says, as plain text with a few
// marks (# headings, • lists, > quotes, | tables); "" for other messages.
func (m *Message) RichText() string {
	if m == nil {
		return ""
	}
	var sb strings.Builder
	writeBlocks(&sb, richBlocks(m.RichMessage), "", 0)
	return strings.TrimSpace(sb.String())
}

func writeBlocks(sb *strings.Builder, blocks []richBlock, prefix string, depth int) {
	if depth > maxRichDepth {
		return
	}
	line := func(s string) {
		if s = strings.TrimSpace(s); s != "" {
			for _, l := range strings.Split(s, "\n") {
				sb.WriteString(prefix + l + "\n")
			}
		}
	}
	for _, b := range blocks {
		switch b.Type {
		case "heading":
			line(strings.Repeat("#", min(max(b.Size, 1), 6)) + " " + richText(b.Text, 0))
		case "pre":
			line("```" + b.Language + "\n" + richText(b.Text, 0) + "\n```")
		case "divider":
			line("———")
		case "mathematical_expression":
			line("$" + b.Expression + "$")
		case "list":
			for _, it := range b.Items {
				mark := "•"
				switch {
				case it.HasCheckbox && it.IsChecked:
					mark = "☑"
				case it.HasCheckbox:
					mark = "☐"
				case strings.TrimSpace(it.Label) != "":
					mark = strings.TrimSpace(it.Label)
				}
				var item strings.Builder
				writeBlocks(&item, decodeBlocks(it.Blocks), "", depth+1)
				line(mark + " " + strings.ReplaceAll(strings.TrimSpace(item.String()), "\n", " "))
			}
		case "blockquote":
			writeBlocks(sb, decodeBlocks(b.Blocks), prefix+"> ", depth+1)
			if c := richText(b.Credit, 0); c != "" {
				line("> — " + c)
			}
		case "expandable_blockquote", "pullquote":
			line("> " + strings.ReplaceAll(richText(b.Text, 0), "\n", "\n> "))
			if c := richText(b.Credit, 0); c != "" {
				line("> — " + c)
			}
		case "collage", "slideshow":
			writeBlocks(sb, decodeBlocks(b.Blocks), prefix, depth+1)
			line(captionText(b.Caption))
		case "table":
			for _, row := range b.Cells {
				cells := make([]string, len(row))
				for i, c := range row {
					cells[i] = strings.ReplaceAll(richText(c.Text, 0), "\n", " ")
				}
				line("| " + strings.Join(cells, " | ") + " |")
			}
			line(captionText(b.Caption))
		case "details":
			line(richText(b.Summary, 0))
			writeBlocks(sb, decodeBlocks(b.Blocks), prefix+"  ", depth+1)
		case "map":
			if b.Location != nil {
				line("[map]")
			}
			line(captionText(b.Caption))
		case "buttons":
			for _, btn := range b.Buttons {
				if t := richText(btn.Text, 0); t != "" {
					line("[" + t + "]")
				}
			}
		case "photo", "video", "animation", "audio", "document", "voice_note":
			name := b.Type
			if b.Document != nil && b.Document.FileName != "" {
				name += ": " + b.Document.FileName
			} else if b.Audio != nil && b.Audio.Title != "" {
				name += ": " + b.Audio.Title
			}
			line("[" + name + "]")
			line(captionText(b.Caption))
		case "anchor":
		default: // paragraph, footer and any block added later that has text
			line(richText(b.Text, 0))
		}
	}
}

// richText reads a RichText: a string, a list of them, or a styled piece.
func richText(raw json.RawMessage, depth int) string {
	raw = json.RawMessage(strings.TrimSpace(string(raw)))
	if len(raw) == 0 || depth > 32 {
		return ""
	}
	switch raw[0] {
	case '"':
		var s string
		_ = json.Unmarshal(raw, &s)
		return s
	case '[':
		var parts []json.RawMessage
		if json.Unmarshal(raw, &parts) != nil {
			return ""
		}
		var sb strings.Builder
		for _, p := range parts {
			sb.WriteString(richText(p, depth+1))
		}
		return sb.String()
	case '{':
		var o struct {
			Type            string          `json:"type"`
			Text            json.RawMessage `json:"text,omitempty"`
			AlternativeText string          `json:"alternative_text,omitempty"`
			Expression      string          `json:"expression,omitempty"`
			URL             string          `json:"url,omitempty"`
			Button          *struct {
				Text json.RawMessage `json:"text,omitempty"`
			} `json:"button,omitempty"`
		}
		if json.Unmarshal(raw, &o) != nil {
			return ""
		}
		switch o.Type {
		case "custom_emoji":
			return o.AlternativeText
		case "mathematical_expression":
			return "$" + o.Expression + "$"
		case "button":
			if o.Button != nil {
				return "[" + richText(o.Button.Text, depth+1) + "]"
			}
			return ""
		case "anchor":
			return ""
		case "url":
			t := richText(o.Text, depth+1)
			if o.URL != "" && o.URL != t {
				return t + " (" + o.URL + ")"
			}
			return t
		}
		return richText(o.Text, depth+1)
	}
	return ""
}

// captionText reads a block's caption: a RichBlockCaption (text and credit)
// or, for tables, a RichText.
func captionText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var c struct {
		Type   string          `json:"type"`
		Text   json.RawMessage `json:"text"`
		Credit json.RawMessage `json:"credit"`
	}
	if raw[0] != '{' || json.Unmarshal(raw, &c) != nil || c.Type != "" {
		return richText(raw, 0)
	}
	t := richText(c.Text, 0)
	if cr := richText(c.Credit, 0); cr != "" {
		t = strings.TrimSpace(t + " — " + cr)
	}
	return t
}

// fillRichMedia gives a rich message the first picture, video, song, voice
// and file of its blocks, where messages keep theirs, so they can be opened
// like any other message's.
func (m *Message) fillRichMedia() {
	if len(m.RichMessage) == 0 || m.HasMedia() {
		return
	}
	var walk func(blocks []richBlock, depth int)
	walk = func(blocks []richBlock, depth int) {
		if depth > maxRichDepth {
			return
		}
		for _, b := range blocks {
			switch {
			case len(b.Photo) > 0 && len(m.Photo) == 0:
				m.Photo = b.Photo
			case b.Video != nil && m.Video == nil:
				m.Video = b.Video
			case b.Animation != nil && m.Animation == nil:
				m.Animation = b.Animation
			case b.Audio != nil && m.Audio == nil:
				m.Audio = b.Audio
			case b.VoiceNote != nil && m.Voice == nil:
				m.Voice = b.VoiceNote
			case b.Document != nil && m.Document == nil:
				m.Document = b.Document
			}
			walk(decodeBlocks(b.Blocks), depth+1)
		}
	}
	walk(richBlocks(m.RichMessage), 0)
}
