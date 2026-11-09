package tools

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

func TestAndroidPackagesCanBeInspectedAndExtracted(t *testing.T) {
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	for name, data := range map[string]string{"AndroidManifest.xml": "\x03\x00\x08\x00binary manifest", "classes.dex": "dex\n035\x00", "assets/config.json": "{\"example\":true}"} {
		f, err := z.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.Write([]byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	r := newRegistry(t, true, nil)
	for _, ext := range []string{"apk", "APK", "aab", "apks", "xapk"} {
		t.Run(ext, func(t *testing.T) {
			session := "android-" + ext
			name := "sample." + ext
			if err := r.Machine.WriteFile(session, name, buf.Bytes()); err != nil {
				t.Fatal(err)
			}
			if !IsAndroidPackage(name) || !IsArchive(name) {
				t.Fatal("Android package is missing from supported input formats")
			}
			listed := call(t, r, session, ToolExtract, map[string]any{"filename": name, "list": true})
			for _, want := range []string{"AndroidManifest.xml", "classes.dex", "assets/config.json"} {
				if !strings.Contains(listed, want) {
					t.Errorf("APK listing misses %s: %s", want, listed)
				}
			}
			out := call(t, r, session, ToolExtract, map[string]any{"filename": name})
			data, _, err := r.Machine.ReadFile(session, "sample/AndroidManifest.xml", 1024)
			if err != nil || !bytes.HasPrefix(data, []byte{3, 0, 8, 0}) {
				t.Fatalf("extraction lost binary XML: %s: %v", out, err)
			}
		})
	}
}
