package updater

import (
	"archive/zip"
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

const testUpdateVersion = "9.9.9"

func TestWindowsInstallPairedAndStandalone(t *testing.T) {
	for _, tc := range []struct {
		name, entry string
		installed   []string
	}{
		{"paired CLI", windowsCLI, []string{windowsCLI, windowsDesktop}},
		{"paired desktop", windowsDesktop, []string{windowsCLI, windowsDesktop}},
		{"standalone CLI", windowsCLI, []string{windowsCLI}},
		{"standalone desktop", windowsDesktop, []string{windowsDesktop}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			for _, name := range tc.installed {
				path := filepath.Join(dir, name)
				writeUpdateFixture(t, path, "old "+name)
				writeUpdateFixture(t, path+".old", "earlier "+name)
			}
			var preflightErr error
			if tc.entry == windowsDesktop {
				preflightErr = errors.New("desktop updates must not run the service startup check")
			}
			setUpdateFixture(t, filepath.Join(dir, tc.entry), preflightErr)
			archiveFiles := []string{windowsCLI}
			if slices.Contains(tc.installed, windowsDesktop) {
				archiveFiles = append(archiveFiles, windowsDesktop)
			}
			rel := releaseFixture(t, archiveFiles...)

			var last Progress
			if err := InstallWithProgress(t.Context(), rel, func(p Progress) { last = p }); err != nil {
				t.Fatal(err)
			}
			if last.Phase != "done" || last.Percent != 100 {
				t.Fatalf("unexpected final progress: %+v", last)
			}
			for _, name := range tc.installed {
				path := filepath.Join(dir, name)
				checkUpdateFixture(t, path, "new "+name)
				checkUpdateFixture(t, path+".old", "old "+name)
			}
			for _, name := range []string{windowsCLI, windowsDesktop} {
				if !slices.Contains(tc.installed, name) {
					if _, err := os.Stat(filepath.Join(dir, name)); !errors.Is(err, os.ErrNotExist) {
						t.Fatalf("unexpected executable %s: %v", name, err)
					}
				}
			}
		})
	}
}

func TestWindowsInstallFailuresPreserveExistingPair(t *testing.T) {
	for _, tc := range []struct {
		name, wantError string
	}{
		{"missing companion in package", "no macron-desktop.exe in the archive"},
		{"failed preflight", "deliberate preflight failure"},
		{"canceled before replacement", "context canceled"},
		{"blocked companion backup", "back up"},
		{"update already installing", "another update"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			cli := filepath.Join(dir, windowsCLI)
			desktop := filepath.Join(dir, windowsDesktop)
			writeUpdateFixture(t, cli, "old CLI")
			writeUpdateFixture(t, desktop, "old desktop")
			archiveFiles := []string{windowsCLI, windowsDesktop}
			var preflightErr error
			switch tc.name {
			case "missing companion in package":
				archiveFiles = archiveFiles[:1]
			case "failed preflight":
				preflightErr = errors.New(tc.wantError)
			case "blocked companion backup":
				if err := os.Mkdir(desktop+".old", 0o755); err != nil {
					t.Fatal(err)
				}
			case "update already installing":
				lock, err := lockUpdate(dir)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { windows.CloseHandle(lock) })
			}
			setUpdateFixture(t, cli, preflightErr)

			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var last Progress
			err := InstallWithProgress(ctx, releaseFixture(t, archiveFiles...), func(p Progress) {
				last = p
				if tc.name == "canceled before replacement" && p.Phase == "installing" {
					cancel()
				}
			})
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("wanted %q, got %v", tc.wantError, err)
			}
			if last.Phase == "done" {
				t.Fatal("failed install reported completion")
			}
			checkUpdateFixture(t, cli, "old CLI")
			checkUpdateFixture(t, desktop, "old desktop")
		})
	}
}

func TestWindowsInstallRollsBackAfterFirstExecutableIsReplaced(t *testing.T) {
	dir := t.TempDir()
	cli := filepath.Join(dir, windowsCLI)
	desktop := filepath.Join(dir, windowsDesktop)
	writeUpdateFixture(t, cli, "old CLI")
	writeUpdateFixture(t, desktop, "old desktop")
	writeUpdateFixture(t, cli+".new", "new CLI")

	err := replaceBinaries([]binaryUpdate{
		{target: cli, next: cli + ".new"},
		{target: desktop, next: desktop + ".new"},
	})
	if err == nil || !strings.Contains(err.Error(), "install "+desktop) {
		t.Fatalf("wanted desktop installation failure, got %v", err)
	}
	checkUpdateFixture(t, cli, "old CLI")
	checkUpdateFixture(t, desktop, "old desktop")
}

func TestWindowsInstallReplacesRunningPair(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	program, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	for _, name := range []string{windowsCLI, windowsDesktop} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, program, 0o755); err != nil {
			t.Fatal(err)
		}
		startUpdateProcessFixture(t, path)
	}

	setUpdateFixture(t, filepath.Join(dir, windowsDesktop), nil)
	if err := Install(t.Context(), releaseFixture(t, windowsCLI, windowsDesktop), nil); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{windowsCLI, windowsDesktop} {
		path := filepath.Join(dir, name)
		checkUpdateFixture(t, path, "new "+name)
		if backup, err := os.Stat(path + ".old"); err != nil || backup.Size() != int64(len(program)) {
			t.Fatalf("missing original executable backup for %s: %v", name, err)
		}
	}
}

func startUpdateProcessFixture(t *testing.T, path string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, path, "-test.run=^TestWindowsUpdateProcessFixture$")
	cmd.SysProcAttr = detached()
	cmd.Env = append(os.Environ(), "MACRON_TEST_UPDATE_CHILD=1")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		if _, err := io.Copy(io.Discard, stdout); err != nil {
			t.Errorf("read fixture process: %v", err)
		}
		if err := cmd.Wait(); err != nil {
			t.Errorf("wait for fixture process: %v", err)
		}
	})
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || scanner.Text() != "ready" {
		t.Fatalf("fixture process did not start: %v", scanner.Err())
	}
}

func TestWindowsUpdateProcessFixture(t *testing.T) {
	if os.Getenv("MACRON_TEST_UPDATE_CHILD") != "1" {
		return
	}
	fmt.Println("ready")
	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		t.Fatal(err)
	}
}

func setUpdateFixture(t *testing.T, self string, preflightErr error) {
	t.Helper()
	origSelf, origPreflight, origVerifyVersion := SelfPath, Preflight, verifyVersion
	SelfPath = func() (string, error) { return self, nil }
	verifyVersion = func(path, version string) error {
		if version != testUpdateVersion {
			t.Fatalf("unexpected preflight version: %s", version)
		}
		checkUpdateFixture(t, path, "new "+filepath.Base(path))
		return nil
	}
	Preflight = func(path, version string) error {
		if err := verifyVersion(path, version); err != nil {
			return err
		}
		return preflightErr
	}
	t.Cleanup(func() {
		SelfPath, Preflight, verifyVersion = origSelf, origPreflight, origVerifyVersion
	})
}

func releaseFixture(t *testing.T, names ...string) *Release {
	t.Helper()
	var buf bytes.Buffer
	zipWriter := zip.NewWriter(&buf)
	for _, name := range names {
		file, err := zipWriter.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := fmt.Fprint(file, "new "+name); err != nil {
			t.Fatal(err)
		}
	}
	if err := zipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(buf.Bytes())
	asset := AssetName(testUpdateVersion)
	mux := http.NewServeMux()
	mux.HandleFunc("/archive", func(w http.ResponseWriter, _ *http.Request) {
		w.Write(buf.Bytes())
	})
	mux.HandleFunc("/sums", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "%x  %s\n", sum, asset)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return &Release{
		TagName: "v" + testUpdateVersion,
		Assets: []struct {
			Name   string `json:"name"`
			URL    string `json:"browser_download_url"`
			APIURL string `json:"url"`
			Size   int64  `json:"size"`
		}{
			{Name: asset, URL: srv.URL + "/archive", Size: int64(buf.Len())},
			{Name: "SHA256SUMS", URL: srv.URL + "/sums"},
		},
	}
}

func writeUpdateFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
}

func checkUpdateFixture(t *testing.T, path, want string) {
	t.Helper()
	if data, err := os.ReadFile(path); err != nil || string(data) != want {
		t.Fatalf("%s = %q, %v; want %q", path, data, err, want)
	}
}
