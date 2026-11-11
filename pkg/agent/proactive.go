package agent

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

type ProactiveDetector struct {
	mu       sync.Mutex
	patterns map[string][]time.Time
}

func NewProactiveDetector() *ProactiveDetector {
	return &ProactiveDetector{
		patterns: make(map[string][]time.Time),
	}
}

func normalizePromptKey(prompt string) string {
	lower := strings.ToLower(strings.TrimSpace(prompt))
	fields := strings.Fields(lower)
	if len(fields) > 4 {
		fields = fields[:4]
	}
	return strings.Join(fields, " ")
}

// Observe records a user turn and detects if an automated routine should be suggested.
func (p *ProactiveDetector) Observe(prompt, lang string) (string, bool) {
	key := normalizePromptKey(prompt)
	if len(key) < 5 {
		return "", false
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	// Filter entries older than 7 days
	var recent []time.Time
	for _, t := range p.patterns[key] {
		if now.Sub(t) < 7*24*time.Hour {
			recent = append(recent, t)
		}
	}
	recent = append(recent, now)
	p.patterns[key] = recent

	// If repeated 3 or more times
	if len(recent) == 3 {
		isFa := strings.HasPrefix(strings.ToLower(lang), "fa")
		if isFa {
			return fmt.Sprintf("💡 **پیشنهاد خودکارسازی:** متوجه شدم درخواست «%s» را مکرراً اجرا می‌کنید. مایلید یک زمان‌بندی روزانه خودکار برای آن تنظیم کنم تا سر ساعت مشخص خروجی را آماده کند؟", prompt), true
		}
		return fmt.Sprintf("💡 **Proactive Suggestion:** I noticed you run '%s' frequently. Would you like me to schedule an automated recurring task for this?", prompt), true
	}
	return "", false
}
