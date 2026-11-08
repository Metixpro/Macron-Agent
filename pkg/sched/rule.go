package sched

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const MinInterval = 30 * time.Minute

type Rule struct {
	Kind   string
	Hour   int
	Minute int
	Days   []time.Weekday
	Day    int
	Every  time.Duration
}

var dayNames = []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}

func ParseDay(s string) (time.Weekday, bool) {
	s = strings.ToLower(strings.TrimSpace(s))
	persian := map[string]time.Weekday{"یکشنبه": 0, "دوشنبه": 1, "سه‌شنبه": 2, "سهشنبه": 2, "سه شنبه": 2, "چهارشنبه": 3,
		"پنجشنبه": 4, "پنج‌شنبه": 4, "پنج شنبه": 4, "جمعه": 5, "شنبه": 6}
	if d, ok := persian[s]; ok {
		return d, true
	}
	for i, n := range dayNames {
		if len(s) >= 3 && strings.HasPrefix(s, n) {
			return time.Weekday(i), true
		}
	}
	return 0, false
}

func Parse(s string) (Rule, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Rule{}, nil
	}
	kind, rest, _ := strings.Cut(s, ":")
	var at string
	if k, a, ok := strings.Cut(kind, "@"); ok {
		kind, at = k, a+":"+rest
		rest = ""
	} else if r, a, ok := strings.Cut(rest, "@"); ok {
		rest, at = r, a
	}
	r := Rule{Kind: kind}
	switch kind {
	case "every":
		d, err := time.ParseDuration(rest)
		if err != nil || d < MinInterval {
			return Rule{}, fmt.Errorf("an interval must be at least %v", MinInterval)
		}
		r.Every = d
		return r, nil
	case "daily":
	case "weekly":
		for _, p := range strings.Split(rest, ",") {
			d, ok := ParseDay(p)
			if !ok {
				return Rule{}, fmt.Errorf("unknown weekday %q", p)
			}
			r.Days = append(r.Days, d)
		}
		if len(r.Days) == 0 {
			return Rule{}, errors.New("a weekly rule needs at least one weekday")
		}
	case "monthly":
		d, err := strconv.Atoi(rest)
		if err != nil || d < 1 || d > 31 {
			return Rule{}, errors.New("a monthly rule needs a day of the month (1–31)")
		}
		r.Day = d
	default:
		return Rule{}, fmt.Errorf("unknown repetition %q", kind)
	}
	h, m, err := ParseClock(at)
	if err != nil {
		return Rule{}, err
	}
	r.Hour, r.Minute = h, m
	return r, nil
}

func ParseClock(s string) (hour, minute int, err error) {
	s = toASCIIDigits(strings.TrimSpace(s))
	hs, ms, ok := strings.Cut(s, ":")
	h, err1 := strconv.Atoi(hs)
	m, err2 := strconv.Atoi(ms)
	if !ok || err1 != nil || err2 != nil || h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, fmt.Errorf("time of day %q must look like 09:30", s)
	}
	return h, m, nil
}

func (r Rule) String() string {
	clock := fmt.Sprintf("@%02d:%02d", r.Hour, r.Minute)
	switch r.Kind {
	case "every":
		return fmt.Sprintf("every:%dm", int(r.Every/time.Minute))
	case "daily":
		return "daily" + clock
	case "weekly":
		var names []string
		for _, d := range r.Days {
			names = append(names, dayNames[d])
		}
		return "weekly:" + strings.Join(names, ",") + clock
	case "monthly":
		return fmt.Sprintf("monthly:%d", r.Day) + clock
	}
	return ""
}

func (r Rule) Once() bool { return r.Kind == "" }

func (r Rule) Next(t time.Time, loc *time.Location) time.Time {
	lt := t.In(loc)
	at := func(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, r.Hour, r.Minute, 0, 0, loc) }
	switch r.Kind {
	case "every":
		return t.Add(r.Every)
	case "daily":
		n := at(lt.Year(), lt.Month(), lt.Day())
		if !n.After(t) {
			n = at(lt.Year(), lt.Month(), lt.Day()+1)
		}
		return n
	case "weekly":
		for i := 0; i <= 7; i++ {
			n := at(lt.Year(), lt.Month(), lt.Day()+i)
			if n.After(t) && containsDay(r.Days, n.Weekday()) {
				return n
			}
		}
	case "monthly":
		for i := 0; i <= 12; i++ {
			y, m := lt.Year(), lt.Month()+time.Month(i)
			n := at(y, m, min(r.Day, daysIn(y, m)))
			if n.After(t) {
				return n
			}
		}
	}
	return time.Time{}
}

func containsDay(days []time.Weekday, d time.Weekday) bool {
	for _, x := range days {
		if x == d {
			return true
		}
	}
	return false
}

func daysIn(y int, m time.Month) int {
	return time.Date(y, m+1, 0, 0, 0, 0, 0, time.UTC).Day()
}

func (r Rule) Describe() string {
	clock := toPersianDigits(fmt.Sprintf("%02d:%02d", r.Hour, r.Minute))
	fa := []string{"یکشنبه", "دوشنبه", "سه‌شنبه", "چهارشنبه", "پنجشنبه", "جمعه", "شنبه"}
	switch r.Kind {
	case "every":
		if r.Every%time.Hour == 0 {
			return "هر " + toPersianDigits(strconv.Itoa(int(r.Every/time.Hour))) + " ساعت"
		}
		return "هر " + toPersianDigits(strconv.Itoa(int(r.Every/time.Minute))) + " دقیقه"
	case "daily":
		return "هر روز ساعت " + clock
	case "weekly":
		var names []string
		for _, d := range r.Days {
			names = append(names, fa[d])
		}
		return "هر " + strings.Join(names, " و ") + " ساعت " + clock
	case "monthly":
		return "هر ماه روز " + toPersianDigits(strconv.Itoa(r.Day)) + " ساعت " + clock
	}
	return "یک‌بار"
}

func (r Rule) DescribeEnglish() string {
	clock := fmt.Sprintf("%02d:%02d", r.Hour, r.Minute)
	plural := func(n int, unit string) string {
		if n == 1 {
			return "every " + unit
		}
		return fmt.Sprintf("every %d %ss", n, unit)
	}
	switch r.Kind {
	case "every":
		if r.Every%time.Hour == 0 {
			return plural(int(r.Every/time.Hour), "hour")
		}
		return plural(int(r.Every/time.Minute), "minute")
	case "daily":
		return "every day at " + clock
	case "weekly":
		var names []string
		for _, d := range r.Days {
			names = append(names, d.String())
		}
		return "every " + strings.Join(names, " and ") + " at " + clock
	case "monthly":
		return fmt.Sprintf("on day %d of every month at %s", r.Day, clock)
	}
	return "once"
}

func toASCIIDigits(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= '۰' && r <= '۹':
			return '0' + (r - '۰')
		case r >= '٠' && r <= '٩':
			return '0' + (r - '٠')
		}
		return r
	}, s)
}

func toPersianDigits(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= '0' && r <= '9' {
			return '۰' + (r - '0')
		}
		return r
	}, s)
}

func ParseLocal(s string, loc *time.Location) (time.Time, error) {
	s = strings.TrimSpace(toASCIIDigits(s))
	for _, layout := range []string{"2006-01-02 15:04", "2006-01-02T15:04", "2006-01-02 15:04:05", "2006-01-02T15:04:05", "2006/01/02 15:04"} {
		if t, err := time.ParseInLocation(layout, s, loc); err == nil {
			return t, nil
		}
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("%q is not a date and time like 2026-10-20 11:00", s)
}
