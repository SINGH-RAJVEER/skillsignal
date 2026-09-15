ALTER TABLE independent_evaluation
	ADD COLUMN IF NOT EXISTS extraction_blocks JSONB;

UPDATE processing_job SET type = 'resume_parse'
	WHERE type = 'resume_processing';
UPDATE processing_job SET type = 'job_description_parse'
	WHERE type = 'job_description_processing';
UPDATE processing_job SET type = 'independent_evaluation_parse'
	WHERE type = 'independent_evaluation_processing';
UPDATE processing_job SET type = 'evaluation_process'
	WHERE type = 'evaluation_processing';

CREATE TABLE service_outbox (
	id TEXT PRIMARY KEY,
	owner_service TEXT NOT NULL,
	event_type TEXT NOT NULL,
	event_version INTEGER NOT NULL DEFAULT 1,
	subject TEXT NOT NULL,
	correlation_id TEXT NOT NULL,
	payload JSONB NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	published_at TIMESTAMPTZ,
	attempt_count INTEGER NOT NULL DEFAULT 0,
	CONSTRAINT ck_service_outbox_version CHECK (event_version > 0),
	CONSTRAINT ck_service_outbox_attempt_count CHECK (attempt_count >= 0)
);

CREATE INDEX ix_service_outbox_unpublished
	ON service_outbox (created_at)
	WHERE published_at IS NULL;

CREATE TABLE service_inbox (
	consumer_service TEXT NOT NULL,
	event_id TEXT NOT NULL,
	processed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	PRIMARY KEY (consumer_service, event_id)
);
