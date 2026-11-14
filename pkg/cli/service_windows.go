package cli

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf16"

	"github.com/Metixpro/macron-agent/pkg/config"
	"golang.org/x/sys/windows/registry"
)

const windowsRunKey = `Software\Microsoft\Windows\CurrentVersion\Run`

func psQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

func encodedPowerShell(script string) (string, error) {
	exe, err := exec.LookPath("powershell.exe")
	if err != nil {
		exe, err = exec.LookPath("pwsh.exe")
	}
	if err != nil {
		return "", fmt.Errorf("PowerShell is required: %w", err)
	}
	words := utf16.Encode([]rune(script))
	data := make([]byte, 2*len(words))
	for i, w := range words {
		binary.LittleEndian.PutUint16(data[2*i:], w)
	}
	return `"` + exe + `" -NoLogo -NoProfile -NonInteractive -WindowStyle Hidden -EncodedCommand ` + base64.StdEncoding.EncodeToString(data), nil
}

func windowsStartupName(home string) string {
	home, _ = filepath.Abs(home)
	sum := sha256.Sum256([]byte(strings.ToLower(filepath.Clean(home))))
	return fmt.Sprintf("Macron-Telegram-%x", sum[:8])
}

func windowsStartupCommand(bin, home, envPath string) (string, error) {
	// EncodedCommand is UTF-16LE on PowerShell 5 and 7. Literal quoting protects
	// spaces, Persian paths, quotes, dollar signs and backticks in all paths.
	script := "$env:MACRON_HOME=" + psQuote(home) + "; $env:MACRON_ENV_FILE=" + psQuote(envPath) + "; & " + psQuote(bin) + " telegram start"
	return encodedPowerShell(script)
}

func windowsStartupInstalled() bool {
	k, err := registry.OpenKey(registry.CURRENT_USER, windowsRunKey, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	value, _, err := k.GetStringValue(windowsStartupName(config.Home()))
	return err == nil && value != ""
}

func removeWindowsStartup(home string) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, windowsRunKey, registry.SET_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer k.Close()
	err = k.DeleteValue(windowsStartupName(home))
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	return err
}

func windowsService(io *IO, action string) int {
	switch action {
	case "install":
		if !trialRun(io) {
			return 1
		}
		bin, err := selfBinary()
		if err != nil {
			io.println(err)
			return 1
		}
		home, err := filepath.Abs(config.Home())
		if err != nil {
			io.println(err)
			return 1
		}
		envPath, err := filepath.Abs(config.EnvFilePath())
		if err != nil {
			io.println(err)
			return 1
		}
		command, err := windowsStartupCommand(bin, home, envPath)
		if err != nil {
			io.println(err)
			return 1
		}
		k, _, err := registry.CreateKey(registry.CURRENT_USER, windowsRunKey, registry.SET_VALUE)
		if err != nil {
			io.println("Cannot register Windows sign-in startup:", err)
			return 1
		}
		err = k.SetStringValue(windowsStartupName(home), command)
		k.Close()
		if err != nil {
			io.println(err)
			return 1
		}
		io.println("✓ Telegram will start when this Windows user signs in (no administrator rights needed).")
		return startDetached(io)
	case "uninstall":
		if err := removeWindowsStartup(config.Home()); err != nil {
			io.println(err)
			return 1
		}
		if readPID() != 0 && !stopDetached() {
			io.println("Cannot stop the background process.")
			return 1
		}
		io.println("✓ Windows sign-in startup removed; Telegram stopped. Conversations and settings are kept.")
		return 0
	case "start":
		if readPID() != 0 {
			io.println("✓ Telegram is already running.")
			return 0
		}
		return startTelegram(io)
	case "restart":
		return startTelegram(io)
	case "stop":
		stopDetached()
		if readPID() != 0 {
			io.println("Cannot stop the background process.")
			return 1
		}
		io.println("✓ stopped")
		return 0
	case "status":
		startup := "disabled"
		if windowsStartupInstalled() {
			startup = "enabled"
		}
		if pid := readPID(); pid > 0 {
			io.printf("✓ running (Windows background process %d); sign-in startup %s\n", pid, startup)
			return 0
		}
		io.printf("Telegram is stopped; Windows sign-in startup %s\n", startup)
		return 3
	}
	return 2
}

func cleanupWindowsPath(prefix string) error {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Environment`, registry.QUERY_VALUE|registry.SET_VALUE)
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer k.Close()
	value, typ, err := k.GetStringValue("Path")
	if errors.Is(err, registry.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var kept []string
	for _, part := range strings.Split(value, ";") {
		expanded, expandErr := registry.ExpandString(part)
		if expandErr != nil {
			expanded = part
		}
		if !strings.EqualFold(filepath.Clean(strings.Trim(expanded, `" `)), filepath.Clean(prefix)) {
			kept = append(kept, part)
		}
	}
	result := strings.Join(kept, ";")
	if result == value {
		return nil
	}
	if typ == registry.EXPAND_SZ {
		return k.SetExpandStringValue("Path", result)
	}
	return k.SetStringValue("Path", result)
}
