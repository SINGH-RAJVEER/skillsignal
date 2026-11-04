package independent

import "testing"

func TestRoleReportUsesDescriptionThresholdAndPreservesUnknown(t *testing.T) {
	facts := map[string]any{
		"skills":     []any{map[string]any{"canonicalName": "Python", "evidenceBlockIds": []any{"experience"}}},
		"employment": []any{map[string]any{"startDate": "2020-01", "endDate": "2022-01", "isCurrent": false, "evidence": []any{map[string]any{"blockId": "experience"}}}},
	}
	score, coverage, requirements, assessments := RoleReport("Requirements\n- At least 3 years of Python experience\n- Kubernetes required", facts)
	if score == nil || *score != 50 || coverage != 50 || len(requirements) != 2 || len(assessments) != 2 {
		t.Fatalf("score=%v coverage=%d requirements=%+v assessments=%+v", score, coverage, requirements, assessments)
	}
	for _, item := range assessments {
		if item["requirementId"] == nil || item["requirementId"] == "" {
			t.Fatal("missing criterion identifier")
		}
	}
	if score, _, _, _ := RoleReport("", facts); score != nil {
		t.Fatal("general evaluation acquired a role score")
	}
}

func TestQualityScoreDoesNotUseIdentity(t *testing.T) {
	facts := map[string]any{"skills": []any{map[string]any{"canonicalName": "Python"}}}
	before, _ := Report(facts, "")
	facts["contact"] = map[string]any{"name": "Jane Doe", "email": "jane@example.com", "location": "London"}
	after, _ := Report(facts, "")
	if before != after {
		t.Fatalf("identity changed quality score: %d -> %d", before, after)
	}
}
