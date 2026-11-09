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
	base := ""
	if strings.HasPrefix(key, "MACRON_") {
		base = strings.TrimPrefix(key, "MACRON_")
	} else if strings.HasPrefix(key, "WAZE_") {
		base = strings.TrimPrefix(key, "WAZE_")
	}
	switch base {
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
	var kept strings.Builder
	changed := false
	for _, line := range strings.SplitAfter(string(data), "\n") {
		raw := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "export "))
		key, _, _ := strings.Cut(raw, "=")
		key = strings.TrimSpace(key)
		removed := RetiredKey(key)
		if removed {
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
