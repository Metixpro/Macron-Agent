package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/cmdgate"
)

func guardedRegistry(t *testing.T) (r *Registry, home string) {
	t.Helper()
	r = newRegistry(t, true, nil)
	home = filepath.Join(t.TempDir(), ".macron")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"config.env": "TELEGRAM_BOT_TOKEN=1:secret\nMACRON_OWNERS=1\n", "secret.key": "k\n", "macron.db": "db", "MEMORY.md": "notes"} {
		if err := os.WriteFile(filepath.Join(home, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	secrets := []string{filepath.Join(home, "config.env"), filepath.Join(home, "secret.key")}
	db := filepath.Join(home, "macron.db")
	r.Machine.Protect(append(secrets, db)...)
	r.Gate = cmdgate.New(cmdgate.Env{SelfNames: []string{"macron"}, SelfDB: []string{db}, SelfHome: home, SelfSecrets: secrets,
		UserHome: filepath.Dir(home), WorkDir: r.Machine.BaseDir}, false)
	return r, home
}

func TestFileToolsCannotReachTheAgentsOwnKeys(t *testing.T) {
	r, home := guardedRegistry(t)
	conf := filepath.Join(home, "config.env")
	if out := call(t, r, "s", "read_file", map[string]any{"filename": conf}); strings.Contains(out, "secret") {
		t.Errorf("read_file showed the keys file:\n%s", out)
	}
	if out := call(t, r, "s", "write_file", map[string]any{"filename": conf, "content": "MACRON_OWNERS=999\n"}); strings.Contains(out, "saved") {
		t.Errorf("write_file wrote the keys file: %s", out)
	}
	call(t, r, "s", "write_file", map[string]any{"filename": filepath.Join(home, "secret.key"), "content": "x", "append": true})
	call(t, r, "s", ToolEditFile, map[string]any{"filename": conf, "old_text": "MACRON_OWNERS=1", "new_text": "MACRON_OWNERS=999"})
	if data, _ := os.ReadFile(conf); !strings.Contains(string(data), "MACRON_OWNERS=1\n") {
		t.Errorf("the keys file was changed:\n%s", data)
	}
	if data, _ := os.ReadFile(filepath.Join(home, "secret.key")); string(data) != "k\n" {
		t.Errorf("the secret key was changed: %q", data)
	}

	ws, _ := r.Machine.Workspace("s")
	if err := os.Symlink(conf, filepath.Join(ws, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if out := call(t, r, "s", "read_file", map[string]any{"filename": "link.txt"}); strings.Contains(out, "secret") {
		t.Errorf("read_file followed a link to the keys file:\n%s", out)
	}
	if err := os.Symlink(home, filepath.Join(ws, "home")); err != nil {
		t.Fatal(err)
	}
	if out := call(t, r, "s", "read_file", map[string]any{"filename": "home/config.env"}); strings.Contains(out, "secret") {
		t.Errorf("read_file went through a linked folder to the keys file:\n%s", out)
	}

	if out := call(t, r, "s", "read_file", map[string]any{"filename": filepath.Join(home, "MEMORY.md")}); !strings.Contains(out, "notes") {
		t.Errorf("the agent's notes stay readable: %s", out)
	}
	if out := call(t, r, "s", "write_file", map[string]any{"filename": "a.txt", "content": "hi"}); !strings.Contains(out, "saved") {
		t.Errorf("a work-folder file is written as before: %s", out)
	}
}

func TestWritingASystemFileFromAFileToolIsGated(t *testing.T) {
	r, _ := guardedRegistry(t)
	asked := ""
	ctx := cmdgate.WithAsker(context.Background(), func(_ context.Context, q cmdgate.Question) cmdgate.Choice {
		asked = q.Command
		return cmdgate.Deny
	})
	r.Gate.Policy = func() cmdgate.Policy { return cmdgate.Policy{Mode: cmdgate.ModeManual} }
	out, err := r.Execute(ctx, "s", "write_file", `{"filename": "/etc/cron.d/macron-test-never", "content": "x"}`)
	if err != nil || !strings.Contains(asked, "/etc/cron.d/macron-test-never") || !strings.Contains(out, "said no") {
		t.Errorf("write_file to /etc: asked %q, answered %q (%v)", asked, out, err)
	}
	if _, err := os.Stat("/etc/cron.d/macron-test-never"); err == nil {
		t.Error("the file was written")
	}
}

func TestRunCodeInOtherLanguagesIsGated(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 unavailable")
	}
	r, home := guardedRegistry(t)
	conf := filepath.Join(home, "config.env")
	out := call(t, r, "s", ToolRunCode, map[string]any{"language": "python", "code": "print(open(" + `"` + conf + `"` + ").read())"})
	if strings.Contains(out, "secret") || !strings.Contains(out, "Blocked") {
		t.Errorf("python read the keys file:\n%s", out)
	}
	call(t, r, "s", "write_file", map[string]any{"filename": "leak.py", "content": "print(open('" + conf + "').read())"})
	if out := call(t, r, "s", ToolRunCode, map[string]any{"filename": "leak.py"}); strings.Contains(out, "secret") || !strings.Contains(out, "Blocked") {
		t.Errorf("a saved script read the keys file:\n%s", out)
	}
	if out := call(t, r, "s", ToolRunCode, map[string]any{"language": "python", "code": "print(6*7)"}); !strings.Contains(out, "42") {
		t.Errorf("plain code runs as before:\n%s", out)
	}
	out = call(t, r, "s", ToolRunCode, map[string]any{"language": "python", "filename": conf, "code": "print(1)"})
	if data, _ := os.ReadFile(conf); !strings.Contains(string(data), "TELEGRAM_BOT_TOKEN") {
		t.Errorf("run_code overwrote the keys file (%s):\n%s", out, data)
	}
}

func TestBashCannotReachTheAgentsOwnKeys(t *testing.T) {
	r, home := guardedRegistry(t)
	for _, cmd := range []string{
		"cat " + filepath.Join(home, "config.env"),
		"echo MACRON_YOLO=1 >> " + filepath.Join(home, "config.env"),
		"macron config set MACRON_OWNERS 999999",
		"macron uninstall --yes",
	} {
		if out := call(t, r, "s", "bash_execute", map[string]any{"command": cmd}); strings.Contains(out, "secret") || !strings.Contains(out, "Blocked") {
			t.Errorf("%q was not blocked:\n%s", cmd, out)
		}
	}
	if data, _ := os.ReadFile(filepath.Join(home, "config.env")); strings.Contains(string(data), "YOLO") {
		t.Errorf("the keys file was changed:\n%s", data)
	}
}
