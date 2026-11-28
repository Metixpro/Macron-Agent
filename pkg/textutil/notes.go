package textutil

import (
	"regexp"
	"strings"
)

var (
	internalNotesRe   = regexp.MustCompile(`(?is)<work_notes\b[^>]*>.*?(?:</work_notes>|\z)|</?work_notes\b[^>]*>|<files_sent\b[^>]*>.*?(?:</files_sent>|\z)|</?files_sent\b[^>]*>`)
	partialOpenNoteRe = regexp.MustCompile(`(?is)<(?:work_notes|files_sent)\b[^>]*\z`)
	codeSpanRe        = regexp.MustCompile("(?s)```.*?(?:```|\\z)|~~~.*?(?:~~~|\\z)|`[^`\n]*`")
	blankLinesRe      = regexp.MustCompile(`\n{3,}`)
)

func outsideCodeSpans(md string, fn func(string) string) string {
	var b strings.Builder
	last := 0
	for _, loc := range codeSpanRe.FindAllStringIndex(md, -1) {
		b.WriteString(fn(md[last:loc[0]]))
		b.WriteString(md[loc[0]:loc[1]])
		last = loc[1]
	}
	b.WriteString(fn(md[last:]))
	return b.String()
}

// StripInternalNotes removes internal agent bookkeeping tags (<work_notes>, <files_sent>)
// emitted outside code blocks, including truncated trailing opening tags during streaming.
func StripInternalNotes(s string) string {
	if !strings.Contains(s, "<") {
		return strings.TrimSpace(s)
	}
	lower := strings.ToLower(s)
	if !strings.Contains(lower, "work_notes") && !strings.Contains(lower, "files_sent") {
		return strings.TrimSpace(s)
	}
	cleaned := outsideCodeSpans(s, func(part string) string {
		part = internalNotesRe.ReplaceAllString(part, "")
		part = partialOpenNoteRe.ReplaceAllString(part, "")
		return part
	})
	return strings.TrimSpace(blankLinesRe.ReplaceAllString(cleaned, "\n\n"))
}
