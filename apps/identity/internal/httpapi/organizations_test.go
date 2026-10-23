package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/skillsignal/backend/auth"
	"github.com/skillsignal/backend/ids"
)

func TestOrganizationJoinPolicyAndDemoGuards(t *testing.T) {
	address := os.Getenv("TEST_DATABASE_URL")
	if address == "" {
		t.Skip("TEST_DATABASE_URL must point to a disposable migrated database")
	}
	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(address)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = "identity,platform,public"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	store := NewPostgresStore(pool)
	mux := New(store, testSecret, time.Hour)
	prefix := ids.New()
	emailDomain := "d" + strings.ReplaceAll(strings.ToLower(prefix), "_", "a") + "x.example.com"
	owner, err := store.RegisterEmployer(ctx, "Owner", "owner@"+emailDomain, "test-hash")
	if err != nil {
		t.Fatal(err)
	}
	org := prefix + "org"
	if _, err := pool.Exec(ctx, `INSERT INTO organization(id,name) VALUES($1,'Test')`, org); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO organization_member(id,organization_id,user_id,role) VALUES($1,$2,$3,'owner')`, ids.New(), org, owner.ID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM organization WHERE id=$1`, org)
		_, _ = pool.Exec(ctx, `DELETE FROM "user" WHERE email LIKE $1`, "%@"+emailDomain)
	})
	request := func(method, path, user, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if user != "" {
			token, _ := auth.IssueToken(user, testSecret, time.Hour, time.Now())
			r.Header.Set("Authorization", "Bearer "+token)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}
	policyPath := "/api/organizations/" + org + "/join-policy"
	w := request("PUT", policyPath, owner.ID, `{"default_role":"recruiter"}`)
	if w.Code != 200 {
		t.Fatalf("frontend role contract: %d %s", w.Code, w.Body.String())
	}
	w = request("POST", policyPath+"/domains", owner.ID, `{"domain":"`+emailDomain+`"}`)
	if w.Code != 201 {
		t.Fatalf("domain: %d %s", w.Code, w.Body.String())
	}
	member, err := store.RegisterEmployer(ctx, "Recruiter", "recruiter@"+emailDomain, "test-hash")
	if err != nil {
		t.Fatal(err)
	}
	var role string
	if err := pool.QueryRow(ctx, `SELECT role FROM organization_member WHERE user_id=$1`, member.ID).Scan(&role); err != nil || role != "recruiter" {
		t.Fatalf("automatic membership %s %v", role, err)
	}
	candidate, err := store.RegisterCandidate(ctx, "Candidate", "candidate@"+emailDomain, "test-hash")
	if err != nil {
		t.Fatal(err)
	}
	var memberCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM organization_member WHERE user_id=$1`, candidate.ID).Scan(&memberCount); err != nil || memberCount != 0 {
		t.Fatalf("candidate joined organization %d %v", memberCount, err)
	}
	w = request("PUT", policyPath, member.ID, `{"default_role":"viewer"}`)
	if w.Code != 404 {
		t.Fatalf("recruiter changed policy: %d", w.Code)
	}
	w = request("DELETE", policyPath+"/domains/"+emailDomain, owner.ID, "")
	if w.Code != 204 {
		t.Fatalf("remove domain %d %s", w.Code, w.Body.String())
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM organization_member WHERE user_id=$1`, member.ID).Scan(&memberCount); err != nil || memberCount != 1 {
		t.Fatalf("removing domain removed member %d %v", memberCount, err)
	}
	if _, err := store.RegisterCandidate(ctx, "Duplicate", "owner@"+emailDomain, "test-hash"); err != ErrEmailAlreadyUsed {
		t.Fatalf("cross account duplicate: %v", err)
	}
	seed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]string{"organizationId": "demo-organization"})
	}))
	defer seed.Close()
	t.Setenv("HIRING_URL", seed.URL)
	t.Setenv("EVALUATION_URL", seed.URL)
	w = request("POST", "/api/demo/session", "", `{"act":"employer"}`)
	if w.Code != 200 {
		t.Fatalf("demo session %d %s", w.Code, w.Body.String())
	}
	var session authResponse
	if err := json.Unmarshal(w.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	if session.User.ID != "demo-employer" || !session.User.IsDemo || session.User.AccountType != "employer" || session.Token == "" {
		t.Fatalf("demo auth %#v", session)
	}
	for _, path := range []string{"/api/organizations", "/api/organizations/demo-organization/members", "/api/organizations/demo-organization/join-policy/domains"} {
		w = request("POST", path, session.User.ID, `{}`)
		if w.Code != 403 {
			t.Fatalf("demo mutation %s: %d %s", path, w.Code, w.Body.String())
		}
	}
	w = request("GET", "/api/organizations", session.User.ID, "")
	if w.Code != 200 {
		t.Fatalf("demo read %d", w.Code)
	}
}
