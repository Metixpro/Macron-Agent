package agent

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const VersionsDir = ".macron_versions"

type SnapshotInfo struct {
	Step      int       `json:"step"`
	Label     string    `json:"label"`
	CreatedAt time.Time `json:"created_at"`
	FileCount int       `json:"file_count"`
}

type VersionManager struct {
	ws string
}

func NewVersionManager(ws string) *VersionManager {
	return &VersionManager{ws: ws}
}

func (v *VersionManager) baseDir() string {
	return filepath.Join(v.ws, VersionsDir)
}

func (v *VersionManager) stepDir(step int) string {
	return filepath.Join(v.baseDir(), fmt.Sprintf("step_%03d", step))
}

// Snapshot captures all files in the workspace at the current step.
func (v *VersionManager) Snapshot(step int, label string) (SnapshotInfo, error) {
	info := SnapshotInfo{
		Step:      step,
		Label:     label,
		CreatedAt: time.Now(),
	}
	if v.ws == "" {
		return info, nil
	}

	dest := v.stepDir(step)
	_ = os.RemoveAll(dest)
	if err := os.MkdirAll(dest, 0o700); err != nil {
		return info, err
	}

	count := 0
	err := filepath.Walk(v.ws, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(v.ws, path)
		if err != nil || rel == "." {
			return nil
		}
		// Skip hidden and internal folders
		if strings.HasPrefix(rel, ".") || strings.Contains(rel, string(filepath.Separator)+".") {
			if fi.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dest, rel)
		if fi.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if fi.Mode().IsRegular() {
			if err := copyFile(path, target); err == nil {
				count++
			}
		}
		return nil
	})

	info.FileCount = count
	meta := fmt.Sprintf("label: %s\nat: %s\nfiles: %d\n", label, info.CreatedAt.Format(time.RFC3339), count)
	_ = os.WriteFile(filepath.Join(dest, ".meta"), []byte(meta), 0o600)
	return info, err
}

// Rollback restores the workspace files to the state captured in the specified step snapshot.
func (v *VersionManager) Rollback(step int) (int, error) {
	src := v.stepDir(step)
	if fi, err := os.Stat(src); err != nil || !fi.IsDir() {
		return 0, fmt.Errorf("no snapshot found for step %d", step)
	}

	restored := 0
	err := filepath.Walk(src, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, err := filepath.Rel(src, path)
		if err != nil || rel == "." || rel == ".meta" {
			return nil
		}
		target := filepath.Join(v.ws, rel)
		if fi.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		if fi.Mode().IsRegular() {
			if err := copyFile(path, target); err == nil {
				restored++
			}
		}
		return nil
	})
	return restored, err
}

// ListSnapshots returns all stored snapshots sorted by step number.
func (v *VersionManager) ListSnapshots() []SnapshotInfo {
	entries, err := os.ReadDir(v.baseDir())
	if err != nil {
		return nil
	}
	var out []SnapshotInfo
	for _, e := range entries {
		if !e.IsDir() || !strings.HasPrefix(e.Name(), "step_") {
			continue
		}
		stepStr := strings.TrimPrefix(e.Name(), "step_")
		step, err := strconv.Atoi(stepStr)
		if err != nil {
			continue
		}
		metaBytes, _ := os.ReadFile(filepath.Join(v.baseDir(), e.Name(), ".meta"))
		label := ""
		for _, line := range strings.Split(string(metaBytes), "\n") {
			if strings.HasPrefix(line, "label: ") {
				label = strings.TrimPrefix(line, "label: ")
			}
		}
		out = append(out, SnapshotInfo{
			Step:  step,
			Label: label,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Step < out[j].Step
	})
	return out
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, in)
	return err
}
