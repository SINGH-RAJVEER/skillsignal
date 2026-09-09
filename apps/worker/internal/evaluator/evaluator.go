// Package evaluator scores resume submissions against confirmed job
// requirements with typed deterministic predicates. Missing evidence is
// unknown, never failure.
package evaluator

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/skillsignal/worker/internal/intervals"
	"github.com/skillsignal/worker/internal/vocab"
)

var yearsPattern = regexp.MustCompile(`(?i)(\d+)\s*\+?\s*years?`)

var educationLevels = []struct {
	level string
	words []string
}{
	{"doctorate", []string{"phd", "doctorate", "d.litt", "dphil"}},
	{"master", []string{"master", "m.tech", "mtech", "m.sc", "msc", "mba", "m.a.", "m.a"}},
	{"bachelor", []string{"bachelor", "b.tech", "btech", "b.sc", "bsc", "b.e.", "b.a."}},
}

// Evidence cites one supporting passage.
type Evidence struct {
	BlockID string
	Quote   string
}

// Assessment is one deterministic requirement verdict.
type Assessment struct {
	RequirementID string
	Outcome       string
	Confidence    float64
	Reasoning     string
	Evidence      []Evidence
}

// Result aggregates assessments into score, coverage, and eligibility.
type Result struct {
	Assessments      []Assessment
	Score            *int
	EvidenceCoverage int
	Eligibility      string
}

// Evaluate assesses every requirement against normalized facts.
func Evaluate(normalizedFacts map[string]any, requirements []map[string]any) Result {
	skills := skillBlockIDs(normalizedFacts)
	assessments := make([]Assessment, 0, len(requirements))
	for _, requirement := range requirements {
		assessments = append(assessments, AssessRequirement(requirement, skills, normalizedFacts))
	}
	return Summarize(assessments, requirements)
}

func skillBlockIDs(facts map[string]any) map[string][]string {
	out := make(map[string][]string)
	for _, item := range factEntries(facts["skills"]) {
		name, _ := item["canonicalName"].(string)
		if name == "" {
			continue
		}
		var ids []string
		for _, raw := range stringList(item["evidenceBlockIds"]) {
			ids = append(ids, raw)
		}
		out[name] = ids
	}
	return out
}

// Summarize weights confident outcomes and resolves hard-gate eligibility.
func Summarize(assessments []Assessment, requirements []map[string]any) Result {
	type pair struct {
		assessment  Assessment
		requirement map[string]any
	}
	var scored []pair
	for i := range assessments {
		if i >= len(requirements) {
			break
		}
		if kindOf(requirements[i]) == "ignored" || kindOf(requirements[i]) == "hard_gate" {
			continue
		}
		scored = append(scored, pair{assessments[i], requirements[i]})
	}
	var known []pair
	for _, item := range scored {
		if item.assessment.Outcome != "unknown" {
			known = append(known, item)
		}
	}
	var score *int
	denominator := 0
	numerator := 0.0
	for _, item := range known {
		weight := weightOf(item.requirement)
		denominator += weight
		numerator += float64(weight) * outcomeValue(item.assessment.Outcome)
	}
	if denominator > 0 {
		value := RoundHalfEven(100 * numerator / float64(denominator))
		score = &value
	}
	coverage := 100
	if len(scored) > 0 {
		coverage = RoundHalfEven(100 * float64(len(known)) / float64(len(scored)))
	}
	eligibility := "eligible"
	for i := range assessments {
		if i >= len(requirements) || kindOf(requirements[i]) != "hard_gate" {
			continue
		}
		switch assessments[i].Outcome {
		case "not_met":
			eligibility = "not_eligible"
		case "partial", "unknown":
			if eligibility == "eligible" {
				eligibility = "needs_review"
			}
		}
	}
	return Result{Assessments: assessments, Score: score, EvidenceCoverage: coverage, Eligibility: eligibility}
}

// RefineAssessments prefers model outcomes while never lowering evidenced
// deterministic matches.
func RefineAssessments(deterministic []Assessment, model []map[string]any, requirements []map[string]any) []Assessment {
	byRequirement := make(map[string]map[string]any)
	for _, item := range model {
		if id, ok := item["requirementId"].(string); ok {
			byRequirement[id] = item
		}
	}
	refined := make([]Assessment, 0, len(deterministic))
	for i, assessment := range deterministic {
		var requirement map[string]any
		if i < len(requirements) {
			requirement = requirements[i]
		}
		modelEntry := byRequirement[stringID(requirement, "id")]
		if modelEntry == nil {
			refined = append(refined, assessment)
			continue
		}
		outcome, _ := modelEntry["outcome"].(string)
		if assessment.Outcome == "met" && outcome != "met" {
			outcome = assessment.Outcome
		}
		reasoning, _ := modelEntry["reasoning"].(string)
		if reasoning == "" {
			reasoning = assessment.Reasoning
		}
		evidence := assessment.Evidence
		if raw, ok := modelEntry["evidence"].([]any); ok {
			var parsed []Evidence
			for _, item := range raw {
				if entry, ok := item.(map[string]any); ok {
					parsed = append(parsed, Evidence{
						BlockID: stringOf(entry, "blockId"),
						Quote:   stringOf(entry, "quote"),
					})
				}
			}
			if len(parsed) > 0 {
				evidence = parsed
			}
		}
		confidence := clampConfidence(modelEntry["confidence"])
		refined = append(refined, Assessment{
			RequirementID: assessment.RequirementID,
			Outcome:       outcome,
			Confidence:    confidence,
			Reasoning:     reasoning,
			Evidence:      evidence,
		})
	}
	return refined
}

// AssessRequirement evaluates one requirement deterministically.
func AssessRequirement(requirement map[string]any, skills map[string][]string, facts map[string]any) Assessment {
	id := stringOf(requirement, "id")
	if assessability, _ := requirement["assessability"].(string); assessability != "" && assessability != "resume_evidence" {
		return Assessment{RequirementID: id, Outcome: "unknown", Confidence: 0,
			Reasoning: "This requirement cannot be assessed from resume evidence."}
	}
	if predicate, ok := requirement["predicate"].(map[string]any); ok {
		if criteria, ok := predicate["criteria"].([]any); ok && len(criteria) > 0 {
			return assessPredicate(id, predicate, skills, facts)
		}
	}
	text := stringOf(requirement, "normalized_text")
	if text == "" {
		text = stringOf(requirement, "normalizedText")
	}
	requiredSkills, err := vocab.MentionedSkills(text)
	if err == nil && len(requiredSkills) > 0 {
		return assessSkills(id, requiredSkills, skills)
	}
	if facts == nil {
		facts = map[string]any{}
	}
	if match := yearsPattern.FindStringSubmatch(text); match != nil {
		return assessExperience(id, parseYears(match[1]), facts)
	}
	if cert, evidence := findCertification(text, facts); cert != "" {
		return Assessment{RequirementID: id, Outcome: "met", Confidence: 1,
			Reasoning: "Documented certification matches the requirement.", Evidence: evidence}
	}
	if outcome, reasoning, found := findEducationLevel(text, facts); found {
		return Assessment{RequirementID: id, Outcome: outcome, Confidence: 1, Reasoning: reasoning}
	}
	return Assessment{RequirementID: id, Outcome: "unknown", Confidence: 0, Reasoning: "No deterministic criterion match."}
}

func assessPredicate(requirementID string, predicate map[string]any, skills map[string][]string, facts map[string]any) Assessment {
	raw, _ := predicate["criteria"].([]any)
	var criteria []Assessment
	for _, item := range raw {
		criterion, ok := item.(map[string]any)
		if !ok {
			continue
		}
		criteria = append(criteria, assessCriterion(requirementID, criterion, skills, facts))
	}
	if len(criteria) == 0 {
		return Assessment{RequirementID: requirementID, Outcome: "unknown", Confidence: 0, Reasoning: "Requirement predicate is invalid."}
	}
	operator, _ := predicate["operator"].(string)
	return combineCriteria(requirementID, operator, criteria)
}

func assessCriterion(requirementID string, criterion map[string]any, skills map[string][]string, facts map[string]any) Assessment {
	criterionType, _ := criterion["type"].(string)
	switch criterionType {
	case "skill":
		name := strings.TrimSpace(stringOf(criterion, "canonicalName"))
		if blockIDs := skills[name]; len(blockIDs) > 0 {
			evidence := make([]Evidence, 0, len(blockIDs))
			for _, blockID := range blockIDs {
				evidence = append(evidence, Evidence{BlockID: blockID, Quote: name})
			}
			return Assessment{RequirementID: requirementID, Outcome: "met", Confidence: 1,
				Reasoning: "Documented evidence names " + name + ".", Evidence: evidence}
		}
		return Assessment{RequirementID: requirementID, Outcome: "unknown", Confidence: 0,
			Reasoning: "The resume does not establish whether the candidate has " + name + " experience."}
	case "experience":
		minimum, ok := intOf(criterion, "minimumMonths")
		if !ok {
			return Assessment{RequirementID: requirementID, Outcome: "unknown", Confidence: 0, Reasoning: "Experience threshold is invalid."}
		}
		if subjects, ok := criterion["subjects"].([]any); ok && len(subjects) > 0 {
			var names []string
			for _, subject := range subjects {
				if name, ok := subject.(string); ok {
					names = append(names, name)
				}
			}
			return assessSubjectExperience(requirementID, names, minimum, skills, facts)
		}
		return assessExperienceMonths(requirementID, minimum, facts)
	case "education":
		return assessEducationCriterion(requirementID, strings.TrimSpace(stringOf(criterion, "minimumLevel")), facts)
	case "certification":
		return assessCertificationCriterion(requirementID, strings.TrimSpace(stringOf(criterion, "canonicalName")), facts)
	default:
		return Assessment{RequirementID: requirementID, Outcome: "unknown", Confidence: 0, Reasoning: "This criterion needs evidence review."}
	}
}

func combineCriteria(requirementID, operator string, criteria []Assessment) Assessment {
	var outcomes []string
	var evidence []Evidence
	for _, criterion := range criteria {
		outcomes = append(outcomes, criterion.Outcome)
		evidence = append(evidence, criterion.Evidence...)
	}
	outcome := "unknown"
	reasoning := ""
	if operator == "any_of" {
		switch {
		case contains(outcomes, "met"):
			outcome, reasoning = "met", "At least one allowed path is documented."
		case contains(outcomes, "partial"):
			outcome, reasoning = "partial", "No allowed path can be established from the resume."
		case len(outcomes) > 0 && all(outcomes, "not_met"):
			outcome, reasoning = "not_met", "No allowed path can be established from the resume."
		default:
			outcome, reasoning = "unknown", "No allowed path can be established from the resume."
		}
	} else {
		switch {
		case len(outcomes) > 0 && all(outcomes, "met"):
			outcome, reasoning = "met", "All required parts are documented."
		case contains(outcomes, "not_met"):
			outcome, reasoning = "not_met", "The complete requirement cannot be established from the resume."
		case contains(outcomes, "met") || contains(outcomes, "partial"):
			outcome, reasoning = "partial", "Only part of the requirement is documented."
		default:
			outcome, reasoning = "unknown", "The complete requirement cannot be established from the resume."
		}
	}
	confidence := 0.0
	if len(criteria) > 0 {
		confidence = criteria[0].Confidence
		for _, criterion := range criteria[1:] {
			if criterion.Confidence < confidence {
				confidence = criterion.Confidence
			}
		}
	}
	if len(criteria) == 1 {
		return Assessment{RequirementID: requirementID, Outcome: outcome, Confidence: confidence,
			Reasoning: criteria[0].Reasoning, Evidence: evidence}
	}
	return Assessment{RequirementID: requirementID, Outcome: outcome, Confidence: confidence, Reasoning: reasoning, Evidence: evidence}
}

func assessSkills(requirementID string, required map[string]struct{}, skills map[string][]string) Assessment {
	names := vocab.SortedNames(required)
	var evidence []Evidence
	matched := 0
	for _, skill := range names {
		blockIDs, ok := skills[skill]
		if !ok || len(blockIDs) == 0 {
			continue
		}
		matched++
		for _, blockID := range blockIDs {
			evidence = append(evidence, Evidence{BlockID: blockID, Quote: skill})
		}
	}
	switch {
	case matched == len(names):
		return Assessment{RequirementID: requirementID, Outcome: "met", Confidence: 1, Reasoning: "All explicit skill evidence found.", Evidence: evidence}
	case matched > 0:
		return Assessment{RequirementID: requirementID, Outcome: "partial", Confidence: 1, Reasoning: "Some explicit skill evidence found.", Evidence: evidence}
	default:
		return Assessment{RequirementID: requirementID, Outcome: "unknown", Confidence: 0, Reasoning: "The resume does not establish whether the required skill is held."}
	}
}

func assessExperience(requirementID string, neededYears int, facts map[string]any) Assessment {
	return assessExperienceMonths(requirementID, neededYears*12, facts)
}

func assessExperienceMonths(requirementID string, neededMonths int, facts map[string]any) Assessment {
	total := employmentMonths(facts["employment"])
	if total == nil {
		return Assessment{RequirementID: requirementID, Outcome: "unknown", Confidence: 0,
			Reasoning: "No dated employment is documented to compute experience."}
	}
	months := *total
	outcome := "unknown"
	switch {
	case months >= neededMonths:
		outcome = "met"
	case months >= neededMonths/2:
		outcome = "partial"
	}
	confidence := 0.0
	if outcome != "unknown" {
		confidence = 1
	}
	reasoning := fmt.Sprintf("Documented employment totals %dy %dm against %g years required.", months/12, months%12, float64(neededMonths)/12)
	return Assessment{RequirementID: requirementID, Outcome: outcome, Confidence: confidence, Reasoning: reasoning}
}

func assessSubjectExperience(requirementID string, subjects []string, neededMonths int, skills map[string][]string, facts map[string]any) Assessment {
	total := datedSubjectMonths(subjects, skills, facts)
	if total == nil {
		return Assessment{RequirementID: requirementID, Outcome: "unknown", Confidence: 0,
			Reasoning: "The resume does not establish a dated duration for the required subject."}
	}
	months := *total
	outcome := "unknown"
	switch {
	case months >= neededMonths:
		outcome = "met"
	case months >= neededMonths/2:
		outcome = "partial"
	}
	confidence := 0.0
	if outcome != "unknown" {
		confidence = 1
	}
	seen := make(map[string]bool)
	var labels []string
	for _, subject := range subjects {
		label := strings.TrimSpace(subject)
		if label == "" {
			label = "the subject"
		}
		if !seen[label] {
			seen[label] = true
			labels = append(labels, label)
		}
	}
	reasoning := fmt.Sprintf("Dated evidence for "+strings.Join(labels, ", ")+" totals %dy %dm against %g years required.", months/12, months%12, float64(neededMonths)/12)
	return Assessment{RequirementID: requirementID, Outcome: outcome, Confidence: confidence, Reasoning: reasoning}
}

func datedSubjectMonths(subjects []string, skills map[string][]string, facts map[string]any) *int {
	v, err := vocab.Load()
	if err != nil {
		return nil
	}
	blocksBySkill := make(map[string]map[string]bool)
	for name, blockIDs := range skills {
		set := make(map[string]bool)
		for _, id := range blockIDs {
			set[id] = true
		}
		blocksBySkill[strings.ToLower(name)] = set
	}
	categoriesBySkill := make(map[string]string)
	for _, item := range factEntries(facts["skills"]) {
		name, _ := item["canonicalName"].(string)
		category, _ := item["category"].(string)
		if name != "" && category != "" {
			categoriesBySkill[strings.ToLower(name)] = strings.ToLower(category)
		}
	}
	matchedBlocks := make(map[string]bool)
	for _, subject := range subjects {
		key := strings.ToLower(strings.TrimSpace(subject))
		if key == "" {
			continue
		}
		canonical := key
		if resolved, found := v.CanonicalFor(key); found {
			canonical = strings.ToLower(resolved)
		}
		if blocks, ok := blocksBySkill[canonical]; ok {
			for id := range blocks {
				matchedBlocks[id] = true
			}
			continue
		}
		for nameKey, blockIDs := range blocksBySkill {
			if categoriesBySkill[nameKey] == key {
				for id := range blockIDs {
					matchedBlocks[id] = true
				}
			}
		}
	}
	if len(matchedBlocks) == 0 {
		return nil
	}
	var dated []any
	for _, entry := range factEntries(facts["employment"]) {
		cited := make(map[string]bool)
		if raw, ok := entry["evidence"].([]any); ok {
			for _, item := range raw {
				if citation, ok := item.(map[string]any); ok {
					if blockID, ok := citation["blockId"].(string); ok && blockID != "" {
						cited[blockID] = true
					}
				}
			}
		}
		for blockID := range matchedBlocks {
			if cited[blockID] {
				dated = append(dated, entry)
				break
			}
		}
	}
	spans := intervals.Employment(dated, time.Time{})
	if len(spans.Merged) == 0 {
		return nil
	}
	total := spans.TotalMonths()
	return &total
}

func employmentMonths(entries any) *int {
	spans := intervals.Employment(entries, time.Time{})
	if len(spans.Merged) == 0 {
		return nil
	}
	total := spans.TotalMonths()
	return &total
}

func findCertification(text string, facts map[string]any) (string, []Evidence) {
	list, ok := facts["certifications"].([]any)
	if !ok {
		return "", nil
	}
	lowered := " " + strings.ToLower(text) + " "
	for _, item := range list {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _ := entry["name"].(string)
		name = strings.TrimSpace(name)
		if len([]rune(name)) < 4 {
			continue
		}
		nameLower := strings.ToLower(name)
		if strings.Contains(strings.ToLower(text), nameLower) {
			return name, []Evidence{{BlockID: "facts", Quote: name}}
		}
		matched := false
		for _, token := range strings.Fields(nameLower) {
			if len([]rune(token)) >= 3 && strings.Contains(lowered, " "+token+" ") {
				matched = true
				break
			}
		}
		if matched {
			// Parentheses split words like the Python token check.
			_ = matched
			return name, []Evidence{{BlockID: "facts", Quote: name}}
		}
	}
	return "", nil
}

func findEducationLevel(text string, facts map[string]any) (string, string, bool) {
	list, ok := facts["education"].([]any)
	if !ok || len(list) == 0 {
		return "", "", false
	}
	lowered := strings.ToLower(text)
	var requested []string
	for _, level := range educationLevels {
		for _, word := range level.words {
			if strings.Contains(lowered, word) {
				requested = append(requested, level.level)
				break
			}
		}
	}
	if len(requested) == 0 && !strings.Contains(lowered, "degree") {
		return "", "", false
	}
	documented := make(map[string]bool)
	for _, item := range list {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		degree, _ := entry["degree"].(string)
		degree = strings.ToLower(degree)
		if degree == "" {
			continue
		}
		for _, level := range educationLevels {
			for _, word := range level.words {
				if strings.Contains(degree, word) {
					documented[level.level] = true
					break
				}
			}
		}
	}
	if len(requested) > 0 {
		for _, level := range requested {
			if documented[level] {
				return "met", "Documented education includes the requested level.", true
			}
		}
		if len(documented) > 0 {
			return "partial", "Education is documented but at a different level than requested.", true
		}
		return "not_met", "No matching education level is documented.", true
	}
	return "met", "A degree is documented.", true
}

func assessEducationCriterion(requirementID, minimumLevel string, facts map[string]any) Assessment {
	ranks := map[string]int{"bachelor": 1, "master": 2, "doctorate": 3}
	requested, ok := ranks[minimumLevel]
	if !ok {
		return Assessment{RequirementID: requirementID, Outcome: "unknown", Confidence: 0, Reasoning: "Education level is unclear."}
	}
	documented := documentedEducationLevels(facts)
	if len(documented) == 0 {
		return Assessment{RequirementID: requirementID, Outcome: "unknown", Confidence: 0, Reasoning: "No education level is documented."}
	}
	best := 0
	for _, level := range documented {
		if ranks[level] > best {
			best = ranks[level]
		}
	}
	if best >= requested {
		return Assessment{RequirementID: requirementID, Outcome: "met", Confidence: 1, Reasoning: "Documented education meets or exceeds the requested level."}
	}
	return Assessment{RequirementID: requirementID, Outcome: "partial", Confidence: 1, Reasoning: "Education is documented below the requested level."}
}

func documentedEducationLevels(facts map[string]any) []string {
	list, ok := facts["education"].([]any)
	if !ok {
		return nil
	}
	seen := make(map[string]bool)
	var out []string
	for _, item := range list {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		degree, _ := entry["degree"].(string)
		degree = strings.ToLower(degree)
		for _, level := range educationLevels {
			for _, word := range level.words {
				if strings.Contains(degree, word) && !seen[level.level] {
					seen[level.level] = true
					out = append(out, level.level)
					break
				}
			}
		}
	}
	return out
}

func assessCertificationCriterion(requirementID, name string, facts map[string]any) Assessment {
	list, ok := facts["certifications"].([]any)
	if !ok {
		return Assessment{RequirementID: requirementID, Outcome: "unknown", Confidence: 0, Reasoning: "No certifications are documented."}
	}
	normalized := normalizeCredentialName(name)
	for _, item := range list {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		documented, _ := entry["name"].(string)
		if normalized != "" && normalizeCredentialName(documented) == normalized {
			return Assessment{RequirementID: requirementID, Outcome: "met", Confidence: 1,
				Reasoning: "The required certification is documented.",
				Evidence:  []Evidence{{BlockID: "facts", Quote: documented}}}
		}
	}
	return Assessment{RequirementID: requirementID, Outcome: "unknown", Confidence: 0,
		Reasoning: "The resume does not establish whether the certification is held."}
}

func normalizeCredentialName(value string) string {
	var tokens []string
	current := ""
	flush := func() {
		if current != "" {
			tokens = append(tokens, current)
			current = ""
		}
	}
	for _, r := range strings.ToLower(value) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			current += string(r)
		} else {
			flush()
		}
	}
	flush()
	return strings.Join(tokens, " ")
}

func outcomeValue(outcome string) float64 {
	switch outcome {
	case "met":
		return 1
	case "partial":
		return 0.5
	default:
		return 0
	}
}

// RoundHalfEven matches Python round() for score computation.
func RoundHalfEven(value float64) int {
	floor := float64(int(value))
	fraction := value - floor
	switch {
	case fraction > 0.5 || (fraction == 0.5 && int(floor)%2 == 1):
		return int(floor) + 1
	case fraction < 0 && (fraction < -0.5 || (fraction == -0.5 && int(floor)%2 == 1)):
		return int(floor) - 1
	default:
		return int(floor)
	}
}

// OutcomeValue is the exported scoring weight.
func OutcomeValue(outcome string) float64 { return outcomeValue(outcome) }

func kindOf(requirement map[string]any) string {
	kind, _ := requirement["kind"].(string)
	return kind
}

func weightOf(requirement map[string]any) int {
	switch weight := requirement["weight"].(type) {
	case int:
		return weight
	case float64:
		return int(weight)
	default:
		return 0
	}
}

func stringOf(item map[string]any, field string) string {
	value, _ := item[field].(string)
	return value
}

func stringID(item map[string]any, field string) string {
	if item == nil {
		return ""
	}
	return stringOf(item, field)
}

func intOf(item map[string]any, field string) (int, bool) {
	switch value := item[field].(type) {
	case int:
		return value, true
	case float64:
		return int(value), value == float64(int(value))
	default:
		return 0, false
	}
}

func clampConfidence(value any) float64 {
	var confidence float64
	switch number := value.(type) {
	case float64:
		confidence = number
	case int:
		confidence = float64(number)
	default:
		return 0
	}
	if confidence < 0 {
		return 0
	}
	if confidence > 1 {
		return 1
	}
	return confidence
}

func factEntries(value any) []map[string]any {
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

func stringList(value any) []string {
	list, ok := value.([]any)
	if !ok {
		return nil
	}
	var out []string
	for _, item := range list {
		if text, ok := item.(string); ok {
			out = append(out, text)
		}
	}
	return out
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func all(values []string, target string) bool {
	for _, value := range values {
		if value != target {
			return false
		}
	}
	return true
}

func parseYears(text string) int {
	value := 0
	for _, r := range text {
		if r >= '0' && r <= '9' {
			value = value*10 + int(r-'0')
		}
	}
	return value
}
