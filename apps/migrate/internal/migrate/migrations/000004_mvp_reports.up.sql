ALTER TABLE evaluation.independent_evaluation
	ADD COLUMN general_quality_score INTEGER CHECK (general_quality_score BETWEEN 0 AND 100),
	ADD COLUMN evidence_coverage INTEGER CHECK (evidence_coverage BETWEEN 0 AND 100),
	ADD COLUMN requirement_assessments JSONB NOT NULL DEFAULT '[]'::jsonb,
	ADD COLUMN requirements JSONB NOT NULL DEFAULT '[]'::jsonb;

CREATE TABLE platform.inference_call (
	id TEXT PRIMARY KEY,
	processing_job_id TEXT,
	model TEXT NOT NULL,
	provider TEXT,
	request_id TEXT,
	task TEXT NOT NULL,
	status TEXT NOT NULL,
	prompt_tokens INTEGER NOT NULL DEFAULT 0,
	completion_tokens INTEGER NOT NULL DEFAULT 0,
	cost_usd NUMERIC,
	latency_ms BIGINT NOT NULL,
	prompt_version TEXT,
	schema_version TEXT,
	fallback_reason TEXT,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX inference_call_job_idx ON platform.inference_call(processing_job_id);

ALTER TABLE billing.point_reservation ADD COLUMN expires_at TIMESTAMPTZ;
ALTER TABLE billing.weekly_free_use
	ADD COLUMN operation_key TEXT UNIQUE,
	ADD COLUMN expires_at TIMESTAMPTZ;
