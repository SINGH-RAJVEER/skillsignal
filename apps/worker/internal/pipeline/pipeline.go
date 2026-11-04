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

	"github.com/skillsignal/backend/config"
	"github.com/skillsignal/backend/ids"
	"github.com/skillsignal/backend/storage"
	"github.com/skillsignal/backend/versions"
	"github.com/skillsignal/contracts/jobs"
	parser "github.com/skillsignal/worker/internal/docparse"
	"github.com/skillsignal/worker/internal/evaluator"
	"github.com/skillsignal/worker/internal/normalizer"
	"github.com/skillsignal/worker/internal/openrouter"
	"github.com/skillsignal/worker/internal/semantic"
)

const (
	// AssessmentUnavailableDegradation marks unevaluated model assessment.
	AssessmentUnavailableDegradation = "Model assessment was unavailable; deterministic outcomes were used"
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
	store    storage.Store
	role     jobs.Role
	jobTypes []string
}

// New builds a worker restricted to the durable job types owned by role.
func New(pool *pgxpool.Pool, settings config.Settings, store storage.Store, role jobs.Role) *Worker {
	var models *openrouter.Client
	if role == jobs.InferenceRole {
		models = openrouter.New(
			settings.OpenRouter.APIKey,
			settings.OpenRouter.BaseURL,
			time.Duration(settings.OpenRouter.TimeoutSeconds*float64(time.Second)),
		)
	}
	if models != nil {
		models.ConfigureFallbacks("extraction", settings.OpenRouter.ExtractionModel, settings.OpenRouter.ExtractionFallbacks)
		models.ConfigureFallbacks("assessment", settings.OpenRouter.AssessmentModel, settings.OpenRouter.AssessmentFallbacks)
		models.ConfigureFallbacks("embedding", settings.OpenRouter.EmbeddingModel, settings.OpenRouter.EmbeddingFallbacks)
	}
	return &Worker{
		pool: pool, settings: settings, models: models, store: store,
		role: role, jobTypes: jobs.TypesFor(role),
	}
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
	if w.models != nil {
		if err := w.models.ValidateConfigured(); err != nil {
			return false, err
		}
	}
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
	finalReport := job.Type == string(jobs.EvaluationProcess) || job.Type == string(jobs.IndependentEvaluationEnrich)
	if dispatchErr != nil || !finalReport {
		if err := w.persistModelCalls(ctx, job); err != nil {
			dispatchErr = err
		}
	}
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
				 WHERE id = $2 AND lease_token = $3 AND status = 'processing' AND lease_expires_at > now()`,
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
		 WHERE type = ANY($1)
		   AND ((status = 'ready' AND available_at <= now())
		    OR (status = 'processing' AND lease_expires_at < now()))
		 ORDER BY available_at, created_at
		 FOR UPDATE SKIP LOCKED
		 LIMIT 1`, w.jobTypes).Scan(&job.ID, &job.Type, &job.PayloadReference, &job.AttemptCount, &job.MaximumAttempts)
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
	case string(jobs.JobDescriptionParse):
		return w.parseJobDescription(ctx, job)
	case string(jobs.JobDescriptionEnrich):
		return w.enrichJobDescription(ctx, job)
	case string(jobs.IndependentEvaluationParse):
		return w.parseIndependentEvaluation(ctx, job)
	case string(jobs.IndependentEvaluationEnrich):
		return w.enrichIndependentEvaluation(ctx, job)
	case string(jobs.EvaluationProcess):
		return w.prepareEvaluation(ctx, job)
	case string(jobs.ResumeParse):
		return w.parseResume(ctx, job)
	case string(jobs.ResumeEnrich):
		return w.enrichResume(ctx, job)
	default:
		return &NonRetryableError{Message: "Unsupported processing job type"}
	}
}

func (w *Worker) queueFollowupJob(ctx context.Context, current *ClaimedJob, next jobs.Type) error {
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	active, err := lockLease(ctx, tx, current)
	if err != nil || !active {
		return err
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO processing_job (
		     id, type, status, payload_reference, idempotency_key,
		     attempt_count, maximum_attempts, available_at, created_at, updated_at
		 ) VALUES ($1, $2, 'ready', $3, $4, 0, 3, now(), now(), now())
		 ON CONFLICT (type, idempotency_key) DO NOTHING`,
		newID(), next, current.PayloadReference, string(next)+":"+current.PayloadReference); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (w *Worker) prepareEvaluation(ctx context.Context, job *ClaimedJob) error {
	if w.models == nil {
		return &NonRetryableError{Message: "Inference is not configured"}
	}
	var err error
	tx := w.pool
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
	semanticByRequirement := make(map[string][]map[string]any)
	if qualityState != "ready" {
		return &NonRetryableError{Message: "Resume extraction quality is insufficient"}
	}
	if w.models != nil {
		doc, _, err := w.loadParsedResume(ctx, resumeVersionID)
		if err != nil {
			return err
		}
		blockTexts := make(map[string]string)
		for _, block := range scoringBlocks(doc.Blocks) {
			blockTexts[block.ID] = block.Text
		}
		modelRequirements := make([]map[string]any, 0, len(requirements))
		for _, requirement := range requirements {
			if requirement["kind"] == "ignored" || requirement["assessability"] != "resume_evidence" {
				continue
			}
			if _, ok := requirement["id"].(string); ok {
				modelRequirements = append(modelRequirements, requirement)
			}
		}
		blockRows, err := tx.Query(ctx, `SELECT block_id, model, vector::text FROM resume_block_embedding WHERE resume_version_id = $1`, resumeVersionID)
		if err != nil {
			return err
		}
		blockVectors := make(map[string][]float64)
		storedModel := ""
		for blockRows.Next() {
			var id, model, vectorJSON string
			if err := blockRows.Scan(&id, &model, &vectorJSON); err != nil {
				blockRows.Close()
				return err
			}
			if storedModel != "" && storedModel != model {
				blockRows.Close()
				return &NonRetryableError{Message: "Stored embeddings mix incompatible models"}
			}
			storedModel = model
			var vector []float64
			if err := json.Unmarshal([]byte(vectorJSON), &vector); err != nil {
				blockRows.Close()
				return err
			}
			if blockTexts[id] != "" {
				blockVectors[id] = vector
			}
		}
		blockRows.Close()
		if err := blockRows.Err(); err != nil {
			return err
		}
		semanticTexts := make([]string, 0, len(modelRequirements))
		for _, requirement := range modelRequirements {
			semanticTexts = append(semanticTexts, requirement["normalizedText"].(string))
		}
		if len(semanticTexts) > 0 && len(blockVectors) > 0 {
			vectors, err := w.models.EmbedTextsExact(storedModel, semanticTexts)
			if err != nil {
				return w.modelFailure(job, "Requirement semantic retrieval", err)
			}
			for index, requirement := range modelRequirements {
				id := requirement["id"].(string)
				for _, match := range semantic.TopSemanticMatches(vectors[index], blockVectors) {
					semanticByRequirement[id] = append(semanticByRequirement[id], map[string]any{
						"blockId": match.BlockID, "text": blockTexts[match.BlockID], "similarity": match.Similarity,
					})
				}
				requirement["semanticEvidence"] = semanticByRequirement[id]
			}
		}
		var modelAssessments []map[string]any
		for _, requirement := range modelRequirements {
			id := requirement["id"].(string)
			retrieved := make(map[string]string)
			for _, match := range semanticByRequirement[id] {
				blockID := match["blockId"].(string)
				retrieved[blockID] = blockTexts[blockID]
			}
			for _, evidence := range deterministicEvidence[id] {
				if text := blockTexts[evidence.BlockID]; text != "" {
					retrieved[evidence.BlockID] = text
				}
			}
			validated, err := w.assessCriterion(job, requirement, retrieved)
			if err != nil {
				return err
			}
			modelAssessments = append(modelAssessments, validated...)
		}
		assessments = evaluator.RefineAssessments(assessments, modelAssessments, requirements)
		assessmentDegradation = ""
		schemaVersion := versions.RequirementAssessmentSchemaVer
		promptVersion := versions.AssessmentPromptVersion
		assessmentSchemaVersion = &schemaVersion
		assessmentPromptVersion = &promptVersion
	}
	writeTx, err := w.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer writeTx.Rollback(ctx)
	active, err := lockLease(ctx, writeTx, job)
	if err != nil || !active {
		return err
	}
	result := evaluator.Summarize(assessments, requirements)
	if err := w.persistModelCallsTx(ctx, writeTx, job); err != nil {
		return err
	}
	for _, assessment := range result.Assessments {
		if _, err := writeTx.Exec(ctx, `
			INSERT INTO requirement_assessment
				(id, evaluation_id, job_requirement_id, outcome, confidence, reasoning, evidence, deterministic_evidence, semantic_evidence)
			VALUES ($1, $2, $3, $4, $5, $6, CAST($7 AS jsonb), CAST($8 AS jsonb), CAST($9 AS jsonb))
			ON CONFLICT (evaluation_id, job_requirement_id) DO UPDATE SET
				outcome = EXCLUDED.outcome, confidence = EXCLUDED.confidence,
				reasoning = EXCLUDED.reasoning, evidence = EXCLUDED.evidence,
				deterministic_evidence = EXCLUDED.deterministic_evidence,
				semantic_evidence = EXCLUDED.semantic_evidence`,
			newID(), job.PayloadReference, assessment.RequirementID,
			assessment.Outcome, assessment.Confidence, assessment.Reasoning,
			marshalJSON(assessment.Evidence), marshalJSON(deterministicEvidence[assessment.RequirementID]), marshalJSON(semanticByRequirement[assessment.RequirementID])); err != nil {
			return err
		}
	}
	var score any
	if result.Score != nil {
		score = *result.Score
	}
	tag, err := writeTx.Exec(ctx, `
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
		nullableIfEmpty(assessmentDegradation), job.PayloadReference, job.ID, job.LeaseToken)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return nil
	}
	if reservationID != nil {
		promptTokens, completionTokens, reportedCost := w.modelUsage()
		charged := settlePointsFromUsage(promptTokens, completionTokens, reportedCost,
			"employer_resume", w.settings.Billing)
		if err := settleReservation(ctx, writeTx, *reservationID, charged, "Employer evaluation charge"); err != nil {
			return err
		}
	}
	if err := completeReportJob(ctx, writeTx, job); err != nil {
		return err
	}
	return writeTx.Commit(ctx)
}

func completeReportJob(ctx context.Context, tx pgx.Tx, job *ClaimedJob) error {
	_, err := tx.Exec(ctx, `UPDATE processing_job SET status = 'completed', lease_token = NULL,
		lease_expires_at = NULL, updated_at = now() WHERE id = $1 AND lease_token = $2`, job.ID, job.LeaseToken)
	return err
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
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	active, err := lockLease(ctx, tx, job)
	if err != nil || !active {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE processing_job SET status = 'dead', safe_error = $1,
		lease_token = NULL, lease_expires_at = NULL, updated_at = now() WHERE id = $2`, message, job.ID); err != nil {
		return err
	}
	switch job.Type {
	case string(jobs.IndependentEvaluationParse), string(jobs.IndependentEvaluationEnrich):
		err = w.failIndependentEvaluation(ctx, tx, job, message)
	case string(jobs.ResumeParse), string(jobs.ResumeEnrich):
		err = w.failResume(ctx, tx, job)
	case string(jobs.EvaluationProcess):
		err = w.failEvaluation(ctx, tx, job)
	}
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
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
		 WHERE id = $4 AND lease_token = $5 AND status = 'processing' AND lease_expires_at > now()
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

func (w *Worker) failIndependentEvaluation(ctx context.Context, tx pgx.Tx, job *ClaimedJob, message string) error {
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
	return nil
}

func (w *Worker) failResume(ctx context.Context, tx pgx.Tx, job *ClaimedJob) error {
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
	return nil
}

func (w *Worker) failEvaluation(ctx context.Context, tx pgx.Tx, job *ClaimedJob) error {
	if _, err := tx.Exec(ctx,
		`UPDATE evaluation SET status = 'failed', eligibility = 'needs_review', completed_at = now()
		 WHERE id = $1`, job.PayloadReference); err != nil {
		return err
	}
	if err := releaseEvaluationReservations(ctx, tx, "id = $1", []any{job.PayloadReference}); err != nil {
		return err
	}
	return nil
}

// prepareDocument parses with a wall-clock bound; the parse runs on a
// worker goroutine so an overlong document fails safely on timeout.
func prepareDocumentBounded(content []byte, mediaType string, timeout time.Duration) (parser.ParsedDocument, []normalizer.Block, map[string]any, error) {
	doc, err := parseSubprocess(content, mediaType, timeout, false)
	if err != nil {
		return parser.ParsedDocument{}, nil, nil, err
	}
	blocks := make([]normalizer.Block, 0, len(doc.Blocks))
	for _, block := range doc.Blocks {
		blocks = append(blocks, normalizer.Block{ID: block.ID, Text: block.Text})
	}
	facts, err := normalizer.Normalize(blocks)
	if err != nil {
		return parser.ParsedDocument{}, nil, nil, err
	}
	return doc, blocks, map[string]any{"contact": contactToMap(facts.Contact), "skills": skillsToMaps(facts.Skills)}, nil
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
