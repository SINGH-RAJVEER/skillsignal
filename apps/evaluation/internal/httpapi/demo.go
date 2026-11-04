package httpapi

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/skillsignal/backend/documents"
	"github.com/skillsignal/backend/domainhttp"
)

type demoInput struct {
	EmployerUserID  string `json:"employerUserId"`
	CandidateUserID string `json:"candidateUserId"`
	OrganizationID  string `json:"organizationId"`
	JobID           string `json:"jobId"`
	JobVersionID    string `json:"jobVersionId"`
	Submissions     []struct {
		ID                string `json:"id"`
		ResumeVersionID   string `json:"resumeVersionId"`
		CandidateRecordID string `json:"candidateRecordId"`
		CandidateName     string `json:"candidateName"`
		CandidateEmail    string `json:"candidateEmail"`
	} `json:"submissions"`
	Requirements []struct {
		requirement
		StableID  string          `json:"stableId"`
		Predicate json.RawMessage `json:"predicate"`
		Aliases   json.RawMessage `json:"aliases"`
	} `json:"requirements"`
}

// These fixtures are isolated by deterministic identifiers and read-only demo accounts.
func (s *Server) seedDemo(w http.ResponseWriter, r *http.Request) {
	var in demoInput
	if !domainhttp.DecodeJSON(w, r, &in) {
		return
	}
	for _, id := range []string{in.EmployerUserID, in.CandidateUserID, in.OrganizationID, in.JobID, in.JobVersionID} {
		if !strings.HasPrefix(id, "demo-") {
			bad(w, "Demo identifiers are required")
			return
		}
	}
	if len(in.Submissions) == 0 || len(in.Requirements) == 0 {
		bad(w, "Demo submissions and requirements are required")
		return
	}
	for _, sub := range in.Submissions {
		if !strings.HasPrefix(sub.ID, "demo-") || sub.ResumeVersionID == "" {
			bad(w, "Demo submission identifiers are required")
			return
		}
	}
	for _, req := range in.Requirements {
		if !strings.HasPrefix(req.ID, "demo-") {
			bad(w, "Demo requirement identifiers are required")
			return
		}
	}
	key := "independent-resumes/demo-independent.txt"
	source := []byte("Demo Candidate\nSoftware engineer\nExperience developing Go and TypeScript services.\nThis is synthetic demonstration data.")
	if err := s.client.Call(r.Context(), "documents", "/internal/documents/objects", map[string]any{"key": key, "content": source}, nil); err != nil {
		fail(w, err)
		return
	}
	evidenceByVersion := map[string]json.RawMessage{}
	for _, sub := range in.Submissions {
		var evidence map[string]string
		if err := s.client.Call(r.Context(), "documents", "/internal/documents/versions/"+url.PathEscape(sub.ResumeVersionID)+"/demo-extraction", map[string]any{}, &evidence); err != nil {
			fail(w, err)
			return
		}
		encoded, _ := json.Marshal([]map[string]string{evidence})
		evidenceByVersion[sub.ResumeVersionID] = encoded
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	batchID := "demo-batch-" + in.JobID
	_, err = tx.Exec(r.Context(), `INSERT INTO batch_evaluation(id,organization_id,job_id,job_version_id,created_by_user_id,requirement_schema_version,model_configuration) VALUES($1,$2,$3,$4,$5,'requirements-1','{"source":"synthetic-demo"}') ON CONFLICT(id) DO NOTHING`, batchID, in.OrganizationID, in.JobID, in.JobVersionID, in.EmployerUserID)
	for index, sub := range in.Submissions {
		if err != nil {
			break
		}
		_, err = tx.Exec(r.Context(), `INSERT INTO batch_evaluation_submission(organization_id,job_id,batch_evaluation_id,resume_submission_id) VALUES($1,$2,$3,$4) ON CONFLICT DO NOTHING`, in.OrganizationID, in.JobID, batchID, sub.ID)
		if err != nil {
			break
		}
		evaluationID := "demo-evaluation-" + sub.ID
		// The first example documents each criterion. The second demonstrates unknown coverage.
		score, coverage := 100, 100
		eligibility := "eligible"
		if index > 0 {
			score, coverage = 0, 0
			eligibility = "needs_review"
			if index > 1 {
				coverage = 100
				eligibility = "not_eligible"
			}
		}
		_, err = tx.Exec(r.Context(), `INSERT INTO evaluation(id,batch_evaluation_id,resume_submission_id,job_version_id,resume_version_id,status,score,evidence_coverage,eligibility,quality_state,assessment_schema_version,assessment_prompt_version,assessment_degradation,completed_at) VALUES($1,$2,$3,$4,$5,'complete',$6,$7,$8,'ready','demo-1','demo-1','synthetic_demo',now()) ON CONFLICT(id) DO NOTHING`, evaluationID, batchID, sub.ID, in.JobVersionID, sub.ResumeVersionID, score, coverage, eligibility)
		for _, req := range in.Requirements {
			if err != nil {
				break
			}
			outcome := "met"
			if index > 0 {
				outcome = "unknown"
				if index > 1 {
					outcome = "not_met"
				}
			}
			if req.Kind == "ignored" {
				outcome = "unknown"
				if index > 1 {
					outcome = "not_met"
				}
			}
			evidence := evidenceByVersion[sub.ResumeVersionID]
			if outcome == "unknown" {
				evidence = json.RawMessage(`[]`)
			}
			_, err = tx.Exec(r.Context(), `INSERT INTO requirement_assessment(id,evaluation_id,job_requirement_id,outcome,confidence,reasoning,evidence) VALUES($1,$2,$3,$4,1,$5,$6::jsonb) ON CONFLICT(evaluation_id,job_requirement_id) DO NOTHING`, evaluationID+":"+req.ID, evaluationID, req.ID, outcome, "Synthetic demo assessment. Review source evidence before making a hiring decision.", evidence)
		}
	}
	facts, _ := json.Marshal(map[string]any{"skills": []map[string]string{{"canonicalName": "Go"}, {"canonicalName": "TypeScript"}}, "warnings": []string{"Synthetic demonstration report"}})
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO independent_evaluation(id,user_id,storage_key,original_name,media_type,status,score,general_quality_score,normalized_facts,suggestions,completed_at,retention_date) VALUES('demo-independent',$1,$2,'demo-resume.txt',$3,'complete',80,80,$4::jsonb,'[{"title":"Show the result","detail":"Describe a documented project result without adding unsupported claims."}]',now(),$5) ON CONFLICT(id) DO NOTHING`, in.CandidateUserID, key, documents.MediaTypeTXT, facts, time.Now().UTC().AddDate(10, 0, 0))
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		fail(w, err)
		return
	}
	reply(w, 200, map[string]string{"batchEvaluationId": batchID, "independentEvaluationId": "demo-independent"})
}
