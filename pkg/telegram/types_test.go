package telegram

import (
	"encoding/json"
	"testing"
)

func TestForwardOrigins(t *testing.T) {
	var m Message
	if m.Origin() != nil || (*MessageOrigin)(nil).Describe() != "" || (*MessageOrigin)(nil).ChatID() != 0 {
		t.Fatal("not forwarded")
	}
	cases := []struct {
		json, want string
		chat, user int64
	}{
		{`{"forward_origin":{"type":"channel","date":1,"chat":{"id":-1001234,"type":"channel","title":"News","username":"news"},"message_id":77,"author_signature":"Sara"}}`,
			"News · @news · id: -1001234 · msg_id: 77 · signed: Sara", -1001234, 0},
		{`{"forward_origin":{"type":"user","date":1,"sender_user":{"id":42,"first_name":"Ali","last_name":"Rezaei","username":"ali"}}}`,
			"Ali Rezaei · @ali · id: 42", 0, 42},
		{`{"forward_origin":{"type":"hidden_user","date":1,"sender_user_name":"Mina"}}`,
			"Mina (hidden user, ID not provided by Telegram)", 0, 0},
		{`{"forward_origin":{"type":"chat","date":1,"sender_chat":{"id":-1005,"type":"supergroup","title":"Team"}}}`,
			"Team · id: -1005 · sent anonymously on behalf of the group", -1005, 0},

		{`{"forward_from_chat":{"id":-1009,"type":"channel","title":"Old"},"forward_from_message_id":5,"forward_date":1}`,
			"Old · id: -1009 · msg_id: 5", -1009, 0},
		{`{"forward_from":{"id":7,"first_name":"Bob","is_bot":true},"forward_date":1}`, "Bob · bot · id: 7", 0, 7},
		{`{"forward_sender_name":"Anon","forward_date":1}`, "Anon (hidden user, ID not provided by Telegram)", 0, 0},
	}
	for _, c := range cases {
		var m Message
		if err := json.Unmarshal([]byte(c.json), &m); err != nil {
			t.Fatal(err)
		}
		o := m.Origin()
		if got := o.Describe(); got != c.want || o.ChatID() != c.chat || o.UserID() != c.user {
			t.Errorf("%s:\n got %q chat %d user %d\nwant %q chat %d user %d", c.json, got, o.ChatID(), o.UserID(), c.want, c.chat, c.user)
		}
	}
}
