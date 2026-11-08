package config

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The agent lives in one folder, its home (default ~/.macron):
//
//	config.env       settings and keys (owner-only permissions)
//	macron.db        the database (conversations, providers, schedules…)
//	secret.key       the key that seals provider keys in the database
//	SOUL.md          the personality; edit it with any editor
//	USER.md          what the agent knows about its owner
//	MEMORY.md        what the agent learned about its work and machine
//	skills/          procedural skills, <category>/<name>/SKILL.md
//	plugins/         script plugins; mcp_servers.json lists MCP servers
//	workspaces/      the files the agent makes and receives
//
// Backing up the folder is backing up the agent.
//
// A profile (MACRON_PROFILE=work) is another agent on the same machine, with
// a home of its own (~/.macron/profiles/work) and a service of its own
// (macron-work.service): its own bot, settings, memory and skills.
const (
	EnvHome    = "MACRON_HOME"
	EnvEnvFile = "MACRON_ENV_FILE"
	EnvProfile = "MACRON_PROFILE"
)

// Profile is the profile this agent runs as ("" the default one). A name
// that is not lower-case letters, digits, - or _ (at most 32) is ignored.
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

// ServiceName is the profile's service ("macron", or "macron-<profile>").
func ServiceName() string {
	if p := Profile(); p != "" {
		return "macron-" + p
	}
	return "macron"
}

// Home is the agent's folder.
func Home() string {
	if h := strings.TrimSpace(os.Getenv(EnvHome)); h != "" {
		return filepath.Clean(h)
	}
	base := ".macron"
	if h, err := os.UserHomeDir(); err == nil && h != "" {
		base = filepath.Join(h, ".macron")
	}
	if p := Profile(); p != "" {
		return filepath.Join(base, "profiles", p)
	}
	return base
}

// HomePath is a path inside the home folder.
func HomePath(parts ...string) string {
	return filepath.Join(append([]string{Home()}, parts...)...)
}

// File names inside the home folder.
const (
	FileConfig = "config.env"
	FileDB     = "macron.db"
	FileSecret = "secret.key"
	FileSoul   = "SOUL.md"
	FileUser   = "USER.md"
	FileMemory = "MEMORY.md"
)

// EnvFilePath is the settings file to read: $MACRON_ENV_FILE, else the home
// folder's config.env.
func EnvFilePath() string {
	if p := strings.TrimSpace(os.Getenv(EnvEnvFile)); p != "" {
		return p
	}
	return HomePath(FileConfig)
}

// AliasEnv gives every MACRON_* variable its WAZE_* name (the names the code
// reads), unless that one is set, and the other way round, so a .env of the
// public Macron works here as well.
func AliasEnv() {
	for _, kv := range os.Environ() {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || v == "" {
			continue
		}
		switch {
		case strings.HasPrefix(k, "MACRON_"):
			if _, set := os.LookupEnv("WAZE_" + strings.TrimPrefix(k, "MACRON_")); !set {
				_ = os.Setenv("WAZE_"+strings.TrimPrefix(k, "MACRON_"), v)
			}
		case strings.HasPrefix(k, "WAZE_"):
			if _, set := os.LookupEnv("MACRON_" + strings.TrimPrefix(k, "WAZE_")); !set {
				_ = os.Setenv("MACRON_"+strings.TrimPrefix(k, "WAZE_"), v)
			}
		}
	}
}

// EnvDB names the database file.
const EnvDB = "WAZE_DB"

// SecretKey is the key that seals the provider keys in the database:
// $WAZE_SECRET_KEY, else the home folder's secret.key, made on first use
// (random, owner-only permissions). Losing the file loses the sealed keys,
// not the conversations.
func SecretKey() (string, error) {
	if k := strings.TrimSpace(os.Getenv("WAZE_SECRET_KEY")); k != "" {
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
