package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestActivatePrivateToolEnvironments(t *testing.T) {
	t.Setenv(EnvHome, t.TempDir())
	t.Setenv("PATH", "unchanged")
	t.Setenv("MACRON_BROWSER_USE_PYTHON", "")
	t.Setenv("PLAYWRIGHT_BROWSERS_PATH", "")
	for _, env := range []string{"venv", "browser-agent"} {
		py := toolPythonPath(env)
		if err := os.MkdirAll(filepath.Dir(py), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(py, []byte("fixture"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(HomePath("browsers"), 0700); err != nil {
		t.Fatal(err)
	}
	ActivateToolEnv()
	first := os.Getenv("PATH")
	ActivateToolEnv()
	if first != toolBin("venv")+string(os.PathListSeparator)+"unchanged" || os.Getenv("PATH") != first {
		t.Fatal(os.Getenv("PATH"))
	}
	if os.Getenv("MACRON_BROWSER_USE_PYTHON") != toolPythonPath("browser-agent") {
		t.Fatal("browser Python not activated")
	}
	if runtime.GOOS == "windows" && !strings.Contains(first, `\Scripts`) {
		t.Fatal(first)
	}
}
