CREATE SCHEMA IF NOT EXISTS identity;
CREATE SCHEMA IF NOT EXISTS hiring;
CREATE SCHEMA IF NOT EXISTS documents;
CREATE SCHEMA IF NOT EXISTS evaluation;
CREATE SCHEMA IF NOT EXISTS billing;
CREATE SCHEMA IF NOT EXISTS platform;

ALTER TABLE "user" SET SCHEMA identity;
ALTER TABLE account SET SCHEMA identity;
ALTER TABLE organization SET SCHEMA identity;
ALTER TABLE organization_member SET SCHEMA identity;
ALTER TABLE organization_email_domain SET SCHEMA identity;
ALTER TABLE organization_allowed_email SET SCHEMA identity;

ALTER TABLE candidate_record SET SCHEMA hiring;
ALTER TABLE job SET SCHEMA hiring;
ALTER TABLE job_version SET SCHEMA hiring;
ALTER TABLE job_requirement SET SCHEMA hiring;
ALTER TABLE resume_submission SET SCHEMA hiring;
ALTER TABLE review_decision SET SCHEMA hiring;
ALTER TABLE invitation SET SCHEMA hiring;

ALTER TABLE resume_document SET SCHEMA documents;
ALTER TABLE resume_version SET SCHEMA documents;

ALTER TABLE resume_block_embedding SET SCHEMA evaluation;
ALTER TABLE embedding_cache SET SCHEMA evaluation;
ALTER TABLE batch_evaluation SET SCHEMA evaluation;
ALTER TABLE batch_evaluation_submission SET SCHEMA evaluation;
ALTER TABLE evaluation SET SCHEMA evaluation;
ALTER TABLE requirement_assessment SET SCHEMA evaluation;
ALTER TABLE independent_evaluation SET SCHEMA evaluation;

ALTER TABLE point_account SET SCHEMA billing;
ALTER TABLE point_ledger_entry SET SCHEMA billing;
ALTER TABLE point_reservation SET SCHEMA billing;
ALTER TABLE organization_entitlement SET SCHEMA billing;
ALTER TABLE weekly_free_use SET SCHEMA billing;
ALTER TABLE razorpay_order SET SCHEMA billing;
ALTER TABLE razorpay_payment SET SCHEMA billing;
ALTER TABLE razorpay_webhook_event SET SCHEMA billing;

ALTER TABLE processing_job SET SCHEMA platform;
ALTER TABLE service_outbox SET SCHEMA platform;
ALTER TABLE service_inbox SET SCHEMA platform;
