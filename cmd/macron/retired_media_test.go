package main

import (
	"fmt"
	"strings"
	"testing"
)

func TestRetiredMediaToolsAndPanelsStayRemoved(t *testing.T) {
	h := newHarness(t)
	for _, name := range []string{"generate_image", "edit_image", "generate_song", "generate_music_synth"} {
		if h.bot.app.Registry.Has(name) {
			t.Errorf("retired tool remains registered: %s", name)
		}
	}
	for _, l := range []lang{fa, en} {
		panel := h.bot.viewAgent(l)
		all := panel.html + fmt.Sprint(panel.kb)
		for _, data := range []string{"a:ig", "a:ie", "a:mu"} {
			if strings.Contains(all, data) {
				t.Errorf("retired panel advertised: %s", data)
			}
		}
	}
	for _, data := range []string{"a:ig:key", "a:ie:key", "a:mu:key"} {
		h.click(ownerID, ownerID, 99, data)
		if h.bot.pending(ownerID) != nil {
			t.Fatalf("retired callback opened an input: %s", data)
		}
	}
	for _, name := range []string{"search_image", "generate_chart", "generate_qr", "media_convert", "text_to_speech"} {
		if !h.bot.app.Registry.Has(name) {
			t.Errorf("unrelated capability was removed: %s", name)
		}
	}
}

func TestRetiredCommandAliasesAreNotHandled(t *testing.T) {
	h := newHarness(t)
	for _, command := range []string{"/lang", "/zaban", "/reset", "/clear", "/cookie", "/schedules", "/stop", "/admin", "/panel", "/fast", "/deep"} {
		h.tg.reset()
		h.send(private(ownerID, command))
		h.wantText(textUnknownCommand)
	}
	if h.llm.count() != 0 {
		t.Fatal("retired commands were sent to the model")
	}
}
