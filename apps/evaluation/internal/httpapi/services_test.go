package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This opt-in contract test runs the real five service binaries against one fresh database.
func TestServiceContracts(t *testing.T) {
	if os.Getenv("SERVICE_INTEGRATION") != "1" {
		t.Skip("SERVICE_INTEGRATION=1 starts the five domain processes")
	}
	_, setup := testDatabase(t, "evaluation")
	root, err := filepath.Abs("../../../..")
	if err != nil {
		t.Fatal(err)
	}
	temp := t.TempDir()
	urls := map[string]string{}
	for _, name := range []string{"identity", "hiring", "documents", "evaluation", "billing"} {
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		urls[name] = "http://" + listener.Addr().String()
		listener.Close()
	}
	databaseURL, err := url.Parse(os.Getenv("TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	databaseURL.Path = "/" + setup.Config().ConnConfig.Database
	env := append(os.Environ(), "DATABASE_URL="+databaseURL.String(), "JWT_SECRET=integration-secret-not-for-production", "STORAGE_ROOT="+filepath.Join(temp, "storage"), "RETENTION_SWEEP_INTERVAL_SECONDS=0", "ADMIN_TOKEN=integration-admin")
	for name, url := range urls {
		env = append(env, strings.ToUpper(name)+"_URL="+url)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	for name, url := range urls {
		binary := filepath.Join(temp, name)
		build := exec.Command("go", "build", "-o", binary, "./apps/"+name+"/cmd/"+name)
		build.Dir = root
		if output, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %s %v", name, output, err)
		}
		logFile, err := os.Create(filepath.Join(temp, name+".log"))
		if err != nil {
			t.Fatal(err)
		}
		command := exec.CommandContext(ctx, binary)
		command.Dir = root
		command.Env = append(append([]string{}, env...), "PORT="+strings.Split(url, ":")[2])
		command.Stdout = logFile
		command.Stderr = logFile
		if err = command.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { command.Process.Kill(); command.Wait(); logFile.Close() })
		deadline := time.Now().Add(15 * time.Second)
		for {
			response, err := http.Get(url + "/readyz")
			if err == nil {
				response.Body.Close()
				if response.StatusCode == 200 {
					break
				}
			}
			if time.Now().After(deadline) {
				log, _ := os.ReadFile(logFile.Name())
				t.Fatalf("%s not ready: %s", name, log)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	t.Cleanup(func() {
		if t.Failed() {
			for name := range urls {
				data, _ := os.ReadFile(filepath.Join(temp, name+".log"))
				t.Logf("%s logs: %s", name, data)
			}
		}
	})
	request := func(service, method, path, token, contentType string, body io.Reader) (int, []byte) {
		t.Helper()
		r, err := http.NewRequest(method, urls[service]+path, body)
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set("Authorization", "Bearer "+token)
		r.Header.Set("Content-Type", contentType)
		r.Header.Set("Idempotency-Key", "integration-upload")
		response, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, _ := io.ReadAll(response.Body)
		return response.StatusCode, data
	}
	for _, accountType := range []string{"employer", "candidate"} {
		status, body := request("identity", "POST", "/api/demo/session", "", "application/json", strings.NewReader(fmt.Sprintf(`{"act":%q}`, accountType)))
		if status != 200 {
			t.Fatalf("demo %s: %d %s", accountType, status, body)
		}
		var session struct {
			Token string `json:"token"`
		}
		if err = json.Unmarshal(body, &session); err != nil || session.Token == "" {
			t.Fatalf("session: %s %v", body, err)
		}
		if accountType == "employer" {
			status, body = request("evaluation", "GET", "/api/jobs/demo-job/evaluations", session.Token, "", nil)
			var results []map[string]any
			json.Unmarshal(body, &results)
			if status != 200 || len(results) != 2 {
				t.Fatalf("demo results: %d %s", status, body)
			}
			status, body = request("evaluation", "GET", "/api/jobs/demo-job/evaluations?eligibility=not_eligible", session.Token, "", nil)
			json.Unmarshal(body, &results)
			if status != 200 || len(results) != 1 || results[0]["score"] != float64(0) {
				t.Fatalf("hard gate demo: %d %s", status, body)
			}
		} else {
			status, body = request("evaluation", "GET", "/api/independent-evaluations", session.Token, "", nil)
			if status != 200 || !bytes.Contains(body, []byte("demo-independent")) {
				t.Fatalf("private demo report: %d %s", status, body)
			}
			status, body = request("evaluation", "DELETE", "/api/independent-evaluations/demo-independent", session.Token, "", nil)
			if status != 403 {
				t.Fatalf("read only demo: %d %s", status, body)
			}
		}
	}
	// Repeating the orchestration must reuse the same fixtures and leave billing untouched.
	var count int
	if err = setup.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM billing.point_reservation)+(SELECT count(*) FROM billing.weekly_free_use)+(SELECT count(*) FROM billing.point_ledger_entry)`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("demo mutated billing %d %v", count, err)
	}
	status, body := request("identity", "POST", "/api/employer/auth/sign-up/email", "", "application/json", strings.NewReader(`{"name":"Employer","email":"employer@example.test","password":"password123"}`))
	var session struct {
		Token string `json:"token"`
		User  struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	if status != 201 || json.Unmarshal(body, &session) != nil || session.Token == "" {
		t.Fatalf("employer signup: %d %s", status, body)
	}
	status, body = request("identity", "POST", "/api/organizations", session.Token, "application/json", strings.NewReader(`{"name":"Workflow organization"}`))
	var org struct {
		ID string `json:"id"`
	}
	json.Unmarshal(body, &org)
	if status != 201 || org.ID == "" {
		t.Fatalf("organization: %d %s", status, body)
	}
	formBody, formType := serviceForm(t, map[string]string{"organization_id": org.ID, "title": "Engineer"}, "file", "role.md", "text/markdown", "# Professional Go experience required")
	status, body = request("hiring", "POST", "/api/jobs", session.Token, formType, bytes.NewReader(formBody))
	var job struct {
		ID        string `json:"id"`
		VersionID string `json:"versionId"`
	}
	json.Unmarshal(body, &job)
	if status != 201 || job.ID == "" {
		t.Fatalf("job upload: %d %s", status, body)
	}
	status, body = request("evaluation", "GET", "/api/jobs/"+job.ID+"/evaluations", session.Token, "", nil)
	if status != 200 || strings.TrimSpace(string(body)) != "[]" {
		t.Fatalf("draft results: %d %s", status, body)
	}
	var sourceKey, media string
	if err = setup.QueryRow(context.Background(), `SELECT source_storage_key,source_media_type FROM hiring.job_version WHERE id=$1`, job.VersionID).Scan(&sourceKey, &media); err != nil || sourceKey == "" || media != "text/markdown" {
		t.Fatalf("job source was not persisted: %s %s %v", sourceKey, media, err)
	}
	status, body = request("hiring", "POST", "/api/jobs/"+job.ID+"/requirements", session.Token, "application/json", strings.NewReader(`{"requirements":[{"stable_id":"go","kind":"required","weight":2,"normalized_text":"Professional Go experience"}]}`))
	if status != 200 {
		t.Fatalf("confirm requirements: %d %s", status, body)
	}
	if _, err = setup.Exec(context.Background(), `INSERT INTO billing.point_account(id,organization_id) VALUES('workflow-account',$1)`, org.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = setup.Exec(context.Background(), `INSERT INTO billing.point_ledger_entry(id,account_id,amount,reason,idempotency_key) VALUES('workflow-grant','workflow-account',100000,'Integration fixture funding','fixture')`); err != nil {
		t.Fatal(err)
	}
	formBody, formType = serviceForm(t, nil, "files", "resume.txt", "text/plain", "Ada Engineer\nSoftware engineer with documented professional Go experience.")
	var accepted struct {
		Accepted []struct {
			SubmissionID string `json:"submissionId"`
		} `json:"accepted"`
	}
	for range 2 {
		status, body = request("hiring", "POST", "/api/jobs/"+job.ID+"/resume-batches/files", session.Token, formType, bytes.NewReader(formBody))
		if status != 202 || json.Unmarshal(body, &accepted) != nil || len(accepted.Accepted) != 1 {
			t.Fatalf("employer upload/retry: %d %s", status, body)
		}
	}
	deadline := time.Now().Add(10 * time.Second)
	var evaluations []map[string]any
	for {
		status, body = request("evaluation", "GET", "/api/jobs/"+job.ID+"/evaluations", session.Token, "", nil)
		if status == 200 && json.Unmarshal(body, &evaluations) == nil && len(evaluations) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("outbox did not create evaluation: %d %s", status, body)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err = setup.QueryRow(context.Background(), `SELECT count(*) FROM billing.point_reservation WHERE account_id='workflow-account'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("upload retry duplicated point hold %d %v", count, err)
	}
	if _, err = setup.Exec(context.Background(), `UPDATE documents.resume_document SET retention_date=now()-interval '1 day' WHERE id IN (SELECT document_id FROM hiring.submission_intake WHERE submission_id=$1)`, accepted.Accepted[0].SubmissionID); err != nil {
		t.Fatal(err)
	}
	status, body = request("documents", "POST", "/api/admin/retention", "integration-admin", "application/json", strings.NewReader(`{}`))
	if status != 200 || !bytes.Contains(body, []byte(`"documentsPurged":1`)) {
		t.Fatalf("coordinated retention: %d %s", status, body)
	}
	if err = setup.QueryRow(context.Background(), `SELECT (SELECT count(*) FROM hiring.resume_submission WHERE id=$1)+(SELECT count(*) FROM evaluation.evaluation WHERE resume_submission_id=$1)+(SELECT count(*) FROM billing.point_reservation WHERE account_id='workflow-account' AND state='reserved')+(SELECT count(*) FROM hiring.candidate_record WHERE organization_id=(SELECT organization_id FROM hiring.submission_intake WHERE submission_id=$1))+(SELECT count(*) FROM documents.resume_document WHERE id=(SELECT document_id FROM hiring.submission_intake WHERE submission_id=$1))`, accepted.Accepted[0].SubmissionID).Scan(&count); err != nil || count != 0 {
		t.Fatalf("retention left records/holds %d %v", count, err)
	}

}

func serviceForm(t *testing.T, fields map[string]string, field, name, media, content string) ([]byte, string) {
	t.Helper()
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	for key, value := range fields {
		if err := form.WriteField(key, value); err != nil {
			t.Fatal(err)
		}
	}
	part, err := form.CreatePart(textproto.MIMEHeader{"Content-Disposition": {fmt.Sprintf(`form-data; name=%q; filename=%q`, field, name)}, "Content-Type": {media}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = part.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err = form.Close(); err != nil {
		t.Fatal(err)
	}
	return body.Bytes(), form.FormDataContentType()
}
