// Package independent builds the private general-evaluation report:
// a readiness score plus evidence-grounded suggestions.
package independent

import (
	"sort"
	"strings"

	"github.com/skillsignal/worker/internal/compiler"
	"github.com/skillsignal/worker/internal/evaluator"
	"github.com/skillsignal/worker/internal/vocab"
)

// MaxListedGaps caps role-specific gap names in one suggestion.
const MaxListedGaps = 8

// Suggestion is one report tip.
type Suggestion struct {
	Title  string
	Detail string
}

// Report returns the readiness score and suggestions.
func Report(normalizedFacts map[string]any, jobDescription string) (int, []map[string]any) {
	contact, _ := normalizedFacts["contact"].(map[string]any)
	skills := DocumentedSkillNames(normalizedFacts)
	suggestions := ReadinessSuggestions(contact, skills, normalizedFacts)
	if jobDescription != "" {
		suggestions = append(suggestions, RoleAlignmentSuggestions(jobDescription, skills)...)
	}
	return ReadinessScore(contact, skills, normalizedFacts), suggestions
}

// RoleReport evaluates job-specific typed criteria and returns evidence
// separately from the general resume-quality score.
func RoleReport(jobDescription string, facts map[string]any) (*int, int, []map[string]any, []map[string]any) {
	if strings.TrimSpace(jobDescription) == "" {
		return nil, 0, []map[string]any{}, []map[string]any{}
	}
	artifact := compiler.CompileJobDescription(jobDescription, nil, false, "")
	requirements := make([]map[string]any, 0)
	for _, entry := range artifact["requirements"].([]any) {
		requirement := entry.(map[string]any)
		requirement["id"] = requirement["stableId"]
		requirement["kind"] = requirement["suggestedKind"]
		requirement["weight"] = requirement["suggestedWeight"]
		requirements = append(requirements, requirement)
	}
	if len(requirements) == 0 {
		return nil, 0, requirements, []map[string]any{}
	}
	result := evaluator.Evaluate(facts, requirements)
	return result.Score, result.EvidenceCoverage, requirements, AssessmentMaps(result.Assessments)
}

// AssessmentMaps gives private reports the same criterion outcomes as employer reports.
func AssessmentMaps(items []evaluator.Assessment) []map[string]any {
	assessments := make([]map[string]any, 0, len(items))
	for _, item := range items {
		evidence := make([]map[string]any, 0, len(item.Evidence))
		for _, reference := range item.Evidence {
			evidence = append(evidence, map[string]any{"blockId": reference.BlockID, "quote": reference.Quote})
		}
		assessments = append(assessments, map[string]any{
			"requirementId": item.RequirementID, "outcome": item.Outcome,
			"confidence": item.Confidence, "reasoning": item.Reasoning,
			"evidence": evidence,
		})
	}
	return assessments
}

// ReadinessSuggestions flags missing contact, skill, employment, and
// education evidence.
func ReadinessSuggestions(contact map[string]any, skills map[string]struct{}, facts map[string]any) []map[string]any {
	var suggestions []map[string]any
	if strOf(contact, "name") == "" {
		suggestions = append(suggestions, map[string]any{
			"title": "Add a clear name", "detail": "Start the resume with your name."})
	}
	if strOf(contact, "email") == "" {
		suggestions = append(suggestions, map[string]any{
			"title": "Add contact details", "detail": "Include a professional email address."})
	}
	if len(skills) == 0 {
		suggestions = append(suggestions, map[string]any{
			"title":  "Make skills easier to verify",
			"detail": "Name the tools and technologies you used in your experience."})
	}
	if len(entriesOf(facts["employment"])) == 0 {
		suggestions = append(suggestions, map[string]any{
			"title":  "Document your experience",
			"detail": "List each role with the employer, title, and dates so your experience can be verified."})
	}
	if len(entriesOf(facts["education"])) == 0 && len(entriesOf(facts["certifications"])) == 0 {
		suggestions = append(suggestions, map[string]any{
			"title":  "Add education or certifications",
			"detail": "Include degrees or credentials with their issuing institutions and dates."})
	}
	return suggestions
}

// RoleAlignmentSuggestions compares documented skills with the description.
func RoleAlignmentSuggestions(jobDescription string, skills map[string]struct{}) []map[string]any {
	mentioned, err := vocab.MentionedSkills(jobDescription)
	if err != nil {
		return nil
	}
	var missing []string
	for name := range mentioned {
		if _, ok := skills[name]; !ok {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	if len(missing) == 0 {
		return []map[string]any{{
			"title":  "Connect experience to the role",
			"detail": "Use specific outcomes to show how your documented skills apply to this role.",
		}}
	}
	if len(missing) > MaxListedGaps {
		missing = missing[:MaxListedGaps]
	}
	return []map[string]any{{
		"title":  "Review role-specific evidence",
		"detail": "The job description mentions " + strings.Join(missing, ", ") + ". Add them only when your resume already supports the claim.",
	}}
}

// ReadinessScore uses documented skills and history without identity fields.
func ReadinessScore(contact map[string]any, skills map[string]struct{}, facts map[string]any) int {
	score := 20
	breadth := len(skills) * 10
	if breadth > 50 {
		breadth = 50
	}
	score += breadth
	if len(entriesOf(facts["employment"])) > 0 {
		score += 15
	}
	if len(entriesOf(facts["education"])) > 0 {
		score += 10
	}
	if len(entriesOf(facts["certifications"])) > 0 {
		score += 5
	}
	if score > 100 {
		score = 100
	}
	return score
}

// DocumentedSkillNames collects canonical skill names from facts.
func DocumentedSkillNames(facts map[string]any) map[string]struct{} {
	names := make(map[string]struct{})
	for _, item := range entriesOf(facts["skills"]) {
		if name, ok := item["canonicalName"].(string); ok && name != "" {
			names[name] = struct{}{}
		}
	}
	return names
}

func strOf(item map[string]any, field string) string {
	if item == nil {
		return ""
	}
	text, _ := item[field].(string)
	return text
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
