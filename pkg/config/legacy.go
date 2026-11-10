package config

import (
	"os"
	"strings"
)

const legacyPrefix = "WA" + "ZE_"

const prefix = "MACRON_"

func AdoptLegacyEnv() {
	for _, kv := range os.Environ() {
		k, v, ok := strings.Cut(kv, "=")
		rest, legacy := strings.CutPrefix(k, legacyPrefix)
		if !ok || !legacy || rest == "" {
			continue
		}
		if _, set := os.LookupEnv(prefix + rest); !set {
			_ = os.Setenv(prefix+rest, v)
		}
		_ = os.Unsetenv(k)
	}
}

func renameLegacyKey(key string) (string, bool) {
	rest, legacy := strings.CutPrefix(key, legacyPrefix)
	if !legacy || rest == "" {
		return key, false
	}
	return prefix + rest, true
}
