// Package extractor validates model-extracted resume facts and assessments
// against evidence, and merges model output with deterministic facts.
package extractor

import (
	"fmt"
	"sort"
	"strings"

	"github.com/skillsignal/worker/internal/docparse"
	"github.com/skillsignal/worker/internal/schemas"
	"github.com/skillsignal/worker/internal/vocab"
)

// EvidenceTextFields lists grounded value fields per fact collection.
var EvidenceTextFields = map[string][]string{
	"skills":         {"sourceText"},
	"employment":     {"employer", "title"},
	"education":      {"institution", "degree", "fieldOfStudy"},
	"certifications": {"name", "issuer"},
	"suggestions":    {},
}

// BlocksDocument shapes blocks for the extraction prompt.
func BlocksDocument(blocks []parser.Block) string {
	var parts []string
	for _, block := range blocks {
		if strings.TrimSpace(block.Text) == "" {
			continue
		}
		parts = append(parts, "["+block.ID+"]\n"+block.Text)
	}
	return strings.Join(parts, "\n\n")
}

// BlockTexts indexes block id to text.
func BlockTexts(blocks []parser.Block) map[string]string {
	out := make(map[string]string, len(blocks))
	for _, block := range blocks {
		if block.ID != "" {
			out[block.ID] = block.Text
		}
	}
	return out
}

// ValidateExtraction schema-checks model output and drops ungrounded facts.
func ValidateExtraction(raw map[string]any, blockTexts map[string]string) (map[string]any, error) {
	extraction, err := schemas.ValidateResumeExtraction(raw)
	if err != nil {
		return nil, &schemas.ModelError{Message: "Model extraction does not match the schema"}
	}
	facts := map[string]any{
		"schemaVersion":  "3",
		"contact":        contactToMap(extraction.Contact),
		"skills":         toAnySlice(extraction.Skills),
		"employment":     toAnySlice(extraction.Employment),
		"education":      toAnySlice(extraction.Education),
		"certifications": toAnySlice(extraction.Certifications),
		"suggestions":    toAnySlice(extraction.Suggestions),
		"warnings":       toAnySliceStrings(extraction.Warnings),
	}
	warnings := append([]string{}, extraction.Warnings...)
	for field, textFields := range EvidenceTextFields {
		entries, _ := facts[field].([]any)
		var grounded []any
		for _, item := range entries {
			entry, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if validated := schemas.ValidateFactEvidence(entry, blockTexts, textFields); validated != nil {
				grounded = append(grounded, validated)
			}
		}
		if grounded == nil {
			grounded = []any{}
		}
		if dropped := len(entries) - len(grounded); dropped > 0 {
			warnings = append(warnings, fmt.Sprintf("%d %s lacked valid evidence", dropped, field))
		}
		facts[field] = grounded
	}
	contact, _ := facts["contact"].(map[string]any)
	groundedContact, droppedFields := schemas.ValidateContactEvidence(contact, blockTexts)
	facts["contact"] = groundedContact
	if droppedFields > 0 {
		warnings = append(warnings, fmt.Sprintf("%d contact fields lacked source evidence", droppedFields))
	}
	converted := make([]any, 0, len(warnings))
	for _, warning := range warnings {
		converted = append(converted, warning)
	}
	facts["warnings"] = converted
	return facts, nil
}

// ValidateAssessments schema-checks model assessments, degrading confirmed
// outcomes without evidence to unknown.
func ValidateAssessments(raw map[string]any, requirementIDs map[string]bool, blockTexts map[string]string) ([]map[string]any, error) {
	assessments, err := schemas.ValidateAssessmentOutput(raw)
	if err != nil {
		return nil, &schemas.ModelError{Message: "Model assessment does not match the schema"}
	}
	var valid []map[string]any
	for _, assessment := range assessments {
		if !requirementIDs[assessment.RequirementID] {
			continue
		}
		var evidence []any
		for _, quote := range assessment.Evidence {
			entry := map[string]any{"blockId": quote.BlockID, "quote": quote.Quote}
			if schemas.ValidEvidenceQuote(entry, blockTexts) {
				evidence = append(evidence, entry)
			}
		}
		outcome := assessment.Outcome
		confidence := assessment.Confidence
		if outcome != schemas.OutcomeUnknown && len(evidence) == 0 {
			outcome = schemas.OutcomeUnknown
			if confidence > 0.5 {
				confidence = 0.5
			}
		}
		convertedEvidence := make([]any, 0, len(evidence))
		convertedEvidence = append(convertedEvidence, evidence...)
		valid = append(valid, map[string]any{
			"requirementId": assessment.RequirementID,
			"outcome":       outcome,
			"confidence":    confidence,
			"reasoning":     assessment.Reasoning,
			"evidence":      convertedEvidence,
		})
	}
	return valid, nil
}

// MergeFacts combines deterministic skills/contact with model facts.
// Deterministic skills win name collisions; model skills resolve through
// the taxonomy; employment/education/certifications come from the model.
func MergeFacts(normalized map[string]any, extracted map[string]any, blocks []parser.Block) (map[string]any, error) {
	v, err := vocab.Load()
	if err != nil {
		return nil, err
	}
	order := make(map[string]int, len(blocks))
	for i, block := range blocks {
		order[block.ID] = i
	}
	merged := make(map[string]map[string]any)
	for _, item := range entriesOf(normalized["skills"]) {
		name, _ := item["canonicalName"].(string)
		if name == "" {
			continue
		}
		merged[strings.ToLower(name)] = map[string]any{
			"canonicalName":    name,
			"category":         item["category"],
			"evidenceBlockIds": item["evidenceBlockIds"],
		}
	}
	for _, item := range entriesOf(extracted["skills"]) {
		source, _ := item["canonicalName"].(string)
		source = strings.TrimSpace(source)
		if source == "" {
			continue
		}
		if _, exists := merged[strings.ToLower(source)]; exists {
			continue
		}
		canonical := source
		if resolved, found := v.CanonicalFor(source); found {
			canonical = resolved
		}
		key := strings.ToLower(canonical)
		if _, exists := merged[key]; exists {
			continue
		}
		var references []string
		seen := make(map[string]bool)
		for _, raw := range entriesOf(item["evidence"]) {
			if blockID, ok := raw["blockId"].(string); ok && blockID != "" && !seen[blockID] {
				seen[blockID] = true
				references = append(references, blockID)
			}
		}
		sort.Slice(references, func(i, j int) bool {
			oi, oki := order[references[i]]
			oj, okj := order[references[j]]
			if !oki {
				oi = len(order)
			}
			if !okj {
				oj = len(order)
			}
			return oi < oj
		})
		merged[key] = map[string]any{
			"canonicalName":    canonical,
			"category":         v.CategoryFor(canonical),
			"evidenceBlockIds": references,
		}
	}
	skills := make([]any, 0, len(merged))
	for _, skill := range merged {
		skills = append(skills, skill)
	}
	sort.Slice(skills, func(i, j int) bool {
		ai, _ := skills[i].(map[string]any)["canonicalName"].(string)
		bj, _ := skills[j].(map[string]any)["canonicalName"].(string)
		return strings.ToLower(ai) < strings.ToLower(bj)
	})
	contact, _ := normalized["contact"].(map[string]any)
	extractedContact, _ := extracted["contact"].(map[string]any)
	mergedContact := make(map[string]any)
	for _, field := range []string{"name", "email", "phone", "location"} {
		value := firstPresent(contact[field], extractedContact[field])
		mergedContact[field] = value
	}
	return map[string]any{
		"contact":        mergedContact,
		"skills":         skills,
		"employment":     entriesAsAny(extracted["employment"]),
		"education":      entriesAsAny(extracted["education"]),
		"certifications": entriesAsAny(extracted["certifications"]),
		"warnings":       stringsAsAny(extracted["warnings"]),
	}, nil
}

func firstPresent(values ...any) any {
	for _, value := range values {
		if text, ok := value.(string); ok && text != "" {
			return text
		}
		if value != nil {
			if _, ok := value.(string); !ok {
				return value
			}
		}
	}
	return nil
}

// MergeSuggestions dedupes deterministic and model suggestions by title.
func MergeSuggestions(deterministic, extracted []map[string]any, limit int) []map[string]any {
	merged := make(map[string]map[string]any)
	var order []string
	for _, suggestion := range append(append([]map[string]any{}, deterministic...), extracted...) {
		title, _ := suggestion["title"].(string)
		title = strings.TrimSpace(title)
		if title == "" {
			continue
		}
		key := strings.ToLower(title)
		if _, exists := merged[key]; !exists {
			merged[key] = map[string]any{"title": title, "detail": suggestion["detail"]}
			order = append(order, key)
		}
	}
	out := make([]map[string]any, 0, len(order))
	for _, key := range order {
		out = append(out, merged[key])
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

func entriesOf(value any) []map[string]any {
	list, ok := value.([]any)
	if !ok {
		return nil
	}
	var out []map[string]any
	for _, item := range list {
		if entry, ok := item.(map[string]any); ok {
			out = append(out, entry)
		}
	}
	return out
}

func entriesAsAny(value any) []any {
	var out []any
	for _, entry := range entriesOf(value) {
		out = append(out, entry)
	}
	if out == nil {
		return []any{}
	}
	return out
}

func stringsAsAny(value any) []any {
	list, ok := value.([]any)
	if !ok {
		return []any{}
	}
	var out []any
	for _, item := range list {
		if text, ok := item.(string); ok {
			out = append(out, text)
		}
	}
	if out == nil {
		return []any{}
	}
	return out
}

func toAnySlice(entries []map[string]any) []any {
	out := make([]any, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry)
	}
	return out
}

func toAnySliceStrings(items []string) []any {
	out := make([]any, 0, len(items))
	for _, item := range items {
		out = append(out, item)
	}
	return out
}

func strPtr(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func contactToMap(contact schemas.Contact) map[string]any {
	evidence := make([]any, 0, len(contact.Evidence))
	for _, quote := range contact.Evidence {
		evidence = append(evidence, map[string]any{"blockId": quote.BlockID, "quote": quote.Quote})
	}
	return map[string]any{
		"name": strPtr(contact.Name), "email": strPtr(contact.Email),
		"phone": strPtr(contact.Phone), "location": strPtr(contact.Location),
		"evidence": evidence,
	}
}
