// Job-description stage: extract source text, compile drafts.
package pipeline

import (
	"context"
	"log"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/skillsignal/contracts/jobs"
	"github.com/skillsignal/go-core/versions"
	"github.com/skillsignal/worker/internal/compiler"
	parser "github.com/skillsignal/worker/internal/docparse"
	"github.com/skillsignal/worker/internal/modelschemas"
	"github.com/skillsignal/worker/internal/openrouter"
	"github.com/skillsignal/worker/internal/prompts"
)

func (w *Worker) parseJobDescription(ctx context.Context, job *ClaimedJob) error {
	var sourceText *string
	var storageKey *string
	var mediaType string
	err := w.pool.QueryRow(ctx,
		`SELECT source_text, source_storage_key, source_media_type FROM job_version WHERE id = $1`,
		job.PayloadReference).Scan(&sourceText, &storageKey, &mediaType)
	if err == pgx.ErrNoRows {
		return &NonRetryableError{Message: "Job version not found"}
	}
	if err != nil {
		return err
	}
	var source string
	if storageKey != nil && *storageKey != "" {
		content, err := w.store.Get(*storageKey)
		if err != nil {
			return &NonRetryableError{Message: "Resume source document is unavailable"}
		}
		text, err := extractTextBounded(content, mediaType, w.parseTimeout())
		if err != nil {
			return err
		}
		source = text
	} else if sourceText != nil {
		source = *sourceText
	} else {
		return &NonRetryableError{Message: "Job description is unavailable"}
	}
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var updated string
	err = tx.QueryRow(ctx,
		`UPDATE job_version
		 SET source_text = $1, normalized_text = $2
		 WHERE id = $3
		   AND EXISTS (
		       SELECT 1 FROM processing_job
		       WHERE id = $4 AND lease_token = $5 AND lease_expires_at > now())
		 RETURNING id`,
		source, trimSpace(source), job.PayloadReference, job.ID, job.LeaseToken).Scan(&updated)
	if err == pgx.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return w.queueFollowupJob(ctx, job, jobs.JobDescriptionEnrich)
}

func (w *Worker) enrichJobDescription(ctx context.Context, job *ClaimedJob) error {
	var source string
	err := w.pool.QueryRow(ctx,
		`SELECT normalized_text FROM job_version WHERE id = $1`, job.PayloadReference).Scan(&source)
	if err == pgx.ErrNoRows {
		return &NonRetryableError{Message: "Job version not found"}
	}
	if err != nil {
		return err
	}
	var artifact map[string]any
	if w.models == nil {
		artifact = compiler.CompileJobDescription(source, nil, true,
			"Model extraction was unavailable; deterministic drafts require careful review")
	} else {
		raw, err := w.models.CompleteJSON(
			w.settings.OpenRouter.ExtractionModel,
			prompts.JobRequirementsSystemPrompt,
			"<job_description_data>\n"+marshalJSON(map[string]any{
				"documentType": "job_description",
				"blocks":       compiler.BlocksForModel(compiler.SourceBlocks(source)),
			})+"\n</job_description_data>",
			"job_requirement_drafts",
			modelschemas.RequirementDrafts(),
			w.settings.OpenRouter.MaxOutputTokens,
		)
		if err != nil {
			if _, ok := err.(*openrouter.RetryableError); ok {
				if rerr := w.requeueOrDegrade(job, "Requirement extraction", err); rerr != nil {
					return rerr
				}
			}
			log.Printf("job %s: job requirement extraction failed; using deterministic drafts: %v", job.ID, err)
			artifact = compiler.CompileJobDescription(source, nil, true,
				"Model extraction failed; deterministic drafts require careful review")
		} else {
			artifact = compiler.CompileJobDescription(source, raw, false, "")
		}
	}
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	schemaVersion, _ := artifact["schemaVersion"].(string)
	if _, err := tx.Exec(ctx,
		`UPDATE job_version
			 SET draft_requirements = CAST($1 AS jsonb),
			     schema_version = $2, prompt_version = $3, compiler_version = $4
			 WHERE id = $5
			   AND EXISTS (
			       SELECT 1 FROM processing_job
			       WHERE id = $6 AND lease_token = $7 AND lease_expires_at > now())`,
		marshalJSON(artifact), schemaVersion,
		versions.JobRequirementsPromptVersion, versions.JobRequirementsCompilerVersion,
		job.PayloadReference, job.ID, job.LeaseToken); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func extractTextBounded(content []byte, mediaType string, timeout time.Duration) (string, error) {
	type outcome struct {
		text string
		err  error
	}
	done := make(chan outcome, 1)
	go func() {
		doc, err := parser.ExtractBlocks(content, mediaType)
		if err != nil {
			done <- outcome{err: err}
			return
		}
		var texts []string
		for _, block := range doc.Blocks {
			if trimmed := trimSpace(block.Text); trimmed != "" {
				texts = append(texts, trimmed)
			}
		}
		done <- outcome{text: joinStrings(texts, "\n\n")}
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case result := <-done:
		if result.err != nil {
			if parseErr, ok := result.err.(*parser.ParseError); ok {
				return "", &NonRetryableError{Message: parseErr.Message}
			}
			return "", result.err
		}
		return result.text, nil
	case <-timer.C:
		return "", &NonRetryableError{Message: "Document parsing exceeded the configured time limit"}
	}
}
