package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Metixpro/macron-agent/pkg/agent"
	"github.com/Metixpro/macron-agent/pkg/app"
	"github.com/Metixpro/macron-agent/pkg/brain"
	"github.com/Metixpro/macron-agent/pkg/config"
	"github.com/Metixpro/macron-agent/pkg/guard"
	"github.com/Metixpro/macron-agent/pkg/memory"
	"github.com/Metixpro/macron-agent/pkg/providers"
	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/telegram"
)

const ownerID = 1000

// ---- fake Telegram ----------------------------------------------------------

type tgCall struct {
	method string
	body   map[string]any
}

type fakeTG struct {
	mu      sync.Mutex
	calls   []tgCall
	nextID  int64
	members map[string]string // "chat:user" → status
	fail    map[string]int    // method → HTTP status to return
	desc    map[string]string
	files   map[string][]byte // file_id → content
	blocked map[int64]bool    // users that blocked the bot
	sets    map[string]string // custom emoji set → its stickers (JSON)

	refuseRichFiles bool // sendRichMessage with uploaded files fails (older servers)
	// reject returns a description to answer a call with 400 Bad Request.
	reject func(method string, body map[string]any) string
}

func newFakeTG() *fakeTG {
	return &fakeTG{nextID: 500, members: map[string]string{}, fail: map[string]int{}, desc: map[string]string{},
		files: map[string][]byte{}, blocked: map[int64]bool{}, sets: map[string]string{}}
}

func (f *fakeTG) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/file/") {
		id := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		f.mu.Lock()
		data, ok := f.files[id]
		f.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write(data)
		return
	}
	method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	body := map[string]any{}
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		_ = r.ParseMultipartForm(32 << 20)
		for k, v := range r.MultipartForm.Value {
			body[k] = v[0]
			if k == "rich_message" || k == "reply_parameters" { // JSON objects, like in a JSON request
				var obj map[string]any
				if json.Unmarshal([]byte(v[0]), &obj) == nil {
					body[k] = obj
				}
			}
		}
		for k, fh := range r.MultipartForm.File {
			body[k+"_name"] = fh[0].Filename
		}
	} else {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	f.mu.Lock()
	f.calls = append(f.calls, tgCall{method, body})
	f.nextID++
	id := f.nextID
	status, failing := f.fail[method]
	desc := f.desc[method]
	chatID, _ := body["chat_id"].(float64)
	blocked := f.blocked[int64(chatID)]
	refuseFiles := f.refuseRichFiles
	reject := f.reject
	f.mu.Unlock()
	if reject != nil {
		if desc := reject(method, body); desc != "" {
			w.WriteHeader(400)
			fmt.Fprintf(w, `{"ok":false,"error_code":400,"description":%q}`, desc)
			return
		}
	}

	if blocked && (method == "copyMessage" || method == "forwardMessage") {
		w.WriteHeader(403)
		fmt.Fprint(w, `{"ok":false,"error_code":403,"description":"Forbidden: bot was blocked by the user"}`)
		return
	}
	if method == "sendRichMessage" && refuseFiles && strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/") {
		w.WriteHeader(400)
		fmt.Fprint(w, `{"ok":false,"error_code":400,"description":"Bad Request: wrong file identifier/HTTP URL specified"}`)
		return
	}
	if failing {
		w.WriteHeader(status)
		fmt.Fprintf(w, `{"ok":false,"error_code":%d,"description":%q}`, status, desc)
		return
	}
	switch method {
	case "getChatMember":
		key := fmt.Sprintf("%d:%d", int64(body["chat_id"].(float64)), int64(body["user_id"].(float64)))
		f.mu.Lock()
		st := f.members[key]
		f.mu.Unlock()
		if strings.HasPrefix(st, "{") { // the whole member, as Telegram sends it
			fmt.Fprintf(w, `{"ok":true,"result":%s}`, st)
			return
		}
		if st == "" {
			st = "left"
		}
		rights := ""
		switch st {
		case "administrator", "creator": // "administrator-limited" is an admin without rights
			rights = `,"can_restrict_members":true,"can_delete_messages":true,"can_pin_messages":true,"can_change_info":true,"can_invite_users":true,"can_promote_members":true`
		case "administrator-limited":
			st = "administrator"
		}
		fmt.Fprintf(w, `{"ok":true,"result":{"status":%q,"user":{"id":%d,"first_name":"x"}%s}}`, st, int64(body["user_id"].(float64)), rights)
	case "getChat":
		if v, ok := body["chat_id"].(string); ok && strings.HasPrefix(v, "@") {
			fmt.Fprintf(w, `{"ok":true,"result":{"id":-1001234,"type":"channel","title":"کانال ما","username":%q}}`, strings.TrimPrefix(v, "@"))
			return
		}
		fmt.Fprint(w, `{"ok":true,"result":{"id":1,"type":"private","first_name":"Ali","bio":"عاشق Go و قهوه ☕"}}`)
	case "getFile":
		fmt.Fprintf(w, `{"ok":true,"result":{"file_id":%q,"file_path":"files/%s","file_size":10}}`, body["file_id"], body["file_id"])
	case "sendPoll":
		opts, _ := json.Marshal(body["options"])
		fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%d,"chat":{"id":%d,"type":"private"},"date":1,"poll":{"id":"poll-%d","question":%q,"options":%s,"is_anonymous":false}}}`,
			id, int64(chatID), id, body["question"], opts)
	case "sendDice":
		fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%d,"chat":{"id":%d,"type":"private"},"date":1,"dice":{"emoji":%q,"value":6}}}`, id, int64(chatID), body["emoji"])
	case "sendMediaGroup":
		fmt.Fprintf(w, `{"ok":true,"result":[{"message_id":%d,"chat":{"id":%d,"type":"private"},"date":1}]}`, id, int64(chatID))
	case "createNewStickerSet":
		f.mu.Lock()
		f.sets[fmt.Sprint(body["name"])] = fmt.Sprint(body["stickers"])
		f.mu.Unlock()
		fmt.Fprint(w, `{"ok":true,"result":true}`)
	case "getStickerSet":
		f.mu.Lock()
		raw, ok := f.sets[fmt.Sprint(body["name"])]
		f.mu.Unlock()
		if !ok {
			w.WriteHeader(400)
			fmt.Fprint(w, `{"ok":false,"error_code":400,"description":"Bad Request: STICKERSET_INVALID"}`)
			return
		}
		var in []struct {
			Emoji []string `json:"emoji_list"`
		}
		_ = json.Unmarshal([]byte(raw), &in)
		var out []map[string]any
		for i, s := range in {
			out = append(out, map[string]any{"file_id": fmt.Sprint("s", i), "type": "custom_emoji", "emoji": s.Emoji[0] + "️",
				"custom_emoji_id": fmt.Sprint(7000 + i), "is_animated": true})
		}
		res, _ := json.Marshal(map[string]any{"name": body["name"], "title": "t", "sticker_type": "custom_emoji", "stickers": out})
		fmt.Fprintf(w, `{"ok":true,"result":%s}`, res)
	case "getChatMemberCount":
		fmt.Fprint(w, `{"ok":true,"result":57}`)
	case "getUserProfilePhotos":
		fmt.Fprint(w, `{"ok":true,"result":{"total_count":1,"photos":[[{"file_id":"avatar","file_unique_id":"a","width":640,"height":640}]]}}`)
	case "copyMessage", "forwardMessage":
		fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%d,"chat":{"id":1,"type":"private"}}}`, id)
	case "createInvoiceLink":
		fmt.Fprintf(w, `{"ok":true,"result":"https://t.me/$invoice-%d"}`, id)
	case "createChatInviteLink":
		fmt.Fprint(w, `{"ok":true,"result":{"invite_link":"https://t.me/+abc123"}}`)
	case "banChatMember", "unbanChatMember", "restrictChatMember", "promoteChatMember", "setChatAdministratorCustomTitle", "setChatPermissions",
		"deleteMessages", "pinChatMessage", "unpinChatMessage", "unpinAllChatMessages", "setChatTitle", "setChatDescription",
		"sendRichMessageDraft", "setMessageReaction", "sendChatAction", "deleteMessage", "answerCallbackQuery", "setMyCommands",
		"deleteWebhook", "editMessageReplyMarkup", "leaveChat", "stopPoll", "answerPreCheckoutQuery", "refundStarPayment":
		fmt.Fprint(w, `{"ok":true,"result":true}`)
	case "answerGuestQuery": // SentGuestMessage: the reply can be edited later
		fmt.Fprintf(w, `{"ok":true,"result":{"inline_message_id":"im-%v"}}`, body["guest_query_id"])
	default:
		if body["inline_message_id"] != nil && strings.HasPrefix(method, "edit") {
			fmt.Fprint(w, `{"ok":true,"result":true}`) // a message sent through the bot: Telegram answers True
			return
		}
		chat := fmt.Sprint(body["chat_id"])
		if f, ok := body["chat_id"].(float64); ok {
			chat = fmt.Sprintf("%d", int64(f)) // %v would print -1.009999e+06
		}
		if mid, ok := body["message_id"].(float64); ok && strings.HasPrefix(method, "edit") {
			id = int64(mid) // an edited message keeps its id
		}
		fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%d,"chat":{"id":%s,"type":"private"},"date":1}}`, id, chat)
	}
}

func (f *fakeTG) of(method string) []tgCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []tgCall
	for _, c := range f.calls {
		if c.method == method {
			out = append(out, c)
		}
	}
	return out
}

func (f *fakeTG) refuseFiles(on bool) {
	f.mu.Lock()
	f.refuseRichFiles = on
	f.mu.Unlock()
}

func (f *fakeTG) setReject(fn func(method string, body map[string]any) string) {
	f.mu.Lock()
	f.reject = fn
	f.mu.Unlock()
}

func (f *fakeTG) reset() {
	f.mu.Lock()
	f.calls = nil
	f.mu.Unlock()
}

// texts returns the visible text of everything the bot sent (classic and rich).
func (f *fakeTG) texts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		switch c.method {
		case "sendMessage", "editMessageText":
			if t, ok := c.body["text"].(string); ok {
				out = append(out, telegram.UnmarkRTL(t)) // the visible text, without direction marks
			}
			if rm, ok := c.body["rich_message"].(map[string]any); ok {
				out = append(out, fmt.Sprint(rm["markdown"], rm["html"]))
			}
		case "sendRichMessage", "editRichMessage":
			if rm, ok := c.body["rich_message"].(map[string]any); ok {
				out = append(out, fmt.Sprint(rm["markdown"], rm["html"]))
			}
		}
	}
	return out
}

func (f *fakeTG) allText() string { return strings.Join(f.texts(), "\n---\n") }

// ---- fake LLM ---------------------------------------------------------------

type fakeLLM struct {
	mu       sync.Mutex
	requests []map[string]any
	reply    func(req map[string]any, n int) (map[string]any, int)
	cached   int // prompt tokens each answer says it read from the cache
	// triage answers a model triage's prompt (triage.go) apart from the
	// requests above, so a test's counts stay those of the work itself.
	triage    func(req map[string]any) (map[string]any, int)
	triageLog []string // the triage prompts it was sent
}

func (f *fakeLLM) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/audio/") { // no speech to text: the tests that need it bring their own
		http.NotFound(w, r)
		return
	}
	var req map[string]any
	_ = json.NewDecoder(r.Body).Decode(&req)
	f.mu.Lock()
	if tr := f.triage; tr != nil && strings.Contains(llmText(req), "You sort requests for an assistant") {
		f.triageLog = append(f.triageLog, llmText(req))
		f.mu.Unlock()
		msg, status := tr(req)
		if status != 0 {
			http.Error(w, "triage exploded", status)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"model": "m", "choices": []any{map[string]any{"message": msg}},
			"usage": map[string]any{"prompt_tokens": 300, "completion_tokens": 40, "total_tokens": 340}})
		return
	}
	n := len(f.requests)
	f.requests = append(f.requests, req)
	cached := f.cached
	f.mu.Unlock()
	msg, status := f.reply(req, n)
	if status != 0 {
		body := "upstream exploded"
		if e, ok := msg["error"].(string); ok {
			body = e // the provider's own error text
		}
		http.Error(w, body, status)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"model": "m", "choices": []any{map[string]any{"message": msg}},
		"usage": map[string]any{"prompt_tokens": 100, "completion_tokens": 20, "total_tokens": 120,
			"prompt_tokens_details": map[string]any{"cached_tokens": cached}}})
}

func (f *fakeLLM) last() map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.requests[len(f.requests)-1]
}

func (f *fakeLLM) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func say(text string) func(map[string]any, int) (map[string]any, int) {
	return func(map[string]any, int) (map[string]any, int) {
		return map[string]any{"role": "assistant", "content": text}, 0
	}
}

func toolCall(name, args string) map[string]any {
	return map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"id": "c1", "type": "function",
		"function": map[string]any{"name": name, "arguments": args}}}}
}

// ---- harness ----------------------------------------------------------------

type harness struct {
	t   *testing.T
	bot *Bot
	tg  *fakeTG
	llm *fakeLLM
	st  *store.Store
}

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	loc, _ := time.LoadLocation("Asia/Tehran")
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "test.db"), store.Options{Location: loc, SecretKey: "test"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func (h *harness) lastModel() string {
	m, _ := h.llm.last()["model"].(string)
	return m
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	st := openTestStore(t)
	tg, fl := newFakeTG(), &fakeLLM{reply: say("سلام! چطوری؟")}
	tgSrv, llmSrv := httptest.NewServer(tg), httptest.NewServer(fl)
	t.Cleanup(tgSrv.Close)
	t.Cleanup(llmSrv.Close)

	ctx := context.Background()
	_ = st.SetSetting(ctx, store.SettingAutoContinue, "-1") // a fixed budget; the tests that need extensions turn them on
	_ = st.SetSetting(ctx, store.SettingLiveMode, liveStop) // the draft with its stop button; the live message's tests choose it
	if _, err := st.AddProvider(ctx, store.Provider{Name: "main", BaseURL: llmSrv.URL, APIKey: "sk-provider-secret-1234", Model: "m",
		Priority: 10, Enabled: true, Vision: true}); err != nil {
		t.Fatal(err)
	}
	prov, err := providers.New(ctx, st, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{WorkspaceDir: t.TempDir(), MaxSteps: 4, RunTimeout: 30 * time.Second,
		SkillsDir: t.TempDir()}
	loc, _ := time.LoadLocation("Asia/Tehran")
	br, err := brain.Open(t.TempDir(), agent.DefaultSoul)
	if err != nil {
		t.Fatal(err)
	}
	a, err := app.NewWith(ctx, cfg, agent.ChannelTelegram, prov, agent.Options{Location: loc, Soul: br.Soul})
	if err != nil {
		t.Fatal(err)
	}
	b := &Bot{
		api: telegram.NewBotWithBase("42:TOKEN-SECRET", tgSrv.URL), app: a, st: st, prov: prov,
		mem: memory.New(st, prov, func() int { return historyBudget(st, agent.DefaultContextWindow) }),
		me:  telegram.User{ID: 42, Username: "WazeBot", FirstName: "Waze"}, loc: loc, owner: map[int64]bool{ownerID: true},
		gate: guard.NewGate(10), flood: guard.NewRateLimiter(1000, time.Minute), runTimeout: 30 * time.Second,
		jobCtx: context.Background(), started: time.Now(), brain: br,
	}
	b.app.Agent.Teachings = b.teachings // as in main
	b.readsAll.Store(true)              // privacy mode off: groupsight_test.go turns it on
	b.api.OnSent = b.noteSent           // as in main
	b.reloadEmoji(ctx)
	a.Search.Treg.SetTokenSource(tregToken(st, cfg.TregToken))     // as in main
	a.Registry.SetImageEditKey(imageEditKey(st, cfg.ImageEditKey)) // as in main
	a.Registry.SetImageKey(imageGenKey(st, cfg.ImageKey))          // as in main
	a.Registry.SetMusicKey(musicKey(st, cfg.MusicKey))             // as in main
	b.cookies = newCookiePool(st, "")                              // as in main
	a.Registry.SetCookiePool(b.cookies)
	a.Registry.SetUserCookies(b.cookies.user)
	a.Registry.SetTranscriber(sttSources(st, cfg))
	a.Registry.SetSkillWriter(b.sessionAdmin) // as in main
	// Tests never reach the real web: an empty search engine, no reader.
	noWeb := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("<html></html>")) }))
	t.Cleanup(noWeb.Close)
	a.Search.SetEndpoints(noWeb.URL+"/lite/", noWeb.URL+"/html/")
	a.Search.ReaderURL = ""
	return &harness{t: t, bot: b, tg: tg, llm: fl, st: st}
}

func private(userID int64, text string) *telegram.Message {
	return &telegram.Message{MessageID: 77, From: &telegram.User{ID: userID, FirstName: "Ali", Username: "ali_dev", LanguageCode: "fa"},
		Chat: telegram.Chat{ID: userID, Type: "private"}, Text: text, Date: time.Now().Unix()}
}

func (h *harness) send(m *telegram.Message) {
	h.bot.handleMessage(m)
	h.bot.wg.Wait()
}

func (h *harness) click(userID, chatID, msgID int64, data string) {
	h.bot.handleCallback(&telegram.CallbackQuery{ID: "q", From: telegram.User{ID: userID}, Data: data,
		Message: &telegram.Message{MessageID: msgID, Chat: telegram.Chat{ID: chatID, Type: "private"}}})
	h.bot.wg.Wait()
}

// clickAsync presses a button without waiting for background work it starts.
func (h *harness) clickAsync(userID, chatID, msgID int64, data string) {
	h.bot.handleCallback(&telegram.CallbackQuery{ID: "q", From: telegram.User{ID: userID}, Data: data,
		Message: &telegram.Message{MessageID: msgID, Chat: telegram.Chat{ID: chatID, Type: "private"}}})
}

func (h *harness) wantText(sub string) {
	h.t.Helper()
	if !strings.Contains(h.tg.allText(), sub) {
		h.t.Fatalf("no message contains %q; sent:\n%s", sub, h.tg.allText())
	}
}

func (h *harness) noText(sub string) {
	h.t.Helper()
	if strings.Contains(h.tg.allText(), sub) {
		h.t.Fatalf("a message unexpectedly contains %q:\n%s", sub, h.tg.allText())
	}
}

func llmText(req map[string]any) string {
	b, _ := json.Marshal(req["messages"])
	return string(b)
}
