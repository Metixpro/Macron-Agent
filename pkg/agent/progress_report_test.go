package agent

import (
	"strings"
	"testing"
	"time"
)

func TestFormatETA(t *testing.T) {
	casesFa := map[time.Duration]string{
		0:                "چند ثانیه",
		25 * time.Second: "حدود 25 ثانیه",
		90 * time.Second: "حدود 2 دقیقه",
		70 * time.Minute: "حدود 1 ساعت و 10 دقیقه",
	}
	for d, want := range casesFa {
		if got := FormatETA(d); got != want {
			t.Errorf("FormatETA(%v) = %q, want %q", d, got, want)
		}
	}

	casesEn := map[time.Duration]string{
		0:                "a few seconds",
		30 * time.Second: "~30s",
		5 * time.Minute:  "~5m",
		90 * time.Minute: "~1h 30m",
	}
	for d, want := range casesEn {
		if got := FormatETAEn(d); got != want {
			t.Errorf("FormatETAEn(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestAutoReporter(t *testing.T) {
	rep := NewAutoReporter(100 * time.Millisecond)

	// No steps -> no report
	msg, ok := rep.Check(0, 0, "", "fa")
	if ok || msg != "" {
		t.Error("expected no report for 0 total steps")
	}

	// First progress
	msg, ok = rep.Check(1, 3, "Scaffold code", "fa")
	if !ok {
		t.Fatal("expected report on initial progress")
	}
	if !strings.Contains(msg, "۳۳٪") && !strings.Contains(msg, "33%") {
		t.Errorf("missing percentage in report: %s", msg)
	}
	if !strings.Contains(msg, "Scaffold code") {
		t.Errorf("missing step in report: %s", msg)
	}

	// English formatting
	msg, ok = rep.Check(2, 3, "Run tests", "en")
	if !ok {
		t.Fatal("expected report on progress")
	}
	if !strings.Contains(msg, "67%") || !strings.Contains(msg, "Run tests") {
		t.Errorf("unexpected en report: %s", msg)
	}
}
