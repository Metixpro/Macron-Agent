package tools

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestGenerateICalendar(t *testing.T) {
	start := time.Date(2024, 8, 15, 10, 0, 0, 0, time.UTC)
	end := time.Date(2024, 8, 15, 11, 30, 0, 0, time.UTC)

	ics := GenerateICalendar("Sprint Planning", "Discuss backlog items", "Room 402", start, end)
	if !strings.Contains(ics, "BEGIN:VCALENDAR") || !strings.Contains(ics, "END:VCALENDAR") {
		t.Fatalf("missing VCALENDAR envelope:\n%s", ics)
	}
	if !strings.Contains(ics, "SUMMARY:Sprint Planning") {
		t.Errorf("missing SUMMARY: %s", ics)
	}
	if !strings.Contains(ics, "DTSTART:20240815T100000Z") {
		t.Errorf("missing DTSTART: %s", ics)
	}
	if !strings.Contains(ics, "LOCATION:Room 402") {
		t.Errorf("missing LOCATION: %s", ics)
	}
}

func TestSendEmailMissingHost(t *testing.T) {
	ctx := context.Background()
	err := SendEmail(ctx, "", 587, "user", "pass", "from@example.com", "to@example.com", "Sub", "Body")
	if err == nil {
		t.Error("expected error when SMTP host is missing")
	}
}

func TestSendIranMessengerValidation(t *testing.T) {
	r := &Registry{}
	ctx := context.Background()

	// Empty text
	_, err := r.SendIranMessenger(ctx, "bale", "token", "chat123", "")
	if err == nil {
		t.Error("expected error for empty text")
	}

	// Unknown messenger
	_, err = r.SendIranMessenger(ctx, "unknown_app", "token", "chat123", "hi")
	if err == nil {
		t.Error("expected error for unknown messenger")
	}
}

func TestSendExternalMessengerValidation(t *testing.T) {
	r := &Registry{}
	ctx := context.Background()

	// Unknown platform
	_, err := r.SendExternalMessenger(ctx, "unknown_platform", "", "", "", "", "hi")
	if err == nil {
		t.Error("expected error for unknown platform")
	}
}
