// Package modelschemas builds the strict JSON Schemas sent with
// structured-output model calls. Every property is required and closed,
// mirroring strict_schema in the Python service.
package modelschemas

func strProp(min, max int) map[string]any {
	return map[string]any{"type": "string", "minLength": min, "maxLength": max}
}

func optStrProp(max int) map[string]any {
	return map[string]any{"type": []any{"string", "null"}, "maxLength": max}
}

func optDateProp() map[string]any {
	return map[string]any{"type": []any{"string", "null"}, "pattern": `^\d{4}(-\d{2})?$`, "maxLength": 16}
}

func evidenceQuote() map[string]any {
	return obj(map[string]any{
		"blockId": strProp(1, 128),
		"quote":   strProp(1, 2000),
	})
}

func evidenceArray(min, max int) map[string]any {
	return map[string]any{"type": "array", "items": evidenceQuote(), "minItems": min, "maxItems": max}
}

func obj(properties map[string]any) map[string]any {
	required := make([]any, 0, len(properties))
	for key := range properties {
		required = append(required, key)
	}
	return map[string]any{
		"type": "object", "properties": properties,
		"required": required, "additionalProperties": false,
	}
}

func arrOf(item map[string]any, min, max int) map[string]any {
	schema := map[string]any{"type": "array", "items": item}
	if min >= 0 {
		schema["minItems"] = min
	}
	if max >= 0 {
		schema["maxItems"] = max
	}
	return schema
}

// ResumeFacts is the resume-extraction structured-output schema.
func ResumeFacts() map[string]any {
	contact := obj(map[string]any{
		"name": optStrProp(256), "email": optStrProp(320),
		"phone": optStrProp(64), "location": optStrProp(256),
		"evidence": evidenceArray(0, 20),
	})
	skill := obj(map[string]any{
		"canonicalName": strProp(1, 128), "sourceText": strProp(1, 1000),
		"evidence": evidenceArray(1, 20),
	})
	employment := obj(map[string]any{
		"employer": optStrProp(256), "title": optStrProp(256),
		"startDate": optDateProp(), "endDate": optDateProp(),
		"isCurrent": map[string]any{"type": "boolean"},
		"evidence":  evidenceArray(1, 20),
	})
	education := obj(map[string]any{
		"institution": optStrProp(256), "degree": optStrProp(256),
		"fieldOfStudy": optStrProp(256), "graduationDate": optDateProp(),
		"evidence": evidenceArray(1, 20),
	})
	certification := obj(map[string]any{
		"name": strProp(1, 256), "issuer": optStrProp(256),
		"evidence": evidenceArray(1, 20),
	})
	suggestion := obj(map[string]any{
		"title": strProp(1, 200), "detail": strProp(1, 1000),
		"evidence": evidenceArray(1, 20),
	})
	return obj(map[string]any{
		"schemaVersion": map[string]any{"type": "string", "const": "3"},
		"contact":       contact,
		"skills":        arrOf(skill, -1, 200),
		"employment":    arrOf(employment, -1, 100),
		"education":     arrOf(education, -1, 50),
		"certifications": arrOf(certification, -1, 100),
		"suggestions":   arrOf(suggestion, -1, 10),
		"warnings":      arrOf(map[string]any{"type": "string"}, -1, 50),
	})
}

// AssessmentOutput is the requirement-assessment structured-output schema.
func AssessmentOutput() map[string]any {
	assessment := obj(map[string]any{
		"requirementId": strProp(1, 128),
		"outcome":       map[string]any{"type": "string", "enum": []any{"met", "partial", "not_met", "unknown"}},
		"confidence":    map[string]any{"type": "number", "minimum": 0, "maximum": 1},
		"reasoning":     strProp(1, 2000),
		"evidence":      evidenceArray(0, 20),
	})
	return obj(map[string]any{"assessments": arrOf(assessment, -1, 200)})
}

// RequirementDrafts is the job-requirement-draft structured-output schema.
func RequirementDrafts() map[string]any {
	criterion := obj(map[string]any{
		"type":          map[string]any{"type": "string", "enum": []any{"skill", "experience", "education", "certification", "other"}},
		"canonicalName": map[string]any{"type": []any{"string", "null"}, "maxLength": 256},
		"minimumMonths": map[string]any{"type": []any{"integer", "null"}, "minimum": 0, "maximum": 1200},
		"minimumLevel":  map[string]any{"type": []any{"string", "null"}, "maxLength": 64},
		"subjects":      arrOf(map[string]any{"type": "string"}, -1, 20),
	})
	predicate := obj(map[string]any{
		"operator": map[string]any{"type": "string", "enum": []any{"all_of", "any_of"}},
		"criteria": arrOf(criterion, 1, 30),
	})
	draft := obj(map[string]any{
		"normalizedText": strProp(3, 2000),
		"category":       map[string]any{"type": "string", "enum": []any{"skill", "experience", "education", "certification", "language", "location", "work_authorization", "schedule", "soft_skill", "other"}},
		"suggestedKind":  map[string]any{"type": "string", "enum": []any{"required", "preferred", "ignored"}},
		"sourceModality": map[string]any{"type": "string", "enum": []any{"explicit_required", "explicit_preferred", "section_required", "section_preferred", "unclear"}},
		"assessability":  map[string]any{"type": "string", "enum": []any{"resume_evidence", "candidate_attestation", "recruiter_review", "prohibited", "unclear"}},
		"predicate":      predicate,
		"evidence":       evidenceArray(1, 10),
		"confidence":     map[string]any{"type": "number", "minimum": 0, "maximum": 1},
	})
	return obj(map[string]any{
		"requirements": arrOf(draft, -1, 100),
		"warnings":     arrOf(map[string]any{"type": "string"}, -1, 50),
	})
}
