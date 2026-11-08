package telegram

import (
	"testing"
	"time"
)

func TestFileDC(t *testing.T) {
	for id, want := range map[string]int{
		"AgACAgQAAxkBAAIBY2": 4, // a photo stored in DC4
		"AgACAgIAAxkBAAIBY2": 2,
		"AgACAgUAAxkBAAIBY2": 5,
		"avatar":             0,
		"":                   0,
		"!!not base64!!":     0,
	} {
		if got := FileDC(id); got != want {
			t.Errorf("FileDC(%q) = %d, want %d", id, got, want)
		}
	}
	if DCLocation(4) != "Amsterdam, Netherlands" || DCLocation(9) != "" {
		t.Error("DC locations")
	}
}

func TestEstimateAccountAge(t *testing.T) {
	month := func(a AccountAge) string { return a.When.Format("2006-01") }
	if a, ok := EstimateAccountAge(400169472); !ok || !a.Approx || month(a) != "2017-07" {
		t.Errorf("a known id: %+v", a)
	}
	if a, _ := EstimateAccountAge(600000000); !a.Approx || a.When.Before(time.Date(2017, 7, 1, 0, 0, 0, 0, time.UTC)) || a.When.After(time.Date(2019, 8, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("between two known ids: %+v", a)
	}
	if a, _ := EstimateAccountAge(1000); !a.Before || month(a) != "2013-11" {
		t.Errorf("older than the oldest: %+v", a)
	}
	if a, _ := EstimateAccountAge(7123456789); !a.After || month(a) != "2021-10" {
		t.Errorf("newer than the newest: %+v", a)
	}
	if _, ok := EstimateAccountAge(-100123); ok {
		t.Error("a chat id has no account age")
	}
	// Dates never go back as ids grow.
	prev := time.Time{}
	for id := int64(3_000_000); id < 1_900_000_000; id += 7_000_000 {
		a, _ := EstimateAccountAge(id)
		if a.When.Before(prev) {
			t.Fatalf("id %d goes back to %v", id, a.When)
		}
		prev = a.When
	}
}
