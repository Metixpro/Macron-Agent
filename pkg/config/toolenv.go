package config

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

func toolBin(env string) string {
	if runtime.GOOS == "windows" {
		return HomePath(env, "Scripts")
	}
	return HomePath(env, "bin")
}

func toolPythonPath(env string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(toolBin(env), "python.exe")
	}
	return filepath.Join(toolBin(env), "python3")
}

func toolExecutable(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir() && (runtime.GOOS == "windows" || fi.Mode()&0111 != 0)
}

// ToolPython resolves the same private environment on both platforms. Windows
// launchers need their arguments preserved; python3 is not required on Windows.
func ToolPython() []string {
	candidates := [][]string{{toolPythonPath("venv")}}
	if runtime.GOOS == "windows" {
		candidates = append(candidates, []string{"python"}, []string{"py", "-3"}, []string{"python3"})
	} else {
		candidates = append(candidates, []string{"python3"}, []string{"python"})
	}
	for _, argv := range candidates {
		p, err := exec.LookPath(argv[0])
		if err != nil {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		cmd := exec.CommandContext(ctx, p, append(append([]string{}, argv[1:]...), "-I", "-c", "import sys")...)
		err = cmd.Run()
		cancel()
		if err == nil {
			return append([]string{p}, argv[1:]...)
		}
	}
	return nil
}

func ActivateToolEnv() {
	if os.Getenv("PLAYWRIGHT_BROWSERS_PATH") == "" {
		browsers := HomePath("browsers")
		if fi, err := os.Stat(browsers); err == nil && fi.IsDir() {
			_ = os.Setenv("PLAYWRIGHT_BROWSERS_PATH", browsers)
		}
	}
	if os.Getenv("MACRON_BROWSER_USE_PYTHON") == "" {
		py := toolPythonPath("browser-agent")
		if toolExecutable(py) {
			_ = os.Setenv("MACRON_BROWSER_USE_PYTHON", py)
		}
	}
	bin := toolBin("venv")
	if !toolExecutable(toolPythonPath("venv")) {
		return
	}
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if p == bin || runtime.GOOS == "windows" && strings.EqualFold(p, bin) {
			return
		}
	}
	_ = os.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}
