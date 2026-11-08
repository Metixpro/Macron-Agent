package telegram

import (
	"encoding/json"
	"testing"
)

// A field that does not fit (a value of a new or changed type) never loses
// the update: the message is read without it, its reply too.
func TestDecodeUpdatesLeavesOutWhatDoesNotFit(t *testing.T) {
	raw := []json.RawMessage{
		[]byte(`{"update_id":1,"message":{"message_id":5,"chat":{"id":-100,"type":"supergroup"},"from":{"id":7,"first_name":"Ali"},"text":"مکرون نظرت چیه",
			"reply_to_message":{"message_id":4,"chat":{"id":-100,"type":"supergroup"},"from":{"id":8,"first_name":"Sara"},"text":"فردا بارون میاد","photo":{"odd":true},"date":"yesterday"},
			"quote":"not an object"}}`),
		[]byte(`{"update_id":2,"message":{"message_id":6,"chat":{"id":1,"type":"private"},"text":"ok"}}`),
		[]byte(`{"update_id":3,"callback_query":{"id":"q","from":{"id":7,"first_name":"A"},"data":"x","message":{"message_id":9,"chat":{"id":1,"type":"private"},"entities":"bad"}}}`),
	}
	ups := decodeUpdates(raw)
	if len(ups) != 3 {
		t.Fatalf("updates: %d", len(ups))
	}
	m := ups[0].Message
	if m == nil || m.Text != "مکرون نظرت چیه" || m.ReplyToMessage == nil || m.ReplyToMessage.Text != "فردا بارون میاد" ||
		m.ReplyToMessage.From.FirstName != "Sara" || m.Quote != nil {
		t.Fatalf("the message and its reply are read without the bad fields: %+v", m)
	}
	if ups[1].Message == nil || ups[1].Message.Text != "ok" {
		t.Error("a good update is read as it is")
	}
	if q := ups[2].CallbackQuery; q == nil || q.Data != "x" || q.Message == nil || q.Message.MessageID != 9 {
		t.Errorf("a button's message too: %+v", q)
	}
}
