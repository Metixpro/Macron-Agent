package tools

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/machine"
	"github.com/Metixpro/macron-agent/pkg/textutil"
)

const (
	ToolPageWatch = "page_watch"
	pageWatchDir  = ".macron_page_watches"
)

type PageWatch struct {
	ID          string    `json:"id"`
	URL         string    `json:"url"`
	Title       string    `json:"title"`
	Selector    string    `json:"selector,omitempty"`
	LastHash    string    `json:"last_hash"`
	LastChecked time.Time `json:"last_checked"`
	LastContent string    `json:"last_content"`
	ChangeCount int       `json:"change_count"`
}

func (r *Registry) CreatePageWatch(ctx context.Context, sessionID, targetURL, selector, title string) (*PageWatch, error) {
	if title == "" {
		title = targetURL
	}
	content, err := r.Search.FetchPage(ctx, targetURL, 8000)
	if err != nil {
		return nil, fmt.Errorf("could not fetch initial page for %s: %w", targetURL, err)
	}

	hash := sha256.Sum256([]byte(content))
	hashStr := hex.EncodeToString(hash[:16])

	id := sanitizeBase(title)
	if len(id) > 24 {
		id = id[:24]
	}
	id = fmt.Sprintf("watch_%s_%x", id, time.Now().Unix()%10000)

	watch := &PageWatch{
		ID:          id,
		URL:         targetURL,
		Title:       title,
		Selector:    selector,
		LastHash:    hashStr,
		LastChecked: time.Now(),
		LastContent: textutil.TruncateRunes(content, 4000, ""),
		ChangeCount: 0,
	}

	data, _ := json.MarshalIndent(watch, "", "  ")
	path := fmt.Sprintf("%s/%s.json", pageWatchDir, id)
	if err := r.Machine.WriteFile(sessionID, path, data); err != nil {
		return nil, err
	}
	return watch, nil
}

func (r *Registry) CheckPageWatch(ctx context.Context, sessionID, watchID string) (bool, string, error) {
	path := fmt.Sprintf("%s/%s.json", pageWatchDir, watchID)
	data, _, err := r.Machine.ReadFile(sessionID, path, 64<<10)
	if err != nil {
		return false, "", fmt.Errorf("watch %s not found", watchID)
	}

	var watch PageWatch
	if err := json.Unmarshal(data, &watch); err != nil {
		return false, "", err
	}

	newContent, err := r.Search.FetchPage(ctx, watch.URL, 8000)
	if err != nil {
		return false, "", fmt.Errorf("fetch error: %w", err)
	}

	newHash := sha256.Sum256([]byte(newContent))
	newHashStr := hex.EncodeToString(newHash[:16])

	if newHashStr == watch.LastHash {
		watch.LastChecked = time.Now()
		updated, _ := json.MarshalIndent(watch, "", "  ")
		_ = r.Machine.WriteFile(sessionID, path, updated)
		return false, "No changes detected on " + watch.URL, nil
	}

	diff := computeLineDiff(watch.LastContent, newContent)
	watch.ChangeCount++
	watch.LastHash = newHashStr
	watch.LastChecked = time.Now()
	watch.LastContent = textutil.TruncateRunes(newContent, 4000, "")

	updated, _ := json.MarshalIndent(watch, "", "  ")
	_ = r.Machine.WriteFile(sessionID, path, updated)

	report := fmt.Sprintf("🔔 **Page Change Detected: %s**\nURL: %s\nTime: %s\nChanges:\n```diff\n%s\n```",
		watch.Title, watch.URL, time.Now().Format("2006-01-02 15:04:05"), diff)
	return true, report, nil
}

func (r *Registry) ListPageWatches(sessionID string) ([]PageWatch, error) {
	files, err := r.jsonFiles(sessionID, pageWatchDir)
	if err != nil {
		return nil, err
	}
	var list []PageWatch
	for _, f := range files {
		data, _, err := r.Machine.ReadFile(sessionID, f.Name, 64<<10)
		if err == nil {
			var w PageWatch
			if json.Unmarshal(data, &w) == nil {
				list = append(list, w)
			}
		}
	}
	return list, nil
}

func computeLineDiff(oldText, newText string) string {
	oldLines := strings.Split(strings.TrimSpace(oldText), "\n")
	newLines := strings.Split(strings.TrimSpace(newText), "\n")

	oldSet := make(map[string]bool)
	for _, l := range oldLines {
		if s := strings.TrimSpace(l); s != "" {
			oldSet[s] = true
		}
	}

	newSet := make(map[string]bool)
	for _, l := range newLines {
		if s := strings.TrimSpace(l); s != "" {
			newSet[s] = true
		}
	}

	var diff strings.Builder
	removedCount := 0
	for _, l := range oldLines {
		s := strings.TrimSpace(l)
		if s != "" && !newSet[s] && removedCount < 10 {
			diff.WriteString("- " + s + "\n")
			removedCount++
		}
	}

	addedCount := 0
	for _, l := range newLines {
		s := strings.TrimSpace(l)
		if s != "" && !oldSet[s] && addedCount < 10 {
			diff.WriteString("+ " + s + "\n")
			addedCount++
		}
	}
	if diff.Len() == 0 {
		return "+ (Content reordered or formatting changed)"
	}
	return strings.TrimRight(diff.String(), "\n")
}

func (r *Registry) jsonFiles(sessionID, dir string) ([]machine.FileEntry, error) {
	abs, _, err := r.Machine.HostPath(sessionID, dir)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(abs)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []machine.FileEntry
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			out = append(out, machine.FileEntry{Name: dir + "/" + e.Name()})
		}
	}
	return out, nil
}
