package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestConnectionDescriptionsHaveTypedFieldsAndHideValues(t *testing.T) {
	t.Setenv("MACRON_HOME", t.TempDir())
	t.Setenv("BALE_BOT_TOKEN", "credential-must-stay-private")
	for _, s := range ConnectionSpecs() {
		var got struct {
			Settings     []connectionField `json:"settings"`
			Alternatives [][]string        `json:"required_credential_alternatives"`
		}
		raw := describeConnection(s)
		if err := json.Unmarshal([]byte(raw), &got); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(raw, "credential-must-stay-private") {
			t.Fatal("credential leaked")
		}
		fields := map[string]connectionField{}
		for _, f := range got.Settings {
			fields[f.Key] = f
			if f.Type == "" || f.Purpose == "" {
				t.Fatalf("missing field documentation: %s", f.Key)
			}
		}
		for _, alt := range got.Alternatives {
			for _, key := range alt {
				if _, ok := fields[key]; !ok {
					t.Fatalf("undocumented required field %s", key)
				}
			}
		}
		if s.ID == "bale" {
			f := fields["MACRON_BALE_API"]
			if f.Type != "url" || f.Secret || len(f.RequiredFor) != 0 || !strings.HasPrefix(f.Default, "https://") {
				t.Fatal("API override is not an optional URL", f)
			}
			if fields["BALE_BOT_TOKEN"].State != "set" || !fields["BALE_BOT_TOKEN"].Secret {
				t.Fatal("token state incorrect")
			}
			if len(fields["MACRON_BALE_OWNERS"].RequiredFor) != 1 {
				t.Fatal("owner requirement missing")
			}
		}
	}
}
