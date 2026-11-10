package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAConfigFromBeforeTheRenameKeepsWorking(t *testing.T) {
	old := legacyPrefix
	path := filepath.Join(t.TempDir(), "config.env")
	body := "# my settings\n" + old + "OWNERS=7\nexport " + old + "MODEL=\"m one\"\nMACRON_API_URL=https://new/v1\n" + old + "API_URL=https://old/v1\n" +
		old + "IMAGE_API_KEY=gone\nTELEGRAM_BOT_TOKEN=1:x\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"MACRON_OWNERS", "MACRON_MODEL", "MACRON_API_URL", "TELEGRAM_BOT_TOKEN"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	if err := LoadDotEnv(path); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("MACRON_OWNERS") != "7" || os.Getenv("MACRON_MODEL") != "m one" || os.Getenv("MACRON_API_URL") != "https://new/v1" {
		t.Errorf("loaded: owners %q, model %q, url %q", os.Getenv("MACRON_OWNERS"), os.Getenv("MACRON_MODEL"), os.Getenv("MACRON_API_URL"))
	}
	data, _ := os.ReadFile(path)
	want := "# my settings\nMACRON_OWNERS=7\nexport MACRON_MODEL=\"m one\"\nMACRON_API_URL=https://new/v1\nTELEGRAM_BOT_TOKEN=1:x\n"
	if string(data) != want {
		t.Errorf("the file was rewritten as:\n%s", data)
	}
	if fi, _ := os.Stat(path); fi.Mode().Perm() != 0o600 {
		t.Errorf("the file's mode became %o", fi.Mode().Perm())
	}
	if strings.Contains(string(data), old) {
		t.Error("an old name stayed in the file")
	}
}

func TestAnOldVariableInTheEnvironmentIsAdopted(t *testing.T) {
	old := legacyPrefix
	t.Setenv(old+"T_ONLY_OLD", "a")
	t.Setenv(old+"T_BOTH", "old")
	t.Setenv("MACRON_T_BOTH", "new")
	t.Setenv("MACRON_T_ONLY_OLD", "")
	os.Unsetenv("MACRON_T_ONLY_OLD")
	AdoptLegacyEnv()
	if os.Getenv("MACRON_T_ONLY_OLD") != "a" || os.Getenv("MACRON_T_BOTH") != "new" {
		t.Errorf("got %q and %q", os.Getenv("MACRON_T_ONLY_OLD"), os.Getenv("MACRON_T_BOTH"))
	}
	if _, set := os.LookupEnv(old + "T_ONLY_OLD"); set {
		t.Error("the old variable stayed, and would reach the commands the agent runs")
	}
}
