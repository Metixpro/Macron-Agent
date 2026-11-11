package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

const ToolGitHubWorkflow = "github_workflow"

// ExecuteGitHubWorkflow manages GitHub pull requests, issues, and comments.
func (r *Registry) ExecuteGitHubWorkflow(ctx context.Context, sessionID, action, repo, title, body, base, head string, number int) (string, error) {
	token := r.GitHubToken
	if token == "" {
		return "", fmt.Errorf("github workflow requires GITHUB_TOKEN configured")
	}

	repo = strings.TrimSpace(repo)
	if repo == "" {
		return "", fmt.Errorf("repo is required (e.g. owner/repo)")
	}

	client := r.HTTP
	if client == nil {
		client = http.DefaultClient
	}

	apiBase := "https://api.github.com/repos/" + repo

	switch action {
	case "create_pr":
		if title == "" || head == "" {
			return "", fmt.Errorf("title and head branch are required for create_pr")
		}
		if base == "" {
			base = "main"
		}
		payload, _ := json.Marshal(map[string]any{
			"title": title,
			"body":  body,
			"head":  head,
			"base":  base,
		})
		req, _ := http.NewRequestWithContext(ctx, "POST", apiBase+"/pulls", bytes.NewReader(payload))
		req.Header.Set("Authorization", "token "+token)
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()

		var pr struct {
			HTMLURL string `json:"html_url"`
			Number  int    `json:"number"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&pr)
		return fmt.Sprintf("✅ Pull Request #%d created: %s", pr.Number, pr.HTMLURL), nil

	case "create_issue":
		if title == "" {
			return "", fmt.Errorf("title is required for create_issue")
		}
		payload, _ := json.Marshal(map[string]any{
			"title": title,
			"body":  body,
		})
		req, _ := http.NewRequestWithContext(ctx, "POST", apiBase+"/issues", bytes.NewReader(payload))
		req.Header.Set("Authorization", "token "+token)
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()

		var issue struct {
			HTMLURL string `json:"html_url"`
			Number  int    `json:"number"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&issue)
		return fmt.Sprintf("✅ Issue #%d created: %s", issue.Number, issue.HTMLURL), nil

	case "list_issues":
		req, _ := http.NewRequestWithContext(ctx, "GET", apiBase+"/issues?state=open&per_page=10", nil)
		req.Header.Set("Authorization", "token "+token)

		resp, err := client.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()

		var issues []struct {
			Number  int    `json:"number"`
			Title   string `json:"title"`
			HTMLURL string `json:"html_url"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&issues)

		var sb strings.Builder
		sb.WriteString(fmt.Sprintf("### Open Issues in %s (%d)\n\n", repo, len(issues)))
		for _, is := range issues {
			sb.WriteString(fmt.Sprintf("- [#%d](%s): %s\n", is.Number, is.HTMLURL, is.Title))
		}
		return strings.TrimSpace(sb.String()), nil

	case "add_comment":
		if number <= 0 || body == "" {
			return "", fmt.Errorf("valid number and comment body are required")
		}
		payload, _ := json.Marshal(map[string]any{"body": body})
		endpoint := fmt.Sprintf("%s/issues/%d/comments", apiBase, number)
		req, _ := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(payload))
		req.Header.Set("Authorization", "token "+token)
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		return fmt.Sprintf("✅ Comment added to #%d.", number), nil
	}

	return "", fmt.Errorf("unknown action: %s", action)
}
