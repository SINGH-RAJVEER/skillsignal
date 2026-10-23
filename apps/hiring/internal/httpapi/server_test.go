package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/skillsignal/backend/auth"
	"github.com/skillsignal/backend/config"
	"github.com/skillsignal/backend/ids"
)

func TestHiringContractsAndSingleUseInvitation(t *testing.T) {
	address := os.Getenv("TEST_DATABASE_URL")
	if address == "" {
		t.Skip("TEST_DATABASE_URL must point to a disposable migrated database")
	}
	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(address)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = "hiring,platform,public"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	prefix := ids.New()
	org, employer, candidate := prefix+"org", prefix+"employer", prefix+"candidate"
	for _, u := range []struct{ id, kind string }{{employer, "employer"}, {candidate, "candidate"}} {
		if _, err := pool.Exec(ctx, `INSERT INTO identity."user"(id,name,email,account_type,email_verified) VALUES($1,'Test',$2,$3,false)`, u.id, u.id+"@example.com", u.kind); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `INSERT INTO identity.organization(id,name) VALUES($1,'Test')`, org); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM platform.service_outbox WHERE payload->>'organizationId'=$1`, org)
		_, _ = pool.Exec(ctx, `DELETE FROM identity.organization WHERE id=$1`, org)
		_, _ = pool.Exec(ctx, `DELETE FROM identity."user" WHERE id=ANY($1)`, []string{employer, candidate})
		_, _ = pool.Exec(ctx, `DELETE FROM platform.processing_job WHERE payload_reference LIKE $1`, prefix+"%")
	})
	secret := "hiring-test-secret-at-least-thirty-two-characters"
	identity := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/retention") {
			json.NewEncoder(w).Encode(map[string]int{"retentionDays": 90})
			return
		}
		id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/internal/identity/users/"), "/access")
		kind := "candidate"
		memberships := []map[string]any{}
		if id == employer {
			kind = "employer"
			memberships = append(memberships, map[string]any{"organizationId": org, "role": "owner", "retentionDays": 90})
		}
		json.NewEncoder(w).Encode(map[string]any{"userId": id, "accountType": kind, "memberships": memberships})
	}))
	defer identity.Close()
	t.Setenv("IDENTITY_URL", identity.URL)
	var documentMu sync.Mutex
	documentResults := make(map[string]map[string]string)
	var documentCreates atomic.Int32
	documents := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			w.WriteHeader(204)
			return
		}
		var input struct {
			OrganizationID string `json:"organizationId"`
			OriginalName   string `json:"originalName"`
			MediaType      string `json:"mediaType"`
			Content        []byte `json:"content"`
			IdempotencyKey string `json:"idempotencyKey"`
		}
		if json.NewDecoder(r.Body).Decode(&input) != nil {
			w.WriteHeader(400)
			return
		}
		documentMu.Lock()
		defer documentMu.Unlock()
		if previous, ok := documentResults[input.IdempotencyKey]; ok {
			json.NewEncoder(w).Encode(previous)
			return
		}
		doc, version := ids.New(), ids.New()
		_, err := pool.Exec(ctx, `INSERT INTO documents.resume_document(id,organization_id,storage_key,checksum,media_type,size_bytes,original_name,retention_date) VALUES($1,$2,$1,'test',$3,$4,$5,now()+interval '90 days')`, doc, input.OrganizationID, input.MediaType, len(input.Content), input.OriginalName)
		if err == nil {
			_, err = pool.Exec(ctx, `INSERT INTO documents.resume_version(id,organization_id,resume_document_id,version) VALUES($1,$2,$3,1)`, version, input.OrganizationID, doc)
		}
		if err != nil {
			t.Error(err)
			w.WriteHeader(500)
			return
		}
		result := map[string]string{"documentId": doc, "versionId": version}
		documentResults[input.IdempotencyKey] = result
		documentCreates.Add(1)
		json.NewEncoder(w).Encode(result)
	}))
	defer documents.Close()
	t.Setenv("DOCUMENTS_URL", documents.URL)
	var enterprise, failReserve atomic.Bool
	var activations, releases atomic.Int32
	billing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/activate") {
			activations.Add(1)
			json.NewEncoder(w).Encode(map[string]bool{"ok": true})
			return
		}
		if strings.HasSuffix(r.URL.Path, "/release") {
			releases.Add(1)
			json.NewEncoder(w).Encode(map[string]bool{"ok": true})
			return
		}
		if failReserve.Swap(false) {
			w.WriteHeader(503)
			json.NewEncoder(w).Encode(map[string]string{"code": "SERVICE_UNAVAILABLE", "message": "Temporary billing outage"})
			return
		}
		if enterprise.Load() {
			json.NewEncoder(w).Encode(map[string]bool{"enterprise": true})
			return
		}
		var input struct {
			Key string `json:"idempotencyKey"`
		}
		_ = json.NewDecoder(r.Body).Decode(&input)
		json.NewEncoder(w).Encode(map[string]string{"reservationId": "hold-" + digest(input.Key)})
	}))
	defer billing.Close()
	t.Setenv("BILLING_URL", billing.URL)
	mux := http.NewServeMux()
	Register(mux, pool, config.Settings{JWTSecret: secret})
	idempotencyKey := ""
	request := func(method, path, user string, body []byte, contentType string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, bytes.NewReader(body))
		if contentType != "" {
			r.Header.Set("Content-Type", contentType)
		}
		if idempotencyKey != "" {
			r.Header.Set("Idempotency-Key", idempotencyKey)
		}
		if user != "" {
			token, _ := auth.IssueToken(user, secret, time.Hour, time.Now())
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	job, version := prefix+"job", prefix+"version"
	if _, err := pool.Exec(ctx, `INSERT INTO job(id,organization_id,title) VALUES($1,$2,'Engineer')`, job, org); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO job_version(id,job_id,version,source_text,source_media_type,draft_requirements) VALUES($1,$2,1,'Go experience','text/plain',$3::jsonb)`, version, job, `{"requirements":[{"stableId":"go","normalizedText":"Go experience","assessability":"resume_evidence","predicate":{"operator":"all_of","criteria":[{"type":"skill","canonicalName":"Go"}]},"evidence":[{"blockId":"jd-b1","quote":"Go experience"}]}],"qualityState":"ready","warnings":[]}`); err != nil {
		t.Fatal(err)
	}
	r := request("GET", "/api/jobs/"+job, employer, nil, "")
	if r.Code != 200 {
		t.Fatalf("job detail: %d %s", r.Code, r.Body.String())
	}
	var detail map[string]any
	if err := json.Unmarshal(r.Body.Bytes(), &detail); err != nil {
		t.Fatal(err)
	}
	if detail["versionId"] != version || detail["draftStatus"] != "ready" || detail["confirmed"] != false {
		t.Fatalf("job detail: %#v", detail)
	}
	r = request("GET", "/api/jobs/"+job, candidate, nil, "")
	if r.Code != 404 {
		t.Fatalf("candidate read: %d", r.Code)
	}
	confirm := []byte(`{"requirements":[{"stable_id":"go","normalized_text":"Go experience","kind":"required","weight":2}]}`)
	r = request("POST", "/api/jobs/"+job+"/requirements", employer, confirm, "application/json")
	if r.Code != 200 {
		t.Fatalf("confirm: %d %s", r.Code, r.Body.String())
	}
	var predicate string
	if err := pool.QueryRow(ctx, `SELECT predicate->'criteria'->0->>'canonicalName' FROM job_requirement WHERE job_version_id=$1`, version).Scan(&predicate); err != nil || predicate != "Go" {
		t.Fatalf("draft predicate not preserved: %s %v", predicate, err)
	}
	r = request("POST", "/api/jobs/"+job+"/requirements", employer, confirm, "application/json")
	if r.Code != 409 {
		t.Fatalf("confirmed mutation: %d", r.Code)
	}
	window := fmt.Sprintf(`{"opens_at":%q,"closes_at":%q}`, time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	r = request("PUT", "/api/jobs/"+job+"/application-window", employer, []byte(window), "application/json")
	if r.Code != 200 {
		t.Fatalf("window: %d %s", r.Code, r.Body.String())
	}
	r = request("POST", "/api/jobs/"+job+"/invitations", employer, []byte(`{"expires_in_hours":1}`), "application/json")
	if r.Code != 201 {
		t.Fatalf("invitation: %d %s", r.Code, r.Body.String())
	}
	var invitation struct{ Token, Passcode string }
	if err := json.Unmarshal(r.Body.Bytes(), &invitation); err != nil {
		t.Fatal(err)
	}
	if invitation.Passcode == "" {
		t.Fatal("missing frontend passcode")
	}
	r = request("POST", "/api/invitations/redeem", candidate, []byte(fmt.Sprintf(`{"passcode":%q}`, invitation.Passcode)), "application/json")
	if r.Code != 200 {
		t.Fatalf("redeem: %d %s", r.Code, r.Body.String())
	}
	upload := func() ([]byte, string) {
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		_ = form.WriteField("invitation_token", invitation.Token)
		file, _ := form.CreateFormFile("file", "resume.txt")
		_, _ = file.Write([]byte("Professional software engineer with five years of Go development experience and API design."))
		_ = form.Close()
		return body.Bytes(), form.FormDataContentType()
	}
	var wg sync.WaitGroup
	responses := make(chan *httptest.ResponseRecorder, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body, ct := upload()
			responses <- request("POST", "/api/jobs/"+job+"/resumes", candidate, body, ct)
		}()
	}
	wg.Wait()
	close(responses)
	accepted := 0
	for response := range responses {
		if response.Code == 202 {
			accepted++
			var receipt map[string]any
			_ = json.Unmarshal(response.Body.Bytes(), &receipt)
			if len(receipt) != 2 || receipt["submissionId"] == nil || receipt["receivedAt"] == nil {
				t.Fatalf("receipt leaks employer data: %s", response.Body.String())
			}
		} else if response.Code != 409 && response.Code != 404 {
			t.Fatalf("racing upload: %d %s", response.Code, response.Body.String())
		}
	}
	if accepted != 1 {
		t.Fatalf("accepted %d concurrent submissions", accepted)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM resume_submission WHERE job_id=$1`, job).Scan(&count); err != nil || count != 1 {
		t.Fatalf("submissions=%d err=%v", count, err)
	}
	batchBody := func(text string) ([]byte, string) {
		var body bytes.Buffer
		form := multipart.NewWriter(&body)
		file, _ := form.CreateFormFile("files", "batch.txt")
		_, _ = file.Write([]byte(text))
		_ = form.Close()
		return body.Bytes(), form.FormDataContentType()
	}
	text := "Experienced software engineer with professional Go development experience and API design."
	enterprise.Store(true)
	idempotencyKey = "enterprise-upload"
	body, ct := batchBody(text)
	r = request("POST", "/api/jobs/"+job+"/resume-batches/files", employer, body, ct)
	if r.Code != 202 {
		t.Fatalf("enterprise batch: %d %s", r.Code, r.Body.String())
	}
	var result struct {
		Accepted []struct {
			SubmissionID string `json:"submissionId"`
		} `json:"accepted"`
	}
	_ = json.Unmarshal(r.Body.Bytes(), &result)
	if len(result.Accepted) != 1 {
		t.Fatalf("enterprise result %s", r.Body.String())
	}
	enterpriseID := result.Accepted[0].SubmissionID
	creates := documentCreates.Load()
	r = request("POST", "/api/jobs/"+job+"/resume-batches/files", employer, body, ct)
	if r.Code != 202 || documentCreates.Load() != creates {
		t.Fatalf("retry duplicates document: %d %s", r.Code, r.Body.String())
	}
	var actor string
	var candidateUser *string
	if err := pool.QueryRow(ctx, `SELECT payload->>'userId' FROM service_outbox WHERE subject=$1`, enterpriseID).Scan(&actor); err != nil || actor != employer {
		t.Fatalf("outbox actor %s %v", actor, err)
	}
	if err := pool.QueryRow(ctx, `SELECT cr.user_id FROM candidate_record cr JOIN resume_submission rs ON rs.candidate_record_id=cr.id WHERE rs.id=$1`, enterpriseID).Scan(&candidateUser); err != nil || candidateUser != nil {
		t.Fatalf("employer linked as candidate %v %v", candidateUser, err)
	}
	if activations.Load() != 0 {
		t.Fatal("intake activated reservation before durable dispatch")
	}
	changed, changedCT := batchBody(text + " Modified.")
	r = request("POST", "/api/jobs/"+job+"/resume-batches/files", employer, changed, changedCT)
	if r.Code != 409 {
		t.Fatalf("changed idempotent body: %d %s", r.Code, r.Body.String())
	}
	enterprise.Store(false)
	idempotencyKey = "resumable-outage"
	failReserve.Store(true)
	r = request("POST", "/api/jobs/"+job+"/resume-batches/files", employer, body, ct)
	if r.Code != 503 {
		t.Fatalf("billing outage: %d %s", r.Code, r.Body.String())
	}
	creates = documentCreates.Load()
	r = request("POST", "/api/jobs/"+job+"/resume-batches/files", employer, body, ct)
	if r.Code != 202 || documentCreates.Load() != creates {
		t.Fatalf("outage retry: %d %s", r.Code, r.Body.String())
	}
	_ = json.Unmarshal(r.Body.Bytes(), &result)
	paidID := result.Accepted[0].SubmissionID
	var paidVersion string
	if err := pool.QueryRow(ctx, `SELECT resume_version_id FROM resume_submission WHERE id=$1`, paidID).Scan(&paidVersion); err != nil {
		t.Fatal(err)
	}
	purgeBody, _ := json.Marshal(map[string]any{"documentIds": []string{}, "versionIds": []string{paidVersion}})
	purge := httptest.NewRequest("POST", "/internal/hiring/prepare-purge-documents", bytes.NewReader(purgeBody))
	purge.Header.Set("X-Internal-Secret", secret)
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, purge)
	if response.Code != 200 || releases.Load() != 1 {
		t.Fatalf("prepare purge: %d %s releases=%d", response.Code, response.Body.String(), releases.Load())
	}
	var pending int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM service_outbox WHERE subject=$1 AND published_at IS NULL`, paidID).Scan(&pending); err != nil || pending != 0 {
		t.Fatalf("pending outbox after purge: %d %v", pending, err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM resume_submission WHERE id=$1`, paidID).Scan(&count); err != nil || count != 1 {
		t.Fatalf("prepare deleted submission: %d %v", count, err)
	}
	r = request("POST", "/api/jobs/"+job+"/resume-batches/files", employer, body, ct)
	if r.Code != 410 {
		t.Fatalf("purged operation accepted retry: %d %s", r.Code, r.Body.String())
	}
	// Expiry proof fences an unattached operation before billing can release it.
	idempotencyKey = "abandoned-intake"
	failReserve.Store(true)
	r = request("POST", "/api/jobs/"+job+"/resume-batches/files", employer, body, ct)
	if r.Code != 503 {
		t.Fatalf("abandoned setup: %d %s", r.Code, r.Body.String())
	}
	var abandoned string
	if err := pool.QueryRow(ctx, `SELECT submission_id FROM submission_intake WHERE organization_id=$1 AND state='pending' ORDER BY created_at DESC LIMIT 1`, org).Scan(&abandoned); err != nil {
		t.Fatal(err)
	}
	proof := httptest.NewRequest("GET", "/internal/hiring/holds/submission:"+abandoned, nil)
	proof.Header.Set("X-Internal-Secret", secret)
	proofResponse := httptest.NewRecorder()
	mux.ServeHTTP(proofResponse, proof)
	if proofResponse.Code != 200 || !strings.Contains(proofResponse.Body.String(), `"state":"absent"`) {
		t.Fatalf("hold proof: %d %s", proofResponse.Code, proofResponse.Body.String())
	}
	r = request("POST", "/api/jobs/"+job+"/resume-batches/files", employer, body, ct)
	if r.Code != 410 {
		t.Fatalf("late attachment after expiry: %d %s", r.Code, r.Body.String())
	}
}
