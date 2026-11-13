package tools

import (
	"fmt"
	"path"
	"strings"
)

type textHunk struct{ old, new []string }

type textFilePatch struct {
	from, to string
	hunks    []textHunk
}

func parseTextPatch(patch, strip string) ([]textFilePatch, error) {
	var files []textFilePatch
	var cur *textFilePatch
	var h *textHunk
	name := func(p string) string {
		p = strings.TrimSpace(p)
		if i := strings.IndexByte(p, '\t'); i >= 0 {
			p = p[:i]
		}
		if p != "/dev/null" && strip == "1" && len(p) > 2 {
			p = p[2:]
		}
		return p
	}
	lines := strings.Split(strings.TrimSuffix(patch, "\n"), "\n")
	for i := 0; i < len(lines); i++ {
		l := strings.TrimSuffix(lines[i], "\r")
		switch {
		case strings.HasPrefix(l, "--- ") && i+1 < len(lines) && strings.HasPrefix(lines[i+1], "+++ "):
			files = append(files, textFilePatch{from: name(l[4:]), to: name(strings.TrimSuffix(lines[i+1], "\r")[4:])})
			cur, h = &files[len(files)-1], nil
			i++
		case strings.HasPrefix(l, "@@"):
			if cur == nil {
				return nil, fmt.Errorf("a hunk comes before any file header")
			}
			cur.hunks = append(cur.hunks, textHunk{})
			h = &cur.hunks[len(cur.hunks)-1]
		case h == nil:
		case strings.HasPrefix(l, "\\"):
		case strings.HasPrefix(l, "+"):
			h.new = append(h.new, l[1:])
		case strings.HasPrefix(l, "-"):
			h.old = append(h.old, l[1:])
		case strings.HasPrefix(l, " "):
			h.old = append(h.old, l[1:])
			h.new = append(h.new, l[1:])
		case l == "":
			h.old = append(h.old, "")
			h.new = append(h.new, "")
		case strings.HasPrefix(l, "diff ") || strings.HasPrefix(l, "index "):
			h = nil
		default:
			return nil, fmt.Errorf("line %d is not part of a unified diff: %q", i+1, clipText(l, 60))
		}
	}
	for fi := range files {
		for hi := range files[fi].hunks {
			hk := &files[fi].hunks[hi]
			for len(hk.old) > 0 && len(hk.new) > 0 && hk.old[len(hk.old)-1] == "" && hk.new[len(hk.new)-1] == "" {
				hk.old, hk.new = hk.old[:len(hk.old)-1], hk.new[:len(hk.new)-1]
			}
		}
	}
	return files, nil
}

func clipText(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func findBlock(lines, block []string, from int, loose bool) []int {
	same := func(a, b string) bool {
		if loose {
			return strings.TrimRight(a, " \t") == strings.TrimRight(b, " \t")
		}
		return a == b
	}
	var at []int
	for i := from; i+len(block) <= len(lines); i++ {
		ok := true
		for j := range block {
			if !same(lines[i+j], block[j]) {
				ok = false
				break
			}
		}
		if ok {
			at = append(at, i)
		}
	}
	return at
}

func applyTextHunks(content string, hunks []textHunk) (string, error) {
	crlf := strings.Contains(content, "\r\n")
	body := strings.ReplaceAll(content, "\r\n", "\n")
	final := strings.HasSuffix(body, "\n")
	lines := strings.Split(strings.TrimSuffix(body, "\n"), "\n")
	if body == "" {
		lines = nil
	}
	cursor := 0
	for n, h := range hunks {
		if len(h.old) == 0 {
			if len(lines) == 0 {
				lines = append(lines, h.new...)
				continue
			}
			return "", fmt.Errorf("hunk %d has no context lines, so there is no way to tell where it goes", n+1)
		}
		at := findBlock(lines, h.old, cursor, false)
		if len(at) == 0 {
			at = findBlock(lines, h.old, cursor, true)
		}
		if len(at) == 0 {
			return "", fmt.Errorf("hunk %d does not match the file (looking for %q)", n+1, clipText(strings.TrimSpace(h.old[0]), 60))
		}
		if len(at) > 1 {
			return "", fmt.Errorf("hunk %d matches %d places; add more context lines", n+1, len(at))
		}
		i := at[0]
		lines = append(lines[:i], append(append([]string{}, h.new...), lines[i+len(h.old):]...)...)
		cursor = i + len(h.new)
	}
	out := strings.Join(lines, "\n")
	if final || len(lines) > 0 && content == "" {
		out += "\n"
	}
	if crlf {
		out = strings.ReplaceAll(out, "\n", "\r\n")
	}
	return out, nil
}

func (r *Registry) textPatch(sessionID, dir, patch, strip string) ([]string, error) {
	files, err := parseTextPatch(patch, strip)
	if err != nil {
		return nil, err
	}
	type write struct {
		name string
		data []byte
		drop bool
	}
	var plan []write
	var names []string
	for _, f := range files {
		switch {
		case f.to == "/dev/null":
			plan = append(plan, write{name: path.Join(dir, f.from), drop: true})
			names = append(names, f.from)
			continue
		case f.from == "/dev/null":
			target := path.Join(dir, f.to)
			if _, err := r.Machine.Stat(sessionID, target); err == nil {
				return nil, fmt.Errorf("%s already exists", f.to)
			}
			out, err := applyTextHunks("", f.hunks)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", f.to, err)
			}
			plan = append(plan, write{name: target, data: []byte(out)})
			names = append(names, f.to)
			continue
		}
		if len(f.hunks) == 0 {
			continue
		}
		target := path.Join(dir, f.from)
		data, truncated, err := r.Machine.ReadFile(sessionID, target, 32<<20)
		if err != nil {
			return nil, fmt.Errorf("%s: %s", f.from, friendlyFSError(err))
		}
		if truncated {
			return nil, fmt.Errorf("%s is too large to patch", f.from)
		}
		out, err := applyTextHunks(string(data), f.hunks)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.from, err)
		}
		dest := path.Join(dir, f.to)
		plan = append(plan, write{name: dest, data: []byte(out)})
		if dest != target {
			plan = append(plan, write{name: target, drop: true})
		}
		names = append(names, f.to)
	}
	if len(plan) == 0 {
		return nil, fmt.Errorf("the patch has no hunks")
	}
	for _, w := range plan {
		if w.drop {
			if err := r.Machine.Remove(sessionID, w.name); err != nil {
				return nil, fmt.Errorf("%s: %s", w.name, friendlyFSError(err))
			}
			continue
		}
		if err := r.Machine.WriteFile(sessionID, w.name, w.data); err != nil {
			return nil, fmt.Errorf("%s: %s", w.name, friendlyFSError(err))
		}
	}
	return names, nil
}
