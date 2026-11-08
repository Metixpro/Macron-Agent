package main

import (
	"regexp"
	"testing"
)

func TestNoEzafeYeAfterFinalHe(t *testing.T) {
	re := regexp.MustCompile(`\p{Arabic}+ه\x{200C}ی([^\p{L}\p{Mn}\x{200C}]|$)`)
	for fa := range english {
		if w := re.FindString(fa); w != "" {
			t.Errorf("«%s» in %q", w, fa)
		}
	}
}
