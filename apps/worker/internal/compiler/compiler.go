// Package compiler turns job-description text into grounded draft
// requirements: deterministic taxonomy candidates plus validated model
// drafts, deduplicated with stable ids.
package compiler

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"golang.org/x/crypto/blake2b"
	"golang.org/x/text/unicode/norm"

	"github.com/skillsignal/go-core/versions"
	"github.com/skillsignal/worker/internal/schemas"
	"github.com/skillsignal/worker/internal/vocab"
)

// MaxDraftRequirements caps the review list.
const MaxDraftRequirements = 50

var headingSections = map[string]string{
	"requirements": "requirements", "qualifications": "requirements",
	"minimum qualifications": "requirements", "what you bring": "requirements",
	"what we are looking for": "requirements", "what we're looking for": "requirements",
	"preferred qualifications": "preferred", "preferred skills": "preferred",
	"nice to have": "preferred", "nice-to-have": "preferred", "bonus points": "preferred",
	"responsibilities": "responsibilities", "what you will do": "responsibilities",
	"what you'll do": "responsibilities", "about us": "about", "about the company": "about",
	"benefits": "benefits", "what we offer": "benefits",
	"equal opportunity": "legal", "how to apply": "application",
}

var (
	requiredCue   = regexp.MustCompile(`(?i)\b(must|required|minimum|need to|needs to|shall|essential)\b`)
	preferredCue  = regexp.MustCompile(`(?i)\b(preferred|nice[ -]to[ -]have|bonus|plus|desirable|ideally)\b`)
	yearsPattern  = regexp.MustCompile(`(?i)\b(\d{1,2})\s*\+?\s*(?:years?|yrs?)\b`)
	wordYears     = regexp.MustCompile(`(?i)\b(one|two|three|four|five|six|seven|eight|nine|ten)\s+years?\b`)
	numberWords   = map[string]int{"one": 1, "two": 2, "three": 3, "four": 4, "five": 5, "six": 6, "seven": 7, "eight": 8, "nine": 9, "ten": 10}
	prohibitedPat = regexp.MustCompile(`(?i)\b(age|gender|sex|race|ethnicity|religion|marital status|pregnan(?:t|cy)|disability|nationality|national origin|health|veteran status)\b`)
	softSkillPat  = regexp.MustCompile(`(?i)\b(communication|team player|self[ -]starter|leadership style|interpersonal)\b`)
	certPat       = regexp.MustCompile(`(?i)\b(certification|certified|licen[cs]e|credential)\b`)
	bulletPat     = regexp.MustCompile(`^\s*(?:[-*•▪◦]+|\d+[.)])\s+`)
	orPattern     = regexp.MustCompile(`(?i)\bor\b`)
	trailingPunct = regexp.MustCompile(`[:\s]+$`)
)

var degreeLevels = []struct {
	level   string
	phrases []string
}{
	{"doctorate", []string{"phd", "doctorate", "doctoral", "dphil"}},
	{"master", []string{"master", "mba", "mtech", "m.tech", "msc", "m.sc"}},
	{"bachelor", []string{"bachelor", "undergraduate degree", "btech", "b.tech", "bsc", "b.sc"}},
}

var attestationPatterns = []struct {
	category string
	pattern  *regexp.Regexp
}{
	{"work_authorization", regexp.MustCompile(`(?i)\b(work authorization|authorized to work|visa sponsorship|right to work)\b`)},
	{"location", regexp.MustCompile(`(?i)\b(relocat(?:e|ion)|reside in|based in|commutable distance)\b`)},
	{"schedule", regexp.MustCompile(`(?i)\b(night shift|weekends?|on[ -]call|travel|time zone|working hours)\b`)},
}

// JobBlock is one source line with section context and offsets.
type JobBlock struct {
	ID          string
	Text        string
	Section     string
	StartOffset int
	EndOffset   int
	WasBullet   bool
}

// SourceBlocks splits description text into section-aware blocks.
func SourceBlocks(sourceText string) []JobBlock {
	var blocks []JobBlock
	section := "unknown"
	offset := 0
	for _, rawLine := range strings.SplitAfter(sourceText, "\n") {
		line := strings.TrimSuffix(strings.TrimSuffix(rawLine, "\n"), "\r")
		stripped := strings.TrimSpace(line)
		lineStart := offset
		offset += len(rawLine)
		if stripped == "" {
			continue
		}
		headingKey := strings.ToLower(trailingPunct.ReplaceAllString(stripped, ""))
		if mapped, ok := headingSections[headingKey]; ok && len([]rune(stripped)) <= 80 {
			section = mapped
			continue
		}
		var content string
		var wasBullet bool
		if match := bulletPat.FindString(line); match != "" {
			content = strings.TrimSpace(line[len(match):])
			wasBullet = true
		} else {
			content = strings.TrimSpace(line)
		}
		if len([]rune(content)) < 3 {
			continue
		}
		localStart := strings.Index(line, content)
		if localStart < 0 {
			localStart = 0
		}
		start := lineStart + localStart
		blocks = append(blocks, JobBlock{
			ID: fmt.Sprintf("jd-b%d", len(blocks)+1), Text: content,
			Section: section, StartOffset: start, EndOffset: start + len(content),
			WasBullet: wasBullet,
		})
	}
	return blocks
}

// BlocksForModel shapes blocks for the requirement-drafting prompt.
func BlocksForModel(blocks []JobBlock) []map[string]any {
	out := make([]map[string]any, 0, len(blocks))
	for _, block := range blocks {
		out = append(out, map[string]any{"id": block.ID, "section": block.Section, "text": block.Text})
	}
	return out
}

// CompileJobDescription merges deterministic and grounded model drafts.
func CompileJobDescription(sourceText string, modelOutput map[string]any, degraded bool, degradedReason string) map[string]any {
	blocks := SourceBlocks(sourceText)
	var warnings []string
	candidates := deterministicCandidates(blocks, &warnings)
	if modelOutput != nil {
		modelCandidates, modelWarnings := groundedModelCandidates(modelOutput, blocks)
		warnings = append(warnings, modelWarnings...)
		candidates = append(candidates, modelCandidates...)
		if len(modelCandidates) == 0 {
			degraded = true
			warnings = append(warnings, "Model extraction produced no grounded requirements")
		}
	}
	requirements := deduplicate(candidates, &warnings)
	if len(candidates) > MaxDraftRequirements {
		warnings = append(warnings, "Low-confidence requirements were omitted after the review limit")
	}
	if len(requirements) > MaxDraftRequirements {
		requirements = requirements[:MaxDraftRequirements]
	}
	if degradedReason != "" {
		warnings = append(warnings, degradedReason)
	}
	qualityState := "review_required"
	if len(requirements) > 0 && len(warnings) == 0 && !degraded {
		qualityState = "ready"
	}
	if len(requirements) == 0 {
		warnings = append(warnings, "No explicit job requirements were found; add criteria manually")
	}
	converted := make([]any, 0, len(requirements))
	for _, requirement := range requirements {
		converted = append(converted, requirement)
	}
	return map[string]any{
		"schemaVersion":   versions.JobRequirementsSchemaVersion,
		"compilerVersion": versions.JobRequirementsCompilerVersion,
		"promptVersion":   versions.JobRequirementsPromptVersion,
		"degraded":        degraded,
		"qualityState":    qualityState,
		"warnings":        uniqueStrings(warnings),
		"requirements":    converted,
	}
}

func deterministicCandidates(blocks []JobBlock, warnings *[]string) []map[string]any {
	var candidates []map[string]any
	for _, block := range blocks {
		text := normalizedStatement(block.Text)
		switch block.Section {
		case "about", "benefits", "legal", "application":
			continue
		}
		if prohibitedPat.MatchString(text) {
			*warnings = append(*warnings, fmt.Sprintf("Potentially prohibited criterion omitted from %s", block.ID))
			continue
		}
		skills, err := vocab.MentionedSkills(text)
		if err != nil {
			continue
		}
		sorted := vocab.SortedNames(skills)
		hasCue := requiredCue.MatchString(text) || preferredCue.MatchString(text)
		inRequirementSection := block.Section == "requirements" || block.Section == "preferred"
		if block.Section == "responsibilities" && !hasCue {
			continue
		}
		if !inRequirementSection && !hasCue && !(block.WasBullet && len(sorted) > 0) {
			continue
		}
		candidates = append(candidates, deterministicCandidate(block, text, sorted))
	}
	return candidates
}

func deterministicCandidate(block JobBlock, text string, skills []string) map[string]any {
	suggestedKind, sourceModality := inferImportance(block.Section, text)
	category, assessability := inferCategory(text, skills)
	predicate := inferPredicate(text, skills)
	evidence := []any{map[string]any{
		"blockId": block.ID, "quote": block.Text,
		"startOffset": block.StartOffset, "endOffset": block.EndOffset,
		"section": block.Section,
	}}
	var signals []string
	if block.Section == "requirements" || block.Section == "preferred" {
		signals = append(signals, "section")
	}
	if len(skills) > 0 {
		signals = append(signals, "taxonomy")
	}
	if requiredCue.MatchString(text) || preferredCue.MatchString(text) {
		signals = append(signals, "language_cue")
	}
	if len(signals) == 0 {
		signals = []string{"bullet"}
	}
	confidence := 0.55 + 0.12*float64(len(signals))
	if confidence > 0.92 {
		confidence = 0.92
	}
	weight := 1
	if suggestedKind == "required" {
		weight = 2
	}
	return finalizeCandidate(map[string]any{
		"normalizedText": text, "category": category,
		"suggestedKind": suggestedKind, "suggestedWeight": weight,
		"sourceModality": sourceModality, "assessability": assessability,
		"predicate": predicate, "evidence": evidence,
		"confidence": confidence, "signals": signals,
	})
}

func inferImportance(section, text string) (string, string) {
	if preferredCue.MatchString(text) {
		return "preferred", "explicit_preferred"
	}
	if requiredCue.MatchString(text) {
		return "required", "explicit_required"
	}
	if section == "preferred" {
		return "preferred", "section_preferred"
	}
	if section == "requirements" {
		return "required", "section_required"
	}
	return "preferred", "unclear"
}

func inferCategory(text string, skills []string) (string, string) {
	for _, attestation := range attestationPatterns {
		if attestation.pattern.MatchString(text) {
			return attestation.category, "candidate_attestation"
		}
	}
	if softSkillPat.MatchString(text) {
		return "soft_skill", "recruiter_review"
	}
	if experienceMonths(text) != nil {
		return "experience", "resume_evidence"
	}
	if educationLevel(text) != "" {
		return "education", "resume_evidence"
	}
	if certPat.MatchString(text) {
		return "certification", "resume_evidence"
	}
	if len(skills) > 0 {
		return "skill", "resume_evidence"
	}
	return "other", "unclear"
}

func inferPredicate(text string, skills []string) map[string]any {
	var criteria []any
	if months := experienceMonths(text); months != nil {
		criteria = append(criteria, map[string]any{
			"type": "experience", "canonicalName": nil,
			"minimumMonths": *months, "minimumLevel": nil, "subjects": skills,
		})
	} else if len(skills) > 0 {
		for _, skill := range skills {
			criteria = append(criteria, map[string]any{
				"type": "skill", "canonicalName": skill,
				"minimumMonths": nil, "minimumLevel": nil, "subjects": []any{},
			})
		}
	}
	if level := educationLevel(text); level != "" {
		criteria = append(criteria, map[string]any{
			"type": "education", "canonicalName": nil,
			"minimumMonths": nil, "minimumLevel": level, "subjects": []any{},
		})
	}
	if certPat.MatchString(text) && len(criteria) == 0 {
		criteria = append(criteria, map[string]any{
			"type": "certification", "canonicalName": text,
			"minimumMonths": nil, "minimumLevel": nil, "subjects": []any{},
		})
	}
	if len(criteria) == 0 {
		criteria = append(criteria, map[string]any{
			"type": "other", "canonicalName": nil,
			"minimumMonths": nil, "minimumLevel": nil, "subjects": []any{},
		})
	}
	operator := "all_of"
	if orPattern.MatchString(text) {
		operator = "any_of"
	}
	return map[string]any{"operator": operator, "criteria": criteria}
}

func groundedModelCandidates(modelOutput map[string]any, blocks []JobBlock) ([]map[string]any, []string) {
	drafts, warnings, err := schemas.ValidateRequirementExtraction(modelOutput)
	if err != nil {
		return nil, []string{"Model requirement output failed schema validation"}
	}
	byID := make(map[string]JobBlock, len(blocks))
	for _, block := range blocks {
		byID[block.ID] = block
	}
	var candidates []map[string]any
	dropped := 0
	for _, draft := range drafts {
		var evidence []any
		for _, citation := range draft.Evidence {
			block, ok := byID[citation.BlockID]
			if !ok {
				continue
			}
			localStart := strings.Index(block.Text, citation.Quote)
			if localStart < 0 {
				continue
			}
			evidence = append(evidence, map[string]any{
				"blockId": block.ID, "quote": citation.Quote,
				"startOffset": block.StartOffset + localStart,
				"endOffset":   block.StartOffset + localStart + len(citation.Quote),
				"section":     block.Section,
			})
		}
		if len(evidence) == 0 || draft.Assessability == "prohibited" {
			dropped++
			continue
		}
		predicate := normalizeModelPredicate(draft.Predicate)
		if !modelPredicateSupported(predicate, evidence) {
			dropped++
			continue
		}
		confidence := draft.Confidence
		if confidence > 0.8 {
			confidence = 0.8
		}
		weight := 1
		if draft.SuggestedKind == "required" {
			weight = 2
		}
		candidates = append(candidates, finalizeCandidate(map[string]any{
			"normalizedText": normalizedStatement(draft.NormalizedText),
			"category":       draft.Category, "suggestedKind": draft.SuggestedKind,
			"suggestedWeight": weight, "sourceModality": draft.SourceModality,
			"assessability": draft.Assessability, "predicate": predicate,
			"evidence": evidence, "confidence": confidence,
			"signals": []any{"model", "grounded_quote"},
		}))
	}
	if dropped > 0 {
		warnings = append(warnings, fmt.Sprintf("%d model requirements were omitted because they were unsafe or ungrounded", dropped))
	}
	return candidates, warnings
}

func normalizeModelPredicate(predicate map[string]any) map[string]any {
	v, err := vocab.Load()
	if err != nil {
		return predicate
	}
	criteria, ok := predicate["criteria"].([]any)
	if !ok {
		return predicate
	}
	for _, raw := range criteria {
		criterion, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if criterion["type"] == "skill" {
			if name, ok := criterion["canonicalName"].(string); ok {
				if canonical, found := v.CanonicalFor(name); found {
					criterion["canonicalName"] = canonical
				}
			}
		}
	}
	return predicate
}

func modelPredicateSupported(predicate map[string]any, evidence []any) bool {
	var quotes []string
	for _, item := range evidence {
		if entry, ok := item.(map[string]any); ok {
			if quote, ok := entry["quote"].(string); ok {
				quotes = append(quotes, quote)
			}
		}
	}
	evidenceText := strings.Join(quotes, " ")
	if prohibitedPat.MatchString(evidenceText) {
		return false
	}
	evidenceSkills, err := vocab.MentionedSkills(evidenceText)
	if err != nil {
		return false
	}
	loweredEvidence := strings.ToLower(evidenceText)
	criteria, ok := predicate["criteria"].([]any)
	if !ok {
		return false
	}
	for _, raw := range criteria {
		criterion, ok := raw.(map[string]any)
		if !ok {
			return false
		}
		switch criterionType(criterion) {
		case "skill":
			name := strings.TrimSpace(stringField(criterion, "canonicalName"))
			if _, found := evidenceSkills[name]; !found && !strings.Contains(loweredEvidence, strings.ToLower(name)) {
				return false
			}
		case "experience":
			minimum, ok := intField(criterion, "minimumMonths")
			if !ok || experienceMonths(evidenceText) == nil || *experienceMonths(evidenceText) != minimum {
				return false
			}
			if subjects, ok := criterion["subjects"].([]any); ok {
				for _, subject := range subjects {
					name, _ := subject.(string)
					if _, found := evidenceSkills[name]; !found && !strings.Contains(loweredEvidence, strings.ToLower(name)) {
						return false
					}
				}
			}
		case "education":
			if stringField(criterion, "minimumLevel") != educationLevel(evidenceText) {
				return false
			}
		case "certification":
			name := strings.TrimSpace(stringField(criterion, "canonicalName"))
			if name == "" || !strings.Contains(loweredEvidence, strings.ToLower(name)) {
				return false
			}
		}
	}
	return true
}

func criterionType(criterion map[string]any) string {
	value, _ := criterion["type"].(string)
	if value == "" {
		return "other"
	}
	return value
}

func stringField(item map[string]any, field string) string {
	value, _ := item[field].(string)
	return value
}

func intField(item map[string]any, field string) (int, bool) {
	switch value := item[field].(type) {
	case int:
		return value, true
	case float64:
		return int(value), value == float64(int(value))
	default:
		return 0, false
	}
}

func deduplicate(candidates []map[string]any, warnings *[]string) []map[string]any {
	ordered := append([]map[string]any(nil), candidates...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return confidenceOf(ordered[i]) > confidenceOf(ordered[j])
	})
	merged := make(map[string]map[string]any)
	var keys []string
	thresholds := make(map[string]map[int]bool)
	thresholdSubjects := make(map[string][]string)
	for _, candidate := range ordered {
		key := predicateKey(candidate)
		if existing, ok := merged[key]; ok {
			existing["evidence"] = mergeEvidence(existing["evidence"], candidate["evidence"])
			existing["signals"] = unionStrings(existing["signals"], candidate["signals"])
			confidence := confidenceOf(existing) + 0.08
			if confidence > 0.98 {
				confidence = 0.98
			}
			existing["confidence"] = confidence
			continue
		}
		merged[key] = candidate
		keys = append(keys, key)
		predicate, _ := candidate["predicate"].(map[string]any)
		criteria, _ := predicate["criteria"].([]any)
		for _, raw := range criteria {
			criterion, ok := raw.(map[string]any)
			if !ok || criterionType(criterion) != "experience" {
				continue
			}
			minimum, ok := intField(criterion, "minimumMonths")
			if !ok {
				continue
			}
			var subjects []string
			if list, ok := criterion["subjects"].([]any); ok {
				for _, subject := range list {
					if name, ok := subject.(string); ok {
						subjects = append(subjects, name)
					}
				}
			}
			sort.Strings(subjects)
			thresholdKey := "experience\x00" + strings.Join(subjects, "\x00")
			if thresholds[thresholdKey] == nil {
				thresholds[thresholdKey] = make(map[int]bool)
				thresholdSubjects[thresholdKey] = subjects
			}
			thresholds[thresholdKey][minimum] = true
		}
	}
	for key, values := range thresholds {
		if len(values) > 1 {
			label := strings.Join(thresholdSubjects[key], ", ")
			if label == "" {
				label = "general experience"
			}
			*warnings = append(*warnings, fmt.Sprintf("Conflicting thresholds found for %s; recruiter review is required", label))
		}
	}
	out := make([]map[string]any, 0, len(keys))
	for _, key := range keys {
		out = append(out, merged[key])
	}
	return out
}

func confidenceOf(candidate map[string]any) float64 {
	switch value := candidate["confidence"].(type) {
	case float64:
		return value
	case int:
		return float64(value)
	default:
		return 0
	}
}

func predicateKey(candidate map[string]any) string {
	predicate, _ := candidate["predicate"].(map[string]any)
	criteria, _ := predicate["criteria"].([]any)
	onlyOther := len(criteria) > 0
	for _, raw := range criteria {
		if criterion, ok := raw.(map[string]any); ok && criterionType(criterion) != "other" {
			onlyOther = false
		}
	}
	if onlyOther {
		text, _ := candidate["normalizedText"].(string)
		return "text:" + strings.ToLower(text)
	}
	raw, _ := json.Marshal(predicate)
	return "predicate:" + string(raw)
}

func mergeEvidence(left, right any) []any {
	leftList, _ := left.([]any)
	rightList, _ := right.([]any)
	seen := make(map[string]bool)
	var merged []any
	for _, item := range append(append([]any{}, leftList...), rightList...) {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		key := fmt.Sprintf("%v|%v|%v", entry["blockId"], entry["startOffset"], entry["endOffset"])
		if !seen[key] {
			seen[key] = true
			merged = append(merged, entry)
		}
	}
	return merged
}

func unionStrings(left, right any) []any {
	seen := make(map[string]bool)
	var out []any
	for _, list := range []any{left, right} {
		items, _ := list.([]any)
		// Deterministic candidates store signals as []string.
		if items == nil {
			if strings_, ok := list.([]string); ok {
				for _, signal := range strings_ {
					if !seen[signal] {
						seen[signal] = true
						out = append(out, signal)
					}
				}
			}
			continue
		}
		for _, item := range items {
			if signal, ok := item.(string); ok && !seen[signal] {
				seen[signal] = true
				out = append(out, signal)
			}
		}
	}
	if out == nil {
		return []any{}
	}
	return out
}

func finalizeCandidate(candidate map[string]any) map[string]any {
	evidence, _ := candidate["evidence"].([]any)
	var source []any
	for _, item := range evidence {
		if entry, ok := item.(map[string]any); ok {
			source = append(source, []any{entry["blockId"], entry["startOffset"], entry["endOffset"]})
		}
	}
	identity := map[string]any{
		"text": candidate["normalizedText"], "predicate": candidate["predicate"], "source": source,
	}
	raw, _ := json.Marshal(identity)
	digest := blake2b.Sum256(raw)
	return mergeMaps(map[string]any{"stableId": "requirement-" + hex.EncodeToString(digest[:8])}, candidate)
}

func mergeMaps(first, second map[string]any) map[string]any {
	out := make(map[string]any, len(first)+len(second))
	for key, value := range first {
		out[key] = value
	}
	for key, value := range second {
		out[key] = value
	}
	return out
}

// EducationLevel returns doctorate/master/bachelor when degree phrasing matches.
func educationLevel(text string) string {
	lowered := strings.ToLower(text)
	for _, level := range degreeLevels {
		for _, phrase := range level.phrases {
			if strings.Contains(lowered, phrase) {
				return level.level
			}
		}
	}
	return ""
}

// ExperienceMonths parses digit and word year counts into months.
func experienceMonths(text string) *int {
	if match := yearsPattern.FindStringSubmatch(text); match != nil {
		years, err := parseInt(match[1])
		if err == nil {
			months := years * 12
			return &months
		}
	}
	if match := wordYears.FindStringSubmatch(text); match != nil {
		if years, ok := numberWords[strings.ToLower(match[1])]; ok {
			months := years * 12
			return &months
		}
	}
	return nil
}

// ExperienceMonthsFor is the exported threshold parser.
func ExperienceMonthsFor(text string) (int, bool) {
	months := experienceMonths(text)
	if months == nil {
		return 0, false
	}
	return *months, true
}

// EducationLevelFor is the exported degree-level classifier.
func EducationLevelFor(text string) string { return educationLevel(text) }

func parseInt(text string) (int, error) {
	value := 0
	for _, r := range text {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("not a number")
		}
		value = value*10 + int(r-'0')
	}
	return value, nil
}

func normalizedStatement(text string) string {
	trimmed := strings.Join(strings.Fields(norm.NFKC.String(text)), " ")
	return strings.Trim(trimmed, " ;")
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, value := range values {
		key := strings.ToLower(value)
		if !seen[key] {
			seen[key] = true
			out = append(out, value)
		}
	}
	return out
}
