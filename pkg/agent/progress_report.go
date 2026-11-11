package agent

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// ProgressReport represents a snapshot of job progress with percentage and estimated completion time.
type ProgressReport struct {
	Done        int           `json:"done"`
	Total       int           `json:"total"`
	Percentage  float64       `json:"percentage"`
	CurrentStep string        `json:"current_step,omitempty"`
	ETA         time.Duration `json:"eta,omitempty"`
	Elapsed     time.Duration `json:"elapsed,omitempty"`
	At          time.Time     `json:"at"`
}

func FormatETA(d time.Duration) string {
	if d <= 0 {
		return "چند ثانیه"
	}
	s := int(math.Round(d.Seconds()))
	switch {
	case s < 60:
		return fmt.Sprintf("حدود %d ثانیه", s)
	case s < 3600:
		m := (s + 30) / 60
		return fmt.Sprintf("حدود %d دقیقه", m)
	default:
		h := s / 3600
		m := (s % 3600) / 60
		if m > 0 {
			return fmt.Sprintf("حدود %d ساعت و %d دقیقه", h, m)
		}
		return fmt.Sprintf("حدود %d ساعت", h)
	}
}

func FormatETAEn(d time.Duration) string {
	if d <= 0 {
		return "a few seconds"
	}
	s := int(math.Round(d.Seconds()))
	switch {
	case s < 60:
		return fmt.Sprintf("~%ds", s)
	case s < 3600:
		return fmt.Sprintf("~%dm", (s+30)/60)
	default:
		return fmt.Sprintf("~%dh %dm", s/3600, (s%3600)/60)
	}
}

func (p ProgressReport) FormatPersian() string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📊 پیشرفت: %.0f٪ (%d از %d مرحله)", p.Percentage, p.Done, p.Total))
	if p.CurrentStep != "" {
		sb.WriteString(fmt.Sprintf(" | مرحله فعلی: «%s»", p.CurrentStep))
	}
	if p.ETA > 0 {
		sb.WriteString(fmt.Sprintf(" | زمان باقی‌مانده: %s", FormatETA(p.ETA)))
	}
	return sb.String()
}

func (p ProgressReport) FormatEnglish() string {
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📊 Progress: %.0f%% (%d/%d steps)", p.Percentage, p.Done, p.Total))
	if p.CurrentStep != "" {
		sb.WriteString(fmt.Sprintf(" | Current: %s", p.CurrentStep))
	}
	if p.ETA > 0 {
		sb.WriteString(fmt.Sprintf(" | ETA: %s", FormatETAEn(p.ETA)))
	}
	return sb.String()
}

// AutoReporter manages periodic mid-task status reports.
type AutoReporter struct {
	interval   time.Duration
	lastReport time.Time
	lastDone   int
	startTime  time.Time
}

func NewAutoReporter(interval time.Duration) *AutoReporter {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	now := time.Now()
	return &AutoReporter{
		interval:   interval,
		lastReport: now,
		startTime:  now,
	}
}

// Check generates a progress report if the interval has elapsed or significant progress happened.
func (r *AutoReporter) Check(done, total int, current string, lang string) (string, bool) {
	if total == 0 {
		return "", false
	}
	now := time.Now()
	timePassed := now.Sub(r.lastReport) >= r.interval
	progressMade := done > r.lastDone

	if !timePassed && !progressMade {
		return "", false
	}

	r.lastReport = now
	r.lastDone = done

	elapsed := now.Sub(r.startTime)
	var eta time.Duration
	if done > 0 && done < total {
		avgPerStep := elapsed / time.Duration(done)
		eta = avgPerStep * time.Duration(total-done)
	}

	pct := float64(done) / float64(total) * 100.0
	rep := ProgressReport{
		Done:        done,
		Total:       total,
		Percentage:  pct,
		CurrentStep: current,
		ETA:         eta,
		Elapsed:     elapsed,
		At:          now,
	}

	if strings.HasPrefix(strings.ToLower(lang), "fa") {
		return rep.FormatPersian(), true
	}
	return rep.FormatEnglish(), true
}
