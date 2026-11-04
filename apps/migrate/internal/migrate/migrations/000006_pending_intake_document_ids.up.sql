UPDATE hiring.submission_intake
SET document_id = COALESCE(document_id, 'upload-' || encode(sha256(convert_to('submission-document:' || submission_id, 'UTF8')), 'hex')),
	resume_version_id = COALESCE(resume_version_id, 'version-' || encode(sha256(convert_to('submission-document:' || submission_id, 'UTF8')), 'hex'))
WHERE document_id IS NULL OR resume_version_id IS NULL;
