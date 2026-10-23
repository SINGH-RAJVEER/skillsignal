package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
)

// Hold discovery reads the durable owner record, including after a lost activation response.
func (s *Server) holdProof(w http.ResponseWriter, r *http.Request) {
	operation := r.PathValue("operation")
	id, valid := strings.CutPrefix(operation, "independent:")
	if !valid || id == "" {
		bad(w, "Unknown evaluation operation")
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	var locked bool
	if err = tx.QueryRow(r.Context(), `SELECT pg_try_advisory_xact_lock(hashtextextended($1,0))`, "evaluation-hold:"+operation).Scan(&locked); err != nil {
		fail(w, err)
		return
	}
	if !locked {
		reply(w, 200, map[string]any{"attached": true, "state": "active"})
		return
	}
	var status string
	err = tx.QueryRow(r.Context(), `SELECT status FROM independent_evaluation WHERE id=$1`, id).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		if _, err = tx.Exec(r.Context(), `INSERT INTO service_inbox(consumer_service,event_id) VALUES('evaluation:expired-hold',$1) ON CONFLICT DO NOTHING`, operation); err != nil {
			fail(w, err)
			return
		}
		if err = tx.Commit(r.Context()); err != nil {
			fail(w, err)
			return
		}
		reply(w, 200, map[string]any{"attached": false, "state": "absent"})
		return
	}
	if err != nil {
		fail(w, err)
		return
	}
	state := "active"
	if status == "complete" {
		state = "completed"
	} else if status == "failed" {
		state = "failed"
	}
	reply(w, http.StatusOK, map[string]any{"attached": true, "state": state})
}

func lockHoldOperation(r *http.Request, tx pgx.Tx, operation string) error {
	_, err := tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "evaluation-hold:"+operation)
	return err
}
