package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestInstallationReceiptValidatesBinaryAndOwner(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "macron")
	home := filepath.Join(dir, "state")
	record := "macron-install-v1\nbinary=" + binary + "\nhome=" + home + "\nscope=user\nuid=" + strconv.Itoa(os.Geteuid()) + "\n"
	path := InstallRecordPath(binary)
	if err := os.WriteFile(path, []byte(record), 0600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadInstallation(binary)
	if err != nil || got.Binary != binary || got.Home != home || got.UID != os.Geteuid() {
		t.Fatalf("receipt: %+v %v", got, err)
	}
	for _, bad := range []string{strings.Replace(record, "scope=user", "scope=unknown", 1), strings.Replace(record, "binary="+binary, "binary=/another/macron", 1), strings.Replace(record, "uid="+strconv.Itoa(os.Geteuid()), "uid=invalid", 1)} {
		_ = os.WriteFile(path, []byte(bad), 0600)
		if _, err := ReadInstallation(binary); err == nil {
			t.Fatal("invalid receipt accepted")
		}
	}
}

func TestRetiredMediaKeysAreRemovedWithoutTouchingOtherSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.env")
	input := "# keep comment\nMACRON_API_KEY=keep-this\nMACRON_IMAGE_API_KEY=remove-one\nexport MACRON_MUSIC_API_KEY=remove-two\nMACRON_MODEL=local\nOTHER_IMAGE_KEY=keep-too\n"
	if err := os.WriteFile(path, []byte(input), 0640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0640); err != nil {
		t.Fatal(err)
	}
	if err := PruneRetiredEnv(path); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	text := string(data)
	if strings.Contains(text, "remove-") || !strings.Contains(text, "MACRON_API_KEY=keep-this") || !strings.Contains(text, "OTHER_IMAGE_KEY=keep-too") || !strings.Contains(text, "# keep comment") {
		t.Fatal("incorrect retirement cleanup")
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0640 {
		t.Fatal("config permissions changed")
	}
	if err := PruneRetiredEnv(path); err != nil {
		t.Fatal("cleanup is not idempotent", err)
	}
}
