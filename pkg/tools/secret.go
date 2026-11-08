package tools

import (
	"os"
	"path/filepath"

	"github.com/Metixpro/macron-agent/pkg/machine"
)

type secretDir struct {
	dir string
}

func newSecretDir() (*secretDir, error) {
	dir, err := os.MkdirTemp("", "waze-secret-")
	if err != nil {
		return nil, err
	}
	return &secretDir{dir: dir}, nil
}

func (s *secretDir) write(name string, data []byte) error {
	return os.WriteFile(filepath.Join(s.dir, name), data, 0o600)
}

func (s *secretDir) remove(name string) { _ = os.Remove(filepath.Join(s.dir, name)) }

func (s *secretDir) path(_ *machine.Machine, name string) string {
	return s.dir + "/" + name
}

func (s *secretDir) close() { _ = os.RemoveAll(s.dir) }
