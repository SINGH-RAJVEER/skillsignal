// Package schemas validates model-produced JSON against the versioned
// resume-fact, assessment, and requirement-draft contracts.
//
// Validation is structural (types, bounds, patterns, enums) plus the
// evidence-grounding pass; unknown extra keys are ignored rather than
// rejected, while every value that survives must cite exact block quotes.
package schemas

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Outcome values for assessed requirements.
const (
	OutcomeMet     = "met"
	OutcomePartial = "partial"
	OutcomeNotMet  = "not_met"
	OutcomeUnknown = "unknown"
)

var datePattern = regexp.MustCompile(`^\d{4}(-\d{2})?$`)

// ModelError marks schema or grounding failures.
type ModelError struct{ Message string }

func (e *ModelError) Error() string { return e.Message }

// EvidenceQuote cites one exact block passage.
type EvidenceQuote struct {
	BlockID string
	Quote   string
}

func decodeQuote(raw any) (EvidenceQuote, error) {
	item, ok := raw.(map[string]any)
	if !ok {
		return EvidenceQuote{}, &ModelError{Message: "evidence entry is not an object"}
	}
	blockID, _ := item["blockId"].(string)
	quote, _ := item["quote"].(string)
	if len(blockID) < 1 || len(blockID) > 128 || len(quote) < 1 || len(quote) > 2000 {
		return EvidenceQuote{}, &ModelError{Message: "evidence quote bounds violated"}
	}
	return EvidenceQuote{BlockID: blockID, Quote: quote}, nil
}

func decodeQuotes(raw any, min, max int) ([]EvidenceQuote, error) {
	list, ok := raw.([]any)
	if !ok {
		return nil, &ModelError{Message: "evidence is not a list"}
	}
	if len(list) < min || len(list) > max {
		return nil, &ModelError{Message: "evidence length out of bounds"}
	}
	quotes := make([]EvidenceQuote, 0, len(list))
	for _, item := range list {
		quote, err := decodeQuote(item)
		if err != nil {
			return nil, err
		}
		quotes = append(quotes, quote)
	}
	return quotes, nil
}

func boundedString(item map[string]any, field string, min, max int, required bool) (string, error) {
	raw, present := item[field]
	if !present || raw == nil {
		if required {
			return "", &ModelError{Message: fmt.Sprintf("%s is required", field)}
		}
		return "", nil
	}
	text, ok := raw.(string)
	if !ok {
		return "", &ModelError{Message: fmt.Sprintf("%s is not a string", field)}
	}
	if len(text) < min || len(text) > max {
		return "", &ModelError{Message: fmt.Sprintf("%s length out of bounds", field)}
	}
	return text, nil
}

func optionalBounded(item map[string]any, field string, max int) (*string, error) {
	raw, present := item[field]
	if !present || raw == nil {
		return nil, nil
	}
	text, ok := raw.(string)
	if !ok {
		return nil, &ModelError{Message: fmt.Sprintf("%s is not a string", field)}
	}
	if len(text) > max {
		return nil, &ModelError{Message: fmt.Sprintf("%s length out of bounds", field)}
	}
	return &text, nil
}

func optionalDate(item map[string]any, field string) (*string, error) {
	value, err := optionalBounded(item, field, 16)
	if err != nil {
		return nil, err
	}
	if value != nil && !datePattern.MatchString(*value) {
		return nil, &ModelError{Message: fmt.Sprintf("%s is not a valid date", field)}
	}
	return value, nil
}

// Contact holds nullable contact facts with evidence.
type Contact struct {
	Name     *string
	Email    *string
	Phone    *string
	Location *string
	Evidence []EvidenceQuote
}

// SkillFact is one model-extracted skill.
type SkillFact struct {
	CanonicalName string
	SourceText    string
	Evidence      []EvidenceQuote
}

// EmploymentFact is one model-extracted role.
type EmploymentFact struct {
	Employer  *string
	Title     *string
	StartDate *string
	EndDate   *string
	IsCurrent bool
	Evidence  []EvidenceQuote
}

// EducationFact is one model-extracted education entry.
type EducationFact struct {
	Institution  *string
	Degree       *string
	FieldOfStudy *string
	Graduation   *string
	Evidence     []EvidenceQuote
}

// CertificationFact is one model-extracted certification.
type CertificationFact struct {
	Name     string
	Issuer   *string
	Evidence []EvidenceQuote
}

// Suggestion is one model-authored improvement note.
type Suggestion struct {
	Title    string
	Detail   string
	Evidence []EvidenceQuote
}

// ResumeExtraction is the validated resume-facts artifact.
type ResumeExtraction struct {
	Contact        Contact
	Skills         []map[string]any
	Employment     []map[string]any
	Education      []map[string]any
	Certifications []map[string]any
	Suggestions    []map[string]any
	Warnings       []string
}

// ValidateResumeExtraction checks the schema version and per-field bounds.
func ValidateResumeExtraction(raw map[string]any) (ResumeExtraction, error) {
	var out ResumeExtraction
	if version, _ := raw["schemaVersion"].(string); version != "3" {
		return out, &ModelError{Message: "resume facts schema version mismatch"}
	}
	contactRaw, ok := raw["contact"].(map[string]any)
	if !ok {
		return out, &ModelError{Message: "contact is not an object"}
	}
	contact := Contact{}
	var err error
	if contact.Name, err = optionalBounded(contactRaw, "name", 256); err != nil {
		return out, err
	}
	if contact.Email, err = optionalBounded(contactRaw, "email", 320); err != nil {
		return out, err
	}
	if contact.Phone, err = optionalBounded(contactRaw, "phone", 64); err != nil {
		return out, err
	}
	if contact.Location, err = optionalBounded(contactRaw, "location", 256); err != nil {
		return out, err
	}
	contact.Evidence, err = decodeQuotes(contactRaw["evidence"], 0, 20)
	if err != nil {
		return out, err
	}
	out.Contact = contact
	if out.Skills, err = decodeFactList(raw["skills"], 0, 200); err != nil {
		return out, err
	}
	if out.Employment, err = decodeFactList(raw["employment"], 0, 100); err != nil {
		return out, err
	}
	if out.Education, err = decodeFactList(raw["education"], 0, 50); err != nil {
		return out, err
	}
	if out.Certifications, err = decodeFactList(raw["certifications"], 0, 100); err != nil {
		return out, err
	}
	suggestions, ok := raw["suggestions"].([]any)
	if !ok && raw["suggestions"] != nil {
		return out, &ModelError{Message: "suggestions is not a list"}
	}
	if len(suggestions) > 10 {
		return out, &ModelError{Message: "suggestions length out of bounds"}
	}
	for _, item := range suggestions {
		entry, ok := item.(map[string]any)
		if !ok {
			return out, &ModelError{Message: "suggestion is not an object"}
		}
		if _, err := boundedString(entry, "title", 1, 200, true); err != nil {
			return out, err
		}
		if _, err := boundedString(entry, "detail", 1, 1000, true); err != nil {
			return out, err
		}
		if _, err := decodeQuotes(entry["evidence"], 1, 20); err != nil {
			return out, err
		}
		out.Suggestions = append(out.Suggestions, entry)
	}
	warnings, ok := raw["warnings"].([]any)
	if !ok && raw["warnings"] != nil {
		return out, &ModelError{Message: "warnings is not a list"}
	}
	if len(warnings) > 50 {
		return out, &ModelError{Message: "warnings length out of bounds"}
	}
	for _, item := range warnings {
		text, ok := item.(string)
		if !ok {
			return out, &ModelError{Message: "warning is not a string"}
		}
		out.Warnings = append(out.Warnings, text)
	}
	// Per-entry bounds mirror the pydantic field limits.
	for _, skill := range out.Skills {
		if _, err := boundedString(skill, "canonicalName", 1, 128, true); err != nil {
			return out, err
		}
		if _, err := boundedString(skill, "sourceText", 1, 1000, true); err != nil {
			return out, err
		}
	}
	for _, job := range out.Employment {
		if _, err := optionalBounded(job, "employer", 256); err != nil {
			return out, err
		}
		if _, err := optionalBounded(job, "title", 256); err != nil {
			return out, err
		}
		if _, err := optionalDate(job, "startDate"); err != nil {
			return out, err
		}
		if _, err := optionalDate(job, "endDate"); err != nil {
			return out, err
		}
		if _, ok := job["isCurrent"].(bool); !ok {
			return out, &ModelError{Message: "isCurrent is not a boolean"}
		}
	}
	for _, entry := range out.Education {
		if _, err := optionalBounded(entry, "institution", 256); err != nil {
			return out, err
		}
		if _, err := optionalBounded(entry, "degree", 256); err != nil {
			return out, err
		}
		if _, err := optionalBounded(entry, "fieldOfStudy", 256); err != nil {
			return out, err
		}
		if _, err := optionalDate(entry, "graduationDate"); err != nil {
			return out, err
		}
	}
	for _, entry := range out.Certifications {
		if _, err := boundedString(entry, "name", 1, 256, true); err != nil {
			return out, err
		}
		if _, err := optionalBounded(entry, "issuer", 256); err != nil {
			return out, err
		}
	}
	return out, nil
}

func decodeFactList(raw any, min, max int) ([]map[string]any, error) {
	if raw == nil {
		return nil, nil
	}
	list, ok := raw.([]any)
	if !ok {
		return nil, &ModelError{Message: "fact collection is not a list"}
	}
	if len(list) < min || len(list) > max {
		return nil, &ModelError{Message: "fact collection length out of bounds"}
	}
	out := make([]map[string]any, 0, len(list))
	for _, item := range list {
		entry, ok := item.(map[string]any)
		if !ok {
			return nil, &ModelError{Message: "fact entry is not an object"}
		}
		out = append(out, entry)
	}
	return out, nil
}

// AssessedRequirement is one validated model assessment.
type AssessedRequirement struct {
	RequirementID string
	Outcome       string
	Confidence    float64
	Reasoning     string
	Evidence      []EvidenceQuote
}

// ValidateAssessmentOutput checks ids, outcomes, confidence, and evidence.
func ValidateAssessmentOutput(raw map[string]any) ([]AssessedRequirement, error) {
	list, ok := raw["assessments"].([]any)
	if !ok {
		return nil, &ModelError{Message: "assessments is not a list"}
	}
	if len(list) > 200 {
		return nil, &ModelError{Message: "assessments length out of bounds"}
	}
	out := make([]AssessedRequirement, 0, len(list))
	for _, item := range list {
		entry, ok := item.(map[string]any)
		if !ok {
			return nil, &ModelError{Message: "assessment is not an object"}
		}
		id, err := boundedString(entry, "requirementId", 1, 128, true)
		if err != nil {
			return nil, err
		}
		outcome, _ := entry["outcome"].(string)
		switch outcome {
		case OutcomeMet, OutcomePartial, OutcomeNotMet, OutcomeUnknown:
		default:
			return nil, &ModelError{Message: "assessment outcome is invalid"}
		}
		confidence, ok := toFloat(entry["confidence"])
		if !ok || confidence < 0 || confidence > 1 {
			return nil, &ModelError{Message: "assessment confidence out of bounds"}
		}
		reasoning, err := boundedString(entry, "reasoning", 1, 2000, true)
		if err != nil {
			return nil, err
		}
		evidence, err := decodeQuotes(entry["evidence"], 0, 20)
		if err != nil {
			return nil, err
		}
		out = append(out, AssessedRequirement{
			RequirementID: id, Outcome: outcome, Confidence: confidence,
			Reasoning: reasoning, Evidence: evidence,
		})
	}
	return out, nil
}

func toFloat(value any) (float64, bool) {
	switch number := value.(type) {
	case float64:
		return number, true
	case int:
		return float64(number), true
	case json.Number:
		parsed, err := number.Float64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

// RequirementDraft is one validated model requirement draft.
type RequirementDraft struct {
	NormalizedText string
	Category       string
	SuggestedKind  string
	SourceModality string
	Assessability  string
	Predicate      map[string]any
	Evidence       []EvidenceQuote
	Confidence     float64
}

var draftEnums = map[string]map[string]bool{
	"category": {
		"skill": true, "experience": true, "education": true, "certification": true,
		"language": true, "location": true, "work_authorization": true,
		"schedule": true, "soft_skill": true, "other": true,
	},
	"suggestedKind": {"required": true, "preferred": true, "ignored": true},
	"sourceModality": {
		"explicit_required": true, "explicit_preferred": true,
		"section_required": true, "section_preferred": true, "unclear": true,
	},
	"assessability": {
		"resume_evidence": true, "candidate_attestation": true,
		"recruiter_review": true, "prohibited": true, "unclear": true,
	},
}

// ValidateRequirementExtraction checks draft requirements and warnings.
func ValidateRequirementExtraction(raw map[string]any) ([]RequirementDraft, []string, error) {
	list, ok := raw["requirements"].([]any)
	if !ok {
		return nil, nil, &ModelError{Message: "requirements is not a list"}
	}
	if len(list) > 100 {
		return nil, nil, &ModelError{Message: "requirements length out of bounds"}
	}
	var drafts []RequirementDraft
	for _, item := range list {
		entry, ok := item.(map[string]any)
		if !ok {
			return nil, nil, &ModelError{Message: "requirement is not an object"}
		}
		text, err := boundedString(entry, "normalizedText", 3, 2000, true)
		if err != nil {
			return nil, nil, err
		}
		draft := RequirementDraft{NormalizedText: text}
		for _, field := range []string{"category", "suggestedKind", "sourceModality", "assessability"} {
			value, _ := entry[field].(string)
			if !draftEnums[field][value] {
				return nil, nil, &ModelError{Message: fmt.Sprintf("requirement %s is invalid", field)}
			}
			switch field {
			case "category":
				draft.Category = value
			case "suggestedKind":
				draft.SuggestedKind = value
			case "sourceModality":
				draft.SourceModality = value
			case "assessability":
				draft.Assessability = value
			}
		}
		predicate, ok := entry["predicate"].(map[string]any)
		if !ok {
			return nil, nil, &ModelError{Message: "requirement predicate is not an object"}
		}
		operator, _ := predicate["operator"].(string)
		if operator != "all_of" && operator != "any_of" {
			return nil, nil, &ModelError{Message: "predicate operator is invalid"}
		}
		criteria, ok := predicate["criteria"].([]any)
		if !ok || len(criteria) < 1 || len(criteria) > 30 {
			return nil, nil, &ModelError{Message: "predicate criteria out of bounds"}
		}
		for _, rawCriterion := range criteria {
			criterion, ok := rawCriterion.(map[string]any)
			if !ok {
				return nil, nil, &ModelError{Message: "predicate criterion is not an object"}
			}
			criterionType, _ := criterion["type"].(string)
			switch criterionType {
			case "skill", "experience", "education", "certification", "other":
			default:
				return nil, nil, &ModelError{Message: "criterion type is invalid"}
			}
		}
		draft.Predicate = predicate
		draft.Evidence, err = decodeQuotes(entry["evidence"], 1, 10)
		if err != nil {
			return nil, nil, err
		}
		confidence, ok := toFloat(entry["confidence"])
		if !ok || confidence < 0 || confidence > 1 {
			return nil, nil, &ModelError{Message: "draft confidence out of bounds"}
		}
		draft.Confidence = confidence
		drafts = append(drafts, draft)
	}
	var warnings []string
	if rawWarnings, ok := raw["warnings"].([]any); ok {
		if len(rawWarnings) > 50 {
			return nil, nil, &ModelError{Message: "warnings length out of bounds"}
		}
		for _, item := range rawWarnings {
			text, ok := item.(string)
			if !ok {
				return nil, nil, &ModelError{Message: "warning is not a string"}
			}
			warnings = append(warnings, text)
		}
	} else if raw["warnings"] != nil {
		return nil, nil, &ModelError{Message: "warnings is not a list"}
	}
	return drafts, warnings, nil
}

// ValidEvidenceQuote reports whether a quote exists verbatim in its block.
func ValidEvidenceQuote(evidence map[string]any, blockTexts map[string]string) bool {
	blockID, _ := evidence["blockId"].(string)
	quote, _ := evidence["quote"].(string)
	if quote == "" {
		return false
	}
	blockText, ok := blockTexts[blockID]
	return ok && strings.Contains(blockText, quote)
}

// ValidateFactEvidence keeps a fact only when its evidence is valid and
// every checked text field occurs inside the cited quotes.
func ValidateFactEvidence(fact map[string]any, blockTexts map[string]string, textFields []string) map[string]any {
	raw, ok := fact["evidence"].([]any)
	if !ok {
		return nil
	}
	var evidence []map[string]any
	for _, item := range raw {
		entry, ok := item.(map[string]any)
		if !ok || !ValidEvidenceQuote(entry, blockTexts) {
			continue
		}
		evidence = append(evidence, entry)
	}
	if len(evidence) == 0 {
		return nil
	}
	var cited strings.Builder
	for _, entry := range evidence {
		quote, _ := entry["quote"].(string)
		cited.WriteString(quote)
		cited.WriteString("\n")
	}
	citedText := strings.ToLower(cited.String())
	for _, field := range textFields {
		if value, ok := fact[field].(string); ok && strings.TrimSpace(value) != "" {
			if !strings.Contains(citedText, strings.ToLower(value)) {
				return nil
			}
		}
	}
	grounded := make(map[string]any, len(fact)+1)
	for key, value := range fact {
		grounded[key] = value
	}
	converted := make([]any, 0, len(evidence))
	for _, entry := range evidence {
		converted = append(converted, entry)
	}
	grounded["evidence"] = converted
	return grounded
}

// ValidateContactEvidence grounds contact fields, nulling unquoted values.
func ValidateContactEvidence(contact map[string]any, blockTexts map[string]string) (map[string]any, int) {
	grounded := ValidateFactEvidence(contact, blockTexts, nil)
	var evidence []map[string]any
	if grounded != nil {
		if list, ok := grounded["evidence"].([]any); ok {
			for _, item := range list {
				if entry, ok := item.(map[string]any); ok {
					evidence = append(evidence, entry)
				}
			}
		}
	}
	var cited strings.Builder
	for _, entry := range evidence {
		quote, _ := entry["quote"].(string)
		cited.WriteString(quote)
		cited.WriteString("\n")
	}
	citedText := strings.ToLower(cited.String())
	result := make(map[string]any, len(contact)+1)
	for key, value := range contact {
		result[key] = value
	}
	converted := make([]any, 0, len(evidence))
	for _, entry := range evidence {
		converted = append(converted, entry)
	}
	result["evidence"] = converted
	dropped := 0
	for _, field := range []string{"name", "email", "phone", "location"} {
		value, present := result[field]
		if !present || value == nil {
			continue
		}
		text, ok := value.(string)
		if !ok {
			continue
		}
		if !strings.Contains(citedText, strings.ToLower(text)) {
			result[field] = nil
			dropped++
		}
	}
	return result, dropped
}
