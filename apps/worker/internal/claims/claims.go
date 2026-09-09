// Package claims measures unsupported-claim rates in extraction artifacts:
// every fact value must occur inside its own cited evidence quotes, and
// those quotes must exist in the cited source blocks.
package claims

import (
	"strings"
)

// ClaimTextFields lists grounded value fields per fact collection.
var ClaimTextFields = map[string][]string{
	"contact":        {"name", "email", "phone", "location"},
	"skills":         {"canonicalName", "sourceText"},
	"employment":     {"employer", "title"},
	"education":      {"institution", "degree", "fieldOfStudy"},
	"certifications": {"name"},
}

// CollectionClaims counts one fact collection.
type CollectionClaims struct {
	Total            int
	InvalidCitations int
	UngroundedValues int
}

// Report aggregates collections with capped examples.
type Report struct {
	Collections map[string]CollectionClaims
	Examples    []string
}

// TotalClaims sums all checked values.
func (r Report) TotalClaims() int {
	total := 0
	for _, item := range r.Collections {
		total += item.Total
	}
	return total
}

// UnsupportedClaims sums invalid citations and ungrounded values.
func (r Report) UnsupportedClaims() int {
	total := 0
	for _, item := range r.Collections {
		total += item.InvalidCitations + item.UngroundedValues
	}
	return total
}

// Rate is the unsupported share, or nil when nothing was checked.
func (r Report) Rate() *float64 {
	if r.TotalClaims() == 0 {
		return nil
	}
	rate := float64(r.UnsupportedClaims()) / float64(r.TotalClaims())
	return &rate
}

// MeasureUnsupportedClaims checks facts against block texts.
func MeasureUnsupportedClaims(facts map[string]any, blockTexts map[string]string) Report {
	report := Report{Collections: make(map[string]CollectionClaims)}
	var examples []string
	for collection, fields := range ClaimTextFields {
		counts := CollectionClaims{}
		for _, entry := range entries(facts[collection]) {
			quotes := validQuotes(entry, blockTexts)
			for _, field := range fields {
				value, ok := entry[field].(string)
				if !ok || strings.TrimSpace(value) == "" {
					continue
				}
				counts.Total++
				if len(quotes) == 0 {
					counts.InvalidCitations++
					examples = append(examples, collection+"."+field+": "+value)
				} else if !strings.Contains(strings.ToLower(strings.Join(quotes, "\n")), strings.ToLower(value)) {
					counts.UngroundedValues++
					examples = append(examples, collection+"."+field+": "+value)
				}
			}
		}
		if counts.Total > 0 {
			report.Collections[collection] = counts
		}
	}
	if len(examples) > 20 {
		examples = examples[:20]
	}
	report.Examples = examples
	return report
}

func validQuotes(entry map[string]any, blockTexts map[string]string) []string {
	raw, ok := entry["evidence"].([]any)
	if !ok {
		return nil
	}
	var quotes []string
	for _, item := range raw {
		citation, ok := item.(map[string]any)
		if !ok {
			continue
		}
		quote, _ := citation["quote"].(string)
		blockID, _ := citation["blockId"].(string)
		blockText, ok := blockTexts[blockID]
		if quote == "" || !ok || !strings.Contains(blockText, quote) {
			continue
		}
		quotes = append(quotes, quote)
	}
	return quotes
}

func entries(value any) []map[string]any {
	switch typed := value.(type) {
	case map[string]any:
		return []map[string]any{typed}
	case []any:
		var out []map[string]any
		for _, item := range typed {
			if entry, ok := item.(map[string]any); ok {
				out = append(out, entry)
			}
		}
		return out
	default:
		return nil
	}
}
