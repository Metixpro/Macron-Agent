package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/config"
	"github.com/Metixpro/macron-agent/pkg/store"
)

func testIO() (*IO, *bytes.Buffer) {
	b := new(bytes.Buffer)
	return &IO{Out: b, Err: b}, b
}

func TestDoctorDesktopProviderWithoutConfigEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv(config.EnvHome, home)
	t.Setenv(config.EnvEnvFile, filepath.Join(home, "missing.env"))
	t.Setenv(config.EnvSecretKey, "test-secret")
	for _, key := range []string{keyToken, keyAPIURL, keyAPIKey, keyModel, "MACRON_DB"} {
		t.Setenv(key, "")
	}
	st, err := store.Open(context.Background(), config.HomePath(config.FileDB), store.Options{SecretKey: "test-secret"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.AddProvider(context.Background(), store.Provider{Name: "Desktop model", BaseURL: "https://example.invalid/v1", Model: "test-model", APIKey: "private-test-key", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	st.Close()
	out, buf := testIO()
	if code := doctor(out, []string{"--offline"}); code != 0 {
		t.Fatalf("doctor exit %d: %s", code, buf)
	}
	text := buf.String()
	if !strings.Contains(text, "saved provider Desktop model · test-model") || strings.Contains(text, "no model in the settings") {
		t.Fatal(text)
	}
	if strings.Contains(text, "private-test-key") {
		t.Fatal("doctor disclosed the provider key")
	}
	if _, err := os.Stat(config.EnvFilePath()); !os.IsNotExist(err) {
		t.Fatalf("doctor created config.env: %v", err)
	}
}

func TestDoctorWindowsHints(t *testing.T) {
	for _, tool := range doctorTools {
		if tool.unixOnly {
			continue
		}
		hint := toolInstallHint(tool, "windows")
		for _, bad := range []string{"sudo", "apt ", "brew", "chmod", "install.sh"} {
			if strings.Contains(hint, bad) {
				t.Errorf("%s: %s", tool.name, hint)
			}
		}
	}
	if got := toolInstallHint(doctorTool{brew: "node", apt: "nodejs npm"}, "darwin"); got != "brew install node" {
		t.Fatal(got)
	}
	if got := pythonInstallHint("windows"); !strings.Contains(got, "Python.Python.3.12") {
		t.Fatal(got)
	}
}
