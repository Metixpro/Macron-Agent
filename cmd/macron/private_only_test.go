package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	sched "github.com/Metixpro/macron-agent/pkg/sched"
	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/telegram"
)

func TestPersonalBotIgnoresAllNonPrivateUpdates(t *testing.T) {
	h := personalHarness(t)
	for _, kind := range []string{"group", "supergroup", "channel"} {
		chat := telegram.Chat{ID: -100777, Type: kind}
		m := &telegram.Message{MessageID: 9, Chat: chat, From: &telegram.User{ID: ownerID}, Text: "/settings"}
		for _, u := range []telegram.Update{
			{Message: m}, {EditedMessage: m}, {GuestMessage: m},
			{CallbackQuery: &telegram.CallbackQuery{ID: "q", From: telegram.User{ID: ownerID}, Message: m, Data: "up:go:v99.0.0"}},
			{MessageReaction: &telegram.MessageReactionUpdated{Chat: chat, User: m.From, MessageID: 9}},
			{StoppedMessageGeneration: &telegram.MessageGenerationStopped{Chat: chat, DraftID: 9}},
		} {
			h.dispatch(u)
		}
	}
	if len(h.tg.calls) != 0 || h.llm.count() != 0 {
		t.Fatal("non-private update caused Telegram or model activity")
	}
}

func TestPersonalBotDisablesOldGroupSchedules(t *testing.T) {
	h := personalHarness(t)
	newUser(h, ownerID)
	now := time.Now()
	id, err := h.st.AddSchedule(bg, store.Schedule{UserID: ownerID, ChatID: -100777, ChatType: "supergroup", Kind: store.ScheduleReminder, Body: "old reminder", Title: "old", Rule: "every:60m", NextRun: now})
	if err != nil {
		t.Fatal(err)
	}
	rule, err := sched.Parse("every:60m")
	if err != nil {
		t.Fatal(err)
	}
	if err = h.bot.fireSchedule(store.Schedule{ID: id, UserID: ownerID, ChatID: -100777, ChatType: "supergroup", Kind: store.ScheduleReminder}, rule, now); err == nil {
		t.Fatal("group reminder accepted")
	}
	if len(h.tg.calls) != 0 || h.llm.count() != 0 {
		t.Fatal("old group schedule produced activity")
	}
}

func TestPersonalMenusHaveNoGroupInstructions(t *testing.T) {
	h := personalHarness(t)
	for _, l := range []lang{fa, en} {
		v := h.bot.viewHome(bg, ownerID)
		if strings.Contains(v.html, "Privacy") || strings.Contains(v.html, "BotFather") {
			t.Fatal("group instructions remain")
		}
		v = h.bot.viewAgent(l)
		if strings.Contains(fmt.Sprint(v.kb), "a:stt") {
			t.Fatal("speech button remains")
		}
	}
	if err := h.bot.registerCommands(bg); err != nil {
		t.Fatal(err)
	}
	privateMenu := false
	for _, c := range h.tg.of("setMyCommands") {
		scope := fmt.Sprint(c.body["scope"])
		commands := fmt.Sprint(c.body["commands"])
		if strings.Contains(scope, "all_group_chats") && commands != "[]" && commands != "<nil>" {
			t.Fatal("group commands remain")
		}
		if strings.Contains(scope, "all_private_chats") && strings.Contains(commands, "start") {
			privateMenu = true
		}
	}
	if !privateMenu {
		t.Fatal("private command menu missing")
	}
}

func TestSpeechRecognitionIsRemoved(t *testing.T) {
	h := personalHarness(t)
	if h.bot.app.Registry.Has("transcribe_audio") {
		t.Fatal("speech tool remains")
	}
	m := private(ownerID, "")
	m.Voice = &telegram.Voice{FileID: "voice", Duration: 4}
	_, _, name := mediaInfo(m)
	if err := h.bot.app.Machine.WriteFile(sessionFor(ownerID), name, []byte("opaque audio")); err != nil {
		t.Fatal(err)
	}
	if err := h.bot.app.Machine.WriteFile(sessionFor(ownerID), "."+name+".transcript", []byte("OLD_TRANSCRIPT_MUST_NOT_BE_READ")); err != nil {
		t.Fatal(err)
	}
	a, err := h.bot.attach(bg, sessionFor(ownerID), m, false, "The user sent", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(a.about, "Speech recognition is not supported") || strings.Contains(a.about, "admin panel") || strings.Contains(a.about, "OLD_TRANSCRIPT") {
		t.Fatalf("voice must stay an opaque media file: %s", a.about)
	}
	h.click(ownerID, ownerID, 9, "a:stt:set")
	if h.bot.pending(ownerID) != nil {
		t.Fatal("old speech button started a form")
	}
}

func TestPersonalBotIgnoresOldGroupPollVotes(t *testing.T) {
	h := personalHarness(t)
	if err := h.st.SavePoll(bg, store.Poll{PollID: "old-group", UserID: ownerID, ChatID: -100777, ChatType: "supergroup", MessageID: 9, Question: "old", Options: []string{"yes", "no"}}); err != nil {
		t.Fatal(err)
	}
	h.dispatch(telegram.Update{PollAnswer: &telegram.PollAnswer{PollID: "old-group", User: &telegram.User{ID: ownerID}, OptionIDs: []int{0}}})
	if len(h.tg.calls) != 0 || h.llm.count() != 0 {
		t.Fatal("old group vote caused activity")
	}
}
