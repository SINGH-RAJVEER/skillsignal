// Independent-evaluation stage: parse, extract, report, render, settle.
package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/skillsignal/backend/versions"
	"github.com/skillsignal/contracts/jobs"
	parser "github.com/skillsignal/worker/internal/docparse"
	"github.com/skillsignal/worker/internal/evaluator"
	"github.com/skillsignal/worker/internal/extractor"
	"github.com/skillsignal/worker/internal/independent"
	"github.com/skillsignal/worker/internal/modelschemas"
	"github.com/skillsignal/worker/internal/prompts"
	"github.com/skillsignal/worker/internal/renderer"
	"github.com/skillsignal/worker/internal/semantic"
)

func (w *Worker) parseTimeout() time.Duration {
	return time.Duration(w.settings.ParseTimeoutSeconds * float64(time.Second))
}

func (w *Worker) parseIndependentEvaluation(ctx context.Context, job *ClaimedJob) error {
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
		   AND EXISTS (
		       SELECT 1 FROM processing_job
		       WHERE id = $2 AND lease_token = $3 AND lease_expires_at > now())
		 RETURNING storage_key, media_type, original_name, job_description,
		           job_description_key, job_description_media_type`,
		job.PayloadReference, job.ID, job.LeaseToken).Scan(
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
		if parseErr, ok := err.(*parser.ParseError); ok {
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
	tx, err = w.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	var updated string
	err = tx.QueryRow(ctx,
		`UPDATE independent_evaluation
		 SET extraction_blocks = CAST($1 AS jsonb),
		     normalized_facts = CAST($2 AS jsonb),
		     job_description = $3,
		     parser_version = $4,
		     parser_configuration_version = $5
		 WHERE id = $6
		   AND EXISTS (
		       SELECT 1 FROM processing_job
		       WHERE id = $7 AND lease_token = $8 AND lease_expires_at > now())
		 RETURNING id`,
		marshalJSON(artifactToMap(doc)), marshalJSON(addDocumentWarnings(normalizedFacts, doc.Quality.Warnings)),
		nullableIfEmpty(description), versions.LocalParserVersion, versions.ParserConfigurationVersion,
		job.PayloadReference, job.ID, job.LeaseToken).Scan(&updated)
	if err == pgx.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	return w.queueFollowupJob(ctx, job, jobs.IndependentEvaluationEnrich)
}

func (w *Worker) enrichIndependentEvaluation(ctx context.Context, job *ClaimedJob) error {
	if w.models == nil {
		return &NonRetryableError{Message: "Inference is not configured"}
	}
	var artifactJSON, factsJSON []byte
	var jobDescription *string
	err := w.pool.QueryRow(ctx,
		`SELECT extraction_blocks, normalized_facts, job_description
		 FROM independent_evaluation WHERE id = $1`,
		job.PayloadReference).Scan(&artifactJSON, &factsJSON, &jobDescription)
	if err == pgx.ErrNoRows {
		return &NonRetryableError{Message: "Independent evaluation not found"}
	}
	if err != nil {
		return err
	}
	var doc parser.ParsedDocument
	if err := json.Unmarshal(artifactJSON, &doc); err != nil {
		return &NonRetryableError{Message: "Parsed resume artifact is invalid"}
	}
	normalizedFacts := unmarshalJSONMap(factsJSON)
	description := ""
	if jobDescription != nil {
		description = *jobDescription
	}
	redactedBlocks := scoringBlocks(doc.Blocks)
	raw, err := w.models.CompleteJSON(
		w.settings.OpenRouter.ExtractionModel,
		prompts.ExtractionSystemPrompt,
		[]any{map[string]any{"type": "text", "text": "<resume_document>\n" + extractor.BlocksDocument(redactedBlocks) + "\n</resume_document>"}},
		"resume_facts",
		modelschemas.ResumeFacts(),
		w.settings.OpenRouter.MaxOutputTokens,
	)
	if err != nil {
		return w.modelFailure(job, "Resume fact extraction", err)
	}
	validated, err := extractor.ValidateExtraction(raw, extractor.BlockTexts(redactedBlocks))
	if err != nil {
		return &NonRetryableError{Message: "Resume extraction failed validation"}
	}
	merged, err := extractor.MergeFacts(normalizedFacts, validated, doc.Blocks)
	if err != nil {
		return err
	}
	facts := addDocumentWarnings(merged, doc.Quality.Warnings)
	generalScore, suggestions := independent.Report(facts, description)
	roleScore, coverage, requirements, assessments := independent.RoleReport(description, facts)
	if len(requirements) > 0 {
		texts := make([]string, 0, len(redactedBlocks))
		for _, block := range redactedBlocks {
			texts = append(texts, block.Text)
		}
		vectors, model, err := w.models.EmbedTextsWithModel(w.settings.OpenRouter.EmbeddingModel, texts)
		if err != nil {
			return w.modelFailure(job, "Private report evidence embedding", err)
		}
		blockVectors := make(map[string][]float64, len(vectors))
		for index, block := range redactedBlocks {
			blockVectors[block.ID] = vectors[index]
		}
		blockTexts := extractor.BlockTexts(redactedBlocks)
		deterministic := evaluator.Evaluate(facts, requirements)
		var refined []map[string]any
		for _, requirement := range requirements {
			if requirement["assessability"] != "resume_evidence" {
				continue
			}
			query, err := w.models.EmbedTextsExact(model, []string{requirement["normalizedText"].(string)})
			if err != nil {
				return w.modelFailure(job, "Private report evidence retrieval", err)
			}
			retrieved := make(map[string]string)
			for _, match := range semantic.TopSemanticMatches(query[0], blockVectors) {
				retrieved[match.BlockID] = blockTexts[match.BlockID]
			}
			id := requirement["id"].(string)
			for _, assessment := range deterministic.Assessments {
				if assessment.RequirementID != id {
					continue
				}
				for _, evidence := range assessment.Evidence {
					if text := blockTexts[evidence.BlockID]; text != "" {
						retrieved[evidence.BlockID] = text
					}
				}
			}
			validated, err := w.assessCriterion(job, requirement, retrieved)
			if err != nil {
				return err
			}
			refined = append(refined, validated...)
		}
		result := evaluator.Summarize(evaluator.RefineAssessments(deterministic.Assessments, refined, requirements), requirements)
		roleScore, coverage, assessments = result.Score, result.EvidenceCoverage, independent.AssessmentMaps(result.Assessments)
	}
	var extra []map[string]any
	if rawSuggestions, ok := validated["suggestions"].([]any); ok {
		for _, item := range rawSuggestions {
			if entry, ok := item.(map[string]any); ok {
				extra = append(extra, entry)
			}
		}
	}
	return w.storeIndependentReport(ctx, job, facts, generalScore, roleScore, coverage, requirements, assessments,
		extractor.MergeSuggestions(suggestions, extra, 10), true, nullableIfEmpty(description))
}

func (w *Worker) storeIndependentReport(ctx context.Context, job *ClaimedJob, facts map[string]any, generalScore int, roleScore *int, coverage int, requirements, assessments, suggestions []map[string]any, modelUsed bool, jobDescription *string) (err error) {
	improvedKey := "independent-resumes/improved/" + job.PayloadReference + "/" + job.LeaseToken + ".docx"
	committed := false
	defer func() {
		if !committed {
			if cleanupErr := w.store.Delete(improvedKey); cleanupErr != nil {
				err = errors.Join(err, &RetryableError{Message: "Corrected resume cleanup failed"})
			}
		}
	}()
	rendered, err := renderer.RenderResumeDocx(facts, suggestions)
	if err != nil {
		return &NonRetryableError{Message: "Corrected resume rendering failed"}
	} else if err := w.store.Put(improvedKey, rendered); err != nil {
		return &RetryableError{Message: "Corrected resume storage failed"}
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
	active, err := lockLease(ctx, tx, job)
	if err != nil || !active {
		return err
	}
	var reservationID *string
	if err := w.persistModelCallsTx(ctx, tx, job); err != nil {
		return err
	}
	var score any
	var coverageValue any
	if roleScore != nil {
		score = *roleScore
	}
	if jobDescription != nil {
		coverageValue = coverage
	}
	err = tx.QueryRow(ctx,
		`UPDATE independent_evaluation
		 SET status = 'complete', score = $1, general_quality_score = $2,
		     evidence_coverage = $3, requirements = CAST($4 AS jsonb),
		     requirement_assessments = CAST($5 AS jsonb),
		     suggestions = CAST($6 AS jsonb),
		     normalized_facts = CAST($7 AS jsonb),
		     job_description = COALESCE(job_description, $8),
		     improved_resume_key = $9,
		     improved_resume_unlocked_at = now(),
		     parser_version = $10,
		     parser_configuration_version = $11,
		     schema_version = $12,
		     extraction_prompt_version = $13,
		     scoring_policy_version = $14,
		     safe_error = NULL, completed_at = now()
		 WHERE id = $15
		   AND EXISTS (
		       SELECT 1 FROM processing_job
		       WHERE id = $16 AND lease_token = $17 AND status = 'processing' AND lease_expires_at > now())
		 RETURNING point_reservation_id`,
		score, generalScore, coverageValue, marshalJSON(requirements), marshalJSON(assessments),
		marshalJSON(suggestions), marshalJSON(facts), jobDescription,
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
	if err := completeReportJob(ctx, tx, job); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	committed = true
	return nil
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
