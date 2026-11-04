package pipeline

import (
	"context"
	"strconv"

	"github.com/jackc/pgx/v5"
	parser "github.com/skillsignal/worker/internal/docparse"
	"github.com/skillsignal/worker/internal/extractor"
	"github.com/skillsignal/worker/internal/modelschemas"
	"github.com/skillsignal/worker/internal/normalizer"
	"github.com/skillsignal/worker/internal/openrouter"
	"github.com/skillsignal/worker/internal/prompts"
)

func scoringBlocks(blocks []parser.Block) []parser.Block {
	input := make([]normalizer.Block, 0, len(blocks))
	byID := make(map[string]parser.Block)
	for _, block := range blocks {
		input = append(input, normalizer.Block{ID: block.ID, Text: block.Text})
		byID[block.ID] = block
	}
	var out []parser.Block
	for _, block := range normalizer.ScoringBlocks(input) {
		original := byID[block.ID]
		original.Text = block.Text
		out = append(out, original)
	}
	return out
}

func (w *Worker) modelFailure(job *ClaimedJob, stage string, err error) error {
	if _, ok := err.(*openrouter.RetryableError); ok {
		if job.AttemptCount < job.MaximumAttempts {
			return &RetryableError{Message: stage + " hit a transient provider failure"}
		}
	}
	return &NonRetryableError{Message: stage + " failed after configured models were exhausted"}
}

func (w *Worker) assessCriterion(job *ClaimedJob, requirement map[string]any, retrieved map[string]string) ([]map[string]any, error) {
	id := requirement["id"].(string)
	raw, err := w.models.CompleteJSON(w.settings.OpenRouter.AssessmentModel, prompts.AssessmentSystemPrompt,
		map[string]any{"requirements": []map[string]any{requirement}, "resumeBlocks": retrieved},
		"requirement_assessments", modelschemas.AssessmentOutput(), w.settings.OpenRouter.MaxOutputTokens)
	if err != nil {
		return nil, w.modelFailure(job, "Criterion assessment", err)
	}
	validated, err := extractor.ValidateAssessments(raw, map[string]bool{id: true}, retrieved)
	if err != nil || len(validated) != 1 {
		return nil, &NonRetryableError{Message: "Criterion assessment failed validation"}
	}
	return validated, nil
}

func (w *Worker) persistModelCalls(ctx context.Context, job *ClaimedJob) error {
	if w.models == nil {
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
	if err := w.persistModelCallsTx(ctx, tx, job); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (w *Worker) persistModelCallsTx(ctx context.Context, tx pgx.Tx, job *ClaimedJob) error {
	if w.models == nil {
		return nil
	}
	for index, call := range w.models.Calls() {
		id := job.ID + ":" + job.LeaseToken + ":" + strconv.Itoa(index)
		_, err := tx.Exec(ctx, `INSERT INTO inference_call (id, processing_job_id, model, provider, request_id, task, status, prompt_tokens, completion_tokens, cost_usd, latency_ms, fallback_reason, prompt_version, schema_version) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) ON CONFLICT (id) DO NOTHING`, id, job.ID, call.Model, call.Provider, call.RequestID, call.Task, call.Status, call.PromptTokens, call.CompletionTokens, call.CostUSD, call.LatencyMS, nullableIfEmpty(call.FallbackReason), nullableIfEmpty(call.PromptVersion), nullableIfEmpty(call.SchemaVersion))
		if err != nil {
			return err
		}
	}
	return nil
}
