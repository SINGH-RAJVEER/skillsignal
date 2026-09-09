// Package semantic ranks embedding cosine matches and hashes block texts
// for the content-addressed embedding cache.
package semantic

import (
	"encoding/hex"
	"math"
	"sort"

	"golang.org/x/crypto/blake2b"
)

// TextHash is the blake2b-128 content key for the embedding cache,
// matching the Python service byte-for-byte.
func TextHash(text string) string {
	h, err := blake2b.New(16, nil)
	if err != nil {
		panic("semantic: blake2b unavailable: " + err.Error())
	}
	h.Write([]byte(text))
	return hex.EncodeToString(h.Sum(nil))
}

// CosineSimilarity of two equal-length vectors, else zero.
func CosineSimilarity(a, b []float64) float64 {
	if len(a) != len(b) {
		return 0
	}
	dot, normA, normB := 0.0, 0.0, 0.0
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

// Match is one ranked block hit.
type Match struct {
	BlockID    string
	Similarity float64
}

// TopMatches ranks block vectors against a requirement vector.
func TopMatches(requirementVector []float64, blockVectors map[string][]float64, limit int, minSimilarity float64) []Match {
	var ranked []Match
	for blockID, vector := range blockVectors {
		similarity := math.Round(CosineSimilarity(requirementVector, vector)*10000) / 10000
		if similarity >= minSimilarity {
			ranked = append(ranked, Match{BlockID: blockID, Similarity: similarity})
		}
	}
	sort.Slice(ranked, func(i, j int) bool { return ranked[i].Similarity > ranked[j].Similarity })
	if len(ranked) > limit {
		ranked = ranked[:limit]
	}
	return ranked
}

// TopSemanticMatches applies the production thresholds: 3 hits at 0.3.
func TopSemanticMatches(requirementVector []float64, blockVectors map[string][]float64) []Match {
	return TopMatches(requirementVector, blockVectors, 3, 0.3)
}
