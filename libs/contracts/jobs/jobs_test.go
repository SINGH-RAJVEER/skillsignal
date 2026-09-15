package jobs

import "testing"

func TestRolesDoNotShareJobTypes(t *testing.T) {
	seen := map[string]Role{}
	for _, role := range []Role{ParserRole, InferenceRole} {
		for _, jobType := range TypesFor(role) {
			if owner, exists := seen[jobType]; exists {
				t.Fatalf("job type %q belongs to both %q and %q", jobType, owner, role)
			}
			seen[jobType] = role
		}
	}
	if len(seen) != 7 {
		t.Fatalf("got %d job types, want 7", len(seen))
	}
}

func TestParseRoleRejectsUnknownRole(t *testing.T) {
	if _, err := ParseRole("worker"); err == nil {
		t.Fatal("expected an invalid role error")
	}
}
