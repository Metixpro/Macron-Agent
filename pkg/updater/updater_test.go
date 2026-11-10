package updater

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewerComparesVersionsByNumber(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"1.3.10", "1.3.9", true}, {"v1.4.0", "1.3.99", true}, {"2.0", "1.9.9", true}, {"1.3.2", "1.3.2", false},
		{"1.3.2", "v1.3.2", false}, {"1.3.1", "1.3.2", false}, {"1.3", "1.3.0", false}, {"1.3.2-rc1", "1.3.2", false}, {"", "1.0.0", false},
	} {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%q, %q) = %v", c.a, c.b, got)
		}
	}
}

func TestChecksumOfReadsOnlyAWholeLine(t *testing.T) {
	sum := strings.Repeat("ab", 32)
	sums := sum + "  macron-1.0.0-linux-amd64.tar.gz\n" + strings.Repeat("cd", 32) + " *macron-1.0.0-darwin-arm64.tar.gz\nshort  x.tar.gz\n"
	if got, ok := ChecksumOf(sums, "macron-1.0.0-linux-amd64.tar.gz"); !ok || got != sum {
		t.Errorf("got %q %v", got, ok)
	}
	if got, ok := ChecksumOf(sums, "macron-1.0.0-darwin-arm64.tar.gz"); !ok || got != strings.Repeat("cd", 32) {
		t.Errorf("a binary-mode line: %q %v", got, ok)
	}
	for _, missing := range []string{"x.tar.gz", "macron-1.0.0-linux-arm64.tar.gz", "amd64.tar.gz"} {
		if _, ok := ChecksumOf(sums, missing); ok {
			t.Errorf("%q has no usable sum", missing)
		}
	}
}

func archiveWith(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

func TestExtractBinaryFindsTheProgram(t *testing.T) {
	bin, err := ExtractBinary(archiveWith(t, map[string]string{"macron-1.0.0-linux-amd64/README.md": "hi", "macron-1.0.0-linux-amd64/macron": "PROGRAM"}))
	if err != nil || string(bin) != "PROGRAM" {
		t.Fatalf("%q %v", bin, err)
	}
	if _, err := ExtractBinary(archiveWith(t, map[string]string{"x/README.md": "hi"})); err == nil {
		t.Error("an archive without the program is refused")
	}
	if _, err := ExtractBinary([]byte("not an archive")); err == nil {
		t.Error("a broken archive is refused")
	}
}

func fakeRelease(t *testing.T, version string, archive []byte, sums string) *Release {
	t.Helper()
	name := AssetName(version)
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	mux.HandleFunc("/dl/"+name, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(archive) })
	mux.HandleFunc("/dl/SHA256SUMS", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, sums) })
	mux.HandleFunc("/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept") != "application/vnd.github+json" {
			t.Errorf("the release is asked for as %q", r.Header.Get("Accept"))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": "v" + version, "assets": []map[string]any{
			{"name": name, "browser_download_url": srv.URL + "/dl/" + name}, {"name": "SHA256SUMS", "browser_download_url": srv.URL + "/dl/SHA256SUMS"}}})
	})
	old := ReleaseAPI
	ReleaseAPI = srv.URL + "/releases"
	t.Cleanup(func() { ReleaseAPI = old })
	rel, err := Latest(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if rel.Version() != version {
		t.Fatalf("version %q", rel.Version())
	}
	return rel
}

func installedProgram(t *testing.T) string {
	t.Helper()
	self := filepath.Join(t.TempDir(), "macron")
	if err := os.WriteFile(self, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	oldSelf, oldPreflight := SelfPath, Preflight
	SelfPath = func() (string, error) { return self, nil }
	Preflight = func(string, string) error { return nil }
	t.Cleanup(func() { SelfPath, Preflight = oldSelf, oldPreflight })
	return self
}

func TestInstallSwapsTheProgramAndKeepsTheOldOne(t *testing.T) {
	archive := archiveWith(t, map[string]string{"macron-9.9.9/macron": "NEW"})
	sum := sha256.Sum256(archive)
	rel := fakeRelease(t, "9.9.9", archive, hex.EncodeToString(sum[:])+"  "+AssetName("9.9.9")+"\n")
	self := installedProgram(t)
	var steps []string
	if err := Install(context.Background(), rel, func(s string) { steps = append(steps, s) }); err != nil {
		t.Fatal(err)
	}
	if now, _ := os.ReadFile(self); string(now) != "NEW" {
		t.Errorf("the program is %q", now)
	}
	if old, _ := os.ReadFile(self + ".old"); string(old) != "OLD" {
		t.Errorf("the kept program is %q", old)
	}
	if fi, err := os.Stat(self); err != nil || fi.Mode().Perm()&0o100 == 0 {
		t.Errorf("the new program cannot be run: %v", err)
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(self), ".macron-update-*")); len(left) > 0 {
		t.Errorf("the staging folder stayed: %v", left)
	}
	if len(steps) < 3 {
		t.Errorf("steps: %v", steps)
	}
}

func TestInstallRefusesADownloadThatDoesNotMatchItsSum(t *testing.T) {
	archive := archiveWith(t, map[string]string{"macron-9.9.9/macron": "TAMPERED"})
	for name, sums := range map[string]string{
		"a wrong sum":       strings.Repeat("0", 64) + "  " + AssetName("9.9.9") + "\n",
		"no line for it":    strings.Repeat("0", 64) + "  another-file.tar.gz\n",
		"an empty sum file": "",
	} {
		rel := fakeRelease(t, "9.9.9", archive, sums)
		self := installedProgram(t)
		if err := Install(context.Background(), rel, nil); err == nil || !strings.Contains(err.Error(), "NOT installed") {
			t.Errorf("%s: %v", name, err)
		}
		if now, _ := os.ReadFile(self); string(now) != "OLD" {
			t.Errorf("%s: the program became %q", name, now)
		}
	}
}

func TestInstallKeepsTheProgramWhenTheNewOneDoesNotStart(t *testing.T) {
	archive := archiveWith(t, map[string]string{"macron-9.9.9/macron": "BROKEN"})
	sum := sha256.Sum256(archive)
	rel := fakeRelease(t, "9.9.9", archive, hex.EncodeToString(sum[:])+"  "+AssetName("9.9.9")+"\n")
	self := installedProgram(t)
	Preflight = func(string, string) error { return fmt.Errorf("it does not start") }
	if err := Install(context.Background(), rel, nil); err == nil {
		t.Fatal("a program that fails its trial run was installed")
	}
	if now, _ := os.ReadFile(self); string(now) != "OLD" {
		t.Errorf("the program became %q", now)
	}
}

func TestInstallNeedsAnArchiveForThisMachine(t *testing.T) {
	rel := &Release{TagName: "v9.9.9"}
	if err := Install(context.Background(), rel, nil); err == nil || !strings.Contains(err.Error(), "SHA256SUMS") {
		t.Errorf("got %v", err)
	}
}
