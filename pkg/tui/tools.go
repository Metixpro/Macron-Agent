package tui

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/Metixpro/macron-agent/pkg/agent"
)

var toolTitles = map[string]string{
	"bash_execute": "Bash", "run_code": "Run", "read_file": "Read", "write_file": "Write", "edit_file": "Update",
	"apply_patch": "Patch", "search_code": "Search", "find_files": "Find", "project_tree": "Tree", "code_outline": "Outline",
	"web_search": "Web Search", "web_fetch": "Fetch", "view_image": "View", "list_files": "List", "read_skill": "Skill",
	"list_skills": "Skills", "shell_jobs": "Jobs", "document_reader": "Read", "pdf_tool": "PDF", "query_data": "Query",
	"remember": "Remember", "forget": "Forget", "more_tools": "Tools", "tool_search": "Tools", "save_skill": "Save Skill",
	agent.ToolDelegate: "Task", "search_past_chats": "Recall", "download": "Download", "http_request": "HTTP",
	"python_kernel": "Python", "database_query": "SQL", "deploy_service": "Deploy", "office_document": "Office", "document_diff": "Diff",
	"ocr_image": "OCR", "translate_document": "Translate", "generate_diagram": "Diagram", "generate_image": "Image",
	"media_summary": "Summarize", "cloud_storage": "Cloud", "tsetmc_stock": "Bourse", "price_alert": "Price Alert", "page_watch": "Watch",
	"academic_search": "Papers", "fact_check": "Fact Check", "create_dashboard": "Dashboard", "openapi_call": "API",
	"statistics_forecast": "Stats", "github_workflow": "GitHub", "live_preview": "Preview", "email_client": "Email",
	"calendar_client": "Calendar", "iran_messenger": "Message", "external_messenger": "Message", "task_board": "Tasks",
	"generate_briefing": "Briefing", "rag_search": "Search Docs", "crawl_site": "Crawl", "deep_research": "Research",
	"currency_gold_price": "Prices", "crypto_currency_price": "Crypto",
}

var lookTools = map[string]bool{
	"search_code": true, "find_files": true, "project_tree": true, "code_outline": true, "list_files": true,
	"web_search": true, "web_fetch": true, "read_skill": true, "list_skills": true, "document_reader": true,
	"view_image": true, "search_past_chats": true, "shell_jobs": true, "more_tools": true, "tool_search": true,
	"academic_search": true, "rag_search": true, "tsetmc_stock": true, "database_query": true,
}

var argKeys = []string{"command", "deploy_cmd", "code", "filename", "file", "path", "pattern", "query", "symbol", "claim", "url", "topic", "title", "goal", "name", "sql", "fact", "to", "repo", "spec_path", "prompt", "remote_path", "action", "language", "id"}

var (
	exitRe  = regexp.MustCompile(`^\[(?:exit (-?\d+)[^\]]*|timed out[^\]]*|could not run: [^\]]*)\]$`)
	noiseRe = regexp.MustCompile(`^\[(?:stderr|output was longer[^\]]*)\]$`)
	filesRe = regexp.MustCompile(`^\[new or changed files in the workspace: (.*)\]$`)
)

func toolArgs(args string) map[string]any {
	a := map[string]any{}
	_ = json.Unmarshal([]byte(args), &a)
	return a
}

func strArg(a map[string]any, k string) string {
	switch v := a[k].(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return fmt.Sprint(v)
	}
	return ""
}

func toolTitle(name, args string, width int) string {
	title := toolTitles[name]
	if title == "" {
		title = name
	}
	a := toolArgs(args)
	arg := ""
	for _, k := range argKeys {
		if v := strArg(a, k); v != "" {
			arg = v
			break
		}
	}
	if arg != "" {
		first, rest, multi := strings.Cut(arg, "\n")
		arg = oneLine(first)
		if multi && strings.TrimSpace(rest) != "" {
			arg += " …"
		}
		switch name {
		case "search_code", "find_files", "web_search", "search_past_chats":
			arg = "\"" + arg + "\""
		}
	}
	head := stBold.Render(title)
	if arg != "" {
		head += stDim.Render("(") + clip(arg, max(width-len(title)-6, 12)) + stDim.Render(")")
	}
	return head
}

func outputLines(result string) (lines []string, status string, code int, files string) {
	all := strings.Split(strings.TrimRight(clean(result), "\n"), "\n")
	if len(all) > 0 && strings.HasPrefix(all[0], "$ ") {
		all = all[1:]
	}
	for _, l := range all {
		t := strings.TrimSpace(l)
		switch {
		case exitRe.MatchString(t):
			m := exitRe.FindStringSubmatch(t)
			status = strings.Trim(t, "[]")
			if m[1] != "" {
				fmt.Sscan(m[1], &code)
				status = "exit " + m[1]
			} else {
				code = -1
			}
		case noiseRe.MatchString(t):
		case filesRe.MatchString(t):
			files = filesRe.FindStringSubmatch(t)[1]
		default:
			lines = append(lines, strings.TrimRight(l, " "))
		}
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	return
}

func meaningful(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

func firstMeaningful(result string) (string, int) {
	var first string
	n := 0
	for _, l := range strings.Split(clean(result), "\n") {
		if t := strings.TrimSpace(l); t != "" && meaningful(t) {
			if first == "" {
				first = t
			}
			n++
		}
	}
	return first, n
}

func diffLines(a map[string]any, width int) []string {
	type pair struct{ old, new string }
	var pairs []pair
	add := func(m map[string]any) {
		o, _ := m["old_text"].(string)
		n, _ := m["new_text"].(string)
		if strings.TrimSpace(o) != "" || strings.TrimSpace(n) != "" {
			pairs = append(pairs, pair{o, n})
		}
	}
	add(a)
	if list, ok := a["edits"].([]any); ok {
		for _, e := range list {
			if m, ok := e.(map[string]any); ok {
				add(m)
			}
		}
	}
	var out []string
	side := func(text, mark string, st func(...string) string) {
		lines := strings.Split(strings.TrimRight(clean(text), "\n"), "\n")
		if strings.TrimSpace(text) == "" {
			return
		}
		for i, l := range lines {
			if i == 6 {
				out = append(out, stDim.Render(fmt.Sprintf("  … +%d lines", len(lines)-6)))
				break
			}
			out = append(out, st(clip(mark+" "+l, width)))
		}
	}
	for i, p := range pairs {
		if i == 3 {
			out = append(out, stDim.Render(fmt.Sprintf("… +%d more changes", len(pairs)-3)))
			break
		}
		if i > 0 {
			out = append(out, stFaint.Render("  ⋯"))
		}
		side(p.old, "-", stDel.Render)
		side(p.new, "+", stAdd.Render)
	}
	return out
}

func warning(result string) string {
	for _, l := range strings.Split(clean(result), "\n") {
		if t := strings.TrimSpace(l); strings.HasPrefix(t, "⚠") {
			return t
		}
	}
	return ""
}

func countLines(s string) int {
	s = strings.TrimRight(s, "\n")
	if strings.TrimSpace(s) == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

func toolBody(name, args, result string, failed bool, width int) []string {
	a := toolArgs(args)
	w := max(width-6, 20)
	var body []string
	add := func(s string) { body = append(body, s) }
	switch name {
	case "bash_execute", "run_code", "python_kernel":
		if name == "bash_execute" && (a["background"] == true || strArg(a, "background") == "true") {
			add(agent.StepLine(name, args, result))
			break
		}
		lines, status, code, files := outputLines(result)
		if len(lines) > 5 {
			add(stDim.Render(fmt.Sprintf("… +%d lines", len(lines)-5)))
			lines = lines[len(lines)-5:]
		}
		for _, l := range lines {
			add(stDim.Render(clip(l, w)))
		}
		switch {
		case status != "" && code != 0:
			add(stFail.Render(status))
		case len(lines) == 0:
			add(stDim.Render("(no output)"))
		}
		if files != "" {
			add(stDim.Render(clip("changed: "+files, w)))
		}
	case "write_file":
		if failed {
			add(stFail.Render(clip(agent.StepLine(name, args, result), w)))
			break
		}
		content, _ := a["content"].(string)
		verb := "Wrote"
		if a["append"] == true {
			verb = "Appended"
		}
		add(fmt.Sprintf("%s %s to %s", verb, plural(countLines(content), "line"), strArg(a, "filename")))
		lines := strings.Split(strings.TrimRight(clean(content), "\n"), "\n")
		for i, l := range lines {
			if i == 4 {
				add(stDim.Render(fmt.Sprintf("… +%d lines", len(lines)-4)))
				break
			}
			add(stDim.Render(clip(fmt.Sprintf("%4d  %s", i+1, l), w)))
		}
	case "edit_file":
		if failed {
			add(stFail.Render(clip(agent.StepLine(name, args, result), w)))
			break
		}
		body = append(body, diffLines(a, w)...)
		if len(body) == 0 {
			add("Edited " + strArg(a, "filename"))
		}
	case "read_file":
		if failed {
			add(stFail.Render(clip(agent.StepLine(name, args, result), w)))
			break
		}
		add(fmt.Sprintf("Read %s", plural(countLines(result), "line")))
	case agent.ToolDelegate:
		first, _ := firstMeaningful(result)
		add(clip(first, w))
	default:
		if lookTools[name] && !failed {
			first, n := firstMeaningful(result)
			if first == "" {
				add(stDim.Render("(nothing)"))
				break
			}
			line := clip(first, w)
			if n > 1 {
				line += stDim.Render(fmt.Sprintf("  +%d lines", n-1))
			}
			add(line)
			break
		}
		line := agent.StepLine(name, args, result)
		if failed {
			line = stFail.Render(clip(line, w))
		}
		add(clip(line, w))
	}
	if wn := warning(result); wn != "" && (name == "write_file" || name == "edit_file" || name == "apply_patch") {
		add(stWarn.Render(clip(wn, w)))
	}
	return body
}

func toolBlock(name, args, result string, width int) string {
	failed := agent.StepFailed(name, args, result)
	mark := stOK.Render(bullet)
	if failed {
		mark = stFail.Render(bullet)
	}
	var b strings.Builder
	b.WriteString(mark + " " + toolTitle(name, args, width))
	for i, l := range toolBody(name, args, result, failed, width) {
		if i == 0 {
			b.WriteString("\n  " + stDim.Render(elbow) + "  " + l)
		} else {
			b.WriteString("\n     " + l)
		}
	}
	return b.String()
}

func planBlock(items []agent.PlanItem, width int) string {
	var b strings.Builder
	b.WriteString(stAccent.Render(bullet) + " " + stBold.Render("Plan"))
	for i, it := range items {
		var line string
		switch it.Status {
		case agent.PlanDone:
			line = stDim.Render("☒ " + clip(oneLine(it.Text), width-8))
		case agent.PlanInProgress:
			line = stAccent.Bold(true).Render("◼ " + clip(oneLine(it.Text), width-8))
		default:
			line = "☐ " + clip(oneLine(it.Text), width-8)
		}
		if i == 0 {
			b.WriteString("\n  " + stDim.Render(elbow) + "  " + line)
		} else {
			b.WriteString("\n     " + line)
		}
	}
	return b.String()
}
