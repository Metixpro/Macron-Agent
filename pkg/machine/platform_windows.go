//go:build windows

package machine

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var windowsSandboxHardened atomic.Bool

func setWindowsSandboxHardening(on bool) {
	windowsSandboxHardened.Store(on)
}

const (
	jobObjectBasicUIRestrictions = 4
	jobUILimitHandles            = 0x00000001
	jobUILimitReadClipboard      = 0x00000002
	jobUILimitWriteClipboard     = 0x00000004
	jobUILimitSystemParameters   = 0x00000008
	jobUILimitDisplaySettings    = 0x00000010
	jobUILimitGlobalAtoms        = 0x00000020
	jobUILimitDesktop            = 0x00000040
	jobUILimitExitWindows        = 0x00000080
)

type jobObjectBasicUIRestrictionsInfo struct {
	UIRestrictionsClass uint32
}

const (
	useGoShell = true
	foldEnv    = true

	createNoWindow   = 0x08000000
	createNewConsole = 0x00000010
)

var (
	psOnce sync.Once
	psExe  string
	psName string
)

func powershell() (string, string) {
	psOnce.Do(func() {
		if p, err := exec.LookPath("pwsh.exe"); err == nil {
			psExe, psName = p, "PowerShell 7"
			return
		}
		psName = "Windows PowerShell 5.1"
		if p, err := exec.LookPath("powershell.exe"); err == nil {
			psExe = p
			return
		}
		root := os.Getenv("SystemRoot")
		if root == "" {
			root = `C:\Windows`
		}
		psExe = filepath.Join(root, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	})
	return psExe, psName
}

func PowerShell() string {
	exe, _ := powershell()
	return exe
}

func ShellName() string {
	_, name := powershell()
	return name
}

func shellArgv(script string) ([]string, func(), error) {
	exe, _ := powershell()
	file, cleanup, err := writePSFile(script)
	if err != nil {
		return nil, nil, err
	}
	return psArgv(exe, file), cleanup, nil
}

func argsScript(args []string) string {
	return psInvoke(resolveArgv(args))
}

var (
	pyOnce sync.Once
	pyArgv []string
)

func storeStub(path string) bool {
	return strings.Contains(strings.ToLower(path), `\microsoft\windowsapps\`)
}

func pythonWorks(argv []string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], append(append([]string{}, argv[1:]...), "-c", "import sys")...)
	prepare(cmd, false)
	return cmd.Run() == nil
}

func python() []string {
	pyOnce.Do(func() {
		for _, c := range [][]string{{"python"}, {"py", "-3"}, {"python3"}} {
			p, err := exec.LookPath(c[0])
			if err != nil {
				continue
			}
			argv := append([]string{p}, c[1:]...)
			if storeStub(p) && !pythonWorks(argv) {
				continue
			}
			pyArgv = argv
			return
		}
	})
	return pyArgv
}

func resolveArgv(argv []string) []string {
	if len(argv) == 0 {
		return argv
	}
	switch strings.ToLower(argv[0]) {
	case "python3", "python":
		if py := python(); py != nil {
			return append(append([]string{}, py...), argv[1:]...)
		}
	}
	return argv
}

func available(program string) bool {
	switch strings.ToLower(program) {
	case "python3", "python":
		return python() != nil
	case "powershell":
		return true
	case "bash", "sh":
		p, err := exec.LookPath(program)
		return err == nil && !strings.Contains(strings.ToLower(p), `\system32\`)
	}
	_, err := exec.LookPath(program)
	return err == nil
}

func prepare(cmd *exec.Cmd, console bool) {
	flags := uint32(createNoWindow)
	if console {
		flags = createNewConsole
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: flags}
}

func Hide(cmd *exec.Cmd) *exec.Cmd {
	prepare(cmd, false)
	return cmd
}

func groupAttr(cmd *exec.Cmd) {
	prepare(cmd, true)
}

type tree struct {
	mu   sync.Mutex
	job  windows.Handle
	pid  int
	done bool
}

func (t *tree) attach(pid int) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pid = pid
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return
	}
	var info windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if windowsSandboxHardened.Load() {
		info.BasicLimitInformation.LimitFlags |= windows.JOB_OBJECT_LIMIT_DIE_ON_UNHANDLED_EXCEPTION |
			windows.JOB_OBJECT_LIMIT_ACTIVE_PROCESS |
			windows.JOB_OBJECT_LIMIT_JOB_MEMORY
		info.BasicLimitInformation.ActiveProcessLimit = 32
		// Cap child job tree memory (1 GiB on 32-bit, 2 GiB on 64-bit) to guard against OOM bombs.
		if unsafe.Sizeof(uintptr(0)) >= 8 {
			info.JobMemoryLimit = uintptr(2) << 30
		} else {
			info.JobMemoryLimit = uintptr(1) << 30
		}
	}
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(job)
		return
	}
	if windowsSandboxHardened.Load() {
		ui := jobObjectBasicUIRestrictionsInfo{
			UIRestrictionsClass: jobUILimitReadClipboard |
				jobUILimitWriteClipboard |
				jobUILimitSystemParameters |
				jobUILimitDisplaySettings |
				jobUILimitGlobalAtoms |
				jobUILimitDesktop |
				jobUILimitExitWindows,
		}
		_, _ = windows.SetInformationJobObject(job, jobObjectBasicUIRestrictions, uintptr(unsafe.Pointer(&ui)), uint32(unsafe.Sizeof(ui)))
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		_ = windows.CloseHandle(job)
		return
	}
	err = windows.AssignProcessToJobObject(job, h)
	_ = windows.CloseHandle(h)
	if err != nil {
		_ = windows.CloseHandle(job)
		return
	}
	t.job = job
}

func startTracked(cmd *exec.Cmd) (*tree, error) {
	t := &tree{}
	cmd.Cancel = func() error {
		t.kill()
		if cmd.Process != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	t.attach(cmd.Process.Pid)
	return t, nil
}

func startGroup(cmd *exec.Cmd) (*tree, error) {
	t := &tree{}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	t.attach(cmd.Process.Pid)
	return t, nil
}

func (t *tree) kill() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done {
		return
	}
	if t.job != 0 {
		_ = windows.TerminateJobObject(t.job, 1)
		return
	}
	if t.pid > 0 {
		k := exec.Command("taskkill", "/T", "/F", "/PID", strconv.Itoa(t.pid))
		prepare(k, false)
		_ = k.Run()
	}
}

func (t *tree) close() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done {
		return
	}
	t.done = true
	if t.job != 0 {
		_ = windows.CloseHandle(t.job)
		t.job = 0
	}
}

func platformHostEnv(map[string]string) {}

func platformDefaults(env map[string]string) {
	env["PYTHONUTF8"] = "1"
	env["PYTHONIOENCODING"] = "utf-8"
}
