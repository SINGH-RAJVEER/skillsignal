// Package lexical retrieves per-requirement lexical evidence with pure
// TF-IDF cosine ranking in application code: no provider, no extension.
package lexical

import (
	"math"
	"sort"
	"strings"

	"github.com/skillsignal/worker/internal/vocab"
)

// StopTokens carry no screening signal.
var StopTokens = func() map[string]struct{} {
	set := make(map[string]struct{})
	for _, token := range []string{"a", "an", "and", "are", "as", "at", "be", "by", "for", "from", "in", "is", "of", "on", "or", "our", "that", "the", "to", "we", "will", "with", "you", "your"} {
		set[token] = struct{}{}
	}
	return set
}()

// Tokens normalizes free text into screening tokens.
func Tokens(text string) []string {
	var out []string
	for _, token := range vocab.TokenPattern.FindAllString(strings.ToLower(text), -1) {
		if len([]rune(token)) > 1 {
			if _, stop := StopTokens[token]; !stop {
				out = append(out, token)
			}
		}
	}
	return out
}

// Match is one ranked block hit.
type Match struct {
	BlockID string
	Score   float64
}

// TopMatches ranks blocks against the requirement text.
func TopMatches(requirementText string, blockTexts map[string]string, limit int, minScore float64) []Match {
	counts := make(map[string]map[string]int)
	for blockID, text := range blockTexts {
		tokens := Tokens(text)
		if len(tokens) == 0 {
			continue
		}
		counter := make(map[string]int)
		for _, token := range tokens {
			counter[token]++
		}
		counts[blockID] = counter
	}
	if len(counts) == 0 {
		return nil
	}
	documentFrequency := make(map[string]int)
	for _, counter := range counts {
		for token := range counter {
			documentFrequency[token]++
		}
	}
	total := float64(len(counts))
	idf := func(token string) float64 {
		return math.Log((total+1)/(float64(documentFrequency[token])+1)) + 1
	}
	vector := func(counter map[string]int) map[string]float64 {
		out := make(map[string]float64, len(counter))
		for token, count := range counter {
			out[token] = float64(count) * idf(token)
		}
		return out
	}
	cosine := func(left, right map[string]float64) float64 {
		dot := 0.0
		for token, weight := range left {
			dot += weight * right[token]
		}
		normLeft, normRight := 0.0, 0.0
		for _, weight := range left {
			normLeft += weight * weight
		}
		for _, weight := range right {
			normRight += weight * weight
		}
		if normLeft == 0 || normRight == 0 {
			return 0
		}
		return dot / (math.Sqrt(normLeft) * math.Sqrt(normRight))
	}
	requirementCounter := make(map[string]int)
	for _, token := range Tokens(requirementText) {
		requirementCounter[token]++
	}
	requirementVector := vector(requirementCounter)
	var scored []Match
	for blockID, counter := range counts {
		score := math.Round(cosine(requirementVector, vector(counter))*10000) / 10000
		if score >= minScore {
			scored = append(scored, Match{BlockID: blockID, Score: score})
		}
	}
	sort.Slice(scored, func(i, j int) bool { return scored[i].Score > scored[j].Score })
	if len(scored) > limit {
		scored = scored[:limit]
	}
	return scored
}

// TopLexicalMatches applies the production thresholds: 3 hits at 0.08.
func TopLexicalMatches(requirementText string, blockTexts map[string]string) []Match {
	return TopMatches(requirementText, blockTexts, 3, 0.08)
}
