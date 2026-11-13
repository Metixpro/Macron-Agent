package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/Metixpro/macron-agent/pkg/config"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const ToolTaskBoard = "task_board"

func apiCall(ctx context.Context, client *http.Client, method, rawURL string, headers map[string]string, body any, secrets ...string) ([]byte, error) {
	redact := func(s string) string {
		for _, sec := range secrets {
			if len(sec) >= 6 {
				s = strings.ReplaceAll(s, sec, "•••")
			}
		}
		return s
	}
	var rd io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(data)
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, method, rawURL, rd)
	if err != nil {
		return nil, fmt.Errorf("%s", redact(err.Error()))
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s", redact(err.Error()))
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if resp.StatusCode/100 != 2 {
		msg := strings.TrimSpace(string(out))
		if len(msg) > 500 {
			msg = msg[:500] + "…"
		}
		return nil, fmt.Errorf("%s: %s", resp.Status, redact(msg))
	}
	return out, nil
}

func needEnv(name, what string) (string, error) {
	if v := strings.TrimSpace(config.ToolEnvValue(name)); v != "" {
		return v, nil
	}
	return "", fmt.Errorf("%s is not set: configure it through connections or macron config set %s <%s>", name, name, what)
}

func (r *Registry) ManageTaskBoard(ctx context.Context, platform, action, _, containerID, _, title, desc string) (string, error) {
	client := r.cloudClient()
	title = strings.TrimSpace(title)
	create := action != "list_tasks"
	if create && title == "" {
		return "", fmt.Errorf("title is required")
	}
	switch strings.ToLower(strings.TrimSpace(platform)) {
	case "notion":
		token, err := needEnv("NOTION_TOKEN", "an integration token")
		if err != nil {
			return "", err
		}
		if containerID == "" {
			containerID = config.ToolEnvValue("NOTION_DATABASE_ID")
		}
		if containerID == "" {
			return "", fmt.Errorf("container_id (the Notion database id) is required, or set NOTION_DATABASE_ID")
		}
		h := map[string]string{"Authorization": "Bearer " + token, "Notion-Version": "2022-06-28"}
		if create {
			page := map[string]any{"parent": map[string]string{"database_id": containerID},
				"properties": map[string]any{"Name": map[string]any{"title": []map[string]any{{"text": map[string]string{"content": title}}}}}}
			if desc = strings.TrimSpace(desc); desc != "" {
				page["children"] = []map[string]any{{"object": "block", "type": "paragraph",
					"paragraph": map[string]any{"rich_text": []map[string]any{{"text": map[string]string{"content": desc}}}}}}
			}
			out, err := apiCall(ctx, client, http.MethodPost, "https://api.notion.com/v1/pages", h, page, token)
			if err != nil {
				return "", err
			}
			var p struct {
				URL string `json:"url"`
			}
			_ = json.Unmarshal(out, &p)
			return "Notion page created: " + p.URL, nil
		}
		out, err := apiCall(ctx, client, http.MethodPost, "https://api.notion.com/v1/databases/"+url.PathEscape(containerID)+"/query", h, map[string]any{"page_size": 50}, token)
		if err != nil {
			return "", err
		}
		var res struct {
			Results []struct {
				URL        string `json:"url"`
				Properties map[string]struct {
					Type  string `json:"type"`
					Title []struct {
						Text string `json:"plain_text"`
					} `json:"title"`
				} `json:"properties"`
			} `json:"results"`
		}
		if err := json.Unmarshal(out, &res); err != nil {
			return "", err
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "%d pages:", len(res.Results))
		for _, p := range res.Results {
			name := ""
			for _, prop := range p.Properties {
				if prop.Type == "title" {
					for _, t := range prop.Title {
						name += t.Text
					}
				}
			}
			fmt.Fprintf(&sb, "\n- %s %s", name, p.URL)
		}
		return sb.String(), nil
	case "todoist":
		token, err := needEnv("TODOIST_TOKEN", "an API token")
		if err != nil {
			return "", err
		}
		h := map[string]string{"Authorization": "Bearer " + token}
		if create {
			task := map[string]any{"content": title, "description": desc}
			if containerID != "" {
				task["project_id"] = containerID
			}
			out, err := apiCall(ctx, client, http.MethodPost, "https://api.todoist.com/api/v1/tasks", h, task, token)
			if err != nil {
				return "", err
			}
			var t struct {
				ID string `json:"id"`
			}
			_ = json.Unmarshal(out, &t)
			return "Todoist task created (id " + t.ID + ").", nil
		}
		u := "https://api.todoist.com/api/v1/tasks"
		if containerID != "" {
			u += "?project_id=" + url.QueryEscape(containerID)
		}
		out, err := apiCall(ctx, client, http.MethodGet, u, h, nil, token)
		if err != nil {
			return "", err
		}
		type task struct {
			Content string `json:"content"`
			Due     *struct {
				String string `json:"string"`
			} `json:"due"`
		}
		var tasks []task
		if json.Unmarshal(out, &tasks) != nil {
			var page struct {
				Results []task `json:"results"`
			}
			if err := json.Unmarshal(out, &page); err != nil {
				return "", err
			}
			tasks = page.Results
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "%d open tasks:", len(tasks))
		for _, t := range tasks {
			due := ""
			if t.Due != nil && t.Due.String != "" {
				due = " (" + t.Due.String + ")"
			}
			fmt.Fprintf(&sb, "\n- %s%s", t.Content, due)
		}
		return sb.String(), nil
	case "trello":
		key, err := needEnv("TRELLO_API_KEY", "your Trello API key")
		if err != nil {
			return "", err
		}
		token, err := needEnv("TRELLO_TOKEN", "a Trello token")
		if err != nil {
			return "", err
		}
		if containerID == "" {
			return "", fmt.Errorf("container_id (the Trello list id) is required")
		}
		auth := url.Values{"key": {key}, "token": {token}}
		if create {
			q := url.Values{"key": {key}, "token": {token}, "idList": {containerID}, "name": {title}, "desc": {desc}}
			out, err := apiCall(ctx, client, http.MethodPost, "https://api.trello.com/1/cards?"+q.Encode(), nil, nil, key, token)
			if err != nil {
				return "", err
			}
			var c struct {
				URL string `json:"shortUrl"`
			}
			_ = json.Unmarshal(out, &c)
			return "Trello card created: " + c.URL, nil
		}
		out, err := apiCall(ctx, client, http.MethodGet, "https://api.trello.com/1/lists/"+url.PathEscape(containerID)+"/cards?"+auth.Encode(), nil, nil, key, token)
		if err != nil {
			return "", err
		}
		var cards []struct {
			Name string `json:"name"`
			URL  string `json:"shortUrl"`
		}
		if err := json.Unmarshal(out, &cards); err != nil {
			return "", err
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "%d cards:", len(cards))
		for _, c := range cards {
			fmt.Fprintf(&sb, "\n- %s %s", c.Name, c.URL)
		}
		return sb.String(), nil
	}
	return "", fmt.Errorf("unknown platform %q: notion, todoist or trello", platform)
}
