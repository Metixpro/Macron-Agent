package updater

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/Metixpro/macron-agent/pkg/config"
)

func TestPreflightKeepsDiscoveredHomeForTemporaryBinary(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv(config.EnvHome, "")
	t.Setenv(config.EnvEnvFile, "")
	t.Setenv(config.EnvProfile, "")
	home := config.Home()
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	envFile := config.EnvFilePath()
	if err := os.WriteFile(envFile, []byte("MACRON_MODEL=local\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MACRON_TEST_EXPECT_HOME", home)
	t.Setenv("MACRON_TEST_EXPECT_ENV", envFile)
	binary := filepath.Join(root, "macron.next")
	body := "#!/bin/sh\ncase \"$1\" in version) echo 'macron 9.9.9';; --check) [ \"$MACRON_HOME\" = \"$MACRON_TEST_EXPECT_HOME\" ] && [ \"$MACRON_ENV_FILE\" = \"$MACRON_TEST_EXPECT_ENV\" ] || exit 1; echo 'ok 9.9.9';; *) exit 1;; esac\n"
	if err := os.WriteFile(binary, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	if err := Preflight(binary, "9.9.9"); err != nil {
		t.Fatal(err)
	}
}
