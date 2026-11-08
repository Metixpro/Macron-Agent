package brain

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func open(t *testing.T) *Brain {
	t.Helper()
	b, err := Open(filepath.Join(t.TempDir(), "home"), "You are {{name}}.")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestOpenWritesTheThreeFilesPrivately(t *testing.T) {
	b := open(t)
	for _, f := range []string{SoulFile, UserFile, MemoryFile} {
		fi, err := os.Stat(filepath.Join(b.Dir, f))
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s has mode %v, want 0600", f, fi.Mode().Perm())
		}
	}
	if b.Soul() != "You are {{name}}." || b.IsCustomSoul() {
		t.Fatalf("soul %q custom=%v", b.Soul(), b.IsCustomSoul())
	}
}

func TestSoulFollowsTheFileAndKeepsABackup(t *testing.T) {
	b := open(t)

	if err := os.WriteFile(filepath.Join(b.Dir, SoulFile), []byte("A pirate.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if b.Soul() != "A pirate." || !b.IsCustomSoul() {
		t.Fatalf("the edit is not seen: %q", b.Soul())
	}
	if err := b.SetSoul("A butler."); err != nil {
		t.Fatal(err)
	}
	if bak, _ := os.ReadFile(filepath.Join(b.Dir, SoulFile+".bak")); strings.TrimSpace(string(bak)) != "A pirate." {
		t.Errorf("backup %q", bak)
	}
	if b.Soul() != "A butler." {
		t.Errorf("soul %q", b.Soul())
	}
	if err := b.ResetSoul(); err != nil || b.IsCustomSoul() {
		t.Errorf("reset: %v custom=%v", err, b.IsCustomSoul())
	}
	if err := b.SetSoul("  "); err == nil {
		t.Error("an empty soul is refused")
	}
}

func TestNotesAddReplaceDeleteAndHandEdits(t *testing.T) {
	b := open(t)
	n := b.User
	id, existed, err := n.Add("Lives in Mashhad")
	if err != nil || id != 1 || existed {
		t.Fatalf("add: %d %v %v", id, existed, err)
	}
	if id, existed, _ = n.Add("lives in mashhad"); id != 1 || !existed {
		t.Errorf("the same fact is not added twice: %d %v", id, existed)
	}
	n.Add("Prefers short answers")

	raw := n.Raw() + "\nMy own comment.\n* Works nights\n"
	if err := n.SetRaw(raw); err != nil {
		t.Fatal(err)
	}
	list, _ := n.List()
	if len(list) != 3 || list[2].Text != "Works nights" || list[2].ID != 3 {
		t.Fatalf("list %+v", list)
	}
	if ok, _ := n.Replace(2, "Prefers detailed answers"); !ok {
		t.Fatal("replace")
	}
	if ok, _ := n.Delete(1); !ok {
		t.Fatal("delete")
	}
	if ok, _ := n.Delete(9); ok {
		t.Error("deleting a fact that is not there reports false")
	}
	list, _ = n.List()
	if len(list) != 2 || list[0].Text != "Prefers detailed answers" || list[1].Text != "Works nights" {
		t.Fatalf("after edits %+v", list)
	}
	if !strings.Contains(n.Raw(), "My own comment.") || !strings.HasPrefix(n.Raw(), "# USER.md") {
		t.Errorf("the owner's lines and the title stay:\n%s", n.Raw())
	}
	if removed, _ := n.Clear(); removed != 2 {
		t.Errorf("clear removed %d", removed)
	}
	if !strings.Contains(n.Raw(), "My own comment.") {
		t.Error("clear keeps what is not a fact")
	}
}

func TestNotesLimits(t *testing.T) {
	b := open(t)
	n := b.Memory
	long := strings.Repeat("ب", 1000)
	id, _, err := n.Add(long)
	if err != nil || id != 1 {
		t.Fatal(err)
	}
	if list, _ := n.List(); len([]rune(list[0].Text)) != MaxEntryChars {
		t.Errorf("a fact is cut to %d characters, got %d", MaxEntryChars, len([]rune(list[0].Text)))
	}
	for i := 2; i <= MaxEntries; i++ {
		if _, _, err := n.Add(strings.Repeat("x", 10) + string(rune('a'+i%26)) + strings.Repeat("y", i)); err != nil && err != ErrTooLong {
			t.Fatalf("fact %d: %v", i, err)
		}
	}
	if err := n.SetRaw(strings.Repeat("- a\n", MaxEntries+1)); err == nil {
		t.Error("too many facts are refused")
	}
	if err := n.SetRaw(strings.Repeat("a", MaxFileChars+1)); err == nil {
		t.Error("a file that is too long is refused")
	}
}
