package tools

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"

	"github.com/Metixpro/macron-agent/pkg/machine"
)

var (
	telegramPartBytes int64 = 45 << 20
	telegramMaxBytes  int64 = 49 << 20
)

const maxListed = 150

var externalExts = []string{".tar.xz", ".txz", ".tar.zst", ".tzst", ".rar", ".7z", ".001", ".gz", ".bz2", ".xz", ".zst", ".lzma",
	".iso", ".cab"}

var archiverOnce struct {
	sync.Once
	list []string
}

func (r *Registry) archivers() []string {
	archiverOnce.Do(func() {
		have := r.Machine.Available("7z", "7zz", "unar", "bsdtar")
		for _, p := range []string{"7z", "7zz", "unar", "bsdtar"} {
			if have[p] && !(p == "7zz" && have["7z"]) {
				archiverOnce.list = append(archiverOnce.list, p)
			}
		}
	})
	return archiverOnce.list
}

func (r *Registry) zipNeedsArchiver(sessionID, name, kind string) bool {
	if kind != "zip" {
		return false
	}
	f, info, err := r.Machine.Open(sessionID, name)
	if err != nil {
		return false
	}
	defer f.Close()
	zr, err := zip.NewReader(f, info.Size())
	if err != nil {
		return false
	}
	for _, zf := range zr.File {
		if zf.Flags&0x1 != 0 || (zf.Method != zip.Store && zf.Method != zip.Deflate) {
			return true
		}
	}
	return false
}

func archiverArgs(tool, name, out, password string) []string {
	switch tool {
	case "7z", "7zz":
		pw := password
		if pw == "" {
			pw = "-"
		}
		return []string{tool, "x", "-y", "-bd", "-bb0", "-p" + pw, "-o./" + out, "./" + name}
	case "unar":
		argv := []string{"unar", "-q", "-f", "-D", "-o", "./" + out}
		if password != "" {
			argv = append(argv, "-p", password)
		}
		return append(argv, "./"+name)
	}
	argv := []string{"bsdtar", "-xf", "./" + name, "-C", "./" + out}
	if password != "" {
		argv = append(argv, "--passphrase", password)
	}
	return argv
}

func needsPassword(out string) bool {
	l := strings.ToLower(out)
	return strings.Contains(l, "wrong password") || strings.Contains(l, "requires a password") || strings.Contains(l, "password is required") ||
		strings.Contains(l, "incorrect password") || strings.Contains(l, "encrypted") && strings.Contains(l, "password")
}

func (r *Registry) extractExternal(ctx context.Context, sessionID, name, dest, password string) string {
	tools := r.archivers()
	if len(tools) == 0 {
		return "error: this server has no archiver for that format (install 7zip or unar); a .zip or .tar.gz works"
	}
	if info, err := r.Machine.Stat(sessionID, name); err != nil || !info.Mode().IsRegular() {
		return fmt.Sprintf("error: %q is not a file in the workspace", name)
	}
	tmp := fmt.Sprintf(".unpack-%d", time.Now().UnixNano())
	defer r.Machine.RemoveAll(sessionID, tmp)
	var out string
	ok := false
	for _, tool := range tools {
		_ = r.Machine.RemoveAll(sessionID, tmp)
		_ = r.Machine.MkdirAll(sessionID, tmp)
		res := r.Machine.Run(ctx, sessionID, machine.RunOptions{Args: archiverArgs(tool, name, tmp, password), Timeout: 10 * time.Minute,
			Heavy: true, MergeOutput: true})
		if res.ExitCode == 0 {
			ok = true
			break
		}
		out = strings.TrimSpace(res.Stdout + " " + res.Error)
		if needsPassword(out) {
			if password == "" {
				return "error: the archive is password protected; ask for its password and call again with password"
			}
			return "error: the password is wrong"
		}
	}
	if !ok {
		return "error: could not unpack " + name + ": " + tail(out, 600)
	}
	files, size, skipped := r.cleanTree(sessionID, tmp)
	if files == 0 {
		return "error: nothing came out of " + name + " (empty, or only links)"
	}
	src := tmp
	if fsys, done, err := r.projectFS(sessionID); err == nil {
		entries, _ := fs.ReadDir(fsys, tmp)
		done()
		switch {
		case len(entries) == 1 && !entries[0].IsDir() && archiveKind(entries[0].Name()) == "tar":

			return r.extract(ctx, sessionID, tmp+"/"+entries[0].Name(), dest, "")
		case len(entries) == 1 && entries[0].IsDir():
			src = tmp + "/" + entries[0].Name()
		}
	}
	moved, err := r.moveTree(sessionID, src, dest)
	if err != nil {
		return "error: " + friendlyFSError(err)
	}
	where := dest + "/"
	if dest == "." {
		where = "the workspace root"
	}
	s := fmt.Sprintf("Unpacked %d files (%s) into %s.", moved, humanSize(size), where)
	if skipped > 0 {
		s += fmt.Sprintf(" %d links or special files were dropped.", skipped)
	}
	return s + " Map it with project_tree."
}

func (r *Registry) cleanTree(sessionID, dir string) (files int, size int64, dropped int) {
	fsys, done, err := r.projectFS(sessionID)
	if err != nil {
		return 0, 0, 0
	}
	var drop []string
	_ = fs.WalkDir(fsys, dir, func(p string, d fs.DirEntry, err error) error {
		switch {
		case err != nil || p == dir:
		case d.Type()&fs.ModeSymlink != 0 || (!d.IsDir() && !d.Type().IsRegular()):
			drop = append(drop, p)
		case !d.IsDir():
			if info, err := d.Info(); err == nil {
				files++
				size += info.Size()
			}
		}
		return nil
	})
	done()
	for _, p := range drop {
		_ = r.Machine.Remove(sessionID, p)
	}
	return files, size, len(drop)
}

func (r *Registry) moveTree(sessionID, src, dest string) (int, error) {
	fsys, done, err := r.projectFS(sessionID)
	if err != nil {
		return 0, err
	}
	var files []string
	_ = fs.WalkDir(fsys, src, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			files = append(files, p)
		}
		return nil
	})
	done()
	n := 0
	for _, f := range files {
		target := strings.TrimPrefix(f, src+"/")
		if dest != "." {
			target = dest + "/" + target
		}
		if d := path.Dir(target); d != "." {
			if err := r.Machine.MkdirAll(sessionID, d); err != nil {
				return n, err
			}
		}
		if err := r.Machine.Rename(sessionID, f, target); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func (r *Registry) listArchive(ctx context.Context, sessionID, name, password string) string {
	kind := archiveKind(name)
	if kind == "" {
		return "error: unsupported archive"
	}
	f, info, err := r.Machine.Open(sessionID, name)
	if err != nil {
		return "error: " + friendlyFSError(err)
	}
	defer f.Close()
	type item struct {
		name      string
		size      int64
		encrypted bool
	}
	var items []item
	switch kind {
	case "zip":
		zr, err := zip.NewReader(f, info.Size())
		if err != nil {
			return "error: not a valid zip: " + err.Error()
		}
		for _, zf := range zr.File {
			if !zf.FileInfo().IsDir() {
				items = append(items, item{zipName(zf), int64(zf.UncompressedSize64), zf.Flags&0x1 != 0})
			}
		}
	case "tar", "tgz", "tbz":
		tr, err := tarReader(f, kind)
		if err != nil {
			return "error: " + err.Error()
		}
		for len(items) < maxArchiveFiles {
			h, err := tr.Next()
			if err != nil {
				break
			}
			//lint:ignore SA1019 archives written by old tar programs still mark files this way
			if h.Typeflag == tar.TypeReg || h.Typeflag == tar.TypeRegA {
				items = append(items, item{h.Name, h.Size, false})
			}
		}
	default:
		return r.listExternal(ctx, sessionID, name, password)
	}
	var total int64
	enc := 0
	var sb strings.Builder
	for i, it := range items {
		total += it.size
		if it.encrypted {
			enc++
		}
		if i < maxListed {
			fmt.Fprintf(&sb, "%9s  %s\n", humanSize(it.size), it.name)
		}
	}
	head := fmt.Sprintf("%s holds %d files, %s unpacked", name, len(items), humanSize(total))
	if enc > 0 {
		head += fmt.Sprintf("; %d are password protected", enc)
	}
	if len(items) > maxListed {
		fmt.Fprintf(&sb, "… and %d more\n", len(items)-maxListed)
	}
	return head + ":\n" + strings.TrimRight(sb.String(), "\n")
}

func (r *Registry) listExternal(ctx context.Context, sessionID, name, password string) string {
	tools := r.archivers()
	if len(tools) == 0 {
		return "error: this server has no archiver for that format (install 7zip or unar)"
	}
	pw := password
	if pw == "" {
		pw = "-"
	}
	var argv []string
	switch tools[0] {
	case "7z", "7zz":
		argv = []string{tools[0], "l", "-ba", "-p" + pw, "./" + name}
	case "unar":
		argv = []string{"lsar", "./" + name}
		if password != "" {
			argv = []string{"lsar", "-p", password, "./" + name}
		}
	default:
		argv = []string{"bsdtar", "-tvf", "./" + name}
	}
	res := r.Machine.Run(ctx, sessionID, machine.RunOptions{Args: argv, Timeout: 2 * time.Minute, MergeOutput: true})
	out := strings.TrimSpace(res.Stdout)
	if res.ExitCode != 0 {
		if needsPassword(out) {
			return "error: the archive's list itself is password protected; call again with password"
		}
		return "error: could not read " + name + ": " + tail(out+" "+res.Error, 600)
	}
	lines := strings.Split(out, "\n")
	if len(lines) > maxListed {
		lines = append(lines[:maxListed], fmt.Sprintf("… and %d more lines", len(lines)-maxListed))
	}
	return name + " holds:\n" + strings.Join(lines, "\n")
}

func (r *Registry) packFiles(ctx context.Context, sessionID string, args map[string]any) (string, error) {
	raw := anyList(args["paths"])
	if p := strArg(args, "path"); p != "" {
		raw = append([]string{p}, raw...)
	}
	if len(raw) == 0 {
		return "error: paths is required (files and folders to pack; '.' for everything)", nil
	}
	var roots []string
	seen := map[string]bool{}
	for _, p := range raw {
		c, err := cleanPath(p)
		if err != nil {
			return "error: " + err.Error(), nil
		}
		if _, err := r.Machine.Stat(sessionID, c); err != nil && c != "." {
			return fmt.Sprintf("error: %s: %s", p, friendlyFSError(err)), nil
		}
		if !seen[c] {
			seen[c] = true
			roots = append(roots, c)
		}
	}
	format := strings.ToLower(strings.TrimPrefix(strArg(args, "format"), "."))
	ext := map[string]string{"": ".zip", "zip": ".zip", "tar.gz": ".tar.gz", "tgz": ".tar.gz", "7z": ".7z"}[format]
	if ext == "" {
		return "error: format is zip, tar.gz or 7z", nil
	}
	password := rawStrArg(args, "password")
	if password != "" && ext == ".tar.gz" {
		return "error: a tar.gz cannot carry a password; use zip or 7z", nil
	}
	name := strArg(args, "filename")
	if name == "" {
		base := path.Base(roots[0])
		if roots[0] == "." {
			base = "workspace"
		} else if len(roots) > 1 {
			base = "files"
		} else if !strings.Contains(base, ".") || ext != ".zip" {
			base = strings.TrimSuffix(base, path.Ext(base))
		}
		name = base + ext
	}
	if !strings.HasSuffix(strings.ToLower(name), ext) {
		name = archiveBase(name)
		name = strings.TrimSuffix(name, "_files") + ext
	}
	name, err := cleanPath(name)
	if err != nil {
		return "error: " + err.Error(), nil
	}
	all := boolArg(args, "include_all")

	var files, left int
	var rawSize int64
	if ext == ".7z" || password != "" {
		files, left, rawSize, err = r.pack7z(ctx, sessionID, roots, name, ext, password, all)
	} else {
		files, left, rawSize, err = r.packGo(sessionID, roots, name, ext, all)
	}
	if err != nil {
		return "error: " + friendlyFSError(err), nil
	}
	st, err := r.Machine.Stat(sessionID, name)
	if err != nil {
		return "error: the archive was not made", nil
	}
	s := fmt.Sprintf("Created %s: %d files, %s", name, files, humanSize(st.Size()))
	if rawSize > 0 {
		s += fmt.Sprintf(" (from %s)", humanSize(rawSize))
	}
	if password != "" {
		s += ", AES-256 password protected"
	}
	if left > 0 {
		s += fmt.Sprintf("; %d dependency/build/cache folders left out", left)
	}
	part := int64(intArg(args, "split_mb", 0, 0, 2000)) << 20
	if part == 0 && st.Size() > telegramMaxBytes {
		part = telegramPartBytes
	}
	if part > 0 && st.Size() > part {
		parts, err := r.splitFile(sessionID, name, part, st.Size())
		if err != nil {
			return s + ". It could not be cut into parts: " + friendlyFSError(err), nil
		}
		return s + fmt.Sprintf(". Cut into %d parts of at most %s ("+SendNotePlural+"): %s. Open the .001 part with 7-Zip, "+
			"WinRAR or ZArchiver with all parts side by side, or join them: cat %s.0* > %s.", len(parts), humanSize(part),
			strings.Join(parts, ", "), path.Base(name), path.Base(name)), nil
	}
	return s + ". Send it with send_file.", nil
}

func (r *Registry) packGo(sessionID string, roots []string, name, ext string, all bool) (files, left int, raw int64, err error) {
	fsys, done, err := r.projectFS(sessionID)
	if err != nil {
		return 0, 0, 0, err
	}
	defer done()
	tmp := name + ".part"
	out, err := r.Machine.Create(sessionID, tmp)
	if err != nil {
		return 0, 0, 0, err
	}
	var zw *zip.Writer
	var gz *gzip.Writer
	var tw *tar.Writer
	if ext == ".zip" {
		zw = zip.NewWriter(out)
	} else {
		gz = gzip.NewWriter(out)
		tw = tar.NewWriter(gz)
	}
	budget := r.Machine.FreeSpace()
	names := map[string]bool{}
	for _, root := range roots {
		base := path.Dir(root)
		err = walk(fsys, root, all, func(p string, d fs.DirEntry, skipped bool) error {
			if skipped {
				left++
			}
			if d.IsDir() || p == tmp || p == name || !d.Type().IsRegular() || (root == "." && hiddenPath(p) && !all) {
				return nil
			}
			rel := p
			if base != "." {
				rel = strings.TrimPrefix(p, base+"/")
			}
			if names[rel] {
				return nil
			}
			names[rel] = true
			info, err := d.Info()
			if err != nil {
				return nil
			}
			if raw += info.Size(); raw > budget {
				return fmt.Errorf("not enough free space for the archive")
			}
			src, err := fsys.Open(p)
			if err != nil {
				return nil
			}
			defer src.Close()
			if zw != nil {
				h, err := zip.FileInfoHeader(info)
				if err != nil {
					return nil
				}
				h.Name, h.Method = rel, zip.Deflate
				w, err := zw.CreateHeader(h)
				if err != nil {
					return err
				}
				if _, err := io.Copy(w, src); err != nil {
					return err
				}
			} else {
				h, err := tar.FileInfoHeader(info, "")
				if err != nil {
					return nil
				}
				h.Name, h.Uname, h.Gname, h.Uid, h.Gid = rel, "", "", 0, 0
				if err := tw.WriteHeader(h); err != nil {
					return err
				}
				if _, err := io.Copy(tw, src); err != nil {
					return err
				}
			}
			files++
			return nil
		})
		if err != nil {
			break
		}
	}
	var cerr error
	if zw != nil {
		cerr = zw.Close()
	} else {
		if cerr = tw.Close(); cerr == nil {
			cerr = gz.Close()
		}
	}
	out.Close()
	if err == nil {
		err = cerr
	}
	if err == nil && files == 0 {
		err = fmt.Errorf("nothing to pack")
	}
	if err != nil {
		_ = r.Machine.Remove(sessionID, tmp)
		return 0, 0, 0, err
	}
	return files, left, raw, r.Machine.Rename(sessionID, tmp, name)
}

func (r *Registry) pack7z(ctx context.Context, sessionID string, roots []string, name, ext, password string, all bool) (int, int, int64, error) {
	tool := ""
	for _, t := range r.archivers() {
		if t == "7z" || t == "7zz" {
			tool = t
		}
	}
	if tool == "" {
		return 0, 0, 0, fmt.Errorf("7-Zip is not installed on this server, so no 7z or password; a plain zip or tar.gz works")
	}
	tmp := name + ".part"
	_ = r.Machine.Remove(sessionID, tmp)
	typ := "-t7z"
	if ext == ".zip" {
		typ = "-tzip"
	}
	argv := []string{tool, "a", typ, "-mx=5", "-bd", "-bb0", "-y"}
	if password != "" {
		argv = append(argv, "-p"+password)
		if ext == ".7z" {
			argv = append(argv, "-mhe=on")
		} else {
			argv = append(argv, "-mem=AES256")
		}
	}
	left := 0
	if !all {
		if fsys, done, err := r.projectFS(sessionID); err == nil {
			for _, root := range roots {
				_ = walk(fsys, root, false, func(_ string, _ fs.DirEntry, skipped bool) error {
					if skipped {
						left++
					}
					return nil
				})
			}
			done()
		}
		var skip []string
		for d, on := range skippedDirs {
			if on {
				skip = append(skip, d)
			}
		}
		sort.Strings(skip)
		for _, d := range append(skip, docIndexDir, machine.SubDir) {
			argv = append(argv, "-xr!"+d)
		}
	}
	argv = append(argv, "./"+tmp)
	for _, root := range roots {
		if root == "." {
			argv = append(argv, "./*", "-x!"+tmp)
		} else {
			argv = append(argv, "./"+root)
		}
	}
	res := r.Machine.Run(ctx, sessionID, machine.RunOptions{Args: argv, Timeout: 15 * time.Minute, Heavy: true, MergeOutput: true})
	if res.ExitCode != 0 {
		_ = r.Machine.Remove(sessionID, tmp)
		return 0, 0, 0, fmt.Errorf("7-Zip failed: %s", tail(strings.TrimSpace(res.Stdout+" "+res.Error), 500))
	}
	if err := r.Machine.Rename(sessionID, tmp, name); err != nil {
		return 0, 0, 0, err
	}
	files := 0
	var raw int64
	pw := password
	if pw == "" {
		pw = "-"
	}
	if l := r.Machine.Run(ctx, sessionID, machine.RunOptions{Args: []string{tool, "l", "-ba", "-slt", "-p" + pw, "./" + name},
		Timeout: time.Minute}); l.ExitCode == 0 {
		for _, block := range strings.Split(l.Stdout, "\n\n") {
			if strings.Contains(block, "Path = ") && !strings.Contains(block, "Folder = +") && !strings.Contains(block, "Attributes = D") {
				files++
				for _, line := range strings.Split(block, "\n") {
					if v, ok := strings.CutPrefix(line, "Size = "); ok {
						var n int64
						fmt.Sscan(v, &n)
						raw += n
					}
				}
			}
		}
	}
	return files, left, raw, nil
}

func (r *Registry) splitFile(sessionID, name string, part, size int64) ([]string, error) {
	if size > r.Machine.FreeSpace() {
		return nil, fmt.Errorf("not enough free space to cut it into parts")
	}
	src, _, err := r.Machine.Open(sessionID, name)
	if err != nil {
		return nil, err
	}
	var parts []string
	for i := 1; ; i++ {
		p := fmt.Sprintf("%s.%03d", name, i)
		out, err := r.Machine.Create(sessionID, p)
		if err != nil {
			src.Close()
			return parts, err
		}
		n, err := io.CopyN(out, src, part)
		out.Close()
		if n == 0 {
			_ = r.Machine.Remove(sessionID, p)
			break
		}
		parts = append(parts, p)
		if err == io.EOF {
			break
		}
		if err != nil {
			src.Close()
			return parts, err
		}
	}
	src.Close()
	return parts, r.Machine.Remove(sessionID, name)
}

func zipName(zf *zip.File) string {
	if utf8.ValidString(zf.Name) {
		return zf.Name
	}
	if s, err := charmap.Windows1256.NewDecoder().String(zf.Name); err == nil && strings.ContainsFunc(s, func(r rune) bool {
		return unicode.Is(unicode.Arabic, r)
	}) {
		return s
	}
	if s, err := charmap.CodePage437.NewDecoder().String(zf.Name); err == nil {
		return s
	}
	return strings.ToValidUTF8(zf.Name, "_")
}
