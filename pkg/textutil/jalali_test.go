package textutil

import (
	"strings"
	"testing"
	"time"
)

func TestGregorianToJalali(t *testing.T) {
	// 2024-03-20 is 1403-01-01 (Nowruz 1403)
	jy, jm, jd := GregorianToJalali(2024, 3, 20)
	if jy != 1403 || jm != 1 || jd != 1 {
		t.Errorf("GregorianToJalali(2024, 3, 20) = %d/%d/%d, want 1403/1/1", jy, jm, jd)
	}

	// 2024-03-21 is 1403-01-02
	jy, jm, jd = GregorianToJalali(2024, 3, 21)
	if jy != 1403 || jm != 1 || jd != 2 {
		t.Errorf("GregorianToJalali(2024, 3, 21) = %d/%d/%d, want 1403/1/2", jy, jm, jd)
	}
}

func TestToPersianDigits(t *testing.T) {
	cases := map[string]string{
		"1403/08/21": "۱۴۰۳/۰۸/۲۱",
		"Price: 95000 Toman": "Price: ۹۵۰۰۰ Toman",
		"": "",
	}
	for in, want := range cases {
		if got := ToPersianDigits(in); got != want {
			t.Errorf("ToPersianDigits(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFormatJalali(t *testing.T) {
	tm := time.Date(2024, 3, 20, 14, 30, 0, 0, time.UTC)
	formatted := FormatJalali(tm, "YYYY/MM/DD")
	if formatted != "1403/01/01" {
		t.Errorf("FormatJalali = %q, want 1403/01/01", formatted)
	}

	full := JalaliDateFull(tm)
	if !strings.Contains(full, "فروردین") || !strings.Contains(full, "۱۴۰۳") {
		t.Errorf("JalaliDateFull = %q, missing Persian month or year", full)
	}
}
