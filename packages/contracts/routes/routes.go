// Package routes assigns each public request to its authoritative domain process.
package routes

import "strings"

const (
	Identity   = "identity"
	Hiring     = "hiring"
	Documents  = "documents"
	Evaluation = "evaluation"
	Billing    = "billing"
)

func ServiceFor(path string) (string, bool) {
	segments := strings.Split(strings.Trim(path, "/"), "/")
	if len(segments) < 2 || segments[0] != "api" {
		return "", false
	}
	switch segments[1] {
	case "auth", "employer", "demo":
		return Identity, true
	case "billing", "me", "payments":
		return Billing, true
	case "independent-evaluations", "evaluations", "processing":
		return Evaluation, true
	case "documents", "resumes", "uploads":
		return Documents, true
	case "invitations", "submissions", "exports", "review-decisions":
		return Hiring, true
	case "organizations":
		if len(segments) >= 4 && segments[3] == "jobs" {
			return Hiring, true
		}
		return Identity, true
	case "jobs":
		for _, segment := range segments[2:] {
			if segment == "evaluations" {
				return Evaluation, true
			}
		}
		return Hiring, true
	case "admin":
		if len(segments) >= 3 && segments[2] == "enterprise" {
			return Billing, true
		}
	}
	return "", false
}
