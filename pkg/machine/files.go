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

// FileEntry describes one entry of a workspace listing.
type FileEntry struct {
	Name    string    `json:"name"` // slash-separated, relative to the workspace
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
	IsDir   bool      `json:"is_dir"`
	Symlink bool      `json:"symlink,omitempty"`
}

// Errors returned by file operations.
var (
	ErrNotRegular = errors.New("not a regular file")
	// ErrNoSpace is a disk with nothing left to write on.
	ErrNoSpace = errors.New("no space left on the disk")
	// ErrOutsideWorkspace is a name that cannot be a path (empty, or with a NUL byte).
	ErrOutsideWorkspace = errors.New("not a usable path")
)

// absPath resolves a name: relative names and /workspace/… are in the
// session's work folder, ~ is the user's home, any other absolute path is
// that very path on this machine. The files are the owner's: nothing keeps
// the agent inside its work folder, as nothing keeps its commands.
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

// Modes of what is made on the machine: readable by the services that need
// to read it (a web server's config, a script to run).
const (
	fileMode = 0o644
	dirMode  = 0o755
)

// OpenRoot opens the session's work folder as an os.Root (for the tools that
// walk a project inside it). The caller must Close it.
func (s *Machine) OpenRoot(sessionID string) (*os.Root, error) {
	ws, err := s.Workspace(sessionID)
	if err != nil {
		return nil, err
	}
	return os.OpenRoot(ws)
}

// Stat describes a file or folder (a link is followed).
func (s *Machine) Stat(sessionID, name string) (fs.FileInfo, error) {
	abs, err := s.absPath(sessionID, name)
	if err != nil {
		return nil, err
	}
	return os.Stat(abs)
}

// Open opens a regular file for reading. FIFOs, devices and directories are
// rejected.
func (s *Machine) Open(sessionID, name string) (*os.File, fs.FileInfo, error) {
	abs, err := s.absPath(sessionID, name)
	if err != nil {
		return nil, nil, err
	}
	// O_NONBLOCK so a FIFO cannot hang the bot.
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

// ReadFile reads at most max bytes; truncated reports whether more existed.
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

// ReadTail reads the last max bytes of a file; cut reports whether it had more.
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

// Create creates or truncates a regular file (parents are created). An
// existing file keeps its mode and owner.
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

// WriteFile writes data to name.
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

// RemoveAll deletes a file or a folder tree (links are removed, never followed).
func (s *Machine) RemoveAll(sessionID, name string) error {
	abs, err := s.absPath(sessionID, name)
	if err != nil {
		return err
	}
	return os.RemoveAll(abs)
}

// Rename moves a file or folder, replacing the target.
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

// Remove deletes a file or an empty folder.
func (s *Machine) Remove(sessionID, name string) error {
	abs, err := s.absPath(sessionID, name)
	if err != nil {
		return err
	}
	return os.Remove(abs)
}

// MkdirAll creates a folder and its parents.
func (s *Machine) MkdirAll(sessionID, name string) error {
	abs, err := s.absPath(sessionID, name)
	if err != nil {
		return err
	}
	return os.MkdirAll(abs, dirMode)
}

// List returns the entries of the workspace, depth-first up to maxEntries and
// maxDepth. It never follows symlinks.
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

// ListFiles returns the regular, non-symlink files directly inside the workspace.
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

// untracked folders hold caches and dependencies, never a run's outputs.
var untracked = map[string]bool{"__pycache__": true, "node_modules": true, "venv": true, "site-packages": true}

// TrackedFiles returns the regular, non-symlink files of the workspace down
// to depth levels (1 = the root only), skipping hidden folders (.git, .cache…),
// caches and dependencies: where the outputs of a run land (out/, build/…).
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

// Recent returns the newest files and folders directly in a session's
// workspace, newest first, without creating the workspace or counting as a
// use of it (its files keep their expiry). Hidden ones and links are left out.
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

// FreeSpace is how many bytes the disk of the work folders has left.
func (s *Machine) FreeSpace() int64 {
	var st unix.Statfs_t
	if err := unix.Statfs(s.BaseDir, &st); err != nil {
		return math.MaxInt64
	}
	return int64(st.Bavail) * int64(st.Bsize)
}
