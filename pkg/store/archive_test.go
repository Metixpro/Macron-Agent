package store

import (
	"strings"
	"testing"
	"time"
)

func TestArchiveSearchPersianEnglishAndNeighbours(t *testing.T) {
	s := openTest(t, Options{})
	turns := [][2]string{
		{"user", "رمز عبور سرور هتزنر رو چطور عوض کنم؟"},
		{"assistant", "با دستور passwd روی سرور وارد شو و رمز تازه بزن."},
		{"user", "Deploy the shop with make ship please"},
		{"assistant", "Done: make ship finished, the shop is live on port 8080."},
		{"user", "یه آهنگ بی‌کلام برای ویدیو بساز"},
	}
	for _, x := range turns {
		if err := s.Archive(bg, 7, ArchiveTurn{Role: x[0], Content: x[1]}); err != nil {
			t.Fatal(err)
		}
	}

	_ = s.Archive(bg, 8, ArchiveTurn{Role: "user", Content: "سرور هتزنر من"})

	hits, err := s.SearchArchive(bg, 7, "سرور هتزنر", time.Time{}, 5)
	if err != nil || len(hits) == 0 || !strings.Contains(hits[0].Snippet, "«") {
		t.Fatalf("persian hits: %+v %v", hits, err)
	}
	if hits[0].Role != "user" && hits[0].Role != "assistant" {
		t.Errorf("role %q", hits[0].Role)
	}
	if hits, _ := s.SearchArchive(bg, 7, "make ship", time.Time{}, 5); len(hits) != 2 {
		t.Errorf("english hits: %+v", hits)
	}
	if hits, _ := s.SearchArchive(bg, 7, "ship dep", time.Time{}, 5); len(hits) == 0 {
		t.Errorf("prefix: %+v", hits)
	}

	if hits, _ := s.SearchArchive(bg, 7, "ship بی‌کلام zzzz", time.Time{}, 5); len(hits) < 2 {
		t.Errorf("any-word fallback: %+v", hits)
	}

	if _, err := s.SearchArchive(bg, 7, `"ship" OR NEAR( ) * -`, time.Time{}, 5); err != nil {
		t.Errorf("query syntax must not leak: %v", err)
	}
	if hits, _ := s.SearchArchive(bg, 7, "ship", time.Now().Add(time.Hour), 5); len(hits) != 0 {
		t.Errorf("since: %+v", hits)
	}
	around, err := s.ArchiveAround(bg, 7, hits0(t, s).ID, 1, 1)
	if err != nil || len(around) != 3 {
		t.Fatalf("around: %+v %v", around, err)
	}
	if n, _ := s.ArchiveCount(bg, 7); n != 5 {
		t.Errorf("count %d", n)
	}
	if n, _ := s.DeleteArchive(bg, 7); n != 5 {
		t.Errorf("deleted %d", n)
	}
	if hits, _ := s.SearchArchive(bg, 7, "ship", time.Time{}, 5); len(hits) != 0 {
		t.Errorf("deleted messages leave the index too: %+v", hits)
	}
	if hits, _ := s.SearchArchive(bg, 8, "هتزنر", time.Time{}, 5); len(hits) != 1 {
		t.Errorf("the other user's archive stays: %+v", hits)
	}
}

func hits0(t *testing.T, s *Store) ArchiveHit {
	t.Helper()
	hits, err := s.SearchArchive(bg, 7, "make ship", time.Time{}, 5)
	if err != nil || len(hits) == 0 {
		t.Fatal(hits, err)
	}
	return hits[len(hits)-1]
}
