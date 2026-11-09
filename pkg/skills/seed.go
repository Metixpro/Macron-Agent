package skills

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

//go:embed seed
var seedFS embed.FS

func (s *Store) Seed() ([]string, error) {
	retired, err := s.retireSeeds()
	if err != nil {
		return nil, err
	}
	marker := filepath.Join(s.dir, seededFile)
	data, err := os.ReadFile(marker)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	done := strings.Fields(string(data))
	oldDone := len(done)
	done = slices.DeleteFunc(done, func(name string) bool { return name == "image-editing" || name == "songwriting" })
	var added []string
	changed := len(done) != oldDone
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
	if len(added) > 0 || retired {
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

func retireSeed(path, expected string) (bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != expected {
		return false, nil
	}
	if err = os.Remove(path); err != nil {
		return false, err
	}
	_ = os.Remove(filepath.Dir(path))
	return true, nil
}
func (s *Store) retireSeeds() (bool, error) {
	retired := false
	for rel, hash := range map[string]string{"media/image-editing/SKILL.md": "ef5c43a8115953f780bcb60f5071c88a9df93d6e396f9de93c2ba1f8b95a692b", "media/songwriting/SKILL.md": "31ab66a225d9eb372ff6a1898f2e2363511ff7d4375c22dfea74cde6af71b2ae"} {
		removed, err := retireSeed(filepath.Join(s.dir, filepath.FromSlash(rel)), hash)
		if err != nil {
			return retired, err
		}
		retired = retired || removed
	}
	return retired, nil
}
