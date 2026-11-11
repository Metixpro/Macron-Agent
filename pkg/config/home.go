package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// SecretFiles lists the files that hold the agent's keys: config.env (and the file MACRON_ENV_FILE names) and
// secret.key.
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
	if b, err := os.ReadFile(path); err == nil {
		if k := strings.TrimSpace(string(b)); k != "" {
			return k, nil
		}
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	k := hex.EncodeToString(buf)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(k+"\n"), 0o600); err != nil {
		return "", fmt.Errorf("save %s: %w", path, err)
	}
	return k, nil
}
