-- Baseline schema for the Go backend port.
--
-- Captures the Alembic end-state on main as a single migration: fresh
-- deployments start here, and the Python migration history stays in git
-- for reference. Table and constraint names match the Python models so
-- data and tooling carry over unchanged.

CREATE TABLE "user" (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	email TEXT NOT NULL,
	account_type TEXT NOT NULL DEFAULT 'candidate',
	email_verified BOOLEAN NOT NULL DEFAULT FALSE,
	is_demo BOOLEAN NOT NULL DEFAULT FALSE,
	image TEXT,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	CONSTRAINT ck_user_account_type CHECK (account_type IN ('employer', 'candidate')),
	CONSTRAINT uq_user_email UNIQUE (email)
);

CREATE TABLE account (
	id TEXT PRIMARY KEY,
	account_id TEXT NOT NULL,
	provider_id TEXT NOT NULL,
	user_id TEXT NOT NULL REFERENCES "user"(id) ON DELETE CASCADE,
	password TEXT,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	CONSTRAINT uq_account_provider_account UNIQUE (provider_id, account_id)
);

CREATE TABLE organization (
	id TEXT PRIMARY KEY,
	name TEXT NOT NULL,
	default_member_role TEXT NOT NULL DEFAULT 'viewer',
	retention_days INTEGER NOT NULL DEFAULT 90,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	CONSTRAINT ck_organization_default_member_role CHECK (default_member_role IN ('recruiter', 'viewer'))
);

CREATE TABLE organization_member (
	id TEXT PRIMARY KEY,
	organization_id TEXT NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
	user_id TEXT NOT NULL REFERENCES "user"(id) ON DELETE CASCADE,
	role TEXT NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	CONSTRAINT ck_member_role CHECK (role IN ('owner', 'recruiter', 'viewer')),
	CONSTRAINT uq_organization_member UNIQUE (organization_id, user_id),
	CONSTRAINT uq_organization_member_user UNIQUE (user_id)
);

CREATE TABLE organization_email_domain (
	id TEXT PRIMARY KEY,
	organization_id TEXT NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
	domain TEXT NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	CONSTRAINT uq_organization_email_domain UNIQUE (domain)
);

CREATE TABLE organization_allowed_email (
	id TEXT PRIMARY KEY,
	organization_id TEXT NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
	email TEXT NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	CONSTRAINT uq_organization_allowed_email UNIQUE (organization_id, email)
);

CREATE TABLE candidate_record (
	id TEXT PRIMARY KEY,
	organization_id TEXT NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
	user_id TEXT REFERENCES "user"(id) ON DELETE SET NULL,
	full_name TEXT,
	email TEXT,
	phone TEXT,
	location TEXT,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	CONSTRAINT uq_candidate_record_organization UNIQUE (organization_id, id)
);

CREATE TABLE resume_document (
	id TEXT PRIMARY KEY,
	owner_user_id TEXT REFERENCES "user"(id) ON DELETE CASCADE,
	organization_id TEXT REFERENCES organization(id) ON DELETE CASCADE,
	candidate_record_id TEXT REFERENCES candidate_record(id) ON DELETE SET NULL,
	storage_key TEXT NOT NULL UNIQUE,
	checksum TEXT NOT NULL,
	media_type TEXT NOT NULL,
	size_bytes INTEGER NOT NULL,
	original_name TEXT NOT NULL,
	retention_date TIMESTAMPTZ NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	CONSTRAINT ck_resume_document_owner CHECK ((owner_user_id IS NOT NULL) <> (organization_id IS NOT NULL)),
	CONSTRAINT uq_resume_document_organization UNIQUE (organization_id, id)
);

CREATE TABLE resume_version (
	id TEXT PRIMARY KEY,
	organization_id TEXT,
	resume_document_id TEXT NOT NULL,
	version INTEGER NOT NULL,
	extraction_blocks JSONB,
	structured_facts JSONB,
	normalized_facts JSONB,
	quality_state TEXT NOT NULL DEFAULT 'pending',
	parser_version TEXT,
	parser_configuration_version TEXT,
	schema_version TEXT,
	extraction_prompt_version TEXT,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	CONSTRAINT ck_resume_version_quality_state CHECK (quality_state IN ('pending', 'ready', 'review_required', 'failed')),
	CONSTRAINT fk_resume_version_document FOREIGN KEY (organization_id, resume_document_id)
		REFERENCES resume_document(organization_id, id) ON DELETE CASCADE,
	CONSTRAINT uq_resume_version UNIQUE (resume_document_id, version),
	CONSTRAINT uq_resume_version_organization UNIQUE (organization_id, id)
);

CREATE TABLE resume_block_embedding (
	resume_version_id TEXT NOT NULL REFERENCES resume_version(id) ON DELETE CASCADE,
	block_id TEXT NOT NULL,
	model TEXT NOT NULL,
	text_hash TEXT NOT NULL,
	vector JSONB NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	PRIMARY KEY (resume_version_id, block_id, model)
);

CREATE TABLE embedding_cache (
	model TEXT NOT NULL,
	text_hash TEXT NOT NULL,
	vector JSONB NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	PRIMARY KEY (model, text_hash)
);

CREATE TABLE job (
	id TEXT PRIMARY KEY,
	organization_id TEXT NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
	title TEXT NOT NULL,
	application_opens_at TIMESTAMPTZ,
	application_closes_at TIMESTAMPTZ,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	CONSTRAINT uq_job_organization UNIQUE (organization_id, id)
);

CREATE TABLE job_version (
	id TEXT PRIMARY KEY,
	job_id TEXT NOT NULL REFERENCES job(id) ON DELETE CASCADE,
	version INTEGER NOT NULL,
	source_text TEXT,
	normalized_text TEXT,
	source_media_type TEXT NOT NULL,
	source_storage_key TEXT,
	draft_requirements JSONB,
	schema_version TEXT,
	prompt_version TEXT NOT NULL DEFAULT '1',
	compiler_version TEXT NOT NULL DEFAULT 'compiler-2',
	confirmed_at TIMESTAMPTZ,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	CONSTRAINT uq_job_version UNIQUE (job_id, version),
	CONSTRAINT uq_job_version_job UNIQUE (job_id, id)
);

CREATE TABLE job_requirement (
	id TEXT PRIMARY KEY,
	job_version_id TEXT NOT NULL REFERENCES job_version(id) ON DELETE CASCADE,
	stable_id TEXT NOT NULL,
	kind TEXT NOT NULL,
	weight INTEGER NOT NULL,
	normalized_text TEXT NOT NULL,
	category TEXT NOT NULL DEFAULT 'other',
	source_modality TEXT NOT NULL DEFAULT 'unclear',
	assessability TEXT NOT NULL DEFAULT 'unclear',
	predicate JSONB NOT NULL DEFAULT '{}'::jsonb,
	aliases JSONB NOT NULL DEFAULT '[]'::jsonb,
	source_evidence JSONB NOT NULL,
	confirmed_at TIMESTAMPTZ,
	CONSTRAINT ck_requirement_kind CHECK (kind IN ('required', 'preferred', 'ignored', 'hard_gate')),
	CONSTRAINT ck_requirement_weight CHECK (weight > 0),
	CONSTRAINT ck_requirement_assessability CHECK (assessability IN ('resume_evidence', 'candidate_attestation', 'recruiter_review', 'prohibited', 'unclear')),
	CONSTRAINT uq_job_requirement_stable_id UNIQUE (job_version_id, stable_id)
);

CREATE TABLE resume_submission (
	id TEXT PRIMARY KEY,
	organization_id TEXT NOT NULL REFERENCES organization(id) ON DELETE CASCADE,
	job_id TEXT NOT NULL,
	candidate_record_id TEXT NOT NULL,
	resume_version_id TEXT NOT NULL,
	submitting_user_id TEXT REFERENCES "user"(id) ON DELETE SET NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	CONSTRAINT fk_submission_job FOREIGN KEY (organization_id, job_id)
		REFERENCES job(organization_id, id) ON DELETE CASCADE,
	CONSTRAINT fk_submission_candidate FOREIGN KEY (organization_id, candidate_record_id)
		REFERENCES candidate_record(organization_id, id) ON DELETE CASCADE,
	CONSTRAINT fk_submission_version FOREIGN KEY (organization_id, resume_version_id)
		REFERENCES resume_version(organization_id, id) ON DELETE RESTRICT,
	CONSTRAINT uq_submission UNIQUE (job_id, candidate_record_id, resume_version_id),
	CONSTRAINT uq_submission_organization_job UNIQUE (organization_id, job_id, id)
);

CREATE TABLE processing_job (
	id TEXT PRIMARY KEY,
	type TEXT NOT NULL,
	status TEXT NOT NULL DEFAULT 'ready',
	payload_reference TEXT NOT NULL,
	idempotency_key TEXT NOT NULL,
	attempt_count INTEGER NOT NULL DEFAULT 0,
	maximum_attempts INTEGER NOT NULL DEFAULT 3,
	available_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	lease_token TEXT,
	lease_expires_at TIMESTAMPTZ,
	safe_error TEXT,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	CONSTRAINT ck_processing_job_attempt_count CHECK (attempt_count >= 0),
	CONSTRAINT ck_processing_job_maximum_attempts CHECK (maximum_attempts > 0),
	CONSTRAINT uq_processing_job_idempotency UNIQUE (type, idempotency_key)
);

CREATE TABLE point_account (
	id TEXT PRIMARY KEY,
	owner_user_id TEXT REFERENCES "user"(id) ON DELETE CASCADE,
	organization_id TEXT REFERENCES organization(id) ON DELETE CASCADE,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	CONSTRAINT ck_point_account_owner CHECK ((owner_user_id IS NOT NULL) <> (organization_id IS NOT NULL)),
	CONSTRAINT uq_point_account_user UNIQUE (owner_user_id),
	CONSTRAINT uq_point_account_organization UNIQUE (organization_id)
);

CREATE TABLE point_ledger_entry (
	id TEXT PRIMARY KEY,
	account_id TEXT NOT NULL REFERENCES point_account(id) ON DELETE CASCADE,
	amount INTEGER NOT NULL,
	reason TEXT NOT NULL,
	idempotency_key TEXT NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	CONSTRAINT ck_point_ledger_entry_nonzero_amount CHECK (amount <> 0),
	CONSTRAINT uq_point_ledger_entry_idempotency UNIQUE (account_id, idempotency_key)
);

CREATE TABLE point_reservation (
	id TEXT PRIMARY KEY,
	account_id TEXT NOT NULL REFERENCES point_account(id) ON DELETE CASCADE,
	amount INTEGER NOT NULL,
	state TEXT NOT NULL DEFAULT 'reserved',
	purpose TEXT NOT NULL,
	idempotency_key TEXT NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	CONSTRAINT ck_point_reservation_positive_amount CHECK (amount > 0),
	CONSTRAINT ck_point_reservation_state CHECK (state IN ('reserved', 'settled', 'released')),
	CONSTRAINT uq_point_reservation_idempotency UNIQUE (account_id, idempotency_key)
);

CREATE TABLE batch_evaluation (
	id TEXT PRIMARY KEY,
	organization_id TEXT NOT NULL,
	job_id TEXT NOT NULL,
	job_version_id TEXT NOT NULL,
	created_by_user_id TEXT NOT NULL REFERENCES "user"(id) ON DELETE RESTRICT,
	requirement_schema_version TEXT NOT NULL,
	scoring_policy_version TEXT NOT NULL DEFAULT 'criterion-weighted-1',
	model_configuration JSONB NOT NULL DEFAULT '{}'::jsonb,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	CONSTRAINT fk_batch_job FOREIGN KEY (organization_id, job_id)
		REFERENCES job(organization_id, id) ON DELETE CASCADE,
	CONSTRAINT fk_batch_job_version FOREIGN KEY (job_id, job_version_id)
		REFERENCES job_version(job_id, id) ON DELETE RESTRICT,
	CONSTRAINT uq_batch_evaluation_organization_job UNIQUE (organization_id, job_id, id),
	CONSTRAINT uq_batch_evaluation_organization UNIQUE (organization_id, id)
);

CREATE TABLE batch_evaluation_submission (
	organization_id TEXT NOT NULL,
	job_id TEXT NOT NULL,
	batch_evaluation_id TEXT NOT NULL,
	resume_submission_id TEXT NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	PRIMARY KEY (batch_evaluation_id, resume_submission_id),
	CONSTRAINT fk_batch_link_batch FOREIGN KEY (organization_id, job_id, batch_evaluation_id)
		REFERENCES batch_evaluation(organization_id, job_id, id) ON DELETE CASCADE,
	CONSTRAINT fk_batch_link_submission FOREIGN KEY (organization_id, job_id, resume_submission_id)
		REFERENCES resume_submission(organization_id, job_id, id) ON DELETE RESTRICT
);

CREATE TABLE evaluation (
	id TEXT PRIMARY KEY,
	batch_evaluation_id TEXT REFERENCES batch_evaluation(id) ON DELETE CASCADE,
	resume_submission_id TEXT NOT NULL REFERENCES resume_submission(id) ON DELETE CASCADE,
	job_version_id TEXT NOT NULL REFERENCES job_version(id) ON DELETE RESTRICT,
	resume_version_id TEXT NOT NULL REFERENCES resume_version(id) ON DELETE RESTRICT,
	status TEXT NOT NULL DEFAULT 'pending',
	score INTEGER,
	evidence_coverage INTEGER,
	eligibility TEXT NOT NULL DEFAULT 'pending',
	quality_state TEXT NOT NULL DEFAULT 'pending',
	scoring_policy_version TEXT NOT NULL DEFAULT 'criterion-weighted-1',
	assessment_schema_version TEXT,
	assessment_prompt_version TEXT,
	assessment_degradation TEXT,
	rank INTEGER,
	point_reservation_id TEXT REFERENCES point_reservation(id) ON DELETE SET NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	completed_at TIMESTAMPTZ,
	CONSTRAINT ck_evaluation_status CHECK (status IN ('pending', 'processing', 'complete', 'failed')),
	CONSTRAINT ck_evaluation_eligibility CHECK (eligibility IN ('pending', 'eligible', 'needs_review', 'not_eligible')),
	CONSTRAINT ck_evaluation_score CHECK (score IS NULL OR score BETWEEN 0 AND 100),
	CONSTRAINT ck_evaluation_evidence_coverage CHECK (evidence_coverage IS NULL OR evidence_coverage BETWEEN 0 AND 100),
	CONSTRAINT ck_evaluation_quality_state CHECK (quality_state IN ('pending', 'ready', 'review_required', 'failed')),
	CONSTRAINT uq_evaluation_batch_submission UNIQUE (batch_evaluation_id, resume_submission_id),
	CONSTRAINT uq_evaluation_batch UNIQUE (batch_evaluation_id, id),
	CONSTRAINT fk_evaluation_batch_link FOREIGN KEY (batch_evaluation_id, resume_submission_id)
		REFERENCES batch_evaluation_submission(batch_evaluation_id, resume_submission_id) ON DELETE CASCADE
);

CREATE TABLE requirement_assessment (
	id TEXT PRIMARY KEY,
	evaluation_id TEXT NOT NULL REFERENCES evaluation(id) ON DELETE CASCADE,
	job_requirement_id TEXT NOT NULL REFERENCES job_requirement(id) ON DELETE RESTRICT,
	outcome TEXT NOT NULL,
	confidence DOUBLE PRECISION NOT NULL,
	reasoning TEXT NOT NULL,
	evidence JSONB NOT NULL,
	deterministic_evidence JSONB,
	semantic_evidence JSONB,
	lexical_evidence JSONB,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	CONSTRAINT ck_assessment_outcome CHECK (outcome IN ('met', 'partial', 'not_met', 'unknown')),
	CONSTRAINT uq_assessment_evaluation_requirement UNIQUE (evaluation_id, job_requirement_id)
);

CREATE TABLE review_decision (
	id TEXT PRIMARY KEY,
	organization_id TEXT NOT NULL,
	batch_evaluation_id TEXT NOT NULL,
	evaluation_id TEXT NOT NULL,
	reviewer_user_id TEXT NOT NULL REFERENCES "user"(id) ON DELETE RESTRICT,
	eligibility TEXT NOT NULL,
	reason TEXT NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	CONSTRAINT ck_review_decision_eligibility CHECK (eligibility IN ('eligible', 'needs_review', 'not_eligible')),
	CONSTRAINT fk_review_batch FOREIGN KEY (organization_id, batch_evaluation_id)
		REFERENCES batch_evaluation(organization_id, id) ON DELETE CASCADE,
	CONSTRAINT fk_review_evaluation FOREIGN KEY (batch_evaluation_id, evaluation_id)
		REFERENCES evaluation(batch_evaluation_id, id) ON DELETE CASCADE
);

CREATE TABLE invitation (
	id TEXT PRIMARY KEY,
	job_id TEXT NOT NULL REFERENCES job(id) ON DELETE CASCADE,
	creator_user_id TEXT NOT NULL REFERENCES "user"(id) ON DELETE RESTRICT,
	token_hash TEXT NOT NULL,
	passcode_hash TEXT,
	expires_at TIMESTAMPTZ NOT NULL,
	redeeming_user_id TEXT REFERENCES "user"(id) ON DELETE SET NULL,
	resume_submission_id TEXT REFERENCES resume_submission(id) ON DELETE SET NULL,
	revoked_at TIMESTAMPTZ,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	CONSTRAINT uq_invitation_token_hash UNIQUE (token_hash),
	CONSTRAINT uq_invitation_passcode_hash UNIQUE (passcode_hash)
);

CREATE TABLE independent_evaluation (
	id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL REFERENCES "user"(id) ON DELETE CASCADE,
	storage_key TEXT NOT NULL,
	original_name TEXT NOT NULL,
	media_type TEXT NOT NULL,
	job_description TEXT,
	job_description_key TEXT,
	job_description_media_type TEXT,
	status TEXT NOT NULL DEFAULT 'queued',
	score INTEGER,
	suggestions JSONB,
	improved_resume_key TEXT,
	improved_resume_unlocked_at TIMESTAMPTZ,
	normalized_facts JSONB,
	parser_version TEXT,
	parser_configuration_version TEXT NOT NULL DEFAULT 'resume-parser-2',
	schema_version TEXT,
	extraction_prompt_version TEXT,
	scoring_policy_version TEXT NOT NULL DEFAULT 'criterion-weighted-1',
	safe_error TEXT,
	point_reservation_id TEXT REFERENCES point_reservation(id) ON DELETE SET NULL,
	free_week_start TIMESTAMPTZ,
	retention_date TIMESTAMPTZ NOT NULL DEFAULT now() + INTERVAL '30 days',
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	completed_at TIMESTAMPTZ,
	CONSTRAINT ck_independent_evaluation_status CHECK (status IN ('queued', 'processing', 'complete', 'failed'))
);

CREATE TABLE organization_entitlement (
	id TEXT PRIMARY KEY,
	organization_id TEXT NOT NULL REFERENCES organization(id) ON DELETE CASCADE UNIQUE,
	provisioned_by TEXT,
	note TEXT,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE weekly_free_use (
	id TEXT PRIMARY KEY,
	user_id TEXT NOT NULL REFERENCES "user"(id) ON DELETE CASCADE,
	week_start TIMESTAMPTZ NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	CONSTRAINT uq_weekly_free_use UNIQUE (user_id, week_start)
);

CREATE TABLE razorpay_order (
	id TEXT PRIMARY KEY,
	razorpay_order_id TEXT NOT NULL UNIQUE,
	account_id TEXT NOT NULL REFERENCES point_account(id) ON DELETE RESTRICT,
	purchaser_user_id TEXT NOT NULL REFERENCES "user"(id) ON DELETE RESTRICT,
	pack_id TEXT NOT NULL,
	points INTEGER NOT NULL,
	amount_inr INTEGER NOT NULL,
	currency TEXT NOT NULL DEFAULT 'INR',
	status TEXT NOT NULL DEFAULT 'created',
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	CONSTRAINT ck_razorpay_order_status CHECK (status IN ('created', 'paid', 'refunded', 'failed'))
);

CREATE TABLE razorpay_payment (
	id TEXT PRIMARY KEY,
	razorpay_payment_id TEXT NOT NULL UNIQUE,
	order_row_id TEXT NOT NULL REFERENCES razorpay_order(id) ON DELETE RESTRICT,
	status TEXT NOT NULL,
	method TEXT,
	amount_inr INTEGER NOT NULL,
	refunded_inr INTEGER NOT NULL DEFAULT 0,
	points_granted BOOLEAN NOT NULL DEFAULT FALSE,
	signature_verified BOOLEAN NOT NULL DEFAULT FALSE,
	source TEXT NOT NULL,
	created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE razorpay_webhook_event (
	id TEXT PRIMARY KEY,
	event_type TEXT NOT NULL,
	payload JSONB NOT NULL,
	received_at TIMESTAMPTZ NOT NULL DEFAULT now(),
	processed_at TIMESTAMPTZ
);
