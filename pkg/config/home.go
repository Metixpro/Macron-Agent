package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	EnvHome    = "MACRON_HOME"
	EnvEnvFile = "MACRON_ENV_FILE"
	EnvProfile = "MACRON_PROFILE"
)

func Profile() string {
	p := strings.TrimSpace(os.Getenv(EnvProfile))
	if p == "" || len(p) > 32 || p == "default" {
		return ""
	}
	for _, r := range p {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return ""
		}
	}
	return p
}

func ServiceName() string {
	if p := Profile(); p != "" {
		return "macron-" + p
	}
	return "macron"
}

func Home() string {
	if h := strings.TrimSpace(os.Getenv(EnvHome)); h != "" {
		return filepath.Clean(h)
	}
	base := ".macron"
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		base = filepath.Join(h, ".macron")
		if binary, err := os.Executable(); err == nil {
			if installed, err := ReadInstallation(binary); err == nil && installed.UID == os.Geteuid() {
				base = installed.Home
			}
		}
	}
	if p := Profile(); p != "" {
		return filepath.Join(base, "profiles", p)
	}
	return base
}

func HomePath(parts ...string) string {
	return filepath.Join(append([]string{Home()}, parts...)...)
}

const (
	FileConfig = "config.env"
	FileDB     = "macron.db"
	FileSecret = "secret.key"
	FileSoul   = "SOUL.md"
	FileUser   = "USER.md"
	FileMemory = "MEMORY.md"
)

func EnvFilePath() string {
	if p := strings.TrimSpace(os.Getenv(EnvEnvFile)); p != "" {
		return p
	}
	return HomePath(FileConfig)
}

func SecretFiles() []string {
	out := []string{HomePath(FileConfig), HomePath(FileSecret)}
	if p := EnvFilePath(); p != out[0] {
		if abs, err := filepath.Abs(p); err == nil {
			out = append(out, filepath.Clean(abs))
		}
	}
	return out
}

const EnvDB = "MACRON_DB"

func SecretKey() (string, error) {
	if k := strings.TrimSpace(os.Getenv("MACRON_SECRET_KEY")); k != "" {
		return k, nil
	}
	path := HomePath(FileSecret)
	readExisting := func() (string, error) {
		b, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		k := strings.TrimSpace(string(b))
		if k == "" {
			return "", fmt.Errorf("secret key file %s is empty", path)
		}
		return k, nil
	}
	k, err := readExisting()
	if err == nil {
		return k, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read %s: %w", path, err)
	}

	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	k = hex.EncodeToString(buf)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		for i := 0; i < 10; i++ {
			if existing, rerr := readExisting(); rerr == nil {
				return existing, nil
			} else if !errors.Is(rerr, os.ErrNotExist) && i == 9 {
				return "", fmt.Errorf("read %s: %w", path, rerr)
			}
			time.Sleep(20 * time.Millisecond)
		}
		return "", fmt.Errorf("read %s: key file exists but is unreadable", path)
	}
	if err != nil {
		return "", fmt.Errorf("save %s: %w", path, err)
	}
	if _, err := f.WriteString(k + "\n"); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("save %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", fmt.Errorf("save %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", fmt.Errorf("save %s: %w", path, err)
	}
	return k, nil
}
