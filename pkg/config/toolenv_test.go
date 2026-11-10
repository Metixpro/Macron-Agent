package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestToolEnvironmentActivation(t *testing.T) {
	home := t.TempDir()
	t.Setenv(EnvHome, home)
	t.Setenv("PATH", "/usr/bin")
	ActivateToolEnv()
	if os.Getenv("PATH") != "/usr/bin" {
		t.Fatal("activated a missing environment")
	}
	bin := filepath.Join(home, "venv", "bin")
	if err := os.MkdirAll(bin, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "python3"), []byte("#!/bin/sh\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ActivateToolEnv()
	ActivateToolEnv()
	if want := bin + string(os.PathListSeparator) + "/usr/bin"; os.Getenv("PATH") != want {
		t.Fatalf("PATH: %s", os.Getenv("PATH"))
	}
}

func TestPrivateBrowserEnvironmentActivation(t *testing.T) {
	home := t.TempDir()
	t.Setenv(EnvHome, home)
	t.Setenv("PLAYWRIGHT_BROWSERS_PATH", "")
	t.Setenv("MACRON_BROWSER_USE_PYTHON", "")
	if err := os.MkdirAll(filepath.Join(home, "browsers"), 0700); err != nil {
		t.Fatal(err)
	}
	py := filepath.Join(home, "browser-agent", "bin", "python3")
	if err := os.MkdirAll(filepath.Dir(py), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(py, []byte("#!/bin/sh\n"), 0700); err != nil {
		t.Fatal(err)
	}
	ActivateToolEnv()
	if os.Getenv("PLAYWRIGHT_BROWSERS_PATH") != filepath.Join(home, "browsers") || os.Getenv("MACRON_BROWSER_USE_PYTHON") != py {
		t.Fatal("private browser runtime not activated")
	}
	t.Setenv("PLAYWRIGHT_BROWSERS_PATH", "/explicit/browser")
	ActivateToolEnv()
	if os.Getenv("PLAYWRIGHT_BROWSERS_PATH") != "/explicit/browser" {
		t.Fatal("explicit browser override lost")
	}
}
