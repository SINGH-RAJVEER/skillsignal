package normalizer

import (
	"strings"
	"testing"
)

func TestScoringBlocksRemoveIdentityAndKeepLaterSkillBlocks(t *testing.T) {
	blocks := []Block{
		{ID: "identity", Text: "Jane Doe\njane@example.com\nLocation: Python\nPhone: +1 555 555 0101\nGender: female"},
		{ID: "skills", Text: "Python Kubernetes\nBuilt software services using Go. Portfolio: https://example.com"},
	}
	redacted := ScoringBlocks(blocks)
	if len(redacted) != 1 || redacted[0].ID != "skills" || !strings.Contains(redacted[0].Text, "Python Kubernetes") || strings.Contains(redacted[0].Text, "example.com") {
		t.Fatalf("unexpected scoring text: %+v", redacted)
	}
	facts, err := Normalize([]Block{{ID: "identity", Text: "Jane Doe\nLocation: Python\nEmail: kubernetes@example.com"}, {ID: "experience", Text: "Built services using Go."}})
	if err != nil {
		t.Fatal(err)
	}
	for _, skill := range facts.Skills {
		if skill.CanonicalName == "Python" || skill.CanonicalName == "Kubernetes" {
			t.Fatalf("identity became skill: %+v", skill)
		}
	}
}

func TestScoringBlocksPreserveDatesAndMixedHeaderEvidence(t *testing.T) {
	blocks := []Block{
		{ID: "header", Text: "Jane Doe | Senior Python Engineer | jane@example.com | +1 (555) 555-0101"},
		{ID: "history", Text: "Python Engineer 2019 - 2023\nSoftware Engineer 2019-01 - 2023-05\nBuilt services from 2020 to 2022."},
	}
	got := ScoringBlocks(blocks)
	if len(got) != 2 || got[0].Text != "Senior Python Engineer" || got[1].Text != blocks[1].Text {
		t.Fatalf("lost role/date evidence or leaked identity: %+v", got)
	}
}
