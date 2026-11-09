package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Installation struct {
	Binary string
	Home   string
	Scope  string
	UID    int
}

func InstallRecordPath(binary string) string {
	return filepath.Join(filepath.Dir(binary), ".macron-install")
}

func ReadInstallation(binary string) (Installation, error) {
	var out Installation
	path := InstallRecordPath(binary)
	st, err := os.Lstat(path)
	if err != nil {
		return out, err
	}
	if !st.Mode().IsRegular() || st.Size() > 16384 {
		return out, fmt.Errorf("invalid installation record: %s", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return out, err
	}
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	if !scanner.Scan() || scanner.Text() != "macron-install-v1" {
		return out, fmt.Errorf("invalid installation record header")
	}
	uidSet := false
	for scanner.Scan() {
		k, v, ok := strings.Cut(scanner.Text(), "=")
		if !ok {
			continue
		}
		switch k {
		case "binary":
			out.Binary = v
		case "home":
			out.Home = v
		case "scope":
			out.Scope = v
		case "uid":
			out.UID, err = strconv.Atoi(v)
			uidSet = err == nil
		}
	}
	if err := scanner.Err(); err != nil {
		return out, err
	}
	if !uidSet || out.UID < 0 || !filepath.IsAbs(out.Binary) || !filepath.IsAbs(out.Home) || (out.Scope != "user" && out.Scope != "system") {
		return out, fmt.Errorf("incomplete installation record")
	}
	actual := filepath.Clean(binary)
	recorded := filepath.Clean(out.Binary)
	if p, e := filepath.EvalSymlinks(actual); e == nil {
		actual = p
	}
	if p, e := filepath.EvalSymlinks(recorded); e == nil {
		recorded = p
	}
	if actual != recorded {
		return out, fmt.Errorf("installation record belongs to a different binary")
	}
	return out, nil
}
