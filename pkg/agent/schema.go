package agent

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"sort"
	"strings"
)

// A sub-agent's report can be held to a contract: the JSON Schema its main
// agent gives (delegate_task's output_schema). The common part of JSON
// Schema is checked: type, properties, required, additionalProperties: false,
// items, enum, const, min/max items, length and value.

// checkSchema reports what in v does not match the schema (none: it matches).
func checkSchema(schema map[string]any, v any) []string {
	var errs []string
	checkAt(schema, v, "$", &errs)
	if len(errs) > 12 {
		errs = append(errs[:12], fmt.Sprintf("… and %d more", len(errs)-12))
	}
	return errs
}

func checkAt(s map[string]any, v any, at string, errs *[]string) {
	bad := func(format string, a ...any) { *errs = append(*errs, at+": "+fmt.Sprintf(format, a...)) }
	if t, ok := s["type"]; ok && !typeMatches(t, v) {
		bad("must be %v, not %s", t, jsonType(v))
		return
	}
	if e, ok := s["enum"].([]any); ok && !slices.ContainsFunc(e, func(x any) bool { return jsonEqual(x, v) }) {
		bad("must be one of %v", e)
	}
	if c, ok := s["const"]; ok && !jsonEqual(c, v) {
		bad("must be %v", c)
	}
	switch x := v.(type) {
	case map[string]any:
		props, _ := s["properties"].(map[string]any)
		if req, ok := s["required"].([]any); ok {
			for _, r := range req {
				if k, _ := r.(string); k != "" {
					if _, has := x[k]; !has {
						bad("%q is required", k)
					}
				}
			}
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if ps, ok := props[k].(map[string]any); ok {
				checkAt(ps, x[k], at+"."+k, errs)
			} else if ap, ok := s["additionalProperties"].(bool); ok && !ap {
				bad("%q is not allowed", k)
			} else if ap, ok := s["additionalProperties"].(map[string]any); ok {
				checkAt(ap, x[k], at+"."+k, errs)
			}
		}
	case []any:
		if n, ok := num(s["minItems"]); ok && float64(len(x)) < n {
			bad("needs at least %v items, has %d", n, len(x))
		}
		if n, ok := num(s["maxItems"]); ok && float64(len(x)) > n {
			bad("takes at most %v items, has %d", n, len(x))
		}
		if is, ok := s["items"].(map[string]any); ok {
			for i, e := range x {
				checkAt(is, e, fmt.Sprintf("%s[%d]", at, i), errs)
			}
		}
	case string:
		l := float64(len([]rune(x)))
		if n, ok := num(s["minLength"]); ok && l < n {
			bad("is shorter than %v characters", n)
		}
		if n, ok := num(s["maxLength"]); ok && l > n {
			bad("is longer than %v characters", n)
		}
		if p, ok := s["pattern"].(string); ok {
			if re, err := regexp.Compile(p); err == nil && !re.MatchString(x) {
				bad("does not match %s", p)
			}
		}
	case float64:
		if n, ok := num(s["minimum"]); ok && x < n {
			bad("is below %v", n)
		}
		if n, ok := num(s["maximum"]); ok && x > n {
			bad("is above %v", n)
		}
	}
}

func typeMatches(t, v any) bool {
	switch tt := t.(type) {
	case string:
		return isType(tt, v)
	case []any:
		for _, x := range tt {
			if s, _ := x.(string); isType(s, v) {
				return true
			}
		}
		return false
	}
	return true
}

func isType(t string, v any) bool {
	switch t {
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	case "string":
		_, ok := v.(string)
		return ok
	case "number":
		_, ok := v.(float64)
		return ok
	case "integer":
		f, ok := v.(float64)
		return ok && f == math.Trunc(f)
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "null":
		return v == nil
	}
	return true
}

func jsonType(v any) string {
	switch x := v.(type) {
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case float64:
		if x == math.Trunc(x) {
			return "integer"
		}
		return "number"
	case bool:
		return "boolean"
	case nil:
		return "null"
	}
	return fmt.Sprintf("%T", v)
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}

func num(v any) (float64, bool) {
	f, ok := v.(float64)
	return f, ok
}

// reportJSON finds the JSON a report ends with: the last ```json block, the
// whole report, or the last object or array in it.
var jsonFenceRe = regexp.MustCompile("(?s)```(?:json)?[ \\t]*\\n(.*?)```")

func reportJSON(report string) (any, bool) {
	var v any
	if ms := jsonFenceRe.FindAllStringSubmatch(report, -1); len(ms) > 0 {
		if json.Unmarshal([]byte(strings.TrimSpace(ms[len(ms)-1][1])), &v) == nil {
			return v, true
		}
	}
	if json.Unmarshal([]byte(strings.TrimSpace(report)), &v) == nil {
		return v, true
	}
	for i := len(report) - 1; i >= 0; i-- { // the last value that parses to the end of its brackets
		if report[i] != '{' && report[i] != '[' {
			continue
		}
		dec := json.NewDecoder(strings.NewReader(report[i:]))
		if dec.Decode(&v) == nil {
			if _, isMap := v.(map[string]any); isMap || report[i] == '[' {
				return v, true
			}
		}
	}
	return nil, false
}

// schemaOf reads an output_schema argument (an object, or JSON in a string).
func schemaOf(arg any) map[string]any {
	switch x := arg.(type) {
	case map[string]any:
		if len(x) > 0 {
			return x
		}
	case string:
		var m map[string]any
		if json.Unmarshal([]byte(x), &m) == nil && len(m) > 0 {
			return m
		}
	}
	return nil
}
