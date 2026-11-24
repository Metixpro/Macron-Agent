package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var connectionConfigMu sync.Mutex

func toolEnvLine(line string) (string, string, bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}
	line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
	key, value, ok := strings.Cut(line, "=")
	if !ok {
		return "", "", false
	}
	key, value = strings.TrimSpace(key), strings.TrimSpace(value)
	if len(value) >= 2 && value[0] == '"' && value[len(value)-1] == '"' {
		value = unescapeDoubleQuoted(value[1 : len(value)-1])
	} else if len(value) >= 2 && value[0] == '\'' && value[len(value)-1] == '\'' {
		value = value[1 : len(value)-1]
	} else if i := strings.Index(value, " #"); i >= 0 {
		value = strings.TrimSpace(value[:i])
	}
	return key, value, key != ""
}

func unescapeDoubleQuoted(s string) string {
	if !strings.ContainsRune(s, '\\') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+1 < len(s) && (s[i+1] == '"' || s[i+1] == '\\') {
			i++
		}
		b.WriteByte(s[i])
	}
	return b.String()
}
func ToolEnvValue(key string) string {
	value, _ := ToolEnvLookup(key)
	return value
}
func ToolEnvLookup(key string) (string, bool) {
	f, err := os.Open(EnvFilePath())
	if err == nil {
		defer f.Close()
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 4096), 1<<20)
		var val string
		found := false
		for sc.Scan() {
			k, v, ok := toolEnvLine(sc.Text())
			if ok && k == key {
				val, found = v, true
			}
		}
		if found {
			return val, true
		}
	}
	return os.LookupEnv(key)
}
func SaveToolEnv(values map[string]string) error {
	connectionConfigMu.Lock()
	defer connectionConfigMu.Unlock()
	path := EnvFilePath()
	if actual, err := filepath.EvalSymlinks(path); err == nil {
		path = actual
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	data, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for key, value := range values {
		if key == "" || strings.ContainsAny(key, "= \r\n\t\x00") || strings.ContainsAny(value, "\r\n\x00") || len(value) > 8192 {
			return fmt.Errorf("invalid single-line setting %s", key)
		}
	}
	var lines []string
	seen := map[string]bool{}
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		key, _, ok := toolEnvLine(line)
		if value, replace := values[key]; ok && replace {
			if !seen[key] {
				lines = append(lines, key+"="+quoteToolEnv(value))
				seen[key] = true
			}
			continue
		}
		if line != "" || len(data) > 0 {
			lines = append(lines, line)
		}
	}
	for key, value := range values {
		if !seen[key] {
			lines = append(lines, key+"="+quoteToolEnv(value))
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".connections-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := f.Chmod(0600); err != nil {
		return err
	}
	if _, err := f.WriteString(strings.Join(lines, "\n") + "\n"); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path)
}
func quoteToolEnv(value string) string {
	if !strings.ContainsAny(value, " #\t\"'") {
		return value
	}
	if !strings.Contains(value, "\"") {
		return "\"" + value + "\""
	}
	if !strings.Contains(value, "'") {
		return "'" + value + "'"
	}
	return "\"" + strings.ReplaceAll(strings.ReplaceAll(value, "\\", "\\\\"), "\"", "\\\"") + "\""
}
