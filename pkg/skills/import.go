package skills

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

// ImportZip, ImportTarGz and ImportArchive add every skill folder of an
// archive (any folder holding a SKILL.md, with its references/, templates/
// and scripts/), e.g. a download of the Hermes Agent skills. The folder above
// a skill names its category when the front matter does not. A skill with the
// same name is replaced. Paths that leave the folder, links, hidden files and
// oversized archives are refused. The archive is read from its raw bytes.

const (
	maxZipBytes   = 30 << 20
	maxZipEntries = 3000
	maxZipFile    = 2 << 20
	maxSkillDepth = 4
)

// ImportResult says what an import did.
type ImportResult struct {
	Imported []string // names
	Skipped  []string // "folder: reason"
}

// entry is one regular file of an archive.
type entry struct {
	size uint64
	read func() ([]byte, error)
}

var gzipMagic = []byte{0x1f, 0x8b}

// IsArchive reports whether data is a zip, a tar.gz or a tar.
func IsArchive(data []byte) bool {
	return bytes.HasPrefix(data, []byte("PK\x03\x04")) || bytes.HasPrefix(data, gzipMagic) || isTar(data)
}

func isTar(data []byte) bool { return len(data) > 262 && string(data[257:262]) == "ustar" }

// ImportArchive imports a zip, a tar.gz (.tgz) or a tar, whichever data is.
func (s *Store) ImportArchive(data []byte) (ImportResult, error) {
	if bytes.HasPrefix(data, []byte("PK\x03\x04")) {
		return s.ImportZip(data)
	}
	if bytes.HasPrefix(data, gzipMagic) || isTar(data) {
		return s.ImportTarGz(data)
	}
	return ImportResult{}, errors.New("not a zip or tar.gz archive")
}

func (s *Store) ImportZip(data []byte) (ImportResult, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return ImportResult{}, errors.New("not a zip archive")
	}
	if len(zr.File) > maxZipEntries {
		return ImportResult{}, fmt.Errorf("the archive has more than %d files", maxZipEntries)
	}
	var total uint64
	files := map[string]entry{}
	for _, f := range zr.File {
		total += f.UncompressedSize64
		if total > maxZipBytes {
			return ImportResult{}, fmt.Errorf("the archive is larger than %d MB unpacked", maxZipBytes>>20)
		}
		name := archiveName(f.Name)
		if f.FileInfo().IsDir() || name == "" || f.Mode()&os.ModeSymlink != 0 {
			continue
		}
		files[name] = entry{size: f.UncompressedSize64, read: func() ([]byte, error) { return readZipFile(f) }}
	}
	return s.importFiles(files)
}

// ImportTarGz is ImportZip for a .tar.gz / .tgz (or a plain .tar).
func (s *Store) ImportTarGz(data []byte) (ImportResult, error) {
	var r io.Reader = bytes.NewReader(data)
	if bytes.HasPrefix(data, gzipMagic) {
		zr, err := gzip.NewReader(r)
		if err != nil {
			return ImportResult{}, errors.New("not a tar.gz archive")
		}
		defer zr.Close()
		r = zr
	}
	tr := tar.NewReader(r)
	var total uint64
	files := map[string]entry{}
	for n := 0; ; n++ {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			if n == 0 {
				return ImportResult{}, errors.New("not a tar.gz archive")
			}
			return ImportResult{}, fmt.Errorf("the archive is damaged: %w", err)
		}
		if n >= maxZipEntries {
			return ImportResult{}, fmt.Errorf("the archive has more than %d files", maxZipEntries)
		}
		name := archiveName(h.Name)
		if !h.FileInfo().Mode().IsRegular() || name == "" || h.Size < 0 {
			continue // folders, links, devices
		}
		size := uint64(h.Size)
		if total += size; total > maxZipBytes {
			return ImportResult{}, fmt.Errorf("the archive is larger than %d MB unpacked", maxZipBytes>>20)
		}
		if size > maxZipFile {
			files[name] = entry{size: size, read: func() ([]byte, error) { return nil, tooBig(name) }}
			continue
		}
		body, err := io.ReadAll(io.LimitReader(tr, maxZipFile+1))
		if err != nil {
			return ImportResult{}, fmt.Errorf("the archive is damaged: %w", err)
		}
		files[name] = entry{size: uint64(len(body)), read: func() ([]byte, error) { return body, nil }}
	}
	return s.importFiles(files)
}

// archiveName is a member's path, cleaned and relative ("" for the root).
func archiveName(name string) string {
	return strings.TrimPrefix(path.Clean("/"+strings.ReplaceAll(name, "\\", "/")), "/")
}

func tooBig(name string) error {
	return fmt.Errorf("%s is larger than %d MB", path.Base(name), maxZipFile>>20)
}

// importFiles imports every skill folder of an archive's files.
func (s *Store) importFiles(files map[string]entry) (ImportResult, error) {
	var res ImportResult
	var roots []string
	for name := range files {
		if path.Base(name) == FileName {
			roots = append(roots, path.Dir(name))
		}
	}
	if len(roots) == 0 {
		return res, errors.New("no SKILL.md in the archive")
	}
	sort.Strings(roots)
	for _, dir := range roots {
		label := dir
		if label == "." {
			label = "(top)"
		}
		name, err := s.importFolder(files, dir)
		if err != nil {
			res.Skipped = append(res.Skipped, label+": "+err.Error())
			continue
		}
		res.Imported = append(res.Imported, name)
	}
	return res, nil
}

func readZipFile(f *zip.File) ([]byte, error) {
	if f.UncompressedSize64 > maxZipFile {
		return nil, tooBig(f.Name)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, maxZipFile+1))
	if err == nil && len(data) > maxZipFile {
		err = tooBig(f.Name)
	}
	return data, err
}

// importFolder writes one skill folder of the archive in place, atomically.
func (s *Store) importFolder(files map[string]entry, dir string) (string, error) {
	raw, err := files[path.Join(dir, FileName)].read()
	if err != nil {
		return "", err
	}
	if len(raw) > MaxBody || !utf8.Valid(raw) {
		return "", ErrBigBody
	}
	meta, body := Parse(string(raw))
	name := NormalizeName(meta.get("name"))
	if name == "" && dir != "." {
		name = NormalizeName(path.Base(dir))
	}
	category := NormalizeName(meta.get("category"))
	if !ValidName(category) {
		category = DefaultCategory
		if parent := path.Base(path.Dir(dir)); dir != "." && parent != "." && ValidName(NormalizeName(parent)) && parent != "skills" {
			category = NormalizeName(parent)
		}
	}
	desc := strings.TrimSpace(meta.get("description"))
	switch {
	case !ValidName(name):
		return "", fmt.Errorf("%w (got %q)", ErrBadName, name)
	case desc == "":
		return "", ErrNoDescription
	case strings.TrimSpace(body) == "":
		return "", ErrEmptyBody
	}
	if utf8.RuneCountInString(desc) > MaxDescription {
		meta.set("description", strings.TrimSpace(string([]rune(desc)[:MaxDescription-1]))+"…")
	}
	meta.set("name", name)
	meta.set("category", category)
	if meta.get("version") == "" {
		meta.set("version", DefaultVersion)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	tmp, err := os.MkdirTemp(s.dir, ".import-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)
	if err := os.WriteFile(filepath.Join(tmp, FileName), []byte(Render(meta, body)), 0o644); err != nil {
		return "", err
	}
	prefix := dir + "/"
	if dir == "." {
		prefix = ""
	}
	for zname, f := range files {
		if !strings.HasPrefix(zname, prefix) {
			continue
		}
		rel := strings.TrimPrefix(zname, prefix)
		if rel == FileName || rel == "" {
			continue
		}
		parts := strings.Split(rel, "/")
		if len(parts) > maxSkillDepth || hiddenPart(parts) || containsSkill(files, prefix, parts) {
			continue
		}
		data, err := f.read()
		if err != nil {
			return "", err
		}
		target := filepath.Join(tmp, filepath.FromSlash(rel))
		if !strings.HasPrefix(target, tmp+string(filepath.Separator)) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(target, data, 0o644); err != nil {
			return "", err
		}
	}
	final := filepath.Join(s.dir, category, name)
	if err := os.MkdirAll(filepath.Dir(final), 0o755); err != nil {
		return "", err
	}
	trash, err := os.MkdirTemp(s.dir, ".trash-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(trash)
	if old, ok := s.byName[name]; ok && old.Dir != final && strings.HasPrefix(old.Dir, s.dir+string(filepath.Separator)) {
		_ = os.Rename(old.Dir, filepath.Join(trash, "other"))
		removeIfEmpty(filepath.Dir(old.Dir), s.dir)
	}
	if _, err := os.Lstat(final); err == nil {
		if err := os.Rename(final, filepath.Join(trash, "old")); err != nil {
			return "", err
		}
	}
	if err := os.Rename(tmp, final); err != nil {
		return "", err
	}
	if err := os.Chmod(final, 0o755); err != nil {
		return "", err
	}
	return name, s.reloadLocked()
}

func hiddenPart(parts []string) bool {
	for _, p := range parts {
		if strings.HasPrefix(p, ".") || p == "__pycache__" || p == "node_modules" {
			return true
		}
	}
	return false
}

// containsSkill reports whether a file belongs to a skill nested deeper.
func containsSkill(files map[string]entry, prefix string, parts []string) bool {
	for i := 1; i < len(parts); i++ {
		if _, ok := files[prefix+strings.Join(parts[:i], "/")+"/"+FileName]; ok {
			return true
		}
	}
	return false
}
