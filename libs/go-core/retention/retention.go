// Package retention sweeps expired resume data from tables and storage.
//
// Independent evaluations expire whole; employer documents expire with every
// submission, evaluation, and embedding derived from them, so no
// candidate-identifying content survives past the configured window.
package retention

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/skillsignal/go-core/points"
	"github.com/skillsignal/go-core/storage"
)

// Result counts one sweep pass.
type Result struct {
	DocumentsPurged              int
	IndependentEvaluationsPurged int
}

// PurgeExpiredData removes expired documents and evaluations, then deletes
// their storage objects only after the deletes succeed.
func PurgeExpiredData(ctx context.Context, tx pgx.Tx, store *storage.Store, now time.Time) (Result, error) {
	var result Result
	documentIDs, documentKeys, err := expiredDocuments(ctx, tx, now)
	if err != nil {
		return result, err
	}
	evaluationIDs, evaluationKeys, err := expiredIndependentEvaluations(ctx, tx, now)
	if err != nil {
		return result, err
	}
	if err := purgeIndependentEvaluations(ctx, tx, evaluationIDs); err != nil {
		return result, err
	}
	if err := purgeResumeDocuments(ctx, tx, documentIDs); err != nil {
		return result, err
	}
	for _, key := range append(documentKeys, evaluationKeys...) {
		if err := store.Delete(key); err != nil {
			return result, err
		}
	}
	result.DocumentsPurged = len(documentIDs)
	result.IndependentEvaluationsPurged = len(evaluationIDs)
	return result, nil
}

func expiredDocuments(ctx context.Context, tx pgx.Tx, now time.Time) ([]string, []string, error) {
	rows, err := tx.Query(ctx,
		`SELECT id, storage_key FROM resume_document WHERE retention_date <= $1 ORDER BY retention_date`, now)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var ids, keys []string
	for rows.Next() {
		var id, key string
		if err := rows.Scan(&id, &key); err != nil {
			return nil, nil, err
		}
		ids, keys = append(ids, id), append(keys, key)
	}
	return ids, keys, rows.Err()
}

func expiredIndependentEvaluations(ctx context.Context, tx pgx.Tx, now time.Time) ([]string, []string, error) {
	rows, err := tx.Query(ctx,
		`SELECT id, storage_key, job_description_key, improved_resume_key
		 FROM independent_evaluation WHERE retention_date <= $1 ORDER BY retention_date`, now)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	var ids, keys []string
	for rows.Next() {
		var id, storageKey string
		var jobKey, improvedKey *string
		if err := rows.Scan(&id, &storageKey, &jobKey, &improvedKey); err != nil {
			return nil, nil, err
		}
		ids = append(ids, id)
		keys = append(keys, storageKey)
		if jobKey != nil && *jobKey != "" {
			keys = append(keys, *jobKey)
		}
		if improvedKey != nil && *improvedKey != "" {
			keys = append(keys, *improvedKey)
		}
	}
	return ids, keys, rows.Err()
}

func purgeIndependentEvaluations(ctx context.Context, tx pgx.Tx, ids []string) error {
	for _, id := range ids {
		var reservationID *string
		if err := tx.QueryRow(ctx,
			`SELECT point_reservation_id FROM independent_evaluation WHERE id = $1`, id).Scan(&reservationID); err != nil {
			return err
		}
		if reservationID != nil {
			if err := points.Release(ctx, tx, *reservationID); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx,
			`DELETE FROM processing_job WHERE type = 'independent_evaluation_processing' AND payload_reference = $1`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `DELETE FROM independent_evaluation WHERE id = $1`, id); err != nil {
			return err
		}
	}
	return nil
}

func purgeResumeDocuments(ctx context.Context, tx pgx.Tx, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	// Release holds first: the bulk delete SET NULLs evaluation links.
	rows, err := tx.Query(ctx,
		`SELECT point_reservation_id FROM evaluation
		 WHERE resume_version_id IN (SELECT id FROM resume_version WHERE resume_document_id = ANY($1))
		 AND point_reservation_id IS NOT NULL`, ids)
	if err != nil {
		return err
	}
	var holds []string
	for rows.Next() {
		var hold string
		if err := rows.Scan(&hold); err != nil {
			rows.Close()
			return err
		}
		holds = append(holds, hold)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, hold := range holds {
		if err := points.Release(ctx, tx, hold); err != nil {
			return err
		}
	}
	statements := []struct {
		query string
		arg   []string
	}{
		{`DELETE FROM evaluation WHERE resume_version_id IN (SELECT id FROM resume_version WHERE resume_document_id = ANY($1))`, ids},
		{`DELETE FROM batch_evaluation_submission WHERE resume_submission_id IN (SELECT id FROM resume_submission WHERE resume_version_id IN (SELECT id FROM resume_version WHERE resume_document_id = ANY($1)))`, ids},
		{`DELETE FROM resume_submission WHERE resume_version_id IN (SELECT id FROM resume_version WHERE resume_document_id = ANY($1))`, ids},
		{`DELETE FROM batch_evaluation WHERE id NOT IN (SELECT batch_evaluation_id FROM batch_evaluation_submission) AND id NOT IN (SELECT batch_evaluation_id FROM evaluation WHERE batch_evaluation_id IS NOT NULL)`, nil},
		{`DELETE FROM resume_document WHERE id = ANY($1)`, ids},
	}
	for _, statement := range statements {
		var err error
		if statement.arg == nil {
			_, err = tx.Exec(ctx, statement.query)
		} else {
			_, err = tx.Exec(ctx, statement.query, statement.arg)
		}
		if err != nil {
			return err
		}
	}
	return nil
}
