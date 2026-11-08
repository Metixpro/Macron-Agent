package main

import (
	"unicode/utf8"

	"github.com/Metixpro/macron-agent/pkg/telegram"
)

const (
	foldMinRunes    = 2000
	foldedPartChars = 16000
)

func foldOptions(l lang) telegram.FoldOptions {
	return telegram.FoldOptions{
		MinRunes: foldMinRunes,
		Code: func(language string, lines int) string {
			if language != "" {
				return l.f("💻 کد %s · %s خط", language, l.num(lines))
			}
			return l.f("💻 کد · %s خط", l.num(lines))
		},
		More: l.t("📖 ادامه متن"),
	}
}

func (b *Bot) answerChunks(l lang, answer string) []string {
	if b.richOff.Load() || utf8.RuneCountInString(answer) < foldMinRunes {
		return telegram.SplitMarkdown(answer, partChars)
	}
	var out []string
	for _, chunk := range telegram.SplitMarkdown(answer, foldedPartChars) {
		if folded := telegram.FoldLong(chunk, foldOptions(l)); telegram.VisibleRunes(folded) <= partChars {
			out = append(out, folded)
			continue
		}
		for _, part := range telegram.SplitMarkdown(chunk, partChars) {
			out = append(out, telegram.FoldLong(part, foldOptions(l)))
		}
	}
	return out
}
