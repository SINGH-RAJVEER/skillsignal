DROP TABLE IF EXISTS service_inbox;
DROP INDEX IF EXISTS ix_service_outbox_unpublished;
DROP TABLE IF EXISTS service_outbox;
UPDATE processing_job SET type = 'resume_processing'
	WHERE type = 'resume_parse';
UPDATE processing_job SET type = 'job_description_processing'
	WHERE type = 'job_description_parse';
UPDATE processing_job SET type = 'independent_evaluation_processing'
	WHERE type = 'independent_evaluation_parse';
UPDATE processing_job SET type = 'evaluation_processing'
	WHERE type = 'evaluation_process';
ALTER TABLE independent_evaluation DROP COLUMN IF EXISTS extraction_blocks;
