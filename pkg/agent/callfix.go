package agent

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/Metixpro/macron-agent/pkg/llm"
)

type callFix struct {
	from  string
	notes []string

	confused bool
}

func (f callFix) any() bool { return f.from != "" || len(f.notes) > 0 }

func (f callFix) String() string {
	var parts []string
	if f.from != "" {
		parts = append(parts, "tool "+f.from)
	}
	return strings.Join(append(parts, f.notes...), "; ")
}

func fixCall(tc *llm.ToolCall, defs map[string]llm.ToolDef) callFix {
	var fx callFix
	if name, ok := resolveTool(tc.Function.Name, defs); ok && name != tc.Function.Name {
		fx.from, fx.confused = tc.Function.Name, true
		tc.Function.Name = name
	}
	def, ok := defs[tc.Function.Name]
	if !ok {
		return fx
	}
	raw, err := repairArgs(tc.Function.Arguments)
	if err != nil {
		return fx
	}
	var args map[string]any
	if json.Unmarshal([]byte(raw), &args) != nil {
		return fx
	}
	changed := fitArgs(args, def.Parameters, "", &fx)
	if changed {
		if b, err := json.Marshal(args); err == nil {
			raw = string(b)
		}
	}
	var asSent map[string]any
	if changed || json.Unmarshal([]byte(tc.Function.Arguments), &asSent) != nil {
		tc.Function.Arguments = raw
	}
	return fx
}

var toolPrefixRe = regexp.MustCompile(`(?i)^(?:functions?|tools?|default_api|api|mcp|tool_call)[.:/]+`)

var toolAliases = map[string][]string{
	"search": {"web_search"}, "googlesearch": {"web_search"}, "google": {"web_search"}, "bingsearch": {"web_search"},
	"internetsearch": {"web_search"}, "searchweb": {"web_search"}, "searchinternet": {"web_search"}, "searchonline": {"web_search"},
	"duckduckgo": {"web_search"}, "searchengine": {"web_search"}, "websearchtool": {"web_search"},
	"fetch": {"web_fetch"}, "fetchurl": {"web_fetch"}, "fetchpage": {"web_fetch"}, "fetchwebpage": {"web_fetch"}, "openurl": {"web_fetch"},
	"readurl": {"web_fetch"}, "readwebpage": {"web_fetch"}, "getwebpage": {"web_fetch"}, "getpage": {"web_fetch"}, "visit": {"web_fetch"},
	"browse": {"web_fetch", "browser"}, "openwebpage": {"web_fetch"}, "scrape": {"web_fetch"},
	"python": {"run_code"}, "runpython": {"run_code"}, "executepython": {"run_code"}, "pythoninterpreter": {"run_code"},
	"codeinterpreter": {"run_code"}, "executecode": {"run_code"}, "codeexecution": {"run_code"}, "runscript": {"run_code"},
	"bash": {"bash_execute", "run_code"}, "shell": {"bash_execute", "run_code"}, "terminal": {"bash_execute", "run_code"},
	"runcommand": {"bash_execute", "run_code"}, "executecommand": {"bash_execute", "run_code"}, "runshell": {"bash_execute", "run_code"},
	"cmd": {"bash_execute", "run_code"}, "exec": {"bash_execute", "run_code"}, "bashexec": {"bash_execute"},
	"openfile": {"read_file"}, "catfile": {"read_file"}, "viewfile": {"read_file"},
	"createfile": {"write_file"}, "savefile": {"write_file"}, "writetofile": {"write_file"},
	"ls": {"list_files"}, "listdir": {"list_files"}, "listdirectory": {"list_files"}, "files": {"list_files"},
	"imagegeneration": {"generate_image"}, "createimage": {"generate_image"}, "texttoimage": {"generate_image"}, "imagegen": {"generate_image"},
	"dalle": {"generate_image"}, "makeimage": {"generate_image"}, "drawimage": {"generate_image"}, "draw": {"generate_image"},
	"imagesearch": {"search_image"}, "findimage": {"search_image"}, "searchimages": {"search_image"}, "searchphoto": {"search_image"},
	"sendfiletouser": {"send_file"}, "uploadfile": {"send_file"}, "sendattachment": {"send_file"}, "attachfile": {"send_file"},
	"tts": {"text_to_speech"}, "speak": {"text_to_speech"}, "texttovoice": {"text_to_speech"},
	"youtubedownload": {"download_media_ytdlp"}, "downloadvideo": {"download_media_ytdlp"}, "downloadmedia": {"download_media_ytdlp"},
	"ytdlp": {"download_media_ytdlp"}, "youtubedl": {"download_media_ytdlp"}, "downloadaudio": {"download_media_ytdlp"},
	"downloadmusic": {"search_music", "download_media_ytdlp"}, "findmusic": {"search_music"}, "searchsong": {"search_music"},
	"download": {"download_file"}, "downloadurl": {"download_file"},
	"remind": {"schedule"}, "reminder": {"schedule"}, "setreminder": {"schedule"}, "createreminder": {"schedule"}, "scheduletask": {"schedule"},
	"chart": {"generate_chart"}, "plot": {"generate_chart"}, "makechart": {"generate_chart"}, "qrcode": {"generate_qr"}, "qr": {"generate_qr"},
	"memory": {"remember"}, "savememory": {"remember"}, "rememberfact": {"remember"},
	"plan": {ToolPlan}, "todo": {ToolPlan}, "updatetodo": {ToolPlan},
	"subagent": {ToolDelegate}, "delegate": {ToolDelegate},
}

func normName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func resolveTool(name string, defs map[string]llm.ToolDef) (string, bool) {
	known := make(map[string]bool, len(defs))
	for d := range defs {
		known[d] = true
	}
	return resolveName(name, known)
}

func resolveName(name string, defs map[string]bool) (string, bool) {
	if defs[name] {
		return name, true
	}
	n := strings.Trim(strings.TrimSpace(name), "`\"' ")
	n = strings.TrimSuffix(n, "()")
	for {
		m := toolPrefixRe.FindString(n)
		if m == "" {
			break
		}
		n = n[len(m):]
	}
	if defs[n] {
		return n, true
	}
	key := normName(n)
	if key == "" {
		return "", false
	}
	names := make([]string, 0, len(defs))
	for d := range defs {
		names = append(names, d)
	}
	sort.Strings(names)
	var same []string
	for _, d := range names {
		if normName(d) == key {
			same = append(same, d)
		}
	}
	if len(same) == 1 {
		return same[0], true
	}
	for _, t := range toolAliases[key] {
		if defs[t] {
			return t, true
		}
	}

	if len(key) < 5 {
		return "", false
	}
	limit := 1
	if len(key) >= 8 {
		limit = 2
	}
	best, bestD, ties := "", limit+1, 0
	for _, d := range names {
		switch dist := editDistance(key, normName(d)); {
		case dist < bestD:
			best, bestD, ties = d, dist, 1
		case dist == bestD:
			ties++
		}
	}
	if bestD <= limit && ties == 1 {
		return best, true
	}
	return "", false
}

func editDistance(a, b string) int {
	if a == b {
		return 0
	}
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

var paramAliases = map[string][]string{
	"q": {"query"}, "search": {"query"}, "searchquery": {"query"}, "searchterm": {"query"}, "term": {"query"}, "keyword": {"query"},
	"keywords": {"query"}, "question": {"query"}, "searchtext": {"query"},
	"file": {"filename", "path", "file_name", "file_path"}, "filepath": {"filename", "path"}, "path": {"filename", "path", "file"},
	"filename": {"path", "file"}, "fname": {"filename", "path"}, "files": {"filenames", "files"}, "paths": {"filenames", "files"},
	"url": {"url", "link"}, "link": {"url"}, "uri": {"url"}, "href": {"url"}, "website": {"url"}, "pageurl": {"url"}, "address": {"url"},
	"cmd": {"command", "code"}, "command": {"command", "code"}, "shell": {"command"}, "bash": {"command"},
	"script": {"code"}, "source": {"code"}, "sourcecode": {"code"}, "program": {"code"},
	"lang": {"language"}, "programminglanguage": {"language"},
	"body": {"content", "text"}, "data": {"content"}, "contents": {"content"}, "filecontent": {"content"}, "text": {"content", "text"},
	"msg": {"text", "message"}, "message": {"text", "message"},
	"count": {"limit", "count", "n"}, "n": {"limit", "count"}, "num": {"limit", "count"}, "number": {"limit", "count"},
	"maxresults": {"limit"}, "numresults": {"limit"}, "topk": {"limit"}, "max": {"limit"}, "results": {"limit"},
	"desc": {"description", "prompt"}, "imageprompt": {"prompt"}, "instruction": {"prompt", "instruction"}, "instructions": {"prompt", "instruction"},
}

func fitArgs(args map[string]any, schema map[string]any, path string, fx *callFix) bool {
	props, _ := schema["properties"].(map[string]any)
	if len(props) == 0 {
		return false
	}
	changed := false
	req := map[string]bool{}
	for _, r := range required(llm.ToolDef{Parameters: schema}) {
		req[r] = true
	}
	rename := func(from, to string) {
		args[to] = args[from]
		delete(args, from)
		fx.notes = append(fx.notes, fmt.Sprintf("%s%s → %s", path, from, to))
		fx.confused, changed = true, true
	}
	unknown := func() []string {
		var out []string
		for k := range args {
			if _, ok := props[k]; !ok {
				out = append(out, k)
			}
		}
		sort.Strings(out)
		return out
	}

	for _, k := range unknown() {
		key := normName(k)
		var same []string
		for p := range props {
			if normName(p) == key {
				same = append(same, p)
			}
		}
		if len(same) == 1 {
			if _, taken := args[same[0]]; !taken {
				rename(k, same[0])
				continue
			}
		}
		for _, p := range paramAliases[key] {
			if _, ok := props[p]; ok {
				if _, taken := args[p]; !taken {
					rename(k, p)
				}
				break
			}
		}
	}

	_, open := schema["additionalProperties"].(map[string]any)
	open = open || schema["additionalProperties"] == true
	if left := unknown(); len(left) == 1 && !open {
		var missing []string
		for p := range req {
			if v, ok := args[p]; !ok || v == nil {
				missing = append(missing, p)
			}
		}
		if len(missing) == 1 {
			rename(left[0], missing[0])
		} else if len(missing) == 0 && len(props) == 1 {
			for p := range props {
				if _, ok := args[p]; !ok {
					rename(left[0], p)
				}
			}
		}
	}

	for k, v := range args {
		p, ok := props[k].(map[string]any)
		if !ok {
			continue
		}
		if v == nil {
			if !req[k] {
				delete(args, k)
				changed = true
			}
			continue
		}
		if nv, note := coerce(v, p, path+k, fx); note != "" {
			args[k] = nv
			fx.notes = append(fx.notes, note)
			changed = true
		}
	}
	return changed
}

func schemaType(p map[string]any) string {
	switch t := p["type"].(type) {
	case string:
		return t
	case []any:
		for _, x := range t {
			if s, ok := x.(string); ok && s != "null" {
				return s
			}
		}
	case []string:
		for _, s := range t {
			if s != "null" {
				return s
			}
		}
	}
	return ""
}

func enumOf(p map[string]any) []string {
	var out []string
	switch e := p["enum"].(type) {
	case []string:
		out = e
	case []any:
		for _, x := range e {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

func coerce(v any, p map[string]any, path string, fx *callFix) (any, string) {
	note := func(nv any) (any, string) {
		return nv, fmt.Sprintf("%s: %s → %s", path, short(v), short(nv))
	}
	switch schemaType(p) {
	case "integer":
		switch x := v.(type) {
		case string:
			if f, ok := parseNumber(x); ok && f == math.Trunc(f) {
				return note(int64(f))
			}
		case float64:
			if x != math.Trunc(x) && math.Abs(x-math.Round(x)) < 1e-9 {
				return note(math.Round(x))
			}
		case bool:
			return note(map[bool]int64{true: 1, false: 0}[x])
		}
	case "number":
		if x, ok := v.(string); ok {
			if f, ok := parseNumber(x); ok {
				return note(f)
			}
		}
	case "boolean":
		switch x := v.(type) {
		case string:
			switch strings.ToLower(strings.TrimSpace(x)) {
			case "true", "yes", "y", "1", "on", "بله", "آره", "اره", "درسته", "روشن":
				return note(true)
			case "false", "no", "n", "0", "off", "نه", "خیر", "خاموش":
				return note(false)
			}
		case float64:
			if x == 0 || x == 1 {
				return note(x == 1)
			}
		}
	case "string":
		switch x := v.(type) {
		case float64:
			return note(strconv.FormatFloat(x, 'f', -1, 64))
		case bool:
			return note(strconv.FormatBool(x))
		case []any:
			var parts []string
			for _, e := range x {
				s, ok := scalarString(e)
				if !ok {
					b, _ := json.Marshal(v)
					return note(string(b))
				}
				parts = append(parts, s)
			}
			if len(parts) == 1 {
				return fixEnum(parts[0], p, path, note)
			}
			return note(strings.Join(parts, ", "))
		case map[string]any:
			b, _ := json.Marshal(x)
			return note(string(b))
		case string:
			if nv, n := fixEnum(x, p, path, nil); n != "" {
				return nv, n
			}
		}
	case "array":
		items, _ := p["items"].(map[string]any)
		var list []any
		switch x := v.(type) {
		case []any:
			list = x
		case string:
			t := strings.TrimSpace(x)
			if strings.HasPrefix(t, "[") && json.Unmarshal([]byte(t), &list) == nil {
				break
			}
			list = []any{x}
		default:
			list = []any{x}
		}
		changed := fmt.Sprint(list) != fmt.Sprint(v)
		if items != nil {
			for i, e := range list {
				if ne, n := coerce(e, items, fmt.Sprintf("%s[%d]", path, i), fx); n != "" {
					list[i], changed = ne, true
				}
				if m, ok := list[i].(map[string]any); ok && schemaType(items) == "object" && fitArgs(m, items, fmt.Sprintf("%s[%d].", path, i), fx) {
					changed = true
				}
			}
		}
		if changed {
			return note(list)
		}
	case "object":
		m, ok := v.(map[string]any)
		if s, isStr := v.(string); isStr {
			if json.Unmarshal([]byte(strings.TrimSpace(s)), &m) != nil {
				return v, ""
			}
			ok = true
			fitArgs(m, p, path+".", fx)
			return note(m)
		}
		if ok && fitArgs(m, p, path+".", fx) {
			return m, path + ": fixed inside"
		}
	}
	return v, ""
}

func fixEnum(s string, p map[string]any, path string, note func(any) (any, string)) (any, string) {
	enum := enumOf(p)
	if len(enum) == 0 {
		if note != nil {
			return note(s)
		}
		return s, ""
	}
	for _, e := range enum {
		if e == s {
			if note != nil {
				return note(s)
			}
			return s, ""
		}
	}
	key := normName(s)
	var hits []string
	for _, e := range enum {
		if normName(e) == key {
			hits = append(hits, e)
		}
	}
	if len(hits) == 0 && len(key) >= 3 {
		for _, e := range enum {
			if strings.HasPrefix(normName(e), key) {
				hits = append(hits, e)
			}
		}
	}
	if len(hits) == 1 {
		return hits[0], fmt.Sprintf("%s: %s → %s", path, short(s), short(hits[0]))
	}
	if note != nil {
		return note(s)
	}
	return s, ""
}

func parseNumber(s string) (float64, bool) {
	var b strings.Builder
	for _, r := range strings.TrimSpace(s) {
		switch {
		case r >= '۰' && r <= '۹':
			b.WriteRune('0' + (r - '۰'))
		case r >= '٠' && r <= '٩':
			b.WriteRune('0' + (r - '٠'))
		case r == ',' || r == '٬' || r == '_' || unicode.IsSpace(r):
		case r == '٫':
			b.WriteRune('.')
		default:
			b.WriteRune(r)
		}
	}
	f, err := strconv.ParseFloat(b.String(), 64)
	return f, err == nil && b.Len() > 0 && !math.IsInf(f, 0) && !math.IsNaN(f)
}

func scalarString(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64), true
	case bool:
		return strconv.FormatBool(x), true
	}
	return "", false
}

func short(v any) string {
	b, _ := json.Marshal(v)
	s := string(b)
	if len(s) > 40 {
		s = s[:37] + "…"
	}
	return s
}
