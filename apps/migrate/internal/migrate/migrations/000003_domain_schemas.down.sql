ALTER TABLE identity."user" SET SCHEMA public;
ALTER TABLE identity.account SET SCHEMA public;
ALTER TABLE identity.organization SET SCHEMA public;
ALTER TABLE identity.organization_member SET SCHEMA public;
ALTER TABLE identity.organization_email_domain SET SCHEMA public;
ALTER TABLE identity.organization_allowed_email SET SCHEMA public;

ALTER TABLE hiring.candidate_record SET SCHEMA public;
ALTER TABLE hiring.job SET SCHEMA public;
ALTER TABLE hiring.job_version SET SCHEMA public;
ALTER TABLE hiring.job_requirement SET SCHEMA public;
ALTER TABLE hiring.resume_submission SET SCHEMA public;
ALTER TABLE hiring.review_decision SET SCHEMA public;
ALTER TABLE hiring.invitation SET SCHEMA public;

ALTER TABLE documents.resume_document SET SCHEMA public;
ALTER TABLE documents.resume_version SET SCHEMA public;

ALTER TABLE evaluation.resume_block_embedding SET SCHEMA public;
ALTER TABLE evaluation.embedding_cache SET SCHEMA public;
ALTER TABLE evaluation.batch_evaluation SET SCHEMA public;
ALTER TABLE evaluation.batch_evaluation_submission SET SCHEMA public;
ALTER TABLE evaluation.evaluation SET SCHEMA public;
ALTER TABLE evaluation.requirement_assessment SET SCHEMA public;
ALTER TABLE evaluation.independent_evaluation SET SCHEMA public;

ALTER TABLE billing.point_account SET SCHEMA public;
ALTER TABLE billing.point_ledger_entry SET SCHEMA public;
ALTER TABLE billing.point_reservation SET SCHEMA public;
ALTER TABLE billing.organization_entitlement SET SCHEMA public;
ALTER TABLE billing.weekly_free_use SET SCHEMA public;
ALTER TABLE billing.razorpay_order SET SCHEMA public;
ALTER TABLE billing.razorpay_payment SET SCHEMA public;
ALTER TABLE billing.razorpay_webhook_event SET SCHEMA public;

ALTER TABLE platform.processing_job SET SCHEMA public;
ALTER TABLE platform.service_outbox SET SCHEMA public;
ALTER TABLE platform.service_inbox SET SCHEMA public;

DROP SCHEMA platform;
DROP SCHEMA billing;
DROP SCHEMA evaluation;
DROP SCHEMA documents;
DROP SCHEMA hiring;
DROP SCHEMA identity;
