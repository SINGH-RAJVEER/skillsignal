package pipeline

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/skillsignal/backend/config"
	"github.com/skillsignal/backend/storage"
	"github.com/skillsignal/contracts/jobs"
	parser "github.com/skillsignal/worker/internal/docparse"
	"github.com/skillsignal/worker/internal/openrouter"
)

func workflowPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL must name an explicit PostgreSQL worker test target")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	schema := "worker_test_" + strings.ToLower(newID())
	if _, err := admin.Exec(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close(); admin.Exec(ctx, `DROP SCHEMA "`+schema+`" CASCADE`); admin.Close() })
	_, err = pool.Exec(ctx, `
		CREATE TABLE processing_job (id text PRIMARY KEY, type text, payload_reference text, status text, lease_token text, lease_expires_at timestamptz, safe_error text, available_at timestamptz DEFAULT now(), created_at timestamptz DEFAULT now(), updated_at timestamptz DEFAULT now(), attempt_count int DEFAULT 0, maximum_attempts int DEFAULT 3, idempotency_key text, UNIQUE(type,idempotency_key));
		CREATE TABLE resume_version (id text PRIMARY KEY, normalized_facts jsonb, extraction_blocks jsonb, quality_state text);
		CREATE TABLE evaluation (id text PRIMARY KEY, resume_version_id text, job_version_id text, point_reservation_id text, status text, score int, evidence_coverage int, eligibility text, quality_state text, scoring_policy_version text, assessment_schema_version text, assessment_prompt_version text, assessment_degradation text, completed_at timestamptz);
		CREATE TABLE job_requirement (id text PRIMARY KEY, job_version_id text, kind text, weight int, normalized_text text, category text, assessability text, predicate jsonb, confirmed_at timestamptz);
		CREATE TABLE requirement_assessment (id text PRIMARY KEY, evaluation_id text, job_requirement_id text, outcome text, confidence float, reasoning text, evidence jsonb, deterministic_evidence jsonb, semantic_evidence jsonb, UNIQUE(evaluation_id,job_requirement_id));
		CREATE TABLE resume_block_embedding (resume_version_id text, block_id text, model text, vector jsonb);
		CREATE TABLE point_reservation (id text PRIMARY KEY, account_id text, amount int, state text, updated_at timestamptz);
		CREATE TABLE point_ledger_entry (id text PRIMARY KEY, account_id text, amount int, reason text, idempotency_key text, UNIQUE(account_id,idempotency_key));
		CREATE TABLE inference_call (id text PRIMARY KEY, processing_job_id text, model text, provider text, request_id text, task text, status text, prompt_tokens int, completion_tokens int, cost_usd numeric, latency_ms bigint, fallback_reason text, prompt_version text, schema_version text);
	`)
	if err != nil {
		t.Fatal(err)
	}
	return pool
}

func seedEvaluation(t *testing.T, pool *pgxpool.Pool) *ClaimedJob {
	t.Helper()
	ctx := context.Background()
	doc := parser.ParsedDocument{Blocks: []parser.Block{{ID: "identity", Text: "Jane Doe\njane@example.com"}, {ID: "evidence", Text: "Built Python services for software engineering teams."}}, Quality: parser.Quality{State: "ready"}}
	_, err := pool.Exec(ctx, `INSERT INTO resume_version VALUES ('resume','{}',$1,'ready');`, marshalJSON(artifactToMap(doc)))
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO processing_job (id,type,payload_reference,status,lease_token,lease_expires_at) VALUES ('queue','evaluation_process','evaluation','processing','current',now()+interval '1 minute');
		INSERT INTO evaluation(id,resume_version_id,job_version_id,point_reservation_id,status,quality_state) VALUES ('evaluation','resume','job','hold','processing','ready');
		INSERT INTO point_reservation VALUES ('hold','account',50,'reserved',now());
		INSERT INTO job_requirement VALUES ('criterion','job','required',1,'Python required','skill','resume_evidence','{}',now());
		INSERT INTO resume_block_embedding VALUES ('resume','evidence','stored-fallback','[1,0]');
	`)
	if err != nil {
		t.Fatal(err)
	}
	return &ClaimedJob{ID: "queue", Type: string(jobs.EvaluationProcess), PayloadReference: "evaluation", LeaseToken: "current", AttemptCount: 1, MaximumAttempts: 3}
}

func TestAssessmentUsesStoredModelAndFencesWritesAndSettlement(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(map[bool]string{false: "active", true: "lease-lost-during-model-call"}[stale], func(t *testing.T) {
			pool := workflowPool(t)
			job := seedEvaluation(t, pool)
			ctx := context.Background()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/models" {
					json.NewEncoder(w).Encode(map[string]any{"data": []any{
						map[string]any{"id": "stored-fallback", "pricing": map[string]string{"prompt": "0", "completion": "0"}, "architecture": map[string]any{"input_modalities": []string{"text"}, "output_modalities": []string{"embeddings"}}},
						map[string]any{"id": "assessment", "pricing": map[string]string{"prompt": "0", "completion": "0"}, "architecture": map[string]any{"input_modalities": []string{"text"}}, "supported_parameters": []string{"structured_outputs"}},
					}})
					return
				}
				var payload map[string]any
				json.NewDecoder(r.Body).Decode(&payload)
				if r.URL.Path == "/embeddings" {
					if payload["model"] != "stored-fallback" {
						t.Error("retrieval used a different embedding space")
					}
					json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"index": 0, "embedding": []int{1, 0}}}})
					return
				}
				messages := payload["messages"].([]any)
				input, ok := messages[1].(map[string]any)["content"].(string)
				if !ok || strings.Contains(input, "Jane") || strings.Contains(input, "example.com") || !strings.Contains(input, "Built Python") {
					t.Errorf("assessment input was not redacted retrieved evidence")
				}
				if stale {
					if _, err := pool.Exec(ctx, `UPDATE processing_job SET lease_token='replacement' WHERE id='queue'`); err != nil {
						t.Error(err)
					}
				}
				content := `{"assessments":[{"requirementId":"criterion","outcome":"met","confidence":1,"reasoning":"Documented Python work.","evidence":[{"blockId":"evidence","quote":"Built Python services"}]}]}`
				json.NewEncoder(w).Encode(map[string]any{"model": "assessment", "choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"content": content}}}})
			}))
			defer server.Close()
			client := openrouter.New("fixture", server.URL, time.Second)
			worker := &Worker{pool: pool, models: client, settings: config.Settings{}}
			worker.settings.OpenRouter.AssessmentModel = "assessment"
			worker.settings.OpenRouter.EmbeddingModel = "different-primary"
			worker.settings.Billing.MinEmployerResumePoints = 7
			if err := worker.prepareEvaluation(ctx, job); err != nil {
				t.Fatal(err)
			}
			var count int
			if err := pool.QueryRow(ctx, `SELECT count(*) FROM requirement_assessment`).Scan(&count); err != nil {
				t.Fatal(err)
			}
			var state string
			pool.QueryRow(ctx, `SELECT state FROM point_reservation WHERE id='hold'`).Scan(&state)
			if stale && (count != 0 || state != "reserved") {
				t.Fatalf("stale claim wrote assessments=%d state=%s", count, state)
			}
			if !stale && (count != 1 || state != "settled") {
				t.Fatalf("active claim failed assessments=%d state=%s", count, state)
			}
			if !stale {
				var amount int
				if err := pool.QueryRow(ctx, `SELECT amount FROM point_ledger_entry`).Scan(&amount); err != nil || amount != -7 {
					t.Fatalf("charge=%d err=%v", amount, err)
				}
				var prompt, schema string
				if err := pool.QueryRow(ctx, `SELECT prompt_version,schema_version FROM inference_call WHERE task='assessment'`).Scan(&prompt, &schema); err != nil || prompt != "1" || schema != "1" {
					t.Fatalf("assessment provenance prompt=%s schema=%s err=%v", prompt, schema, err)
				}
			}
		})
	}
}

func TestFailureReleasesEmployerHoldOnlyForActiveLease(t *testing.T) {
	pool := workflowPool(t)
	job := seedEvaluation(t, pool)
	worker := &Worker{pool: pool}
	ctx := context.Background()
	job.LeaseToken = "stale"
	if err := worker.fail(ctx, job, "Unreadable resume", false); err != nil {
		t.Fatal(err)
	}
	var state string
	pool.QueryRow(ctx, `SELECT state FROM point_reservation WHERE id='hold'`).Scan(&state)
	if state != "reserved" {
		t.Fatal("stale failure released current hold")
	}
	job.LeaseToken = "current"
	job.Type = string(jobs.ResumeParse)
	job.PayloadReference = "resume"
	if err := worker.fail(ctx, job, "Unreadable resume", false); err != nil {
		t.Fatal(err)
	}
	pool.QueryRow(ctx, `SELECT state FROM point_reservation WHERE id='hold'`).Scan(&state)
	if state != "released" {
		t.Fatalf("failed resume hold=%s", state)
	}
	var status string
	pool.QueryRow(ctx, `SELECT status FROM evaluation WHERE id='evaluation'`).Scan(&status)
	if status != "failed" {
		t.Fatalf("evaluation status=%s", status)
	}
}

func TestParserSubprocessHandlesHTML(t *testing.T) {
	text, err := extractTextBounded([]byte("<h2>Requirements</h2><p>Python experience is required for our software engineering team to build services.</p>"), "text/html", 2*time.Second)
	if err != nil || !strings.Contains(text, "Python experience") {
		t.Fatalf("text=%q err=%v", text, err)
	}
}

func TestIndependentAssessmentAndArtifactCleanup(t *testing.T) {
	for _, stale := range []bool{false, true} {
		t.Run(map[bool]string{false: "completed-report", true: "stale-report"}[stale], func(t *testing.T) {
			pool := workflowPool(t)
			ctx := context.Background()
			_, err := pool.Exec(ctx, `CREATE TABLE independent_evaluation (
				id text PRIMARY KEY, extraction_blocks jsonb, normalized_facts jsonb, job_description text,
				status text, score int, general_quality_score int, evidence_coverage int,
				requirements jsonb, requirement_assessments jsonb, suggestions jsonb, improved_resume_key text,
				improved_resume_unlocked_at timestamptz, parser_version text, parser_configuration_version text,
				schema_version text, extraction_prompt_version text, scoring_policy_version text,
				safe_error text, completed_at timestamptz, point_reservation_id text);
				INSERT INTO processing_job(id,status,lease_token,lease_expires_at) VALUES ('private-job','processing','current',now()+interval '1 minute');
				INSERT INTO point_reservation VALUES ('private-hold','account',50,'reserved',now());`)
			if err != nil {
				t.Fatal(err)
			}
			doc := parser.ParsedDocument{Blocks: []parser.Block{{ID: "header", Text: "Jane Doe | Software Engineer | jane@example.com"}, {ID: "work", Text: "Built Python software services for engineering teams."}}, Quality: parser.Quality{State: "ready"}}
			facts := map[string]any{"contact": map[string]any{"name": "Jane Doe", "email": "jane@example.com"}, "skills": []any{map[string]any{"canonicalName": "Python", "evidenceBlockIds": []any{"work"}}}}
			_, err = pool.Exec(ctx, `INSERT INTO independent_evaluation(id,extraction_blocks,normalized_facts,job_description,status,point_reservation_id) VALUES ('private',$1,$2,'Requirements
- Python required','processing','private-hold')`, marshalJSON(artifactToMap(doc)), marshalJSON(facts))
			if err != nil {
				t.Fatal(err)
			}
			assessed := false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/models" {
					json.NewEncoder(w).Encode(map[string]any{"data": []any{
						map[string]any{"id": "chat", "pricing": map[string]string{"prompt": "0", "completion": "0"}, "architecture": map[string]any{"input_modalities": []string{"text"}}, "supported_parameters": []string{"structured_outputs"}},
						map[string]any{"id": "embedding", "pricing": map[string]string{"prompt": "0", "completion": "0"}, "architecture": map[string]any{"input_modalities": []string{"text"}, "output_modalities": []string{"embeddings"}}},
					}})
					return
				}
				var payload map[string]any
				json.NewDecoder(r.Body).Decode(&payload)
				if r.URL.Path == "/embeddings" {
					var data []any
					for index := range payload["input"].([]any) {
						data = append(data, map[string]any{"index": index, "embedding": []int{1, 0}})
					}
					json.NewEncoder(w).Encode(map[string]any{"data": data})
					return
				}
				messages := payload["messages"].([]any)
				encoded, _ := json.Marshal(messages[1])
				if strings.Contains(string(encoded), "Jane") || strings.Contains(string(encoded), "example.com") {
					t.Error("identity reached private inference")
				}
				name := payload["response_format"].(map[string]any)["json_schema"].(map[string]any)["name"]
				var response map[string]any
				if name == "resume_facts" {
					response = map[string]any{"schemaVersion": "3", "contact": map[string]any{"name": nil, "email": nil, "phone": nil, "location": nil, "evidence": []any{}}, "skills": []any{}, "employment": []any{}, "education": []any{}, "certifications": []any{}, "suggestions": []any{}, "warnings": []any{}}
				} else {
					assessed = true
					var input map[string]any
					json.Unmarshal([]byte(messages[1].(map[string]any)["content"].(string)), &input)
					id := input["requirements"].([]any)[0].(map[string]any)["id"]
					response = map[string]any{"assessments": []any{map[string]any{"requirementId": id, "outcome": "met", "confidence": 1, "reasoning": "Documented Python work.", "evidence": []any{map[string]any{"blockId": "work", "quote": "Built Python software services"}}}}}
					if stale {
						if _, err := pool.Exec(ctx, `UPDATE processing_job SET lease_token='replacement' WHERE id='private-job'`); err != nil {
							t.Error(err)
						}
					}
				}
				content, _ := json.Marshal(response)
				json.NewEncoder(w).Encode(map[string]any{"model": "chat", "choices": []any{map[string]any{"finish_reason": "stop", "message": map[string]any{"content": string(content)}}}})
			}))
			defer server.Close()
			root := t.TempDir()
			store, err := storage.New(root)
			if err != nil {
				t.Fatal(err)
			}
			worker := &Worker{pool: pool, store: store, models: openrouter.New("fixture", server.URL, time.Second)}
			worker.settings.OpenRouter.ExtractionModel = "chat"
			worker.settings.OpenRouter.AssessmentModel = "chat"
			worker.settings.OpenRouter.EmbeddingModel = "embedding"
			worker.settings.Billing.MinIndependentPoints = 5
			job := &ClaimedJob{ID: "private-job", PayloadReference: "private", LeaseToken: "current", AttemptCount: 1, MaximumAttempts: 3}
			if err := worker.enrichIndependentEvaluation(ctx, job); err != nil {
				t.Fatal(err)
			}
			if !assessed {
				t.Fatal("JD report skipped criterion assessment")
			}
			files := 0
			filepath.WalkDir(root, func(_ string, entry fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if !entry.IsDir() {
					files++
				}
				return nil
			})
			var state string
			pool.QueryRow(ctx, `SELECT state FROM point_reservation WHERE id='private-hold'`).Scan(&state)
			if stale && (files != 0 || state != "reserved") {
				t.Fatalf("stale report leaked %d files or settled hold=%s", files, state)
			}
			if !stale {
				var score, quality, coverage int
				if err := pool.QueryRow(ctx, `SELECT score,general_quality_score,evidence_coverage FROM independent_evaluation WHERE id='private'`).Scan(&score, &quality, &coverage); err != nil {
					t.Fatal(err)
				}
				if files != 1 || state != "settled" || score != 100 || quality == score || coverage != 100 {
					t.Fatalf("files=%d hold=%s role=%d quality=%d coverage=%d", files, state, score, quality, coverage)
				}
			}
		})
	}
}

func TestIndependentReportCleansArtifactOnDatabaseFailure(t *testing.T) {
	pool := workflowPool(t)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO processing_job(id,status,lease_token,lease_expires_at) VALUES ('private-job','processing','current',now()+interval '1 minute')`); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	store, err := storage.New(root)
	if err != nil {
		t.Fatal(err)
	}
	worker := &Worker{pool: pool, store: store}
	job := &ClaimedJob{ID: "private-job", PayloadReference: "private", LeaseToken: "current"}
	// No report table exists, so persistence must fail after rendering and storage.
	err = worker.storeIndependentReport(ctx, job, map[string]any{"contact": map[string]any{"name": "Jane Doe"}}, 42, nil, 0, []map[string]any{}, []map[string]any{}, []map[string]any{}, true, nil)
	if err == nil {
		t.Fatal("expected report persistence failure")
	}
	files := 0
	if err := filepath.WalkDir(root, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			files++
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if files != 0 {
		t.Fatalf("failed transaction left %d corrected resumes", files)
	}
}
