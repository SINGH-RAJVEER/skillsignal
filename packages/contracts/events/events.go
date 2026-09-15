// Package events defines versioned messages exchanged between SkillSignal modules.
package events

import (
	"encoding/json"
	"time"
)

const CurrentVersion = 1

type Envelope struct {
	ID            string          `json:"id"`
	Type          string          `json:"type"`
	Version       int             `json:"version"`
	Source        string          `json:"source"`
	Subject       string          `json:"subject"`
	CorrelationID string          `json:"correlationId"`
	OccurredAt    time.Time       `json:"occurredAt"`
	Data          json.RawMessage `json:"data"`
}

const (
	ResumeUploaded        = "documents.resume-uploaded"
	ResumeParsed          = "documents.resume-parsed"
	ResumeParsingFailed   = "documents.resume-parsing-failed"
	RequirementsDrafted   = "hiring.requirements-drafted"
	RequirementsConfirmed = "hiring.requirements-confirmed"
	SubmissionCreated     = "hiring.submission-created"
	EvaluationRequested   = "evaluation.requested"
	EvaluationCompleted   = "evaluation.completed"
	EvaluationFailed      = "evaluation.failed"
	PointsReserved        = "billing.points-reserved"
	PointsSettled         = "billing.points-settled"
	PointsReleased        = "billing.points-released"
	DocumentExpired       = "documents.document-expired"
)
