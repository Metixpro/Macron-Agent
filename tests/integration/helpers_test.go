package integration

import (
	"os"
	"testing"
)

func liveModelKey(t *testing.T) []byte {
	t.Helper()
	if os.Getenv("MACRON_TEST_LIVE") != "1" {
		t.Skip("opt-in live model test; see docs/testing.md")
	}
	key := os.Getenv("MACRON_TEST_API_KEY")
	if key == "" {
		t.Fatal("MACRON_TEST_API_KEY is required for live tests")
	}
	return []byte(key)
}
func liveModelURL() string {
	if s := os.Getenv("MACRON_TEST_API_URL"); s != "" {
		return s
	}
	return "https://api.openai.com/v1"
}
func liveModelName() string {
	if s := os.Getenv("MACRON_TEST_MODEL"); s != "" {
		return s
	}
	return "qwen-3-8-27b"
}
