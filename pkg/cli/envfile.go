package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

// envFile is the settings file (config.env): KEY=VALUE lines, with the
// owner's comments and blank lines kept as they are when a value changes.
type envFile struct {
	path  string
	lines []string
}

func loadEnvFile(path string) (*envFile, error) {
	f := &envFile{path: path}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	f.lines = strings.Split(strings.TrimRight(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n"), "\n")
	if len(f.lines) == 1 && f.lines[0] == "" {
		f.lines = nil
	}
	return f, nil
}

func keyOf(line string) (string, bool) {
	t := strings.TrimSpace(line)
	if t == "" || strings.HasPrefix(t, "#") {
		return "", false
	}
	t = strings.TrimSpace(strings.TrimPrefix(t, "export "))
	k, _, ok := strings.Cut(t, "=")
	k = strings.TrimSpace(k)
	return k, ok && k != ""
}

// Get returns the value of key ("" and false when it is not set).
func (f *envFile) Get(key string) (string, bool) {
	for _, l := range f.lines {
		if k, ok := keyOf(l); ok && k == key {
			_, v, _ := strings.Cut(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(l), "export ")), "=")
			v = strings.TrimSpace(v)
			if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
				v = v[1 : len(v)-1]
			}
			return v, true
		}
	}
	return "", false
}

// Set changes key (or adds it at the end).
func (f *envFile) Set(key, value string) {
	line := key + "=" + quoteEnv(value)
	for i, l := range f.lines {
		if k, ok := keyOf(l); ok && k == key {
			f.lines[i] = line
			return
		}
	}
	f.lines = append(f.lines, line)
}

// Unset removes key.
func (f *envFile) Unset(key string) bool {
	for i, l := range f.lines {
		if k, ok := keyOf(l); ok && k == key {
			f.lines = append(f.lines[:i], f.lines[i+1:]...)
			return true
		}
	}
	return false
}

// Keys lists the keys in the file's order.
func (f *envFile) Keys() []string {
	var out []string
	for _, l := range f.lines {
		if k, ok := keyOf(l); ok {
			out = append(out, k)
		}
	}
	return out
}

// quoteEnv writes a value so that config.LoadDotEnv reads it back unchanged.
func quoteEnv(v string) string {
	if v == "" || !strings.ContainsAny(v, " #\"'\t") {
		return v
	}
	if !strings.Contains(v, `"`) {
		return `"` + v + `"`
	}
	return "'" + v + "'"
}

// Save writes the file with owner-only permissions (it holds keys).
func (f *envFile) Save() error {
	if err := os.MkdirAll(filepath.Dir(f.path), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(f.path), ".config-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.WriteString(strings.Join(f.lines, "\n") + "\n"); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, f.path)
}

// secretKey says whether a setting holds a secret (its value is never printed whole).
func secretKey(k string) bool {
	u := strings.ToUpper(k)
	for _, w := range []string{"TOKEN", "KEY", "SECRET", "PASSWORD", "DSN"} {
		if strings.Contains(u, w) {
			return true
		}
	}
	return false
}

// mask shows the ends of a secret.
func mask(v string) string {
	r := []rune(v)
	if len(r) <= 8 {
		return strings.Repeat("•", len(r))
	}
	return string(r[:3]) + strings.Repeat("•", 6) + string(r[len(r)-3:])
}
