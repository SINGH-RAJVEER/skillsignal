DROP TABLE platform.inference_call;
ALTER TABLE evaluation.independent_evaluation
	DROP COLUMN requirements,
	DROP COLUMN requirement_assessments,
	DROP COLUMN evidence_coverage,
	DROP COLUMN general_quality_score;
ALTER TABLE billing.weekly_free_use DROP COLUMN expires_at, DROP COLUMN operation_key;
ALTER TABLE billing.point_reservation DROP COLUMN expires_at;
