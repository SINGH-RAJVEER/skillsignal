package httpapi

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/skillsignal/backend/config"
	"github.com/skillsignal/backend/documents"
	"github.com/skillsignal/backend/domainhttp"
	"github.com/skillsignal/backend/ids"
	"github.com/skillsignal/contracts/events"
	"github.com/skillsignal/contracts/jobs"
)

type Server struct {
	pool   *pgxpool.Pool
	client *domainhttp.Client
	secret string
}

func Register(mux *http.ServeMux, pool *pgxpool.Pool, settings config.Settings) {
	s := &Server{pool: pool, client: domainhttp.New(settings), secret: settings.JWTSecret}
	mux.HandleFunc("GET /api/organizations/{organization}/jobs", s.jobs)
	mux.HandleFunc("POST /api/organizations/{organization}/jobs", s.jobs)
	mux.HandleFunc("POST /api/jobs", s.createJobForm)
	mux.HandleFunc("GET /api/jobs/{job}", s.job)
	mux.HandleFunc("PATCH /api/jobs/{job}", s.job)
	mux.HandleFunc("POST /api/jobs/{job}/versions", s.jobVersion)
	mux.HandleFunc("GET /api/jobs/{job}/versions/{version}", s.jobVersion)
	mux.HandleFunc("POST /api/jobs/{job}/versions/{version}/confirm", s.confirmVersion)
	mux.HandleFunc("POST /api/jobs/{job}/requirements", s.confirmCurrentRequirements)
	mux.HandleFunc("PUT /api/jobs/{job}/application-window", s.applicationWindow)
	mux.HandleFunc("POST /api/jobs/{job}/invitations", s.createInvitation)
	mux.HandleFunc("DELETE /api/invitations/{id}", s.revokeInvitation)
	mux.HandleFunc("GET /api/invitations/{token}", s.inspectInvitation)
	mux.HandleFunc("POST /api/invitations/{token}/submissions", s.invitedSubmission)
	mux.HandleFunc("POST /api/invitations/{token}/redeem", s.redeemInvitation)
	mux.HandleFunc("POST /api/invitations/redeem", s.redeemInvitation)
	mux.HandleFunc("POST /api/jobs/{job}/resumes", s.invitedSubmissionForm)
	mux.HandleFunc("POST /api/jobs/{job}/submissions", s.directSubmission)
	mux.HandleFunc("POST /api/organizations/{organization}/jobs/{job}/resume-batches", s.batchSubmission)
	mux.HandleFunc("POST /api/jobs/{job}/resume-batches", s.batchSubmission)
	mux.HandleFunc("POST /api/jobs/{job}/resume-batches/files", s.batchSubmission)
	mux.HandleFunc("GET /api/jobs/{job}/submissions", s.submissions)
	mux.HandleFunc("GET /api/submissions/{submission}", s.submission)
	mux.HandleFunc("GET /api/submissions/receipts", s.receipts)
	mux.HandleFunc("GET /api/organizations/{organization}/jobs/{job}/submissions", s.submissions)
	mux.HandleFunc("POST /api/review-decisions", s.reviewDecision)
	mux.HandleFunc("GET /api/review-decisions", s.reviewDecisions)
	mux.HandleFunc("GET /internal/hiring/jobs/{id}", s.internalJob)
	mux.HandleFunc("GET /internal/hiring/submissions/{id}", s.internalSubmission)
	mux.HandleFunc("POST /internal/hiring/purge-documents", s.internalPurgeDocuments)
	mux.HandleFunc("POST /internal/hiring/prepare-purge-documents", s.preparePurgeDocuments)
	mux.HandleFunc("GET /internal/hiring/holds/{operationKey}", s.internalHold)
	mux.HandleFunc("POST /internal/hiring/demo", s.demo)
}

func (s *Server) access(w http.ResponseWriter, r *http.Request, organization string, write bool) (domainhttp.Access, bool) {
	a, err := s.client.Authenticate(r)
	if err != nil {
		domainhttp.WriteError(w, err)
		return domainhttp.Access{}, false
	}
	if a.AccountType != "employer" || a.Role(organization) == "" {
		domainhttp.WriteError(w, &domainhttp.Error{Status: 404, Code: "NOT_FOUND", Message: "Organization or job not found"})
		return a, false
	}
	if write && (a.DemoReadOnly || (a.Role(organization) != "owner" && a.Role(organization) != "recruiter")) {
		domainhttp.WriteError(w, &domainhttp.Error{Status: 403, Code: "FORBIDDEN", Message: "Recruiter access required"})
		return a, false
	}
	return a, true
}

func (s *Server) jobAccess(w http.ResponseWriter, r *http.Request, jobID string, write bool) (domainhttp.Access, string, bool) {
	var organization string
	err := s.pool.QueryRow(r.Context(), `SELECT organization_id FROM job WHERE id=$1`, jobID).Scan(&organization)
	if errors.Is(err, pgx.ErrNoRows) {
		domainhttp.WriteError(w, &domainhttp.Error{Status: 404, Code: "NOT_FOUND", Message: "Job not found"})
		return domainhttp.Access{}, "", false
	}
	if err != nil {
		domainhttp.WriteError(w, err)
		return domainhttp.Access{}, "", false
	}
	a, ok := s.access(w, r, organization, write)
	return a, organization, ok
}

func (s *Server) jobs(w http.ResponseWriter, r *http.Request) {
	org := r.PathValue("organization")
	a, ok := s.access(w, r, org, r.Method == http.MethodPost)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		rows, err := s.pool.Query(r.Context(), `SELECT j.id,j.organization_id,j.title,j.application_opens_at,j.application_closes_at,j.created_at,j.updated_at,(SELECT v.id FROM job_version v WHERE v.job_id=j.id ORDER BY v.version DESC LIMIT 1),(SELECT v.version FROM job_version v WHERE v.job_id=j.id ORDER BY v.version DESC LIMIT 1),(SELECT v.confirmed_at FROM job_version v WHERE v.job_id=j.id ORDER BY v.version DESC LIMIT 1) FROM job j WHERE j.organization_id=$1 ORDER BY j.created_at DESC`, org)
		if err != nil {
			domainhttp.WriteError(w, err)
			return
		}
		defer rows.Close()
		items := make([]map[string]any, 0)
		for rows.Next() {
			var id, organization, title string
			var versionID *string
			var opens, closes, confirmed *time.Time
			var version *int
			var created, updated time.Time
			if err := rows.Scan(&id, &organization, &title, &opens, &closes, &created, &updated, &versionID, &version, &confirmed); err != nil {
				domainhttp.WriteError(w, err)
				return
			}
			items = append(items, map[string]any{"id": id, "organizationId": organization, "title": title, "applicationOpensAt": opens, "applicationClosesAt": closes, "createdAt": created, "updatedAt": updated, "versionId": versionID, "confirmed": confirmed != nil, "currentVersionId": versionID, "currentVersion": version, "confirmedAt": confirmed})
		}
		if err := rows.Err(); err != nil {
			domainhttp.WriteError(w, err)
			return
		}
		domainhttp.WriteJSON(w, 200, items)
	case http.MethodPost:
		var input struct {
			Title               string     `json:"title"`
			Description         string     `json:"description"`
			ApplicationOpensAt  *time.Time `json:"applicationOpensAt"`
			ApplicationClosesAt *time.Time `json:"applicationClosesAt"`
		}
		if !domainhttp.DecodeJSON(w, r, &input) {
			return
		}
		input.Title = strings.TrimSpace(input.Title)
		input.Description = strings.TrimSpace(input.Description)
		if input.Title == "" || len(input.Title) > 200 || input.Description == "" || len(input.Description) > 1_000_000 || (input.ApplicationOpensAt != nil && input.ApplicationClosesAt != nil && !input.ApplicationClosesAt.After(*input.ApplicationOpensAt)) {
			domainhttp.WriteError(w, &domainhttp.Error{Status: 400, Code: "INVALID_REQUEST", Message: "A title, description, and valid application window are required"})
			return
		}
		if a.DemoReadOnly {
			domainhttp.WriteError(w, &domainhttp.Error{Status: 403, Code: "DEMO_ACCOUNT", Message: "Demo accounts are read-only"})
			return
		}
		id, version := ids.New(), ids.New()
		now := time.Now().UTC()
		tx, err := s.pool.Begin(r.Context())
		if err != nil {
			domainhttp.WriteError(w, err)
			return
		}
		defer tx.Rollback(r.Context())
		_, err = tx.Exec(r.Context(), `INSERT INTO job(id,organization_id,title,application_opens_at,application_closes_at) VALUES($1,$2,$3,$4,$5)`, id, org, input.Title, input.ApplicationOpensAt, input.ApplicationClosesAt)
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO job_version(id,job_id,version,source_text,source_media_type) VALUES($1,$2,1,$3,'text/plain')`, version, id, input.Description)
		}
		if err == nil {
			_, err = tx.Exec(r.Context(), `INSERT INTO processing_job(id,type,payload_reference,idempotency_key) VALUES($1,$2,$3,$4)`, ids.New(), jobs.JobDescriptionParse, version, "job-description-parse:"+version)
		}
		if err == nil {
			err = tx.Commit(r.Context())
		}
		if err != nil {
			domainhttp.WriteError(w, err)
			return
		}
		domainhttp.WriteJSON(w, 201, map[string]any{"id": id, "organizationId": org, "title": input.Title, "versionId": version, "version": 1, "draftStatus": "queued", "createdAt": now})
	default:
		w.WriteHeader(405)
	}
}

func (s *Server) job(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("job")
	_, org, ok := s.jobAccess(w, r, id, r.Method == http.MethodPatch)
	if !ok {
		return
	}
	if r.Method == http.MethodGet {
		s.jobDetail(w, r, id, org)
		return
	}
	if r.Method != http.MethodPatch {
		w.WriteHeader(405)
		return
	}
	var in struct {
		Title               *string    `json:"title"`
		ApplicationOpensAt  *time.Time `json:"applicationOpensAt"`
		ApplicationClosesAt *time.Time `json:"applicationClosesAt"`
	}
	if !domainhttp.DecodeJSON(w, r, &in) {
		return
	}
	if in.Title != nil {
		*in.Title = strings.TrimSpace(*in.Title)
		if *in.Title == "" || len(*in.Title) > 200 {
			domainhttp.WriteError(w, &domainhttp.Error{Status: 400, Code: "INVALID_REQUEST", Message: "Job title is invalid"})
			return
		}
	}
	_, err := s.pool.Exec(r.Context(), `UPDATE job SET title=COALESCE($1,title),application_opens_at=COALESCE($2,application_opens_at),application_closes_at=COALESCE($3,application_closes_at),updated_at=now() WHERE id=$4`, in.Title, in.ApplicationOpensAt, in.ApplicationClosesAt, id)
	if err != nil {
		domainhttp.WriteError(w, err)
		return
	}
	domainhttp.WriteJSON(w, 200, map[string]any{"id": id, "updated": true})
}

func (s *Server) jobVersion(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("job")
	_, _, ok := s.jobAccess(w, r, jobID, r.Method == http.MethodPost)
	if !ok {
		return
	}
	if r.Method == http.MethodGet {
		var raw []byte
		err := s.pool.QueryRow(r.Context(), `SELECT jsonb_build_object('id',v.id,'jobId',v.job_id,'version',v.version,'sourceText',v.source_text,'normalizedText',v.normalized_text,'draftRequirements',v.draft_requirements,'confirmedAt',v.confirmed_at,'createdAt',v.created_at) FROM job_version v WHERE v.job_id=$1 AND v.id=$2`, jobID, r.PathValue("version")).Scan(&raw)
		if err != nil {
			domainhttp.WriteError(w, &domainhttp.Error{Status: 404, Code: "NOT_FOUND", Message: "Job version not found"})
			return
		}
		domainhttp.WriteJSON(w, 200, json.RawMessage(raw))
		return
	}
	var in struct {
		Description string `json:"description"`
	}
	if !domainhttp.DecodeJSON(w, r, &in) {
		return
	}
	in.Description = strings.TrimSpace(in.Description)
	if in.Description == "" || len(in.Description) > 1_000_000 {
		domainhttp.WriteError(w, &domainhttp.Error{Status: 400, Code: "INVALID_REQUEST", Message: "Job description is required"})
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		domainhttp.WriteError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	var version int
	err = tx.QueryRow(r.Context(), `SELECT COALESCE(MAX(version),0)+1 FROM job_version WHERE job_id=$1`, jobID).Scan(&version)
	id := ids.New()
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO job_version(id,job_id,version,source_text,source_media_type) VALUES($1,$2,$3,$4,'text/plain')`, id, jobID, version, in.Description)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO processing_job(id,type,payload_reference,idempotency_key) VALUES($1,$2,$3,$4)`, ids.New(), jobs.JobDescriptionParse, id, "job-description-parse:"+id)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		domainhttp.WriteError(w, err)
		return
	}
	domainhttp.WriteJSON(w, 202, map[string]any{"id": id, "jobId": jobID, "version": version, "draftStatus": "queued"})
}

func (s *Server) confirmVersion(w http.ResponseWriter, r *http.Request) {
	jobID, versionID := r.PathValue("job"), r.PathValue("version")
	_, _, ok := s.jobAccess(w, r, jobID, true)
	if !ok {
		return
	}
	var in struct {
		Requirements []struct {
			StableID       string          `json:"stableId"`
			Kind           string          `json:"kind"`
			Weight         int             `json:"weight"`
			NormalizedText string          `json:"normalizedText"`
			Category       string          `json:"category"`
			SourceModality string          `json:"sourceModality"`
			Assessability  string          `json:"assessability"`
			Predicate      json.RawMessage `json:"predicate"`
			Aliases        json.RawMessage `json:"aliases"`
			SourceEvidence json.RawMessage `json:"sourceEvidence"`
		} `json:"requirements"`
	}
	if !domainhttp.DecodeJSON(w, r, &in) {
		return
	}
	if len(in.Requirements) == 0 || len(in.Requirements) > 50 {
		domainhttp.WriteError(w, &domainhttp.Error{Status: 400, Code: "INVALID_REQUEST", Message: "Confirm between 1 and 50 requirements"})
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		domainhttp.WriteError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	var confirmed *time.Time
	err = tx.QueryRow(r.Context(), `SELECT confirmed_at FROM job_version WHERE id=$1 AND job_id=$2 FOR UPDATE`, versionID, jobID).Scan(&confirmed)
	if err != nil {
		domainhttp.WriteError(w, &domainhttp.Error{Status: 404, Code: "NOT_FOUND", Message: "Job version not found"})
		return
	}
	if confirmed != nil {
		domainhttp.WriteError(w, &domainhttp.Error{Status: 409, Code: "VERSION_IMMUTABLE", Message: "Confirmed job versions cannot be changed"})
		return
	}
	seen := make(map[string]bool)
	for _, q := range in.Requirements {
		q.StableID = strings.TrimSpace(q.StableID)
		q.NormalizedText = strings.TrimSpace(q.NormalizedText)
		if q.StableID == "" || seen[q.StableID] || q.NormalizedText == "" || q.Weight < 1 || q.Weight > 100 || !oneOf(q.Kind, "required", "preferred", "ignored", "hard_gate") || (q.Assessability != "" && !oneOf(q.Assessability, "resume_evidence", "candidate_attestation", "recruiter_review", "prohibited", "unclear")) || (q.Kind == "hard_gate" && q.Assessability != "resume_evidence") || q.Assessability == "prohibited" {
			domainhttp.WriteError(w, &domainhttp.Error{Status: 400, Code: "INVALID_REQUEST", Message: "A requirement is invalid"})
			return
		}
		seen[q.StableID] = true
		if (len(q.Predicate) > 0 && !json.Valid(q.Predicate)) || (len(q.Aliases) > 0 && !json.Valid(q.Aliases)) || (len(q.SourceEvidence) > 0 && !json.Valid(q.SourceEvidence)) {
			domainhttp.WriteError(w, &domainhttp.Error{Status: 400, Code: "INVALID_REQUEST", Message: "Requirement evidence is invalid"})
			return
		}
		_, err = tx.Exec(r.Context(), `INSERT INTO job_requirement(id,job_version_id,stable_id,kind,weight,normalized_text,category,source_modality,assessability,predicate,aliases,source_evidence,confirmed_at) VALUES($1,$2,$3,$4,$5,$6,COALESCE(NULLIF($7,''),'other'),COALESCE(NULLIF($8,''),'unclear'),COALESCE(NULLIF($9,''),'unclear'),$10::jsonb,$11::jsonb,$12::jsonb,now())`, ids.New(), versionID, q.StableID, q.Kind, q.Weight, q.NormalizedText, q.Category, q.SourceModality, q.Assessability, defaultJSON(q.Predicate, `{}`), defaultJSON(q.Aliases, `[]`), defaultJSON(q.SourceEvidence, `[]`))
		if err != nil {
			domainhttp.WriteError(w, err)
			return
		}
	}
	_, err = tx.Exec(r.Context(), `UPDATE job_version SET confirmed_at=now() WHERE id=$1`, versionID)
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		domainhttp.WriteError(w, err)
		return
	}
	domainhttp.WriteJSON(w, 200, map[string]any{"jobId": jobID, "versionId": versionID, "confirmed": true})
}

func oneOf(value string, options ...string) bool {
	for _, option := range options {
		if value == option {
			return true
		}
	}
	return false
}
func defaultJSON(raw json.RawMessage, fallback string) string {
	if len(raw) == 0 {
		return fallback
	}
	return string(raw)
}
func (s *Server) createInvitation(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("job")
	a, org, ok := s.jobAccess(w, r, jobID, true)
	if !ok {
		return
	}
	var in struct {
		ExpiresAt           time.Time  `json:"expiresAt"`
		ExpiresInHours      int        `json:"expires_in_hours"`
		ApplicationOpensAt  *time.Time `json:"applicationOpensAt"`
		ApplicationClosesAt *time.Time `json:"applicationClosesAt"`
		WithPasscode        bool       `json:"withPasscode"`
	}
	if !domainhttp.DecodeJSON(w, r, &in) {
		return
	}
	now := time.Now().UTC()
	if in.ExpiresAt.IsZero() && in.ExpiresInHours > 0 && in.ExpiresInHours <= 90*24 {
		in.ExpiresAt = now.Add(time.Duration(in.ExpiresInHours) * time.Hour)
		in.WithPasscode = true
	}
	if in.ExpiresAt.IsZero() || !in.ExpiresAt.After(now) || in.ExpiresAt.After(now.AddDate(0, 0, 90)) {
		domainhttp.WriteError(w, &domainhttp.Error{Status: 400, Code: "INVALID_REQUEST", Message: "Invitation expiry must be within 90 days"})
		return
	}
	token := ids.New() + ids.New()
	sum := sha256.Sum256([]byte(token))
	id := ids.New()
	var code string
	if in.WithPasscode {
		code = ids.Passcode()
	}
	var passHash any
	if code != "" {
		p := sha256.Sum256([]byte(code))
		passHash = hex.EncodeToString(p[:])
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		domainhttp.WriteError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	_, err = tx.Exec(r.Context(), `INSERT INTO invitation(id,job_id,creator_user_id,token_hash,passcode_hash,expires_at) VALUES($1,$2,$3,$4,NULLIF($5,''),$6)`, id, jobID, a.UserID, hex.EncodeToString(sum[:]), passHash, in.ExpiresAt)
	if err == nil && (in.ApplicationOpensAt != nil || in.ApplicationClosesAt != nil) {
		_, err = tx.Exec(r.Context(), `UPDATE job SET application_opens_at=COALESCE($1,application_opens_at),application_closes_at=COALESCE($2,application_closes_at),updated_at=now() WHERE id=$3`, in.ApplicationOpensAt, in.ApplicationClosesAt, jobID)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		domainhttp.WriteError(w, err)
		return
	}
	out := map[string]any{"id": id, "jobId": jobID, "organizationId": org, "token": token, "expiresAt": in.ExpiresAt}
	if code != "" {
		out["passcode"] = code
	}
	domainhttp.WriteJSON(w, 201, out)
}
func (s *Server) revokeInvitation(w http.ResponseWriter, r *http.Request) {
	a, err := s.client.Authenticate(r)
	if err != nil {
		domainhttp.WriteError(w, err)
		return
	}
	id := r.PathValue("id")
	var org string
	err = s.pool.QueryRow(r.Context(), `SELECT j.organization_id FROM invitation i JOIN job j ON j.id=i.job_id WHERE i.id=$1`, id).Scan(&org)
	if err != nil {
		domainhttp.WriteError(w, &domainhttp.Error{Status: 404, Code: "NOT_FOUND", Message: "Invitation not found"})
		return
	}
	if _, ok := s.access(w, r, org, true); !ok {
		return
	}
	if a.DemoReadOnly {
		domainhttp.WriteError(w, &domainhttp.Error{Status: 403, Code: "DEMO_ACCOUNT", Message: "Demo accounts are read-only"})
		return
	}
	_, err = s.pool.Exec(r.Context(), `UPDATE invitation SET revoked_at=COALESCE(revoked_at,now()) WHERE id=$1`, id)
	if err != nil {
		domainhttp.WriteError(w, err)
		return
	}
	domainhttp.WriteJSON(w, 200, map[string]bool{"revoked": true})
}
func (s *Server) inspectInvitation(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	sum := sha256.Sum256([]byte(token))
	var id, jobID, title string
	var expires time.Time
	var opens, closes *time.Time
	err := s.pool.QueryRow(r.Context(), `SELECT i.id,j.id,j.title,i.expires_at,j.application_opens_at,j.application_closes_at FROM invitation i JOIN job j ON j.id=i.job_id WHERE i.token_hash=$1 AND i.revoked_at IS NULL AND i.resume_submission_id IS NULL AND i.expires_at>now()`, hex.EncodeToString(sum[:])).Scan(&id, &jobID, &title, &expires, &opens, &closes)
	if err != nil {
		domainhttp.WriteError(w, &domainhttp.Error{Status: 404, Code: "NOT_FOUND", Message: "Invitation is unavailable"})
		return
	}
	now := time.Now()
	if opens == nil || closes == nil || now.Before(*opens) || !now.Before(*closes) {
		domainhttp.WriteError(w, &domainhttp.Error{Status: 410, Code: "APPLICATIONS_CLOSED", Message: "Applications are not open"})
		return
	}
	domainhttp.WriteJSON(w, 200, map[string]any{"jobId": jobID, "jobTitle": title, "expiresAt": expires, "uploadOnly": true})
}

func (s *Server) directSubmission(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("job")
	_, org, ok := s.jobAccess(w, r, jobID, true)
	if !ok {
		return
	}
	files, err := uploadFiles(w, r)
	if err != nil {
		return
	}
	entries, err := documents.InspectResumeFiles(files)
	if err != nil {
		domainhttp.WriteError(w, &domainhttp.Error{Status: 400, Code: "INVALID_DOCUMENT", Message: err.Error()})
		return
	}
	out, err := s.acceptEntries(r, org, jobID, entries, "", "", ids.New())
	if err != nil {
		domainhttp.WriteError(w, err)
		return
	}
	domainhttp.WriteJSON(w, 202, out)
}
func (s *Server) batchSubmission(w http.ResponseWriter, r *http.Request) {
	jobID, org := r.PathValue("job"), r.PathValue("organization")
	_, actual, ok := s.jobAccess(w, r, jobID, true)
	if !ok {
		return
	}
	if org != "" && actual != org {
		domainhttp.WriteError(w, &domainhttp.Error{Status: 404, Code: "NOT_FOUND", Message: "Job not found"})
		return
	}
	org = actual
	r.Body = http.MaxBytesReader(w, r.Body, documents.MaxBatchUncompressedBytes+(2<<20))
	if err := r.ParseMultipartForm(502 << 20); err != nil {
		domainhttp.WriteError(w, &domainhttp.Error{Status: 400, Code: "INVALID_REQUEST", Message: "Invalid resume batch"})
		return
	}
	defer r.MultipartForm.RemoveAll()
	if archives := r.MultipartForm.File["archive"]; len(archives) > 0 {
		r.MultipartForm.File["files"] = archives
	}
	var entries []documents.BatchEntry
	var e error
	if files := r.MultipartForm.File["files"]; len(files) == 1 && strings.EqualFold(strings.TrimSpace(files[0].Filename[strings.LastIndex(files[0].Filename, ".")+1:]), "zip") {
		data, name, err := readFile(files[0], documents.MaxBatchUncompressedBytes)
		if err != nil {
			domainhttp.WriteError(w, &domainhttp.Error{Status: 400, Code: "INVALID_DOCUMENT", Message: err.Error()})
			return
		}
		if documents.MediaTypeForName(name) != "" {
			domainhttp.WriteError(w, &domainhttp.Error{Status: 400, Code: "INVALID_DOCUMENT", Message: "ZIP archives must be uploaded as an archive file"})
			return
		}
		entries, e = documents.InspectResumeZip(data)
	} else {
		files := make([]documents.NamedContent, 0)
		for _, f := range r.MultipartForm.File["files"] {
			data, name, err := readFile(f, documents.MaxResumeBytes)
			if err != nil {
				files = append(files, documents.NamedContent{Name: f.Filename, Content: nil})
				continue
			}
			files = append(files, documents.NamedContent{Name: name, Content: data})
		}
		entries, e = documents.InspectResumeFiles(files)
	}
	if e != nil {
		domainhttp.WriteError(w, &domainhttp.Error{Status: 400, Code: "INVALID_DOCUMENT", Message: e.Error()})
		return
	}
	out, err := s.acceptEntries(r, org, jobID, entries, "", "", ids.New())
	if err != nil {
		domainhttp.WriteError(w, err)
		return
	}
	domainhttp.WriteJSON(w, 202, out)
}

func uploadFiles(w http.ResponseWriter, r *http.Request) ([]documents.NamedContent, error) {
	r.Body = http.MaxBytesReader(w, r.Body, documents.MaxBatchUncompressedBytes+(2<<20))
	if err := r.ParseMultipartForm(21 << 20); err != nil {
		domainhttp.WriteError(w, &domainhttp.Error{Status: 400, Code: "INVALID_REQUEST", Message: "Invalid resume upload"})
		return nil, err
	}
	defer r.MultipartForm.RemoveAll()
	files := make([]documents.NamedContent, 0)
	for _, header := range r.MultipartForm.File["files"] {
		data, name, err := readFile(header, documents.MaxResumeBytes)
		if err != nil {
			files = append(files, documents.NamedContent{Name: header.Filename})
			continue
		}
		files = append(files, documents.NamedContent{Name: name, Content: data})
	}
	if len(files) == 0 {
		if h := r.MultipartForm.File["file"]; len(h) == 1 {
			data, name, err := readFile(h[0], documents.MaxResumeBytes)
			if err == nil {
				files = append(files, documents.NamedContent{Name: name, Content: data})
			}
		}
	}
	return files, nil
}
func readFile(header *multipart.FileHeader, limit int64) ([]byte, string, error) {
	f, err := header.Open()
	if err != nil {
		return nil, header.Filename, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err == nil && int64(len(data)) > limit {
		err = fmt.Errorf("file exceeds allowed size")
	}
	return data, header.Filename, err
}

func (s *Server) invitedSubmission(w http.ResponseWriter, r *http.Request) {
	token := r.PathValue("token")
	a, err := s.client.Authenticate(r)
	if err != nil {
		domainhttp.WriteError(w, err)
		return
	}
	if a.AccountType != "candidate" || a.DemoReadOnly {
		domainhttp.WriteError(w, &domainhttp.Error{Status: 403, Code: "FORBIDDEN", Message: "Candidate account required"})
		return
	}
	entries, err := uploadFiles(w, r)
	if err != nil {
		return
	}
	if len(entries) != 1 {
		domainhttp.WriteError(w, &domainhttp.Error{Status: 400, Code: "INVALID_REQUEST", Message: "Upload one resume for this invitation"})
		return
	}
	entry := entries[0]
	if reason := documents.FileEntryReason(entry.Name, entry.Content); reason != "" {
		domainhttp.WriteError(w, &domainhttp.Error{Status: 400, Code: "INVALID_DOCUMENT", Message: reason})
		return
	}
	var id, jobID, org string
	var expires time.Time
	var opens, closes *time.Time
	err = s.pool.QueryRow(r.Context(), `SELECT i.id,j.id,j.organization_id,i.expires_at,j.application_opens_at,j.application_closes_at FROM invitation i JOIN job j ON j.id=i.job_id WHERE (i.token_hash=$1 OR i.passcode_hash=$2) AND i.revoked_at IS NULL AND i.resume_submission_id IS NULL`, invitationHash(token), invitationHash(strings.ToUpper(strings.TrimSpace(token)))).Scan(&id, &jobID, &org, &expires, &opens, &closes)
	if err != nil {
		domainhttp.WriteError(w, &domainhttp.Error{Status: 404, Code: "NOT_FOUND", Message: "Invitation is unavailable"})
		return
	}
	now := time.Now().UTC()
	if (r.PathValue("job") != "" && r.PathValue("job") != jobID) || !expires.After(now) || opens == nil || closes == nil || now.Before(*opens) || !now.Before(*closes) {
		domainhttp.WriteError(w, &domainhttp.Error{Status: 410, Code: "APPLICATIONS_CLOSED", Message: "Applications are not open"})
		return
	}
	claimed, err := s.pool.Exec(r.Context(), `UPDATE invitation SET redeeming_user_id=$1 WHERE id=$2 AND resume_submission_id IS NULL AND (redeeming_user_id IS NULL OR redeeming_user_id=$1)`, a.UserID, id)
	if err != nil {
		domainhttp.WriteError(w, err)
		return
	}
	if claimed.RowsAffected() == 0 {
		domainhttp.WriteError(w, &domainhttp.Error{Status: 409, Code: "INVITATION_REDEEMED", Message: "Invitation has already been used"})
		return
	}
	out, err := s.acceptEntries(r, org, jobID, []documents.BatchEntry{{Name: entry.Name, Content: entry.Content}}, a.UserID, id, ids.New())
	if err != nil {
		domainhttp.WriteError(w, err)
		return
	}
	items, _ := out["accepted"].([]map[string]any)
	if len(items) == 0 {
		domainhttp.WriteError(w, &domainhttp.Error{Status: 402, Code: "INSUFFICIENT_POINTS", Message: "The employer cannot accept this submission"})
		return
	}
	domainhttp.WriteJSON(w, 202, map[string]any{"submissionId": items[0]["submissionId"], "receivedAt": time.Now().UTC()})
}

func nullString(v string) any {
	if v == "" {
		return nil
	}
	return v
}

func (s *Server) submissions(w http.ResponseWriter, r *http.Request) {
	jobID := r.PathValue("job")
	if jobID == "" {
		jobID = r.PathValue("job")
	}
	_, org, ok := s.jobAccess(w, r, jobID, false)
	if !ok {
		return
	}
	if requested := r.PathValue("organization"); requested != "" && requested != org {
		domainhttp.WriteError(w, &domainhttp.Error{Status: 404, Code: "NOT_FOUND", Message: "Job not found"})
		return
	}
	rows, err := s.pool.Query(r.Context(), `SELECT rs.id,rs.candidate_record_id,cr.full_name,cr.email,rs.resume_version_id,rs.created_at FROM resume_submission rs JOIN candidate_record cr ON cr.id=rs.candidate_record_id WHERE rs.organization_id=$1 AND rs.job_id=$2 ORDER BY rs.created_at DESC`, org, jobID)
	if err != nil {
		domainhttp.WriteError(w, err)
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id, candidate, version string
		var name, email *string
		var created time.Time
		if err := rows.Scan(&id, &candidate, &name, &email, &version, &created); err != nil {
			domainhttp.WriteError(w, err)
			return
		}
		status, err := s.evaluationStatus(r, id)
		if err != nil {
			domainhttp.WriteError(w, err)
			return
		}
		items = append(items, map[string]any{"id": id, "candidateRecordId": candidate, "candidateName": name, "candidateEmail": email, "resumeVersionId": version, "createdAt": created, "status": status["status"]})
	}
	if err := rows.Err(); err != nil {
		domainhttp.WriteError(w, err)
		return
	}
	domainhttp.WriteJSON(w, 200, items)
}
func (s *Server) submission(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("submission")
	var org, job, candidate, version string
	var name, email *string
	var created time.Time
	err := s.pool.QueryRow(r.Context(), `SELECT rs.organization_id,rs.job_id,rs.candidate_record_id,rs.resume_version_id,cr.full_name,cr.email,rs.created_at FROM resume_submission rs JOIN candidate_record cr ON cr.id=rs.candidate_record_id WHERE rs.id=$1`, id).Scan(&org, &job, &candidate, &version, &name, &email, &created)
	if err != nil {
		domainhttp.WriteError(w, &domainhttp.Error{Status: 404, Code: "NOT_FOUND", Message: "Submission not found"})
		return
	}
	if _, ok := s.access(w, r, org, false); !ok {
		return
	}
	status, err := s.evaluationStatus(r, id)
	if err != nil {
		domainhttp.WriteError(w, err)
		return
	}
	domainhttp.WriteJSON(w, 200, map[string]any{"id": id, "jobId": job, "candidateRecordId": candidate, "resumeVersionId": version, "candidateName": name, "candidateEmail": email, "createdAt": created, "status": status["status"]})
}

func (s *Server) reviewDecision(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	var in struct {
		EvaluationID      string `json:"evaluationId"`
		BatchEvaluationID string `json:"batchEvaluationId"`
		Eligibility       string `json:"eligibility"`
		Reason            string `json:"reason"`
	}
	if !domainhttp.DecodeJSON(w, r, &in) {
		return
	}
	if !oneOf(in.Eligibility, "eligible", "needs_review", "not_eligible") || strings.TrimSpace(in.Reason) == "" {
		domainhttp.WriteError(w, &domainhttp.Error{Status: 400, Code: "INVALID_REQUEST", Message: "Review eligibility and reason are required"})
		return
	}
	var evaluation struct {
		OrganizationID string `json:"organizationId"`
		BatchID        string `json:"batchEvaluationId"`
	}
	err := s.client.Call(r.Context(), "evaluation", "/internal/evaluation/evaluations/"+url.PathEscape(in.EvaluationID), nil, &evaluation)
	if err != nil {
		domainhttp.WriteError(w, &domainhttp.Error{Status: 404, Code: "NOT_FOUND", Message: "Batch evaluation not found"})
		return
	}
	if evaluation.OrganizationID == "" || evaluation.BatchID != in.BatchEvaluationID {
		domainhttp.WriteError(w, &domainhttp.Error{Status: 404, Code: "NOT_FOUND", Message: "Evaluation not found in this batch"})
		return
	}
	org := evaluation.OrganizationID
	a, ok := s.access(w, r, org, true)
	if !ok {
		return
	}
	id := ids.New()
	_, err = s.pool.Exec(r.Context(), `INSERT INTO review_decision(id,organization_id,batch_evaluation_id,evaluation_id,reviewer_user_id,eligibility,reason) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, org, in.BatchEvaluationID, in.EvaluationID, a.UserID, in.Eligibility, strings.TrimSpace(in.Reason))
	if err != nil {
		domainhttp.WriteError(w, err)
		return
	}
	domainhttp.WriteJSON(w, 201, map[string]any{"id": id, "eligibility": in.Eligibility, "reason": strings.TrimSpace(in.Reason)})
}
func (s *Server) reviewDecisions(w http.ResponseWriter, r *http.Request) {
	org := r.URL.Query().Get("organizationId")
	if _, ok := s.access(w, r, org, false); !ok {
		return
	}
	rows, err := s.pool.Query(r.Context(), `SELECT id,batch_evaluation_id,evaluation_id,reviewer_user_id,eligibility,reason,created_at FROM review_decision WHERE organization_id=$1 AND ($2='' OR evaluation_id=$2) ORDER BY created_at DESC`, org, r.URL.Query().Get("evaluationId"))
	if err != nil {
		domainhttp.WriteError(w, err)
		return
	}
	defer rows.Close()
	items := make([]map[string]any, 0)
	for rows.Next() {
		var id, batch, evaluation, reviewer, eligibility, reason string
		var created time.Time
		if err := rows.Scan(&id, &batch, &evaluation, &reviewer, &eligibility, &reason, &created); err != nil {
			domainhttp.WriteError(w, err)
			return
		}
		items = append(items, map[string]any{"id": id, "batchEvaluationId": batch, "evaluationId": evaluation, "reviewerUserId": reviewer, "eligibility": eligibility, "reason": reason, "createdAt": created})
	}
	if err := rows.Err(); err != nil {
		domainhttp.WriteError(w, err)
		return
	}
	domainhttp.WriteJSON(w, 200, items)
}

func Start(ctx context.Context, pool *pgxpool.Pool, settings config.Settings) {
	s := &Server{pool: pool, client: domainhttp.New(settings), secret: settings.JWTSecret}
	go s.dispatchOutbox(ctx)
}

func (s *Server) internal(w http.ResponseWriter, r *http.Request) bool {
	secret := r.Header.Get("X-Internal-Secret")
	if s.secret == "" || subtle.ConstantTimeCompare([]byte(secret), []byte(s.secret)) != 1 {
		domainhttp.WriteError(w, &domainhttp.Error{Status: 404, Code: "NOT_FOUND", Message: "Route not found"})
		return false
	}
	return true
}

func (s *Server) internalJob(w http.ResponseWriter, r *http.Request) {
	if !s.internal(w, r) {
		return
	}
	var raw []byte
	err := s.pool.QueryRow(r.Context(), `SELECT jsonb_build_object(
		'id',j.id,'organizationId',j.organization_id,'title',j.title,
		'jobVersionId',v.id,'requirements',COALESCE((SELECT jsonb_agg(jsonb_build_object('id',q.id,'stableId',q.stable_id,'normalizedText',q.normalized_text,'kind',q.kind,'weight',q.weight,'predicate',q.predicate,'aliases',q.aliases) ORDER BY q.stable_id) FROM job_requirement q WHERE q.job_version_id=v.id),'[]'::jsonb))
		FROM job j LEFT JOIN LATERAL (SELECT id FROM job_version WHERE job_id=j.id AND confirmed_at IS NOT NULL AND ($2='' OR id=$2) ORDER BY version DESC LIMIT 1) v ON true WHERE j.id=$1 AND ($2='' OR v.id IS NOT NULL)`, r.PathValue("id"), r.URL.Query().Get("versionId")).Scan(&raw)
	if err != nil {
		domainhttp.WriteError(w, &domainhttp.Error{Status: 404, Code: "NOT_FOUND", Message: "Confirmed job not found"})
		return
	}
	domainhttp.WriteJSON(w, 200, json.RawMessage(raw))
}

func (s *Server) internalSubmission(w http.ResponseWriter, r *http.Request) {
	if !s.internal(w, r) {
		return
	}
	var raw []byte
	err := s.pool.QueryRow(r.Context(), `SELECT jsonb_build_object('id',rs.id,'organizationId',rs.organization_id,'jobId',rs.job_id,'candidateRecordId',cr.id,'candidateName',cr.full_name,'candidateEmail',cr.email,'candidateLocation',cr.location,'resumeVersionId',rs.resume_version_id) FROM resume_submission rs JOIN candidate_record cr ON cr.id=rs.candidate_record_id WHERE rs.id=$1`, r.PathValue("id")).Scan(&raw)
	if err != nil {
		domainhttp.WriteError(w, &domainhttp.Error{Status: 404, Code: "NOT_FOUND", Message: "Submission not found"})
		return
	}
	domainhttp.WriteJSON(w, 200, json.RawMessage(raw))
}

func (s *Server) internalPurgeDocuments(w http.ResponseWriter, r *http.Request) {
	if !s.internal(w, r) {
		return
	}
	var input struct {
		DocumentIDs []string `json:"documentIds"`
		VersionIDs  []string `json:"versionIds"`
	}
	if !domainhttp.DecodeJSON(w, r, &input) {
		return
	}
	if len(input.DocumentIDs) > 500 || len(input.VersionIDs) > 500 {
		domainhttp.WriteError(w, &domainhttp.Error{Status: 400, Code: "INVALID_REQUEST", Message: "Purge request is too large"})
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		domainhttp.WriteError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	var candidates []string
	if len(input.VersionIDs) > 0 {
		rows, err := tx.Query(r.Context(), `SELECT DISTINCT candidate_record_id FROM resume_submission WHERE resume_version_id=ANY($1)`, input.VersionIDs)
		if err != nil {
			domainhttp.WriteError(w, err)
			return
		}
		for rows.Next() {
			var id string
			if err = rows.Scan(&id); err != nil {
				rows.Close()
				domainhttp.WriteError(w, err)
				return
			}
			candidates = append(candidates, id)
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			domainhttp.WriteError(w, err)
			return
		}
		rows.Close()
		if _, err = tx.Exec(r.Context(), `DELETE FROM resume_submission WHERE resume_version_id=ANY($1)`, input.VersionIDs); err != nil {
			domainhttp.WriteError(w, err)
			return
		}
	}
	if len(candidates) > 0 {
		if _, err = tx.Exec(r.Context(), `DELETE FROM candidate_record cr WHERE cr.id=ANY($1) AND NOT EXISTS(SELECT 1 FROM resume_submission rs WHERE rs.candidate_record_id=cr.id)`, candidates); err != nil {
			domainhttp.WriteError(w, err)
			return
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		domainhttp.WriteError(w, err)
		return
	}
	domainhttp.WriteJSON(w, 200, map[string]int{"deletedSubmissions": len(input.VersionIDs) + len(input.DocumentIDs)})
}

func (s *Server) dispatchOutbox(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		s.dispatchPending(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Server) dispatchPending(ctx context.Context) {
	rows, err := s.pool.Query(ctx, `SELECT id FROM service_outbox WHERE owner_service='hiring' AND published_at IS NULL ORDER BY created_at LIMIT 20`)
	if err != nil {
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) != nil {
			rows.Close()
			return
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return
	}
	for _, id := range ids {
		s.dispatchMessage(ctx, id)
	}
}

func (s *Server) dispatchMessage(ctx context.Context, id string) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return
	}
	defer tx.Rollback(ctx)
	var kind string
	var version int
	var payload []byte
	err = tx.QueryRow(ctx, `SELECT event_type,event_version,payload FROM service_outbox WHERE id=$1 AND owner_service='hiring' AND published_at IS NULL FOR UPDATE SKIP LOCKED`, id).Scan(&kind, &version, &payload)
	if err != nil {
		return
	}
	var input map[string]any
	if kind != events.EvaluationRequested || version != events.CurrentVersion || json.Unmarshal(payload, &input) != nil {
		_, _ = tx.Exec(ctx, `UPDATE service_outbox SET attempt_count=attempt_count+1 WHERE id=$1`, id)
		_ = tx.Commit(ctx)
		return
	}
	if reservationID, _ := input["reservationId"].(string); reservationID != "" {
		err = s.client.Call(ctx, "billing", "/internal/billing/reservations/activate", map[string]string{"reservationId": reservationID}, nil)
	}
	if err == nil {
		err = s.client.Call(ctx, "evaluation", "/internal/evaluation/submissions", input, nil)
	}
	if err != nil {
		_, _ = tx.Exec(ctx, `UPDATE service_outbox SET attempt_count=attempt_count+1 WHERE id=$1`, id)
	} else {
		_, _ = tx.Exec(ctx, `UPDATE service_outbox SET published_at=now() WHERE id=$1`, id)
	}
	_ = tx.Commit(ctx)
}
