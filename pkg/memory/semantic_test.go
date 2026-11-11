package memory

import (
	"testing"
)

func TestSemanticIndex(t *testing.T) {
	idx := NewSemanticIndex()

	idx.Add(1, "User prefers dark mode and minimalist terminal themes", "pref")
	idx.Add(2, "PostgreSQL production database is hosted on 10.0.1.5", "infrastructure")
	idx.Add(3, "کاربر زبان فارسی و قالب‌های ساده را ترجیح می‌دهد", "pref")
	idx.Add(4, "Python FastAPI application runs on port 8000", "code")

	// 1. Search for dark theme in English
	matches := idx.Search("dark theme interface colors", 2, 0.1)
	if len(matches) == 0 || matches[0].Entry.ID != 1 {
		t.Fatalf("expected entry 1 as top match for dark theme, got %v", matches)
	}

	// 2. Search for database IP
	matches = idx.Search("postgres server host IP", 1, 0.1)
	if len(matches) == 0 || matches[0].Entry.ID != 2 {
		t.Fatalf("expected entry 2 for postgres, got %v", matches)
	}

	// 3. Search in Persian
	matches = idx.Search("زبان پیش‌فرض کاربر", 1, 0.1)
	if len(matches) == 0 || matches[0].Entry.ID != 3 {
		t.Fatalf("expected entry 3 for Persian language query, got %v", matches)
	}
}

func TestCosineSimilarityProperties(t *testing.T) {
	v1, m1 := buildVector("quick brown fox")
	v2, m2 := buildVector("quick brown fox")
	sim := CosineSimilarity(v1, m1, v2, m2)
	if sim < 0.99 {
		t.Errorf("identical vectors similarity = %f, want ~1.0", sim)
	}

	v3, m3 := buildVector("completely unrelated quantum physics formula")
	simDisjoint := CosineSimilarity(v1, m1, v3, m3)
	if simDisjoint > 0.15 {
		t.Errorf("disjoint vectors similarity = %f, want near 0", simDisjoint)
	}
}
