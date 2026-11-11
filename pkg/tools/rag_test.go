package tools

import (
	"testing"
)

func TestChunkDocument(t *testing.T) {
	content := `line 1
line 2
line 3
line 4
line 5
line 6
line 7
line 8
line 9
line 10`

	chunks := chunkDocument("test.txt", content, 30, 10)
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(chunks))
	}
	if chunks[0].File != "test.txt" {
		t.Errorf("chunk file = %q, want test.txt", chunks[0].File)
	}
	if chunks[0].LineStart != 1 {
		t.Errorf("chunk 0 lineStart = %d, want 1", chunks[0].LineStart)
	}
}

func TestRAGVectorSimilarity(t *testing.T) {
	text := "FastAPI handles asynchronous request routing in Python."
	v1, m1 := buildVectorMap("python async routing")
	v2, m2 := buildVectorMap(text)
	sim := cosineSim(v1, m1, v2, m2)
	if sim < 0.2 {
		t.Errorf("similarity = %f, expected relevant match (>0.2)", sim)
	}

	vUnrelated, mUnrelated := buildVectorMap("baking a chocolate cake recipe")
	simUn := cosineSim(vUnrelated, mUnrelated, v2, m2)
	if simUn > 0.05 {
		t.Errorf("unrelated similarity = %f, expected near 0", simUn)
	}
}
