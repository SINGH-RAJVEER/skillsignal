package httpapi

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/skillsignal/backend/auth"
	"github.com/skillsignal/backend/config"
	"github.com/skillsignal/backend/domainhttp"
)

func TestEmployerReportsFiltersExportAndPurge(t *testing.T) {
	pool, setup := testDatabase(t, "evaluation")
	ctx := context.Background()
	_, err := setup.Exec(ctx, `
	INSERT INTO identity."user"(id,name,email,account_type) VALUES('employer','Employer','e@test.example','employer');
	INSERT INTO identity.organization(id,name) VALUES('org','Organization');
	INSERT INTO hiring.job(id,organization_id,title) VALUES('job','org','Engineer');
	INSERT INTO hiring.job_version(id,job_id,version,source_media_type,confirmed_at) VALUES('v1','job',1,'text/plain',now()),('v2','job',2,'text/plain',now());
	INSERT INTO hiring.job_requirement(id,job_version_id,stable_id,kind,weight,normalized_text,source_evidence) VALUES('req','v1','go','required',2,'Go experience','[]');
	INSERT INTO documents.resume_document(id,organization_id,storage_key,checksum,media_type,size_bytes,original_name,retention_date) VALUES('doc','org','resume.txt','hash','text/plain',10,'resume.txt',now());
	INSERT INTO documents.resume_version(id,organization_id,resume_document_id,version) VALUES('resume','org','doc',1);
	INSERT INTO hiring.candidate_record(id,organization_id,full_name) VALUES('candidate','org','=Formula Name');
	INSERT INTO hiring.resume_submission(id,organization_id,job_id,candidate_record_id,resume_version_id) VALUES('submission','org','job','candidate','resume');
	INSERT INTO evaluation.batch_evaluation(id,organization_id,job_id,job_version_id,created_by_user_id,requirement_schema_version) VALUES('batch','org','job','v1','employer','1');
	INSERT INTO evaluation.batch_evaluation_submission(organization_id,job_id,batch_evaluation_id,resume_submission_id) VALUES('org','job','batch','submission');
	INSERT INTO evaluation.evaluation(id,batch_evaluation_id,resume_submission_id,job_version_id,resume_version_id,status,score,evidence_coverage,eligibility,quality_state) VALUES('evaluation','batch','submission','v1','resume','complete',100,100,'eligible','ready');
	INSERT INTO evaluation.requirement_assessment(id,evaluation_id,job_requirement_id,outcome,confidence,reasoning,evidence,semantic_evidence,lexical_evidence) VALUES('assessment','evaluation','req','met',1,'Documented Go','[{"blockId":"b1","quote":"Go"}]','{"model":"test","matches":[]}','{"matches":[]}');
	INSERT INTO evaluation.resume_block_embedding(resume_version_id,block_id,model,text_hash,vector) VALUES('resume','b1','model','hash','[1]');
	INSERT INTO evaluation.embedding_cache(model,text_hash,vector) VALUES('model','hash','[1]');
	INSERT INTO platform.processing_job(id,type,payload_reference,idempotency_key) VALUES('eval-job','evaluation_process','evaluation','eval-job');
	INSERT INTO platform.inference_call(id,processing_job_id,model,provider,task,status,latency_ms,prompt_version,schema_version) VALUES('model-call','eval-job','model-one','provider-one','assessment','complete',10,'prompt-1','schema-1');`)
	if err != nil {
		t.Fatal(err)
	}
	var historical bool
	role := "viewer"
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Internal-Secret") != "secret" {
			t.Error("missing internal authentication")
		}
		switch {
		case strings.HasPrefix(r.URL.Path, "/internal/identity/"):
			reply(w, 200, domainhttp.Access{UserID: "employer", AccountType: "employer", Memberships: []domainhttp.Membership{{OrganizationID: "org", Role: role}}})
		case r.URL.Path == "/internal/hiring/jobs/job":
			version := "v2"
			reqs := []requirement{}
			if r.URL.Query().Get("versionId") == "v1" {
				historical = true
				version = "v1"
				reqs = []requirement{{ID: "req", Kind: "required", Weight: 2, NormalizedText: "Go experience"}}
			}
			reply(w, 200, map[string]any{"id": "job", "organizationId": "org", "jobVersionId": version, "requirements": reqs})
		case r.URL.Path == "/internal/hiring/submissions/submission":
			reply(w, 200, map[string]any{"organizationId": "org", "jobId": "job", "resumeVersionId": "resume", "candidateName": "=Formula Name", "candidateEmail": "ada@example.test", "candidateLocation": "Remote"})
		case r.URL.Path == "/internal/documents/versions/resume":
			reply(w, 200, map[string]any{"facts": map[string]any{"skills": []map[string]string{{"canonicalName": "Go"}}}, "blocks": map[string]any{"Metadata": map[string]int{"PageCount": 1}, "warnings": []string{"Synthetic fixture"}}})
		default:
			t.Errorf("unexpected peer operation %s", r.URL)
			w.WriteHeader(500)
		}
	}))
	defer peer.Close()
	for _, key := range []string{"IDENTITY_URL", "HIRING_URL", "DOCUMENTS_URL"} {
		t.Setenv(key, peer.URL)
	}
	mux := http.NewServeMux()
	Register(mux, pool, config.Settings{JWTSecret: "secret", StorageRoot: t.TempDir()})
	token, _ := auth.IssueToken("employer", "secret", time.Hour, time.Now())
	call := func(method, path string, body io.Reader, internal bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, body)
		r.Header.Set("Authorization", "Bearer "+token)
		if internal {
			r.Header.Set("X-Internal-Secret", "secret")
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	w := call("GET", "/api/jobs/job/evaluations?minimum_score=90&minimum_coverage=80&status=complete&outcome=met&search=Formula&skill=go", nil, false)
	if w.Code != 200 {
		t.Fatalf("results: %d %s", w.Code, w.Body.String())
	}
	var rows []map[string]any
	if err = json.Unmarshal(w.Body.Bytes(), &rows); err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || !historical || rows[0]["candidateLocation"] != "Remote" {
		t.Fatalf("results %s historical %v", w.Body.String(), historical)
	}
	provenance := rows[0]["modelProvenance"].([]any)
	if len(provenance) != 1 || provenance[0].(map[string]any)["model"] != "model-one" {
		t.Fatalf("model provenance: %v", provenance)
	}
	if rows[0]["extractionMetadata"].(map[string]any)["pageCount"] != float64(1) {
		t.Fatalf("parser metadata: %v", rows[0]["extractionMetadata"])
	}
	assessment := rows[0]["assessments"].([]any)[0].(map[string]any)
	if assessment["contribution"] != float64(100) || assessment["weight"] != float64(2) {
		t.Fatalf("historical assessment %v", assessment)
	}
	w = call("GET", "/api/jobs/job/evaluations.csv", nil, false)
	if w.Code != 403 {
		t.Fatalf("viewer export: %d", w.Code)
	}
	role = "recruiter"
	w = call("GET", "/api/jobs/job/evaluations.csv?columns=candidate_name&columns=score&labels=Name&labels=Fit&minimum_score=90", nil, false)
	csvRows, err := csv.NewReader(w.Body).ReadAll()
	if err != nil || w.Code != 200 || len(csvRows) != 2 || csvRows[1][0] != "'=Formula Name" || csvRows[1][1] != "100" {
		t.Fatalf("CSV %d %v %v", w.Code, csvRows, err)
	}
	w = call("GET", "/api/jobs/job/evaluations?minimum_score=NaN", nil, false)
	if w.Code != 400 {
		t.Fatalf("NaN accepted: %d", w.Code)
	}
	w = call("GET", "/internal/evaluation/evaluations/evaluation", nil, true)
	if !strings.Contains(w.Body.String(), `"batchEvaluationId": "batch"`) && !strings.Contains(w.Body.String(), `"batchEvaluationId":"batch"`) {
		t.Fatalf("lookup: %s", w.Body.String())
	}
	w = call("GET", "/internal/evaluation/submissions/submission", nil, true)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"complete"`) {
		t.Fatalf("submission status: %s", w.Body.String())
	}
	for range 2 {
		w = call("POST", "/internal/evaluation/purge-documents", strings.NewReader(`{"documentIds":["doc"],"versionIds":["resume"]}`), true)
		if w.Code != 200 {
			t.Fatalf("purge: %d %s", w.Code, w.Body.String())
		}
	}
	w = call("POST", "/internal/evaluation/submissions", strings.NewReader(`{"organizationId":"org","jobId":"job","jobVersionId":"v1","userId":"employer","submissionId":"submission","resumeVersionId":"resume","batchId":"batch"}`), true)
	if w.Code != 409 {
		t.Fatalf("purged source replay accepted: %d %s", w.Code, w.Body.String())
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM evaluation)+(SELECT count(*) FROM requirement_assessment)+(SELECT count(*) FROM embedding_cache)+(SELECT count(*) FROM resume_block_embedding)+(SELECT count(*) FROM batch_evaluation_submission)+(SELECT count(*) FROM batch_evaluation)`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("derived data remains: %d %v", count, err)
	}
}

func TestIndependentUploadSurvivesLostActivationAndDeletesWithOperationKey(t *testing.T) {
	pool, setup := testDatabase(t, "evaluation")
	if _, err := setup.Exec(context.Background(), `INSERT INTO identity."user"(id,name,email) VALUES('candidate','Candidate','candidate@example.test'),('other','Other','other@example.test')`); err != nil {
		t.Fatal(err)
	}
	var releaseKey string
	week := time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)
	peer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/internal/identity/"):
			user := "candidate"
			if strings.Contains(r.URL.Path, "/other/") {
				user = "other"
			}
			reply(w, 200, domainhttp.Access{UserID: user, AccountType: "candidate"})
		case r.URL.Path == "/internal/billing/reservations":
			reply(w, 200, map[string]any{"reservationId": "", "freeWeekStart": week})
		case r.URL.Path == "/internal/billing/reservations/activate":
			w.WriteHeader(503)
		case r.URL.Path == "/internal/billing/reservations/release":
			var in map[string]any
			json.NewDecoder(r.Body).Decode(&in)
			releaseKey, _ = in["operationKey"].(string)
			reply(w, 200, map[string]bool{"ok": true})
		default:
			t.Errorf("unexpected peer %s", r.URL)
			w.WriteHeader(500)
		}
	}))
	defer peer.Close()
	t.Setenv("IDENTITY_URL", peer.URL)
	t.Setenv("BILLING_URL", peer.URL)
	mux := http.NewServeMux()
	Register(mux, pool, config.Settings{JWTSecret: "secret", StorageRoot: t.TempDir(), IndependentRetentionDays: 30})
	token, _ := auth.IssueToken("candidate", "secret", time.Hour, time.Now())
	call := func(method, path string, body io.Reader, contentType, user string, internal bool) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, body)
		jwt := token
		if user == "other" {
			jwt, _ = auth.IssueToken("other", "secret", time.Hour, time.Now())
		}
		r.Header.Set("Authorization", "Bearer "+jwt)
		r.Header.Set("Content-Type", contentType)
		if internal {
			r.Header.Set("X-Internal-Secret", "secret")
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	resume, _ := form.CreateFormFile("file", "resume.txt")
	resume.Write([]byte("Resume Go engineer"))
	jd, _ := form.CreateFormFile("job_description_file", "role.md")
	jd.Write([]byte("# Go engineer"))
	form.Close()
	// Browser multipart uses octet-stream, so filename-derived MIME is part of the upload contract.
	// A resume has a strict declared type; use a second writer with an explicit header.
	raw := strings.Replace(body.String(), "Content-Type: application/octet-stream", "Content-Type: text/plain", 1)
	w := call("POST", "/api/independent-evaluations", strings.NewReader(raw), form.FormDataContentType(), "candidate", false)
	if w.Code != 202 {
		t.Fatalf("create after lost activate: %d %s", w.Code, w.Body.String())
	}
	var created struct {
		ID  string `json:"id"`
		Job string `json:"processingJobId"`
	}
	json.Unmarshal(w.Body.Bytes(), &created)
	w = call("GET", "/internal/evaluation/holds/independent:"+created.ID, nil, "", "", true)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"state":"active"`) {
		t.Fatalf("durable proof %d %s", w.Code, w.Body.String())
	}
	w = call("GET", "/api/independent-evaluations/"+created.ID, nil, "", "other", false)
	if w.Code != 404 {
		t.Fatalf("other user read: %d", w.Code)
	}
	w = call("DELETE", "/api/independent-evaluations/"+created.ID, nil, "", "candidate", false)
	if w.Code != 204 || releaseKey != "independent:"+created.ID {
		t.Fatalf("release %d %s key=%s", w.Code, w.Body.String(), releaseKey)
	}
	var count int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM processing_job WHERE payload_reference=$1`, created.ID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("queue remains %d %v", count, err)
	}
}
