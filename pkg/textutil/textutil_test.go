package textutil

import (
	"testing"
	"unicode/utf8"
)

func TestTruncate(t *testing.T) {
	fa := "سلام دنیا، این یک متن فارسی است"
	for n := 1; n < 20; n++ {
		if got := TruncateRunes(fa, n, "…"); !utf8.ValidString(got) {
			t.Fatalf("TruncateRunes(%d) produced invalid UTF-8", n)
		}
		if got := TruncateBytes(fa, n, "…"); !utf8.ValidString(got) {
			t.Fatalf("TruncateBytes(%d) produced invalid UTF-8: %q", n, got)
		}
	}
	if TruncateRunes("abc", 5, "…") != "abc" || TruncateRunes("abcdef", 3, "…") != "abc…" {
		t.Fatal("ascii truncation")
	}
	if TruncateRunes("abc", 0, "…") != "" {
		t.Fatal("max 0")
	}
}

func TestStripHTML(t *testing.T) {
	if got := StripHTML("<b>a &amp; b</b> &lt;c&gt;<br>x"); got != "a & b <c>\nx" {
		t.Fatalf("got %q", got)
	}
}

func TestOneLine(t *testing.T) {
	if got := OneLine("a\n\tb\x00  c"); got != "a b c" {
		t.Fatalf("got %q", got)
	}
}

func TestPersianEzafe(t *testing.T) {
	for in, want := range map[string]string{
		"همه‌ی فایل‌ها رو با دکمه‌ی توقف نگه داشتم.":    "همه فایل‌ها رو با دکمه توقف نگه داشتم.",
		"سابقه‌ی دستورها، شبکه‌ی TON و خونه‌ی":          "سابقه دستورها، شبکه TON و خونه",
		"«همه‌ی» و (تازه‌ی)":                            "«همه» و (تازه)",
		"نکته‌ای و خانه‌یی و همه‌ی‌شان":                 "نکته‌ای و خانه‌یی و همه‌ی‌شان",
		"متن `همه‌ی` و\n```\nx = 'همه‌ی'\n```\nو همه‌ی": "متن `همه‌ی` و\n```\nx = 'همه‌ی'\n```\nو همه",
		"no Persian here": "no Persian here",
	} {
		if got := PersianEzafe(in); got != want {
			t.Errorf("%q → %q, want %q", in, got, want)
		}
	}
}
