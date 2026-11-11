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

const (
	ToolRunCode = "run_code"

	scratchDir        = ".run"
	maxCodeBytes      = 512 << 10
	maxShownOutput    = 12000
	installTimeout    = 4 * time.Minute
	maxRunSeconds     = 7200
	maxBashSeconds    = 7200
	maxInstallCount   = 10
	maxProjectInstall = 80
)

type language struct {
	Name    string
	Program string
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

	r.add(ToolPythonKernel,
		"Run code in a persistent Python kernel session that retains state, variables, and imports across turns.",
		map[string]any{
			"code":   prop("string", "Python code to execute or evaluate"),
			"action": enumProp("execute", "execute", "reset", "variables"),
		}, nil, func(ctx context.Context, sessionID string, args map[string]any) (string, error) {
			code := strArg(args, "code")
			action := strArg(args, "action")
			if action == "" || action == "execute" {
				if refused := r.gateCode(ctx, sessionID, "python", code); refused != "" {
					return refused, nil
				}
			}
			return r.ExecuteInPythonKernel(ctx, sessionID, action, code)
		})

	r.add(ToolGitHubWorkflow,
		"Manage GitHub pull requests, issues, and discussions.",
		map[string]any{
			"action": enumProp("create_pr", "create_pr", "list_issues", "create_issue", "add_comment"),
			"repo":   prop("string", "GitHub repository (owner/repo)"),
			"title":  prop("string", "PR or Issue title"),
			"body":   prop("string", "PR or Issue body description"),
			"head":   prop("string", "Head branch name"),
			"base":   prop("string", "Base branch name (default 'main')"),
			"number": prop("integer", "Issue/PR number (for add_comment)"),
		}, []string{"action", "repo"}, func(ctx context.Context, sessionID string, args map[string]any) (string, error) {
			action := strArg(args, "action")
			repo := strArg(args, "repo")
			title := strArg(args, "title")
			body := strArg(args, "body")
			head := strArg(args, "head")
			base := strArg(args, "base")
			number := intArg(args, "number", 0, 0, 1000000)
			if action != "list_issues" {
				if refused := r.gateSend(ctx, sessionID, "GitHub "+repo, action+": "+title+"\n"+body); refused != "" {
					return refused, nil
				}
			}
			return r.ExecuteGitHubWorkflow(ctx, sessionID, action, repo, title, body, base, head, number)
		})

	r.add(ToolLivePreview,
		"Serve a workspace folder over HTTP on this server (127.0.0.1) to preview a static site in a browser.",
		map[string]any{
			"action":  enumProp("start", "start", "stop"),
			"port":    prop("integer", "Port (default: a free one)"),
			"sub_dir": prop("string", "Folder to serve (default: the workspace)"),
		}, nil, func(_ context.Context, sessionID string, args map[string]any) (string, error) {
			if strArg(args, "action") == "stop" {
				if StopLivePreview(sessionID) {
					return "Preview stopped.", nil
				}
				return "No preview was running.", nil
			}
			out, err := r.StartLivePreview(sessionID, intArg(args, "port", 0, 0, 65535), strArg(args, "sub_dir"))
			if err != nil {
				return "error: " + err.Error(), nil
			}
			return out, nil
		})
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
	if name == "typescript" || name == "ts" {
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
	if lang.Name == "bash" {
		script := code
		if strings.TrimSpace(script) == "" {
			if b, _, err := r.Machine.ReadFile(sessionID, file, maxCodeBytes); err == nil {
				script = string(b)
			}
		}
		if refused := r.gateCommand(ctx, sessionID, script); refused != "" {
			return refused, nil
		}
	} else {
		program := code
		if strings.TrimSpace(program) == "" {
			if b, _, err := r.Machine.ReadFile(sessionID, file, maxCodeBytes); err == nil {
				program = string(b)
			}
		}
		if refused := r.gateCode(ctx, sessionID, lang.Name, program); refused != "" {
			return refused, nil
		}
	}
	if strings.TrimSpace(code) != "" && file != "" {
		if refused := r.gatePath(ctx, sessionID, ToolRunCode, file); refused != "" {
			return refused, nil
		}
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
	if dir := r.goPackageDir(sessionID, lang, file); dir != "" {
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
		Tee:         outputTee(ctx),
		InSession:   true,
	})
	out := terminalTranscript(shown, res, true)
	if files := r.changedFiles(sessionID, before); len(files) > 0 {
		out += "\n[new or changed files in the workspace: " + strings.Join(files, ", ") + "]"
	}
	return out, nil
}

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

const TranscriptPrefix = "$ "

func terminalTranscript(cmd string, res *machine.ExecutionResult, hints bool) string {
	var sb strings.Builder
	sb.WriteString(TranscriptPrefix + cmd + "\n")
	out := strings.TrimRight(res.Stdout, "\n")
	if errOut := strings.TrimRight(res.Stderr, "\n"); errOut != "" {
		if out != "" {
			out += "\n"
		}
		out += "[stderr]\n" + errOut
	}
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

func argList(v any) []string {
	if s, ok := v.(string); ok && !strings.HasPrefix(strings.TrimSpace(s), "[") {
		return strings.Fields(s)
	}
	return anyList(v)
}

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
		case strings.HasPrefix(x, "["):
			var list []any
			if json.Unmarshal([]byte(x), &list) == nil {
				return anyList(list)
			}
			out = strings.Fields(x)
		case strings.Contains(x, "\n"), strings.Contains(x, ","):
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

func argFile(name string) string {
	if strings.HasPrefix(name, "-") {
		return "./" + name
	}
	return name
}

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
