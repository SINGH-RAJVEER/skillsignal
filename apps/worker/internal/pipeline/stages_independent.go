// Independent-evaluation stage: parse, extract, report, render, settle.
package pipeline

import (
	"context"
	"log"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/skillsignal/go-core/versions"
	"github.com/skillsignal/worker/internal/docparse"
	"github.com/skillsignal/worker/internal/extractor"
	"github.com/skillsignal/worker/internal/independent"
	"github.com/skillsignal/worker/internal/modelschemas"
	"github.com/skillsignal/worker/internal/openrouter"
	"github.com/skillsignal/worker/internal/prompts"
	"github.com/skillsignal/worker/internal/renderer"
)

func (w *Worker) parseTimeout() time.Duration {
	return time.Duration(w.settings.ParseTimeoutSeconds * float64(time.Second))
}

func (w *Worker) processIndependentEvaluation(ctx context.Context, job *ClaimedJob) error {
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return err
	}
	var storageKey, mediaType, originalName string
	var jobDescription *string
	var descriptionKey, descriptionMediaType *string
	err = tx.QueryRow(ctx,
		`UPDATE independent_evaluation
		 SET status = 'processing', safe_error = NULL
		 WHERE id = $1
		 RETURNING storage_key, media_type, original_name, job_description,
		           job_description_key, job_description_media_type`,
		job.PayloadReference).Scan(
		&storageKey, &mediaType, &originalName,
		&jobDescription, &descriptionKey, &descriptionMediaType)
	if err == pgx.ErrNoRows {
		tx.Rollback(ctx)
		return &NonRetryableError{Message: "Independent evaluation not found"}
	}
	if err != nil {
		tx.Rollback(ctx)
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	content, err := w.store.Get(storageKey)
	if err != nil {
		return &NonRetryableError{Message: "Resume source document is unavailable"}
	}
	doc, _, normalizedFacts, err := prepareDocumentBounded(content, mediaType, w.parseTimeout())
	if err != nil {
		if parseErr, ok := err.(*docparse.ParseError); ok {
			return &NonRetryableError{Message: parseErr.Message}
		}
		return err
	}
	if doc.Quality.State != "ready" {
		return &NonRetryableError{Message: documentQualityError("Resume", doc.Quality.Warnings)}
	}
	var description string
	if descriptionKey != nil && *descriptionKey != "" {
		descriptionMedia := ""
		if descriptionMediaType != nil {
			descriptionMedia = *descriptionMediaType
		}
		descriptionContent, err := w.store.Get(*descriptionKey)
		if err != nil {
			return &NonRetryableError{Message: "Resume source document is unavailable"}
		}
		text, err := extractTextBounded(descriptionContent, descriptionMedia, w.parseTimeout())
		if err != nil {
			return err
		}
		description = text
	} else if jobDescription != nil {
		description = *jobDescription
	}
	modelUsed := false
	if w.models != nil {
		raw, err := w.models.CompleteJSON(
			w.settings.OpenRouter.ExtractionModel,
			prompts.ExtractionSystemPrompt,
			[]any{map[string]any{"type": "text", "text": "<resume_document>\n" + extractor.BlocksDocument(doc.Blocks) + "\n</resume_document>"}},
			"resume_facts",
			modelschemas.ResumeFacts(),
			w.settings.OpenRouter.MaxOutputTokens,
		)
		if err != nil {
			if _, ok := err.(*openrouter.RetryableError); ok {
				if rerr := w.requeueOrDegrade(job, "Resume fact extraction", err); rerr != nil {
					return rerr
				}
			}
			log.Printf("job %s: model extraction failed; using deterministic facts: %v", job.ID, err)
			normalizedFacts = addDocumentWarnings(normalizedFacts, []string{DeterministicExtractionWarning})
		} else {
			validated, err := extractor.ValidateExtraction(raw, extractor.BlockTexts(doc.Blocks))
			if err != nil {
				log.Printf("job %s: model extraction failed; using deterministic facts: %v", job.ID, err)
				normalizedFacts = addDocumentWarnings(normalizedFacts, []string{DeterministicExtractionWarning})
			} else {
				merged, err := extractor.MergeFacts(normalizedFacts, validated, doc.Blocks)
				if err != nil {
					return err
				}
				facts := addDocumentWarnings(merged, doc.Quality.Warnings)
				score, suggestions := independent.Report(facts, description)
				var extra []map[string]any
				if rawSuggestions, ok := validated["suggestions"].([]any); ok {
					for _, item := range rawSuggestions {
						if entry, ok := item.(map[string]any); ok {
							extra = append(extra, entry)
						}
					}
				}
				return w.storeIndependentReport(ctx, job, facts, score,
					extractor.MergeSuggestions(suggestions, extra, 10),
					true, nullableIfEmpty(description))
			}
		}
	} else {
		normalizedFacts = addDocumentWarnings(normalizedFacts, []string{DeterministicExtractionWarning})
	}
	score, suggestions := independent.Report(normalizedFacts, description)
	return w.storeIndependentReport(ctx, job, normalizedFacts,
		score, suggestions, modelUsed, nullableIfEmpty(description))
}

func (w *Worker) storeIndependentReport(ctx context.Context, job *ClaimedJob, facts map[string]any, score int, suggestions []map[string]any, modelUsed bool, jobDescription *string) error {
	improvedKey := "independent-resumes/improved/" + job.PayloadReference + ".docx"
	rendered, err := renderer.RenderResumeDocx(facts, suggestions)
	if err != nil {
		log.Printf("job %s: corrected resume rendering failed: %v", job.ID, err)
		improvedKey = ""
	} else if err := w.store.Put(improvedKey, rendered); err != nil {
		log.Printf("job %s: corrected resume storage failed: %v", job.ID, err)
		improvedKey = ""
	}
	var improvedKeyPtr *string
	if improvedKey != "" {
		improvedKeyPtr = &improvedKey
	}
	var extractionPromptVersion *string
	if modelUsed {
		version := versions.ExtractionPromptVersion
		extractionPromptVersion = &version
	}
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var reservationID *string
	err = tx.QueryRow(ctx,
		`UPDATE independent_evaluation
		 SET status = 'complete', score = $1,
		     suggestions = CAST($2 AS jsonb),
		     normalized_facts = CAST($3 AS jsonb),
		     job_description = COALESCE(job_description, $4),
		     improved_resume_key = $5,
		     improved_resume_unlocked_at = now(),
		     parser_version = $6,
		     parser_configuration_version = $7,
		     schema_version = $8,
		     extraction_prompt_version = $9,
		     scoring_policy_version = $10,
		     safe_error = NULL, completed_at = now()
		 WHERE id = $11
		   AND EXISTS (
		       SELECT 1 FROM processing_job
		       WHERE id = $12 AND lease_token = $13 AND lease_expires_at > now())
		 RETURNING point_reservation_id`,
		score, marshalJSON(suggestions), marshalJSON(facts), jobDescription,
		improvedKeyPtr, versions.LocalParserVersion, versions.ParserConfigurationVersion,
		versions.ResumeFactsSchemaVersion, extractionPromptVersion,
		versions.ScoringPolicyVersion,
		job.PayloadReference, job.ID, job.LeaseToken).Scan(&reservationID)
	if err == pgx.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	if reservationID != nil {
		promptTokens, completionTokens, costUSD := w.modelUsage()
		kind := "independent_evaluation"
		charged := settlePointsFromUsage(promptTokens, completionTokens, costUSD, kind, w.settings.Billing)
		if err := settleReservation(ctx, tx, *reservationID, charged, "Independent evaluation charge"); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (w *Worker) modelUsage() (int, int, float64) {
	if w.models == nil {
		return 0, 0, 0
	}
	return w.models.Usage()
}

func nullableIfEmpty(text string) *string {
	if text == "" {
		return nil
	}
	return &text
}
