package tools

import (
	"os"
	"path/filepath"

	"github.com/Metixpro/macron-agent/pkg/machine"
)

// secretDir holds what one command of the server needs and the files of the
// conversation should not carry (the cookies of a logged-in account): it
// lives outside every work folder and only its owner can open it.
type secretDir struct {
	dir string
}

func newSecretDir() (*secretDir, error) {
	dir, err := os.MkdirTemp("", "waze-secret-") // 0700
	if err != nil {
		return nil, err
	}
	return &secretDir{dir: dir}, nil
}

func (s *secretDir) write(name string, data []byte) error {
	return os.WriteFile(filepath.Join(s.dir, name), data, 0o600)
}

func (s *secretDir) remove(name string) { _ = os.Remove(filepath.Join(s.dir, name)) }

// path is where the command finds the file.
func (s *secretDir) path(_ *machine.Machine, name string) string {
	return s.dir + "/" + name
}

func (s *secretDir) close() { _ = os.RemoveAll(s.dir) }
