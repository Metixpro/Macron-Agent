package textutil

import (
	"fmt"
	"strings"
	"time"
)

var persianMonths = []string{
	"فروردین", "اردیبهشت", "خرداد",
	"تیر", "مرداد", "شهریور",
	"مهر", "آبان", "آذر",
	"دی", "بهمن", "اسفند",
}

var persianWeekdays = map[time.Weekday]string{
	time.Saturday:  "شنبه",
	time.Sunday:    "یکشنبه",
	time.Monday:    "دوشنبه",
	time.Tuesday:   "سه‌شنبه",
	time.Wednesday: "چهارشنبه",
	time.Thursday:  "پنجشنبه",
	time.Friday:    "جمعه",
}

var persianDigits = []string{"۰", "۱", "۲", "۳", "۴", "۵", "۶", "۷", "۸", "۹"}

func ToPersianDigits(s string) string {
	var sb strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			sb.WriteString(persianDigits[r-'0'])
		} else {
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

func GregorianToJalali(gy, gm, gd int) (jy, jm, jd int) {
	gDaysInMonth := []int{31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31}
	jDaysInMonth := []int{31, 31, 31, 31, 31, 31, 30, 30, 30, 30, 30, 29}

	gy2 := gy - 1600
	gm2 := gm - 1
	gd2 := gd - 1

	gDayNo := 365*gy2 + (gy2+3)/4 - (gy2+99)/100 + (gy2+399)/400

	for i := 0; i < gm2; i++ {
		gDayNo += gDaysInMonth[i]
	}
	if gm2 > 1 && ((gy%4 == 0 && gy%100 != 0) || (gy%400 == 0)) {
		gDayNo++
	}
	gDayNo += gd2

	jDayNo := gDayNo - 79
	jNp := jDayNo / 12053
	jDayNo %= 12053

	jy = 979 + 33*jNp + 4*(jDayNo/1461)
	jDayNo %= 1461

	if jDayNo >= 366 {
		jy += (jDayNo - 1) / 365
		jDayNo = (jDayNo - 1) % 365
	}

	for i := 0; i < 11 && jDayNo >= jDaysInMonth[i]; i++ {
		jDayNo -= jDaysInMonth[i]
		jm = i + 1
	}
	jm++
	jd = jDayNo + 1
	return jy, jm, jd
}

func FormatJalali(t time.Time, layout string) string {
	jy, jm, jd := GregorianToJalali(t.Year(), int(t.Month()), t.Day())
	monthName := ""
	if jm >= 1 && jm <= 12 {
		monthName = persianMonths[jm-1]
	}
	weekdayName := persianWeekdays[t.Weekday()]

	out := layout
	out = strings.ReplaceAll(out, "YYYY", fmt.Sprintf("%04d", jy))
	out = strings.ReplaceAll(out, "MM", fmt.Sprintf("%02d", jm))
	out = strings.ReplaceAll(out, "DD", fmt.Sprintf("%02d", jd))
	out = strings.ReplaceAll(out, "Month", monthName)
	out = strings.ReplaceAll(out, "Weekday", weekdayName)
	out = strings.ReplaceAll(out, "HH", fmt.Sprintf("%02d", t.Hour()))
	out = strings.ReplaceAll(out, "mm", fmt.Sprintf("%02d", t.Minute()))
	out = strings.ReplaceAll(out, "ss", fmt.Sprintf("%02d", t.Second()))
	return out
}

func JalaliDateFull(t time.Time) string {
	raw := FormatJalali(t, "Weekday DD Month YYYY - HH:mm")
	return ToPersianDigits(raw)
}

func JalaliDateShort(t time.Time) string {
	raw := FormatJalali(t, "YYYY/MM/DD")
	return ToPersianDigits(raw)
}
