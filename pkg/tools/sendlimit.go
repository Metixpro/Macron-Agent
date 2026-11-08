package tools

import (
	"fmt"
	"time"
)

func SetSendLimit(n int64) {
	if n <= 0 {
		return
	}
	dlSendLimit = n
	dlFitBytes = n - n/16
	telegramMaxBytes = n - n/50
	telegramPartBytes = n - n/10
	maxDownloadBytes = max(100<<20, n)
}

func SendLimit() int64 { return dlSendLimit }

func limitText() string { return fmt.Sprintf("%d MB", dlSendLimit>>20) }

func partMaxMB() int { return max(int(dlSendLimit>>20)-1, 5) }

func compressTime(size int64) time.Duration {
	return min(max(6*time.Minute, time.Duration(size/(100<<20))*time.Minute), 45*time.Minute)
}
