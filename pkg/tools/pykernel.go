package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Metixpro/macron-agent/pkg/machine"
)

const ToolPythonKernel = "python_kernel"

const pythonKernelServer = `import sys, json, traceback

_scope = {"__name__": "__main__"}

def run_block(code):
    try:
        # First try evaluating as an expression
        result = eval(code, _scope)
        if result is not None:
            print(repr(result))
    except SyntaxError:
        # If syntax error, execute as statement block
        try:
            exec(code, _scope)
        except Exception:
            traceback.print_exc(file=sys.stderr)
    except Exception:
        traceback.print_exc(file=sys.stderr)

for line in sys.stdin:
    line = line.strip()
    if not line:
        continue
    try:
        msg = json.loads(line)
    except Exception:
        continue
    op = msg.get("op")
    if op == "eval":
        run_block(msg.get("code", ""))
        sys.stdout.flush()
        sys.stderr.flush()
        print("__MACRON_KERNEL_EOD__")
        sys.stdout.flush()
    elif op == "vars":
        user_vars = {k: str(type(v).__name__) for k, v in _scope.items() if not k.startswith("_")}
        print(json.dumps(user_vars))
        print("__MACRON_KERNEL_EOD__")
        sys.stdout.flush()
    elif op == "reset":
        _scope.clear()
        _scope["__name__"] = "__main__"
        print("Kernel reset.")
        print("__MACRON_KERNEL_EOD__")
        sys.stdout.flush()
`

type pyKernelSession struct {
	mu   sync.Mutex
	proc *machine.Process
}

type KernelRegistry struct {
	mu       sync.Mutex
	sessions map[string]*pyKernelSession
}

var kernels = &KernelRegistry{
	sessions: make(map[string]*pyKernelSession),
}

func (r *Registry) ExecuteInPythonKernel(ctx context.Context, sessionID, action, code string) (string, error) {
	code = strings.TrimSpace(code)
	if action == "" {
		action = "execute"
	}
	if action == "execute" && code == "" {
		return "error: code is required for execute", nil
	}

	kernels.mu.Lock()
	ks := kernels.sessions[sessionID]
	if ks == nil {
		ks = &pyKernelSession{}
		kernels.sessions[sessionID] = ks
	}
	kernels.mu.Unlock()

	ks.mu.Lock()
	defer ks.mu.Unlock()

	if ks.proc == nil {
		opts := machine.RunOptions{
			Args: []string{"python3", "-u", "-c", pythonKernelServer},
		}
		p, err := r.Machine.Start(ctx, sessionID, opts)
		if err != nil {
			return "", fmt.Errorf("could not start persistent python kernel: %w", err)
		}
		ks.proc = p
	}

	op := "eval"
	if action == "reset" {
		op = "reset"
	} else if action == "variables" {
		op = "vars"
	}

	reqData, _ := json.Marshal(map[string]any{
		"op":   op,
		"code": code,
	})

	if _, err := ks.proc.Stdin.Write(append(reqData, '\n')); err != nil {
		ks.proc.Close()
		ks.proc = nil
		return "", fmt.Errorf("kernel stdin pipe broken: %w", err)
	}

	buf := make([]byte, 4096)
	var output strings.Builder
	deadline := time.Now().Add(45 * time.Second)

	for {
		if time.Now().After(deadline) {
			return output.String() + "\n(execution timed out)", nil
		}
		n, err := ks.proc.Stdout.Read(buf)
		if n > 0 {
			chunk := string(buf[:n])
			if idx := strings.Index(chunk, "__MACRON_KERNEL_EOD__"); idx >= 0 {
				output.WriteString(chunk[:idx])
				break
			}
			output.WriteString(chunk)
		}
		if err != nil {
			break
		}
	}

	stderr := ks.proc.Stderr()
	res := strings.TrimSpace(output.String())
	if stderr != "" {
		res += "\n[stderr]:\n" + stderr
	}
	if res == "" {
		res = "(executed successfully with no output)"
	}
	return res, nil
}
