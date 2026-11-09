package router

import "testing"

func TestAndroidAttachmentsHaveTheirOwnWorkContext(t *testing.T) {
	history := []Turn{{Role: "user", Text: "install nginx"}, {Role: "assistant", Text: "installed"}}
	for _, name := range []string{"app.apk", "app.APK", "app.aab", "app.apks", "app.xapk"} {
		in := TriageInput{Input: Input{Text: "اینو بررسی کن", Files: []string{name}}, History: history}
		v := Classify(in.Input)
		if v.Kind != Code || v.Signals[0].Why != "android_package" {
			t.Errorf("wrong APK routing for %s: %+v", name, v)
		}
		tri := Local(in)
		if tri.Context != ContextStandalone || tri.NeedsTools < 0.8 {
			t.Errorf("fresh upload inherited old installation context: %+v", tri)
		}
	}
	reply := Local(TriageInput{Input: Input{Text: "اینو بررسی کن", Files: []string{"app.apk"}, Reply: true}, History: history})
	if reply.Context == ContextStandalone {
		t.Fatal("explicit reply was treated as an unrelated new upload")
	}
}
