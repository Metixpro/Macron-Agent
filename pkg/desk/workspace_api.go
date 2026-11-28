package desk

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Metixpro/macron-agent/pkg/machine"
)

var skipWalkDirs = map[string]bool{
	".git":                 true,
	"node_modules":         true,
	"vendor":               true,
	"dist":                 true,
	"bin":                  true,
	"build":                true,
	"__pycache__":          true,
	".venv":                true,
	"venv":                 true,
	".next":                true,
	".nuxt":                true,
	"target":               true,
	".agents":              true,
	".jobs":                true,
	".tool-output":         true,
	".run":                 true,
	".docindex":            true,
	".widgets":             true,
	".macron-uploads":      true,
	".macron_page_watches": true,
	".macron_price_alerts": true,
}

func (s *Server) projectFilesAPI(w http.ResponseWriter, r *http.Request) {
	p := s.withProject(w, r)
	if p == nil {
		return
	}
	q := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	const maxResults = 60
	const maxScanned = 4000

	var exact, prefix, contains []string
	scanned := 0

	baseReal, berr := evalPathAncestor(p.path)
	if berr != nil {
		baseReal = p.path
	}

	_ = filepath.WalkDir(p.path, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if path == p.path {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if skipWalkDirs[name] || (strings.HasPrefix(name, ".") && name != ".github" && name != ".agents") {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			real, rerr := filepath.EvalSymlinks(path)
			if rerr != nil {
				return nil
			}
			if relCheck, cerr := filepath.Rel(baseReal, real); cerr != nil || relCheck == ".." || strings.HasPrefix(relCheck, ".."+string(filepath.Separator)) {
				return nil
			}
		}
		scanned++
		if scanned > maxScanned {
			return filepath.SkipAll
		}
		rel, rerr := filepath.Rel(p.path, path)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if q == "" {
			contains = append(contains, rel)
			if len(contains) >= maxResults {
				return filepath.SkipAll
			}
			return nil
		}
		low := strings.ToLower(rel)
		base := strings.ToLower(filepath.Base(rel))
		switch {
		case base == q || low == q:
			exact = append(exact, rel)
		case strings.HasPrefix(base, q) || strings.HasPrefix(low, q):
			prefix = append(prefix, rel)
		case strings.Contains(low, q):
			contains = append(contains, rel)
		}
		return nil
	})

	sort.Strings(exact)
	sort.Strings(prefix)
	sort.Strings(contains)
	out := make([]string, 0, maxResults)
	for _, group := range [][]string{exact, prefix, contains} {
		for _, f := range group {
			out = append(out, f)
			if len(out) >= maxResults {
				break
			}
		}
		if len(out) >= maxResults {
			break
		}
	}
	writeJSON(w, map[string]any{"files": out})
}

type GitChange struct {
	Path    string `json:"path"`
	Status  string `json:"status"` // M, A, D, R, ?
	Staged  bool   `json:"staged"`
	Added   int    `json:"added"`
	Deleted int    `json:"deleted"`
}

func runGit(ctx context.Context, dir string, args ...string) ([]byte, error) {
	full := append([]string{"-c", "core.quotepath=false", "-C", dir}, args...)
	return machine.Hide(exec.CommandContext(ctx, "git", full...)).Output()
}

func evalPathAncestor(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	cur := abs
	var tail []string
	for {
		if real, err := filepath.EvalSymlinks(cur); err == nil {
			for i := len(tail) - 1; i >= 0; i-- {
				real = filepath.Join(real, tail[i])
			}
			return real, nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return abs, nil
		}
		tail = append(tail, filepath.Base(cur))
		cur = parent
	}
}

func resolveInside(base, rel string) (string, string, error) {
	rel = strings.TrimSpace(rel)
	if rel == "" {
		return "", "", errors.New("file path is required")
	}
	clean := filepath.Clean(filepath.FromSlash(rel))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", "", errors.New("path must stay inside the project folder")
	}
	abs := filepath.Join(base, clean)
	r, err := filepath.Rel(base, abs)
	if err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", "", errors.New("path must stay inside the project folder")
	}
	baseReal, berr := evalPathAncestor(base)
	absReal, aerr := evalPathAncestor(abs)
	if berr != nil || aerr != nil {
		return "", "", errors.New("path must stay inside the project folder")
	}
	rr, rerr := filepath.Rel(baseReal, absReal)
	if rerr != nil || rr == ".." || strings.HasPrefix(rr, ".."+string(filepath.Separator)) {
		return "", "", errors.New("symlink escapes the project folder")
	}
	return abs, filepath.ToSlash(clean), nil
}

func (s *Server) changesAPI(w http.ResponseWriter, r *http.Request) {
	p := s.withProject(w, r)
	if p == nil {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	branchOut, err := runGit(ctx, p.path, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		branchOut, err = runGit(ctx, p.path, "symbolic-ref", "--short", "HEAD")
	}
	if err != nil {
		writeJSON(w, map[string]any{"git": false, "branch": "", "files": []GitChange{}})
		return
	}
	branch := strings.TrimSpace(string(branchOut))

	statusOut, err := runGit(ctx, p.path, "status", "--porcelain=v1", "-uall")
	if err != nil {
		writeJSON(w, map[string]any{"git": true, "branch": branch, "files": []GitChange{}})
		return
	}

	stats := map[string][2]int{}
	for _, args := range [][]string{{"diff", "--numstat"}, {"diff", "--cached", "--numstat"}} {
		if numOut, nerr := runGit(ctx, p.path, args...); nerr == nil {
			for _, line := range strings.Split(strings.TrimSpace(string(numOut)), "\n") {
				parts := strings.Split(line, "\t")
				if len(parts) < 3 {
					continue
				}
				add, _ := strconv.Atoi(parts[0])
				del, _ := strconv.Atoi(parts[1])
				fp := filepath.ToSlash(parts[2])
				cur := stats[fp]
				stats[fp] = [2]int{cur[0] + add, cur[1] + del}
			}
		}
	}

	files := []GitChange{}
	for _, line := range strings.Split(string(statusOut), "\n") {
		if len(line) < 4 {
			continue
		}
		x, y := line[0], line[1]
		fp := strings.TrimSpace(line[3:])
		if i := strings.Index(fp, " -> "); i >= 0 {
			fp = fp[i+4:]
		}
		fp = strings.Trim(fp, `"`)
		fp = filepath.ToSlash(fp)
		if fp == "" {
			continue
		}
		st := "M"
		staged := x != ' ' && x != '?'
		switch {
		case x == '?' && y == '?':
			st = "?"
			staged = false
		case x == 'A' || y == 'A':
			st = "A"
		case x == 'D' || y == 'D':
			st = "D"
		case x == 'R' || y == 'R':
			st = "R"
		}
		stCount := stats[fp]
		if st == "?" {
			if abs, _, rerr := resolveInside(p.path, fp); rerr == nil {
				if info, serr := os.Stat(abs); serr == nil && !info.IsDir() && info.Size() <= 512<<10 {
					if raw, rerr := os.ReadFile(abs); rerr == nil {
						stCount[0] = strings.Count(string(raw), "\n")
						if len(raw) > 0 && raw[len(raw)-1] != '\n' {
							stCount[0]++
						}
					}
				}
			}
		}
		files = append(files, GitChange{
			Path:    fp,
			Status:  st,
			Staged:  staged,
			Added:   stCount[0],
			Deleted: stCount[1],
		})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	writeJSON(w, map[string]any{"git": true, "branch": branch, "files": files})
}

func (s *Server) diffAPI(w http.ResponseWriter, r *http.Request) {
	p := s.withProject(w, r)
	if p == nil {
		return
	}
	abs, rel, err := resolveInside(p.path, r.URL.Query().Get("path"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// Try git diff HEAD first (covers both staged and unstaged changes against HEAD).
	diffOut, err := runGit(ctx, p.path, "diff", "HEAD", "--", rel)
	if err != nil || len(diffOut) == 0 {
		// Fallback for repos without HEAD commit yet or unstaged-only diff.
		if d2, e2 := runGit(ctx, p.path, "diff", "--", rel); e2 == nil && len(d2) > 0 {
			diffOut = d2
		} else if d3, e3 := runGit(ctx, p.path, "diff", "--cached", "--", rel); e3 == nil && len(d3) > 0 {
			diffOut = d3
		}
	}

	const maxDiffBytes = 200 << 10
	text := string(diffOut)
	if text == "" {
		// Synthesize unified diff for untracked file.
		info, serr := os.Stat(abs)
		if serr == nil && !info.IsDir() {
			if info.Size() > maxDiffBytes {
				text = fmt.Sprintf("--- /dev/null\n+++ b/%s\n@@ -0,0 +1 @@\n+(file too large to preview: %d bytes)\n", rel, info.Size())
			} else if raw, rerr := os.ReadFile(abs); rerr == nil {
				if strings.ContainsRune(string(raw), '\x00') {
					text = fmt.Sprintf("Binary file %s (%d bytes)\n", rel, info.Size())
				} else {
					lines := strings.Split(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n")
					var sb strings.Builder
					fmt.Fprintf(&sb, "--- /dev/null\n+++ b/%s\n@@ -0,0 +1,%d @@\n", rel, len(lines))
					for _, ln := range lines {
						sb.WriteByte('+')
						sb.WriteString(ln)
						sb.WriteByte('\n')
					}
					text = sb.String()
				}
			}
		}
	}

	truncated := false
	if len(text) > maxDiffBytes {
		cut := maxDiffBytes
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = text[:cut] + "\n… (diff truncated)\n"
		truncated = true
	}
	writeJSON(w, map[string]any{"path": rel, "diff": text, "truncated": truncated})
}

func (s *Server) revertAPI(w http.ResponseWriter, r *http.Request) {
	p := s.withProject(w, r)
	if p == nil {
		return
	}
	p.mu.Lock()
	running := p.live.Running
	p.mu.Unlock()
	if running {
		writeErr(w, http.StatusConflict, errors.New("stop the current job before reverting files"))
		return
	}
	var in struct {
		Path string `json:"path"`
	}
	if err := readJSON(r, &in); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	abs, rel, err := resolveInside(p.path, in.Path)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	// Check if file is tracked by git.
	_, trackedErr := runGit(ctx, p.path, "ls-files", "--error-unmatch", "--", rel)
	if trackedErr != nil {
		// Untracked file: remove it.
		if err := os.Remove(abs); err != nil && !os.IsNotExist(err) {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
	} else {
		// Tracked file: unstage and restore from HEAD.
		_, _ = runGit(ctx, p.path, "reset", "HEAD", "--", rel)
		if out, err := machine.Hide(exec.CommandContext(ctx, "git", "-c", "core.quotepath=false", "-C", p.path, "checkout", "HEAD", "--", rel)).CombinedOutput(); err != nil {
			msg := strings.TrimSpace(string(out))
			if msg == "" {
				msg = err.Error()
			}
			writeErr(w, http.StatusBadRequest, errors.New(msg))
			return
		}
	}
	writeJSON(w, map[string]bool{"ok": true})
}
