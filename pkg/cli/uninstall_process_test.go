package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"
)

func TestUninstallProcessHelper(t *testing.T) {
	if os.Getenv("MACRON_TEST_SLEEP_HELPER") != "1" {
		return
	}
	time.Sleep(time.Minute)
	os.Exit(0)
}

func TestUninstallerStopsOnlyItsOwnBinaryProcesses(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("uses Linux process identity")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "macron")
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(target, data, 0700); err != nil {
		t.Fatal(err)
	}
	child := exec.Command(target, "-test.run=^TestUninstallProcessHelper$")
	child.Env = append(os.Environ(), "MACRON_TEST_SLEEP_HELPER=1")
	if err = child.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = child.Process.Kill(); _ = child.Wait() })
	found := false
	for i := 0; i < 100; i++ {
		if slices.Contains(matchingProcesses(target, os.Geteuid()), child.Process.Pid) {
			found = true
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !found {
		t.Fatal("owned process not discovered")
	}
	if err = stopInstallationProcesses(target); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(matchingProcesses(target, os.Geteuid()), child.Process.Pid) {
		t.Fatal("owned process remained alive")
	}
}
