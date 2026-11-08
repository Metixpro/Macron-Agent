package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

// Through a local Bot API server the tools work up to 2000 MB: they say so,
// and downloads, splits and archives follow it.
func TestSendLimitFollowsTheBotAPIServer(t *testing.T) {
	old := [...]int64{dlSendLimit, dlFitBytes, telegramMaxBytes, telegramPartBytes, maxDownloadBytes}
	defer func() {
		dlSendLimit, dlFitBytes, telegramMaxBytes, telegramPartBytes, maxDownloadBytes = old[0], old[1], old[2], old[3], old[4]
	}()
	SetSendLimit(2000 << 20)
	if SendLimit() != 2000<<20 || dlFitBytes >= dlSendLimit || telegramPartBytes >= telegramMaxBytes || maxDownloadBytes != 2000<<20 {
		t.Fatalf("limits: %d %d %d %d", dlSendLimit, dlFitBytes, telegramMaxBytes, telegramPartBytes)
	}
	r := newRegistry(t, false, nil)
	raw, _ := json.Marshal(r.GetTools())
	if s := string(raw); strings.Contains(s, "50 MB") || !strings.Contains(s, "2000 MB") {
		t.Errorf("the tools speak of the new limit")
	}
}
