// Resume-processing stage: parse, extract, store, embed, queue evaluations.
package pipeline

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/skillsignal/backend/versions"
	"github.com/skillsignal/contracts/jobs"
	parser "github.com/skillsignal/worker/internal/docparse"
	"github.com/skillsignal/worker/internal/extractor"
	"github.com/skillsignal/worker/internal/modelschemas"
	"github.com/skillsignal/worker/internal/prompts"
	"github.com/skillsignal/worker/internal/semantic"
)

func (w *Worker) parseResume(ctx context.Context, job *ClaimedJob) error {
	var storageKey, mediaType string
	err := w.pool.QueryRow(ctx,
		`SELECT document.storage_key, document.media_type
		 FROM resume_version AS version
		 JOIN resume_document AS document ON document.id = version.resume_document_id
		 WHERE version.id = $1`, job.PayloadReference).Scan(&storageKey, &mediaType)
	if err == pgx.ErrNoRows {
		return &NonRetryableError{Message: "Resume version not found"}
	}
	if err != nil {
		return err
	}
	content, err := w.store.Get(storageKey)
	if err != nil {
		return &NonRetryableError{Message: "Resume source document is unavailable"}
	}
	doc, _, normalizedFacts, err := prepareDocumentBounded(
		content, mediaType, time.Duration(w.settings.ParseTimeoutSeconds*float64(time.Second)))
	if err != nil {
		if parseErr, ok := err.(*parser.ParseError); ok {
			return &NonRetryableError{Message: parseErr.Message}
		}
		return err
	}
	normalizedFacts = addDocumentWarnings(normalizedFacts, doc.Quality.Warnings)
	stored, err := w.storeParsedResume(ctx, job, doc, nil, normalizedFacts)
	if err != nil {
		return err
	}
	if !stored {
		return nil
	}
	if doc.Quality.State != "ready" {
		return &NonRetryableError{Message: documentQualityError("Resume", doc.Quality.Warnings)}
	}
	return w.queueFollowupJob(ctx, job, jobs.ResumeEnrich)
}

func (w *Worker) enrichResume(ctx context.Context, job *ClaimedJob) error {
	doc, normalizedFacts, err := w.loadParsedResume(ctx, job.PayloadReference)
	if err != nil {
		return err
	}
	var extractedFacts map[string]any
	if w.models == nil {
		return &NonRetryableError{Message: "Inference is not configured"}
	} else {
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
		} else {
			validated, err := extractor.ValidateExtraction(raw, extractor.BlockTexts(redactedBlocks))
			if err != nil {
				return &NonRetryableError{Message: "Resume extraction failed validation"}
			} else {
				extractedFacts = validated
				merged, err := extractor.MergeFacts(normalizedFacts, validated, doc.Blocks)
				if err != nil {
					return err
				}
				normalizedFacts = addDocumentWarnings(merged, doc.Quality.Warnings)
			}
		}
	}
	stored, err := w.storeEnrichedResume(ctx, job, extractedFacts, normalizedFacts)
	if err != nil {
		return err
	}
	if !stored {
		return nil
	}
	if err := w.storeBlockEmbeddings(ctx, job); err != nil {
		return err
	}
	return w.queueEvaluations(ctx, job)
}

func (w *Worker) loadParsedResume(ctx context.Context, resumeVersionID string) (parser.ParsedDocument, map[string]any, error) {
	var artifactJSON, factsJSON []byte
	err := w.pool.QueryRow(ctx,
		`SELECT extraction_blocks, normalized_facts FROM resume_version WHERE id = $1`,
		resumeVersionID).Scan(&artifactJSON, &factsJSON)
	if err == pgx.ErrNoRows {
		return parser.ParsedDocument{}, nil, &NonRetryableError{Message: "Resume version not found"}
	}
	if err != nil {
		return parser.ParsedDocument{}, nil, err
	}
	var doc parser.ParsedDocument
	if err := json.Unmarshal(artifactJSON, &doc); err != nil {
		return parser.ParsedDocument{}, nil, &NonRetryableError{Message: "Parsed resume artifact is invalid"}
	}
	return doc, unmarshalJSONMap(factsJSON), nil
}

func (w *Worker) storeEnrichedResume(ctx context.Context, job *ClaimedJob, structuredFacts, normalizedFacts map[string]any) (bool, error) {
	structured := map[string]any{}
	var extractionPromptVersion *string
	if structuredFacts != nil {
		structured = structuredFacts
		version := versions.ExtractionPromptVersion
		extractionPromptVersion = &version
	}
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	active, err := lockLease(ctx, tx, job)
	if err != nil || !active {
		return false, err
	}
	var id string
	err = tx.QueryRow(ctx,
		`UPDATE resume_version
		 SET structured_facts = CAST($1 AS jsonb), normalized_facts = CAST($2 AS jsonb),
		     schema_version = $3, extraction_prompt_version = $4
		 WHERE id = $5
		   AND EXISTS (
		       SELECT 1 FROM processing_job
		       WHERE id = $6 AND lease_token = $7 AND lease_expires_at > now())
		 RETURNING id`,
		marshalJSON(structured), marshalJSON(normalizedFacts), versions.ResumeFactsSchemaVersion,
		extractionPromptVersion, job.PayloadReference, job.ID, job.LeaseToken).Scan(&id)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	contact, _ := normalizedFacts["contact"].(map[string]any)
	if contact == nil {
		return false, &NonRetryableError{Message: "Resume contact facts are invalid"}
	}
	if _, err := tx.Exec(ctx,
		`UPDATE candidate_record AS candidate
		 SET full_name = COALESCE(candidate.full_name, $1),
		     email = COALESCE(candidate.email, $2),
		     location = COALESCE(candidate.location, $3), updated_at = now()
		 FROM resume_submission AS submission
		 WHERE submission.candidate_record_id = candidate.id
		   AND submission.resume_version_id = $4`,
		nullableString(contact["name"]), nullableString(contact["email"]),
		nullableString(contact["location"]), job.PayloadReference); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

func (w *Worker) storeParsedResume(ctx context.Context, job *ClaimedJob, doc parser.ParsedDocument, structuredFacts, normalizedFacts map[string]any) (bool, error) {
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	active, err := lockLease(ctx, tx, job)
	if err != nil || !active {
		return false, err
	}
	structured := map[string]any{}
	if structuredFacts != nil {
		structured = structuredFacts
	}
	extractionPromptVersion := (*string)(nil)
	if structuredFacts != nil {
		version := versions.ExtractionPromptVersion
		extractionPromptVersion = &version
	}
	var id string
	err = tx.QueryRow(ctx,
		`UPDATE resume_version
		 SET extraction_blocks = CAST($1 AS jsonb),
		     structured_facts = CAST($2 AS jsonb),
		     normalized_facts = CAST($3 AS jsonb),
		     quality_state = $4,
		     parser_version = $5,
		     parser_configuration_version = $6,
		     schema_version = $7,
		     extraction_prompt_version = $8
		 WHERE id = $9
		   AND EXISTS (
		       SELECT 1 FROM processing_job
		       WHERE id = $10 AND lease_token = $11 AND lease_expires_at > now()
		   )
		 RETURNING id`,
		marshalJSON(artifactToMap(doc)), marshalJSON(structured), marshalJSON(normalizedFacts),
		doc.Quality.State, versions.LocalParserVersion, versions.ParserConfigurationVersion,
		versions.ResumeFactsSchemaVersion, extractionPromptVersion,
		job.PayloadReference, job.ID, job.LeaseToken).Scan(&id)
	if err == pgx.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if doc.Quality.State != "ready" {
		if _, err := tx.Exec(ctx,
			`UPDATE evaluation
			 SET status = 'failed', score = NULL, evidence_coverage = NULL,
			     eligibility = 'needs_review', quality_state = $1, completed_at = now()
			 WHERE resume_version_id = $2`,
			doc.Quality.State, job.PayloadReference); err != nil {
			return false, err
		}
		if err := releaseEvaluationReservations(ctx, tx, "resume_version_id = $1", []any{job.PayloadReference}); err != nil {
			return false, err
		}
		return true, tx.Commit(ctx)
	}
	contact, _ := normalizedFacts["contact"].(map[string]any)
	if contact == nil {
		return false, &NonRetryableError{Message: "Resume contact facts are invalid"}
	}
	if _, err := tx.Exec(ctx,
		`UPDATE candidate_record AS candidate
		 SET full_name = COALESCE(candidate.full_name, $1),
		     email = COALESCE(candidate.email, $2),
		     location = COALESCE(candidate.location, $3), updated_at = now()
		 FROM resume_submission AS submission
		 WHERE submission.candidate_record_id = candidate.id
		   AND submission.resume_version_id = $4`,
		nullableString(contact["name"]), nullableString(contact["email"]),
		nullableString(contact["location"]), job.PayloadReference); err != nil {
		return false, err
	}
	return true, tx.Commit(ctx)
}

func (w *Worker) queueEvaluations(ctx context.Context, job *ClaimedJob) error {
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	active, err := lockLease(ctx, tx, job)
	if err != nil || !active {
		return err
	}
	rows, err := tx.Query(ctx,
		`SELECT evaluation.id
		 FROM evaluation
		 WHERE evaluation.resume_version_id = $1
		   AND EXISTS (
		       SELECT 1 FROM processing_job
		       WHERE id = $2 AND lease_token = $3 AND lease_expires_at > now())`,
		job.PayloadReference, job.ID, job.LeaseToken)
	if err != nil {
		return err
	}
	var evaluationIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		evaluationIDs = append(evaluationIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, evaluationID := range evaluationIDs {
		if _, err := tx.Exec(ctx,
			`INSERT INTO processing_job (
			     id, type, status, payload_reference, idempotency_key,
			     attempt_count, maximum_attempts, available_at, created_at, updated_at
			 ) VALUES (
			     $1, $2, 'ready', $3, $3, 0, 3, now(), now(), now()
			 )
			 ON CONFLICT (type, idempotency_key) DO NOTHING`,
			newID(), jobs.EvaluationProcess, evaluationID); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (w *Worker) storeBlockEmbeddings(ctx context.Context, job *ClaimedJob) error {
	if w.models == nil {
		return nil
	}
	model := w.settings.OpenRouter.EmbeddingModel
	doc, _, err := w.loadParsedResume(ctx, job.PayloadReference)
	if err != nil {
		return err
	}
	textsByBlock := make(map[string]string)
	hashesByBlock := make(map[string]string)
	for _, block := range scoringBlocks(doc.Blocks) {
		textsByBlock[block.ID] = block.Text
		hashesByBlock[block.ID] = semantic.TextHash(block.Text)
	}
	if len(textsByBlock) == 0 {
		return nil
	}
	uniqueHashes := uniqueStrings(hashValues(hashesByBlock))
	cached, err := w.pool.Query(ctx,
		`SELECT text_hash, vector::text FROM embedding_cache WHERE model = $1 AND text_hash = ANY($2)`,
		model, uniqueHashes)
	if err != nil {
		return err
	}
	vectorsByHash := make(map[string][]float64)
	for cached.Next() {
		var hash, vectorText string
		if err := cached.Scan(&hash, &vectorText); err != nil {
			cached.Close()
			return err
		}
		var vector []float64
		if err := json.Unmarshal([]byte(vectorText), &vector); err != nil {
			cached.Close()
			return err
		}
		vectorsByHash[hash] = vector
	}
	cached.Close()
	if err := cached.Err(); err != nil {
		return err
	}
	missingTexts := make(map[string]string)
	for blockID, hash := range hashesByBlock {
		if _, ok := vectorsByHash[hash]; !ok {
			missingTexts[hash] = textsByBlock[blockID]
		}
	}
	if len(missingTexts) > 0 {
		var hashes []string
		var texts []string
		for hash, text := range missingTexts {
			hashes = append(hashes, hash)
			texts = append(texts, text)
		}
		vectors, usedModel, err := w.models.EmbedTextsWithModel(model, texts)
		if err != nil {
			return w.modelFailure(job, "Resume embedding", err)
		}
		if usedModel != model {
			// Cached primary vectors cannot be mixed with a fallback model's space.
			model = usedModel
			hashes, texts = nil, nil
			seen := make(map[string]bool)
			for blockID, hash := range hashesByBlock {
				if !seen[hash] {
					seen[hash] = true
					hashes = append(hashes, hash)
					texts = append(texts, textsByBlock[blockID])
				}
			}
			vectors, err = w.models.EmbedTextsExact(model, texts)
			if err != nil {
				return w.modelFailure(job, "Resume embedding", err)
			}
			vectorsByHash = make(map[string][]float64)
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
		for i, hash := range hashes {
			vectorsByHash[hash] = vectors[i]
			if _, err := tx.Exec(ctx,
				`INSERT INTO embedding_cache (model, text_hash, vector)
				 VALUES ($1, $2, CAST($3 AS jsonb))
				 ON CONFLICT (model, text_hash) DO NOTHING`,
				model, hash, marshalJSON(vectors[i])); err != nil {
				return err
			}
		}
		if err := tx.Commit(ctx); err != nil {
			return err
		}
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
	if _, err := tx.Exec(ctx, `DELETE FROM resume_block_embedding WHERE resume_version_id = $1`, job.PayloadReference); err != nil {
		return err
	}
	for blockID, hash := range hashesByBlock {
		vector, ok := vectorsByHash[hash]
		if !ok {
			continue
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO resume_block_embedding (resume_version_id, block_id, model, text_hash, vector)
			 VALUES ($1, $2, $3, $4, CAST($5 AS jsonb))
			 ON CONFLICT (resume_version_id, block_id, model) DO UPDATE
			     SET vector = EXCLUDED.vector, created_at = now()`,
			job.PayloadReference, blockID, model, hash, marshalJSON(vector)); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func artifactToMap(doc parser.ParsedDocument) map[string]any {
	blocks := make([]any, 0, len(doc.Blocks))
	for _, block := range doc.Blocks {
		blocks = append(blocks, map[string]any{
			"id": block.ID, "page": block.Page, "readingOrder": block.ReadingOrder,
			"text": block.Text, "method": block.Method, "bbox": block.BBox,
		})
	}
	return map[string]any{
		"blocks": blocks,
		"metadata": map[string]any{
			"mediaType": doc.Metadata.MediaType, "pageCount": doc.Metadata.PageCount,
			"blockCount": doc.Metadata.BlockCount, "characterCount": doc.Metadata.CharacterCount,
			"nonWhitespaceCharacterCount": doc.Metadata.NonWhitespaceCharacterCount,
		},
		"quality": map[string]any{"state": doc.Quality.State, "warnings": doc.Quality.Warnings},
	}
}

func addDocumentWarnings(facts map[string]any, warnings []string) map[string]any {
	seen := make(map[string]bool)
	var merged []any
	for _, warning := range warnings {
		if !seen[warning] {
			seen[warning] = true
			merged = append(merged, warning)
		}
	}
	if existing, ok := facts["warnings"].([]any); ok {
		for _, item := range existing {
			if warning, ok := item.(string); ok && !seen[warning] {
				seen[warning] = true
				merged = append(merged, warning)
			}
		}
	}
	out := make(map[string]any, len(facts)+1)
	for key, value := range facts {
		out[key] = value
	}
	out["warnings"] = merged
	return out
}

func nullableString(value any) *string {
	text, ok := value.(string)
	if !ok || text == "" {
		return nil
	}
	return &text
}

func trimSpace(text string) string {
	start := 0
	for start < len(text) && (text[start] == ' ' || text[start] == '\t' || text[start] == '\n' || text[start] == '\r') {
		start++
	}
	end := len(text)
	for end > start && (text[end-1] == ' ' || text[end-1] == '\t' || text[end-1] == '\n' || text[end-1] == '\r') {
		end--
	}
	return text[start:end]
}

func hashValues(mapping map[string]string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, hash := range mapping {
		if !seen[hash] {
			seen[hash] = true
			out = append(out, hash)
		}
	}
	return out
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}
