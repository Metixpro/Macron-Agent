package machine

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

type FileEntry struct {
	Name    string    `json:"name"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
	IsDir   bool      `json:"is_dir"`
	Symlink bool      `json:"symlink,omitempty"`
}

var (
	ErrNotRegular = errors.New("not a regular file")

	ErrNoSpace = errors.New("no space left on the disk")

	ErrOutsideWorkspace = errors.New("not a usable path")
)

func (s *Machine) absPath(sessionID, name string) (string, error) {
	if name == "" || strings.ContainsRune(name, 0) {
		return "", fmt.Errorf("%w: %q", ErrOutsideWorkspace, name)
	}
	ws, err := s.Workspace(sessionID)
	if err != nil {
		return "", err
	}
	p := filepath.FromSlash(name)
	switch {
	case p == MountPoint || strings.HasPrefix(p, MountPoint+string(filepath.Separator)):
		p = filepath.Join(ws, strings.TrimPrefix(p, MountPoint))
	case p == "~" || strings.HasPrefix(p, "~"+string(filepath.Separator)):
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, strings.TrimPrefix(p, "~"))
		}
	case !filepath.IsAbs(p):
		p = filepath.Join(ws, p)
	}
	return filepath.Clean(p), nil
}

func (s *Machine) HostPath(sessionID, name string) (abs string, inside bool, err error) {
	abs, err = s.absPath(sessionID, name)
	if err != nil {
		return "", false, err
	}
	top := s.topWorkspace(sessionID)
	return abs, abs == top || strings.HasPrefix(abs, top+string(filepath.Separator)), nil
}

// Inside reports whether name is an existing file or folder of the session's workspace once every symlink on the way
// is resolved, so a link that points out of the workspace does not count.
func (s *Machine) Inside(sessionID, name string) bool {
	abs, inside, err := s.HostPath(sessionID, name)
	if err != nil || !inside {
		return false
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return false
	}
	top, err := filepath.EvalSymlinks(s.topWorkspace(sessionID))
	if err != nil {
		return false
	}
	return real == top || strings.HasPrefix(real, top+string(filepath.Separator))
}

const (
	fileMode = 0o644
	dirMode  = 0o755
)

func (s *Machine) OpenRoot(sessionID string) (*os.Root, error) {
	ws, err := s.Workspace(sessionID)
	if err != nil {
		return nil, err
	}
	return os.OpenRoot(ws)
}

func (s *Machine) Stat(sessionID, name string) (fs.FileInfo, error) {
	abs, err := s.absPath(sessionID, name)
	if err != nil {
		return nil, err
	}
	return os.Stat(abs)
}

func (s *Machine) Open(sessionID, name string) (*os.File, fs.FileInfo, error) {
	abs, err := s.absPath(sessionID, name)
	if err != nil {
		return nil, nil, err
	}

	f, err := os.OpenFile(abs, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	if !info.Mode().IsRegular() {
		f.Close()
		return nil, nil, fmt.Errorf("%w: %q", ErrNotRegular, name)
	}
	return f, info, nil
}

func (s *Machine) ReadFile(sessionID, name string, max int64) (data []byte, truncated bool, err error) {
	f, _, err := s.Open(sessionID, name)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	data, err = io.ReadAll(io.LimitReader(f, max+1))
	if err != nil {
		return nil, false, err
	}
	if int64(len(data)) > max {
		return data[:max], true, nil
	}
	return data, false, nil
}

func (s *Machine) ReadTail(sessionID, name string, max int64) (data []byte, cut bool, err error) {
	f, info, err := s.Open(sessionID, name)
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	if info.Size() > max {
		if _, err := f.Seek(-max, io.SeekEnd); err != nil {
			return nil, false, err
		}
		cut = true
	}
	data, err = io.ReadAll(io.LimitReader(f, max))
	return data, cut, err
}

func (s *Machine) Create(sessionID, name string) (*os.File, error) {
	abs, err := s.absPath(sessionID, name)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(abs), dirMode); err != nil {
		return nil, err
	}
	return os.OpenFile(abs, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, fileMode)
}

func (s *Machine) WriteFile(sessionID, name string, data []byte) error {
	f, err := s.Create(sessionID, name)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func (s *Machine) RemoveAll(sessionID, name string) error {
	abs, err := s.absPath(sessionID, name)
	if err != nil {
		return err
	}
	return os.RemoveAll(abs)
}

func (s *Machine) Rename(sessionID, from, to string) error {
	a, err := s.absPath(sessionID, from)
	if err != nil {
		return err
	}
	b, err := s.absPath(sessionID, to)
	if err != nil {
		return err
	}
	return os.Rename(a, b)
}

func (s *Machine) Remove(sessionID, name string) error {
	abs, err := s.absPath(sessionID, name)
	if err != nil {
		return err
	}
	return os.Remove(abs)
}

func (s *Machine) MkdirAll(sessionID, name string) error {
	abs, err := s.absPath(sessionID, name)
	if err != nil {
		return err
	}
	return os.MkdirAll(abs, dirMode)
}

func (s *Machine) List(sessionID string, maxDepth, maxEntries int) ([]FileEntry, error) {
	ws, err := s.Workspace(sessionID)
	if err != nil {
		return nil, err
	}
	var out []FileEntry
	err = filepath.WalkDir(ws, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(ws, p)
		if rel == "." {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		out = append(out, FileEntry{
			Name:    filepath.ToSlash(rel),
			Size:    info.Size(),
			ModTime: info.ModTime(),
			IsDir:   d.IsDir(),
			Symlink: d.Type()&fs.ModeSymlink != 0,
		})
		if len(out) >= maxEntries {
			return fs.SkipAll
		}
		if d.IsDir() && strings.Count(rel, string(filepath.Separator))+1 >= maxDepth {
			return fs.SkipDir
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, err
}

func (s *Machine) ListFiles(sessionID string) ([]FileEntry, error) {
	entries, err := s.List(sessionID, 1, 10000)
	if err != nil {
		return nil, err
	}
	files := entries[:0]
	for _, e := range entries {
		if !e.IsDir && !e.Symlink {
			files = append(files, e)
		}
	}
	return files, nil
}

var untracked = map[string]bool{"__pycache__": true, "node_modules": true, "venv": true, "site-packages": true}

func (s *Machine) TrackedFiles(sessionID string, depth, maxFiles int) ([]FileEntry, error) {
	ws, err := s.Workspace(sessionID)
	if err != nil {
		return nil, err
	}
	var out []FileEntry
	err = filepath.WalkDir(ws, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == ws {
			return nil
		}
		rel, _ := filepath.Rel(ws, p)
		if d.IsDir() {
			if strings.HasPrefix(d.Name(), ".") || untracked[d.Name()] || strings.Count(rel, string(filepath.Separator))+1 >= depth {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, ierr := d.Info()
		if ierr != nil {
			return nil
		}
		out = append(out, FileEntry{Name: filepath.ToSlash(rel), Size: info.Size(), ModTime: info.ModTime()})
		if len(out) >= maxFiles {
			return fs.SkipAll
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, err
}

func (s *Machine) Recent(sessionID string, n int) []FileEntry {
	dir := s.topWorkspace(sessionID)
	if sub := SubPath(sessionID); sub != "" {
		dir = filepath.Join(dir, filepath.FromSlash(sub))
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []FileEntry
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") || (!e.Type().IsRegular() && !e.IsDir()) || untracked[e.Name()] {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, FileEntry{Name: e.Name(), Size: info.Size(), ModTime: info.ModTime(), IsDir: e.IsDir()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ModTime.After(out[j].ModTime) })
	if len(out) > n {
		out = out[:n]
	}
	return out
}

func (s *Machine) FreeSpace() int64 {
	var st unix.Statfs_t
	if err := unix.Statfs(s.BaseDir, &st); err != nil {
		return math.MaxInt64
	}
	return int64(st.Bavail) * int64(st.Bsize)
}
