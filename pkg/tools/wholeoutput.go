package tools

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"unicode/utf8"

	"github.com/Metixpro/macron-agent/pkg/textutil"
)

const (
	WholeOutputDir  = ".tool-output"
	wholeOutputKeep = 20
	wholeOutputMax  = 4 << 20
)

var wholeSeq atomic.Int64

func (r *Registry) KeepWhole(sessionID, tool, out string) string {
	if utf8.RuneCountInString(out) <= MaxToolOutput || r.Machine == nil || sessionID == "" {
		return textutil.HeadTail(out, MaxToolOutput)
	}
	name := fmt.Sprintf("%s/%s-%d.txt", WholeOutputDir, fileSafe(tool), wholeSeq.Add(1))
	data := out
	if len(data) > wholeOutputMax {
		data = data[:wholeOutputMax] + "\n[cut at 4 MB]"
	}
	if err := r.Machine.WriteFile(sessionID, name, []byte(data)); err != nil {
		return textutil.HeadTail(out, MaxToolOutput)
	}
	r.pruneWhole(sessionID)
	note := fmt.Sprintf("\n[The whole output (%d lines) is saved in %s: read_file it with start_line to see the part cut out above.]",
		strings.Count(out, "\n")+1, name)
	return textutil.HeadTail(out, MaxToolOutput-utf8.RuneCountInString(note)) + note
}

func (r *Registry) pruneWhole(sessionID string) {
	ws, err := r.Machine.Workspace(sessionID)
	if err != nil {
		return
	}
	dir := filepath.Join(ws, WholeOutputDir)
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) <= wholeOutputKeep {
		return
	}
	type f struct {
		name string
		mod  int64
	}
	var files []f
	for _, e := range entries {
		if info, err := e.Info(); err == nil && info.Mode().IsRegular() {
			files = append(files, f{e.Name(), info.ModTime().UnixNano()})
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod < files[j].mod })
	for _, old := range files[:max(len(files)-wholeOutputKeep, 0)] {
		_ = os.Remove(filepath.Join(dir, old.name))
	}
}

func fileSafe(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '_' || r == '-' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			return r
		}
		return '_'
	}, s)
}
