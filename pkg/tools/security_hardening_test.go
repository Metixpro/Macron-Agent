package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/machine"
	"github.com/Metixpro/macron-agent/pkg/netclient"
)

func TestCleanPathAndSecretEnvBlocking(t *testing.T) {
	badPaths := []string{
		"../secret.txt",
		"sub/../../etc/passwd",
		"/etc/passwd",
		`C:\Windows\win.ini`,
		`\\attacker\share\file`,
		`~/.ssh/id_rsa`,
	}
	for _, p := range badPaths {
		if _, err := cleanPath(p); err == nil {
			t.Fatalf("cleanPath(%q) unexpectedly succeeded", p)
		}
	}
	if got, err := cleanPath("/workspace/sub/ok.txt"); err != nil || got != "sub/ok.txt" {
		t.Fatalf("cleanPath valid failed: got=%q err=%v", got, err)
	}

	if !isBlockedSecretEnv("MACRON_API_KEY") || !isBlockedSecretEnv("TELEGRAM_BOT_TOKEN") || !isBlockedSecretEnv("GITHUB_TOKEN") {
		t.Fatal("expected core secret env vars to be blocked")
	}
	if isBlockedSecretEnv("MY_CUSTOM_REST_TOKEN") {
		t.Fatal("custom API env var should be allowed (gated)")
	}
}

func TestTextPatchBlocksTraversal(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(filepath.Dir(base), "outside-secret.txt")
	_ = os.WriteFile(outside, []byte("secret\n"), 0o600)
	defer os.Remove(outside)

	m, err := machine.New(machine.Config{BaseDir: base})
	if err != nil {
		t.Fatalf("machine.New: %v", err)
	}
	r := &Registry{Machine: m}
	patch := "--- a/../outside-secret.txt\n+++ b/../outside-secret.txt\n@@ -1 +1 @@\n-secret\n+pwned\n"
	if _, err := r.textPatch("s1", ".", patch, "1"); err == nil {
		t.Fatal("textPatch allowed traversal outside workspace")
	}
	got, _ := os.ReadFile(outside)
	if string(got) != "secret\n" {
		t.Fatalf("outside file was modified: %q", string(got))
	}
}

func TestValidateURLBlocksCloudMetadata(t *testing.T) {
	for _, raw := range []string{
		"http://169.254.169.254/latest/meta-data/",
		"http://metadata.google.internal/computeMetadata/v1/",
		"http://[::ffff:169.254.169.254]/",
	} {
		if _, err := netclient.ValidateURL(raw); err == nil {
			t.Fatalf("ValidateURL(%q) should have been blocked", raw)
		}
	}
	if _, err := netclient.ValidateURL("http://127.0.0.1:18941/v1"); err != nil {
		t.Fatalf("ValidateURL blocked localhost provider: %v", err)
	}
}

func TestOpenAPIGatesGetWithAuthEnv(t *testing.T) {
	base := t.TempDir()
	m, err := machine.New(machine.Config{BaseDir: base})
	if err != nil {
		t.Fatalf("machine.New: %v", err)
	}
	r := &Registry{Machine: m}
	if err := m.WriteFile("s1", "spec.json", []byte(`{"openapi":"3.0.0","paths":{"/items":{"get":{"operationId":"getItems"}}}}`)); err != nil {
		t.Fatal(err)
	}
	_, err = r.CallOpenAPIEndpoint(context.Background(), "s1", "spec.json", "getItems", "", "", "https://example.com/api", "MACRON_API_KEY", nil, nil)
	if err == nil {
		t.Fatal("expected blocked core secret env var to error")
	}
}

func TestBrowserTargetAndBrowseActionValidation(t *testing.T) {
	for _, bad := range []string{
		"file:///etc/passwd",
		"javascript:alert(1)",
		"http://169.254.169.254/latest/meta-data/",
		"169.254.169.254/latest/meta-data",
		"metadata.google.internal/computeMetadata/v1/",
		"//169.254.169.254/latest/meta-data/",
	} {
		if err := validateBrowserTarget(bad); err == nil {
			t.Fatalf("validateBrowserTarget(%q) unexpectedly succeeded", bad)
		}
	}
	for _, ok := range []string{"", "https://www.google.com", "example.com/docs"} {
		if err := validateBrowserTarget(ok); err != nil {
			t.Fatalf("validateBrowserTarget(%q) rejected valid target: %v", ok, err)
		}
	}

	base := t.TempDir()
	m, err := machine.New(machine.Config{BaseDir: base})
	if err != nil {
		t.Fatalf("machine.New: %v", err)
	}
	r := &Registry{Machine: m, browser: &browserRuntime{}}
	out, err := r.browse(context.Background(), "s1", map[string]any{
		"actions": []any{
			map[string]any{"action": "goto", "url": "http://169.254.169.254/latest/meta-data/"},
		},
	})
	if err != nil {
		t.Fatalf("browse returned error instead of validation message: %v", err)
	}
	if out == "" || len(out) < 6 || out[:6] != "error:" {
		t.Fatalf("browse did not reject metadata goto action: %q", out)
	}
}
