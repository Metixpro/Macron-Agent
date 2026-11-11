package tools

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/llm"
)

const ToolMediaSummary = "media_summary"

const timestampedSummaryPrompt = `Summarize this transcript of a podcast, talk or video.
1. An overview in 2-3 sentences.
2. The main segments in order, each with 2-3 bullet points (arguments, numbers, quotes). Put a timestamp before a segment only when the transcript itself has timestamps; never estimate or invent one.
3. Key takeaways.
Write in the transcript's language. Use only what the transcript says.`

const meetingMinutesPrompt = `Write meeting minutes from this transcript.
1. Topic and goal, and the date only if the transcript states it.
2. Participants: only names or speaker labels that appear in the transcript (or are listed below as known speakers).
3. The discussion in order. Attribute a point to a person only when the transcript labels who said it; otherwise write it without a name. Use timestamps only if the transcript has them.
4. Decisions.
5. Action items as a table: | Task | Owner | Deadline |, leaving Owner or Deadline empty when the transcript does not say.
Write in the transcript's language. Never invent names, times or decisions.`

func (r *Registry) SummarizeMedia(ctx context.Context, sessionID, action, filename string, speakers []string) (string, error) {
	if r.LLM == nil {
		return "", fmt.Errorf("media summary requires language model")
	}

	data, truncated, err := r.Machine.ReadFile(sessionID, filename, 400<<10)
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

	cctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
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
	if err := r.Machine.WriteFile(sessionID, outName, []byte(summary+"\n")); err != nil {
		return "", err
	}
	note := ""
	if truncated {
		note = " (only the first 400 KB of the transcript was used)"
	}
	return fmt.Sprintf("Saved to %s%s:\n\n%s", outName, note, summary), nil
}
