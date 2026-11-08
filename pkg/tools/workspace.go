package tools

import (
	"fmt"
	"io"
	"path"
	"strings"

	"github.com/Metixpro/macron-agent/pkg/machine"
)

// A sub-agent works in a workspace of its own (machine.SubSession): the files
// and folders its task names are copied in, the files it hands back are
// moved out when it is done, and the rest goes with its workspace. Sub-agents
// side by side never see, overwrite or report each other's files.

// shareLimit is how much is copied into a sub-agent's workspace at most.
const shareLimit = 200 << 20

// ShareFiles copies into sub the files and folders of its parent's
// workspace that text names, and returns their names.
func (r *Registry) ShareFiles(parent, sub, text string) []string {
	entries, _ := r.Machine.List(parent, 8, 5000)
	budget := min(int64(shareLimit), r.Machine.FreeSpace())
	var shared []string
	copyFile := func(name string) bool {
		src, info, err := r.Machine.Open(parent, name)
		if err != nil {
			return false
		}
		defer src.Close()
		if info.Size() > budget {
			return false
		}
		dst, err := r.Machine.Create(sub, name)
		if err != nil {
			return false
		}
		n, err := io.Copy(dst, src)
		budget -= n
		if cerr := dst.Close(); err == nil {
			err = cerr
		}
		return err == nil
	}
	for _, e := range entries {
		if e.Symlink || hiddenPath(e.Name) || !mentions(text, e.Name) || inside(shared, e.Name) {
			continue
		}
		if !e.IsDir {
			if copyFile(e.Name) {
				shared = append(shared, e.Name)
			}
			continue
		}
		ok := true
		for _, f := range entries {
			if !f.IsDir && !f.Symlink && strings.HasPrefix(f.Name, e.Name+"/") && !hiddenPath(f.Name) {
				ok = copyFile(f.Name) && ok
			}
		}
		if ok {
			shared = append(shared, e.Name)
		}
	}
	return shared
}

// SubFiles lists the files of a (sub-)workspace with their size and time.
func (r *Registry) SubFiles(sessionID string) map[string]string {
	out := map[string]string{}
	entries, _ := r.Machine.List(sessionID, 8, 5000)
	for _, e := range entries {
		if !e.IsDir && !e.Symlink && !hiddenPath(e.Name) {
			out[e.Name] = fmt.Sprintf("%d/%d", e.Size, e.ModTime.UnixNano())
		}
	}
	return out
}

// HandOver moves files of a sub-agent's workspace into its parent's and
// returns where each went. A file it was given and changed replaces the
// original; a new one whose name is taken gets another (photo-2.png).
func (r *Registry) HandOver(sub string, names, shared []string) map[string]string {
	parent, dir := machine.ParentSession(sub), machine.SubPath(sub)
	moved := map[string]string{}
	for _, name := range names {
		dst := name
		if _, err := r.Machine.Stat(parent, dst); err == nil && !inside(shared, name) {
			dst = r.freeName(parent, name)
		}
		if d := path.Dir(dst); d != "." {
			_ = r.Machine.MkdirAll(parent, d)
		}
		if r.Machine.Rename(parent, dir+"/"+name, dst) == nil {
			moved[name] = dst
		}
	}
	return moved
}

// RemoveSub deletes a sub-agent's workspace.
func (r *Registry) RemoveSub(sub string) {
	if dir := machine.SubPath(sub); dir != "" {
		_ = r.Machine.RemoveAll(machine.ParentSession(sub), dir)
	}
}

func (r *Registry) freeName(sessionID, name string) string {
	ext := path.Ext(name)
	stem := strings.TrimSuffix(name, ext)
	for i := 2; ; i++ {
		n := fmt.Sprintf("%s-%d%s", stem, i, ext)
		if _, err := r.Machine.Stat(sessionID, n); err != nil {
			return n
		}
	}
}

// hiddenPath: a dot file or folder anywhere in the path (internal).
func hiddenPath(name string) bool {
	for _, part := range strings.Split(name, "/") {
		if strings.HasPrefix(part, ".") {
			return true
		}
	}
	return false
}

// inside reports whether name is one of names or inside one of them.
func inside(names []string, name string) bool {
	for _, n := range names {
		if name == n || strings.HasPrefix(name, n+"/") {
			return true
		}
	}
	return false
}

// mentions reports whether text names a file or folder (not as a part of a
// longer name: "data.png" does not name "a.png").
func mentions(text, name string) bool {
	word := func(r byte) bool {
		return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.'
	}
	for i := 0; ; {
		j := strings.Index(text[i:], name)
		if j < 0 {
			return false
		}
		at, end := i+j, i+j+len(name)
		if (at == 0 || !word(text[at-1]) && text[at-1] != '/') && (end == len(text) || !word(text[end]) || text[end] == '.' && (end+1 == len(text) || !word(text[end+1]))) {
			return true
		}
		i = at + 1
	}
}
