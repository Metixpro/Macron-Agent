package tools

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
)

var ErrTooLarge = errors.New("data exceeds the size limit")

const browserUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"

func prop(typ, desc string) map[string]any {
	return described(map[string]any{"type": typ}, desc)
}

func arrayProp(itemType, desc string) map[string]any {
	return described(map[string]any{"type": "array", "items": map[string]any{"type": itemType}}, desc)
}

func enumProp(desc string, values ...string) map[string]any {
	return described(map[string]any{"type": "string", "enum": values}, desc)
}

func described(p map[string]any, desc string) map[string]any {
	if desc != "" {
		p["description"] = desc
	}
	return p
}

func schema(props map[string]any, required ...string) map[string]any {
	if props == nil {
		props = map[string]any{}
	}
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func strArg(args map[string]any, key string) string {
	switch v := args[key].(type) {
	case string:
		return strings.TrimSpace(v)
	case nil:
		return ""
	case float64, bool:
		return fmt.Sprint(v)
	}
	return ""
}

func rawStrArg(args map[string]any, key string) string {
	s, _ := args[key].(string)
	return s
}

func textArg(args map[string]any, key string) string {
	switch v := args[key].(type) {
	case nil:
		return ""
	case string:
		return v
	case float64, bool:
		return fmt.Sprint(v)
	default:
		return jsonText(v)
	}
}

func jsonText(v any) string {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return fmt.Sprint(v)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func intArg(args map[string]any, key string, def, lo, hi int) int {
	n := def
	switch v := args[key].(type) {
	case float64:
		n = int(v)
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			n = parsed
		}
	}
	return min(max(n, lo), hi)
}

func sanitizeBase(s string) string {
	s = strings.ReplaceAll(s, "\\", "/")
	s = path.Base(strings.TrimSpace(s))
	if s == "." || s == "/" || s == ".." {
		return ""
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r), strings.ContainsRune("._-()", r):
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	out := strings.TrimLeft(b.String(), ".-_")
	runes := []rune(out)
	if len(runes) > 100 {
		ext := path.Ext(out)
		if len([]rune(ext)) > 10 {
			ext = ""
		}
		out = string(runes[:100-len([]rune(ext))]) + ext
	}
	return out
}

func outputName(provided, prefix string, exts []string, defExt string) string {
	name := sanitizeBase(provided)
	if name == "" {
		name = fmt.Sprintf("%s_%d", prefix, time.Now().UnixMilli())
	}
	if len(exts) > 0 && !slices.Contains(exts, strings.ToLower(path.Ext(name))) {
		name = strings.TrimSuffix(name, path.Ext(name)) + defExt
		if name == defExt {
			name = fmt.Sprintf("%s_%d%s", prefix, time.Now().UnixMilli(), defExt)
		}
	}
	return name
}

func slug(s string, maxLen int) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case b.Len() > 0 && !strings.HasSuffix(b.String(), "_"):
			b.WriteByte('_')
		}
		if b.Len() >= maxLen {
			break
		}
	}
	return strings.Trim(b.String(), "_")
}

func copyLimited(dst io.Writer, src io.Reader, max int64) (int64, error) {
	n, err := io.Copy(dst, io.LimitReader(src, max+1))
	if err != nil {
		return n, err
	}
	if n > max {
		return n, ErrTooLarge
	}
	return n, nil
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

func (r *Registry) rootSnapshot(sessionID string) map[string]string {
	out := map[string]string{}
	files, _ := r.Machine.ListFiles(sessionID)
	for _, f := range files {
		out[f.Name] = fmt.Sprintf("%d/%d", f.Size, f.ModTime.UnixNano())
	}
	return out
}

func (r *Registry) changedFiles(sessionID string, before map[string]string) []string {
	var names []string
	for name, sig := range r.rootSnapshot(sessionID) {
		if before[name] != sig && !strings.HasPrefix(name, ".") {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	if len(names) > 15 {
		names = append(names[:15], fmt.Sprintf("… and %d more", len(names)-15))
	}
	return names
}

func boolArg(args map[string]any, key string) bool {
	switch v := args[key].(type) {
	case bool:
		return v
	case string:
		return v == "true" || v == "1" || v == "yes"
	case float64:
		return v != 0
	}
	return false
}

func (r *Registry) WorkspaceSnapshot(sessionID string) map[string]string {
	return r.rootSnapshot(sessionID)
}

func (r *Registry) ChangedFiles(sessionID string, before map[string]string) []string {
	return r.changedFiles(sessionID, before)
}

var numberText = strings.NewReplacer("۰", "0", "۱", "1", "۲", "2", "۳", "3", "۴", "4", "۵", "5", "۶", "6", "۷", "7", "۸", "8", "۹", "9",
	"٠", "0", "١", "1", "٢", "2", "٣", "3", "٤", "4", "٥", "5", "٦", "6", "٧", "7", "٨", "8", "٩", "9", "٫", ".", "٬", "", ",", "", "_", "", " ", "")

func parseNumber(s string) (float64, bool) {
	s = strings.TrimSuffix(strings.TrimSpace(numberText.Replace(s)), "%")
	f, err := strconv.ParseFloat(s, 64)
	return f, err == nil && !math.IsNaN(f) && !math.IsInf(f, 0)
}
