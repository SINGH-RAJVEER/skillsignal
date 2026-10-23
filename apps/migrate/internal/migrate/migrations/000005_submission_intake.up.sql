CREATE TABLE hiring.submission_intake (
	operation_key TEXT PRIMARY KEY,
	organization_id TEXT NOT NULL REFERENCES identity.organization(id) ON DELETE CASCADE,
	job_id TEXT NOT NULL REFERENCES hiring.job(id) ON DELETE CASCADE,
	job_version_id TEXT NOT NULL REFERENCES hiring.job_version(id) ON DELETE CASCADE,
	request_hash TEXT NOT NULL,
	submission_id TEXT NOT NULL UNIQUE,
	candidate_record_id TEXT NOT NULL,
	batch_id TEXT NOT NULL,
	document_id TEXT,
	resume_version_id TEXT,
	reservation_id TEXT,
	enterprise BOOLEAN NOT NULL DEFAULT false,
	state TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','accepted','cancelled')),
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
