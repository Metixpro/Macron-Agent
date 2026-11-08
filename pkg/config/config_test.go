package config

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newFS() (*Config, *flag.FlagSet) {
	c := &Config{}
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	c.Bind(fs)
	return c, fs
}

func clearEnv(t *testing.T) {
	for _, k := range []string{EnvAPIURL, EnvAPIKey, EnvModel, EnvWorkspaceDir, EnvEnvProxy} {
		t.Setenv(k, "")
	}
}

func TestEnvThenFlagPrecedence(t *testing.T) {
	clearEnv(t)
	t.Setenv(EnvAPIURL, "https://env.example/v1/")
	t.Setenv(EnvAPIKey, "env-key")
	c, fs := newFS()
	if err := fs.Parse([]string{"-model", "flag-model"}); err != nil {
		t.Fatal(err)
	}
	warns, err := c.Validate(fs)
	if err != nil {
		t.Fatal(err)
	}
	if c.APIURL != "https://env.example/v1" || c.APIKey != "env-key" || c.Model != "flag-model" || len(warns) != 0 {
		t.Fatalf("%+v warns=%v", c, warns)
	}
}

func TestValidationErrors(t *testing.T) {
	clearEnv(t)
	for name, args := range map[string][]string{
		"missing url": {},
		"bad scheme":  {"-api-url", "ftp://x/v1"},
		"no host":     {"-api-url", "http://"},
		"steps":       {"-api-url", "https://x/v1", "-max-steps", "0"},
		"timeout":     {"-api-url", "https://x/v1", "-run-timeout", "1s"},
	} {
		c, fs := newFS()
		_ = fs.Parse(args)
		if _, err := c.Validate(fs); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestWarnings(t *testing.T) {
	clearEnv(t)
	c, fs := newFS()
	_ = fs.Parse([]string{"-api-url", "http://203.0.113.10:20128/v1", "-api-key", "k"})
	warns, err := c.Validate(fs)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(warns, "\n")
	for _, want := range []string{"plain http", "process list"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing warning about %q in %v", want, warns)
		}
	}
	c2, fs2 := newFS()
	_ = fs2.Parse([]string{"-api-url", "http://127.0.0.1:8080/v1"})
	warns, _ = c2.Validate(fs2)
	if strings.Contains(strings.Join(warns, "\n"), "plain http") {
		t.Error("loopback http is fine")
	}
	if len(warns) == 0 || !strings.Contains(warns[0], "no LLM API key") {
		t.Errorf("missing key should warn: %v", warns)
	}
}

func TestLoadDotEnv(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
	content := "# comment\n\nWAZE_T_A=plain\nexport WAZE_T_B=\"quoted value\"\nWAZE_T_C='single'\nWAZE_T_D=with comment # trailing\nWAZE_T_E=already\nbadline\n=novalue\nWAZE_T_F=a=b=c\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WAZE_T_E", "from-process")
	for _, k := range []string{"WAZE_T_A", "WAZE_T_B", "WAZE_T_C", "WAZE_T_D", "WAZE_T_F"} {
		os.Unsetenv(k)
		t.Cleanup(func() { os.Unsetenv(k) })
	}
	if err := LoadDotEnv(path); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"WAZE_T_A": "plain", "WAZE_T_B": "quoted value", "WAZE_T_C": "single", "WAZE_T_D": "with comment", "WAZE_T_E": "from-process", "WAZE_T_F": "a=b=c"}
	for k, v := range want {
		if got := os.Getenv(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if err := LoadDotEnv(filepath.Join(t.TempDir(), "missing")); err != nil {
		t.Errorf("missing file must not be an error: %v", err)
	}
}

func TestParseIDsAndLocation(t *testing.T) {
	ids, err := ParseIDs("12, 34;56\n78")
	if err != nil || len(ids) != 4 || !ids[56] {
		t.Fatalf("%v %v", ids, err)
	}
	for _, bad := range []string{"abc", "12,x", "-5", "0"} {
		if _, err := ParseIDs(bad); err == nil {
			t.Errorf("%q should be rejected", bad)
		}
	}
	if ids, _ := ParseIDs(""); len(ids) != 0 {
		t.Error("empty")
	}
	t.Setenv(EnvTimezone, "")
	if loc, err := Location(); err != nil || loc.String() != "Asia/Tehran" {
		t.Errorf("default zone: %v %v", loc, err)
	}
	t.Setenv(EnvTimezone, "Mars/Olympus")
	if _, err := Location(); err == nil {
		t.Error("unknown zone must fail")
	}
}

func TestOptionalAPIURL(t *testing.T) {
	clearEnv(t)
	c := &Config{OptionalAPIURL: true}
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	c.Bind(fs)
	_ = fs.Parse(nil)
	if warns, err := c.Validate(fs); err != nil || len(warns) != 0 {
		t.Fatalf("the bot may start without WAZE_API_URL: %v %v", warns, err)
	}
}
