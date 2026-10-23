package httpapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/jackc/pgx/v5"
	"github.com/skillsignal/backend/documents"
	"github.com/skillsignal/backend/domainhttp"
	"github.com/skillsignal/backend/ids"
	"github.com/skillsignal/contracts/events"
)

func (s *Server) acceptEntries(r *http.Request, org, jobID string, entries []documents.BatchEntry, candidateUserID, invitationID, batchID string) (map[string]any, error) {
	a, err := s.client.Authenticate(r)
	if err != nil {
		return nil, err
	}
	var policy struct {
		RetentionDays int `json:"retentionDays"`
	}
	if err := s.client.Call(r.Context(), "identity", "/internal/identity/organizations/"+url.PathEscape(org)+"/retention", nil, &policy); err != nil {
		return nil, err
	}
	if policy.RetentionDays <= 0 {
		return nil, &domainhttp.Error{Status: 503, Code: "SERVICE_UNAVAILABLE", Message: "Organization retention policy is unavailable"}
	}
	var jobVersionID string
	if err := s.pool.QueryRow(r.Context(), `SELECT id FROM job_version WHERE job_id=$1 AND confirmed_at IS NOT NULL ORDER BY version DESC LIMIT 1`, jobID).Scan(&jobVersionID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, &domainhttp.Error{Status: 409, Code: "REQUIREMENTS_NOT_CONFIRMED", Message: "Confirm job requirements before accepting resumes"}
		}
		return nil, err
	}
	requestKey := r.Header.Get("Idempotency-Key")
	if len(requestKey) > 200 {
		return nil, &domainhttp.Error{Status: 400, Code: "INVALID_REQUEST", Message: "Idempotency key must be at most 200 characters"}
	}
	if invitationID != "" {
		requestKey = "invitation:" + invitationID
	} else if requestKey == "" {
		requestKey = ids.New()
	}
	batchID = "batch-" + digest(org+"\x00"+jobID+"\x00"+a.UserID+"\x00"+requestKey)
	fingerprint := sha256.New()
	for _, entry := range entries {
		fmt.Fprintf(fingerprint, "%d:%s:%d:%s:", len(entry.Name), entry.Name, len(entry.Content), entry.Reason)
		_, _ = fingerprint.Write(entry.Content)
	}
	requestHash := hex.EncodeToString(fingerprint.Sum(nil))
	accepted, rejected, items := make([]map[string]any, 0), make([]map[string]any, 0), make([]map[string]any, 0)
	for index, entry := range entries {
		if entry.Reason != "" {
			rejected = append(rejected, map[string]any{"name": entry.Name, "reason": entry.Reason})
			continue
		}
		op := batchID + fmt.Sprintf(":%d", index)
		hash := requestHash
		submissionID, candidateID := "submission-"+digest(op), "candidate-"+digest(op)
		_, err := s.pool.Exec(r.Context(), `INSERT INTO submission_intake(operation_key,organization_id,job_id,job_version_id,request_hash,submission_id,candidate_record_id,batch_id) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(operation_key) DO NOTHING`, op, org, jobID, jobVersionID, hash, submissionID, candidateID, batchID)
		if err != nil {
			return nil, err
		}
		result, err := s.acceptEntry(r, op, hash, entry, a.UserID, candidateUserID, invitationID, policy.RetentionDays)
		if err != nil {
			var failure *domainhttp.Error
			if errors.As(err, &failure) && failure.Status == 402 {
				rejected = append(rejected, map[string]any{"name": entry.Name, "reason": failure.Message})
				continue
			}
			return nil, err
		}
		accepted = append(accepted, map[string]any{"name": entry.Name, "submissionId": result["submissionId"]})
		items = append(items, result)
	}
	return map[string]any{"accepted": accepted, "rejected": rejected, "items": items}, nil
}

func digest(value string) string {
	hash := sha256.Sum256([]byte(value))
	return hex.EncodeToString(hash[:])
}

func (s *Server) acceptEntry(r *http.Request, op, requestHash string, entry documents.BatchEntry, actorID, candidateUserID, invitationID string, retentionDays int) (map[string]any, error) {
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(r.Context())
	if _, err := tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "submission:submission-"+digest(op)); err != nil {
		return nil, err
	}
	var org, jobID, jobVersionID, hash, submissionID, candidateID, batchID, state string
	var documentID, versionID, reservationID *string
	var enterprise bool
	err = tx.QueryRow(r.Context(), `SELECT organization_id,job_id,job_version_id,request_hash,submission_id,candidate_record_id,batch_id,state,document_id,resume_version_id,reservation_id,enterprise FROM submission_intake WHERE operation_key=$1 FOR UPDATE`, op).Scan(&org, &jobID, &jobVersionID, &hash, &submissionID, &candidateID, &batchID, &state, &documentID, &versionID, &reservationID, &enterprise)
	if err != nil {
		return nil, err
	}
	if hash != requestHash {
		return nil, &domainhttp.Error{Status: 409, Code: "IDEMPOTENCY_CONFLICT", Message: "Idempotency key was already used for different files"}
	}
	if state == "cancelled" {
		return nil, &domainhttp.Error{Status: 410, Code: "SUBMISSION_EXPIRED", Message: "This upload operation is no longer available"}
	}
	if state == "accepted" {
		if invitationID != "" {
			return nil, &domainhttp.Error{Status: 409, Code: "INVITATION_REDEEMED", Message: "Invitation has already been used"}
		}
		return map[string]any{"submissionId": submissionID, "candidateRecordId": candidateID, "resumeVersionId": versionID, "filename": entry.Name, "status": "queued"}, nil
	}
	var doc struct {
		DocumentID string `json:"documentId"`
		VersionID  string `json:"versionId"`
	}
	if err := s.client.Call(r.Context(), "documents", "/internal/documents", map[string]any{"organizationId": org, "originalName": entry.Name, "mediaType": documents.MediaTypeForName(entry.Name), "content": entry.Content, "retentionDays": retentionDays, "idempotencyKey": "submission-document:" + submissionID}, &doc); err != nil {
		return nil, err
	}
	if doc.DocumentID == "" || doc.VersionID == "" {
		return nil, &domainhttp.Error{Status: 503, Code: "SERVICE_UNAVAILABLE", Message: "Document service returned an invalid version"}
	}
	var reservation struct {
		ReservationID string `json:"reservationId"`
		Enterprise    bool   `json:"enterprise"`
	}
	if err := s.client.Call(r.Context(), "billing", "/internal/billing/reservations", map[string]any{"organizationId": org, "userId": actorID, "kind": "employer_resume", "idempotencyKey": "submission:" + submissionID}, &reservation); err != nil {
		return nil, err
	}
	if reservation.ReservationID == "" && !reservation.Enterprise {
		return nil, &domainhttp.Error{Status: 503, Code: "SERVICE_UNAVAILABLE", Message: "Billing service returned an invalid reservation"}
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO candidate_record(id,organization_id,user_id) VALUES($1,$2,NULLIF($3,''))`, candidateID, org, candidateUserID)
	if err == nil {
		_, err = tx.Exec(r.Context(), `INSERT INTO resume_submission(id,organization_id,job_id,candidate_record_id,resume_version_id,submitting_user_id) VALUES($1,$2,$3,$4,$5,NULLIF($6,''))`, submissionID, org, jobID, candidateID, doc.VersionID, candidateUserID)
	}
	if err == nil {
		payload, _ := json.Marshal(map[string]string{"organizationId": org, "jobId": jobID, "jobVersionId": jobVersionID, "batchId": batchID, "submissionId": submissionID, "resumeVersionId": doc.VersionID, "reservationId": reservation.ReservationID, "userId": actorID})
		_, err = tx.Exec(r.Context(), `INSERT INTO service_outbox(id,owner_service,event_type,event_version,subject,correlation_id,payload) VALUES($1,'hiring',$2,$3,$4,$4,$5::jsonb)`, ids.New(), events.EvaluationRequested, events.CurrentVersion, submissionID, payload)
	}
	if err == nil && invitationID != "" {
		var claimed string
		err = tx.QueryRow(r.Context(), `UPDATE invitation i SET redeeming_user_id=$1,resume_submission_id=$2 FROM job j WHERE i.id=$3 AND j.id=i.job_id AND i.resume_submission_id IS NULL AND i.revoked_at IS NULL AND i.expires_at>now() AND j.application_opens_at<=now() AND j.application_closes_at>now() AND (i.redeeming_user_id IS NULL OR i.redeeming_user_id=$1) RETURNING i.id`, candidateUserID, submissionID, invitationID).Scan(&claimed)
		if errors.Is(err, pgx.ErrNoRows) {
			err = &domainhttp.Error{Status: 409, Code: "INVITATION_REDEEMED", Message: "Invitation is no longer available"}
		}
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE submission_intake SET document_id=$1,resume_version_id=$2,reservation_id=NULLIF($3,''),enterprise=$4,state='accepted',updated_at=now() WHERE operation_key=$5`, doc.DocumentID, doc.VersionID, reservation.ReservationID, reservation.Enterprise, op)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		return nil, err
	}
	// The durable outbox owns activation, so a crash after attachment cannot lose work.
	return map[string]any{"submissionId": submissionID, "candidateRecordId": candidateID, "resumeVersionId": doc.VersionID, "filename": entry.Name, "status": "queued"}, nil
}
