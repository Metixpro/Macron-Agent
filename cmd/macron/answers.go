package main

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/Metixpro/macron-agent/pkg/llm"
)

func fileNote(delivered []string) string {
	if len(delivered) == 0 {
		return ""
	}
	return "<files_sent>" + strings.Join(delivered, ", ") + "</files_sent>"
}

func workNote(notes string) string {
	if notes = strings.TrimSpace(notes); notes == "" {
		return ""
	}
	return "<work_notes>\n" + notes + "\n</work_notes>"
}

var (
	fileNoteRe = regexp.MustCompile(`(?is)<files_sent>.*?</files_sent>|</?files_sent>|<work_notes>.*?</work_notes>|</?work_notes>`)

	legacyNoteRe = regexp.MustCompile(`(?im)^[ \t]*\(?(?:sent files?|files? sent|فایل(?:‌|\s)?های? ارسال(?:‌|\s)?شده|فایل(?:‌|\s)?های? ارسالی)\s*:[^\n)]*\)?[ \t]*$|\((?:sent files?|files? sent)\s*:[^)\n]*\)`)

	sentNoteRe = regexp.MustCompile(`(?i)\[(?:the user )?sent (?:a |an )?(?:voice message|audio file|video|round video message)\b[^\]\n]*\][^\n]*|` +
		`\[(?:the user )?sent (?:a |an )?(?:photo|file|sticker|gif animation)\b[^\]\n]*\]`)

	botSentNoteRe = regexp.MustCompile(`(?i)\[sent (?:a |an )?(?:photo|file|voice message|audio file|video|round video message|gif animation)\b[^\]\n]*\]`)

	memoNoteRe = regexp.MustCompile(`(?i)\[(?:messages the user sent while I was working on it|work log before stopping — the job is not finished)\][^\n]*(?:\n[^\n]+)*|` +
		`\[(?:the user stopped this request before I finished it|this request ran out of time before I finished it|an error stopped this request before I finished it[^\]\n]*)\]`)

	codeRe       = regexp.MustCompile("(?s)```.*?(?:```|\\z)|`[^`\n]*`")
	blankLinesRe = regexp.MustCompile(`\n{3,}`)
)

func outsideCode(md string, fn func(string) string) string {
	var b strings.Builder
	last := 0
	for _, loc := range codeRe.FindAllStringIndex(md, -1) {
		b.WriteString(fn(md[last:loc[0]]))
		b.WriteString(md[loc[0]:loc[1]])
		last = loc[1]
	}
	b.WriteString(fn(md[last:]))
	return b.String()
}

var notePrefixes = []string{"sent ", "the user sent ", "the user stopped ", "messages the user ", "this request ran ", "an error stopped ", "work log before "}

func streamSafe(s string) string {
	i := strings.LastIndexByte(s, '[')
	if i < 0 || strings.ContainsRune(s[i:], ']') {
		return s
	}
	rest := strings.ToLower(s[i+1:])
	for _, p := range notePrefixes {
		if strings.HasPrefix(p, rest) || strings.HasPrefix(rest, p) {
			return s[:i]
		}
	}
	return s
}

func scrubFileNotes(s string) string {
	if !strings.ContainsAny(s, "[(<:") {
		return s
	}
	bracketed := strings.Contains(s, "[") && (sentNoteRe.MatchString(s) || memoNoteRe.MatchString(s))
	if !strings.Contains(s, "files_sent") && !strings.Contains(s, "work_notes") && !legacyNoteRe.MatchString(s) && !bracketed {
		return s
	}
	s = fileNoteRe.ReplaceAllString(s, "")
	s = legacyNoteRe.ReplaceAllString(s, "")
	if bracketed {
		s = outsideCode(s, func(part string) string {
			return memoNoteRe.ReplaceAllString(sentNoteRe.ReplaceAllString(part, ""), "")
		})
	}
	return strings.TrimSpace(blankLinesRe.ReplaceAllString(s, "\n\n"))
}

func modernizeHistory(msgs []llm.Message) {
	for i := range msgs {
		if msgs[i].Role != "assistant" || !legacyNoteRe.MatchString(msgs[i].Content) {
			continue
		}
		msgs[i].Content = legacyNoteRe.ReplaceAllStringFunc(msgs[i].Content, func(note string) string {
			_, list, _ := strings.Cut(note, ":")
			return "<files_sent>" + strings.TrimSpace(strings.Trim(strings.TrimSpace(list), "()")) + "</files_sent>"
		})
	}
}

var (
	sentClaimRe = regexp.MustCompile(`(?i)\b(?:i(?:'ve| have)?\s+(?:just\s+)?(?:sent|attached|uploaded)|sent\s+(?:it|them|you)|here(?:'s| is| are)\s+(?:the|your)\s+(?:file|pdf|zip|document|image|picture|photo|video|song|track|audio)s?|attached\s+(?:below|above|here))\b|` +
		`فرستادم|فرستاده\s*شد|ارسال\s*(?:کردم|شد)|پیوست\s*(?:کردم|شد)|براتو?ن?\s+گذاشتم|آپلود\s*(?:کردم|شد)|اینم\s+(?:فایل|عکس|تصویر|ویدیو|آهنگ|موزیک)|این\s+هم\s+(?:فایل|عکس|تصویر|ویدیو|آهنگ)`)

	fileWordRe = regexp.MustCompile(`(?i)\b[\w\-]+\.(?:png|jpe?g|webp|gif|mp4|mov|mkv|webm|mp3|m4a|wav|ogg|flac|pdf|zip|rar|7z|docx?|xlsx?|pptx?|csv|txt|md|py|js|ts|go|html|css|json|srt|apk)\b|` +
		`\b(?:file|files|pdf|zip|document|image|images|picture|pictures|photo|photos|video|videos|song|songs|track|tracks|audio|voice|archive)\b|` +
		`فایل|عکس|تصویر|ویدیو|ویدئو|فیلم|آهنگ|موزیک|صوت|ویس|پی‌دی‌اف|پی\s?دی\s?اف|زیپ|سند|داکیومنت|اسکریپت`)
)

func claimsSent(answer string) bool {
	return sentClaimRe.MatchString(answer) && fileWordRe.MatchString(answer)
}

const noticeNoFileSent = "Your answer says a file was sent or attached, but no file goes to the user with it. If the file is ready in the " +
	"workspace, show it with ![caption](filename) in your answer or send it with send_file; if it still has to be made, make it and " +
	"send it; if it cannot be done, answer again without saying it was sent and tell the user plainly what went wrong."

const noticeUnsent = "Nothing goes to the user with this answer, but you made %s in this request. Only what you choose is sent: if any " +
	"of it is what the user wants, show it with ![caption](filename) in your answer or send it with send_file; if not (a test, a " +
	"draft, something you only needed for the work), give the same answer again."

const maxUnsentNamed = 6

func fileClaimCheck(chosen func(answer string) []string, made func(chosen []string) []string) func(string) string {
	reminded := false
	return func(answer string) string {
		sent := chosen(answer)
		if (claimsSent(scrubFileNotes(answer)) || botSentNoteRe.MatchString(codeRe.ReplaceAllString(answer, ""))) && len(sent) == 0 {
			return noticeNoFileSent
		}
		if !reminded && len(sent) == 0 && made != nil {
			if list := made(sent); len(list) > 0 {
				reminded = true
				if len(list) > maxUnsentNamed {
					list = append(list[:maxUnsentNamed], fmt.Sprintf("and %d more", len(list)-maxUnsentNamed))
				}
				return fmt.Sprintf(noticeUnsent, strings.Join(list, ", "))
			}
		}
		return ""
	}
}
