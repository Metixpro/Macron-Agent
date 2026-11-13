package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/Metixpro/macron-agent/pkg/config"
)

const ToolGitHubWorkflow = "github_workflow"

func (r *Registry) githubToken() string {
	if token, set := config.ToolEnvLookup("GITHUB_TOKEN"); set {
		return strings.TrimSpace(token)
	}
	return r.GitHubToken
}
func (r *Registry) ExecuteGitHubWorkflow(ctx context.Context, _, action, repo, title, body, base, head string, number int) (string, error) {
	token := r.githubToken()
	if token == "" {
		return "", fmt.Errorf("GITHUB_TOKEN is not set: configure GitHub through connections or macron config set GITHUB_TOKEN <a token with repo access>")
	}
	repo = strings.Trim(strings.TrimSpace(repo), "/")
	if strings.Count(repo, "/") != 1 {
		return "", fmt.Errorf("repo must be owner/name")
	}
	h := map[string]string{"Authorization": "Bearer " + token, "Accept": "application/vnd.github+json", "X-GitHub-Api-Version": "2022-11-28"}
	api := "https://api.github.com/repos/" + repo
	client := r.cloudClient()
	var made struct {
		HTMLURL string `json:"html_url"`
		Number  int    `json:"number"`
	}
	switch action {
	case "create_pr":
		if title == "" || head == "" {
			return "", fmt.Errorf("title and head (the branch with the changes) are required")
		}
		if base == "" {
			base = "main"
		}
		out, err := apiCall(ctx, client, http.MethodPost, api+"/pulls", h, map[string]any{"title": title, "body": body, "head": head, "base": base}, token)
		if err != nil {
			return "", err
		}
		_ = json.Unmarshal(out, &made)
		return fmt.Sprintf("Pull request #%d opened: %s", made.Number, made.HTMLURL), nil
	case "create_issue":
		if title == "" {
			return "", fmt.Errorf("title is required")
		}
		out, err := apiCall(ctx, client, http.MethodPost, api+"/issues", h, map[string]any{"title": title, "body": body}, token)
		if err != nil {
			return "", err
		}
		_ = json.Unmarshal(out, &made)
		return fmt.Sprintf("Issue #%d opened: %s", made.Number, made.HTMLURL), nil
	case "add_comment":
		if number <= 0 || strings.TrimSpace(body) == "" {
			return "", fmt.Errorf("number and body are required")
		}
		out, err := apiCall(ctx, client, http.MethodPost, fmt.Sprintf("%s/issues/%d/comments", api, number), h, map[string]any{"body": body}, token)
		if err != nil {
			return "", err
		}
		_ = json.Unmarshal(out, &made)
		return fmt.Sprintf("Comment added on #%d: %s", number, made.HTMLURL), nil
	case "list_issues", "":
		out, err := apiCall(ctx, client, http.MethodGet, api+"/issues?state=open&per_page=30", h, nil, token)
		if err != nil {
			return "", err
		}
		var issues []struct {
			Number int    `json:"number"`
			Title  string `json:"title"`
			PR     *struct {
				URL string `json:"url"`
			} `json:"pull_request"`
		}
		if err := json.Unmarshal(out, &issues); err != nil {
			return "", err
		}
		var sb strings.Builder
		fmt.Fprintf(&sb, "%s: %d open issues and pull requests", repo, len(issues))
		for _, is := range issues {
			kind := "issue"
			if is.PR != nil {
				kind = "PR"
			}
			fmt.Fprintf(&sb, "\n#%d [%s] %s", is.Number, kind, is.Title)
		}
		return sb.String(), nil
	}
	return "", fmt.Errorf("unknown action %q", action)
}
