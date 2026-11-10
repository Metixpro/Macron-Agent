package config

import (
	"os"
	"strings"
)

// legacyPrefix is how the settings were named before the agent was called Macron.
// It is the only place the old name lives: a server set up back then keeps working
// after an update, and its config.env is rewritten with the MACRON_ names.
const legacyPrefix = "WA" + "ZE_"

const prefix = "MACRON_"

// AdoptLegacyEnv gives a variable still set under the old prefix (a service unit,
// a shell profile) its MACRON_ name, unless that one is set, and drops the old one.
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

// renameLegacyKey is the config.env side: the key's new name, when it has an old one.
func renameLegacyKey(key string) (string, bool) {
	rest, legacy := strings.CutPrefix(key, legacyPrefix)
	if !legacy || rest == "" {
		return key, false
	}
	return prefix + rest, true
}
