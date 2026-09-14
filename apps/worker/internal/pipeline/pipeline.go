// Package pipeline runs the processing-job queue: lease claims with
// heartbeats, per-type stage dispatch, guarded writes, completion billing,
// and retryable-vs-fatal failure handling.
package pipeline

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"log"
	"math"
	mrand "math/rand"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/skillsignal/go-core/config"
	"github.com/skillsignal/go-core/ids"
	"github.com/skillsignal/go-core/storage"
	"github.com/skillsignal/go-core/versions"
	parser "github.com/skillsignal/worker/internal/docparse"
	"github.com/skillsignal/worker/internal/evaluator"
	"github.com/skillsignal/worker/internal/extractor"
	"github.com/skillsignal/worker/internal/modelschemas"
	"github.com/skillsignal/worker/internal/normalizer"
	"github.com/skillsignal/worker/internal/openrouter"
	"github.com/skillsignal/worker/internal/prompts"
)

const (
	// DeterministicExtractionWarning marks reports built without the model.
	DeterministicExtractionWarning = "Structured extraction was unavailable; only deterministic facts were used"
	// AssessmentUnavailableDegradation marks unevaluated model assessment.
	AssessmentUnavailableDegradation = "Model assessment was unavailable; deterministic outcomes were used"
	// AssessmentFailureDegradation marks failed model assessment.
	AssessmentFailureDegradation = "Model assessment failed after retries; deterministic outcomes were used"
)

// NonRetryableError fails a job without requeueing.
type NonRetryableError struct{ Message string }

func (e *NonRetryableError) Error() string { return e.Message }

// RetryableError sends a job back to the queue with backoff.
type RetryableError struct{ Message string }

func (e *RetryableError) Error() string { return e.Message }

// ClaimedJob is one leased queue row.
type ClaimedJob struct {
	ID               string
	Type             string
	PayloadReference string
	LeaseToken       string
	AttemptCount     int
	MaximumAttempts  int
}

// Worker drains the queue against one pool.
type Worker struct {
	pool     *pgxpool.Pool
	settings config.Settings
	models   *openrouter.Client
	store    *storage.Store
}

// New builds a worker; models is nil when no API key is configured.
func New(pool *pgxpool.Pool, settings config.Settings, store *storage.Store) *Worker {
	var models *openrouter.Client
	if settings.OpenRouter.APIKey != "" {
		models = openrouter.New(
			settings.OpenRouter.APIKey,
			settings.OpenRouter.BaseURL,
			time.Duration(settings.OpenRouter.TimeoutSeconds*float64(time.Second)),
		)
	}
	return &Worker{pool: pool, settings: settings, models: models, store: store}
}

// Run polls until the context ends.
func (w *Worker) Run(ctx context.Context) {
	for {
		worked, err := w.RunOnce(ctx)
		if err != nil {
			log.Printf("worker iteration error: %v", err)
		}
		if !worked {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Duration(w.settings.WorkerPollIntervalSeconds * float64(time.Second))):
			}
		}
		if ctx.Err() != nil {
			return
		}
	}
}

// RunOnce claims and processes a single job.
func (w *Worker) RunOnce(ctx context.Context) (bool, error) {
	job, err := w.claim(ctx)
	if err != nil {
		return false, err
	}
	if job == nil {
		return false, nil
	}
	started := time.Now()
	if w.models != nil {
		w.models.ResetUsage()
	}
	outcome := "completed"
	heartbeatCtx, stopHeartbeat := context.WithCancel(context.Background())
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		w.heartbeat(heartbeatCtx, job)
	}()
	dispatchErr := w.dispatch(ctx, job)
	stopHeartbeat()
	<-heartbeatDone
	switch classified := dispatchErr.(type) {
	case nil:
		if err := w.complete(ctx, job); err != nil {
			return true, err
		}
	case *NonRetryableError:
		outcome = "failed_non_retryable"
		if err := w.fail(ctx, job, classified.Message, false); err != nil {
			return true, err
		}
	case *RetryableError:
		outcome = "failed_retryable"
		if err := w.fail(ctx, job, classified.Message, true); err != nil {
			return true, err
		}
	default:
		outcome = "failed"
		log.Printf("processing job %s failed: %v", job.ID, dispatchErr)
		if err := w.fail(ctx, job, "Processing failed", true); err != nil {
			return true, err
		}
	}
	log.Printf("job %s (%s) %s in %s", job.ID, job.Type, outcome, time.Since(started).Truncate(time.Millisecond))
	return true, nil
}

func (w *Worker) heartbeat(ctx context.Context, job *ClaimedJob) {
	interval := w.settings.WorkerLeaseSeconds / 3
	if interval < 1 {
		interval = 1
	}
	ticker := time.NewTicker(time.Duration(interval) * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			expiresAt := time.Now().UTC().Add(time.Duration(w.settings.WorkerLeaseSeconds) * time.Second)
			tag, err := w.pool.Exec(context.Background(),
				`UPDATE processing_job SET lease_expires_at = $1
				 WHERE id = $2 AND lease_token = $3 AND status = 'processing'`,
				expiresAt, job.ID, job.LeaseToken)
			if err != nil {
				log.Printf("lease renewal failed for job %s: %v", job.ID, err)
				continue
			}
			if tag.RowsAffected() == 0 {
				log.Printf("lease renewal found no active claim for job %s", job.ID)
			}
		}
	}
}

// requeueOrDegrade requeues on transient model failures while attempts
// remain; on the final attempt it returns nil so the caller degrades.
func (w *Worker) requeueOrDegrade(job *ClaimedJob, stage string, err error) error {
	if _, ok := err.(*openrouter.RetryableError); !ok {
		return nil
	}
	if job.AttemptCount < job.MaximumAttempts {
		return &RetryableError{Message: stage + " hit a transient provider failure"}
	}
	return nil
}

func (w *Worker) claim(ctx context.Context) (*ClaimedJob, error) {
	var tokenBytes [24]byte
	if _, err := rand.Read(tokenBytes[:]); err != nil {
		return nil, err
	}
	leaseToken := base64.RawURLEncoding.EncodeToString(tokenBytes[:])
	expiresAt := time.Now().UTC().Add(time.Duration(w.settings.WorkerLeaseSeconds) * time.Second)
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)
	var job ClaimedJob
	err = tx.QueryRow(ctx,
		`SELECT id, type, payload_reference, attempt_count, maximum_attempts
		 FROM processing_job
		 WHERE (status = 'ready' AND available_at <= now())
		    OR (status = 'processing' AND lease_expires_at < now())
		 ORDER BY available_at, created_at
		 FOR UPDATE SKIP LOCKED
		 LIMIT 1`).Scan(&job.ID, &job.Type, &job.PayloadReference, &job.AttemptCount, &job.MaximumAttempts)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE processing_job
		 SET status = 'processing', lease_token = $1, lease_expires_at = $2,
		     attempt_count = attempt_count + 1, updated_at = now()
		 WHERE id = $3`,
		leaseToken, expiresAt, job.ID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	job.LeaseToken = leaseToken
	job.AttemptCount++
	return &job, nil
}

func (w *Worker) dispatch(ctx context.Context, job *ClaimedJob) error {
	switch job.Type {
	case "job_description_processing":
		return w.processJobDescription(ctx, job)
	case "independent_evaluation_processing":
		return w.processIndependentEvaluation(ctx, job)
	case "evaluation_processing":
		return w.prepareEvaluation(ctx, job)
	case "resume_processing":
		return w.processResume(ctx, job)
	default:
		return &NonRetryableError{Message: "Unsupported processing job type"}
	}
}

func (w *Worker) prepareEvaluation(ctx context.Context, job *ClaimedJob) error {
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var factsJSON []byte
	var jobVersionID string
	var resumeVersionID string
	var qualityState string
	var reservationID *string
	err = tx.QueryRow(ctx, `
		SELECT resume.normalized_facts, evaluation.job_version_id, resume.id,
		       resume.quality_state, evaluation.point_reservation_id
		FROM evaluation AS evaluation
		JOIN resume_version AS resume ON resume.id = evaluation.resume_version_id
		WHERE evaluation.id = $1`, job.PayloadReference).Scan(
		&factsJSON, &jobVersionID, &resumeVersionID, &qualityState, &reservationID)
	if err == pgx.ErrNoRows {
		return &NonRetryableError{Message: "Evaluation not found"}
	}
	if err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `
		SELECT id, kind, weight, normalized_text, category, assessability, predicate
		FROM job_requirement
		WHERE job_version_id = $1 AND confirmed_at IS NOT NULL
		ORDER BY id`, jobVersionID)
	if err != nil {
		return err
	}
	var requirements []map[string]any
	for rows.Next() {
		var id, kind, text, category, assessability string
		var weight int
		var predicate []byte
		if err := rows.Scan(&id, &kind, &weight, &text, &category, &assessability, &predicate); err != nil {
			rows.Close()
			return err
		}
		requirements = append(requirements, map[string]any{
			"id": id, "kind": kind, "weight": weight,
			"normalizedText": text, "category": category,
			"assessability": assessability, "predicate": unmarshalJSONMap(predicate),
		})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	facts := unmarshalJSONMap(factsJSON)
	deterministic := evaluator.Evaluate(facts, requirements)
	deterministicEvidence := make(map[string][]evaluator.Evidence, len(deterministic.Assessments))
	for _, assessment := range deterministic.Assessments {
		deterministicEvidence[assessment.RequirementID] = assessment.Evidence
	}
	assessments := deterministic.Assessments
	assessmentDegradation := AssessmentUnavailableDegradation
	assessmentSchemaVersion := (*string)(nil)
	assessmentPromptVersion := (*string)(nil)
	if w.models != nil {
		blockRows, err := tx.Query(ctx, `
			SELECT block->>'id', block->>'text'
			FROM resume_version, jsonb_array_elements(extraction_blocks->'blocks') AS block
			WHERE id = $1`, resumeVersionID)
		if err != nil {
			return err
		}
		blockTexts := make(map[string]string)
		blocks := make([]any, 0)
		for blockRows.Next() {
			var id, text string
			if err := blockRows.Scan(&id, &text); err != nil {
				blockRows.Close()
				return err
			}
			blockTexts[id] = text
			blocks = append(blocks, map[string]any{"id": id, "text": text})
		}
		blockRows.Close()
		if err := blockRows.Err(); err != nil {
			return err
		}
		modelRequirements := make([]map[string]any, 0, len(requirements))
		requirementIDs := make(map[string]bool, len(requirements))
		for _, requirement := range requirements {
			if requirement["kind"] == "ignored" || requirement["assessability"] != "resume_evidence" {
				continue
			}
			if id, ok := requirement["id"].(string); ok {
				requirementIDs[id] = true
				modelRequirements = append(modelRequirements, requirement)
			}
		}
		raw, err := w.models.CompleteJSON(
			w.settings.OpenRouter.AssessmentModel,
			prompts.AssessmentSystemPrompt,
			map[string]any{"requirements": modelRequirements, "resumeBlocks": blocks},
			"requirement_assessments",
			modelschemas.AssessmentOutput(),
			w.settings.OpenRouter.MaxOutputTokens,
		)
		if err == nil {
			modelAssessments, validationErr := extractor.ValidateAssessments(raw, requirementIDs, blockTexts)
			if validationErr == nil {
				assessments = evaluator.RefineAssessments(assessments, modelAssessments, requirements)
				assessmentDegradation = ""
				schemaVersion := versions.RequirementAssessmentSchemaVer
				promptVersion := versions.AssessmentPromptVersion
				assessmentSchemaVersion = &schemaVersion
				assessmentPromptVersion = &promptVersion
			} else {
				log.Printf("job %s: model assessment validation failed; using deterministic outcomes: %v", job.ID, validationErr)
				assessmentDegradation = AssessmentFailureDegradation
			}
		} else {
			if _, ok := err.(*openrouter.RetryableError); ok {
				if rerr := w.requeueOrDegrade(job, "Requirement assessment", err); rerr != nil {
					return rerr
				}
			}
			log.Printf("job %s: model assessment failed; using deterministic outcomes: %v", job.ID, err)
			assessmentDegradation = AssessmentFailureDegradation
		}
	}
	result := evaluator.Summarize(assessments, requirements)
	for _, assessment := range result.Assessments {
		if _, err := tx.Exec(ctx, `
			INSERT INTO requirement_assessment
				(id, evaluation_id, job_requirement_id, outcome, confidence, reasoning, evidence, deterministic_evidence)
			VALUES ($1, $2, $3, $4, $5, $6, CAST($7 AS jsonb), CAST($8 AS jsonb))
			ON CONFLICT (evaluation_id, job_requirement_id) DO UPDATE SET
				outcome = EXCLUDED.outcome, confidence = EXCLUDED.confidence,
				reasoning = EXCLUDED.reasoning, evidence = EXCLUDED.evidence,
				deterministic_evidence = EXCLUDED.deterministic_evidence`,
			newID(), job.PayloadReference, assessment.RequirementID,
			assessment.Outcome, assessment.Confidence, assessment.Reasoning,
			marshalJSON(assessment.Evidence), marshalJSON(deterministicEvidence[assessment.RequirementID])); err != nil {
			return err
		}
	}
	var score any
	if result.Score != nil {
		score = *result.Score
	}
	if _, err := tx.Exec(ctx, `
		UPDATE evaluation SET status = 'complete', score = $1,
			evidence_coverage = $2, eligibility = $3, quality_state = $4,
			scoring_policy_version = $5, assessment_schema_version = $6,
			assessment_prompt_version = $7, assessment_degradation = $8,
			completed_at = now()
		WHERE id = $9 AND EXISTS (
			SELECT 1 FROM processing_job
			WHERE id = $10 AND lease_token = $11 AND lease_expires_at > now())`,
		score, result.EvidenceCoverage, result.Eligibility, qualityState,
		"criterion-weighted-1", assessmentSchemaVersion, assessmentPromptVersion,
		nullableIfEmpty(assessmentDegradation), job.PayloadReference, job.ID, job.LeaseToken); err != nil {
		return err
	}
	if reservationID != nil {
		promptTokens, completionTokens, reportedCost := w.modelUsage()
		charged := settlePointsFromUsage(promptTokens, completionTokens, reportedCost,
			"employer_resume", w.settings.Billing)
		if err := settleReservation(ctx, tx, *reservationID, charged, "Employer evaluation charge"); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (w *Worker) complete(ctx context.Context, job *ClaimedJob) error {
	return w.updateJob(ctx, job, "completed", nil, nil)
}

func (w *Worker) fail(ctx context.Context, job *ClaimedJob, message string, retryable bool) error {
	if retryable && job.AttemptCount < job.MaximumAttempts {
		delay := math.Min(300, math.Pow(2, float64(job.AttemptCount))) + mrand.Float64()
		availableAt := time.Now().UTC().Add(time.Duration(delay * float64(time.Second)))
		if err := w.updateJob(ctx, job, "ready", &message, &availableAt); err != nil {
			return err
		}
		return nil
	}
	updated, err := w.updateJobReturning(ctx, job, "dead", &message, nil)
	if err != nil {
		return err
	}
	if !updated {
		return nil
	}
	switch job.Type {
	case "independent_evaluation_processing":
		return w.failIndependentEvaluation(ctx, job, message)
	case "resume_processing":
		return w.failResume(ctx, job)
	case "evaluation_processing":
		return w.failEvaluation(ctx, job)
	default:
		return nil
	}
}

func (w *Worker) updateJob(ctx context.Context, job *ClaimedJob, status string, safeError *string, availableAt *time.Time) error {
	_, err := w.updateJobReturning(ctx, job, status, safeError, availableAt)
	return err
}

func (w *Worker) updateJobReturning(ctx context.Context, job *ClaimedJob, status string, safeError *string, availableAt *time.Time) (bool, error) {
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	var id string
	err = tx.QueryRow(ctx,
		`UPDATE processing_job
		 SET status = $1, safe_error = $2, available_at = COALESCE($3, available_at),
		     lease_token = NULL, lease_expires_at = NULL, updated_at = now()
		 WHERE id = $4 AND lease_token = $5
		 RETURNING id`,
		status, safeError, availableAt, job.ID, job.LeaseToken).Scan(&id)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

func (w *Worker) failIndependentEvaluation(ctx context.Context, job *ClaimedJob, message string) error {
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx,
		`UPDATE independent_evaluation SET status = 'failed', safe_error = $1 WHERE id = $2`,
		message, job.PayloadReference); err != nil {
		return err
	}
	// Failed evaluations release the point hold and return a consumed
	// weekly free allowance so it can be used again.
	if _, err := tx.Exec(ctx,
		`UPDATE point_reservation SET state = 'released', updated_at = now()
		 WHERE state = 'reserved' AND id = (
		     SELECT point_reservation_id FROM independent_evaluation WHERE id = $1)`,
		job.PayloadReference); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`DELETE FROM weekly_free_use
		 WHERE (user_id, week_start) IN (
		     SELECT user_id, free_week_start FROM independent_evaluation
		     WHERE id = $1 AND free_week_start IS NOT NULL)`,
		job.PayloadReference); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (w *Worker) failResume(ctx context.Context, job *ClaimedJob) error {
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx,
		`UPDATE resume_version
		 SET quality_state = CASE WHEN quality_state = 'pending' THEN 'failed' ELSE quality_state END
		 WHERE id = $1`, job.PayloadReference); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE evaluation
		 SET status = 'failed', eligibility = 'needs_review',
		     quality_state = CASE WHEN quality_state = 'pending' THEN 'failed' ELSE quality_state END,
		     completed_at = now()
		 WHERE resume_version_id = $1`, job.PayloadReference); err != nil {
		return err
	}
	if err := releaseEvaluationReservations(ctx, tx, "resume_version_id = $1", []any{job.PayloadReference}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (w *Worker) failEvaluation(ctx context.Context, job *ClaimedJob) error {
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx,
		`UPDATE evaluation SET status = 'failed', eligibility = 'needs_review', completed_at = now()
		 WHERE id = $1`, job.PayloadReference); err != nil {
		return err
	}
	if err := releaseEvaluationReservations(ctx, tx, "id = $1", []any{job.PayloadReference}); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// prepareDocument parses with a wall-clock bound; the parse runs on a
// worker goroutine so an overlong document fails safely on timeout.
func prepareDocumentBounded(content []byte, mediaType string, timeout time.Duration) (parser.ParsedDocument, []normalizer.Block, map[string]any, error) {
	type outcome struct {
		doc    parser.ParsedDocument
		blocks []normalizer.Block
		facts  map[string]any
		err    error
	}
	done := make(chan outcome, 1)
	go func() {
		doc, err := parser.ExtractBlocks(content, mediaType)
		if err != nil {
			done <- outcome{err: err}
			return
		}
		blocks := make([]normalizer.Block, 0, len(doc.Blocks))
		for _, block := range doc.Blocks {
			blocks = append(blocks, normalizer.Block{ID: block.ID, Text: block.Text})
		}
		facts, err := normalizer.Normalize(blocks)
		if err != nil {
			done <- outcome{err: err}
			return
		}
		normalized := map[string]any{
			"contact": contactToMap(facts.Contact),
			"skills":  skillsToMaps(facts.Skills),
		}
		done <- outcome{doc: doc, blocks: blocks, facts: normalized}
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case result := <-done:
		return result.doc, result.blocks, result.facts, result.err
	case <-timer.C:
		return parser.ParsedDocument{}, nil, nil, &NonRetryableError{Message: "Document parsing exceeded the configured time limit"}
	}
}

func contactToMap(contact map[string]*string) map[string]any {
	out := make(map[string]any, len(contact)+1)
	for key, value := range contact {
		if value == nil {
			out[key] = nil
		} else {
			out[key] = *value
		}
	}
	out["phone"] = nil
	return out
}

func skillsToMaps(skills []normalizer.Skill) []any {
	out := make([]any, 0, len(skills))
	for _, skill := range skills {
		ids := make([]any, 0, len(skill.EvidenceBlockIDs))
		for _, id := range skill.EvidenceBlockIDs {
			ids = append(ids, id)
		}
		out = append(out, map[string]any{
			"canonicalName": skill.CanonicalName, "category": skill.Category,
			"evidenceBlockIds": ids,
		})
	}
	return out
}

func documentQualityError(label string, warnings []string) string {
	detail := "Extraction quality was too low"
	if len(warnings) > 0 {
		detail = joinStrings(warnings, "; ")
	}
	return label + " could not be read reliably. " + detail + ". Upload a clearer digital document"
}

func joinStrings(values []string, separator string) string {
	out := ""
	for i, value := range values {
		if i > 0 {
			out += separator
		}
		out += value
	}
	return out
}

func marshalJSON(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return "null"
	}
	return string(raw)
}

func unmarshalJSONMap(raw []byte) map[string]any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]any{}
	}
	if out == nil {
		return map[string]any{}
	}
	return out
}

func newID() string { return ids.New() }
