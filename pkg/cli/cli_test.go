package cli

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/app"
)

// testIO is a terminal nobody sits at.
func testIO(input string) (*IO, *bytes.Buffer) {
	var out bytes.Buffer
	return &IO{In: bufio.NewReader(strings.NewReader(input)), Out: &out, Err: &out,
		Secret: func(string) (string, error) { return "", fmt.Errorf("no terminal") }}, &out
}

func fakeServices(t *testing.T) (telegram, llm *httptest.Server) {
	t.Helper()
	telegram = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/bot123:good/getMe"):
			w.Write([]byte(`{"ok":true,"result":{"id":777,"is_bot":true,"first_name":"Macron","username":"macron_test_bot"}}`))
		default:
			w.WriteHeader(http.StatusUnauthorized)
			w.Write([]byte(`{"ok":false,"error_code":401,"description":"Unauthorized"}`))
		}
	}))
	llm = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-test" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`))
	}))
	t.Cleanup(telegram.Close)
	t.Cleanup(llm.Close)
	return
}

func TestSetupWritesPrivateSettingsAndTestsTheModel(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MACRON_HOME", home)
	t.Setenv("MACRON_ENV_FILE", "")
	tg, llm := fakeServices(t)
	old := telegramAPI
	telegramAPI = tg.URL
	defer func() { telegramAPI = old }()

	io, out := testIO("")
	args := []string{"setup", "--yes", "--no-service", "--token", "123:good", "--owner", "42,43", "--api-url", llm.URL + "/v1",
		"--api-key", "sk-test", "--model", "my-model", "--timezone", "Asia/Tehran"}
	if handled, code := Run(args, io); !handled || code != 0 {
		t.Fatalf("setup: handled=%v code=%d\n%s", handled, code, out)
	}
	path := filepath.Join(home, "config.env")
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("config.env: %v %v", err, fi)
	}
	env, _ := loadEnvFile(path)
	for k, want := range map[string]string{keyToken: "123:good", keyOwners: "42,43", keyAPIURL: llm.URL + "/v1", keyAPIKey: "sk-test",
		keyModel: "my-model", keyTimezone: "Asia/Tehran"} {
		if got, _ := env.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
	if !strings.Contains(out.String(), "@macron_test_bot") || !strings.Contains(out.String(), "my-model answers") {
		t.Errorf("the wizard says what it checked:\n%s", out)
	}

	// Run again: what is saved is kept, nothing needs to be typed.
	io, out = testIO("")
	if _, code := Run([]string{"setup", "--yes", "--no-service"}, io); code != 0 {
		t.Fatalf("second setup: %d\n%s", code, out)
	}
	env, _ = loadEnvFile(path)
	if got, _ := env.Get(keyAPIKey); got != "sk-test" {
		t.Errorf("the key is kept: %q", got)
	}
}

func TestSetupRefusesWhatDoesNotWork(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MACRON_HOME", home)
	t.Setenv("MACRON_ENV_FILE", "")
	tg, llm := fakeServices(t)
	old := telegramAPI
	telegramAPI = tg.URL
	defer func() { telegramAPI = old }()

	base := []string{"setup", "--yes", "--no-service", "--owner", "42", "--api-url", llm.URL + "/v1", "--api-key", "sk-test", "--model", "m"}
	cases := map[string][]string{
		"a token Telegram refuses": append(base[:len(base):len(base)], "--token", "123:bad"),
		"no token":                 base,
		"a bad owner id":           {"setup", "--yes", "--no-service", "--token", "123:good", "--owner", "me", "--api-url", llm.URL + "/v1", "--api-key", "sk-test", "--model", "m"},
		"a wrong model key":        {"setup", "--yes", "--no-service", "--token", "123:good", "--owner", "42", "--api-url", llm.URL + "/v1", "--api-key", "wrong", "--model", "m"},
		"an unknown time zone":     append(base[:len(base):len(base)], "--token", "123:good", "--timezone", "Mars/Olympus"),
	}
	for name, args := range cases {
		io, out := testIO("")
		if _, code := Run(args, io); code == 0 {
			t.Errorf("%s: setup succeeded\n%s", name, out)
		}
		if _, err := os.Stat(filepath.Join(home, "config.env")); err == nil {
			t.Errorf("%s: settings were saved", name)
		}
	}
}

func TestEnvFileKeepsTheOwnersLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.env")
	os.WriteFile(path, []byte("# my notes\nTELEGRAM_BOT_TOKEN=abc\n\nexport MACRON_MODEL=\"a b\"\nMACRON_X=1 # x\n"), 0o600)
	env, err := loadEnvFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if v, _ := env.Get("MACRON_MODEL"); v != "a b" {
		t.Errorf("quoted value %q", v)
	}
	env.Set("MACRON_MODEL", "plain")
	env.Set("MACRON_NEW", "has space")
	env.Unset("MACRON_X")
	if err := env.Save(); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(path)
	want := "# my notes\nTELEGRAM_BOT_TOKEN=abc\n\nMACRON_MODEL=plain\nMACRON_NEW=\"has space\"\n"
	if string(got) != want {
		t.Errorf("file:\n%s\nwant:\n%s", got, want)
	}
	if secretKey("MACRON_API_KEY") != true || secretKey("MACRON_MODEL") || mask("sk-abcdefghij") != "sk-••••••hij" {
		t.Errorf("secrets are masked: %v %v %q", secretKey("MACRON_API_KEY"), secretKey("MACRON_MODEL"), mask("sk-abcdefghij"))
	}
}

func TestConfigCommandMasksSecrets(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MACRON_HOME", home)
	t.Setenv("MACRON_ENV_FILE", "")
	os.WriteFile(filepath.Join(home, "config.env"), []byte("TELEGRAM_BOT_TOKEN=123456:SECRETVALUE\nMACRON_MODEL=m\n"), 0o600)
	io, out := testIO("")
	Run([]string{"config", "list"}, io)
	if strings.Contains(out.String(), "SECRETVALUE") || !strings.Contains(out.String(), "MACRON_MODEL=m") {
		t.Errorf("list:\n%s", out)
	}
	io, out = testIO("")
	if _, code := Run([]string{"config", "set", "MACRON_OWNERS", "abc"}, io); code == 0 {
		t.Error("a bad owner id is refused")
	}
	io, out = testIO("")
	if _, code := Run([]string{"config", "set", "MACRON_OWNERS", "5"}, io); code != 0 {
		t.Errorf("set: %d %s", code, out)
	}
	io, out = testIO("")
	Run([]string{"config", "get", "MACRON_OWNERS"}, io)
	if strings.TrimSpace(out.String()) != "5" {
		t.Errorf("get: %q", out)
	}
}

func TestServiceUnits(t *testing.T) {
	sys := renderSystemd(unit{Binary: "/usr/local/bin/macron", Home: "/home/me/.macron", User: "me", System: true})
	for _, want := range []string{"User=me", "Environment=MACRON_HOME=/home/me/.macron\n", "WorkingDirectory=/home/me/.macron\n", "ExecStart=/usr/local/bin/macron\n", "Restart=always", "WantedBy=multi-user.target"} {
		if !strings.Contains(sys, want) {
			t.Errorf("system unit lacks %q:\n%s", want, sys)
		}
	}
	for _, banned := range []string{"ProtectSystem", "NoNewPrivileges", "PrivateTmp", "ReadOnlyPaths"} {
		if strings.Contains(sys, banned) {
			t.Errorf("the personal agent has full access; the unit must not restrict it (%s)", banned)
		}
	}
	usr := renderSystemd(unit{Binary: "/x/macron", Home: "/h", User: "me"})
	if strings.Contains(usr, "User=") || !strings.Contains(usr, "WantedBy=default.target") {
		t.Errorf("user unit:\n%s", usr)
	}
	pl := renderLaunchd(unit{Binary: "/x/macron", Home: "/h"})
	if !strings.Contains(pl, "<key>KeepAlive</key><true/>") || !strings.Contains(pl, "MACRON_HOME") {
		t.Errorf("plist:\n%s", pl)
	}
}

func TestVersionsAndChecksums(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{{"1.0.1", "1.0.0", true}, {"v1.10.0", "1.9.9", true}, {"1.0.0", "1.0.0", false}, {"1.0", "1.0.0", false}, {"2.0.0", "10.0.0", false}} {
		if got := newer(c.a, c.b); got != c.want {
			t.Errorf("newer(%q, %q) = %v", c.a, c.b, got)
		}
	}
	sum := strings.Repeat("ab", 32)
	if got, ok := checksumOf("garbage\n"+sum+"  macron-1.0.0-linux-amd64.tar.gz\n", "macron-1.0.0-linux-amd64.tar.gz"); !ok || got != sum {
		t.Errorf("checksum %q %v", got, ok)
	}
	if _, ok := checksumOf(sum+"  other.tar.gz\n", "macron.tar.gz"); ok {
		t.Error("only the named file")
	}
}

// release builds a fake GitHub release with a program that says its version.
func fakeRelease(t *testing.T, version string, tamper bool) *httptest.Server {
	t.Helper()
	script := "#!/bin/sh\n[ \"$1\" = version ] && echo 'macron " + version + "'\n"
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	tw.WriteHeader(&tar.Header{Name: "macron-" + version + "/macron", Mode: 0o755, Size: int64(len(script)), Typeflag: tar.TypeReg})
	tw.Write([]byte(script))
	tw.Close()
	gz.Close()
	archive := buf.Bytes()
	name := assetName(version)
	sum := sha256.Sum256(archive)
	sums := hex.EncodeToString(sum[:]) + "  " + name + "\n"
	if tamper {
		sums = strings.Repeat("0", 64) + "  " + name + "\n"
	}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/releases/latest"):
			json.NewEncoder(w).Encode(map[string]any{"tag_name": "v" + version, "html_url": srv.URL + "/rel", "assets": []map[string]any{
				{"name": name, "browser_download_url": srv.URL + "/dl/" + name, "size": len(archive)},
				{"name": "SHA256SUMS", "browser_download_url": srv.URL + "/dl/SHA256SUMS", "size": len(sums)},
			}})
		case strings.HasSuffix(r.URL.Path, "/dl/"+name):
			w.Write(archive)
		case strings.HasSuffix(r.URL.Path, "/dl/SHA256SUMS"):
			w.Write([]byte(sums))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestUpdateReplacesTheProgramAfterVerifyingIt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script stand-in")
	}
	dir := t.TempDir()
	self := filepath.Join(dir, "macron")
	os.WriteFile(self, []byte("#!/bin/sh\necho old\n"), 0o755)
	oldSelf, oldAPI := selfPath, releaseAPI
	selfPath = func() (string, error) { return self, nil }
	defer func() { selfPath, releaseAPI = oldSelf, oldAPI }()

	srv := fakeRelease(t, "99.0.0", false)
	releaseAPI = srv.URL + "/releases"
	io, out := testIO("")
	if _, code := Run([]string{"update", "--check"}, io); code != 0 || !strings.Contains(out.String(), "99.0.0") {
		t.Fatalf("check: %d\n%s", code, out)
	}
	if b, _ := os.ReadFile(self); !strings.Contains(string(b), "echo old") {
		t.Fatal("--check changes nothing")
	}
	io, out = testIO("")
	if _, code := Run([]string{"update", "--no-restart"}, io); code != 0 {
		t.Fatalf("update: %d\n%s", code, out)
	}
	if b, _ := os.ReadFile(self); !strings.Contains(string(b), "macron 99.0.0") {
		t.Fatalf("the program was not replaced:\n%s\n%s", b, out)
	}
	if b, _ := os.ReadFile(self + ".old"); !strings.Contains(string(b), "echo old") {
		t.Error("the old program is kept")
	}
	if fi, _ := os.Stat(self); fi.Mode().Perm()&0o100 == 0 {
		t.Error("the new program is executable")
	}

	// Already on the newest release.
	srv2 := fakeRelease(t, app.Version, false)
	releaseAPI = srv2.URL + "/releases"
	io, out = testIO("")
	if _, code := Run([]string{"update"}, io); code != 0 || !strings.Contains(out.String(), "newest") {
		t.Errorf("up to date: %d\n%s", code, out)
	}
}

func TestUpdateRefusesATamperedDownload(t *testing.T) {
	dir := t.TempDir()
	self := filepath.Join(dir, "macron")
	os.WriteFile(self, []byte("#!/bin/sh\necho old\n"), 0o755)
	oldSelf, oldAPI := selfPath, releaseAPI
	selfPath = func() (string, error) { return self, nil }
	defer func() { selfPath, releaseAPI = oldSelf, oldAPI }()
	srv := fakeRelease(t, "99.0.0", true)
	releaseAPI = srv.URL + "/releases"
	io, out := testIO("")
	if _, code := Run([]string{"update", "--no-restart"}, io); code == 0 || !strings.Contains(out.String(), "SHA-256") {
		t.Fatalf("a download that does not match its sum must fail: %d\n%s", code, out)
	}
	if b, _ := os.ReadFile(self); !strings.Contains(string(b), "echo old") {
		t.Error("nothing is replaced")
	}
}

func TestBackupHoldsTheDatabaseAndNotesButNotWorkspaces(t *testing.T) {
	home := t.TempDir()
	t.Setenv("MACRON_HOME", home)
	os.WriteFile(filepath.Join(home, "SOUL.md"), []byte("soul"), 0o600)
	os.WriteFile(filepath.Join(home, "config.env"), []byte("A=1\n"), 0o600)
	os.MkdirAll(filepath.Join(home, "workspaces", "tg-1"), 0o700)
	os.WriteFile(filepath.Join(home, "workspaces", "tg-1", "big.bin"), []byte("x"), 0o600)
	os.MkdirAll(filepath.Join(home, "skills", "dev", "x"), 0o700)
	os.WriteFile(filepath.Join(home, "skills", "dev", "x", "SKILL.md"), []byte("s"), 0o600)
	out := filepath.Join(t.TempDir(), "b.tar.gz")
	io, text := testIO("")
	if _, code := Run([]string{"backup", out}, io); code != 0 {
		t.Fatalf("backup: %d\n%s", code, text)
	}
	names := tarNames(t, out)
	for _, want := range []string{"macron/SOUL.md", "macron/config.env", "macron/skills/dev/x/SKILL.md"} {
		if !names[want] {
			t.Errorf("backup lacks %s: %v", want, names)
		}
	}
	if names["macron/workspaces/tg-1/big.bin"] {
		t.Error("workspaces are left out without --all")
	}
	out2 := filepath.Join(t.TempDir(), "all.tar.gz")
	Run([]string{"backup", "--all", out2}, io)
	if !tarNames(t, out2)["macron/workspaces/tg-1/big.bin"] {
		t.Error("--all includes them")
	}
	if fi, _ := os.Stat(out); fi.Mode().Perm() != 0o600 {
		t.Errorf("a backup holds keys: mode %v", fi.Mode().Perm())
	}
}

func tarNames(t *testing.T, path string) map[string]bool {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	names := map[string]bool{}
	for {
		h, err := tr.Next()
		if err != nil {
			return names
		}
		names[h.Name] = true
	}
}

func TestUnknownCommandsAndFlagsPassThrough(t *testing.T) {
	io, _ := testIO("")
	if handled, _ := Run(nil, io); handled {
		t.Error("no arguments start the agent")
	}
	if handled, _ := Run([]string{"-check"}, io); handled {
		t.Error("an agent flag is not a command")
	}
	if handled, code := Run([]string{"nonsense"}, io); !handled || code == 0 {
		t.Error("an unknown command is an error")
	}
	io, out := testIO("")
	Run([]string{"version"}, io)
	if !strings.Contains(out.String(), app.Version) {
		t.Errorf("version: %q", out)
	}
}
