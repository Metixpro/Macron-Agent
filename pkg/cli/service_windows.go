package cli

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
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

type windowsStartupRecord struct{ Binary, Home, EnvFile string }

func windowsStartupRecordPath(name string) (string, error) {
	if !strings.HasPrefix(name, "Macron-Telegram-") || len(name) != len("Macron-Telegram-")+16 {
		return "", fmt.Errorf("invalid startup profile")
	}
	for _, c := range strings.TrimPrefix(name, "Macron-Telegram-") {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return "", fmt.Errorf("invalid startup profile")
		}
	}
	root, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "Macron", "startup", name+".json"), nil
}

func windowsStartupCommand(bin, home, _ string) (string, error) {
	// Windows Run values have a 260-character limit. Keep paths/settings in a
	// private record; an encoded PowerShell script exceeds that limit easily.
	command := `"` + bin + `" telegram autostart ` + windowsStartupName(home)
	if len(utf16.Encode([]rune(command))) > 260 {
		return "", fmt.Errorf("the installation path is too long for Windows sign-in startup; install Macron in a shorter path")
	}
	return command, nil
}

func saveWindowsStartupRecord(bin, home, envPath string) error {
	path, err := windowsStartupRecordPath(windowsStartupName(home))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	data, err := json.Marshal(windowsStartupRecord{bin, home, envPath})
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0600)
}

func windowsAutoStart(io *IO, args []string) int {
	if len(args) != 1 {
		io.println("telegram autostart requires a registered Windows profile")
		return 2
	}
	path, err := windowsStartupRecordPath(args[0])
	if err != nil {
		io.println(err)
		return 2
	}
	data, err := os.ReadFile(path)
	if err != nil {
		io.println("Cannot read Windows startup profile:", err)
		return 1
	}
	var record windowsStartupRecord
	if len(data) > 16384 || json.Unmarshal(data, &record) != nil || !filepath.IsAbs(record.Home) || !filepath.IsAbs(record.EnvFile) || windowsStartupName(record.Home) != args[0] {
		io.println("Invalid Windows startup profile.")
		return 1
	}
	bin, err := selfBinary()
	if err != nil || !strings.EqualFold(bin, record.Binary) {
		io.println("The startup profile belongs to another Macron installation.")
		return 1
	}
	// Restore the environment on return for callers embedding the CLI. The bot
	// inherits the selected profile when the detached process starts.
	for key, value := range map[string]string{config.EnvHome: record.Home, config.EnvEnvFile: record.EnvFile} {
		old, existed := os.LookupEnv(key)
		defer func() {
			if existed {
				_ = os.Setenv(key, old)
			} else {
				_ = os.Unsetenv(key)
			}
		}()
		_ = os.Setenv(key, value)
	}
	return startTelegram(io)
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

func removeWindowsStartup(home string) (result error) {
	defer func() {
		if path, err := windowsStartupRecordPath(windowsStartupName(home)); err == nil {
			if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
				result = errors.Join(result, err)
			}
		}
	}()
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
		if err := saveWindowsStartupRecord(bin, home, envPath); err != nil {
			io.println("Cannot save Windows startup profile:", err)
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
