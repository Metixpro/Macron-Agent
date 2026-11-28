package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/dop251/goja"
)

const (
	browserScriptDefaultTimeout = 30 * time.Second
	browserScriptMaxTimeout     = 90 * time.Second
	browserScriptMaxSteps       = 60
	browserScriptMaxEvalBytes   = 1024 * 1024 // 1 MB local in-script evaluate payload
	browserScriptMaxReturnBytes = 64 * 1024   // 64 KB returned to LLM
	browserScriptMaxLogs        = 80
)

// browserScriptHost abstracts the browser daemon operations exposed inside the
// Goja sandbox so run_script can be unit-tested cleanly without Chromium.
type browserScriptHost interface {
	IsTakeover() bool
	ExecAction(ctx context.Context, act map[string]any) (map[string]any, error)
	Snapshot(ctx context.Context) (map[string]any, error)
	Evaluate(ctx context.Context, expr string, maxBytes int) (any, error)
}

type browserScriptResult struct {
	Value       any            `json:"value,omitempty"`
	Logs        []string       `json:"logs,omitempty"`
	Steps       int            `json:"steps"`
	Interrupted string         `json:"interrupted,omitempty"`
	Page        map[string]any `json:"page,omitempty"`
}

// runBrowserScript executes an OpenBot-style multi-step JS script inside a
// sandboxed Goja VM with strict timeout, step cap, takeover awareness, and
// 64KB output truncation.
func runBrowserScript(ctx context.Context, host browserScriptHost, code string, timeout time.Duration) (browserScriptResult, error) {
	var res browserScriptResult
	code = strings.TrimSpace(code)
	if code == "" {
		return res, errors.New("script is required")
	}
	if timeout <= 0 {
		timeout = browserScriptDefaultTimeout
	}
	if timeout > browserScriptMaxTimeout {
		timeout = browserScriptMaxTimeout
	}

	sctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	vm := goja.New()
	vm.SetFieldNameMapper(goja.TagFieldNameMapper("json", true))

	var interrupted atomic.Value
	interruptWith := func(reason string) {
		if interrupted.CompareAndSwap(nil, reason) {
			vm.Interrupt(reason)
		}
	}

	// Watch context cancellation & deadline to interrupt infinite JS loops.
	doneWatch := make(chan struct{})
	defer close(doneWatch)
	go func() {
		select {
		case <-sctx.Done():
			if errors.Is(sctx.Err(), context.DeadlineExceeded) {
				interruptWith(fmt.Sprintf("script timed out after %v", timeout))
			} else {
				interruptWith("script cancelled")
			}
		case <-doneWatch:
		}
	}()

	var steps int
	var logs []string

	guardStep := func() {
		if sctx.Err() != nil {
			panic(vm.ToValue(sctx.Err().Error()))
		}
		if host.IsTakeover() {
			interruptWith("paused: user took over browser control")
			panic(vm.ToValue("paused: user took over browser control"))
		}
		steps++
		if steps > browserScriptMaxSteps {
			msg := fmt.Sprintf("script exceeded maximum of %d browser steps", browserScriptMaxSteps)
			interruptWith(msg)
			panic(vm.ToValue(msg))
		}
	}

	runAct := func(act map[string]any) any {
		guardStep()
		repaired, _ := repairBrowserAction(act)
		out, err := host.ExecAction(sctx, repaired)
		if err != nil {
			panic(vm.NewGoError(err))
		}
		return out
	}

	_ = vm.Set("log", func(call goja.FunctionCall) goja.Value {
		if len(logs) >= browserScriptMaxLogs {
			return goja.Undefined()
		}
		parts := make([]string, 0, len(call.Arguments))
		for _, a := range call.Arguments {
			parts = append(parts, stringifyJSArg(a))
		}
		line := strings.Join(parts, " ")
		if len(line) > 1000 {
			line = line[:1000] + "…"
		}
		logs = append(logs, line)
		return goja.Undefined()
	})
	_ = vm.Set("console", map[string]any{
		"log": func(call goja.FunctionCall) goja.Value {
			if len(logs) < browserScriptMaxLogs {
				parts := make([]string, 0, len(call.Arguments))
				for _, a := range call.Arguments {
					parts = append(parts, stringifyJSArg(a))
				}
				logs = append(logs, strings.Join(parts, " "))
			}
			return goja.Undefined()
		},
	})

	_ = vm.Set("snapshot", func() any {
		guardStep()
		snap, err := host.Snapshot(sctx)
		if err != nil {
			panic(vm.NewGoError(err))
		}
		return snap
	})

	_ = vm.Set("navigate", func(u string, opts ...map[string]any) any {
		if err := validateBrowserTarget(u); err != nil {
			panic(vm.NewGoError(err))
		}
		act := map[string]any{"action": "goto", "url": u}
		if len(opts) > 0 && opts[0] != nil {
			for k, v := range opts[0] {
				act[k] = v
			}
		}
		return runAct(act)
	})
	_ = vm.Set("goto", func(u string, opts ...map[string]any) any {
		if err := validateBrowserTarget(u); err != nil {
			panic(vm.NewGoError(err))
		}
		act := map[string]any{"action": "goto", "url": u}
		if len(opts) > 0 && opts[0] != nil {
			for k, v := range opts[0] {
				act[k] = v
			}
		}
		return runAct(act)
	})

	_ = vm.Set("click", func(target string, opts ...map[string]any) any {
		act := map[string]any{"action": "click", "selector": target}
		if len(opts) > 0 && opts[0] != nil {
			for k, v := range opts[0] {
				act[k] = v
			}
		}
		return runAct(act)
	})

	_ = vm.Set("fill", func(target, text string, opts ...map[string]any) any {
		act := map[string]any{"action": "fill", "selector": target, "text": text}
		if len(opts) > 0 && opts[0] != nil {
			for k, v := range opts[0] {
				act[k] = v
			}
		}
		return runAct(act)
	})

	_ = vm.Set("fillFields", func(fields []any) any {
		return runAct(map[string]any{"action": "fill_fields", "fields": fields})
	})

	_ = vm.Set("press", func(key string) any {
		return runAct(map[string]any{"action": "press", "key": key})
	})

	_ = vm.Set("select", func(target, value string) any {
		return runAct(map[string]any{"action": "select", "selector": target, "value": value})
	})

	_ = vm.Set("scroll", func(arg any) any {
		act := map[string]any{"action": "scroll"}
		switch v := arg.(type) {
		case int64:
			act["y"] = int(v)
		case float64:
			act["y"] = int(v)
		case string:
			act["to"] = v
		case map[string]any:
			for k, val := range v {
				act[k] = val
			}
		default:
			act["y"] = 600
		}
		return runAct(act)
	})

	_ = vm.Set("evaluate", func(expr string) any {
		guardStep()
		val, err := host.Evaluate(sctx, expr, browserScriptMaxEvalBytes)
		if err != nil {
			panic(vm.NewGoError(err))
		}
		return val
	})

	_ = vm.Set("sleep", func(ms int64) {
		if ms <= 0 {
			return
		}
		if ms > 5000 {
			ms = 5000
		}
		deadline := time.Now().Add(time.Duration(ms) * time.Millisecond)
		for time.Now().Before(deadline) {
			if host.IsTakeover() {
				interruptWith("paused: user took over browser control")
				panic(vm.ToValue("paused: user took over browser control"))
			}
			select {
			case <-sctx.Done():
				panic(vm.ToValue(sctx.Err().Error()))
			case <-time.After(100 * time.Millisecond):
			}
		}
	})

	_ = vm.Set("waitFor", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) == 0 {
			panic(vm.NewTypeError("waitFor requires a selector string or predicate function"))
		}
		timeoutMs := int64(8000)
		if len(call.Arguments) > 1 && !goja.IsUndefined(call.Arguments[1]) && !goja.IsNull(call.Arguments[1]) {
			if n := call.Arguments[1].ToInteger(); n > 0 {
				timeoutMs = n
			}
		}
		if timeoutMs > 30000 {
			timeoutMs = 30000
		}

		// Case 1: selector string -> delegate to daemon wait action.
		if fn, ok := goja.AssertFunction(call.Arguments[0]); !ok {
			sel := call.Arguments[0].String()
			out := runAct(map[string]any{"action": "wait", "selector": sel, "timeout_ms": int(timeoutMs)})
			return vm.ToValue(out)
		} else {
			// Case 2: JS predicate function -> poll until truthy.
			deadline := time.Now().Add(time.Duration(timeoutMs) * time.Millisecond)
			for {
				if host.IsTakeover() {
					interruptWith("paused: user took over browser control")
					panic(vm.ToValue("paused: user took over browser control"))
				}
				if sctx.Err() != nil {
					panic(vm.ToValue(sctx.Err().Error()))
				}
				v, err := fn(goja.Undefined())
				if err != nil {
					panic(vm.NewGoError(err))
				}
				if v != nil && v.ToBoolean() {
					return v
				}
				if time.Now().After(deadline) {
					panic(vm.NewGoError(fmt.Errorf("waitFor predicate timed out after %dms", timeoutMs)))
				}
				select {
				case <-sctx.Done():
					panic(vm.ToValue(sctx.Err().Error()))
				case <-time.After(150 * time.Millisecond):
				}
			}
		}
	})

	// Normalize optional `await` keywords outside string literals so LLMs
	// accustomed to OpenBot's `await click(...)` style work seamlessly.
	prepared := stripAwaitOutsideStrings(code)
	wrapped := "(function(){\n" + prepared + "\n})()"

	val, err := vm.RunString(wrapped)
	res.Steps = steps
	res.Logs = logs

	if reason, ok := interrupted.Load().(string); ok && reason != "" {
		res.Interrupted = reason
		if strings.HasPrefix(reason, "paused:") {
			return res, nil
		}
		return res, errors.New(reason)
	}
	if err != nil {
		if ie, ok := err.(*goja.InterruptedError); ok {
			return res, fmt.Errorf("script interrupted: %v", ie.Value())
		}
		return res, fmt.Errorf("script error: %w", err)
	}

	if val != nil && !goja.IsUndefined(val) && !goja.IsNull(val) {
		exported := val.Export()
		res.Value = capReturnPayload(exported, browserScriptMaxReturnBytes)
	}

	// Take one clean final snapshot if not interrupted by takeover.
	if !host.IsTakeover() && sctx.Err() == nil {
		if snap, snapErr := host.Snapshot(sctx); snapErr == nil {
			res.Page = snap
		}
	}
	return res, nil
}

func stringifyJSArg(v goja.Value) string {
	if v == nil || goja.IsUndefined(v) {
		return "undefined"
	}
	if goja.IsNull(v) {
		return "null"
	}
	exp := v.Export()
	switch x := exp.(type) {
	case string:
		return x
	case bool, int64, float64:
		return fmt.Sprint(x)
	default:
		if raw, err := json.Marshal(x); err == nil {
			return string(raw)
		}
		return v.String()
	}
}

func capReturnPayload(v any, maxBytes int) any {
	raw, err := json.Marshal(v)
	if err != nil || len(raw) <= maxBytes {
		return v
	}
	return map[string]any{
		"_truncated": true,
		"bytes":      len(raw),
		"preview":    string(raw[:maxBytes]) + "…",
	}
}

// stripAwaitOutsideStrings removes standalone `await ` tokens outside JS
// strings and comments so scripts written with `await click(...)` execute
// natively in Goja's synchronous host bridge.
func stripAwaitOutsideStrings(src string) string {
	var b strings.Builder
	b.Grow(len(src))
	n := len(src)
	i := 0
	for i < n {
		c := src[i]
		// Single/double/template strings
		if c == '\'' || c == '"' || c == '`' {
			quote := c
			b.WriteByte(c)
			i++
			for i < n {
				ch := src[i]
				b.WriteByte(ch)
				i++
				if ch == '\\' && i < n {
					b.WriteByte(src[i])
					i++
					continue
				}
				if ch == quote {
					break
				}
			}
			continue
		}
		// Line comment
		if c == '/' && i+1 < n && src[i+1] == '/' {
			for i < n && src[i] != '\n' {
				b.WriteByte(src[i])
				i++
			}
			continue
		}
		// Block comment
		if c == '/' && i+1 < n && src[i+1] == '*' {
			b.WriteString("/*")
			i += 2
			for i < n {
				if src[i] == '*' && i+1 < n && src[i+1] == '/' {
					b.WriteString("*/")
					i += 2
					break
				}
				b.WriteByte(src[i])
				i++
			}
			continue
		}
		// Check identifier boundary for `await`
		if strings.HasPrefix(src[i:], "await") {
			prevOK := i == 0 || !isIdentChar(src[i-1])
			afterIdx := i + 5
			nextOK := afterIdx < n && (src[afterIdx] == ' ' || src[afterIdx] == '\t' || src[afterIdx] == '\n' || src[afterIdx] == '(')
			if prevOK && nextOK {
				i += 5
				for i < n && (src[i] == ' ' || src[i] == '\t') {
					i++
				}
				continue
			}
		}
		b.WriteByte(c)
		i++
	}
	return b.String()
}

func isIdentChar(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '$'
}

// daemonScriptHost connects runBrowserScript to a live browserDaemon.
type daemonScriptHost struct {
	pool *browserPool
	key  string
	d    *browserDaemon
}

func (h *daemonScriptHost) IsTakeover() bool {
	return h.pool.isTakeover(h.key)
}

func (h *daemonScriptHost) ExecAction(ctx context.Context, act map[string]any) (map[string]any, error) {
	rep, err := h.d.call(ctx, map[string]any{"op": "script_action", "action": act})
	if err != nil {
		return nil, err
	}
	if !rep.OK {
		return nil, errors.New(rep.Error)
	}
	var out map[string]any
	if len(rep.Result) > 0 {
		_ = json.Unmarshal(rep.Result, &out)
	}
	if out == nil {
		out = map[string]any{"ok": true}
	}
	return out, nil
}

func (h *daemonScriptHost) Snapshot(ctx context.Context) (map[string]any, error) {
	rep, err := h.d.call(ctx, map[string]any{"op": "script_snapshot"})
	if err != nil {
		return nil, err
	}
	if !rep.OK {
		return nil, errors.New(rep.Error)
	}
	var out map[string]any
	if len(rep.Result) > 0 {
		if err := json.Unmarshal(rep.Result, &out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (h *daemonScriptHost) Evaluate(ctx context.Context, expr string, maxBytes int) (any, error) {
	rep, err := h.d.call(ctx, map[string]any{"op": "script_eval", "script": expr, "max_bytes": maxBytes})
	if err != nil {
		return nil, err
	}
	if !rep.OK {
		return nil, errors.New(rep.Error)
	}
	var payload struct {
		Value any `json:"value"`
	}
	if len(rep.Result) > 0 {
		if err := json.Unmarshal(rep.Result, &payload); err != nil {
			return nil, err
		}
	}
	return payload.Value, nil
}
