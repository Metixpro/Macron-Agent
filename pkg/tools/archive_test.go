package tools

import (
	"bytes"
	"crypto/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestArchivesBeyondZip(t *testing.T) {
	if _, err := exec.LookPath("7z"); err != nil {
		t.Skip("7-Zip is not installed")
	}
	r := bigRegistryUnsafe(t)
	ws, _ := r.Machine.Workspace("ar")
	_ = r.Machine.WriteFile("ar", "notes.txt", []byte("hello archive"))
	_ = r.Machine.WriteFile("ar", "proj/main.go", []byte("package main"))
	_ = r.Machine.WriteFile("ar", "proj/node_modules/x/index.js", []byte("junk"))

	// A 7z with a password: the names are hidden too.
	out := call(t, r, "ar", ToolPackFiles, map[string]any{"paths": []string{"notes.txt", "proj"}, "format": "7z", "password": "s3cret", "filename": "secret.7z"})
	if !strings.Contains(out, "Created secret.7z: 2 files") || !strings.Contains(out, "AES-256") || !strings.Contains(out, "1 dependency") {
		t.Fatalf("7z: %s", out)
	}
	if out := call(t, r, "ar", ToolExtract, map[string]any{"filename": "secret.7z", "list": true}); !strings.Contains(out, "password protected") {
		t.Errorf("hidden names: %s", out)
	}
	if out := call(t, r, "ar", ToolExtract, map[string]any{"filename": "secret.7z", "list": true, "password": "s3cret"}); !strings.Contains(out, "notes.txt") {
		t.Errorf("list with the password: %s", out)
	}
	if out := call(t, r, "ar", ToolExtract, map[string]any{"filename": "secret.7z", "dest": "x"}); !strings.Contains(out, "password protected; ask for its password") {
		t.Errorf("no password: %s", out)
	}
	if out := call(t, r, "ar", ToolExtract, map[string]any{"filename": "secret.7z", "dest": "x", "password": "nope"}); !strings.Contains(out, "the password is wrong") {
		t.Errorf("wrong password: %s", out)
	}
	if out := call(t, r, "ar", ToolExtract, map[string]any{"filename": "secret.7z", "dest": "x", "password": "s3cret"}); !strings.Contains(out, "Unpacked 2 files") {
		t.Fatalf("unpack: %s", out)
	}
	if data, _, _ := r.Machine.ReadFile("ar", "x/proj/main.go", 100); string(data) != "package main" {
		t.Error("the files came out")
	}

	// A zip with a password (ZipCrypto from zip -P) goes to 7-Zip.
	if _, err := exec.LookPath("zip"); err == nil {
		_ = exec.Command("bash", "-c", "cd \"$1\" && zip -q -P pw locked.zip notes.txt", "z", ws).Run()
		if out := call(t, r, "ar", ToolExtract, map[string]any{"filename": "locked.zip"}); !strings.Contains(out, "password protected") {
			t.Errorf("an encrypted zip: %s", out)
		}
		if out := call(t, r, "ar", ToolExtract, map[string]any{"filename": "locked.zip", "dest": "l", "password": "pw"}); !strings.Contains(out, "Unpacked 1 files") {
			t.Errorf("with its password: %s", out)
		}
	}

	// A tar.xz with a link that points out of the workspace; a lone .gz.
	mk := `import tarfile, io, gzip, sys
d = sys.argv[1]
with tarfile.open(d + "/pkg.tar.xz", "w:xz") as t:
    data = b"inside"
    i = tarfile.TarInfo("pkg/readme.txt"); i.size = len(data); t.addfile(i, io.BytesIO(data))
    l = tarfile.TarInfo("pkg/evil"); l.type = tarfile.SYMTYPE; l.linkname = "/etc/passwd"; t.addfile(l)
with gzip.open(d + "/log.txt.gz", "wb") as f: f.write(b"line one")`
	if out, err := exec.Command("python3", "-c", mk, ws).CombinedOutput(); err != nil {
		t.Fatalf("%s %v", out, err)
	}
	out = call(t, r, "ar", ToolExtract, map[string]any{"filename": "pkg.tar.xz"})
	if !strings.Contains(out, "Unpacked 1 files") || !strings.Contains(out, " 1 links") {
		t.Errorf("tar.xz: %s", out)
	}
	if _, err := os.Lstat(filepath.Join(ws, "pkg", "evil")); err == nil {
		t.Error("the link is gone")
	}
	if data, _, _ := r.Machine.ReadFile("ar", "pkg/readme.txt", 100); string(data) != "inside" {
		t.Errorf("the single top folder is the destination: %q", data)
	}
	call(t, r, "ar", ToolExtract, map[string]any{"filename": "log.txt.gz", "dest": "."})
	if data, _, _ := r.Machine.ReadFile("ar", "log.txt", 100); string(data) != "line one" {
		t.Errorf("a lone .gz: %q", data)
	}

	// Several paths into one zip and a tar.gz, here, without 7-Zip.
	if out := call(t, r, "ar", ToolPackFiles, map[string]any{"paths": []string{"notes.txt", "proj"}}); !strings.Contains(out, "Created files.zip: 2 files") {
		t.Errorf("zip of two paths: %s", out)
	}
	if out := call(t, r, "ar", ToolExtract, map[string]any{"filename": "files.zip", "list": true}); !strings.Contains(out, "proj/main.go") || !strings.Contains(out, "notes.txt") {
		t.Errorf("listing: %s", out)
	}
	if out := call(t, r, "ar", ToolPackFiles, map[string]any{"path": "proj", "format": "tar.gz"}); !strings.Contains(out, "Created proj.tar.gz: 1 files") {
		t.Errorf("tar.gz: %s", out)
	}
	if out := call(t, r, "ar", ToolPackFiles, map[string]any{"paths": []string{"proj"}, "format": "tar.gz", "password": "x"}); !strings.Contains(out, "cannot carry a password") {
		t.Errorf("tar.gz + password: %s", out)
	}

	// Too big for one message: parts that 7-Zip opens from the first.
	big := make([]byte, 3<<20)
	_, _ = rand.Read(big)
	_ = r.Machine.WriteFile("ar", "big/blob.bin", big)
	out = call(t, r, "ar", ToolPackFiles, map[string]any{"paths": []string{"big"}, "split_mb": 1})
	if !strings.Contains(out, "Cut into 4 parts") || !strings.Contains(out, "big.zip.001") {
		t.Fatalf("split: %s", out)
	}
	if _, err := r.Machine.Stat("ar", "big.zip"); err == nil {
		t.Error("the whole goes once it is cut")
	}
	if out := call(t, r, "ar", ToolExtract, map[string]any{"filename": "big.zip.001", "dest": "joined"}); !strings.Contains(out, "Unpacked 1 files") {
		t.Fatalf("from the first part: %s", out)
	}
	if data, _, _ := r.Machine.ReadFile("ar", "joined/blob.bin", 4<<20); !bytes.Equal(data, big) {
		t.Error("the parts give back the same file")
	}
}
