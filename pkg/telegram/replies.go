package telegram

import (
	"encoding/json"
	"strings"
)

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

type Story struct {
	Chat Chat  `json:"chat"`
	ID   int64 `json:"id"`
}

func (m *Message) RepliedInChat() *Message {
	if m == nil || m.ReplyToMessage == nil || m.ReplyToMessage.ForumTopicCreated != nil {
		return nil
	}
	return m.ReplyToMessage
}

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
		r.MessageID = -m.MessageID
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
		r.From = &User{FirstName: o.SenderUserName}
	}
	if m.Quote != nil {
		r.Text = m.Quote.Text
	}
	return r
}

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

const maxRichDepth = 6

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
		default:
			line(richText(b.Text, 0))
		}
	}
}

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
