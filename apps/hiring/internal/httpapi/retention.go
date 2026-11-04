package httpapi

import (
	"encoding/json"
	"net/http"

	"github.com/skillsignal/backend/domainhttp"
)

func (s *Server) preparePurgeDocuments(w http.ResponseWriter, r *http.Request) {
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
	if len(input.VersionIDs) > 500 || len(input.DocumentIDs) > 500 {
		domainhttp.WriteError(w, &domainhttp.Error{Status: 400, Code: "INVALID_REQUEST", Message: "Purge request is too large"})
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		domainhttp.WriteError(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	rows, err := tx.Query(r.Context(), `SELECT o.id,o.payload FROM service_outbox o WHERE o.owner_service='hiring' AND (o.published_at IS NULL OR o.event_type='hiring.submission-cancelled') AND o.subject IN (SELECT id FROM resume_submission WHERE resume_version_id=ANY($1)) ORDER BY o.id FOR UPDATE`, input.VersionIDs)
	if err != nil {
		domainhttp.WriteError(w, err)
		return
	}
	var messageIDs []string
	reservations := map[string]bool{}
	for rows.Next() {
		var id string
		var payload []byte
		if err := rows.Scan(&id, &payload); err != nil {
			rows.Close()
			domainhttp.WriteError(w, err)
			return
		}
		var content struct {
			ReservationID string `json:"reservationId"`
		}
		if err := json.Unmarshal(payload, &content); err != nil {
			rows.Close()
			domainhttp.WriteError(w, err)
			return
		}
		if content.ReservationID != "" {
			reservations[content.ReservationID] = true
		}
		messageIDs = append(messageIDs, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		domainhttp.WriteError(w, err)
		return
	}
	_, err = tx.Exec(r.Context(), `UPDATE submission_intake SET state='cancelled',updated_at=now() WHERE resume_version_id=ANY($1)`, input.VersionIDs)
	if err != nil {
		domainhttp.WriteError(w, err)
		return
	}
	if len(messageIDs) > 0 {
		_, err = tx.Exec(r.Context(), `UPDATE service_outbox SET event_type='hiring.submission-cancelled',published_at=COALESCE(published_at,now()) WHERE id=ANY($1)`, messageIDs)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		domainhttp.WriteError(w, err)
		return
	}
	for id := range reservations {
		if err := s.client.Call(r.Context(), "billing", "/internal/billing/reservations/release", map[string]string{"reservationId": id}, nil); err != nil {
			domainhttp.WriteError(w, err)
			return
		}
	}
	domainhttp.WriteJSON(w, 200, map[string]int{"cancelled": len(messageIDs)})
}
