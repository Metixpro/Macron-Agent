package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/agent"
	"github.com/Metixpro/macron-agent/pkg/llm"
	"github.com/Metixpro/macron-agent/pkg/store"
	"github.com/Metixpro/macron-agent/pkg/textutil"
)

// Recall: everything said between the owner and the agent is kept for good
// (the conversation window summarizes what it forgets; the archive does
// not), and search_past_chats finds it again by words, in Persian or any
// other language: «what did we decide about the server last month?».

const toolSearchChats = "search_past_chats"

const recallBudget = 6000 // characters one search returns

func (b *Bot) recallTool(u *store.User) agent.LocalTool {
	uid := u.ID
	return agent.LocalTool{
		Def: llm.Tool{Type: "function", Function: llm.ToolDef{
			Name:        toolSearchChats,
			Description: "Search every past conversation with the owner by words (nothing is lost, even after /new): when they refer to earlier talk or work.",
			Parameters: map[string]any{"type": "object", "properties": map[string]any{
				"query": map[string]any{"type": "string"},
				"days":  map[string]any{"type": "integer", "description": "Only the last N days"},
			}, "required": []string{"query"}},
		}},
		Handler: func(ctx context.Context, args map[string]any) (agent.ToolOutput, error) {
			query, _ := args["query"].(string)
			var since time.Time
			if d := int(numArg(args["days"])); d > 0 {
				since = b.st.Now().AddDate(0, 0, -d)
			}
			hits, err := b.st.SearchArchive(ctx, uid, query, since, 5)
			if err != nil {
				return agent.ToolOutput{}, err
			}
			if len(hits) == 0 {
				return agent.ToolOutput{Text: "No earlier message matches. Try other words (the same thing may have been said differently), or a longer period."}, nil
			}
			var sb strings.Builder
			for i, h := range hits {
				around, err := b.st.ArchiveAround(ctx, uid, h.ID, 1, 1)
				if err != nil {
					return agent.ToolOutput{}, err
				}
				fmt.Fprintf(&sb, "— match %d, %s\n", i+1, h.CreatedAt.In(b.loc).Format("2006-01-02 15:04"))
				for _, t := range around {
					who := "owner"
					if t.Role == "assistant" {
						who = "you"
					}
					text := t.Content
					if t.ID == h.ID {
						text = h.Snippet // the match, with «the words» marked
					} else {
						text = textutil.TruncateRunes(text, 400, "…")
					}
					fmt.Fprintf(&sb, "  %s: %s\n", who, strings.Join(strings.Fields(text), " "))
				}
				if sb.Len() > recallBudget {
					sb.WriteString("(more matches were cut; narrow the search)\n")
					break
				}
			}
			return agent.ToolOutput{Text: sb.String()}, nil
		},
	}
}
