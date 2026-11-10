package config

import (
	"os"
	"path/filepath"
	"strings"
)

func ActivateToolEnv() {
	if os.Getenv("PLAYWRIGHT_BROWSERS_PATH") == "" {
		browsers := HomePath("browsers")
		if fi, err := os.Stat(browsers); err == nil && fi.IsDir() {
			_ = os.Setenv("PLAYWRIGHT_BROWSERS_PATH", browsers)
		}
	}
	if os.Getenv("MACRON_BROWSER_USE_PYTHON") == "" {
		py := HomePath("browser-agent", "bin", "python3")
		if fi, err := os.Stat(py); err == nil && fi.Mode()&0111 != 0 {
			_ = os.Setenv("MACRON_BROWSER_USE_PYTHON", py)
		}
	}
	bin := HomePath("venv", "bin")
	if fi, err := os.Stat(filepath.Join(bin, "python3")); err != nil || fi.Mode()&0111 == 0 {
		return
	}
	for _, p := range filepath.SplitList(os.Getenv("PATH")) {
		if p == bin {
			return
		}
	}
	_ = os.Setenv("PATH", strings.Join([]string{bin, os.Getenv("PATH")}, string(os.PathListSeparator)))
}
