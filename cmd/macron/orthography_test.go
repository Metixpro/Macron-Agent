package main

import (
	"regexp"
	"testing"
)

// The bot writes the ezafe after a final ه without «‌ی»: «همه فایل‌ها»,
// «دکمه توقف», never «همه‌ی فایل‌ها». Every text it shows is a key of the
// English catalog.
func TestNoEzafeYeAfterFinalHe(t *testing.T) {
	re := regexp.MustCompile(`\p{Arabic}+ه\x{200C}ی([^\p{L}\p{Mn}\x{200C}]|$)`)
	for fa := range english {
		if w := re.FindString(fa); w != "" {
			t.Errorf("«%s» in %q", w, fa)
		}
	}
}
