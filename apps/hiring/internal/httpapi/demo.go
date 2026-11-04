package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/skillsignal/backend/domainhttp"
)

func (s *Server) demo(w http.ResponseWriter, r *http.Request) {
	if !s.internal(w, r) {
		return
	}
	var input struct {
		OrganizationID  string `json:"organizationId"`
		EmployerUserID  string `json:"employerUserId"`
		CandidateUserID string `json:"candidateUserId"`
	}
	if !domainhttp.DecodeJSON(w, r, &input) {
		return
	}
	if input.OrganizationID != "demo-organization" || input.EmployerUserID != "demo-employer" || input.CandidateUserID != "demo-candidate" {
		domainhttp.WriteError(w, &domainhttp.Error{Status: 400, Code: "INVALID_REQUEST", Message: "Invalid demo owner"})
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		domainhttp.WriteError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	_, err = tx.Exec(r.Context(), `INSERT INTO job(id,organization_id,title,application_opens_at,application_closes_at) VALUES('demo-job',$1,'Go software engineer',now()-interval '1 day','2099-01-01') ON CONFLICT(id) DO NOTHING`, input.OrganizationID)
	if err == nil {
		_, err = tx.Exec(r.Context(), `SELECT id FROM job WHERE id='demo-job' FOR UPDATE`)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO job_version(id,job_id,version,source_text,normalized_text,source_media_type,confirmed_at) VALUES('demo-job-version','demo-job',1,'Professional Go experience is required. Go experience is an eligibility gate.','Professional Go experience is required. Go experience is an eligibility gate.','text/plain',now()) ON CONFLICT(id) DO NOTHING`)
	}
	predicate := map[string]any{"operator": "all_of", "criteria": []map[string]string{{"type": "skill", "canonicalName": "Go"}}}
	requirements := make([]map[string]any, 0, 2)
	for _, q := range []struct{ id, kind string }{{"demo-go-required", "required"}, {"demo-go-gate", "hard_gate"}} {
		encoded, _ := json.Marshal(predicate)
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO job_requirement(id,job_version_id,stable_id,kind,weight,normalized_text,category,source_modality,assessability,predicate,aliases,source_evidence,confirmed_at) VALUES($1,'demo-job-version',$1,$2,2,'Professional Go experience','skill','explicit','resume_evidence',$3::jsonb,'[]','[{"blockId":"jd-b1","quote":"Professional Go experience is required."}]',now()) ON CONFLICT(id) DO NOTHING`, q.id, q.kind, encoded)
		}
		requirements = append(requirements, map[string]any{"id": q.id, "stableId": q.id, "normalizedText": "Professional Go experience", "kind": q.kind, "weight": 2, "predicate": predicate, "aliases": []string{}})
	}
	if err != nil {
		domainhttp.WriteError(w, err)
		return
	}
	submissions := make([]map[string]any, 0, 3)
	for _, person := range []struct{ suffix, name, text string }{
		{"experienced", "Alex Morgan", "Alex Morgan\nalex@example.invalid\nSoftware engineer with five years of professional Go experience building APIs and distributed services."},
		{"unclear", "Sam Rivera", "Sam Rivera\nsam@example.invalid\nSoftware engineer building APIs and distributed services. Programming languages are not specified in this synthetic resume."},
		{"ineligible", "Taylor Chen", "Taylor Chen\ntaylor@example.invalid\nSoftware engineer building Python APIs. I have no Go experience and have never used Go professionally."},
	} {
		submissionID, candidateID := "demo-submission-"+person.suffix, "demo-record-"+person.suffix
		var versionID string
		err = tx.QueryRow(r.Context(), `SELECT COALESCE((SELECT resume_version_id FROM resume_submission WHERE id=$1),'')`, submissionID).Scan(&versionID)
		if err != nil {
			domainhttp.WriteError(w, err)
			return
		}
		if versionID == "" {
			var document struct {
				VersionID string `json:"versionId"`
			}
			if err := s.client.Call(r.Context(), "documents", "/internal/documents", map[string]any{"organizationId": input.OrganizationID, "originalName": person.name + ".txt", "mediaType": "text/plain", "content": []byte(person.text), "retentionDays": 36500}, &document); err != nil {
				domainhttp.WriteError(w, err)
				return
			}
			versionID = document.VersionID
			if versionID == "" {
				domainhttp.WriteError(w, &domainhttp.Error{Status: 503, Code: "SERVICE_UNAVAILABLE", Message: "Demo document unavailable"})
				return
			}
			_, err = tx.Exec(r.Context(), `INSERT INTO candidate_record(id,organization_id,full_name,email) VALUES($1,$2,$3,$4)`, candidateID, input.OrganizationID, person.name, person.suffix+"@example.invalid")
			if err == nil {
				_, err = tx.Exec(r.Context(), `INSERT INTO resume_submission(id,organization_id,job_id,candidate_record_id,resume_version_id) VALUES($1,$2,'demo-job',$3,$4)`, submissionID, input.OrganizationID, candidateID, versionID)
			}
			if err != nil {
				domainhttp.WriteError(w, err)
				return
			}
		}
		submissions = append(submissions, map[string]any{"id": submissionID, "resumeVersionId": versionID, "candidateRecordId": candidateID, "candidateName": person.name, "candidateEmail": person.suffix + "@example.invalid"})
	}
	_, err = tx.Exec(r.Context(), `UPDATE resume_submission SET submitting_user_id=$1 WHERE id='demo-submission-experienced'`, input.CandidateUserID)
	if err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE candidate_record SET user_id=$1 WHERE id='demo-record-experienced'`, input.CandidateUserID)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO invitation(id,job_id,creator_user_id,token_hash,expires_at,redeeming_user_id,resume_submission_id) VALUES('demo-invitation','demo-job',$1,$2,'2099-01-01',$3,'demo-submission-experienced') ON CONFLICT(id) DO NOTHING`, input.EmployerUserID, invitationHash("demo-consumed-invitation"), input.CandidateUserID)
	}
	if err != nil {
		domainhttp.WriteError(w, err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		domainhttp.WriteError(w, err)
		return
	}
	domainhttp.WriteJSON(w, 200, map[string]any{"organizationId": input.OrganizationID, "employerUserId": input.EmployerUserID, "candidateUserId": input.CandidateUserID, "jobId": "demo-job", "jobVersionId": "demo-job-version", "requirements": requirements, "submissions": submissions})
}
