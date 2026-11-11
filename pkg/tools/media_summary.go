package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/llm"
)

const ToolMediaSummary = "media_summary"

const timestampedSummaryPrompt = `You are an expert podcast and video summarizer.
Analyze the provided transcript and produce a high-signal, timestamped summary.
Requirements:
1. Provide a 2-3 sentence Executive Overview.
2. Provide Key Topic Segments with precise timestamps formatted as [HH:MM:SS] or [MM:SS].
3. For each timestamped section, write 2-3 bullet points of what was discussed, key arguments, quotes, or numbers.
4. Conclude with Key Takeaways.
Write in the language of the transcript (Persian if Persian).`

const meetingMinutesPrompt = `You are a professional executive secretary.
Analyze the meeting audio transcript and produce official meeting minutes with speaker diarization.
Requirements:
1. Meeting Overview (Date, Topic, Goal).
2. Attendees / Participants (attributed from context or speaker labels).
3. Discussion Log with Speaker Attribution: e.g. **[سارا 00:04:15]**: ...
4. Key Decisions Agreed Upon.
5. Action Items & Next Steps Table with columns: | وظیفه / Task | مسئول / Assignee | مهلت / Deadline |.
Write in the language of the transcript (Persian if Persian).`

// SummarizeMedia generates timestamped summaries or meeting minutes with diarization.
func (r *Registry) SummarizeMedia(ctx context.Context, sessionID, action, filename string, speakers []string) (string, error) {
	if r.LLM == nil {
		return "", fmt.Errorf("media summary requires language model")
	}

	data, _, err := r.Machine.ReadFile(sessionID, filename, 256<<10)
	if err != nil {
		return "", fmt.Errorf("read transcript %s: %w", filename, err)
	}

	transcript := string(data)
	if strings.TrimSpace(transcript) == "" {
		return "", fmt.Errorf("file %s is empty", filename)
	}

	prompt := timestampedSummaryPrompt
	if action == "meeting_minutes" {
		prompt = meetingMinutesPrompt
		if len(speakers) > 0 {
			prompt += fmt.Sprintf("\nKnown attendees/speakers: %s", strings.Join(speakers, ", "))
		}
	}

	userMsg := fmt.Sprintf("File: %s\n\nTranscript Content:\n%s", filename, transcript)
	req := llm.Request{
		Messages: []llm.Message{
			{Role: "system", Content: prompt},
			{Role: "user", Content: userMsg},
		},
	}

	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	resp, err := r.LLM.Complete(cctx, req)
	if err != nil {
		return "", fmt.Errorf("summary completion: %w", err)
	}

	summary := strings.TrimSpace(resp.Message.Content)

	outName := fmt.Sprintf("%s_summary.md", strings.TrimSuffix(filename, ".txt"))
	if action == "meeting_minutes" {
		outName = fmt.Sprintf("%s_minutes.md", strings.TrimSuffix(filename, ".txt"))
	}
	_ = r.Machine.WriteFile(sessionID, outName, []byte(summary))

	return fmt.Sprintf("📋 Summary generated and saved to `%s`:\n\n%s", outName, summary), nil
}
