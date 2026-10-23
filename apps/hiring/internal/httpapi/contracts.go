package httpapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/skillsignal/backend/domainhttp"
	"github.com/skillsignal/backend/ids"
	"github.com/skillsignal/contracts/jobs"
)

func (s *Server) createJobForm(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 12<<20)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		domainhttp.WriteError(w, &domainhttp.Error{Status: 400, Code: "INVALID_REQUEST", Message: "Invalid job description upload"})
		return
	}
	defer r.MultipartForm.RemoveAll()
	org := r.FormValue("organization_id")
	if _, ok := s.access(w, r, org, true); !ok {
		return
	}
	title, description := strings.TrimSpace(r.FormValue("title")), strings.TrimSpace(r.FormValue("description"))
	files := r.MultipartForm.File["file"]
	if title == "" || len(title) > 200 || len(description) > 1_000_000 || len(files) > 1 || (description == "" && len(files) == 0) {
		domainhttp.WriteError(w, &domainhttp.Error{Status: 400, Code: "INVALID_REQUEST", Message: "Supply a title and one job description"})
		return
	}
	mediaType, storageKey := "text/plain", ""
	if len(files) == 1 {
		data, name, err := readFile(files[0], 10<<20)
		if err != nil {
			domainhttp.WriteError(w, &domainhttp.Error{Status: 400, Code: "INVALID_DOCUMENT", Message: err.Error()})
			return
		}
		switch strings.ToLower(filepath.Ext(name)) {
		case ".pdf":
			mediaType = "application/pdf"
		case ".docx":
			mediaType = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
		case ".txt":
			mediaType = "text/plain"
		case ".md":
			mediaType = "text/markdown"
		case ".html", ".htm":
			mediaType = "text/html"
		default:
			domainhttp.WriteError(w, &domainhttp.Error{Status: 400, Code: "INVALID_DOCUMENT", Message: "Unsupported job description format"})
			return
		}
		var stored struct {
			StorageKey string `json:"storageKey"`
			MediaType  string `json:"mediaType"`
		}
		if err := s.client.Call(r.Context(), "documents", "/internal/documents/job-descriptions", map[string]any{"originalName": name, "mediaType": mediaType, "content": data}, &stored); err != nil {
			domainhttp.WriteError(w, err)
			return
		}
		storageKey = stored.StorageKey
		if storageKey == "" {
			domainhttp.WriteError(w, &domainhttp.Error{Status: 503, Code: "SERVICE_UNAVAILABLE", Message: "Document service returned an invalid object"})
			return
		}
	}
	id, versionID, processingID := ids.New(), ids.New(), ids.New()
	tx, err := s.pool.Begin(r.Context())
	if err == nil {
		defer tx.Rollback(r.Context())
		_, err = tx.Exec(r.Context(), `INSERT INTO job(id,organization_id,title) VALUES($1,$2,$3)`, id, org, title)
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO job_version(id,job_id,version,source_text,source_media_type,source_storage_key) VALUES($1,$2,1,NULLIF($3,''),$4,NULLIF($5,''))`, versionID, id, description, mediaType, storageKey)
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO processing_job(id,type,payload_reference,idempotency_key) VALUES($1,$2,$3,$4)`, processingID, jobs.JobDescriptionParse, versionID, "job-description-parse:"+versionID)
		}
		if err == nil {
			err = tx.Commit(r.Context())
		}
	}
	if err != nil {
		if storageKey != "" {
			_ = s.client.Request(r.Context(), http.MethodDelete, "documents", "/internal/documents/objects/"+storageKey, nil, nil)
		}
		domainhttp.WriteError(w, err)
		return
	}
	domainhttp.WriteJSON(w, 201, map[string]any{"id": id, "versionId": versionID, "processingJobId": processingID})
}

func (s *Server) jobDetail(w http.ResponseWriter, r *http.Request, id, org string) {
	var raw []byte
	err := s.pool.QueryRow(r.Context(), `SELECT jsonb_build_object('id',j.id,'organizationId',j.organization_id,'title',j.title,'applicationOpensAt',j.application_opens_at,'applicationClosesAt',j.application_closes_at,'versionId',v.id,'version',v.version,'description',v.source_text,'confirmed',v.confirmed_at IS NOT NULL,
	'draftStatus',CASE WHEN v.draft_requirements IS NOT NULL THEN 'ready' WHEN EXISTS(SELECT 1 FROM processing_job p WHERE p.payload_reference=v.id AND p.status='dead') THEN 'failed' ELSE 'processing' END,
	'draftError',(SELECT p.safe_error FROM processing_job p WHERE p.payload_reference=v.id AND p.status='dead' ORDER BY p.created_at DESC LIMIT 1),
	'draftQualityState',v.draft_requirements->'qualityState','draftWarnings',COALESCE(v.draft_requirements->'warnings','[]'::jsonb),'draftDegraded',COALESCE((v.draft_requirements->>'degraded')::boolean,false),
	'draftRequirements',COALESCE(v.draft_requirements->'requirements','[]'::jsonb),
	'requirements',COALESCE((SELECT jsonb_agg(jsonb_build_object('id',q.id,'stableId',q.stable_id,'normalizedText',q.normalized_text,'kind',q.kind,'weight',q.weight,'category',q.category,'sourceModality',q.source_modality,'assessability',q.assessability,'predicate',q.predicate,'aliases',q.aliases,'sourceEvidence',q.source_evidence) ORDER BY q.stable_id) FROM job_requirement q WHERE q.job_version_id=v.id),'[]'::jsonb)) FROM job j LEFT JOIN LATERAL (SELECT * FROM job_version WHERE job_id=j.id ORDER BY version DESC LIMIT 1) v ON true WHERE j.id=$1 AND j.organization_id=$2`, id, org).Scan(&raw)
	if err != nil {
		domainhttp.WriteError(w, err)
		return
	}
	domainhttp.WriteJSON(w, 200, json.RawMessage(raw))
}

func (s *Server) confirmCurrentRequirements(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("job")
	if _, _, ok := s.jobAccess(w, r, jobID, true); !ok {
		return
	}
	var input struct {
		Requirements []struct {
			StableID string `json:"stable_id"`
			Text     string `json:"normalized_text"`
			Kind     string `json:"kind"`
			Weight   int    `json:"weight"`
		} `json:"requirements"`
	}
	if !domainhttp.DecodeJSON(w, r, &input) {
		return
	}
	var versionID string
	var draft []byte
	err := s.pool.QueryRow(r.Context(), `SELECT id,COALESCE(draft_requirements,'{}'::jsonb) FROM job_version WHERE job_id=$1 ORDER BY version DESC LIMIT 1`, jobID).Scan(&versionID, &draft)
	if err != nil {
		domainhttp.WriteError(w, err)
		return
	}
	var artifact struct {
		Requirements []map[string]any `json:"requirements"`
	}
	if err := json.Unmarshal(draft, &artifact); err != nil {
		domainhttp.WriteError(w, err)
		return
	}
	requirements := make([]map[string]any, 0, len(input.Requirements))
	for _, q := range input.Requirements {
		value := map[string]any{"stableId": q.StableID, "normalizedText": q.Text, "kind": q.Kind, "weight": q.Weight, "predicate": map[string]any{}, "aliases": []string{}, "sourceEvidence": []any{}}
		for _, original := range artifact.Requirements {
			if original["stableId"] == q.StableID {
				for _, key := range []string{"category", "sourceModality", "assessability", "predicate", "aliases", "sourceEvidence"} {
					if v, exists := original[key]; exists {
						value[key] = v
					}
				}
				if evidence, exists := original["evidence"]; exists {
					value["sourceEvidence"] = evidence
				}
				break
			}
		}
		requirements = append(requirements, value)
	}
	body, err := json.Marshal(map[string]any{"requirements": requirements})
	if err != nil {
		domainhttp.WriteError(w, err)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	r.SetPathValue("version", versionID)
	s.confirmVersion(w, r)
}

func (s *Server) applicationWindow(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("job")
	if _, _, ok := s.jobAccess(w, r, jobID, true); !ok {
		return
	}
	var input struct {
		OpensAt  time.Time `json:"opens_at"`
		ClosesAt time.Time `json:"closes_at"`
	}
	if !domainhttp.DecodeJSON(w, r, &input) {
		return
	}
	if input.OpensAt.IsZero() || !input.ClosesAt.After(input.OpensAt) {
		domainhttp.WriteError(w, &domainhttp.Error{Status: 400, Code: "INVALID_REQUEST", Message: "Application closing time must follow opening time"})
		return
	}
	_, err := s.pool.Exec(r.Context(), `UPDATE job SET application_opens_at=$1,application_closes_at=$2,updated_at=now() WHERE id=$3`, input.OpensAt, input.ClosesAt, jobID)
	if err != nil {
		domainhttp.WriteError(w, err)
		return
	}
	domainhttp.WriteJSON(w, 200, map[string]any{"opensAt": input.OpensAt, "closesAt": input.ClosesAt})
}

func invitationHash(value string) string {
	hash := sha256.Sum256([]byte(value))
	return hex.EncodeToString(hash[:])
}

func (s *Server) redeemInvitation(w http.ResponseWriter, r *http.Request) {
	a, err := s.client.Authenticate(r)
	if err != nil {
		domainhttp.WriteError(w, err)
		return
	}
	if a.AccountType != "candidate" || a.DemoReadOnly {
		domainhttp.WriteError(w, &domainhttp.Error{Status: 403, Code: "FORBIDDEN", Message: "Candidate access required"})
		return
	}
	value := r.PathValue("token")
	column := "token_hash"
	if value == "" {
		var input struct {
			Passcode string `json:"passcode"`
		}
		if !domainhttp.DecodeJSON(w, r, &input) {
			return
		}
		value = strings.ToUpper(strings.TrimSpace(input.Passcode))
		column = "passcode_hash"
	}
	var id, jobID string
	err = s.pool.QueryRow(r.Context(), `UPDATE invitation i SET redeeming_user_id=$1 FROM job j WHERE i.job_id=j.id AND i.`+column+`=$2 AND i.revoked_at IS NULL AND i.expires_at>now() AND i.resume_submission_id IS NULL AND (i.redeeming_user_id IS NULL OR i.redeeming_user_id=$1) AND j.application_opens_at IS NOT NULL AND j.application_closes_at IS NOT NULL AND j.application_opens_at<=now() AND j.application_closes_at>now() RETURNING i.id,i.job_id`, a.UserID, invitationHash(value)).Scan(&id, &jobID)
	if errors.Is(err, pgx.ErrNoRows) {
		domainhttp.WriteError(w, &domainhttp.Error{Status: 410, Code: "INVITATION_UNAVAILABLE", Message: "Invitation is unavailable or applications are closed"})
		return
	}
	if err != nil {
		domainhttp.WriteError(w, err)
		return
	}
	domainhttp.WriteJSON(w, 200, map[string]string{"jobId": jobID, "invitationId": id})
}

func (s *Server) invitedSubmissionForm(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 22<<20)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		domainhttp.WriteError(w, &domainhttp.Error{Status: 400, Code: "INVALID_REQUEST", Message: "Invalid resume upload"})
		return
	}
	defer r.MultipartForm.RemoveAll()
	value := r.FormValue("invitation_token")
	if value == "" {
		domainhttp.WriteError(w, &domainhttp.Error{Status: 400, Code: "INVALID_REQUEST", Message: "Invitation required"})
		return
	}
	r.SetPathValue("token", value)
	s.invitedSubmission(w, r)
}

func (s *Server) evaluationStatus(r *http.Request, id string) (map[string]any, error) {
	var result map[string]any
	err := s.client.Call(r.Context(), "evaluation", "/internal/evaluation/submissions/"+url.PathEscape(id), nil, &result)
	var failure *domainhttp.Error
	if errors.As(err, &failure) && failure.Status == 404 {
		return map[string]any{"status": "queued"}, nil
	}
	return result, err
}

func (s *Server) internalHold(w http.ResponseWriter, r *http.Request) {
	if !s.internal(w, r) {
		return
	}
	key := r.PathValue("operationKey")
	if !strings.HasPrefix(key, "submission:") || strings.TrimPrefix(key, "submission:") == "" {
		domainhttp.WriteError(w, &domainhttp.Error{Status: 400, Code: "INVALID_REQUEST", Message: "Invalid submission operation"})
		return
	}
	id := strings.TrimPrefix(key, "submission:")
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		domainhttp.WriteError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	var locked bool
	if err := tx.QueryRow(r.Context(), `SELECT pg_try_advisory_xact_lock(hashtextextended($1,0))`, key).Scan(&locked); err != nil {
		domainhttp.WriteError(w, err)
		return
	}
	if !locked {
		domainhttp.WriteJSON(w, 200, map[string]any{"attached": true, "state": "active"})
		return
	}
	var attached bool
	err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM resume_submission WHERE id=$1) OR EXISTS(SELECT 1 FROM service_outbox WHERE owner_service='hiring' AND subject=$1 AND published_at IS NULL)`, id).Scan(&attached)
	if err != nil {
		domainhttp.WriteError(w, err)
		return
	}
	state := "absent"
	if !attached {
		_, err = tx.Exec(r.Context(), `UPDATE submission_intake SET state='cancelled',updated_at=now() WHERE submission_id=$1 AND state='pending'`, id)
		if err == nil {
			err = tx.Commit(r.Context())
		}
		if err != nil {
			domainhttp.WriteError(w, err)
			return
		}
	}
	if attached {
		state = "active"
		status, err := s.evaluationStatus(r, id)
		if err != nil {
			domainhttp.WriteError(w, err)
			return
		}
		if status["status"] == "complete" || status["status"] == "completed" {
			state = "completed"
		} else if status["status"] == "failed" {
			state = "failed"
		}
	}
	domainhttp.WriteJSON(w, 200, map[string]any{"attached": attached, "state": state})
}

func (s *Server) receipts(w http.ResponseWriter, r *http.Request) {
	a, err := s.client.Authenticate(r)
	if err != nil {
		domainhttp.WriteError(w, err)
		return
	}
	if a.AccountType != "candidate" {
		domainhttp.WriteError(w, &domainhttp.Error{Status: 403, Code: "FORBIDDEN", Message: "Candidate account required"})
		return
	}
	rows, err := s.pool.Query(r.Context(), `SELECT rs.id,j.id,j.title,rs.created_at FROM resume_submission rs JOIN job j ON j.id=rs.job_id WHERE rs.submitting_user_id=$1 ORDER BY rs.created_at DESC LIMIT 100`, a.UserID)
	if err != nil {
		domainhttp.WriteError(w, err)
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var submissionID, jobID, title string
		var received time.Time
		if err := rows.Scan(&submissionID, &jobID, &title, &received); err != nil {
			domainhttp.WriteError(w, err)
			return
		}
		items = append(items, map[string]any{"submissionId": submissionID, "jobId": jobID, "jobTitle": title, "receivedAt": received})
	}
	if err := rows.Err(); err != nil {
		domainhttp.WriteError(w, err)
		return
	}
	domainhttp.WriteJSON(w, 200, items)
}
