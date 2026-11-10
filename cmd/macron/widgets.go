package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/agent"
	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/machine"
	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/telegram"
	"github.com/Metixpro/macron-agent/pkg/textutil"
)

const (
	toolSendWidget   = "send_widget"
	toolUpdateWidget = "update_widget"

	widgetAnswerWait = 14 * time.Second
	widgetRunLimit   = 11 * time.Second
	widgetLogDir     = ".widgets"
	maxWidgetLog     = 16 << 10
	maxHandlerBytes  = 256 << 10
)

var handlerRunners = map[string][]string{
	".py": {"python3"}, ".js": {"node"}, ".mjs": {"node"}, ".cjs": {"node"}, ".sh": {"bash"}, ".bash": {"bash"},
	".rb": {"ruby"}, ".php": {"php"},
}

var handlerLanguages = map[string]string{
	".py": "python", ".js": "node", ".mjs": "node", ".cjs": "node", ".rb": "ruby", ".php": "php",
}

func (b *Bot) widgetTools(u *store.User, m *telegram.Message) []agent.LocalTool {
	session := sessionFor(u.ID)
	str := func(args map[string]any, k string) string {
		v, _ := args[k].(string)
		return v
	}
	return []agent.LocalTool{{
		Def: llm.Tool{Type: "function", Function: llm.ToolDef{
			Name: toolSendWidget,
			Description: "Show an interactive mini app inside the chat (dashboard, form, calculator, game, live status): one HTML document " +
				"with inline CSS and JS, no network or external files (images only as data: URLs or SVG), ≤32 KB and ≤4 KB zlib-compressed. " +
				"Read the telegram-widgets skill before your first one. Write the HTML to a workspace file and give it here. A handler script " +
				"answers the widget's TelegramWidget.request(data) on this server, so taps need no model call.",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{
				"file":        map[string]any{"type": "string", "description": "Workspace .html file"},
				"html":        map[string]any{"type": "string", "description": "Or the HTML itself"},
				"handler":     map[string]any{"type": "string", "description": "Workspace script (.py, .js, .sh) that answers requests"},
				"title":       map[string]any{"type": "string", "description": "Short name"},
				"chat_data":   map[string]any{"type": "string", "description": "Initial TelegramWidget.chatData (≤1 KB)"},
				"public_data": map[string]any{"type": "string", "description": "Initial TelegramWidget.publicData (≤1 KB)"},
				"width":       map[string]any{"type": "integer", "description": "Logical px; omit to fill the bubble"},
				"height":      map[string]any{"type": "integer", "description": "50–600; omit for automatic"},
				"caption":     map[string]any{"type": "string"},
			}},
		}},
		Handler: func(ctx context.Context, args map[string]any) (agent.ToolOutput, error) {
			return agent.ToolOutput{Text: b.sendWidget(ctx, u, m, session, args, str)}, nil
		},
	}, {
		Def: llm.Tool{Type: "function", Function: llm.ToolDef{
			Name: toolUpdateWidget,
			Description: "Push new data into a widget you sent: it gets chatDataChanged or publicDataChanged and reads TelegramWidget.chatData " +
				"or publicData. ≤1 KB each; \"\" clears; an omitted field stays.",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{
				"widget":      map[string]any{"type": "integer", "description": "The number send_widget gave"},
				"chat_data":   map[string]any{"type": "string"},
				"public_data": map[string]any{"type": "string"},
			}, "required": []string{"widget"}},
		}},
		Handler: func(ctx context.Context, args map[string]any) (agent.ToolOutput, error) {
			return agent.ToolOutput{Text: b.updateWidget(ctx, u, args)}, nil
		},
	}}
}

func optionalText(args map[string]any, k string) *string {
	v, ok := args[k]
	if !ok || v == nil {
		return nil
	}
	s, ok := v.(string)
	if !ok {
		raw, _ := json.Marshal(v)
		s = string(raw)
	}
	return &s
}

func widgetError(err error) string {
	var api *telegram.APIError
	if !errors.As(err, &api) {
		return "error: the widget was not sent: " + err.Error()
	}
	switch d := api.Description; {
	case strings.Contains(d, "WIDGET_TOO_LARGE"):
		return "error: Telegram says the widget is too large (≤32768 bytes, ≤4096 bytes zlib-compressed): shorten it and send it again"
	case strings.Contains(d, "WIDGET_HTML_INVALID"):
		return "error: Telegram refused the HTML (it may hold a NUL character): " + d
	case strings.Contains(d, "WIDGET_DATA"):
		return "error: Telegram refused the widget data (≤1024 bytes each, valid UTF-8): " + d
	case strings.Contains(d, "method not found") || api.Code == 404:
		return "error: this Telegram Bot API server does not offer widgets yet (Bot API 10.4 is needed)"
	}
	return "error: the widget was not sent: " + api.Description
}

func (b *Bot) sendWidget(ctx context.Context, u *store.User, m *telegram.Message, session string, args map[string]any,
	str func(map[string]any, string) string) string {
	file, html := strings.TrimSpace(str(args, "file")), str(args, "html")
	if file != "" {
		data, cut, err := b.app.Machine.ReadFile(session, file, telegram.MaxWidgetHTML+1)
		if err != nil {
			return "error: " + file + ": " + fileProblem(err)
		}
		if cut {
			return fmt.Sprintf("error: %s is over %d bytes, the most a widget may be: shorten it", file, telegram.MaxWidgetHTML)
		}
		html = string(data)
	}
	if strings.TrimSpace(html) == "" {
		return "error: give the widget's HTML as a workspace file (file) or as html"
	}
	if strings.ContainsRune(html, 0) {
		return "error: the HTML holds a NUL character, which Telegram refuses"
	}
	if raw, packed := telegram.WidgetSize(html); raw > telegram.MaxWidgetHTML || packed > telegram.MaxWidgetPacked {
		return fmt.Sprintf("error: the widget is %d bytes, %d compressed; the limits are %d and %d. Make it smaller (shorter CSS and JS, "+
			"no comments, fewer repeated elements, data built by JS loops) and send it again", raw, packed, telegram.MaxWidgetHTML, telegram.MaxWidgetPacked)
	}
	publicData, chatData := str(args, "public_data"), str(args, "chat_data")
	if len(publicData) > telegram.MaxWidgetData || len(chatData) > telegram.MaxWidgetData {
		return fmt.Sprintf("error: chat_data and public_data are at most %d bytes each", telegram.MaxWidgetData)
	}
	handler := strings.TrimSpace(str(args, "handler"))
	if handler != "" {
		if note := b.checkHandler(ctx, session, handler); note != "" {
			return note
		}
	}
	spec := telegram.WidgetSpec{HTML: html, PublicData: publicData, ChatData: chatData, AllowQueries: handler != "",
		Width: max(intArgument(args["width"]), 0), Height: intArgument(args["height"]),
		Caption: textutil.TruncateRunes(strings.TrimSpace(str(args, "caption")), 1024, "…")}
	if spec.Height > 0 {
		spec.Height = min(max(spec.Height, 50), 600)
	}
	sent, err := b.api.SendWidget(ctx, m.Chat.ID, spec, telegram.SendOptions{})
	if err != nil {
		return widgetError(err)
	}
	title := textutil.TruncateRunes(textutil.OneLine(strings.TrimSpace(str(args, "title"))), 60, "…")
	if title == "" && file != "" {
		title = strings.TrimSuffix(path.Base(file), path.Ext(file))
	}
	w := store.Widget{UserID: u.ID, ChatID: m.Chat.ID, MessageID: sent.MessageID, Title: title, File: file, Handler: handler,
		Session: session, PublicData: publicData, ChatData: chatData}
	if sent.Widget != nil {
		w.SharedID = sent.Widget.ID
	}
	id, err := b.st.AddWidget(ctx, w)
	if err != nil {
		log.Printf("[user %d] saving widget: %v", u.ID, err)
		return "The widget was sent, but it could not be recorded, so update_widget and its handler will not work for it: " + err.Error()
	}
	out := fmt.Sprintf("Sent widget #%d «%s» to the chat; it is live there. Change its data with update_widget widget=%d.", id, title, id)
	if handler != "" {
		out += fmt.Sprintf(" Each TelegramWidget.request runs %s; its failures are logged in %s/%d.log.", handler, widgetLogDir, id)
	} else {
		out += " It has no handler, so TelegramWidget.request is off; keep its logic in its own JS."
	}
	return out + " To change its look, edit the HTML and send it again."
}

func fileProblem(err error) string {
	var pe *fs.PathError
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "there is no such file"
	case errors.As(err, &pe):
		return pe.Err.Error()
	}
	return err.Error()
}

func intArgument(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(x))
		return n
	}
	return 0
}

func (b *Bot) checkHandler(ctx context.Context, session, handler string) string {
	code, cut, err := b.app.Machine.ReadFile(session, handler, maxHandlerBytes)
	if err != nil {
		return "error: handler " + handler + ": " + fileProblem(err) + " (write the script first, then send the widget)"
	}
	if cut {
		return "error: the handler is larger than 256 KB"
	}
	g := b.app.Registry.Gate
	if g == nil {
		return ""
	}
	ext := strings.ToLower(path.Ext(handler))
	if lang, ok := handlerLanguages[ext]; ok {
		if verdict := g.CheckCode(ctx, session, lang, string(code)); !verdict.Allow {
			return verdict.Message
		}
		return ""
	}
	if verdict := g.Check(ctx, session, string(code)); !verdict.Allow {
		return verdict.Message
	}
	return ""
}

func (b *Bot) updateWidget(ctx context.Context, u *store.User, args map[string]any) string {
	id := int64(intArgument(args["widget"]))
	w, err := b.st.WidgetByID(ctx, u.ID, id)
	if err != nil {
		return fmt.Sprintf("error: there is no widget #%d", id)
	}
	publicData, chatData := optionalText(args, "public_data"), optionalText(args, "chat_data")
	if publicData == nil && chatData == nil {
		return "error: give chat_data or public_data"
	}
	if publicData != nil && len(*publicData) > telegram.MaxWidgetData || chatData != nil && len(*chatData) > telegram.MaxWidgetData {
		return fmt.Sprintf("error: chat_data and public_data are at most %d bytes each", telegram.MaxWidgetData)
	}
	if err := b.pushWidgetData(ctx, w, publicData, chatData); err != nil {
		return "error: the widget was not updated: " + err.Error()
	}
	return fmt.Sprintf("Widget #%d «%s» updated.", w.ID, w.Title)
}

func (b *Bot) pushWidgetData(ctx context.Context, w *store.Widget, publicData, chatData *string) error {
	if w.SharedID != "" && publicData != nil {
		if err := b.api.EditSharedWidgetData(ctx, w.SharedID, *publicData, 0); err != nil {
			return err
		}
		if chatData == nil {
			return b.st.SetWidgetData(ctx, w.ID, publicData, nil)
		}
		publicData = nil
	}
	if err := b.api.EditWidgetData(ctx, w.ChatID, w.MessageID, publicData, chatData); err != nil {
		return err
	}
	return b.st.SetWidgetData(ctx, w.ID, publicData, chatData)
}

func (b *Bot) handleWidgetQuery(q *telegram.WidgetQuery) {
	ctx, cancel := b.ctx(widgetAnswerWait)
	defer cancel()
	answer := ""
	defer func() {
		if err := b.api.AnswerWidgetQuery(ctx, q.ID, answer); err != nil {
			log.Printf("[user %d] answering a widget: %v", q.From.ID, err)
		}
	}()
	if q.Type.Type != "data" {
		return
	}
	var w *store.Widget
	var err error
	switch {
	case q.WidgetID != "":
		w, err = b.st.WidgetByShared(ctx, q.WidgetID)
	case q.Chat != nil:
		w, err = b.st.WidgetByMessage(ctx, q.Chat.ID, q.MessageID)
	default:
		return
	}
	if err != nil || w.UserID != q.From.ID {
		return
	}
	answer = b.answerWidget(ctx, w, &q.From, q.Data)
}

func (b *Bot) handleSharedWidgetQuery(q *telegram.SharedWidgetQuery) {
	b.handleWidgetQuery(&telegram.WidgetQuery{ID: q.ID, From: q.From, Type: q.Type, Data: q.Data, WidgetID: q.WidgetID})
}

func widgetFailure(msg string) string {
	raw, _ := json.Marshal(map[string]string{"error": textutil.TruncateBytes(msg, telegram.MaxWidgetAnswer-20, "…")})
	return string(raw)
}

func (b *Bot) answerWidget(ctx context.Context, w *store.Widget, from *telegram.User, data string) string {
	if w.Handler == "" {
		return ""
	}
	_ = b.st.TouchWidget(ctx, w.ID)
	ext := strings.ToLower(path.Ext(w.Handler))
	argv := append([]string(nil), handlerRunners[ext]...)
	if len(argv) == 0 {
		target := w.Handler
		if !strings.HasPrefix(target, "/") && !strings.HasPrefix(target, "./") {
			target = "./" + target
		}
		argv = []string{target}
	} else {
		argv = append(argv, w.Handler)
	}
	argv = append(argv, data)
	payload, _ := json.Marshal(map[string]any{"data": data, "widget": w.ID, "title": w.Title, "chat_data": w.ChatData,
		"public_data": w.PublicData, "user": map[string]any{"id": from.ID, "name": from.FirstName}})
	res := b.app.Machine.Run(ctx, w.Session, machine.RunOptions{Args: argv, Stdin: payload, Timeout: widgetRunLimit,
		Env: map[string]string{"MACRON_WIDGET": strconv.FormatInt(w.ID, 10), "MACRON_WIDGET_DATA": data}})
	if res.ExitCode != 0 || res.Error != "" {
		why := strings.TrimSpace(res.Error + " " + res.Stderr)
		if why == "" {
			why = fmt.Sprintf("exit %d", res.ExitCode)
		}
		b.logWidget(w, data, fmt.Sprintf("exit %d: %s\n%s", res.ExitCode, res.Error, textutil.HeadTail(res.Stderr+"\n"+res.Stdout, 4000)))
		return widgetFailure(textutil.OneLine(textutil.TruncateRunes(why, 300, "…")))
	}
	answer, publicData, chatData := handlerOutput(strings.TrimSpace(res.Stdout))
	if len(answer) > telegram.MaxWidgetAnswer {
		b.logWidget(w, data, fmt.Sprintf("the answer was %d bytes; at most %d are sent", len(answer), telegram.MaxWidgetAnswer))
		return widgetFailure(fmt.Sprintf("the answer is %d bytes; at most %d fit", len(answer), telegram.MaxWidgetAnswer))
	}
	if publicData != nil && len(*publicData) > telegram.MaxWidgetData || chatData != nil && len(*chatData) > telegram.MaxWidgetData {
		b.logWidget(w, data, "chat_data or public_data was over 1024 bytes and was not applied")
		publicData, chatData = nil, nil
	}
	if publicData != nil || chatData != nil {
		if err := b.pushWidgetData(ctx, w, publicData, chatData); err != nil {
			b.logWidget(w, data, "updating its data failed: "+err.Error())
		}
	}
	return answer
}

func handlerOutput(out string) (answer string, publicData, chatData *string) {
	if !strings.HasPrefix(out, "{") {
		return out, nil, nil
	}
	var env map[string]json.RawMessage
	if json.Unmarshal([]byte(out), &env) != nil {
		return out, nil, nil
	}
	_, a := env["answer"]
	_, p := env["public_data"]
	_, c := env["chat_data"]
	if !a && !p && !c {
		return out, nil, nil
	}
	text := func(raw json.RawMessage) *string {
		var s string
		if json.Unmarshal(raw, &s) != nil {
			s = string(raw)
		}
		return &s
	}
	if a {
		answer = *text(env["answer"])
	}
	if p {
		publicData = text(env["public_data"])
	}
	if c {
		chatData = text(env["chat_data"])
	}
	return answer, publicData, chatData
}

func (b *Bot) logWidget(w *store.Widget, data, what string) {
	name := fmt.Sprintf("%s/%d.log", widgetLogDir, w.ID)
	old, _, _ := b.app.Machine.ReadFile(w.Session, name, maxWidgetLog)
	entry := fmt.Sprintf("%s request %q: %s\n", time.Now().Format("2006-01-02 15:04:05"), data, strings.TrimSpace(what))
	text := string(old) + entry
	if len(text) > maxWidgetLog {
		text = text[len(text)-maxWidgetLog:]
	}
	if err := b.app.Machine.WriteFile(w.Session, name, []byte(text)); err != nil {
		log.Printf("widget %d log: %v", w.ID, err)
	}
	log.Printf("[user %d] widget #%d: %s", w.UserID, w.ID, textutil.TruncateRunes(textutil.OneLine(what), 200, "…"))
}

func (b *Bot) widgetAbout(ctx context.Context, r *telegram.Message) string {
	if r == nil || r.Widget == nil {
		return ""
	}
	w, err := b.st.WidgetByMessage(ctx, r.Chat.ID, r.MessageID)
	if err != nil {
		return "It is an interactive widget (one this bot did not record)."
	}
	line := fmt.Sprintf("It is widget #%d «%s» that you sent", w.ID, w.Title)
	if w.File != "" {
		line += ", made from " + w.File
	}
	if w.Handler != "" {
		line += ", answered by " + w.Handler
	}
	return line + ". Change its data with update_widget; to change its look, edit the HTML and send it again with send_widget."
}
