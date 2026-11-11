package tools

import (
	"testing"
)

func TestExtractFileSymbolsGo(t *testing.T) {
	code := `package main

type Config struct {
	Port int
}

func NewServer() *Config {
	return nil
}

func (c *Config) Start() error {
	return nil
}
`
	syms := ExtractFileSymbols("server.go", code)
	if len(syms) < 3 {
		t.Fatalf("expected at least 3 symbols, got %d", len(syms))
	}

	names := map[string]bool{}
	for _, s := range syms {
		names[s.Name] = true
	}
	if !names["Config"] || !names["NewServer"] || !names["Start"] {
		t.Errorf("missing expected symbols in extracted map: %v", names)
	}
}

func TestExtractFileSymbolsPython(t *testing.T) {
	code := `import os

class DataPipeline:
    def process(self):
        pass

def run():
    pass
`
	syms := ExtractFileSymbols("pipeline.py", code)
	names := map[string]bool{}
	for _, s := range syms {
		names[s.Name] = true
	}
	if !names["DataPipeline"] || !names["process"] || !names["run"] {
		t.Errorf("missing Python symbols: %v", names)
	}
}
