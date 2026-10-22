package routes

import "testing"

func TestServiceFor(t *testing.T) {
	tests := map[string]string{
		"/api/auth/session":                   Identity,
		"/api/me":                             Identity,
		"/api/employer/auth/sign-in/email":    Identity,
		"/api/organizations/org-1/members":    Identity,
		"/api/organizations/org-1/jobs":       Hiring,
		"/api/jobs/job-1/invitations":         Hiring,
		"/api/jobs/job-1/evaluations":         Evaluation,
		"/api/independent-evaluations/eval-1": Evaluation,
		"/api/billing/orders/order-1/verify":  Billing,
		"/api/me/points":                      Billing,
		"/api/documents/document-1":           Documents,
	}
	for path, want := range tests {
		t.Run(path, func(t *testing.T) {
			got, ok := ServiceFor(path)
			if !ok || got != want {
				t.Fatalf("ServiceFor(%q) = %q, %t; want %q, true", path, got, ok, want)
			}
		})
	}
}

func TestServiceForRejectsUnknownPath(t *testing.T) {
	if got, ok := ServiceFor("/api/unknown"); ok || got != "" {
		t.Fatalf("got %q, %t; want no owner", got, ok)
	}
}
