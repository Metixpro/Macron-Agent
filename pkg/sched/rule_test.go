package sched

import (
	"testing"
	"time"
)

var tehran, _ = time.LoadLocation("Asia/Tehran")

func at(s string) time.Time {
	t, err := ParseLocal(s, tehran)
	if err != nil {
		panic(err)
	}
	return t
}

func TestParseAndString(t *testing.T) {
	for _, s := range []string{"", "daily@11:00", "weekly:sat,mon@10:00", "monthly:15@09:30", "every:90m", "every:120m"} {
		r, err := Parse(s)
		if err != nil || r.String() != s {
			t.Errorf("%q → %+v → %q (%v)", s, r, r.String(), err)
		}
	}
	for _, bad := range []string{"daily@25:00", "weekly:@10:00", "weekly:xyz@10:00", "monthly:40@10:00", "every:5m", "hourly@10:00"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("%q must be refused", bad)
		}
	}
	if d, ok := ParseDay("پنج‌شنبه"); !ok || d != time.Thursday {
		t.Errorf("Persian weekday: %v %v", d, ok)
	}
	if h, m, err := ParseClock("۰۹:۳۰"); err != nil || h != 9 || m != 30 {
		t.Errorf("Persian digits: %d %d %v", h, m, err)
	}
}

func TestNext(t *testing.T) {
	cases := []struct {
		rule, from, want string
	}{
		{"daily@11:00", "2026-10-19 10:59", "2026-10-19 11:00"},
		{"daily@11:00", "2026-10-19 11:00", "2026-10-20 11:00"},
		{"weekly:sat,mon@10:00", "2026-10-19 12:00", "2026-10-24 10:00"},
		{"weekly:mon@10:00", "2026-10-19 09:00", "2026-10-19 10:00"},
		{"monthly:31@09:00", "2026-11-01 00:00", "2026-11-30 09:00"},
		{"monthly:15@09:00", "2026-12-20 00:00", "2027-01-15 09:00"},
		{"every:90m", "2026-10-19 10:00", "2026-10-19 11:30"},
	}
	for _, c := range cases {
		r, err := Parse(c.rule)
		if err != nil {
			t.Fatal(err)
		}
		if got := r.Next(at(c.from), tehran); !got.Equal(at(c.want)) {
			t.Errorf("%s after %s = %s, want %s", c.rule, c.from, got.In(tehran).Format("2006-01-02 15:04 Mon"), c.want)
		}
	}
	if !(Rule{}).Next(time.Now(), tehran).IsZero() {
		t.Error("a one-time rule has no next run")
	}
}

func TestDescribe(t *testing.T) {
	for rule, want := range map[string]string{
		"daily@10:00": "هر روز ساعت ۱۰:۰۰", "weekly:sat,wed@08:30": "هر شنبه و چهارشنبه ساعت ۰۸:۳۰",
		"every:120m": "هر ۲ ساعت", "every:45m": "هر ۴۵ دقیقه", "": "یک‌بار",
	} {
		r, _ := Parse(rule)
		if got := r.Describe(); got != want {
			t.Errorf("%q: %q, want %q", rule, got, want)
		}
	}
}

func TestDescribeEnglish(t *testing.T) {
	for rule, want := range map[string]string{
		"daily@10:00": "every day at 10:00", "weekly:sat,wed@08:30": "every Saturday and Wednesday at 08:30",
		"every:120m": "every 2 hours", "every:60m": "every hour", "every:45m": "every 45 minutes",
		"monthly:5@09:00": "on day 5 of every month at 09:00", "": "once",
	} {
		r, err := Parse(rule)
		if err != nil && rule != "" {
			t.Fatalf("%q: %v", rule, err)
		}
		if got := r.DescribeEnglish(); got != want {
			t.Errorf("%q: %q, want %q", rule, got, want)
		}
	}
}
