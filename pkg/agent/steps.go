package agent

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Metixpro/macron-agent/pkg/textutil"
)

type step struct {
	tool   string
	text   string
	look   string
	failed bool
}

var lookVerbs = map[string]string{
	"document_reader": "read the document", "pdf_tool": "read the PDF",
	"read_file": "read", "list_files": "listed the workspace", "search_code": "searched the code for", "find_files": "looked for files",
	"project_tree": "mapped", "code_outline": "outlined", "view_image": "looked at", "web_search": "searched the web for",
	"web_fetch": "read the page", "read_skill": "read the skill", "list_skills": "listed the skills", "more_tools": "asked for more tools",
	"tool_search": "looked up tools", "search_past_chats": "searched past chats for", "shell_jobs": "checked background jobs",
}

const (
	stepRunes    = 180
	outcomeRunes = 110
	quoteRunes   = 48
)

var (
	exitLineRe   = regexp.MustCompile(`^\[(?:exit (-?\d+)[^\]]*|timed out[^\]]*|could not run: [^\]]*)\]$`)
	boilerRe     = regexp.MustCompile(`(?i)\s*(?:it|they) reach(?:es)? the user only if you send (?:it|them)[^.:]*[.:]?|\s*send it with send_file\.?|\s*map it with project_tree\.?|\s*\(look with view_image, deliver with send_file\)`)
	genericRe    = regexp.MustCompile(`(?i)^(?:chart|audio|images?|pdf|qr code|media|video|file)s? (?:created|converted|downloaded)[^.]*\.?$|^downloaded \d+ file\(s\)|^done\b|^media converted|^tables\b`)
	queuedRe     = regexp.MustCompile(`(?i)\s+and queued for delivery`)
	bulletRe     = regexp.MustCompile(`^\s*(?:[-•*]|\d+[.)])\s+`)
	appliedRe    = regexp.MustCompile(`Applied to \d+ files?: ([^\n]+)`)
	nowSizeRe    = regexp.MustCompile(`\(now ([^)]+)\)`)
	startedJobRe = regexp.MustCompile(`^Started background job (\d+)`)
)

func StepLine(name, args, result string) string {
	s := describeStep(name, args, result)
	if s.look != "" {
		verb := lookVerbs[name]
		if s.look == "-" {
			return verb
		}
		return verb + " " + s.look
	}
	return s.text
}

func StepFailed(name, args, result string) bool { return describeStep(name, args, result).failed }

func describeStep(name, argsJSON, result string) step {
	a := map[string]any{}
	_ = json.Unmarshal([]byte(argsJSON), &a)
	s := step{tool: name, failed: failedOutput(result) || execFailedRe.MatchString(result)}
	str := func(k string) string {
		v, _ := a[k].(string)
		return strings.TrimSpace(v)
	}
	switch name {
	case "write_file":
		file := str("filename")
		switch {
		case s.failed:
			s.text = "writing " + file + " failed: " + outcome(result)
		case boolValue(a["append"]):
			s.text = "appended to " + file
			if m := nowSizeRe.FindStringSubmatch(result); m != nil {
				s.text += " (now " + m[1] + ")"
			}
		default:
			content, _ := a["content"].(string)
			s.text = fmt.Sprintf("wrote %s (%s)", file, linesWord(content))
		}
		s.text += syntaxFlag(result)
	case "edit_file":
		file := str("filename")
		if s.failed {
			s.text = "editing " + file + " failed: " + outcome(result)
			break
		}
		changes := editPairs(a)
		switch len(changes) {
		case 0:
			s.text = "edited " + file
		case 1:
			s.text = "edited " + file + ": " + changes[0]
		default:
			s.text = fmt.Sprintf("edited %s (%d changes): %s", file, len(changes), strings.Join(changes, "; "))
		}
		s.text += syntaxFlag(result)
	case "apply_patch":
		if m := appliedRe.FindStringSubmatch(result); m != nil && !s.failed {
			s.text = "patched " + strings.TrimSpace(m[1])
		} else {
			s.text = "patch failed: " + execTail(result, 1)
		}
		s.text += syntaxFlag(result)
	case "bash_execute":
		cmd := str("command")
		if boolValue(a["background"]) {
			if m := startedJobRe.FindStringSubmatch(result); m != nil {
				s.text = "started background job " + m[1] + ": " + commandWord(cmd)
			} else {
				s.text = "starting in the background failed: " + commandWord(cmd) + " → " + outcome(result)
			}
			break
		}
		s.text = execLine(commandWord(cmd), result)
	case "run_code":
		label := strings.TrimPrefix(firstLine(result), "$ ")
		if !strings.HasPrefix(firstLine(result), "$ ") {
			label = commandWord(str("filename") + " " + str("language"))
		}
		s.text = execLine(label, result)
	case "shell_jobs":
		switch str("action") {
		case "kill":
			s.text = outcome(result)
		default:
			s.look = "-"
		}
	case "read_file":
		file := str("filename")
		if list, ok := a["files"].([]any); ok && len(list) > 0 {
			file += fmt.Sprintf(" and %d more", len(list))
		}
		if from, to := intValue(a["start_line"]), intValue(a["end_line"]); from > 0 || to > 0 {
			file += fmt.Sprintf(" (lines %d-%d)", max(from, 1), to)
		}
		s.look = file
	case "search_code", "find_files":
		s.look = "«" + textutil.TruncateRunes(textutil.OneLine(str("pattern")), quoteRunes, "…") + "»"
	case "project_tree", "code_outline":
		s.look = orWord(str("path"), "the workspace")
	case "view_image":
		s.look = orWord(str("filename"), orWord(str("file"), "a picture"))
	case "web_search", "search_past_chats":
		s.look = "«" + textutil.TruncateRunes(textutil.OneLine(str("query")), quoteRunes, "…") + "»"
	case "web_fetch":
		s.look = textutil.TruncateRunes(str("url"), 70, "…")
	case "read_skill":
		s.look = orWord(str("name"), "-")
	case "list_files", "list_skills", "more_tools", "tool_search":
		s.look = "-"
	case "document_reader":
		s.look = orWord(str("filename"), "-")
	case "pdf_tool":
		if str("action") == "read" {
			s.look = orWord(str("filename"), "-")
			break
		}
		s.text = actionLabel(name, a) + " → " + outcome(result)
	case "query_data":
		s.text = "query " + quote(orWord(str("sql"), "tables")) + " → " + rowsOutcome(result)
	case ToolDelegate:
		s.text = "sub-agent «" + textutil.TruncateRunes(textutil.OneLine(orWord(str("title"), str("goal"))), 60, "…") + "» → " + outcome(result)
	default:
		s.text = actionLabel(name, a) + " → " + outcome(result)
	}
	if s.look != "" && s.failed {
		s.text, s.look = lookVerbs[name]+" "+strings.TrimPrefix(s.look, "-")+" failed: "+outcome(result), ""
	}
	s.text = textutil.TruncateRunes(strings.TrimSpace(s.text), stepRunes, "…")
	return s
}

var rowsRe = regexp.MustCompile(`(?m)^\((\d+ rows?)[^)]*\)\s*$`)

func rowsOutcome(result string) string {
	if m := rowsRe.FindStringSubmatch(result); m != nil {
		return m[1]
	}
	return outcome(result)
}

func actionLabel(name string, a map[string]any) string {
	label := name
	if v, ok := a["action"].(string); ok && strings.TrimSpace(v) != "" {
		label += " " + strings.TrimSpace(v)
	}
	return label
}

func orWord(s, alt string) string {
	if strings.TrimSpace(s) == "" {
		return alt
	}
	return s
}

func boolValue(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x == "true" || x == "1" || x == "yes"
	case float64:
		return x != 0
	}
	return false
}

func intValue(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case string:
		n, _ := strconv.Atoi(strings.TrimSpace(x))
		return n
	}
	return 0
}

func linesWord(content string) string {
	n := strings.Count(strings.TrimRight(content, "\n"), "\n") + 1
	if strings.TrimSpace(content) == "" {
		return "empty"
	}
	if n == 1 {
		return "1 line"
	}
	return fmt.Sprintf("%d lines", n)
}

func quote(s string) string {
	s = textutil.OneLine(s)
	if s == "" {
		return "«»"
	}
	return "«" + textutil.TruncateRunes(s, quoteRunes, "…") + "»"
}

func editPairs(a map[string]any) []string {
	pair := func(m map[string]any) string {
		o, _ := m["old_text"].(string)
		n, _ := m["new_text"].(string)
		if strings.TrimSpace(o) == "" && strings.TrimSpace(n) == "" {
			return ""
		}
		return quote(o) + " → " + quote(n)
	}
	var out []string
	if p := pair(a); p != "" {
		out = append(out, p)
	}
	if list, ok := a["edits"].([]any); ok {
		for _, e := range list {
			if m, ok := e.(map[string]any); ok {
				if p := pair(m); p != "" {
					out = append(out, p)
				}
			}
		}
	}
	if len(out) > 3 {
		out = append(out[:3], fmt.Sprintf("+%d more", len(out)-3))
	}
	return out
}

var lineNoRe = regexp.MustCompile(`\bline (\d+)`)

func syntaxFlag(result string) string {
	lines := strings.Split(result, "\n")
	for i, line := range lines {
		t := strings.TrimSpace(line)
		if !strings.HasPrefix(t, "⚠") {
			continue
		}
		t = strings.TrimSuffix(t, ":")
		last, named, at := "", "", ""
		for _, next := range lines[i+1:] {
			n := strings.TrimSpace(next)
			if strings.HasPrefix(n, "(") {
				break
			}
			if m := lineNoRe.FindStringSubmatch(n); m != nil {
				at = m[1]
			}
			if n == "" || !meaningful(n) || strings.HasPrefix(n, "Node.js v") || strings.HasPrefix(n, "at ") {
				continue
			}
			last = n
			if strings.Contains(strings.ToLower(n), "error") {
				named = n
			}
		}
		if named != "" {
			last = named
		}
		if last != "" {
			t += ": " + last
		}
		if at != "" && !strings.Contains(last, "line "+at) {
			t += " (line " + at + ")"
		}
		return " " + textutil.TruncateRunes(t, 100, "…")
	}
	return ""
}

func commandWord(cmd string) string {
	cmd = strings.TrimSpace(cmd)
	lines := strings.Split(cmd, "\n")
	word := textutil.CollapseSpace(lines[0])
	if n := len(lines); n > 1 {
		word += fmt.Sprintf(" … (%d lines)", n)
	}
	return textutil.TruncateRunes(word, 90, "…")
}

func execLine(label, result string) string {
	status, code, ok := execStatus(result)
	if !ok {
		return "$ " + label + " → " + outcome(result)
	}
	tail := execTail(result, 1)
	if code != 0 {
		tail = execTail(result, 2)
	}
	line := "$ " + label + " → " + status
	if tail != "" {
		line += ": " + tail
	}
	return line
}

func execStatus(result string) (status string, code int, ok bool) {
	lines := strings.Split(strings.TrimRight(result, "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		m := exitLineRe.FindStringSubmatch(strings.TrimSpace(lines[i]))
		if m == nil {
			continue
		}
		t := strings.Trim(strings.TrimSpace(lines[i]), "[]")
		if m[1] != "" {
			code, _ = strconv.Atoi(m[1])
			return "exit " + m[1], code, true
		}
		if strings.HasPrefix(t, "timed out") {
			return "timed out", 124, true
		}
		return textutil.TruncateRunes(t, 80, "…"), -1, true
	}
	return "", 0, false
}

func execTail(result string, n int) string {
	lines := strings.Split(strings.TrimRight(result, "\n"), "\n")
	var keep []string
	for i := len(lines) - 1; i > 0 && len(keep) < n; i-- {
		t := strings.TrimSpace(lines[i])
		if t == "" || t == "[stderr]" || exitLineRe.MatchString(t) || strings.HasPrefix(t, "[new or changed files") ||
			strings.HasPrefix(t, "[output was longer") || !meaningful(t) {
			continue
		}
		keep = append([]string{textutil.TruncateRunes(textutil.CollapseSpace(t), outcomeRunes/n, "…")}, keep...)
	}
	return strings.Join(keep, " ⏎ ")
}

func meaningful(line string) bool {
	for _, r := range line {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

func outcome(result string) string {
	if strings.HasPrefix(result, "$ ") {
		if status, _, ok := execStatus(result); ok {
			if tail := execTail(result, 1); tail != "" {
				return status + ": " + tail
			}
			return status
		}
	}
	var lines []string
	for _, line := range strings.Split(result, "\n") {
		if t := strings.TrimSpace(line); t != "" && meaningful(t) {
			lines = append(lines, t)
		}
	}
	if len(lines) == 0 {
		return "(no output)"
	}
	head := strings.TrimSpace(queuedRe.ReplaceAllString(boilerRe.ReplaceAllString(lines[0], ""), ""))
	if head == "" && len(lines) > 1 {
		head, lines = lines[1], lines[1:]
	}
	if genericRe.MatchString(head) || strings.HasSuffix(head, ":") {
		var extra []string
		for _, l := range lines[1:] {
			if !bulletRe.MatchString(l) {
				break
			}
			extra = append(extra, strings.TrimSpace(boilerRe.ReplaceAllString(bulletRe.ReplaceAllString(l, ""), "")))
			if len(extra) == 2 {
				break
			}
		}
		if len(extra) > 0 {
			head = strings.TrimRight(strings.TrimSuffix(head, ":"), ". ") + ": " + strings.Join(extra, "; ")
		}
	}
	return textutil.TruncateRunes(textutil.CollapseSpace(head), outcomeRunes, "…")
}

type entry struct {
	text   string
	look   bool
	failed bool
	tool   string
}

func groupSteps(steps []step) []entry {
	var out []entry
	for _, s := range steps {
		if s.look == "" {
			out = append(out, entry{text: s.text, failed: s.failed, tool: s.tool})
			continue
		}
		verb := lookVerbs[s.tool]
		if n := len(out); n > 0 && out[n-1].look && out[n-1].tool == s.tool {
			if s.look != "-" && !strings.HasSuffix(out[n-1].text, s.look) {
				out[n-1].text += ", " + s.look
			}
			continue
		}
		text := verb
		if s.look != "-" {
			text += " " + s.look
		}
		out = append(out, entry{text: text, look: true, tool: s.tool})
	}
	for i := range out {
		if out[i].look {
			out[i].text = textutil.TruncateRunes(out[i].text, stepRunes, "…")
		}
	}
	return out
}

func renderEntries(es []entry) string {
	var sb strings.Builder
	for i, e := range es {
		fmt.Fprintf(&sb, "%d. %s\n", i+1, e.text)
	}
	return strings.TrimRight(sb.String(), "\n")
}

func runeLen(es []entry) int {
	n := 0
	for i, e := range es {
		n += utf8.RuneCountInString(e.text) + len(strconv.Itoa(i+1)) + 3
	}
	return n
}

func fitEntries(es []entry, budget int) string {
	if runeLen(es) <= budget {
		return renderEntries(es)
	}
	looks, kept := 0, es[:0:0]
	for _, e := range es {
		if e.look {
			looks++
			continue
		}
		kept = append(kept, e)
	}
	note := ""
	if looks > 0 {
		note = fmt.Sprintf("(+%d look-ups: reading files, searches, listings)", looks)
	}
	if runeLen(kept)+utf8.RuneCountInString(note) <= budget {
		return strings.TrimSpace(renderEntries(kept) + "\n" + note)
	}
	head := min(3, len(kept))
	room := budget - runeLen(kept[:head]) - utf8.RuneCountInString(note) - 80
	tail := len(kept)
	for tail > head && room-(utf8.RuneCountInString(kept[tail-1].text)+6) > 0 {
		room -= utf8.RuneCountInString(kept[tail-1].text) + 6
		tail--
	}
	var sb strings.Builder
	for i, e := range kept[:head] {
		fmt.Fprintf(&sb, "%d. %s\n", i+1, e.text)
	}
	if mid := kept[head:tail]; len(mid) > 0 {
		failed := 0
		for _, e := range mid {
			if e.failed {
				failed++
			}
		}
		fmt.Fprintf(&sb, "… %d more steps", len(mid))
		if failed > 0 {
			fmt.Fprintf(&sb, " (%d of them failed and were followed up)", failed)
		}
		sb.WriteString(" …\n")
	}
	for i, e := range kept[tail:] {
		fmt.Fprintf(&sb, "%d. %s\n", tail+i+1, e.text)
	}
	if note != "" {
		sb.WriteString(note)
	}
	return strings.TrimSpace(sb.String())
}
