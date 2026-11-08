package main

import (
	"unicode/utf8"

	"github.com/Metixpro/macron-agent/pkg/telegram"
)

// Long answers fold (see telegram.FoldLong): instead of a wall of text, or
// several messages in a row, one message keeps its opening in view and the
// rest opens with a tap. A folded answer goes as fewer, longer messages.

const (
	foldMinRunes    = 2000  // answers shorter than this stay as they are
	foldedPartChars = 16000 // the size of one message of a folded answer
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

// answerChunks splits an answer into its messages, folding a long one.
func (b *Bot) answerChunks(l lang, answer string) []string {
	if b.richOff.Load() || utf8.RuneCountInString(answer) < foldMinRunes {
		return telegram.SplitMarkdown(answer, partChars)
	}
	var out []string
	for _, chunk := range telegram.SplitMarkdown(answer, foldedPartChars) {
		if folded := telegram.FoldLong(chunk, foldOptions(l)); telegram.VisibleRunes(folded) <= partChars {
			out = append(out, folded) // one message, short on screen
			continue
		}
		for _, part := range telegram.SplitMarkdown(chunk, partChars) { // little to fold: messages of the usual size
			out = append(out, telegram.FoldLong(part, foldOptions(l)))
		}
	}
	return out
}
