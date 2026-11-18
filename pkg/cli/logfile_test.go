package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/config"
)

func TestLogTail(t *testing.T) {
	for _, tc := range []struct {
		name, text, want string
		n                int
	}{
		{"empty", "", "", 10}, {"none", "a\nb\n", "", 0},
		{"terminated", "a\nb\nc\n", "b\nc\n", 2}, {"unterminated", "a\nb\nc", "b\nc", 2},
		{"CRLF-Persian", "old\r\nسلام\r\nدنیا\r\n", "سلام\r\nدنیا\r\n", 2},
		{"large", strings.Repeat("old line\n", 20000) + "last\n", "last\n", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "log")
			if err := os.WriteFile(path, []byte(tc.text), 0600); err != nil {
				t.Fatal(err)
			}
			f, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			got, err := readLogTail(f, tc.n)
			if err != nil || string(got) != tc.want {
				t.Fatalf("got %q, %v; want %q", got, err, tc.want)
			}
			pos, _ := f.Seek(0, 1)
			if pos != int64(len(tc.text)) {
				t.Fatalf("follow starts at %d", pos)
			}
		})
	}
}

func TestFileLogsWithoutExternalPrograms(t *testing.T) {
	t.Setenv(config.EnvHome, t.TempDir())
	t.Setenv("PATH", "")
	if err := os.WriteFile(config.HomePath(logFile), []byte("one\nسلام\nthree\n"), 0600); err != nil {
		t.Fatal(err)
	}
	out, buf := testIO()
	if code := fileLogs(out, []string{"-n", "2"}); code != 0 || buf.String() != "سلام\nthree\n" {
		t.Fatalf("%d %s", code, buf)
	}
	for _, args := range [][]string{{"-n"}, {"-n", "-1"}, {"-n", "bad"}, {"--unknown"}} {
		if code := fileLogs(out, args); code != 2 {
			t.Fatalf("%v: %d", args, code)
		}
	}
}
