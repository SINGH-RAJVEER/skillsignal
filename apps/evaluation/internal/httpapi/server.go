package httpapi

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"io"
	"math"
	"mime"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/skillsignal/backend/config"
	"github.com/skillsignal/backend/documents"
	"github.com/skillsignal/backend/domainhttp"
	"github.com/skillsignal/backend/httpx"
	"github.com/skillsignal/backend/ids"
	"github.com/skillsignal/backend/storage"
)

type Server struct {
	pool     *pgxpool.Pool
	client   *domainhttp.Client
	settings config.Settings
	store    *storage.Store
}

func Register(mux *http.ServeMux, pool *pgxpool.Pool, settings config.Settings) {
	store, err := storage.New(settings.StorageRoot)
	if err != nil {
		panic(err)
	}
	s := &Server{pool, domainhttp.New(settings), settings, store}
	mux.HandleFunc("POST /api/independent-evaluations", s.createIndependent)
	mux.HandleFunc("GET /api/independent-evaluations", s.independentList)
	mux.HandleFunc("GET /api/independent-evaluations/{id}", s.independentDetail)
	mux.HandleFunc("DELETE /api/independent-evaluations/{id}", s.independentDelete)
	mux.HandleFunc("GET /api/independent-evaluations/{id}/improved-resume", s.improved)
	mux.HandleFunc("GET /api/jobs/{id}/evaluations", s.results)
	mux.HandleFunc("GET /api/jobs/{id}/evaluations.csv", s.export)
	mux.HandleFunc("GET /api/evaluations/{id}", s.detail)
	mux.HandleFunc("GET /api/processing/{id}", s.processing)
	mux.Handle("POST /internal/evaluation/submissions", domainhttp.Internal(settings.JWTSecret, http.HandlerFunc(s.createEmployer)))
	mux.Handle("GET /internal/evaluation/holds/{operation}", domainhttp.Internal(settings.JWTSecret, http.HandlerFunc(s.holdProof)))
	mux.Handle("POST /internal/evaluation/demo", domainhttp.Internal(settings.JWTSecret, http.HandlerFunc(s.seedDemo)))
	mux.Handle("GET /internal/evaluation/submissions/{id}", domainhttp.Internal(settings.JWTSecret, http.HandlerFunc(s.submissionDetail)))
	mux.Handle("GET /internal/evaluation/evaluations/{id}", domainhttp.Internal(settings.JWTSecret, http.HandlerFunc(s.internalDetail)))
	mux.Handle("POST /internal/evaluation/retention", domainhttp.Internal(settings.JWTSecret, http.HandlerFunc(s.retention)))
	mux.Handle("POST /internal/evaluation/purge-documents", domainhttp.Internal(settings.JWTSecret, http.HandlerFunc(s.purgeDocuments)))
}
func reply(w http.ResponseWriter, status int, v any) { domainhttp.WriteJSON(w, status, v) }
func fail(w http.ResponseWriter, err error)          { domainhttp.WriteError(w, err) }
func bad(w http.ResponseWriter, msg string)          { httpx.WriteError(w, 400, httpx.CodeInvalidRequest, msg) }
func (s *Server) access(w http.ResponseWriter, r *http.Request, write bool, candidate bool) (domainhttp.Access, bool) {
	a, err := s.client.Authenticate(r)
	if err != nil {
		fail(w, err)
		return a, false
	}
	if candidate && a.AccountType != "candidate" {
		httpx.WriteError(w, 403, httpx.CodeForbidden, "Candidate account required")
		return a, false
	}
	if write && a.DemoReadOnly {
		httpx.WriteError(w, 403, httpx.CodeDemoAccount, "Demo accounts are read-only")
		return a, false
	}
	return a, true
}
func readUpload(r *http.Request, field string) (content []byte, name, media string, err error) {
	file, header, err := r.FormFile(field)
	if err != nil {
		return
	}
	defer file.Close()
	content, err = io.ReadAll(io.LimitReader(file, documents.MaxResumeBytes+1))
	name = header.Filename
	media, _, _ = mime.ParseMediaType(header.Header.Get("Content-Type"))
	if err == nil {
		if field == "job_description_file" {
			media = jobDescriptionMedia(media, name)
			var validated documents.ValidatedResume
			validated, err = documents.ValidateJobDescription(content, media, name)
			if err == nil {
				media = validated.MediaType
			}
		} else {
			_, err = documents.ValidateDocument(content, media, name)
		}
	}
	return
}

type hold struct {
	ReservationID string     `json:"reservationId"`
	OperationKey  string     `json:"operationKey"`
	FreeWeekStart *time.Time `json:"freeWeekStart"`
	Enterprise    bool       `json:"enterprise"`
}

func (s *Server) holdCall(r *http.Request, action string, h hold, user string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 30*time.Second)
	defer cancel()
	return s.client.Call(ctx, "billing", "/internal/billing/reservations/"+action, map[string]any{"reservationId": h.ReservationID, "userId": user, "freeWeekStart": h.FreeWeekStart, "operationKey": h.OperationKey}, nil)
}
func (s *Server) createIndependent(w http.ResponseWriter, r *http.Request) {
	a, ok := s.access(w, r, true, true)
	if !ok {
		return
	}
	content, name, media, err := readUpload(r, "file")
	if err != nil {
		httpx.WriteError(w, 400, httpx.CodeInvalidDocument, "Upload a supported PDF, DOCX, or TXT resume")
		return
	}
	description := strings.TrimSpace(r.FormValue("job_description"))
	if len(description) > 200000 {
		bad(w, "Job description is too long")
		return
	}
	var jdContent []byte
	var jdMedia string
	if file, _, e := r.FormFile("job_description_file"); e == nil {
		file.Close()
		jdContent, _, jdMedia, err = readUpload(r, "job_description_file")
		if len(jdContent) > 10<<20 {
			err = errors.New("Job description exceeds 10 MB")
		}
		if err != nil {
			httpx.WriteError(w, 400, httpx.CodeInvalidDocument, "Upload a supported job description document")
			return
		}
		if description != "" {
			bad(w, "Supply pasted text or an uploaded job description")
			return
		}
	}
	id, jobID := ids.New(), ids.New()
	key := "independent-resumes/" + id + documents.SupportedResumeTypes[media]
	jdKey := ""
	if len(jdContent) > 0 {
		jdKey = "independent-descriptions/" + id + documents.JobDescriptionExtension(jdMedia)
	}
	// Persist source bytes before claiming allowance; rollback removes all created objects.
	if err = s.store.Put(key, content); err != nil {
		fail(w, err)
		return
	}
	if jdKey != "" {
		if err = s.store.Put(jdKey, jdContent); err != nil {
			_ = s.store.Delete(key)
			fail(w, err)
			return
		}
	}
	cleanup := func() {
		_ = s.store.Delete(key)
		if jdKey != "" {
			_ = s.store.Delete(jdKey)
		}
	}
	var h hold
	if err = s.client.Call(r.Context(), "billing", "/internal/billing/reservations", map[string]any{"userId": a.UserID, "kind": "independent_evaluation", "idempotencyKey": "independent:" + id, "freeEligible": true}, &h); err != nil {
		cleanup()
		fail(w, err)
		return
	}
	h.OperationKey = "independent:" + id
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		_ = s.holdCall(r, "release", h, a.UserID)
		cleanup()
		fail(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	if err = lockHoldOperation(r, tx, h.OperationKey); err != nil {
		_ = s.holdCall(r, "release", h, a.UserID)
		cleanup()
		fail(w, err)
		return
	}
	var fenced bool
	if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM service_inbox WHERE consumer_service='evaluation:expired-hold' AND event_id=$1)`, h.OperationKey).Scan(&fenced); err != nil {
		fail(w, err)
		return
	}
	if fenced {
		_ = s.holdCall(r, "release", h, a.UserID)
		cleanup()
		httpx.WriteError(w, 409, httpx.CodeInvalidRequest, "Evaluation reservation expired before it could be accepted")
		return
	}

	_, err = tx.Exec(r.Context(), `INSERT INTO independent_evaluation(id,user_id,storage_key,original_name,media_type,job_description,job_description_key,job_description_media_type,point_reservation_id,free_week_start,retention_date) VALUES($1,$2,$3,$4,$5,NULLIF($6,''),NULLIF($7,''),NULLIF($8,''),NULLIF($9,''),$10,$11)`, id, a.UserID, key, name, media, description, jdKey, jdMedia, h.ReservationID, h.FreeWeekStart, time.Now().UTC().AddDate(0, 0, s.settings.IndependentRetentionDays))
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO processing_job(id,type,payload_reference,idempotency_key) VALUES($1,'independent_evaluation_parse',$2,$3)`, jobID, id, "independent_evaluation_parse:"+id)
	}
	if err != nil {
		_ = tx.Rollback(r.Context())
		_ = s.holdCall(r, "release", h, a.UserID)
		cleanup()
		fail(w, err)
		return
	}
	// Commit acknowledgement can be lost after success. Preserve sources and let owner discovery recover the hold.
	if err = tx.Commit(r.Context()); err != nil {
		fail(w, err)
		return
	}
	// Expiring pending holds discover this committed evaluation if activation is interrupted.
	if err = s.holdCall(r, "activate", h, a.UserID); err != nil {
		reply(w, 202, map[string]string{"id": id, "processingJobId": jobID})
		return
	}
	reply(w, 202, map[string]string{"id": id, "processingJobId": jobID})
}

const independentJSON = `jsonb_build_object('id',id,'originalName',original_name,'status',status,'score',score,'generalQualityScore',general_quality_score,'evidenceCoverage',evidence_coverage,'requirementAssessments',COALESCE(requirement_assessments,'[]'::jsonb),'requirements',COALESCE(requirements,'[]'::jsonb),'safeError',safe_error,'createdAt',created_at,'completedAt',completed_at,'jobDescriptionProvided',job_description IS NOT NULL OR job_description_key IS NOT NULL,'hasImprovedResume',improved_resume_key IS NOT NULL AND improved_resume_unlocked_at IS NOT NULL,'suggestions',COALESCE(suggestions,'[]'::jsonb),'facts',COALESCE(normalized_facts,'{}'::jsonb),'modelProvenance',(SELECT COALESCE(jsonb_agg(jsonb_build_object('model',c.model,'provider',c.provider,'task',c.task,'status',c.status,'promptVersion',c.prompt_version,'schemaVersion',c.schema_version,'fallbackReason',c.fallback_reason) ORDER BY c.created_at,c.id),'[]'::jsonb) FROM inference_call c JOIN processing_job p ON p.id=c.processing_job_id WHERE p.payload_reference=independent_evaluation.id))`

func (s *Server) independentList(w http.ResponseWriter, r *http.Request) {
	a, ok := s.access(w, r, false, true)
	if !ok {
		return
	}
	rows, err := s.pool.Query(r.Context(), `SELECT `+independentJSON+` FROM independent_evaluation WHERE user_id=$1 ORDER BY created_at DESC,id LIMIT 200`, a.UserID)
	if err != nil {
		fail(w, err)
		return
	}
	defer rows.Close()
	out := []json.RawMessage{}
	for rows.Next() {
		var raw []byte
		if err = rows.Scan(&raw); err != nil {
			fail(w, err)
			return
		}
		out = append(out, json.RawMessage(raw))
	}
	if err = rows.Err(); err != nil {
		fail(w, err)
		return
	}
	reply(w, 200, out)
}
func (s *Server) independentDetail(w http.ResponseWriter, r *http.Request) {
	a, ok := s.access(w, r, false, true)
	if !ok {
		return
	}
	var raw []byte
	if err := s.pool.QueryRow(r.Context(), `SELECT `+independentJSON+` FROM independent_evaluation WHERE id=$1 AND user_id=$2`, r.PathValue("id"), a.UserID).Scan(&raw); err != nil {
		httpx.WriteError(w, 404, httpx.CodeNotFound, "Evaluation not found")
		return
	}
	reply(w, 200, json.RawMessage(raw))
}
func (s *Server) improved(w http.ResponseWriter, r *http.Request) {
	a, ok := s.access(w, r, false, true)
	if !ok {
		return
	}
	var key string
	if err := s.pool.QueryRow(r.Context(), `SELECT improved_resume_key FROM independent_evaluation WHERE id=$1 AND user_id=$2 AND status='complete' AND improved_resume_unlocked_at IS NOT NULL`, r.PathValue("id"), a.UserID).Scan(&key); err != nil {
		httpx.WriteError(w, 404, httpx.CodeNotFound, "Corrected resume is not available")
		return
	}
	body, err := s.store.Get(key)
	if err != nil {
		fail(w, err)
		return
	}
	w.Header().Set("Content-Type", documents.MediaTypeDOCX)
	w.Header().Set("Content-Disposition", `attachment; filename="corrected-resume.docx"`)
	_, _ = w.Write(body)
}
func (s *Server) deleteIndependent(r *http.Request, id, user string) error {
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	var key, status string
	var jd, improved, reservation *string
	var week *time.Time
	err = tx.QueryRow(r.Context(), `SELECT storage_key,job_description_key,improved_resume_key,point_reservation_id,free_week_start,status FROM independent_evaluation WHERE id=$1 AND user_id=$2 FOR UPDATE`, id, user).Scan(&key, &jd, &improved, &reservation, &week, &status)
	if err != nil {
		return err
	}
	_, err = tx.Exec(r.Context(), `DELETE FROM processing_job WHERE payload_reference=$1 AND type IN ('independent_evaluation_parse','independent_evaluation_enrich')`, id)
	if err != nil {
		return err
	}
	h := hold{FreeWeekStart: week, OperationKey: "independent:" + id}
	if status == "complete" {
		h.FreeWeekStart = nil
	}
	if reservation != nil {
		h.ReservationID = *reservation
	}
	if err = s.holdCall(r, "release", h, user); err != nil {
		return err
	}
	for _, k := range []*string{&key, jd, improved} {
		if k != nil {
			if err = s.store.Delete(*k); err != nil {
				return err
			}
		}
	}
	if _, err = tx.Exec(r.Context(), `DELETE FROM independent_evaluation WHERE id=$1`, id); err != nil {
		return err
	}
	return tx.Commit(r.Context())
}
func (s *Server) independentDelete(w http.ResponseWriter, r *http.Request) {
	a, ok := s.access(w, r, true, true)
	if !ok {
		return
	}
	err := s.deleteIndependent(r, r.PathValue("id"), a.UserID)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.WriteError(w, 404, httpx.CodeNotFound, "Evaluation not found")
		return
	}
	if err != nil {
		fail(w, err)
		return
	}
	w.WriteHeader(204)
}
func (s *Server) retention(w http.ResponseWriter, r *http.Request) {
	rows, err := s.pool.Query(r.Context(), `SELECT id,user_id FROM independent_evaluation WHERE retention_date <= now() LIMIT 500`)
	if err != nil {
		fail(w, err)
		return
	}
	type item struct{ id, user string }
	items := []item{}
	for rows.Next() {
		var i item
		if err = rows.Scan(&i.id, &i.user); err != nil {
			break
		}
		items = append(items, i)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		fail(w, err)
		return
	}
	for _, i := range items {
		if err = s.deleteIndependent(r, i.id, i.user); err != nil {
			fail(w, err)
			return
		}
	}
	reply(w, 200, map[string]int{"deleted": len(items), "purged": len(items)})
}

type employerInput struct {
	OrganizationID  string `json:"organizationId"`
	JobID           string `json:"jobId"`
	JobVersionID    string `json:"jobVersionId"`
	UserID          string `json:"userId"`
	SubmissionID    string `json:"submissionId"`
	ResumeVersionID string `json:"resumeVersionId"`
	ReservationID   string `json:"reservationId"`
	EvaluationID    string `json:"evaluationId"`
	BatchID         string `json:"batchId"`
}

func (s *Server) createEmployer(w http.ResponseWriter, r *http.Request) {
	var in employerInput
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.OrganizationID == "" || in.JobID == "" || in.JobVersionID == "" || in.SubmissionID == "" || in.ResumeVersionID == "" || in.UserID == "" {
		bad(w, "Incomplete evaluation submission")
		return
	}
	var submission struct {
		OrganizationID  string `json:"organizationId"`
		JobID           string `json:"jobId"`
		ResumeVersionID string `json:"resumeVersionId"`
	}
	if err := s.client.Call(r.Context(), "hiring", "/internal/hiring/submissions/"+url.PathEscape(in.SubmissionID), nil, &submission); err != nil {
		fail(w, err)
		return
	}
	if submission.OrganizationID != in.OrganizationID || submission.JobID != in.JobID || submission.ResumeVersionID != in.ResumeVersionID {
		bad(w, "Submission does not match the evaluation owner and source")
		return
	}
	if in.EvaluationID == "" {
		in.EvaluationID = ids.New()
	}
	if in.BatchID == "" {
		in.BatchID = ids.New()
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	if _, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, in.SubmissionID+":"+in.JobVersionID); err != nil {
		fail(w, err)
		return
	}
	if _, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "evaluation-source:"+in.ResumeVersionID); err != nil {
		fail(w, err)
		return
	}
	var purged bool
	if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM service_inbox WHERE consumer_service='evaluation:purged-version' AND event_id=$1)`, in.ResumeVersionID).Scan(&purged); err != nil {
		fail(w, err)
		return
	}
	if purged {
		httpx.WriteError(w, 409, httpx.CodeInvalidRequest, "Resume source has been deleted")
		return
	}
	var existing string
	err = tx.QueryRow(r.Context(), `SELECT id FROM evaluation WHERE resume_submission_id=$1 AND job_version_id=$2 ORDER BY created_at DESC LIMIT 1`, in.SubmissionID, in.JobVersionID).Scan(&existing)
	if err == nil {
		reply(w, 200, map[string]string{"evaluationId": existing, "batchId": in.BatchID})
		return
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		fail(w, err)
		return
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO batch_evaluation(id,organization_id,job_id,job_version_id,created_by_user_id,requirement_schema_version) VALUES($1,$2,$3,$4,$5,'requirements-1') ON CONFLICT(id) DO NOTHING`, in.BatchID, in.OrganizationID, in.JobID, in.JobVersionID, in.UserID)
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO batch_evaluation_submission(organization_id,job_id,batch_evaluation_id,resume_submission_id) VALUES($1,$2,$3,$4)`, in.OrganizationID, in.JobID, in.BatchID, in.SubmissionID)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO evaluation(id,batch_evaluation_id,resume_submission_id,job_version_id,resume_version_id,point_reservation_id) VALUES($1,$2,$3,$4,$5,NULLIF($6,''))`, in.EvaluationID, in.BatchID, in.SubmissionID, in.JobVersionID, in.ResumeVersionID, in.ReservationID)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO processing_job(id,type,payload_reference,idempotency_key) VALUES($1,'resume_parse',$2,$3) ON CONFLICT(type,idempotency_key) DO NOTHING`, ids.New(), in.ResumeVersionID, "resume_parse:"+in.ResumeVersionID)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		fail(w, err)
		return
	}
	reply(w, 201, map[string]string{"evaluationId": in.EvaluationID, "batchId": in.BatchID})
}

type requirement struct {
	ID             string  `json:"id"`
	NormalizedText string  `json:"normalizedText"`
	Kind           string  `json:"kind"`
	Weight         float64 `json:"weight"`
}
type jobInfo struct {
	ID             string        `json:"id"`
	OrganizationID string        `json:"organizationId"`
	Requirements   []requirement `json:"requirements"`
}

func (s *Server) job(r *http.Request, id string, a domainhttp.Access) (jobInfo, error) {
	var j jobInfo
	err := s.client.Call(r.Context(), "hiring", "/internal/hiring/jobs/"+url.PathEscape(id), nil, &j)
	if err != nil {
		return j, err
	}
	if a.Role(j.OrganizationID) == "" {
		return j, &domainhttp.Error{Status: 403, Code: httpx.CodeForbidden, Message: "Organization access required"}
	}
	return j, nil
}
func (s *Server) loadResults(r *http.Request, j jobInfo) ([]map[string]any, error) {
	rows, err := s.pool.Query(r.Context(), `SELECT e.id,e.resume_submission_id,e.resume_version_id,e.status,e.score,e.evidence_coverage,e.eligibility,e.quality_state,e.batch_evaluation_id,e.job_version_id,e.assessment_degradation,e.assessment_schema_version,e.assessment_prompt_version,e.scoring_policy_version FROM evaluation e JOIN batch_evaluation b ON b.id=e.batch_evaluation_id WHERE b.job_id=$1 ORDER BY e.score DESC NULLS LAST,e.created_at,e.id`, j.ID)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for rows.Next() {
		var id, sub, version, status, eligibility, quality, batch, jobVersion, scoring string
		var degradation, schema, prompt *string
		var score, coverage *int
		if err = rows.Scan(&id, &sub, &version, &status, &score, &coverage, &eligibility, &quality, &batch, &jobVersion, &degradation, &schema, &prompt, &scoring); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, map[string]any{"id": id, "submissionId": sub, "resumeVersionId": version, "status": status, "score": score, "coverage": coverage, "eligibility": eligibility, "qualityState": quality, "batchId": batch, "jobVersionId": jobVersion, "assessmentDegradation": degradation, "assessmentSchemaVersion": schema, "assessmentPromptVersion": prompt, "scoringPolicyVersion": scoring})
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return nil, err
	}
	versions := map[string]jobInfo{}
	for _, e := range out {
		versionID := e["jobVersionId"].(string)
		jobVersionInfo, exists := versions[versionID]
		if !exists {
			if err = s.client.Call(r.Context(), "hiring", "/internal/hiring/jobs/"+url.PathEscape(j.ID)+"?versionId="+url.QueryEscape(versionID), nil, &jobVersionInfo); err != nil {
				return nil, err
			}
			versions[versionID] = jobVersionInfo
		}
		requirements := map[string]requirement{}
		totalWeight := 0.0
		for _, req := range jobVersionInfo.Requirements {
			requirements[req.ID] = req
			if req.Kind == "required" || req.Kind == "preferred" {
				totalWeight += req.Weight
			}
		}
		var sub map[string]any
		if err = s.client.Call(r.Context(), "hiring", "/internal/hiring/submissions/"+url.PathEscape(e["submissionId"].(string)), nil, &sub); err != nil {
			return nil, err
		}
		for _, field := range []string{"candidateName", "candidateEmail", "candidateLocation"} {
			e[field] = sub[field]
		}
		var version map[string]any
		if err = s.client.Call(r.Context(), "documents", "/internal/documents/versions/"+url.PathEscape(e["resumeVersionId"].(string)), nil, &version); err != nil {
			return nil, err
		}
		facts, _ := version["facts"].(map[string]any)
		skills := []string{}
		if fs, ok := facts["skills"].([]any); ok {
			for _, f := range fs {
				if m, ok := f.(map[string]any); ok {
					if name, ok := m["canonicalName"].(string); ok {
						skills = append(skills, name)
					}
				}
			}
		}
		sort.Strings(skills)
		e["skills"] = skills
		e["qualityWarnings"] = []string{}
		if warnings, ok := facts["warnings"]; ok {
			e["qualityWarnings"] = warnings
		}
		if blocks, ok := version["blocks"].(map[string]any); ok {
			metadata := blocks["metadata"]
			if metadata == nil {
				if original, ok := blocks["Metadata"].(map[string]any); ok {
					metadata = map[string]any{"mediaType": original["MediaType"], "pageCount": original["PageCount"], "blockCount": original["BlockCount"], "characterCount": original["CharacterCount"], "nonWhitespaceCharacterCount": original["NonWhitespaceCharacterCount"]}
				}
			}
			e["extractionMetadata"] = metadata
			if warnings, ok := blocks["warnings"]; ok {
				e["qualityWarnings"] = warnings
			}
		}
		assessments := []map[string]any{}
		gates := []map[string]any{}
		arows, err := s.pool.Query(r.Context(), `SELECT job_requirement_id,outcome,reasoning,evidence,semantic_evidence,lexical_evidence,confidence,deterministic_evidence FROM requirement_assessment WHERE evaluation_id=$1 ORDER BY created_at,id`, e["id"])
		if err != nil {
			return nil, err
		}
		for arows.Next() {
			var requirementID, outcome, reason string
			var evidence, semantic, lexical, deterministic []byte
			var confidence float64
			if err = arows.Scan(&requirementID, &outcome, &reason, &evidence, &semantic, &lexical, &confidence, &deterministic); err != nil {
				arows.Close()
				return nil, err
			}
			req, exists := requirements[requirementID]
			if !exists {
				arows.Close()
				return nil, errors.New("Evaluation requirement is absent from its confirmed job version")
			}
			a := map[string]any{"requirement": req.NormalizedText, "requirementId": requirementID, "kind": req.Kind, "weight": req.Weight, "outcome": outcome, "reasoning": reason, "evidence": json.RawMessage(evidence), "contribution": 0, "confidence": confidence}
			if len(deterministic) > 0 {
				a["deterministicEvidence"] = json.RawMessage(deterministic)
			}
			if len(semantic) > 0 {
				a["semanticEvidence"] = json.RawMessage(semantic)
			}
			if len(lexical) > 0 {
				a["lexicalEvidence"] = json.RawMessage(lexical)
			}
			if totalWeight > 0 && (req.Kind == "required" || req.Kind == "preferred") {
				factor := 0.0
				if outcome == "met" {
					factor = 1
				} else if outcome == "partial" {
					factor = .5
				}
				a["contribution"] = 100 * factor * req.Weight / totalWeight
			}
			assessments = append(assessments, a)
			if req.Kind == "hard_gate" {
				gates = append(gates, map[string]any{"requirement": req.NormalizedText, "outcome": outcome})
			}
		}
		arows.Close()
		if err = arows.Err(); err != nil {
			return nil, err
		}
		e["assessments"] = assessments
		e["hardGates"] = gates
		var provenance []byte
		if err = s.pool.QueryRow(r.Context(), `SELECT COALESCE(jsonb_agg(jsonb_build_object('model',c.model,'provider',c.provider,'task',c.task,'status',c.status,'promptVersion',c.prompt_version,'schemaVersion',c.schema_version,'fallbackReason',c.fallback_reason) ORDER BY c.created_at,c.id),'[]'::jsonb) FROM inference_call c JOIN processing_job p ON p.id=c.processing_job_id WHERE p.payload_reference=$1 OR p.payload_reference=$2`, e["id"], e["resumeVersionId"]).Scan(&provenance); err != nil {
			return nil, err
		}
		e["modelProvenance"] = json.RawMessage(provenance)
	}
	return out, nil
}
func selected(values []string, value string) bool {
	for _, v := range values {
		if v == value {
			return true
		}
	}
	return false
}
func number(v any) float64 {
	switch n := v.(type) {
	case *int:
		if n != nil {
			return float64(*n)
		}
	case float64:
		return n
	case int:
		return float64(n)
	}
	return -1
}
func filterResults(items []map[string]any, q url.Values) ([]map[string]any, error) {
	for _, key := range []string{"minimum_score", "minimum_coverage"} {
		if raw := q.Get(key); raw != "" {
			n, err := strconv.ParseFloat(raw, 64)
			if err != nil || math.IsNaN(n) || math.IsInf(n, 0) || n < 0 || n > 100 {
				return nil, &domainhttp.Error{Status: 400, Code: httpx.CodeInvalidRequest, Message: "Score and coverage filters must be between 0 and 100"}
			}
		}
	}
	result := []map[string]any{}
	for _, e := range items {
		eligibility, _ := e["eligibility"].(string)
		if len(q["eligibility"]) == 0 && eligibility == "not_eligible" {
			continue
		}
		if len(q["eligibility"]) > 0 && !selected(q["eligibility"], eligibility) {
			continue
		}
		if len(q["status"]) > 0 && !selected(q["status"], e["status"].(string)) {
			continue
		}
		if raw := q.Get("minimum_score"); raw != "" {
			n, _ := strconv.ParseFloat(raw, 64)
			if number(e["score"]) < n {
				continue
			}
		}
		if raw := q.Get("minimum_coverage"); raw != "" {
			n, _ := strconv.ParseFloat(raw, 64)
			if number(e["coverage"]) < n {
				continue
			}
		}
		if term := strings.ToLower(q.Get("search")); term != "" {
			name, _ := e["candidateName"].(string)
			email, _ := e["candidateEmail"].(string)
			if !strings.Contains(strings.ToLower(name+" "+email), term) {
				continue
			}
		}
		if skill := strings.ToLower(q.Get("skill")); skill != "" {
			found := false
			for _, v := range e["skills"].([]string) {
				if strings.Contains(strings.ToLower(v), skill) {
					found = true
				}
			}
			if !found {
				continue
			}
		}
		if len(q["outcome"]) > 0 {
			found := false
			for _, a := range e["assessments"].([]map[string]any) {
				if selected(q["outcome"], a["outcome"].(string)) {
					found = true
				}
			}
			if !found {
				continue
			}
		}
		result = append(result, e)
	}
	return result, nil
}
func paginate(items []map[string]any, q url.Values) ([]map[string]any, error) {
	offset, limit := 0, len(items)
	var err error
	if raw := q.Get("offset"); raw != "" {
		offset, err = strconv.Atoi(raw)
		if err != nil || offset < 0 {
			return nil, errors.New("Invalid offset")
		}
	}
	if raw := q.Get("limit"); raw != "" {
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 1 || limit > 500 {
			return nil, errors.New("Limit must be between 1 and 500")
		}
	}
	if offset > len(items) {
		offset = len(items)
	}
	end := offset + limit
	if end > len(items) {
		end = len(items)
	}
	return items[offset:end], nil
}
func (s *Server) results(w http.ResponseWriter, r *http.Request) {
	a, ok := s.access(w, r, false, false)
	if !ok {
		return
	}
	j, err := s.job(r, r.PathValue("id"), a)
	if err != nil {
		fail(w, err)
		return
	}
	items, err := s.loadResults(r, j)
	if err == nil {
		items, err = filterResults(items, r.URL.Query())
	}
	if err != nil {
		fail(w, err)
		return
	}
	w.Header().Set("X-Total-Count", strconv.Itoa(len(items)))
	items, err = paginate(items, r.URL.Query())
	if err != nil {
		bad(w, err.Error())
		return
	}
	reply(w, 200, items)
}

var exportColumns = []string{"candidate_name", "candidate_email", "candidate_location", "status", "score", "eligibility", "evidence_coverage", "quality_state", "quality_warnings"}

func safeCell(value string) string {
	trimmed := strings.TrimLeft(value, " \t\r\n")
	if trimmed != "" && strings.ContainsRune("=+-@", rune(trimmed[0])) {
		return "'" + value
	}
	return value
}
func (s *Server) export(w http.ResponseWriter, r *http.Request) {
	a, ok := s.access(w, r, false, false)
	if !ok {
		return
	}
	j, err := s.job(r, r.PathValue("id"), a)
	if err != nil {
		fail(w, err)
		return
	}
	if role := a.Role(j.OrganizationID); role != "owner" && role != "recruiter" {
		httpx.WriteError(w, 403, httpx.CodeForbidden, "Owner or recruiter access required for exports")
		return
	}
	columns := r.URL.Query()["columns"]
	if len(columns) == 0 {
		columns = exportColumns
	}
	labels := r.URL.Query()["labels"]
	if len(labels) == 0 {
		labels = columns
	}
	if len(labels) != len(columns) {
		bad(w, "Export labels must match columns")
		return
	}
	seen := map[string]bool{}
	for _, c := range columns {
		if !selected(exportColumns, c) || seen[c] {
			bad(w, "Unknown or duplicate export column")
			return
		}
		seen[c] = true
	}
	items, err := s.loadResults(r, j)
	if err == nil {
		items, err = filterResults(items, r.URL.Query())
	}
	if err != nil {
		fail(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="evaluations.csv"`)
	writer := csv.NewWriter(w)
	defer writer.Flush()
	header := []string{}
	for _, label := range labels {
		header = append(header, safeCell(label))
	}
	_ = writer.Write(header)
	keys := map[string]string{"candidate_name": "candidateName", "candidate_email": "candidateEmail", "candidate_location": "candidateLocation", "evidence_coverage": "coverage", "quality_state": "qualityState", "quality_warnings": "qualityWarnings"}
	for _, e := range items {
		row := []string{}
		for _, c := range columns {
			key := keys[c]
			if key == "" {
				key = c
			}
			value := ""
			switch v := e[key].(type) {
			case string:
				value = v
			case *int:
				if v != nil {
					value = strconv.Itoa(*v)
				}
			case []string:
				value = strings.Join(v, "; ")
			case []any:
				parts := []string{}
				for _, p := range v {
					if st, ok := p.(string); ok {
						parts = append(parts, st)
					}
				}
				value = strings.Join(parts, "; ")
			}
			row = append(row, safeCell(value))
		}
		_ = writer.Write(row)
	}
}
func (s *Server) detail(w http.ResponseWriter, r *http.Request) {
	a, ok := s.access(w, r, false, false)
	if !ok {
		return
	}
	var jobID string
	err := s.pool.QueryRow(r.Context(), `SELECT b.job_id FROM evaluation e JOIN batch_evaluation b ON b.id=e.batch_evaluation_id WHERE e.id=$1`, r.PathValue("id")).Scan(&jobID)
	if err != nil {
		httpx.WriteError(w, 404, httpx.CodeNotFound, "Evaluation not found")
		return
	}
	j, err := s.job(r, jobID, a)
	if err != nil {
		fail(w, err)
		return
	}
	items, err := s.loadResults(r, j)
	if err != nil {
		fail(w, err)
		return
	}
	for _, e := range items {
		if e["id"] == r.PathValue("id") {
			reply(w, 200, e)
			return
		}
	}
	httpx.WriteError(w, 404, httpx.CodeNotFound, "Evaluation not found")
}
func (s *Server) internalDetail(w http.ResponseWriter, r *http.Request) {
	var raw []byte
	err := s.pool.QueryRow(r.Context(), `SELECT jsonb_build_object('id',e.id,'batchId',e.batch_evaluation_id,'batchEvaluationId',e.batch_evaluation_id,'organizationId',b.organization_id,'jobId',b.job_id,'submissionId',e.resume_submission_id,'status',e.status,'eligibility',e.eligibility) FROM evaluation e JOIN batch_evaluation b ON b.id=e.batch_evaluation_id WHERE e.id=$1`, r.PathValue("id")).Scan(&raw)
	if err != nil {
		httpx.WriteError(w, 404, httpx.CodeNotFound, "Evaluation not found")
		return
	}
	reply(w, 200, json.RawMessage(raw))
}
func (s *Server) processing(w http.ResponseWriter, r *http.Request) {
	a, ok := s.access(w, r, false, false)
	if !ok {
		return
	}
	var status, reference, kind string
	var safe *string
	err := s.pool.QueryRow(r.Context(), `SELECT status,payload_reference,type,safe_error FROM processing_job WHERE id=$1`, r.PathValue("id")).Scan(&status, &reference, &kind, &safe)
	if err != nil {
		httpx.WriteError(w, 404, httpx.CodeNotFound, "Processing job not found")
		return
	}
	allowed := false
	if strings.HasPrefix(kind, "independent_evaluation") {
		var user string
		err = s.pool.QueryRow(r.Context(), `SELECT user_id FROM independent_evaluation WHERE id=$1`, reference).Scan(&user)
		allowed = err == nil && user == a.UserID
	} else {
		var jobID string
		err = s.pool.QueryRow(r.Context(), `SELECT b.job_id FROM evaluation e JOIN batch_evaluation b ON b.id=e.batch_evaluation_id WHERE e.id=$1 OR e.resume_version_id=$1 LIMIT 1`, reference).Scan(&jobID)
		if err == nil {
			_, err = s.job(r, jobID, a)
			allowed = err == nil
		}
	}
	if !allowed {
		httpx.WriteError(w, 404, httpx.CodeNotFound, "Processing job not found")
		return
	}
	reply(w, 200, map[string]any{"id": r.PathValue("id"), "status": status, "stage": kind, "retryable": status == "ready" || status == "retry", "safeError": safe})
}

func (s *Server) purgeDocuments(w http.ResponseWriter, r *http.Request) {
	var in struct {
		DocumentIDs []string `json:"documentIds"`
		VersionIDs  []string `json:"versionIds"`
		Now         string   `json:"now"`
	}
	if !domainhttp.DecodeJSON(w, r, &in) {
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	sort.Strings(in.VersionIDs)
	for _, version := range in.VersionIDs {
		if _, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "evaluation-source:"+version); err != nil {
			fail(w, err)
			return
		}
		if _, err = tx.Exec(r.Context(), `INSERT INTO service_inbox(consumer_service,event_id) VALUES('evaluation:purged-version',$1) ON CONFLICT DO NOTHING`, version); err != nil {
			fail(w, err)
			return
		}
	}
	rows, err := tx.Query(r.Context(), `SELECT point_reservation_id FROM evaluation WHERE resume_version_id=ANY($1) AND point_reservation_id IS NOT NULL FOR UPDATE`, in.VersionIDs)
	if err != nil {
		fail(w, err)
		return
	}
	holds := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			break
		}
		holds = append(holds, id)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		fail(w, err)
		return
	}
	for _, id := range holds {
		if err = s.holdCall(r, "release", hold{ReservationID: id}, ""); err != nil {
			fail(w, err)
			return
		}
	}
	_, err = tx.Exec(r.Context(), `DELETE FROM processing_job WHERE payload_reference=ANY($1) OR payload_reference IN (SELECT id FROM evaluation WHERE resume_version_id=ANY($1))`, in.VersionIDs)
	if err == nil {
		_, err = tx.Exec(r.Context(), `DELETE FROM evaluation WHERE resume_version_id=ANY($1)`, in.VersionIDs)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `DELETE FROM resume_block_embedding WHERE resume_version_id=ANY($1)`, in.VersionIDs)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `DELETE FROM embedding_cache c WHERE NOT EXISTS(SELECT 1 FROM resume_block_embedding b WHERE b.model=c.model AND b.text_hash=c.text_hash)`)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `DELETE FROM batch_evaluation_submission WHERE NOT EXISTS(SELECT 1 FROM evaluation e WHERE e.batch_evaluation_id=batch_evaluation_submission.batch_evaluation_id AND e.resume_submission_id=batch_evaluation_submission.resume_submission_id)`)
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `DELETE FROM batch_evaluation b WHERE NOT EXISTS(SELECT 1 FROM batch_evaluation_submission s WHERE s.batch_evaluation_id=b.id)`)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		fail(w, err)
		return
	}
	reply(w, 200, map[string]bool{"ok": true})
}

func (s *Server) submissionDetail(w http.ResponseWriter, r *http.Request) {
	var raw []byte
	err := s.pool.QueryRow(r.Context(), `SELECT jsonb_build_object('evaluationId',e.id,'batchEvaluationId',e.batch_evaluation_id,'organizationId',b.organization_id,'jobId',b.job_id,'submissionId',e.resume_submission_id,'status',e.status,'eligibility',e.eligibility,'score',e.score) FROM evaluation e JOIN batch_evaluation b ON b.id=e.batch_evaluation_id WHERE e.resume_submission_id=$1 ORDER BY e.created_at DESC,e.id DESC LIMIT 1`, r.PathValue("id")).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		httpx.WriteError(w, 404, httpx.CodeNotFound, "Evaluation not found")
		return
	}
	if err != nil {
		fail(w, err)
		return
	}
	reply(w, 200, json.RawMessage(raw))
}

func jobDescriptionMedia(media, name string) string {
	if media != "" && media != "application/octet-stream" {
		return media
	}
	switch strings.ToLower(path.Ext(name)) {
	case ".md", ".markdown":
		return "text/markdown"
	case ".html", ".htm":
		return "text/html"
	case ".pdf":
		return documents.MediaTypePDF
	case ".docx":
		return documents.MediaTypeDOCX
	case ".txt":
		return documents.MediaTypeTXT
	}
	return media
}
