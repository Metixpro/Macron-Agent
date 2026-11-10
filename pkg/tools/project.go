package tools

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/Metixpro/macron-agent/pkg/machine"
	"github.com/Metixpro/macron-agent/pkg/textutil"
)

const (
	ToolProjectTree = "project_tree"
	ToolSearchCode  = "search_code"
	ToolFindFiles   = "find_files"
	ToolCodeOutline = "code_outline"
	ToolEditFile    = "edit_file"
	ToolExtract     = "extract_archive"
	ToolImportRepo  = "import_repo"
	ToolPackFiles   = "pack_files"

	maxScanFiles     = 20000
	maxScanFileBytes = 2 << 20
	maxEditBytes     = 20 << 20
	maxReadLines     = 2000
	maxArchiveFiles  = 50000
	maxArchiveBytes  = 2 << 30
	maxImportBytes   = 300 << 20
)

var skippedDirs = map[string]bool{
	".git": true, ".hg": true, ".svn": true, "node_modules": true, "vendor": true, "bower_components": true,
	".venv": true, "venv": true, "env": false, "__pycache__": true, ".mypy_cache": true, ".pytest_cache": true, ".tox": true,
	"dist": true, "build": true, "target": true, "out": true, ".next": true, ".nuxt": true, ".svelte-kit": true,
	".cache": true, ".gradle": true, ".idea": true, ".vscode": true, "coverage": true, ".terraform": true,
	"Pods": true, ".dart_tool": true, scratchDir: true, docIndexDir: true, machine.SubDir: true,
}

func (r *Registry) registerProjectTools() {
	r.add(ToolProjectTree,
		"Map a project: its folder tree with file counts and sizes, and its languages (files, lines). Dependency and build folders are summarized.",
		map[string]any{
			"path":  prop("string", "Default: the whole workspace"),
			"depth": prop("integer", "Default 3, max 8"),
		}, nil, r.projectTree)

	r.add(ToolSearchCode,
		"grep -rn: every line matching a regular expression (RE2), with file and line number.",
		map[string]any{
			"pattern":     prop("string", "RE2, e.g. 'func \\w+Handler'"),
			"path":        prop("string", "Default: the whole workspace"),
			"glob":        prop("string", "e.g. '*.go', 'src/**/*.py'"),
			"literal":     prop("boolean", "Plain text, not a regex"),
			"ignore_case": prop("boolean", ""),
			"context":     prop("integer", "Lines around each match (0-5)"),
			"max_results": prop("integer", "Default 100, max 500"),
			"count":       prop("boolean", "Counts per file only"),
		}, []string{"pattern"}, r.searchCode)

	r.add(ToolFindFiles,
		"Find files by glob: '*.go' anywhere, 'src/**/*.test.ts', '**/Dockerfile'.",
		map[string]any{
			"pattern": prop("string", "** crosses folders, {a,b} picks one"),
			"path":    prop("string", "Default: the whole workspace"),
		}, []string{"pattern"}, r.findFiles)

	r.add(ToolCodeOutline,
		"The functions, classes and types of a code file or folder, with line numbers.",
		map[string]any{
			"path": prop("string", "A file or folder"),
		}, []string{"path"}, r.codeOutline)

	r.add(ToolEditFile,
		"Edit a file: old_text (copied from it, unique) becomes new_text; whitespace differences are forgiven. Several edits apply "+
			"in order, all or none.",
		map[string]any{
			"filename": prop("string", ""),
			"old_text": prop("string", "One edit: the exact text"),
			"new_text": prop("string", "One edit: its replacement (empty deletes)"),
			"edits": map[string]any{"type": "array", "description": "Several edits",
				"items": map[string]any{"type": "object", "properties": map[string]any{
					"old_text":    prop("string", ""),
					"new_text":    prop("string", ""),
					"replace_all": prop("boolean", ""),
				}, "required": []string{"old_text", "new_text"}}},
			"replace_all": prop("boolean", "Every occurrence, not exactly one"),
		}, []string{"filename"}, r.editFile)

	r.add(ToolExtract,
		"Unpack an archive (.zip, .apk, .aab, .apks, .xapk, .rar, .7z, .tar.*, .gz, .xz, .iso…) into a folder; password if protected; list=true only lists it.",
		map[string]any{
			"filename": prop("string", ""),
			"dest":     prop("string", ""),
			"password": prop("string", ""),
			"list":     prop("boolean", ""),
		}, []string{"filename"}, r.extractArchive)

	r.add(ToolImportRepo,
		"Bring a public GitHub/GitLab repository (or a .zip/.tar.gz link of a project) into the workspace, files only.",
		map[string]any{
			"url":  prop("string", ""),
			"ref":  prop("string", "Branch, tag or commit"),
			"dest": prop("string", "Default: the repository's name"),
		}, []string{"url"}, r.importRepo)

	r.add(ToolPackFiles,
		"Pack files and folders into a zip, tar.gz or 7z (password: AES-256); over 45 MB or split_mb it comes in parts. "+
			"Dependency and cache folders are left out.",
		map[string]any{
			"paths":       arrayProp("string", "'.' = everything"),
			"filename":    prop("string", ""),
			"format":      enumProp("", "zip", "tar.gz", "7z"),
			"password":    prop("string", ""),
			"split_mb":    prop("integer", ""),
			"include_all": prop("boolean", ""),
		}, nil, r.packFiles)
}

func cleanPath(p string) (string, error) {
	p = strings.TrimSpace(p)
	p = strings.TrimPrefix(p, "/workspace")
	p = strings.Trim(path.Clean("/"+strings.ReplaceAll(p, "\\", "/")), "/")
	if p == "" {
		return ".", nil
	}
	if !fs.ValidPath(p) {
		return "", fmt.Errorf("path must stay inside the workspace")
	}
	return p, nil
}

func walk(fsys fs.FS, start string, includeSkipped bool, fn func(p string, d fs.DirEntry, skipped bool) error) error {
	return fs.WalkDir(fsys, start, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			if p == start {
				return err
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() && p != start && !includeSkipped && skippedDirs[d.Name()] {
			if err := fn(p, d, true); err != nil && !errors.Is(err, fs.SkipDir) {
				return err
			}
			return fs.SkipDir
		}
		return fn(p, d, false)
	})
}

func (r *Registry) projectFS(sessionID string) (fs.FS, func(), error) {
	root, err := r.Machine.OpenRoot(sessionID)
	if err != nil {
		return nil, nil, err
	}
	return root.FS(), func() { root.Close() }, nil
}

func isBinary(b []byte) bool {
	if len(b) > 8000 {
		b = b[:8000]
	}
	return bytes.IndexByte(b, 0) >= 0
}

func readSmall(fsys fs.FS, p string, max int64) ([]byte, bool) {
	f, err := fsys.Open(p)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	if st, err := f.Stat(); err != nil || !st.Mode().IsRegular() || st.Size() > max {
		return nil, false
	}
	data, err := io.ReadAll(io.LimitReader(f, max))
	return data, err == nil
}

func globRegexp(glob string) (*regexp.Regexp, error) {
	var sb strings.Builder
	sb.WriteString("^")
	depth := 0
	for i := 0; i < len(glob); i++ {
		c := glob[i]
		switch {
		case c == '*' && i+1 < len(glob) && glob[i+1] == '*':
			i++
			if i+1 < len(glob) && glob[i+1] == '/' {
				i++
				sb.WriteString("(?:.*/)?")
			} else {
				sb.WriteString(".*")
			}
		case c == '*':
			sb.WriteString("[^/]*")
		case c == '?':
			sb.WriteString("[^/]")
		case c == '{':
			depth++
			sb.WriteString("(?:")
		case c == '}' && depth > 0:
			depth--
			sb.WriteString(")")
		case c == ',' && depth > 0:
			sb.WriteString("|")
		default:
			sb.WriteString(regexp.QuoteMeta(string(c)))
		}
	}
	sb.WriteString("$")
	return regexp.Compile(sb.String())
}

func globMatcher(glob string) (func(rel string) bool, error) {
	glob = strings.TrimPrefix(strings.TrimSpace(glob), "./")
	if glob == "" {
		return func(string) bool { return true }, nil
	}
	re, err := globRegexp(glob)
	if err != nil {
		return nil, err
	}
	if !strings.Contains(glob, "/") {
		return func(rel string) bool { return re.MatchString(path.Base(rel)) }, nil
	}
	return re.MatchString, nil
}

func relTo(start, p string) string {
	if start == "." {
		return p
	}
	return strings.TrimPrefix(strings.TrimPrefix(p, start), "/")
}

var langByExt = map[string]string{
	".go": "Go", ".py": "Python", ".js": "JavaScript", ".mjs": "JavaScript", ".cjs": "JavaScript", ".jsx": "JavaScript",
	".ts": "TypeScript", ".tsx": "TypeScript", ".php": "PHP", ".java": "Java", ".kt": "Kotlin", ".kts": "Kotlin", ".cs": "C#",
	".rs": "Rust", ".c": "C", ".h": "C/C++ header", ".cc": "C++", ".cpp": "C++", ".hpp": "C++", ".cxx": "C++", ".rb": "Ruby",
	".swift": "Swift", ".dart": "Dart", ".sh": "Shell", ".bash": "Shell", ".sql": "SQL", ".html": "HTML", ".htm": "HTML",
	".css": "CSS", ".scss": "SCSS", ".vue": "Vue", ".svelte": "Svelte", ".lua": "Lua", ".r": "R", ".scala": "Scala",
	".md": "Markdown", ".json": "JSON", ".yaml": "YAML", ".yml": "YAML", ".toml": "TOML", ".xml": "XML", ".proto": "Protobuf",
}

func (r *Registry) projectTree(_ context.Context, sessionID string, args map[string]any) (string, error) {
	start, err := cleanPath(strArg(args, "path"))
	if err != nil {
		return "error: " + err.Error(), nil
	}
	depth := intArg(args, "depth", 3, 1, 8)
	fsys, done, err := r.projectFS(sessionID)
	if err != nil {
		return "error: " + friendlyFSError(err), nil
	}
	defer done()

	type node struct {
		path, name   string
		level        int
		dir, skipped bool
		size         int64
		files        int
	}
	var nodes []*node
	dirs := map[string]*node{}
	langFiles, langLines := map[string]int{}, map[string]int{}
	var totalFiles, scanned int
	var totalSize int64
	var skipped []string
	err = walk(fsys, start, false, func(p string, d fs.DirEntry, skip bool) error {
		rel := relTo(start, p)
		level := 0
		if rel != "" {
			level = strings.Count(rel, "/") + 1
		}
		n := &node{path: p, name: d.Name(), level: level, dir: d.IsDir(), skipped: skip}
		if skip {
			skipped = append(skipped, p)
		}
		if d.IsDir() {
			dirs[p] = n
		} else if info, err := d.Info(); err == nil {
			n.size = info.Size()
			totalFiles++
			totalSize += n.size
			for dir := path.Dir(p); ; dir = path.Dir(dir) {
				if dn := dirs[dir]; dn != nil {
					dn.files++
					dn.size += n.size
				}
				if dir == start || dir == "." {
					break
				}
			}
			if lang := langByExt[strings.ToLower(path.Ext(p))]; lang != "" {
				langFiles[lang]++
				if scanned < maxScanFiles {
					scanned++
					if data, ok := readSmall(fsys, p, maxScanFileBytes); ok {
						langLines[lang] += bytes.Count(data, []byte{'\n'}) + 1
					}
				}
			}
		}
		if level >= 1 && level <= depth {
			nodes = append(nodes, n)
		}
		return nil
	})
	if err != nil {
		return "error: " + friendlyFSError(err), nil
	}

	var sb strings.Builder
	title := "workspace"
	if start != "." {
		title = start
	}
	fmt.Fprintf(&sb, "%s: %d files, %s", title, totalFiles, humanSize(totalSize))
	if len(skipped) > 0 {
		fmt.Fprintf(&sb, " (not counting %s)", strings.Join(capList(skipped, 6), ", "))
	}
	if len(langFiles) > 0 {
		type lang struct {
			name         string
			files, lines int
		}
		var langs []lang
		for n, f := range langFiles {
			langs = append(langs, lang{n, f, langLines[n]})
		}
		sort.Slice(langs, func(i, j int) bool {
			if langs[i].lines != langs[j].lines {
				return langs[i].lines > langs[j].lines
			}
			return langs[i].name < langs[j].name
		})
		sb.WriteString("\nLanguages: ")
		for i, l := range langs {
			if i == 10 {
				break
			}
			if i > 0 {
				sb.WriteString(", ")
			}
			fmt.Fprintf(&sb, "%s %d files / %d lines", l.name, l.files, l.lines)
		}
	}
	sb.WriteString("\n")
	if len(nodes) == 0 {
		sb.WriteString("\n(empty)")
	}
	for i, n := range nodes {
		if i == 400 {
			fmt.Fprintf(&sb, "\n… %d more entries (map a subfolder, or use a smaller depth)", len(nodes)-400)
			break
		}
		indent := strings.Repeat("  ", n.level-1)
		switch {
		case n.skipped:
			fmt.Fprintf(&sb, "\n%s%s/ (skipped: dependencies, build or cache)", indent, n.name)
		case n.dir:
			fmt.Fprintf(&sb, "\n%s%s/ (%d files, %s)", indent, n.name, n.files, humanSize(n.size))
		default:
			fmt.Fprintf(&sb, "\n%s%s (%s)", indent, n.name, humanSize(n.size))
		}
	}
	return sb.String(), nil
}

func capList(list []string, n int) []string {
	if len(list) <= n {
		return list
	}
	return append(list[:n:n], fmt.Sprintf("%d more", len(list)-n))
}

func (r *Registry) searchCode(ctx context.Context, sessionID string, args map[string]any) (string, error) {
	pattern := rawStrArg(args, "pattern")
	if pattern == "" {
		return "error: pattern is required", nil
	}
	if boolArg(args, "literal") {
		pattern = regexp.QuoteMeta(pattern)
	}
	if boolArg(args, "ignore_case") {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return fmt.Sprintf("error: invalid regular expression: %v (set literal=true to search plain text)", err), nil
	}
	start, err := cleanPath(strArg(args, "path"))
	if err != nil {
		return "error: " + err.Error(), nil
	}
	match, err := globMatcher(strArg(args, "glob"))
	if err != nil {
		return "error: invalid glob: " + err.Error(), nil
	}
	ctxLines := intArg(args, "context", 0, 0, 5)
	limit := intArg(args, "max_results", 100, 1, 500)
	fsys, done, err := r.projectFS(sessionID)
	if err != nil {
		return "error: " + friendlyFSError(err), nil
	}
	defer done()

	var out strings.Builder
	matches, files, scanned := 0, 0, 0
	more := false
	counting := boolArg(args, "count")
	type fileCount struct {
		path string
		n    int
	}
	var counts []fileCount
	err = walk(fsys, start, false, func(p string, d fs.DirEntry, _ bool) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if d.IsDir() {
			return nil
		}
		if !match(relTo(start, p)) && !(start == p && d.Type().IsRegular()) {
			return nil
		}
		if scanned++; scanned > maxScanFiles {
			return fs.SkipAll
		}
		data, ok := readSmall(fsys, p, maxScanFileBytes)
		if !ok || isBinary(data) {
			return nil
		}
		lines := strings.Split(string(data), "\n")
		var hits []int
		for i, line := range lines {
			if re.MatchString(line) {
				hits = append(hits, i)
			}
		}
		if len(hits) == 0 {
			return nil
		}
		files++
		if counting {
			matches += len(hits)
			counts = append(counts, fileCount{p, len(hits)})
			return nil
		}
		fmt.Fprintf(&out, "\n%s\n", p)
		shownTo := -1
		for _, h := range hits {
			if matches >= limit {
				more = true
				return fs.SkipAll
			}
			matches++
			from, to := max(0, h-ctxLines), min(len(lines)-1, h+ctxLines)
			if from <= shownTo {
				from = shownTo + 1
			} else if shownTo >= 0 && ctxLines > 0 {
				out.WriteString("  --\n")
			}
			for i := from; i <= to; i++ {
				mark := "-"
				if i == h || (ctxLines > 0 && re.MatchString(lines[i])) {
					mark = ":"
				}
				fmt.Fprintf(&out, "  %d%s %s\n", i+1, mark, clipLine(lines[i]))
			}
			shownTo = to
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.SkipAll) {
		return "error: " + friendlyFSError(err), nil
	}
	if matches == 0 {
		return fmt.Sprintf("No matches for %q in %s (%d files searched).", rawStrArg(args, "pattern"), start, min(scanned, maxScanFiles)), nil
	}
	if counting {
		sort.SliceStable(counts, func(i, j int) bool { return counts[i].n > counts[j].n })
		for _, c := range counts {
			fmt.Fprintf(&out, "%s: %d\n", c.path, c.n)
		}
		return fmt.Sprintf("%d matching lines in %d files (of %d searched):\n%s", matches, files, min(scanned, maxScanFiles), out.String()), nil
	}
	head := fmt.Sprintf("%d matching lines in %d files", matches, files)
	if more {
		head += fmt.Sprintf(" (stopped at %d: narrow the pattern, path or glob)", limit)
	}
	return head + ":\n" + out.String(), nil
}

func countLines(s string) int {
	if s == "" {
		return 0
	}
	return strings.Count(strings.TrimSuffix(s, "\n"), "\n") + 1
}

func clipLine(s string) string {
	s = strings.TrimRight(s, "\r")
	if utf8.RuneCountInString(s) > 240 {
		return string([]rune(s)[:240]) + "…"
	}
	return s
}

func (r *Registry) findFiles(_ context.Context, sessionID string, args map[string]any) (string, error) {
	glob := strArg(args, "pattern")
	if glob == "" {
		return "error: pattern is required", nil
	}
	match, err := globMatcher(glob)
	if err != nil {
		return "error: invalid pattern: " + err.Error(), nil
	}
	start, err := cleanPath(strArg(args, "path"))
	if err != nil {
		return "error: " + err.Error(), nil
	}
	fsys, done, err := r.projectFS(sessionID)
	if err != nil {
		return "error: " + friendlyFSError(err), nil
	}
	defer done()
	var found []string
	total := 0
	err = walk(fsys, start, false, func(p string, d fs.DirEntry, _ bool) error {
		if d.IsDir() {
			return nil
		}
		if !match(relTo(start, p)) {
			return nil
		}
		total++
		if len(found) < 500 {
			size := ""
			if info, err := d.Info(); err == nil {
				size = " (" + humanSize(info.Size()) + ")"
			}
			found = append(found, p+size)
		}
		return nil
	})
	if err != nil {
		return "error: " + friendlyFSError(err), nil
	}
	if total == 0 {
		return fmt.Sprintf("No files match %q in %s.", glob, start), nil
	}
	s := fmt.Sprintf("%d files match %q:\n%s", total, glob, strings.Join(found, "\n"))
	if total > len(found) {
		s += fmt.Sprintf("\n… %d more", total-len(found))
	}
	return s, nil
}

var outlineRules = func() map[string][]*regexp.Regexp {
	re := func(ps ...string) []*regexp.Regexp {
		var out []*regexp.Regexp
		for _, p := range ps {
			out = append(out, regexp.MustCompile(p))
		}
		return out
	}
	js := re(`^\s*(export\s+)?(default\s+)?(async\s+)?function\*?\s+[\w$]+`,
		`^\s*(export\s+)?(default\s+)?(abstract\s+)?class\s+[\w$]+`,
		`^\s*(export\s+)?(const|let|var)\s+[\w$]+\s*(:[^=]+)?=\s*(async\s+)?(\([^)]*\)|[\w$]+)\s*(:[^=]+)?=>`,
		`^\s*(export\s+)?(declare\s+)?(interface|type|enum|namespace)\s+[\w$]+`,
		`^\s+(public\s+|private\s+|protected\s+|static\s+|async\s+|get\s+|set\s+)*(?:[\w$]+)\s*\([^)]*\)\s*(:\s*[^{]+)?\{\s*$`)
	c := re(`^\s*(class|struct|enum|union|namespace)\s+\w+`,
		`^[A-Za-z_][\w\s\*&:<>,]*[\s\*&]+[\w:~]+\s*\([^;]*\)\s*(const\s*)?\{?\s*$`)
	jvm := re(`^\s*(public\s+|private\s+|protected\s+|internal\s+|abstract\s+|final\s+|sealed\s+|static\s+|data\s+|open\s+|partial\s+)*(class|interface|enum|record|object|struct)\s+\w+`,
		`^\s*(public\s+|private\s+|protected\s+|internal\s+|static\s+|final\s+|abstract\s+|override\s+|virtual\s+|async\s+|suspend\s+|synchronized\s+)+[\w<>\[\],.?\s]*\s\w+\s*\(`,
		`^\s*(override\s+|suspend\s+|private\s+|public\s+|internal\s+)*fun\s+[\w.<>]+`)
	return map[string][]*regexp.Regexp{
		".go": re(`^func\s+(\([^)]*\)\s*)?\w+`, `^type\s+\w+`, `^\t?(var|const)\s+\w+\s`),
		".py": re(`^\s*(async\s+)?def\s+\w+`, `^\s*class\s+\w+`),
		".js": js, ".mjs": js, ".cjs": js, ".jsx": js, ".ts": js, ".tsx": js, ".vue": js, ".svelte": js,
		".php": re(`^\s*(abstract\s+|final\s+)?(class|interface|trait|enum)\s+\w+`,
			`^\s*((public|private|protected|static|abstract|final)\s+)*function\s+&?\w+`),
		".java": jvm, ".kt": jvm, ".kts": jvm, ".cs": jvm, ".scala": jvm,
		".rs": re(`^\s*(pub(\([^)]*\))?\s+)?(const\s+)?(async\s+)?(unsafe\s+)?fn\s+\w+`,
			`^\s*(pub(\([^)]*\))?\s+)?(struct|enum|trait|union|type|mod)\s+\w+`, `^\s*impl\b`),
		".c": c, ".h": c, ".cc": c, ".cpp": c, ".hpp": c, ".cxx": c,
		".rb":    re(`^\s*(def|class|module)\s+`),
		".swift": re(`^\s*(public\s+|private\s+|internal\s+|open\s+|static\s+|final\s+|@\w+\s+)*(func|class|struct|enum|protocol|extension)\s+\w+`),
		".dart":  re(`^\s*(abstract\s+)?(class|mixin|enum|extension)\s+\w+`, `^\s*[\w<>?,\s]+\s+\w+\s*\([^;]*\)\s*(async\s*)?\{\s*$`),
		".sh":    re(`^\s*(function\s+)?[\w-]+\s*\(\)\s*\{?`), ".bash": re(`^\s*(function\s+)?[\w-]+\s*\(\)\s*\{?`),
		".lua": re(`^\s*(local\s+)?function\s+[\w.:]+`),
	}
}()

var notDefinition = regexp.MustCompile(`^\s*(if|for|while|switch|catch|return|else|do|try|with|elif|foreach|new|throw|await|sizeof)\b`)

func (r *Registry) codeOutline(_ context.Context, sessionID string, args map[string]any) (string, error) {
	start, err := cleanPath(strArg(args, "path"))
	if err != nil {
		return "error: " + err.Error(), nil
	}
	fsys, done, err := r.projectFS(sessionID)
	if err != nil {
		return "error: " + friendlyFSError(err), nil
	}
	defer done()
	var out strings.Builder
	files, defs := 0, 0
	err = walk(fsys, start, false, func(p string, d fs.DirEntry, _ bool) error {
		if d.IsDir() {
			return nil
		}
		rules := outlineRules[strings.ToLower(path.Ext(p))]
		if rules == nil {
			return nil
		}
		if files >= 80 || out.Len() > 60000 {
			return fs.SkipAll
		}
		data, ok := readSmall(fsys, p, maxScanFileBytes)
		if !ok || isBinary(data) {
			return nil
		}
		lines := strings.Split(string(data), "\n")
		var found []string
		for i, line := range lines {
			if notDefinition.MatchString(line) {
				continue
			}
			for _, rule := range rules {
				if rule.MatchString(line) {
					found = append(found, fmt.Sprintf("  %d  %s", i+1, clipDef(line)))
					break
				}
			}
		}
		files++
		defs += len(found)
		fmt.Fprintf(&out, "\n%s (%d lines)\n", p, countLines(string(data)))
		if len(found) == 0 {
			out.WriteString("  (no definitions found)\n")
		}
		for _, f := range found {
			out.WriteString(f + "\n")
		}
		return nil
	})
	if err != nil && !errors.Is(err, fs.SkipAll) {
		return "error: " + friendlyFSError(err), nil
	}
	if files == 0 {
		return "No code files at " + start + " (supported: Go, Python, JS/TS, PHP, Java, Kotlin, C#, Rust, C/C++, Ruby, Swift, Dart, shell, Lua).", nil
	}
	head := fmt.Sprintf("%d definitions in %d files:", defs, files)
	if errors.Is(err, fs.SkipAll) {
		head += " (stopped early: outline a subfolder for the rest)"
	}
	return head + "\n" + out.String(), nil
}

func clipDef(line string) string {
	line = strings.TrimSpace(strings.TrimRight(line, "{ \r"))
	if utf8.RuneCountInString(line) > 160 {
		return string([]rune(line)[:160]) + "…"
	}
	return line
}

func (r *Registry) readLines(sessionID, name string, from, to int) string {
	data, truncated, err := r.Machine.ReadFile(sessionID, name, maxEditBytes)
	if err != nil {
		return "read error: " + friendlyFSError(err)
	}
	if truncated {
		return fmt.Sprintf("error: %q is over %s; search it with search_code instead", name, humanSize(maxEditBytes))
	}
	if isBinary(data) {
		return fmt.Sprintf("error: %q is a binary file", name)
	}
	lines := strings.Split(string(data), "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1]
	}
	total := len(lines)
	if from < 1 {
		from = 1
	}
	if to <= 0 || to > total {
		to = total
	}
	if to-from+1 > maxReadLines {
		to = from + maxReadLines - 1
	}
	if from > total {
		return fmt.Sprintf("%s has %d lines; start_line %d is past the end.", name, total, from)
	}

	var body strings.Builder
	width, used, long := len(fmt.Sprint(to)), 0, 0
	for i := from; i <= to; i++ {
		line := strings.TrimRight(lines[i-1], "\r")
		if utf8.RuneCountInString(line) > maxLineRunes {
			line = textutil.TruncateRunes(line, maxLineRunes, fmt.Sprintf(" …[line cut: %d characters]", utf8.RuneCountInString(line)))
			long++
		}
		entry := fmt.Sprintf("%*d│ %s\n", width, i, line)
		if n := utf8.RuneCountInString(entry); i > from && used+n > readShowRunes {
			to = i - 1
			break
		} else {
			used += n
		}
		body.WriteString(entry)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s (lines %d-%d of %d)\n", name, from, to, total)
	sb.WriteString(body.String())
	if to < total {
		fmt.Fprintf(&sb, "… %d more lines (start_line=%d)", total-to, to+1)
	}
	if long > 0 {
		fmt.Fprintf(&sb, "\n(%d very long line(s) shortened: search_code finds text in them)", long)
	}
	return sb.String()
}

type textEdit struct {
	old, new string
	all      bool
}

func (r *Registry) editFile(ctx context.Context, sessionID string, args map[string]any) (string, error) {
	name := strArg(args, "filename")
	if name == "" {
		return "error: filename is required", nil
	}
	var edits []textEdit
	if list, ok := args["edits"].([]any); ok {
		for i, e := range list {
			m, ok := e.(map[string]any)
			if !ok {
				return fmt.Sprintf("error: edit %d must be an object {old_text, new_text}", i+1), nil
			}
			edits = append(edits, textEdit{rawStrArg(m, "old_text"), rawStrArg(m, "new_text"), boolArg(m, "replace_all")})
		}
	}
	if old := rawStrArg(args, "old_text"); old != "" {
		edits = append(edits, textEdit{old, rawStrArg(args, "new_text"), boolArg(args, "replace_all")})
	}
	if len(edits) == 0 {
		return "error: give old_text and new_text, or edits [{old_text, new_text}]", nil
	}
	if refused := r.gatePath(ctx, sessionID, ToolEditFile, name); refused != "" {
		return refused, nil
	}
	data, truncated, err := r.Machine.ReadFile(sessionID, name, maxEditBytes)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return fmt.Sprintf("error: %q does not exist; create it with write_file", name), nil
	case err != nil:
		return "read error: " + friendlyFSError(err), nil
	case truncated:
		return fmt.Sprintf("error: %q is over %s", name, humanSize(maxEditBytes)), nil
	case isBinary(data):
		return fmt.Sprintf("error: %q is a binary file", name), nil
	}
	text := string(data)
	crlf := strings.Contains(text, "\r\n")
	var report []string
	for i, e := range edits {
		if e.old == "" {
			return fmt.Sprintf("error: edit %d has an empty old_text; nothing was changed", i+1), nil
		}
		old, repl := e.old, e.new
		if crlf && !strings.Contains(old, "\r\n") {
			old, repl = strings.ReplaceAll(old, "\n", "\r\n"), strings.ReplaceAll(repl, "\n", "\r\n")
		}
		n := strings.Count(text, old)
		if n == 0 {
			if start, end, fixed, ok := fuzzyEdit(text, e.old, e.new); ok {
				line := strings.Count(text[:start], "\n") + 1
				text = text[:start] + fixed + text[end:]
				report = append(report, editSummary(line, 1, e)+"\n(matched ignoring spaces and tabs; the file's indentation was kept)")
				continue
			}
		}
		switch {
		case n == 0:
			return fmt.Sprintf("error: edit %d: old_text was not found in %s; nothing was changed.%s", i+1, name, nearMiss(text, e.old)), nil
		case n > 1 && !e.all:
			return fmt.Sprintf("error: edit %d: old_text appears %d times in %s (lines %s); add surrounding lines to make it unique, "+
				"or set replace_all. Nothing was changed.", i+1, n, name, strings.Join(occurrenceLines(text, old, 8), ", ")), nil
		}
		line := strings.Count(text[:strings.Index(text, old)], "\n") + 1
		if e.all {
			text = strings.ReplaceAll(text, old, repl)
		} else {
			text = strings.Replace(text, old, repl, 1)
		}
		report = append(report, editSummary(line, n, e))
	}
	if err := r.Machine.WriteFile(sessionID, name, []byte(text)); err != nil {
		return "write error: " + friendlyFSError(err), nil
	}
	before, after := countLines(string(data)), countLines(text)
	return fmt.Sprintf("Edited %s: %d change(s), %d → %d lines.\n%s", name, len(edits), before, after, strings.Join(report, "\n")) +
		r.syntaxNote(ctx, sessionID, name), nil
}

func fuzzyEdit(text, old, repl string) (start, end int, fixed string, ok bool) {
	oldLines, top, bottom := trimBlankLines(strings.Split(strings.ReplaceAll(old, "\r\n", "\n"), "\n"))
	if len(oldLines) == 0 {
		return 0, 0, "", false
	}
	lines := strings.Split(text, "\n")
	offsets := make([]int, len(lines))
	for i, at := 0, 0; i < len(lines); i++ {
		offsets[i], at = at, at+len(lines[i])+1
	}
	want := make([]string, len(oldLines))
	for i, l := range oldLines {
		want[i] = wsKey(l)
	}
	found := -1
	for i := 0; i+len(want) <= len(lines); i++ {
		same := true
		for j, w := range want {
			if wsKey(lines[i+j]) != w {
				same = false
				break
			}
		}
		if same {
			if found >= 0 {
				return 0, 0, "", false
			}
			found = i
		}
	}
	if found < 0 {
		return 0, 0, "", false
	}
	matched := lines[found : found+len(want)]
	last := matched[len(matched)-1]
	start, end = offsets[found], offsets[found+len(want)-1]+len(last)
	eol, cr := "\n", ""
	if strings.HasSuffix(last, "\r") {
		eol, cr = "\r\n", "\r"
	}

	indent := map[string]string{}
	var oldLeads, fileLeads []string
	for j, l := range oldLines {
		if strings.TrimSpace(l) == "" {
			continue
		}
		o, f := leadingWS(l), leadingWS(strings.TrimSuffix(matched[j], "\r"))
		if _, seen := indent[o]; !seen {
			indent[o] = f
		}
		oldLeads, fileLeads = append(oldLeads, o), append(fileLeads, f)
	}
	tabs := strings.Contains(strings.Join(fileLeads, ""), "\t")
	unit, fileUnit := indentUnit(oldLeads), indentUnit(fileLeads)
	convert := func(ws string) string {
		if strings.Contains(ws, "\t") {
			if tabs {
				return ws
			}
			ws = strings.ReplaceAll(ws, "\t", strings.Repeat(" ", unit))
		}
		if tabs {
			return strings.Repeat("\t", len(ws)/unit) + strings.Repeat(" ", len(ws)%unit)
		}
		return strings.Repeat(" ", len(ws)/unit*fileUnit+len(ws)%unit)
	}
	newLines := strings.Split(strings.ReplaceAll(repl, "\r\n", "\n"), "\n")
	for top > 0 && len(newLines) > 0 && strings.TrimSpace(newLines[0]) == "" {
		newLines, top = newLines[1:], top-1
	}
	for bottom > 0 && len(newLines) > 0 && strings.TrimSpace(newLines[len(newLines)-1]) == "" {
		newLines, bottom = newLines[:len(newLines)-1], bottom-1
	}
	for i, l := range newLines {
		if strings.TrimSpace(l) == "" {
			newLines[i] = ""
			continue
		}
		lead := leadingWS(l)
		rest := l[len(lead):]
		if i < len(oldLines) && strings.TrimSpace(oldLines[i]) != "" && lead == leadingWS(oldLines[i]) {
			newLines[i] = leadingWS(strings.TrimSuffix(matched[i], "\r")) + rest
			continue
		}
		if f, ok := indent[lead]; ok {
			newLines[i] = f + rest
			continue
		}
		best := -1
		for o := range indent {
			if strings.HasPrefix(lead, o) && len(o) > best {
				best = len(o)
			}
		}
		if best >= 0 {
			newLines[i] = indent[lead[:best]] + convert(lead[best:]) + rest
		} else {
			newLines[i] = convert(lead) + rest
		}
	}
	fixed = strings.Join(newLines, eol)
	if len(newLines) > 0 {
		fixed += cr
	}
	return start, end, fixed, true
}

func wsKey(line string) string { return strings.Join(strings.Fields(line), " ") }

func leadingWS(line string) string { return line[:len(line)-len(strings.TrimLeft(line, " \t"))] }

func trimBlankLines(lines []string) (kept []string, top, bottom int) {
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines, top = lines[1:], top+1
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines, bottom = lines[:len(lines)-1], bottom+1
	}
	return lines, top, bottom
}

func indentUnit(leads []string) int {
	unit := 0
	for _, a := range leads {
		for _, b := range leads {
			if d := len(b) - len(a); d > 0 && !strings.Contains(a+b, "\t") && (unit == 0 || d < unit) {
				unit = d
			}
		}
	}
	if unit == 0 || unit > 8 {
		return 4
	}
	return unit
}

func editSummary(line, count int, e textEdit) string {
	var sb strings.Builder
	if count > 1 {
		fmt.Fprintf(&sb, "@@ line %d (%d places) @@\n", line, count)
	} else {
		fmt.Fprintf(&sb, "@@ line %d @@\n", line)
	}
	show := func(prefix, s string, max int) {
		lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
		for i, l := range lines {
			if i == max {
				fmt.Fprintf(&sb, "%s … %d more lines\n", prefix, len(lines)-max)
				break
			}
			sb.WriteString(prefix + " " + clipLine(l) + "\n")
		}
	}
	show("-", e.old, 6)
	if e.new != "" {
		show("+", e.new, 12)
	}
	return strings.TrimRight(sb.String(), "\n")
}

func nearMiss(text, old string) string {
	first := ""
	for _, l := range strings.Split(old, "\n") {
		if strings.TrimSpace(l) != "" {
			first = strings.TrimSpace(l)
			break
		}
	}
	if first == "" {
		return ""
	}
	var hits []string
	for i, l := range strings.Split(text, "\n") {
		if strings.TrimSpace(l) == first || (len(first) > 12 && strings.Contains(l, first)) {
			hits = append(hits, fmt.Sprintf("%d: %s", i+1, clipLine(l)))
			if len(hits) == 3 {
				break
			}
		}
	}
	if len(hits) == 0 {
		return " Read the file again (read_file with start_line) and copy the text exactly."
	}
	return " Lines that look similar (check spaces, tabs and the lines around them):\n" + strings.Join(hits, "\n")
}

func occurrenceLines(text, old string, max int) []string {
	var out []string
	offset := 0
	for len(out) < max {
		i := strings.Index(text[offset:], old)
		if i < 0 {
			break
		}
		out = append(out, fmt.Sprint(strings.Count(text[:offset+i], "\n")+1))
		offset += i + len(old)
	}
	return out
}

func archiveKind(name string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.HasSuffix(lower, ".zip"), IsAndroidPackage(lower):
		return "zip"
	case strings.HasSuffix(lower, ".tar.gz"), strings.HasSuffix(lower, ".tgz"):
		return "tgz"
	case strings.HasSuffix(lower, ".tar.bz2"), strings.HasSuffix(lower, ".tbz2"):
		return "tbz"
	case strings.HasSuffix(lower, ".tar"):
		return "tar"
	}
	for _, ext := range externalExts {
		if strings.HasSuffix(lower, ext) {
			return "ext"
		}
	}
	return ""
}

func archiveBase(name string) string {
	lower := strings.ToLower(name)
	for _, ext := range append([]string{".tar.gz", ".tar.bz2", ".tgz", ".tbz2", ".tar", ".zip", ".apk", ".aab", ".apks", ".xapk"}, externalExts...) {
		if strings.HasSuffix(lower, ext) {
			return name[:len(name)-len(ext)]
		}
	}
	return name + "_files"
}

func tarReader(f *os.File, kind string) (*tar.Reader, error) {
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	var r io.Reader = f
	switch kind {
	case "tgz":
		gz, err := gzip.NewReader(f)
		if err != nil {
			return nil, fmt.Errorf("not a valid .tar.gz: %w", err)
		}
		r = gz
	case "tbz":
		r = bzip2.NewReader(f)
	}
	return tar.NewReader(r), nil
}

type entryName struct {
	name string
	dir  bool
}

func unsafeName(name string) bool {
	name = strings.ReplaceAll(name, "\\", "/")
	if strings.HasPrefix(name, "/") || (len(name) > 1 && name[1] == ':') {
		return true
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return true
		}
	}
	return false
}

func commonTop(entries []entryName) string {
	top := ""
	for _, e := range entries {
		if unsafeName(e.name) {
			continue
		}
		n := strings.TrimPrefix(path.Clean("/"+strings.ReplaceAll(e.name, "\\", "/")), "/")
		if n == "" {
			continue
		}
		first, rest, _ := strings.Cut(n, "/")
		if rest == "" && !e.dir {
			return ""
		}
		if top == "" {
			top = first
		} else if first != top {
			return ""
		}
	}
	return top
}

type extractor struct {
	r       *Registry
	session string
	dest    string
	strip   string
	files   int
	bytes   int64
	budget  int64
	skipped int
}

func (x *extractor) target(name string) (string, bool) {
	if unsafeName(name) {
		x.skipped++
		return "", false
	}
	name = strings.TrimPrefix(path.Clean("/"+strings.ReplaceAll(name, "\\", "/")), "/")
	if x.strip != "" {
		if name == x.strip {
			return "", false
		}
		name = strings.TrimPrefix(name, x.strip+"/")
	}
	if name == "" || !fs.ValidPath(name) {
		x.skipped++
		return "", false
	}
	if x.dest != "." {
		name = x.dest + "/" + name
	}
	return name, true
}

func (x *extractor) write(name string, src io.Reader) error {
	if x.files++; x.files > maxArchiveFiles {
		return fmt.Errorf("the archive has more than %d files", maxArchiveFiles)
	}
	f, err := x.r.Machine.Create(x.session, name)
	if err != nil {
		return fmt.Errorf("%s: %s", name, friendlyFSError(err))
	}
	n, err := io.Copy(f, io.LimitReader(src, x.budget-x.bytes+1))
	f.Close()
	x.bytes += n
	if x.bytes > x.budget {
		return fmt.Errorf("the unpacked files would exceed the free space (%s)", humanSize(x.budget))
	}
	return err
}

func (x *extractor) mkdir(name string) {
	if root, err := x.r.Machine.OpenRoot(x.session); err == nil {
		_ = root.MkdirAll(name, 0o700)
		root.Close()
	}
}

func (r *Registry) extractArchive(ctx context.Context, sessionID string, args map[string]any) (string, error) {
	name := strArg(args, "filename")
	if name == "" {
		return "error: filename is required", nil
	}
	password := rawStrArg(args, "password")
	if boolArg(args, "list") {
		return r.listArchive(ctx, sessionID, name, password), nil
	}
	dest := strArg(args, "dest")
	if dest == "" {
		dest = archiveBase(path.Base(name))
	}
	return r.extract(ctx, sessionID, name, dest, password), nil
}

func (r *Registry) extract(ctx context.Context, sessionID, name, dest, password string) string {
	kind := archiveKind(name)
	if kind == "" {
		return "error: unsupported archive: use .zip, .rar, .7z, .tar(.gz/.bz2/.xz/.zst), .gz, .bz2, .xz or .iso"
	}
	dest, err := cleanPath(dest)
	if err != nil {
		return "error: " + err.Error()
	}
	if kind == "ext" || password != "" || r.zipNeedsArchiver(sessionID, name, kind) {
		return r.extractExternal(ctx, sessionID, name, dest, password)
	}
	f, info, err := r.Machine.Open(sessionID, name)
	if err != nil {
		return "error: " + friendlyFSError(err)
	}
	defer f.Close()
	x := &extractor{r: r, session: sessionID, dest: dest, budget: min(r.Machine.FreeSpace(), maxArchiveBytes)}
	fail := func(err error) string {
		return fmt.Sprintf("error: %v (%d files were unpacked before this)", err, max(0, x.files-1))
	}

	if kind == "zip" {
		zr, err := zip.NewReader(f, info.Size())
		if err != nil {
			return "error: not a valid zip: " + err.Error()
		}
		var names []entryName
		for _, zf := range zr.File {
			names = append(names, entryName{zipName(zf), zf.FileInfo().IsDir()})
		}
		x.strip = commonTop(names)
		for _, zf := range zr.File {
			mode := zf.Mode()
			if mode&fs.ModeSymlink != 0 || (!mode.IsRegular() && !mode.IsDir()) {
				x.skipped++
				continue
			}
			target, ok := x.target(zipName(zf))
			if !ok {
				continue
			}
			if mode.IsDir() {
				x.mkdir(target)
				continue
			}
			rc, err := zf.Open()
			if err != nil {
				return fail(err)
			}
			err = x.write(target, rc)
			rc.Close()
			if err != nil {
				return fail(err)
			}
		}
	} else {

		tr, err := tarReader(f, kind)
		if err != nil {
			return "error: " + err.Error()
		}
		var names []entryName
		for {
			h, err := tr.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return "error: the archive is damaged: " + err.Error()
			}

			//lint:ignore SA1019 archives written by old tar programs still mark files this way
			if h.Typeflag == tar.TypeDir || h.Typeflag == tar.TypeReg || h.Typeflag == tar.TypeRegA {
				names = append(names, entryName{h.Name, h.Typeflag == tar.TypeDir})
			}
		}
		x.strip = commonTop(names)
		if tr, err = tarReader(f, kind); err != nil {
			return "error: " + err.Error()
		}
		for {
			h, err := tr.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			if err != nil {
				return fail(fmt.Errorf("the archive is damaged: %w", err))
			}
			switch h.Typeflag {
			case tar.TypeXGlobalHeader, tar.TypeXHeader:
				continue
			case tar.TypeDir:
				if target, ok := x.target(h.Name); ok {
					x.mkdir(target)
				}

			//lint:ignore SA1019 archives written by old tar programs still mark files this way
			case tar.TypeReg, tar.TypeRegA:
				if target, ok := x.target(h.Name); ok {
					if err := x.write(target, tr); err != nil {
						return fail(err)
					}
				}
			default:
				x.skipped++
			}
		}
	}
	where := dest + "/"
	if dest == "." {
		where = "the workspace root"
	}
	if x.files == 0 && x.skipped > 0 {
		return fmt.Sprintf("error: nothing was unpacked: all %d entries are links, special files or unsafe paths", x.skipped)
	}
	s := fmt.Sprintf("Unpacked %d files (%s) into %s.", x.files, humanSize(x.bytes), where)
	if x.skipped > 0 {
		s += fmt.Sprintf(" %d links, special files or unsafe paths were skipped.", x.skipped)
	}
	return s + " Map it with project_tree."
}

var (
	githubRe = regexp.MustCompile(`^(?:https?://)?(?:www\.)?github\.com/([\w.-]+)/([\w.-]+?)(?:\.git)?(?:/(?:tree|commit|releases/tag)/([^?#]+))?/?(?:[?#].*)?$`)
	gitlabRe = regexp.MustCompile(`^(?:https?://)?gitlab\.com/([\w.-]+(?:/[\w.-]+)+?)(?:\.git)?(?:/-/tree/([^?#]+))?/?(?:[?#].*)?$`)
)

func (r *Registry) importRepo(ctx context.Context, sessionID string, args map[string]any) (string, error) {
	raw, ref, dest := strArg(args, "url"), strArg(args, "ref"), strArg(args, "dest")
	if raw == "" {
		return "error: url is required", nil
	}
	var archiveURL, archiveName, repoName string
	header := http.Header{}
	switch {
	case githubRe.MatchString(raw):
		m := githubRe.FindStringSubmatch(raw)
		owner, repo := m[1], m[2]
		if ref == "" {
			ref = m[3]
		}
		repoName, archiveName = repo, repo+".tar.gz"
		archiveURL = "https://api.github.com/repos/" + owner + "/" + repo + "/tarball"
		if ref != "" {
			archiveURL += "/" + url.PathEscape(ref)
		}
		header.Set("Accept", "application/vnd.github+json")
		if r.GitHubToken != "" {
			header.Set("Authorization", "Bearer "+r.GitHubToken)
		}
	case gitlabRe.MatchString(raw):
		m := gitlabRe.FindStringSubmatch(raw)
		if ref == "" {
			ref = m[2]
		}
		repoName = path.Base(m[1])
		archiveName = repoName + ".tar.gz"
		archiveURL = "https://gitlab.com/api/v4/projects/" + url.PathEscape(m[1]) + "/repository/archive.tar.gz"
		if ref != "" {
			archiveURL += "?sha=" + url.QueryEscape(ref)
		}
	default:
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
			return "error: give a GitHub/GitLab repository link or a direct https link to a .zip/.tar.gz archive", nil
		}
		archiveName = path.Base(u.Path)
		if archiveBase(archiveName) == archiveName+"_files" {
			return "error: this link is not a repository or an archive (.zip, .tar.gz, .tgz, .tar.bz2, .tar)", nil
		}
		archiveURL, repoName = u.String(), archiveBase(archiveName)
	}
	if dest == "" {
		dest = repoName
	}
	tmp := fmt.Sprintf(".import-%d-%s", time.Now().UnixNano(), archiveName)
	defer r.Machine.Remove(sessionID, tmp)

	dctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	req, err := http.NewRequestWithContext(dctx, http.MethodGet, archiveURL, nil)
	if err != nil {
		return "error: " + err.Error(), nil
	}
	req.Header = header
	req.Header.Set("User-Agent", "Macron-Agent")
	resp, err := r.HTTP.Do(req)
	if err != nil {
		return "error: download failed: " + err.Error(), nil
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return "error: repository or ref not found (private repositories cannot be imported without access)", nil
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
		return fmt.Sprintf("error: the host refused the download (HTTP %d, probably a rate limit); try again later", resp.StatusCode), nil
	case resp.StatusCode/100 != 2:
		return fmt.Sprintf("error: download failed (HTTP %d)", resp.StatusCode), nil
	}
	limit := min(r.Machine.FreeSpace(), maxImportBytes)
	out, err := r.Machine.Create(sessionID, tmp)
	if err != nil {
		return "error: " + friendlyFSError(err), nil
	}
	n, err := io.Copy(out, io.LimitReader(resp.Body, limit+1))
	out.Close()
	switch {
	case err != nil:
		return "error: download interrupted: " + err.Error(), nil
	case n > limit:
		return fmt.Sprintf("error: the archive is bigger than the free space or the %s limit", humanSize(maxImportBytes)), nil
	}
	msg := r.extract(ctx, sessionID, tmp, dest, "")
	if strings.HasPrefix(msg, "error") {
		return msg, nil
	}
	return fmt.Sprintf("Imported %s (%s download). %s", raw, humanSize(n), msg), nil
}

func IsArchive(name string) bool { return archiveKind(name) != "" }

func IsAndroidPackage(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".apk", ".aab", ".apks", ".xapk":
		return true
	}
	return false
}
