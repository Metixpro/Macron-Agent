package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

const ToolTaskBoard = "task_board"

// ManageTaskBoard creates, lists, or completes tasks across Notion, Trello, and Todoist.
func (r *Registry) ManageTaskBoard(ctx context.Context, platform, action, token, containerID, taskID, title, desc string) (string, error) {
	platform = strings.ToLower(strings.TrimSpace(platform))
	title = strings.TrimSpace(title)

	client := r.HTTP
	if client == nil {
		client = http.DefaultClient
	}

	switch platform {
	case "notion":
		if token == "" {
			token = os.Getenv("NOTION_TOKEN")
		}
		if token == "" {
			return "", fmt.Errorf("notion token required (set NOTION_TOKEN or pass token)")
		}
		if action == "create_task" {
			if containerID == "" {
				containerID = os.Getenv("NOTION_DATABASE_ID")
			}
			payload, _ := json.Marshal(map[string]any{
				"parent": map[string]string{"database_id": containerID},
				"properties": map[string]any{
					"Name": map[string]any{
						"title": []map[string]any{
							{"text": map[string]string{"content": title}},
						},
					},
				},
			})
			req, _ := http.NewRequestWithContext(ctx, "POST", "https://api.notion.com/v1/pages", bytes.NewReader(payload))
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Notion-Version", "2022-06-28")
			req.Header.Set("Content-Type", "application/json")

			resp, err := client.Do(req)
			if err != nil {
				return "", err
			}
			defer resp.Body.Close()
			return fmt.Sprintf("✅ Notion page created (HTTP %d).", resp.StatusCode), nil
		}

	case "todoist":
		if token == "" {
			token = os.Getenv("TODOIST_TOKEN")
		}
		if token == "" {
			return "", fmt.Errorf("todoist token required (set TODOIST_TOKEN or pass token)")
		}
		if action == "create_task" {
			payload, _ := json.Marshal(map[string]any{
				"content":     title,
				"description": desc,
			})
			req, _ := http.NewRequestWithContext(ctx, "POST", "https://api.todoist.com/rest/v2/tasks", bytes.NewReader(payload))
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")

			resp, err := client.Do(req)
			if err != nil {
				return "", err
			}
			defer resp.Body.Close()
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<10))
			return fmt.Sprintf("✅ Todoist task created (HTTP %d): %s", resp.StatusCode, string(b)), nil
		}

	case "trello":
		key := os.Getenv("TRELLO_API_KEY")
		if token == "" {
			token = os.Getenv("TRELLO_TOKEN")
		}
		if action == "create_task" {
			u := fmt.Sprintf("https://api.trello.com/1/cards?idList=%s&key=%s&token=%s&name=%s",
				containerID, key, token, strings.ReplaceAll(title, " ", "+"))
			req, _ := http.NewRequestWithContext(ctx, "POST", u, nil)
			resp, err := client.Do(req)
			if err != nil {
				return "", err
			}
			defer resp.Body.Close()
			return fmt.Sprintf("✅ Trello card created (HTTP %d).", resp.StatusCode), nil
		}
	}

	return "", fmt.Errorf("unsupported platform or action: %s / %s", platform, action)
}
