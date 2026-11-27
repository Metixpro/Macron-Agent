package tools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/Metixpro/macron-agent/pkg/machine"
)

const (
	ToolPythonKernel = "python_kernel"
	maxKernelOutput  = 512 << 10
)

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
    eod = msg.get("eod") or "__MACRON_KERNEL_EOD__"
    if op == "eval":
        run_block(msg.get("code", ""))
        sys.stdout.flush()
        sys.stderr.flush()
        print(eod)
        sys.stdout.flush()
    elif op == "vars":
        user_vars = {k: str(type(v).__name__) for k, v in _scope.items() if not k.startswith("_")}
        print(json.dumps(user_vars))
        print(eod)
        sys.stdout.flush()
    elif op == "reset":
        _scope.clear()
        _scope["__name__"] = "__main__"
        print("Kernel reset.")
        print(eod)
        sys.stdout.flush()
`

type pyKernelSession struct {
	mu      sync.Mutex
	proc    *machine.Process
	cancel  context.CancelFunc
	chunks  chan kernelChunk
	errRead int
}

type kernelChunk struct {
	data string
	err  error
}

type KernelRegistry struct {
	mu       sync.Mutex
	sessions map[string]*pyKernelSession
}

var kernels = &KernelRegistry{
	sessions: make(map[string]*pyKernelSession),
}

func pythonBinary() string {
	if runtime.GOOS == "windows" {
		if _, err := exec.LookPath("python3"); err != nil {
			if _, err2 := exec.LookPath("python"); err2 == nil {
				return "python"
			}
		}
	}
	return "python3"
}

func newKernelNonce() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return "__MACRON_KERNEL_EOD_" + hex.EncodeToString(b[:]) + "__"
}

func (r *Registry) ExecuteInPythonKernel(ctx context.Context, sessionID, action, code string) (string, error) {
	code = strings.TrimSpace(code)
	if action == "" {
		action = "execute"
	}
	if action == "execute" {
		if code == "" {
			return "error: code is required for execute", nil
		}
		if refused := r.gateCode(ctx, sessionID, "python", code); refused != "" {
			return refused, nil
		}
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

	resetProc := func() {
		if ks.cancel != nil {
			ks.cancel()
			ks.cancel = nil
		}
		if ks.proc != nil {
			ks.proc.Close()
			ks.proc = nil
		}
		ks.chunks = nil
		ks.errRead = 0
	}

	if ks.proc == nil {
		pctx, pcancel := context.WithCancel(context.Background())
		opts := machine.RunOptions{
			Args: []string{pythonBinary(), "-u", "-c", pythonKernelServer},
		}
		p, err := r.Machine.Start(pctx, sessionID, opts)
		if err != nil {
			pcancel()
			return "", fmt.Errorf("could not start persistent python kernel: %w", err)
		}
		ch := make(chan kernelChunk, 16)
		go func(proc *machine.Process) {
			defer close(ch)
			buf := make([]byte, 8192)
			for {
				n, err := proc.Stdout.Read(buf)
				if n > 0 {
					ch <- kernelChunk{data: string(buf[:n])}
				}
				if err != nil {
					ch <- kernelChunk{err: err}
					return
				}
			}
		}(p)
		ks.proc = p
		ks.cancel = pcancel
		ks.chunks = ch
		ks.errRead = 0
	}

	op := "eval"
	if action == "reset" {
		op = "reset"
	} else if action == "variables" {
		op = "vars"
	}

	marker := newKernelNonce()
	reqData, _ := json.Marshal(map[string]any{
		"op":   op,
		"code": code,
		"eod":  marker,
	})

	if _, err := ks.proc.Stdin.Write(append(reqData, '\n')); err != nil {
		resetProc()
		return "", fmt.Errorf("kernel stdin pipe broken: %w", err)
	}

	var output strings.Builder
	timer := time.NewTimer(45 * time.Second)
	defer timer.Stop()
	timedOut := false
	eof := false

readLoop:
	for {
		select {
		case <-ctx.Done():
			resetProc()
			return "", ctx.Err()
		case <-timer.C:
			timedOut = true
			resetProc()
			break readLoop
		case ck, ok := <-ks.chunks:
			if !ok {
				eof = true
				break readLoop
			}
			if ck.data != "" {
				if output.Len() < maxKernelOutput {
					rem := maxKernelOutput - output.Len()
					if len(ck.data) > rem {
						output.WriteString(ck.data[:rem])
					} else {
						output.WriteString(ck.data)
					}
				}
				if idx := strings.Index(output.String(), marker); idx >= 0 {
					trimmed := output.String()[:idx]
					output.Reset()
					output.WriteString(trimmed)
					break readLoop
				}
			}
			if ck.err != nil {
				eof = true
				break readLoop
			}
		}
	}

	var stderr string
	if ks.proc != nil {
		allErr := ks.proc.Stderr()
		if len(allErr) > ks.errRead {
			stderr = strings.TrimSpace(allErr[ks.errRead:])
			ks.errRead = len(allErr)
		}
	}
	if eof {
		resetProc()
	}

	res := strings.TrimSpace(output.String())
	if timedOut {
		if res != "" {
			res += "\n"
		}
		res += "(execution timed out after 45s; kernel reset)"
	}
	if stderr != "" {
		res += "\n[stderr]:\n" + stderr
	}
	if res == "" {
		res = "(executed successfully with no output)"
	}
	return res, nil
}
