package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Metixpro/macron-agent/pkg/config"
	"golang.org/x/sys/windows/registry"
)

func TestMain(m *testing.M) {
	if os.Getenv("MACRON_CLI_PROCESS_HELPER") == "1" && len(os.Args) > 1 && os.Args[1] == "telegram" {
		for _, arg := range os.Args {
			if arg == "--check" {
				fmt.Println("ok fixture")
				os.Exit(0)
			}
		}
		fmt.Println("سلام Windows background fixture")
		time.Sleep(time.Minute)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestWindowsBackgroundLifecycleAndStartup(t *testing.T) {
	home := filepath.Join(t.TempDir(), "پروفایل ' من")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv(config.EnvHome, home)
	t.Setenv(config.EnvEnvFile, filepath.Join(home, "custom settings.env"))
	t.Setenv("MACRON_CLI_PROCESS_HELPER", "1")
	defer func() { stopDetached(); _ = removeWindowsStartup(home) }()
	out, buf := testIO()
	if code := windowsService(out, "install"); code != 0 {
		t.Fatalf("install: %d %s", code, buf)
	}
	first := readPID()
	if first == 0 || !alive(first) || !windowsStartupInstalled() {
		t.Fatalf("not running: %s", buf)
	}
	if code := windowsService(out, "status"); code != 0 {
		t.Fatal(buf.String())
	}
	if code := windowsService(out, "stop"); code != 0 {
		t.Fatal(buf.String())
	}
	if code := windowsAutoStart(out, []string{windowsStartupName(home)}); code != 0 {
		t.Fatalf("sign-in launch: %d %s", code, buf)
	}
	first = readPID()
	if first == 0 {
		t.Fatal("sign-in launch did not start the bot")
	}
	buf.Reset()
	if code := logs(out, []string{"-n", "1"}); code != 0 || !strings.Contains(buf.String(), "سلام Windows") {
		t.Fatalf("logs %d: %s", code, buf)
	}
	if code := windowsService(out, "restart"); code != 0 {
		t.Fatalf("restart: %d %s", code, buf)
	}
	if readPID() == 0 || readPID() == first || alive(first) {
		t.Fatal("restart did not replace process")
	}
	if code := windowsService(out, "uninstall"); code != 0 {
		t.Fatalf("uninstall: %d %s", code, buf)
	}
	if readPID() != 0 || windowsStartupInstalled() {
		t.Fatal("background process or startup entry left behind")
	}
	if code := windowsService(out, "status"); code != 3 {
		t.Fatal("stopped status", code)
	}
}

func TestWindowsStartupUnicodeAndLiteralPaths(t *testing.T) {
	bin := `C:\برنامه من\O'Brien $x ` + "`" + `\macron.exe`
	home := filepath.Join(t.TempDir(), "my home", ".macron")
	command, err := windowsStartupCommand(bin, home, home+`\config.env`)
	if err != nil {
		t.Fatal(err)
	}
	if len(command) > 260 || !strings.Contains(command, `"`+bin+`" telegram autostart `+windowsStartupName(home)) {
		t.Fatal(command)
	}
	defer removeWindowsStartup(home)
	if err := saveWindowsStartupRecord(bin, home, home+`\config.env`); err != nil {
		t.Fatal(err)
	}
	path, err := windowsStartupRecordPath(windowsStartupName(home))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var saved windowsStartupRecord
	if err := json.Unmarshal(raw, &saved); err != nil || saved.Binary != bin || saved.Home != home {
		t.Fatal("Unicode startup record did not round-trip", err)
	}
	if windowsStartupName(home) != windowsStartupName(strings.ToUpper(home)) {
		t.Fatal("case changes startup identity")
	}
	if windowsStartupName(home) == windowsStartupName(home+`\profiles\other`) {
		t.Fatal("profiles share a startup entry")
	}
}

func TestWindowsStalePIDDoesNotStopForeignProcess(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	if err := os.WriteFile(config.HomePath(pidFile), []byte(fmt.Sprint(os.Getpid())), 0600); err != nil {
		t.Fatal(err)
	}
	if readPID() != 0 {
		t.Fatal("accepted current CLI as bot")
	}
	if !alive(os.Getpid()) || alive(-1) {
		t.Fatal("invalid process liveness")
	}
	if err := terminate(os.Getpid()); err == nil {
		t.Fatal("allowed terminating current command")
	}
	bin := `C:\Apps\Macron\macron.exe`
	if windowsInstallationMatch(`C:\Other\macron.exe`, bin) || !windowsInstallationMatch(`c:\apps\macron\MACRON-DESKTOP.EXE`, bin) {
		t.Fatal("installation scoping failed")
	}
}

func TestWindowsDoctorNoUnixAdvice(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	// Empty PATH also covers installations with no Git, Python or Unix utilities.
	t.Setenv("PATH", filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0"))
	out, buf := testIO()
	if code := doctor(out, []string{"--offline"}); code != 0 {
		t.Fatalf("doctor %d: %s", code, buf)
	}
	for _, bad := range []string{"sudo ", "apt install", "chmod ", "install.sh", "deadsnakes", "sshpass is", "rsync is", "journalctl", "bin\\python"} {
		if strings.Contains(buf.String(), bad) {
			t.Fatalf("Windows doctor prints %q: %s", bad, buf)
		}
	}
}

func TestWindowsStartupRemovalPreservesOtherProfiles(t *testing.T) {
	home := t.TempDir()
	other := home + "-other"
	k, _, err := registry.CreateKey(registry.CURRENT_USER, windowsRunKey, registry.SET_VALUE|registry.QUERY_VALUE)
	if err != nil {
		t.Fatal(err)
	}
	defer k.Close()
	for _, p := range []string{home, other} {
		if err := k.SetStringValue(windowsStartupName(p), "fixture"); err != nil {
			t.Fatal(err)
		}
	}
	defer k.DeleteValue(windowsStartupName(other))
	defer k.DeleteValue(windowsStartupName(home))
	if err := removeWindowsStartup(home); err != nil {
		t.Fatal(err)
	}
	if _, _, err := k.GetStringValue(windowsStartupName(other)); err != nil {
		t.Fatal("removed another profile", err)
	}
}

func TestWindowsForeignPIDCannotBeTerminated(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	cmd := exec.Command("powershell.exe", "-NoProfile", "-Command", "Start-Sleep -Seconds 30")
	cmd.SysProcAttr = detached()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	pid := cmd.Process.Pid
	if err := os.WriteFile(config.HomePath(pidFile), []byte(fmt.Sprint(pid)), 0600); err != nil {
		t.Fatal(err)
	}
	if readPID() != 0 {
		t.Fatal("accepted unrelated PowerShell as Telegram bot")
	}
	if err := terminate(pid); err == nil {
		t.Fatal("allowed terminating another executable")
	}
	if !alive(pid) {
		t.Fatal("foreign process was terminated")
	}
}

func TestWindowsStartupRejectsForeignProfile(t *testing.T) {
	home := t.TempDir()
	name := windowsStartupName(home)
	defer removeWindowsStartup(home)
	if err := saveWindowsStartupRecord(`C:\Other\macron.exe`, home, filepath.Join(home, "config.env")); err != nil {
		t.Fatal(err)
	}
	out, buf := testIO()
	if code := windowsAutoStart(out, []string{name}); code != 1 || !strings.Contains(buf.String(), "another Macron installation") {
		t.Fatalf("%d %s", code, buf)
	}
	if _, err := windowsStartupRecordPath("../../other"); err == nil {
		t.Fatal("allowed startup record traversal")
	}
}
