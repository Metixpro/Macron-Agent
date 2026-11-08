package skills

import (
	"embed"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// The built-in skills, so a new server starts with some.
//
//go:embed seed
var seedFS embed.FS

// Seed copies the built-in skills that were never offered before. A list of
// the ones already offered is kept in the folder, so a built-in skill an admin
// deleted stays deleted, and one added by a newer version still arrives.
func (s *Store) Seed() ([]string, error) {
	marker := filepath.Join(s.dir, seededFile)
	data, err := os.ReadFile(marker)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	done := strings.Fields(string(data))
	var added []string
	changed := false
	cats, _ := fs.ReadDir(seedFS, "seed")
	for _, c := range cats {
		names, _ := fs.ReadDir(seedFS, path.Join("seed", c.Name()))
		for _, n := range names {
			if !n.IsDir() || slices.Contains(done, n.Name()) {
				continue
			}
			done, changed = append(done, n.Name()), true
			if _, exists := s.Get(n.Name()); exists {
				continue
			}
			if err := copySeed(path.Join("seed", c.Name(), n.Name()), filepath.Join(s.dir, c.Name(), n.Name())); err != nil {
				return added, err
			}
			added = append(added, n.Name())
		}
	}
	if changed {
		slices.Sort(done)
		if err := writeAtomic(marker, []byte(strings.Join(done, "\n")+"\n")); err != nil {
			return added, err
		}
	}
	if len(added) > 0 {
		return added, s.Reload()
	}
	return added, nil
}

func copySeed(src, dst string) error {
	return fs.WalkDir(seedFS, src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		target := filepath.Join(dst, filepath.FromSlash(strings.TrimPrefix(p, src)))
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := seedFS.ReadFile(p)
		if err != nil {
			return err
		}
		return writeAtomic(target, data)
	})
}
