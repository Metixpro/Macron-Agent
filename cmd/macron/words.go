package main

import (
	"math"
	"strconv"
	"strings"

	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/telegram"
)

// apiShort names a wire format on a button.
func apiShort(api string) string {
	switch api {
	case llm.APIAnthropic:
		return "Anthropic"
	case llm.APIResponses:
		return "Responses"
	}
	return "OpenAI"
}

// msWord writes a latency: 850ms, 1.2s.
func msWord(l lang, ms int) string {
	if ms < 1000 {
		return l.num(ms) + "ms"
	}
	s := strconv.FormatFloat(float64(ms)/1000, 'f', 1, 64)
	if l == fa {
		s = strings.NewReplacer(".", "٫", "0", "۰", "1", "۱", "2", "۲", "3", "۳", "4", "۴", "5", "۵", "6", "۶", "7", "۷", "8", "۸", "9", "۹").Replace(s)
	}
	return s + "s"
}

// chunkButtons lays buttons out n to a row.
func chunkButtons(btns []telegram.InlineKeyboardButton, n int) [][]telegram.InlineKeyboardButton {
	var rows [][]telegram.InlineKeyboardButton
	for len(btns) > 0 {
		k := min(n, len(btns))
		rows = append(rows, btns[:k:k])
		btns = btns[k:]
	}
	return rows
}

// tokenWord writes a number of tokens shortly: ۸۰۰، ۱۲۰ هزار، ۲٫۵ میلیون (800, 120k, 2.5M).
func tokenWord(l lang, n int) string {
	short := func(v float64) string {
		s := strconv.FormatFloat(math.Round(v*10)/10, 'f', -1, 64)
		if l == fa {
			s = strings.NewReplacer(".", "٫", "0", "۰", "1", "۱", "2", "۲", "3", "۳", "4", "۴", "5", "۵", "6", "۶", "7", "۷", "8", "۸", "9", "۹").Replace(s)
		}
		return s
	}
	switch {
	case n >= 1000000:
		return l.f("%s میلیون", short(float64(n)/1e6))
	case n >= 10000:
		return l.f("%s هزار", l.num((n+500)/1000))
	case n >= 1000:
		return l.f("%s هزار", short(float64(n)/1e3))
	}
	return l.num(max(n, 0))
}
