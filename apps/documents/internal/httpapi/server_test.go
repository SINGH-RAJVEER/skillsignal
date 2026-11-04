package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/skillsignal/backend/config"
	"github.com/skillsignal/backend/documents"
)

func TestUploadSourcesAndRetentionListing(t *testing.T) {
	pool, setup := testDatabase(t, "documents")
	if _, err := setup.Exec(context.Background(), `INSERT INTO identity.organization(id,name) VALUES('org','Test')`); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	Register(mux, pool, config.Settings{JWTSecret: "secret", StorageRoot: t.TempDir()})
	call := func(method, path string, input any, secret string) *httptest.ResponseRecorder {
		data, _ := json.Marshal(input)
		r := httptest.NewRequest(method, path, bytes.NewReader(data))
		r.Header.Set("X-Internal-Secret", secret)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	if w := call("POST", "/internal/documents/job-descriptions", nil, ""); w.Code != 404 {
		t.Fatalf("untrusted request: %d", w.Code)
	}
	for _, source := range []struct {
		name, media string
		content     []byte
	}{{"role.md", "text/markdown", []byte("# Engineer\nGo experience")}, {"role.html", "text/html", []byte("<h1>Engineer</h1>")}, {"role.txt", documents.MediaTypeTXT, []byte("Engineer")}, {"role.pdf", documents.MediaTypePDF, []byte("%PDF-1.7\n")}} {
		w := call("POST", "/internal/documents/job-descriptions", map[string]any{"originalName": source.name, "mediaType": source.media, "content": source.content}, "secret")
		if w.Code != 201 {
			t.Fatalf("upload %s: %d %s", source.name, w.Code, w.Body.String())
		}
		var result struct {
			Key string `json:"storageKey"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		w = call("GET", "/internal/documents/objects/"+result.Key, nil, "secret")
		if w.Code != 200 || !bytes.Equal(w.Body.Bytes(), source.content) {
			t.Fatalf("stored source: %d %s", w.Code, w.Body.String())
		}
	}
	w := call("POST", "/internal/documents", map[string]any{"organizationId": "org", "originalName": "resume.txt", "mediaType": documents.MediaTypeTXT, "content": []byte("Resume"), "retentionDays": 1}, "secret")
	if w.Code != 201 {
		t.Fatalf("resume: %d %s", w.Code, w.Body.String())
	}
	var resume struct {
		ID      string `json:"documentId"`
		Version string `json:"versionId"`
	}
	json.Unmarshal(w.Body.Bytes(), &resume)
	if _, err := pool.Exec(context.Background(), `UPDATE resume_document SET retention_date=now()-interval '1 day' WHERE id=$1`, resume.ID); err != nil {
		t.Fatal(err)
	}
	w = call("GET", "/internal/documents/expired", nil, "secret")
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var expired []struct {
		ID         string   `json:"id"`
		VersionIDs []string `json:"versionIds"`
	}
	json.Unmarshal(w.Body.Bytes(), &expired)
	if len(expired) != 1 || expired[0].ID != resume.ID || len(expired[0].VersionIDs) != 1 || expired[0].VersionIDs[0] != resume.Version {
		t.Fatalf("expired: %s", w.Body.String())
	}
	for range 2 {
		w = call("DELETE", "/internal/documents/"+resume.ID, nil, "secret")
		if w.Code != 204 {
			t.Fatalf("delete: %d %s", w.Code, w.Body.String())
		}
	}
	w = call("GET", "/internal/documents/expired", nil, "secret")
	if w.Body.String() != "[]\n" {
		t.Fatalf("remaining: %s", w.Body.String())
	}
}

func TestDocumentRetryRejectsPayloadMismatchAndAdminIsHidden(t *testing.T) {
	pool, setup := testDatabase(t, "documents")
	if _, err := setup.Exec(context.Background(), `INSERT INTO identity.organization(id,name) VALUES('org','Test')`); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	Register(mux, pool, config.Settings{JWTSecret: "secret", StorageRoot: t.TempDir(), Billing: config.BillingSettings{AdminToken: "operator"}})
	post := func(input any) *httptest.ResponseRecorder {
		data, _ := json.Marshal(input)
		r := httptest.NewRequest("POST", "/internal/documents", bytes.NewReader(data))
		r.Header.Set("X-Internal-Secret", "secret")
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	input := map[string]any{"organizationId": "org", "originalName": "resume.txt", "mediaType": documents.MediaTypeTXT, "content": []byte("Original resume"), "idempotencyKey": "submission-document:stable-submission"}
	first := post(input)
	retry := post(input)
	if first.Code != 201 || retry.Code != 200 || first.Body.String() != retry.Body.String() {
		t.Fatalf("retry changed identity: %d %s %d %s", first.Code, first.Body.String(), retry.Code, retry.Body.String())
	}
	input["content"] = []byte("Different resume")
	mismatch := post(input)
	if mismatch.Code != 409 {
		t.Fatalf("mismatch accepted %d %s", mismatch.Code, mismatch.Body.String())
	}
	var original struct {
		Key string `json:"storageKey"`
	}
	json.Unmarshal(first.Body.Bytes(), &original)
	r := httptest.NewRequest("GET", "/internal/documents/objects/"+original.Key, nil)
	r.Header.Set("X-Internal-Secret", "secret")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Body.String() != "Original resume" {
		t.Fatalf("retry overwrote source: %s", w.Body.String())
	}
	var count int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM resume_document`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("duplicate document %d %v", count, err)
	}
	for _, token := range []string{"", "wrong"} {
		r := httptest.NewRequest("POST", "/api/admin/retention", nil)
		r.Header.Set("X-Admin-Token", token)
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != 404 {
			t.Fatalf("operator route exposed %d", w.Code)
		}
	}
}
