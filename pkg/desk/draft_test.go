package desk

import "testing"

func TestDraftArgFromPartialArguments(t *testing.T) {
	cases := map[string]string{
		`{"filename": "resume/index.html", "content": "<!doctype html>`:    "resume/index.html",
		`{"command": "Get-ChildItem -Name; Write-Output \"hi\"\nnext line`: `Get-ChildItem -Name; Write-Output "hi"`,
		`{"filename": "C:\\Users\\me\\a.txt", "content": "`:                `C:\Users\me\a.txt`,
		`{"content": "only content so far`:                                 "",
		`{"filena`:                                                         "",
		`{"url": "https://example.com/page", "max`:                         "https://example.com/page",
	}
	for head, want := range cases {
		if got := draftArg(head); got != want {
			t.Errorf("draftArg(%q) = %q, want %q", head, got, want)
		}
	}
}
