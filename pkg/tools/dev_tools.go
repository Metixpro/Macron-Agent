package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/machine"
)

// Developer tools: run code like a terminal and install packages into the
// user's own environment.

const (
	ToolRunCode = "run_code"

	scratchDir        = ".run"
	maxCodeBytes      = 512 << 10
	maxShownOutput    = 12000 // the end of a background job's log the model is shown
	installTimeout    = 4 * time.Minute
	maxRunSeconds     = 7200 // run_code
	maxBashSeconds    = 7200 // bash_execute
	maxInstallCount   = 10
	maxProjectInstall = 80 // from a project's manifest
)

// language describes how to run one kind of source file.
type language struct {
	Name    string
	Program string // the program that must be installed
	Ext     string
	argv    func(file string, args []string) []string
	display func(file string) string
	env     map[string]string
}

var languages = []language{
	{Name: "python", Program: "python3", Ext: ".py",
		argv: func(f string, a []string) []string { return append([]string{"python3", f}, a...) }},
	{Name: "bash", Program: "bash", Ext: ".sh",
		argv: func(f string, a []string) []string { return append([]string{"bash", f}, a...) }},
	{Name: "node", Program: "node", Ext: ".js",
		argv: func(f string, a []string) []string { return append([]string{"node", f}, a...) }},
	{Name: "deno", Program: "deno", Ext: ".ts",
		argv: func(f string, a []string) []string {
			return append([]string{"deno", "run", "--quiet", "--no-prompt", "--allow-all", f}, a...)
		},
		display: func(f string) string { return "deno run " + f },
		env:     map[string]string{"DENO_NO_UPDATE_CHECK": "1"}},
	{Name: "bun", Program: "bun", Ext: ".ts",
		argv:    func(f string, a []string) []string { return append([]string{"bun", "run", f}, a...) },
		display: func(f string) string { return "bun run " + f },
		env:     map[string]string{"DO_NOT_TRACK": "1"}},
	{Name: "php", Program: "php", Ext: ".php",
		argv: func(f string, a []string) []string { return append([]string{"php", f}, a...) }},
	{Name: "ruby", Program: "ruby", Ext: ".rb",
		argv: func(f string, a []string) []string { return append([]string{"ruby", f}, a...) }},
	{Name: "perl", Program: "perl", Ext: ".pl",
		argv: func(f string, a []string) []string { return append([]string{"perl", f}, a...) }},
	{Name: "go", Program: "go", Ext: ".go",
		argv: func(f string, a []string) []string { return append([]string{"go", "run", f}, a...) },
	},
	{Name: "c", Program: "gcc", Ext: ".c",
		argv: func(f string, a []string) []string {
			return append([]string{"bash", "-c", `gcc -O2 -o "${TMPDIR:-/tmp}/macron-prog-$$" "$1" -lm && exec "${TMPDIR:-/tmp}/macron-prog-$$" "${@:2}"`, "cc", f}, a...)
		},
		display: func(f string) string { return "gcc " + f + " -o prog && ./prog" }},
	{Name: "cpp", Program: "g++", Ext: ".cpp",
		argv: func(f string, a []string) []string {
			return append([]string{"bash", "-c", `g++ -std=c++17 -O2 -o "${TMPDIR:-/tmp}/macron-prog-$$" "$1" && exec "${TMPDIR:-/tmp}/macron-prog-$$" "${@:2}"`, "c++", f}, a...)
		},
		display: func(f string) string { return "g++ " + f + " -o prog && ./prog" }},
	{Name: "java", Program: "java", Ext: ".java",
		// A single source file runs as it is (JEP 330).
		argv: func(f string, a []string) []string {
			return append([]string{"java", "-Dfile.encoding=UTF-8", f}, a...)
		},
		display: func(f string) string { return "java " + f }},
	{Name: "rust", Program: "rustc", Ext: ".rs",
		argv: func(f string, a []string) []string {
			return append([]string{"bash", "-c", `rustc --edition 2021 -O -o "${TMPDIR:-/tmp}/macron-prog-$$" "$1" && exec "${TMPDIR:-/tmp}/macron-prog-$$" "${@:2}"`, "rust", f}, a...)
		},
		display: func(f string) string { return "rustc -O " + f + " -o prog && ./prog" }},
}

var languageAliases = map[string]string{
	"py": "python", "python3": "python", "sh": "bash", "shell": "bash", "js": "node", "javascript": "node",
	"nodejs": "node", "rb": "ruby", "pl": "perl", "golang": "go", "c++": "cpp", "cxx": "cpp", "cc": "cpp", "rs": "rust",
}

var extAliases = map[string]string{".mjs": "node", ".cjs": "node", ".cc": "cpp", ".cxx": "cpp", ".bash": "bash", ".python": "python",
	".mts": "typescript", ".tsx": "typescript"}

func (r *Registry) registerDevTools() {
	have := r.Machine.Available("python3", "bash", "node", "deno", "bun", "php", "ruby", "perl", "go", "gcc", "g++", "rustc", "gofmt", "git", "java")
	r.syntax = have
	if have["git"] {
		r.add(ToolApplyPatch,
			"Apply a unified diff (git diff format) to several files at once, new and deleted ones too; all or nothing. Code files get a syntax check.",
			map[string]any{
				"patch": prop("string", "--- a/file / +++ b/file / @@ hunks"),
				"path":  prop("string", "The folder its paths are relative to (default: the workspace)"),
			}, []string{"patch"}, r.applyPatch)
	}
	r.langs = map[string]language{}
	var names []string
	for _, l := range languages {
		if have[l.Program] {
			r.langs[l.Name] = l
			names = append(names, l.Name)
		}
	}
	if len(names) == 0 {
		return
	}
	runDesc := "Run a program on this machine and get its real output (stdout and stderr in order) and exit code. " +
		"code: a snippet; filename + code: save it and run; filename alone: run that file."
	r.add(ToolRunCode, runDesc,
		map[string]any{
			"language":        enumProp("Optional when the extension tells it", names...),
			"code":            prop("string", ""),
			"filename":        prop("string", "e.g. 'app_fixed.py'"),
			"args":            arrayProp("string", ""),
			"stdin":           prop("string", ""),
			"timeout_seconds": prop("integer", "Default 60, max 7200"),
		}, nil, r.runCode)
}

func (r *Registry) pickLanguage(name, file string) (language, error) {
	name = strings.ToLower(strings.TrimSpace(name))
	if a, ok := languageAliases[name]; ok {
		name = a
	}
	if name == "" && path.Base(file) == "Cargo.toml" {
		name = "rust"
	}
	if name == "" && file != "" {
		ext := strings.ToLower(path.Ext(file))
		if a, ok := extAliases[ext]; ok {
			name = a
		}
		for _, l := range languages {
			if _, ok := r.langs[l.Name]; l.Ext == ext && (ok || name == "") {
				name = l.Name
				if ok {
					break
				}
			}
		}
	}
	if name == "typescript" || name == "ts" { // the runtime that is installed
		name = "deno"
		if _, ok := r.langs["deno"]; !ok {
			name = "bun"
		}
	}
	if name == "" {
		return language{}, fmt.Errorf("language is required (one of: %s)", strings.Join(r.languageNames(), ", "))
	}
	l, ok := r.langs[name]
	if !ok {
		return language{}, fmt.Errorf("%s is not available here (available: %s)", name, strings.Join(r.languageNames(), ", "))
	}
	return l, nil
}

func (r *Registry) languageNames() []string {
	var out []string
	for n := range r.langs {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func (r *Registry) runCode(ctx context.Context, sessionID string, args map[string]any) (string, error) {
	code := rawStrArg(args, "code")
	file := strings.TrimPrefix(strings.TrimSpace(strArg(args, "filename")), "./")
	file = strings.TrimPrefix(file, machine.MountPoint+"/")
	if strings.TrimSpace(code) == "" && file == "" {
		return "error: give 'code' to run, or the 'filename' of a workspace file", nil
	}
	if len(code) > maxCodeBytes {
		return "error: the code is larger than 512 KB", nil
	}
	lang, err := r.pickLanguage(strArg(args, "language"), file)
	if err != nil {
		return "error: " + err.Error(), nil
	}

	switch {
	case strings.TrimSpace(code) != "" && file != "":
		if err := r.Machine.WriteFile(sessionID, file, []byte(code)); err != nil {
			return "error: could not save the file: " + friendlyFSError(err), nil
		}
	case strings.TrimSpace(code) != "":
		file = scratchDir + "/main" + lang.Ext
		if err := r.Machine.WriteFile(sessionID, file, []byte(code)); err != nil {
			return "error: could not save the code: " + friendlyFSError(err), nil
		}
	default:
		if _, err := r.Machine.Stat(sessionID, file); err != nil {
			return "error: " + file + ": " + friendlyFSError(err), nil
		}
	}

	argv := argList(args["args"])
	timeout := time.Duration(intArg(args, "timeout_seconds", 60, 1, maxRunSeconds)) * time.Second
	run := lang.argv(argFile(file), argv)
	shown, env := commandLine(lang, file, argv), lang.env
	if dir := r.goPackageDir(sessionID, lang, file); dir != "" { // a module: the whole package, its dependencies
		run = append([]string{"bash", "-c", `cd "$1" && shift && exec go run . "$@"`, "go", dir}, argv...)
		shown = "cd " + shellQuote(dir) + " && go run ."
		for _, a := range argv {
			shown += " " + shellQuote(a)
		}
	}
	before := r.rootSnapshot(sessionID)
	res := r.Machine.Run(ctx, sessionID, machine.RunOptions{
		Args:        run,
		Stdin:       []byte(rawStrArg(args, "stdin")),
		Timeout:     timeout,
		Env:         env,
		MergeOutput: true,
		Tee:         outputTee(ctx), // the live view shows the last line printed
		InSession:   true,           // in the conversation's shell, beside its background jobs
	})
	out := terminalTranscript(shown, res, true)
	if files := r.changedFiles(sessionID, before); len(files) > 0 {
		out += "\n[new or changed files in the workspace: " + strings.Join(files, ", ") + "]"
	}
	return out, nil
}

// commandLine is the command as a person would type it.
func commandLine(l language, file string, args []string) string {
	shown := strings.TrimPrefix(file, scratchDir+"/")
	cmd := ""
	if l.display != nil {
		cmd = l.display(shown)
	} else {
		cmd = strings.Join(l.argv(shown, nil), " ")
	}
	for _, a := range args {
		cmd += " " + shellQuote(a)
	}
	return cmd
}

func shellQuote(s string) string {
	if s != "" && !strings.ContainsAny(s, " \t\n'\"\\$`!*?&|;<>(){}[]#~") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// TranscriptPrefix starts every terminal transcript; front-ends use it to
// recognise one.
const TranscriptPrefix = "$ "

// terminalTranscript renders a run the way a terminal shows it:
//
//	$ python3 app.py
//	...output...
//	[exit 1 · 52ms]
func terminalTranscript(cmd string, res *machine.ExecutionResult, hints bool) string {
	var sb strings.Builder
	sb.WriteString(TranscriptPrefix + cmd + "\n")
	out := res.Stdout
	if res.Stderr != "" {
		out = strings.TrimRight(out, "\n")
		if out != "" {
			out += "\n"
		}
		out += res.Stderr
	}
	out = strings.TrimRight(out, "\n") // whole: Execute caps it and keeps the rest in a file (KeepWhole)
	if out != "" {
		sb.WriteString(out + "\n")
	}
	switch {
	case res.ExitCode == 124 && res.Error != "":
		fmt.Fprintf(&sb, "[timed out: %s — the program was stopped]", res.Error)
	case res.ExitCode == -1 && res.Error != "":
		fmt.Fprintf(&sb, "[could not run: %s]", res.Error)
	default:
		fmt.Fprintf(&sb, "[exit %d · %s]", res.ExitCode, res.Duration)
	}
	if res.Truncated {
		sb.WriteString("\n[output was longer than 64 KB and was cut]")
	}
	if hints {
		if h := missingModuleHint(out); h != "" {
			sb.WriteString("\n" + h)
		}
	}
	return sb.String()
}

var (
	pyMissingRe   = regexp.MustCompile(`ModuleNotFoundError: No module named '([A-Za-z0-9_.]+)'`)
	nodeMissingRe = regexp.MustCompile(`Cannot find module '([@A-Za-z0-9_./-]+)'`)
)

// pipNames maps import names to the package that provides them.
var pipNames = map[string]string{
	"cv2": "opencv-python-headless", "PIL": "pillow", "sklearn": "scikit-learn", "yaml": "pyyaml", "bs4": "beautifulsoup4",
	"dateutil": "python-dateutil", "dotenv": "python-dotenv", "telegram": "python-telegram-bot", "Crypto": "pycryptodome",
	"jwt": "pyjwt", "magic": "python-magic", "docx": "python-docx", "pptx": "python-pptx", "fitz": "pymupdf",
}

func missingModuleHint(out string) string {
	if m := pyMissingRe.FindStringSubmatch(out); m != nil {
		mod := strings.Split(m[1], ".")[0]
		pkg := mod
		if p, ok := pipNames[mod]; ok {
			pkg = p
		}
		return fmt.Sprintf("[hint: module %q is missing — pip install %s (a virtualenv, or --break-system-packages), then run again]", mod, pkg)
	}
	if m := nodeMissingRe.FindStringSubmatch(out); m != nil && !strings.HasPrefix(m[1], ".") && !strings.HasPrefix(m[1], "/") {
		return fmt.Sprintf("[hint: module %q is missing — npm install %s, then run again]", m[1], m[1])
	}
	return ""
}

// argList is a program's arguments: a list, or one string split at spaces
// (a comma belongs to its argument: --format=csv,json).
func argList(v any) []string {
	if s, ok := v.(string); ok && !strings.HasPrefix(strings.TrimSpace(s), "[") {
		return strings.Fields(s)
	}
	return anyList(v)
}

// anyList is a list argument however it was sent: a list, a list as JSON
// text, one item per line or per comma, or words.
func anyList(v any) []string {
	var out []string
	switch x := v.(type) {
	case []any:
		for _, e := range x {
			switch s := e.(type) {
			case string:
				out = append(out, s)
			case float64, bool:
				out = append(out, fmt.Sprint(s))
			}
		}
	case string:
		x = strings.TrimSpace(x)
		switch {
		case x == "":
		case strings.HasPrefix(x, "["): // a list sent as JSON text
			var list []any
			if json.Unmarshal([]byte(x), &list) == nil {
				return anyList(list)
			}
			out = strings.Fields(x)
		case strings.Contains(x, "\n"), strings.Contains(x, ","): // one per line, or "a.pdf, my file.pdf"
			for _, part := range strings.FieldsFunc(x, func(r rune) bool { return r == '\n' || r == ',' }) {
				if part = strings.TrimSpace(part); part != "" {
					out = append(out, part)
				}
			}
		default:
			out = strings.Fields(x)
		}
	}
	return out
}

// argFile keeps a file name from being read as an option.
func argFile(name string) string {
	if strings.HasPrefix(name, "-") {
		return "./" + name
	}
	return name
}

// goPackageDir is the folder of a Go file inside a module (a go.mod above
// it): run_code runs its whole package (main split over files) with the
// module's dependencies. "" for a file on its own.
func (r *Registry) goPackageDir(sessionID string, lang language, file string) string {
	if lang.Name != "go" || file == "" || strings.HasPrefix(file, scratchDir+"/") {
		return ""
	}
	for dir := path.Dir(file); ; dir = path.Dir(dir) {
		if info, err := r.Machine.Stat(sessionID, path.Join(dir, "go.mod")); err == nil && info.Mode().IsRegular() {
			return path.Dir(file)
		}
		if dir == "." || dir == "/" {
			return ""
		}
	}
}
