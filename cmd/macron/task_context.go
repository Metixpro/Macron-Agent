package main

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/telegram"
	"github.com/Metixpro/macron-agent/pkg/textutil"
)

var (
	separateTaskRe        = regexp.MustCompile(`(?i)(?:کار|تسک|درخواست|موضوع)\s*(?:جدید|دیگه|دیگر|جدا|مستقل|بعدی)|(?:new|another|separate|unrelated|different)\s+(?:task|request|topic|question)|(?:به|بریم)\s+سراغ\s+(?:یه|یک)\s+(?:کار|موضوع)|بیخیال\s+(?:کار\s+)?قبلی`)
	attachmentReferenceRe = regexp.MustCompile(`(?i)(?:این|همین|اون|همون)\s*(?:فایل|برنامه|اپ|اپلیکیشن|عکس|تصویر|سند|پیوست)|\b(?:this|that|the|these)\s+(?:file|app|apk|photo|image|document|attachment)s?\b|\bapk\b|اینو|اینارو|اینها|این رو|this one`)
)

func intentText(text string) string {
	return strings.Join(intentWords(text), " ")
}

func intentWords(text string) []string {
	text = strings.NewReplacer("‌", "", "ي", "ی", "ك", "ک").Replace(strings.ToLower(textutil.Fold(text)))
	return strings.FieldsFunc(text, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
}

func separateTask(text string) bool { return separateTaskRe.MatchString(intentText(text)) }

func (r *activeRun) describePendingAttachment(m *telegram.Message, text string) bool {
	if m.HasMedia() || strings.TrimSpace(text) == "" || separateTask(text) {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return false
	}
	reply := m.RepliedInChat()
	if reply == nil && (m.ExternalReply != nil || m.Quote != nil || m.ReplyToStory != nil) {
		return false
	}
	for i := len(r.pending) - 1; i >= 0; i-- {
		x := r.pending[i]
		if x.m == nil || !x.m.HasMedia() {
			if reply == nil {
				return false
			}
			continue
		}
		if reply != nil {
			if reply.MessageID != x.m.MessageID || m.Chat.ID != x.m.Chat.ID {
				continue
			}
		} else {
			if i != len(r.pending)-1 || !attachmentReferenceRe.MatchString(intentText(text)) {
				return false
			}
		}
		copy := *x
		copy.text = strings.TrimSpace(x.text + "\n" + text)
		copy.memo = copy.text
		r.pending[i] = &copy
		return true
	}
	return false
}

func messageJob(j *store.OpenJob, m *telegram.Message, text string, explicit bool) (string, bool) {
	if j == nil {
		return "", false
	}
	reply := m.RepliedInChat()
	if !explicit && (separateTask(text) || m.HasMedia() || len(m.Album) > 0 || m.Quote != nil || m.ExternalReply != nil || reply != nil) {
		if reply == nil || reply.MessageID != j.MsgID {
			return "", false
		}
	}
	return jobOf(j, m.Chat.ID, text, explicit || (reply != nil && reply.MessageID == j.MsgID))
}
