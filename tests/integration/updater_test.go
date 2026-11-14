package integration

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/app"
	"github.com/Metixpro/macron-agent/pkg/updater"
)

var binaryDir, binaryPath string
var binaryErr error
var binaryOnce sync.Once

func testBinary(t *testing.T) string {
	t.Helper()
	binaryOnce.Do(func() {
		if p := os.Getenv("MACRON_TEST_BINARY"); p != "" {
			binaryPath, binaryErr = filepath.Abs(p)
			return
		}
		binaryDir, binaryErr = os.MkdirTemp("", "macron-test-bin-")
		if binaryErr != nil {
			return
		}
		binaryPath = filepath.Join(binaryDir, "macron")
		if runtime.GOOS == "windows" {
			binaryPath += ".exe"
		}
		_, file, _, _ := runtime.Caller(0)
		root := filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
		cmd := exec.Command("go", "build", "-buildvcs=false", "-o", binaryPath, "./cmd/macron")
		cmd.Dir = root
		out, err := cmd.CombinedOutput()
		if err != nil {
			binaryErr = fmt.Errorf("build fixture: %w: %s", err, out)
		}
	})
	if binaryErr != nil {
		t.Fatal(binaryErr)
	}
	return binaryPath
}

func TestUpdater503CompatibilityAndBackup(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(503)
		fmt.Fprint(w, "<html>temporarily unavailable</html>")
	}))
	defer server.Close()
	home := t.TempDir()
	t.Setenv("MACRON_HOME", home)
	t.Setenv("MACRON_ENV_FILE", filepath.Join(home, "config.env"))
	if err := os.WriteFile(filepath.Join(home, "config.env"), []byte("MACRON_TIMEZONE=UTC\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MACRON_MESSENGER", "bale")
	t.Setenv("BALE_BOT_TOKEN", "fixture-token")
	t.Setenv("MACRON_BALE_OWNERS", "42")
	t.Setenv("MACRON_BALE_API", server.URL)
	if err := updater.Preflight(testBinary(t), app.Version); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("local check contacted the failing messenger API")
	}
	next, err := os.ReadFile(testBinary(t))
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), filepath.Base(testBinary(t)))
	old := []byte("old program sentinel")
	if err := os.WriteFile(target, old, 0755); err != nil {
		t.Fatal(err)
	}
	if err := updater.Swap(target, next, app.Version); err != nil {
		t.Fatal(err)
	}
	backup, err := os.ReadFile(target + ".old")
	if err != nil || !bytes.Equal(backup, old) {
		t.Fatal("old program not preserved", err)
	}
	out, err := exec.Command(target, "version").CombinedOutput()
	if err != nil || !strings.Contains(string(out), app.Version) {
		t.Fatal(string(out), err)
	}
	if calls.Load() != 0 {
		t.Fatal("swap contacted the failing API")
	}
}
func TestUpdaterRejectsBrokenLocalDatabase(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MACRON_HOME", home)
	t.Setenv("MACRON_ENV_FILE", filepath.Join(home, "config.env"))
	if err := os.WriteFile(filepath.Join(home, "config.env"), []byte("MACRON_TIMEZONE=UTC\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MACRON_MESSENGER", "")
	t.Setenv("TELEGRAM_BOT_TOKEN", "")
	t.Setenv("MACRON_OWNERS", "")
	broken := filepath.Join(home, "bad.db")
	t.Setenv("MACRON_DB", broken)
	if err := os.WriteFile(broken, []byte("not sqlite"), 0600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), filepath.Base(testBinary(t)))
	old := []byte("old program sentinel")
	if err := os.WriteFile(target, old, 0755); err != nil {
		t.Fatal(err)
	}
	next, err := os.ReadFile(testBinary(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := updater.Swap(target, next, app.Version); err == nil || !strings.Contains(err.Error(), "database") {
		t.Fatal("broken DB passed", err)
	}
	same, err := os.ReadFile(target)
	if err != nil || !bytes.Equal(same, old) {
		t.Fatal("failed check changed the installed program", err)
	}
	if _, err := os.Stat(target + ".old"); !os.IsNotExist(err) {
		t.Fatal("backup changed before validation")
	}
}
