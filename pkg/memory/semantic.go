package memory

import (
	"math"
	"sort"
	"strings"
	"unicode"
)

// SemanticEntry represents a stored memory item with its feature vector.
type SemanticEntry struct {
	ID        int64              `json:"id"`
	Text      string             `json:"text"`
	Category  string             `json:"category,omitempty"`
	Vector    map[string]float64 `json:"-"`
	Magnitude float64            `json:"-"`
}

type ScoredMemory struct {
	Entry SemanticEntry
	Score float64
}

// SemanticIndex provides semantic similarity search over long-term memories.
type SemanticIndex struct {
	entries []SemanticEntry
}

func NewSemanticIndex() *SemanticIndex {
	return &SemanticIndex{}
}

func tokenize(s string) []string {
	var tokens []string
	var cur strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			cur.WriteRune(r)
		} else {
			if cur.Len() > 0 {
				tokens = append(tokens, cur.String())
				cur.Reset()
			}
		}
	}
	if cur.Len() > 0 {
		tokens = append(tokens, cur.String())
	}
	return tokens
}

// buildVector generates a normalized term-frequency feature vector including word tokens and character 3-grams.
func buildVector(text string) (map[string]float64, float64) {
	vec := make(map[string]float64)
	tokens := tokenize(text)
	for _, t := range tokens {
		vec["w:"+t] += 1.0
	}

	// Generate character 3-grams to support fuzzy matches and morphological variations (especially in Persian)
	runes := []rune(strings.ToLower(text))
	for i := 0; i+3 <= len(runes); i++ {
		tri := "3g:" + string(runes[i:i+3])
		vec[tri] += 0.5
	}

	mag := 0.0
	for _, v := range vec {
		mag += v * v
	}
	mag = math.Sqrt(mag)
	return vec, mag
}

// Add adds a memory entry to the semantic index.
func (idx *SemanticIndex) Add(id int64, text, category string) {
	vec, mag := buildVector(text)
	idx.entries = append(idx.entries, SemanticEntry{
		ID:        id,
		Text:      text,
		Category:  category,
		Vector:    vec,
		Magnitude: mag,
	})
}

// CosineSimilarity computes cosine similarity between two feature vectors.
func CosineSimilarity(v1 map[string]float64, mag1 float64, v2 map[string]float64, mag2 float64) float64 {
	if mag1 == 0 || mag2 == 0 {
		return 0.0
	}
	dot := 0.0
	// iterate over smaller vector
	smaller, larger := v1, v2
	if len(v2) < len(v1) {
		smaller, larger = v2, v1
	}
	for k, val1 := range smaller {
		if val2, ok := larger[k]; ok {
			dot += val1 * val2
		}
	}
	sim := dot / (mag1 * mag2)
	if sim > 1.0 {
		return 1.0
	}
	return sim
}

// Search returns the topK most semantically relevant memories matching the query.
func (idx *SemanticIndex) Search(query string, topK int, threshold float64) []ScoredMemory {
	if len(idx.entries) == 0 || topK <= 0 {
		return nil
	}
	qVec, qMag := buildVector(query)
	if qMag == 0 {
		return nil
	}

	var scored []ScoredMemory
	for _, e := range idx.entries {
		sim := CosineSimilarity(qVec, qMag, e.Vector, e.Magnitude)
		if sim >= threshold {
			scored = append(scored, ScoredMemory{
				Entry: e,
				Score: sim,
			})
		}
	}

	sort.Slice(scored, func(i, j int) bool {
		return scored[i].Score > scored[j].Score
	})

	if len(scored) > topK {
		scored = scored[:topK]
	}
	return scored
}
