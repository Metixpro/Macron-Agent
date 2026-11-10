package tools

import (
	"context"
	"encoding/json"
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
	r.Gate = cmdgate.New(cmdgate.Env{SelfNames: []string{"macron"}, SelfDB: []string{filepath.Join(home, "macron.db")}, SelfHome: home,
		SelfSecrets: secrets, UserHome: filepath.Dir(home), WorkDir: r.Machine.BaseDir}, false)
	return r, home
}

// owner answers every question of the gate the same way and counts them.
func owner(answer cmdgate.Choice, asked *int) context.Context {
	return cmdgate.WithAsker(context.Background(), func(context.Context, cmdgate.Question) cmdgate.Choice {
		*asked++
		return answer
	})
}

func run(t *testing.T, r *Registry, ctx context.Context, tool string, args map[string]any) string {
	t.Helper()
	b, _ := json.Marshal(args)
	out, err := r.Execute(ctx, "s", tool, string(b))
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	return out
}

func TestTheOwnerCanHaveTheAgentReadAndChangeItsOwnKeys(t *testing.T) {
	r, home := guardedRegistry(t)
	conf := filepath.Join(home, "config.env")
	asked := 0
	yes := owner(cmdgate.Once, &asked)
	if out := run(t, r, yes, "read_file", map[string]any{"filename": conf}); !strings.Contains(out, "MACRON_OWNERS=1") {
		t.Errorf("read_file after a yes: %s", out)
	}
	if out := run(t, r, yes, "bash_execute", map[string]any{"command": "cat " + conf}); !strings.Contains(out, "MACRON_OWNERS=1") {
		t.Errorf("cat after a yes: %s", out)
	}
	if out := run(t, r, yes, ToolEditFile, map[string]any{"filename": conf, "old_text": "MACRON_OWNERS=1", "new_text": "MACRON_OWNERS=2"}); !strings.Contains(out, "Edited") {
		t.Errorf("edit_file after a yes: %s", out)
	}
	if out := run(t, r, yes, "write_file", map[string]any{"filename": conf, "content": "MACRON_OWNERS=3\n"}); !strings.Contains(out, "saved") {
		t.Errorf("write_file after a yes: %s", out)
	}
	if data, _ := os.ReadFile(conf); string(data) != "MACRON_OWNERS=3\n" {
		t.Errorf("the file holds %q", data)
	}
	if asked != 4 {
		t.Errorf("the owner was asked %d times, once for each", asked)
	}
}

func TestNothingTouchesTheAgentsKeysWithoutTheOwnersYes(t *testing.T) {
	r, home := guardedRegistry(t)
	conf := filepath.Join(home, "config.env")
	ws, _ := r.Machine.Workspace("s")
	if err := os.Symlink(conf, filepath.Join(ws, "link.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(home, filepath.Join(ws, "home")); err != nil {
		t.Fatal(err)
	}
	asked := 0
	for name, ctx := range map[string]context.Context{
		"the owner says no":        owner(cmdgate.Deny, &asked),
		"a job nobody is watching": cmdgate.WithOrigin(context.Background(), cmdgate.OriginCron),
	} {
		for _, try := range []struct {
			tool string
			args map[string]any
		}{
			{"read_file", map[string]any{"filename": conf}},
			{"read_file", map[string]any{"filename": "link.txt"}},
			{"read_file", map[string]any{"filename": "home/config.env"}},
			{"write_file", map[string]any{"filename": conf, "content": "MACRON_OWNERS=999\n"}},
			{"write_file", map[string]any{"filename": filepath.Join(home, "secret.key"), "content": "x", "append": true}},
			{ToolEditFile, map[string]any{"filename": conf, "old_text": "MACRON_OWNERS=1", "new_text": "MACRON_OWNERS=999"}},
			{"bash_execute", map[string]any{"command": "cat " + conf}},
			{"bash_execute", map[string]any{"command": "echo MACRON_YOLO=1 >> " + conf}},
			{"bash_execute", map[string]any{"command": "macron config set MACRON_OWNERS 999999"}},
			{"bash_execute", map[string]any{"command": "macron uninstall --yes"}},
		} {
			if out := run(t, r, ctx, try.tool, try.args); strings.Contains(out, "secret") || !strings.Contains(out, "[!]") {
				t.Errorf("%s: %s %v went through:\n%s", name, try.tool, try.args, out)
			}
		}
	}
	if data, _ := os.ReadFile(conf); string(data) != "TELEGRAM_BOT_TOKEN=1:secret\nMACRON_OWNERS=1\n" {
		t.Errorf("the keys file was changed:\n%s", data)
	}
	if data, _ := os.ReadFile(filepath.Join(home, "secret.key")); string(data) != "k\n" {
		t.Errorf("the secret key was changed: %q", data)
	}
	if asked != 10 {
		t.Errorf("the owner was asked %d times", asked)
	}
}

func TestTheRestOfTheMachineNeedsNoQuestion(t *testing.T) {
	r, home := guardedRegistry(t)
	asked := 0
	ctx := owner(cmdgate.Deny, &asked)
	if out := run(t, r, ctx, "read_file", map[string]any{"filename": filepath.Join(home, "MEMORY.md")}); !strings.Contains(out, "notes") {
		t.Errorf("the agent's notes: %s", out)
	}
	if out := run(t, r, ctx, "write_file", map[string]any{"filename": "a.txt", "content": "hi"}); !strings.Contains(out, "saved") {
		t.Errorf("a work-folder file: %s", out)
	}
	other := filepath.Join(t.TempDir(), "app", "settings.conf")
	if out := run(t, r, ctx, "write_file", map[string]any{"filename": other, "content": "x=1\n"}); !strings.Contains(out, "saved") {
		t.Errorf("a file elsewhere on the server: %s", out)
	}
	if out := run(t, r, ctx, "read_file", map[string]any{"filename": other}); !strings.Contains(out, "x=1") {
		t.Errorf("reading it back: %s", out)
	}
	if asked != 0 {
		t.Errorf("the owner was asked %d times about ordinary files", asked)
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
	asked := 0
	no := owner(cmdgate.Deny, &asked)
	code := "print(open(" + `"` + conf + `"` + ").read())"
	if out := run(t, r, no, ToolRunCode, map[string]any{"language": "python", "code": code}); strings.Contains(out, "secret") || !strings.Contains(out, "said no") {
		t.Errorf("python read the keys file after a no:\n%s", out)
	}
	run(t, r, no, "write_file", map[string]any{"filename": "leak.py", "content": code})
	if out := run(t, r, no, ToolRunCode, map[string]any{"filename": "leak.py"}); strings.Contains(out, "secret") || !strings.Contains(out, "said no") {
		t.Errorf("a saved script read the keys file after a no:\n%s", out)
	}
	if asked != 2 {
		t.Errorf("the owner was asked %d times", asked)
	}
	if out := run(t, r, owner(cmdgate.Once, &asked), ToolRunCode, map[string]any{"language": "python", "code": code}); !strings.Contains(out, "MACRON_OWNERS=1") {
		t.Errorf("python after a yes:\n%s", out)
	}
	if out := run(t, r, no, ToolRunCode, map[string]any{"language": "python", "code": "print(6*7)"}); !strings.Contains(out, "42") || asked != 3 {
		t.Errorf("plain code runs without a question (asked %d):\n%s", asked, out)
	}
}
