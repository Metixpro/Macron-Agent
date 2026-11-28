package config

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestSecretKeyPreservesExistingAndErrorsOnInvalid(t *testing.T) {
	t.Setenv(EnvSecretKey, "")

	home1 := t.TempDir()
	t.Setenv(EnvHome, home1)
	if err := os.WriteFile(filepath.Join(home1, FileSecret), []byte("existing-secret-key\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := SecretKey()
	if err != nil || got != "existing-secret-key" {
		t.Fatalf("SecretKey() = %q, %v; want existing-secret-key", got, err)
	}

	home2 := t.TempDir()
	t.Setenv(EnvHome, home2)
	if err := os.WriteFile(filepath.Join(home2, FileSecret), []byte("   \n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := SecretKey(); err == nil {
		t.Fatal("SecretKey() overwrote empty secret.key instead of erroring")
	}

	home3 := t.TempDir()
	t.Setenv(EnvHome, home3)
	if err := os.Mkdir(filepath.Join(home3, FileSecret), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := SecretKey(); err == nil {
		t.Fatal("SecretKey() succeeded when secret.key is a directory")
	}
}

func runConcurrentSecretKey(t *testing.T, workers int, fn func() (string, error)) {
	t.Helper()
	results := make(chan string, workers)
	errs := make(chan error, workers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			k, err := fn()
			if err != nil {
				errs <- err
				return
			}
			results <- k
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		t.Fatalf("concurrent SecretKey error: %v", err)
	}
	var first string
	count := 0
	for k := range results {
		count++
		if first == "" {
			first = k
		} else if k != first {
			t.Fatalf("concurrent callers got different keys: %q vs %q", first, k)
		}
	}
	if count != workers || len(first) != 64 {
		t.Fatalf("count=%d len=%d", count, len(first))
	}
}

func TestSecretKeyConcurrentCreationMatches(t *testing.T) {
	t.Setenv(EnvSecretKey, "")
	home := t.TempDir()
	t.Setenv(EnvHome, home)
	runConcurrentSecretKey(t, 16, SecretKey)
}

func TestSecretKeyAtomicCreateNoEmptyWindow(t *testing.T) {
	home := t.TempDir()
	path := filepath.Join(home, FileSecret)
	runConcurrentSecretKey(t, 16, func() (string, error) {
		return secretKeyFromPath(path)
	})

	entries, err := os.ReadDir(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != FileSecret {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Fatalf("unexpected files in home after atomic secret key creation: %v", names)
	}
}
