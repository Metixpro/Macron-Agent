package desk

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/Metixpro/macron-agent/pkg/machine"
)

const (
	uploadDirName  = ".macron-uploads"
	maxUploadBytes = 25 << 20 // 25 MB
)

var unsafeFileRe = regexp.MustCompile(`[^a-zA-Z0-9._\-\p{L}]+`)

func sanitizeUploadName(name string) string {
	base := filepath.Base(strings.TrimSpace(name))
	if base == "" || base == "." || base == "/" || base == `\` {
		base = "attachment"
	}
	ext := filepath.Ext(base)
	stem := strings.TrimSuffix(base, ext)
	stem = strings.Trim(unsafeFileRe.ReplaceAllString(stem, "_"), "._-")
	ext = unsafeFileRe.ReplaceAllString(ext, "")
	if stem == "" {
		stem = "file"
	}
	if r := []rune(stem); len(r) > 80 {
		stem = string(r[:80])
	}
	if r := []rune(ext); len(r) > 16 {
		ext = string(r[:16])
	}
	return stem + ext
}

func randomHex(nBytes int) string {
	b := make([]byte, nBytes)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func ensureUploadExcluded(projectDir string) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	out, err := machine.Hide(exec.CommandContext(ctx, "git", "-C", projectDir, "rev-parse", "--git-path", "info/exclude")).Output()
	if err != nil {
		return
	}
	p := strings.TrimSpace(string(out))
	if p == "" {
		return
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(projectDir, p)
	}
	entry := uploadDirName + "/"
	existing, _ := os.ReadFile(p)
	for _, line := range strings.Split(string(existing), "\n") {
		if strings.TrimSpace(line) == entry {
			return
		}
	}
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	if f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
		prefix := ""
		if len(existing) > 0 && existing[len(existing)-1] != '\n' {
			prefix = "\n"
		}
		_, _ = f.WriteString(prefix + entry + "\n")
		_ = f.Close()
	}
}

func (s *Server) uploadAPI(w http.ResponseWriter, r *http.Request) {
	p := s.withProject(w, r)
	if p == nil {
		return
	}

	var (
		rawName string
		data    []byte
		err     error
	)

	ct := strings.ToLower(r.Header.Get("Content-Type"))
	if strings.HasPrefix(ct, "multipart/form-data") {
		r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes+1<<20)
		if err = r.ParseMultipartForm(8 << 20); err != nil {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("file too large or invalid upload: %w", err))
			return
		}
		if r.MultipartForm != nil {
			defer r.MultipartForm.RemoveAll()
		}
		f, hdr, ferr := r.FormFile("file")
		if ferr != nil {
			writeErr(w, http.StatusBadRequest, errors.New("missing file field"))
			return
		}
		defer f.Close()
		rawName = hdr.Filename
		data, err = io.ReadAll(io.LimitReader(f, maxUploadBytes+1))
		if err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		if len(data) > maxUploadBytes {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("file exceeds %d MB limit", maxUploadBytes>>20))
			return
		}
	} else {
		var in struct {
			Name string `json:"name"`
			Data string `json:"data"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, (maxUploadBytes*4)/3+1<<20)
		if err = readJSONLimit(r, &in, (maxUploadBytes*4)/3+1<<20); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		rawName = in.Name
		encoded := strings.TrimSpace(in.Data)
		if idx := strings.Index(encoded, ","); strings.HasPrefix(encoded, "data:") && idx >= 0 {
			encoded = encoded[idx+1:]
		}
		data, err = base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			writeErr(w, http.StatusBadRequest, errors.New("invalid base64 file data"))
			return
		}
		if len(data) > maxUploadBytes {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("file exceeds %d MB limit", maxUploadBytes>>20))
			return
		}
	}

	if len(data) == 0 {
		writeErr(w, http.StatusBadRequest, errors.New("empty file"))
		return
	}

	dir := filepath.Join(p.path, uploadDirName)
	if fi, err := os.Lstat(dir); err == nil {
		if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
			writeErr(w, http.StatusForbidden, errors.New("path must stay inside the project folder"))
			return
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if _, _, err := resolveInside(p.path, uploadDirName); err != nil {
		writeErr(w, http.StatusForbidden, err)
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	ensureUploadExcluded(p.path)

	safe := sanitizeUploadName(rawName)
	stamped := fmt.Sprintf("%d-%s-%s", time.Now().UnixMilli(), randomHex(4), safe)
	abs, _, err := resolveInside(p.path, filepath.Join(uploadDirName, stamped))
	if err != nil {
		writeErr(w, http.StatusForbidden, err)
		return
	}
	if err := os.WriteFile(abs, data, 0o600); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	rel := filepath.ToSlash(filepath.Join(uploadDirName, stamped))
	mimeType := fileType(abs)
	if mimeType == "" {
		mimeType = http.DetectContentType(data)
		if i := strings.IndexByte(mimeType, ';'); i >= 0 {
			mimeType = strings.TrimSpace(mimeType[:i])
		}
	}

	writeJSON(w, File{
		Name: rel,
		Base: safe,
		Size: int64(len(data)),
		Type: mimeType,
	})
}
