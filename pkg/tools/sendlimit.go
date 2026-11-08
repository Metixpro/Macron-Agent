package tools

import (
	"fmt"
	"time"
)

// The largest file the chat takes decides when a download is split or
// re-encoded, how big the parts of an archive are and what the tools say
// about it: Telegram's 50 MB, or 2000 MB through a local Bot API server.
// SetSendLimit is called before the tools are made.

// SetSendLimit sets the largest file the chat takes.
func SetSendLimit(n int64) {
	if n <= 0 {
		return
	}
	dlSendLimit = n
	dlFitBytes = n - n/16 // room for the container's overhead
	telegramMaxBytes = n - n/50
	telegramPartBytes = n - n/10
	maxDownloadBytes = max(100<<20, n)
}

// SendLimit is the largest file the chat takes.
func SendLimit() int64 { return dlSendLimit }

// limitText is the limit as the tools say it: "50 MB", "2000 MB".
func limitText() string { return fmt.Sprintf("%d MB", dlSendLimit>>20) }

// partMaxMB is the largest part a file is split into.
func partMaxMB() int { return max(int(dlSendLimit>>20)-1, 5) }

// compressTime is how long re-encoding a file may take: a few minutes, more
// for a big one (about a minute per 100 MB).
func compressTime(size int64) time.Duration {
	return min(max(6*time.Minute, time.Duration(size/(100<<20))*time.Minute), 45*time.Minute)
}
