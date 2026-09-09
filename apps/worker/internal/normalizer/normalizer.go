// Package normalizer derives deterministic resume facts (contact,
// normalized skill aliases with source block references) from parsed blocks.
package normalizer

import (
	"regexp"
	"sort"
	"strings"

	"github.com/skillsignal/worker/internal/vocab"
)

// Block is one parsed evidence block.
type Block struct {
	ID   string
	Text string
}

var emailPattern = regexp.MustCompile(`(?i)\b[A-Z0-9._%+-]+@[A-Z0-9.-]+\.[A-Z]{2,}\b`)
var namePattern = regexp.MustCompile(`^[A-Za-z][A-Za-z .'-]{1,80}$`)

// Skill is a normalized skill alias with evidence block references.
type Skill struct {
	CanonicalName    string
	Category         string
	EvidenceBlockIDs []string
}

// Facts are the deterministic resume facts.
type Facts struct {
	Contact map[string]*string
	Skills  []Skill
}

// Normalize builds contact facts and skill aliases from blocks.
func Normalize(blocks []Block) (Facts, error) {
	v, err := vocab.Load()
	if err != nil {
		return Facts{}, err
	}
	order := make(map[string]int, len(blocks))
	for i, block := range blocks {
		order[block.ID] = i
	}
	hits := make(map[string][]string)
	for _, block := range blocks {
		for canonical := range v.Mention(block.Text) {
			ids := hits[canonical]
			duplicate := false
			for _, id := range ids {
				if id == block.ID {
					duplicate = true
					break
				}
			}
			if !duplicate {
				hits[canonical] = append(ids, block.ID)
			}
		}
	}
	names := make([]string, 0, len(hits))
	for name := range hits {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		return strings.ToLower(names[i]) < strings.ToLower(names[j])
	})
	skills := make([]Skill, 0, len(names))
	for _, name := range names {
		ids := hits[name]
		sort.Slice(ids, func(i, j int) bool { return order[ids[i]] < order[ids[j]] })
		skills = append(skills, Skill{
			CanonicalName:    name,
			Category:         v.CategoryFor(name),
			EvidenceBlockIDs: ids,
		})
	}
	return Facts{Contact: ContactFacts(blocks), Skills: skills}, nil
}

// ContactFacts applies the name/email/location heuristics over block lines.
func ContactFacts(blocks []Block) map[string]*string {
	var lines []string
	for _, block := range blocks {
		for _, line := range strings.Split(block.Text, "\n") {
			if trimmed := strings.TrimSpace(line); trimmed != "" {
				lines = append(lines, trimmed)
			}
		}
	}
	combined := strings.Join(lines, "\n")
	var email *string
	if match := emailPattern.FindString(combined); match != "" {
		email = &match
	}
	var name *string
	if len(lines) > 0 {
		first := lines[0]
		words := strings.Fields(first)
		allCapitalized := true
		for _, word := range words {
			if word == "" || !isUpper(rune(word[0])) {
				allCapitalized = false
				break
			}
		}
		if len(words) >= 2 && len(words) <= 5 && namePattern.MatchString(first) && allCapitalized {
			name = &first
		}
	}
	var location *string
	for _, line := range lines[:min(12, len(lines))] {
		if strings.HasPrefix(strings.ToLower(line), "location:") {
			trimmed := strings.TrimSpace(line[len("location:"):])
			location = &trimmed
			break
		}
	}
	return map[string]*string{"name": name, "email": email, "location": location}
}

func isUpper(r rune) bool { return r >= 'A' && r <= 'Z' }

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
