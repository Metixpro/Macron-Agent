package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

func RetiredKey(key string) bool {
	switch key {
	case "image_key", "image_edit_key", "music_key":
		return true
	}
	switch strings.TrimPrefix(key, prefix) {
	case "IMAGE_API_URL", "IMAGE_API_KEY", "IMAGE_EDIT_URL", "IMAGE_EDIT_KEY", "MUSIC_API_URL", "MUSIC_API_KEY":
		return true
	}
	return false
}

func PruneRetiredEnv(path string) error {
	actual, err := filepath.EvalSymlinks(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	data, err := os.ReadFile(actual)
	if err != nil {
		return err
	}
	keyOf := func(line string) string {
		raw := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "export "))
		key, _, _ := strings.Cut(raw, "=")
		return strings.TrimSpace(key)
	}
	lines := strings.SplitAfter(string(data), "\n")
	current := map[string]bool{}
	for _, line := range lines {
		current[keyOf(line)] = true
	}
	var kept strings.Builder
	changed := false
	for _, line := range lines {
		key := keyOf(line)
		if renamed, legacy := renameLegacyKey(key); legacy {
			changed = true
			if current[renamed] {
				continue
			}
			line, key = strings.Replace(line, key, renamed, 1), renamed
		}
		if RetiredKey(key) {
			changed = true
			continue
		}
		kept.WriteString(line)
	}
	if !changed {
		return nil
	}
	st, err := os.Stat(actual)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(actual), ".macron-config-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(st.Mode().Perm()); err == nil {
		_, err = f.WriteString(kept.String())
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return os.Rename(f.Name(), actual)
}
