package httpapi

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/skillsignal/backend/auth"
	"github.com/skillsignal/backend/billing"
	"github.com/skillsignal/backend/config"
	"github.com/skillsignal/backend/domainhttp"
	"github.com/skillsignal/backend/ids"
)

// Billing tables use the real migration DDL in a disposable schema. Identity
// stubs supply only the foreign keys; no application database is modified.
func testServer(t *testing.T) *Server {
	t.Helper()
	dsn := os.Getenv("BILLING_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("BILLING_TEST_DATABASE_URL is required for PostgreSQL service tests")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "billing_test_" + strings.ReplaceAll(ids.New(), "-", "")
	if _, err = admin.Exec(ctx, `CREATE SCHEMA `+schema); err != nil {
		t.Fatal(err)
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pool.Close(); _, _ = admin.Exec(ctx, `DROP SCHEMA `+schema+` CASCADE`); admin.Close() })
	if _, err = pool.Exec(ctx, `CREATE TABLE "user"(id TEXT PRIMARY KEY); CREATE TABLE organization(id TEXT PRIMARY KEY); INSERT INTO "user" VALUES('candidate'),('other'),('owner'),('recruiter'); INSERT INTO organization VALUES('org')`); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join("..", "..", "..", "migrate", "internal", "migrate", "migrations")
	raw, err := os.ReadFile(filepath.Join(root, "000001_baseline.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"point_account", "point_ledger_entry", "point_reservation", "organization_entitlement", "weekly_free_use", "razorpay_order", "razorpay_payment", "razorpay_webhook_event"} {
		ddl := regexp.MustCompile(`(?s)CREATE TABLE ` + table + ` \(.*?\n\);`).Find(raw)
		if len(ddl) == 0 {
			t.Fatalf("missing migration table %s", table)
		}
		if _, err = pool.Exec(ctx, string(ddl)); err != nil {
			t.Fatal(err)
		}
	}
	raw, err = os.ReadFile(filepath.Join(root, "000004_mvp_reports.up.sql"))
	if err != nil {
		t.Fatal(err)
	}
	ddl := string(raw[strings.Index(string(raw), "ALTER TABLE billing.point_reservation"):])
	if _, err = pool.Exec(ctx, strings.ReplaceAll(ddl, "billing.", "")); err != nil {
		t.Fatal(err)
	}
	settings := config.Settings{JWTSecret: "billing-test-secret"}
	identity := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/internal/identity/users/"), "/access")
		a := domainhttp.Access{UserID: user, AccountType: "candidate"}
		a.DemoReadOnly = user == "demo"
		if user == "owner" || user == "recruiter" {
			a.AccountType = "employer"
			a.Memberships = []domainhttp.Membership{{OrganizationID: "org", Role: user}}
		}
		reply(w, 200, a)
	}))
	t.Cleanup(identity.Close)
	t.Setenv("IDENTITY_URL", identity.URL)
	return &Server{pool: pool, settings: settings, economics: billing.DefaultSettings(), client: domainhttp.New(settings)}
}

func call(t *testing.T, s *Server, handler http.HandlerFunc, payload any, user string) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/", strings.NewReader(string(body)))
	if user != "" {
		token, _ := auth.IssueToken(user, s.settings.JWTSecret, time.Hour, time.Now())
		r.Header.Set("Authorization", "Bearer "+token)
	}
	w := httptest.NewRecorder()
	handler(w, r)
	return w
}

func execute(t *testing.T, s *Server, query string, args ...any) {
	t.Helper()
	if _, err := s.pool.Exec(context.Background(), query, args...); err != nil {
		t.Fatal(err)
	}
}

func scalar[T any](t *testing.T, s *Server, query string, args ...any) T {
	t.Helper()
	var value T
	if err := s.pool.QueryRow(context.Background(), query, args...).Scan(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func fund(t *testing.T, s *Server) {
	execute(t, s, `INSERT INTO point_account(id,owner_user_id) VALUES('account','candidate'); INSERT INTO point_ledger_entry(id,account_id,amount,reason,idempotency_key) VALUES('grant','account',10000,'test','test')`)
}

func TestConcurrentReservationsAndSettlement(t *testing.T) {
	s := testServer(t)
	fund(t, s)
	var wg sync.WaitGroup
	responses := make(chan *httptest.ResponseRecorder, 12)
	for range 12 {
		wg.Go(func() {
			responses <- call(t, s, s.reserve, reserveInput{UserID: "candidate", Kind: billing.IndependentQuote, IdempotencyKey: "independent:one"}, "")
		})
	}
	wg.Wait()
	close(responses)
	for w := range responses {
		if w.Code != 200 {
			t.Fatalf("reserve %d: %s", w.Code, w.Body)
		}
	}
	if n := scalar[int](t, s, `SELECT count(*) FROM point_reservation`); n != 1 {
		t.Fatalf("%d reservations", n)
	}
	id := scalar[string](t, s, `SELECT id FROM point_reservation`)
	for _, charge := range []int{-10, 0} {
		if w := call(t, s, s.settle, holdInput{ReservationID: id, ChargedPoints: charge}, ""); w.Code != 400 {
			t.Fatalf("invalid settlement accepted: %d", w.Code)
		}
	}
	for range 2 {
		if w := call(t, s, s.settle, holdInput{ReservationID: id, ChargedPoints: 9999, Reason: "evaluation"}, ""); w.Code != 200 {
			t.Fatalf("settle %d: %s", w.Code, w.Body)
		}
	}
	amount := scalar[int](t, s, `SELECT amount FROM point_reservation`)
	if balance := scalar[int](t, s, `SELECT sum(amount) FROM point_ledger_entry`); balance != 10000-amount {
		t.Fatalf("balance %d", balance)
	}
	if w := call(t, s, s.release, holdInput{ReservationID: id}, ""); w.Code != 200 {
		t.Fatal(w.Body)
	}
	if w := call(t, s, s.activate, holdInput{ReservationID: id}, ""); w.Code != 200 {
		t.Fatalf("settled activation replay: %d %s", w.Code, w.Body)
	}
	if state := scalar[string](t, s, `SELECT state FROM point_reservation`); state != "settled" {
		t.Fatal(state)
	}
}

func TestWeeklyClaimAndStaleRelease(t *testing.T) {
	s := testServer(t)
	fund(t, s)
	var wg sync.WaitGroup
	responses := make(chan *httptest.ResponseRecorder, 8)
	for range 8 {
		wg.Go(func() {
			responses <- call(t, s, s.reserve, reserveInput{UserID: "candidate", Kind: billing.IndependentQuote, FreeEligible: true, IdempotencyKey: ids.New()}, "")
		})
	}
	wg.Wait()
	close(responses)
	for w := range responses {
		if w.Code != 200 {
			t.Fatalf("reserve %d: %s", w.Code, w.Body)
		}
	}
	if n := scalar[int](t, s, `SELECT count(*) FROM weekly_free_use`); n != 1 {
		t.Fatalf("%d free claims", n)
	}
	if n := scalar[int](t, s, `SELECT count(*) FROM point_reservation`); n != 7 {
		t.Fatalf("%d paid holds", n)
	}
	week := billing.WeekStart(time.Now())
	if w := call(t, s, s.release, holdInput{UserID: "candidate", FreeWeekStart: &week, OperationKey: "stale"}, ""); w.Code != 200 {
		t.Fatal(w.Body)
	}
	if n := scalar[int](t, s, `SELECT count(*) FROM weekly_free_use`); n != 1 {
		t.Fatal("stale release removed the current weekly claim")
	}
}

func TestRecoveryRequiresOwnerProof(t *testing.T) {
	s := testServer(t)
	fund(t, s)
	var proof any = map[string]any{"attached": true}
	status := 200
	owner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Internal-Secret") != s.settings.JWTSecret {
			t.Error("missing internal authentication")
		}
		if !strings.HasPrefix(r.URL.Path, "/internal/evaluation/holds/") {
			t.Error(r.URL.Path)
		}
		reply(w, status, proof)
	}))
	t.Cleanup(owner.Close)
	t.Setenv("EVALUATION_URL", owner.URL)
	s.client = domainhttp.New(s.settings)
	call(t, s, s.reserve, reserveInput{UserID: "candidate", Kind: billing.IndependentQuote, IdempotencyKey: "independent:held"}, "")
	call(t, s, s.reserve, reserveInput{UserID: "candidate", Kind: billing.IndependentQuote, IdempotencyKey: "independent:free", FreeEligible: true}, "")
	for _, scenario := range []string{"attached", "outage", "malformed", "absent"} {
		t.Run(scenario, func(t *testing.T) {
			switch scenario {
			case "outage":
				status = 503
			case "malformed":
				status = 200
				proof = map[string]any{}
			case "absent":
				proof = map[string]any{"attached": false}
			}
			execute(t, s, `UPDATE point_reservation SET expires_at=now()-interval '1 minute'; UPDATE weekly_free_use SET expires_at=now()-interval '1 minute'`)
			if w := call(t, s, s.expire, nil, ""); w.Code != 200 {
				t.Fatalf("expire %d: %s", w.Code, w.Body)
			}
			want := "reserved"
			free := 1
			if scenario == "absent" {
				want = "released"
				free = 0
			}
			if got := scalar[string](t, s, `SELECT state FROM point_reservation`); got != want {
				t.Fatalf("state %s", got)
			}
			if got := scalar[int](t, s, `SELECT count(*) FROM weekly_free_use`); got != free {
				t.Fatalf("free claims %d", got)
			}
		})
	}
}

func seedOrder(t *testing.T, s *Server) {
	fund(t, s)
	execute(t, s, `INSERT INTO razorpay_order(id,razorpay_order_id,account_id,purchaser_user_id,pack_id,points,amount_inr) VALUES('order','order_provider','account','candidate','pack',101,100)`)
}

func payment(refunded int) map[string]any {
	status := "captured"
	if refunded == 10000 {
		status = "refunded"
	}
	return map[string]any{"id": "pay_one", "order_id": "order_provider", "amount": 10000, "currency": "INR", "status": status, "amount_refunded": refunded}
}

func sendWebhook(t *testing.T, s *Server, id string, p map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(p)
	mac := hmac.New(sha256.New, []byte(s.economics.RazorpayWebhookSecret))
	mac.Write(body)
	r := httptest.NewRequest("POST", "/api/webhooks/razorpay", strings.NewReader(string(body)))
	r.Header.Set("X-Razorpay-Signature", hex.EncodeToString(mac.Sum(nil)))
	r.Header.Set("X-Razorpay-Event-Id", id)
	w := httptest.NewRecorder()
	s.webhook(w, r)
	return w
}

func TestWebhookRefundOrderingAndDurableRetry(t *testing.T) {
	s := testServer(t)
	seedOrder(t, s)
	s.economics.RazorpayWebhookSecret = "webhook-secret"
	for i, refunded := range []int{5050, 0, 10000, 0} {
		p := map[string]any{"event": "payment.refunded", "payload": map[string]any{"payment": map[string]any{"entity": payment(refunded)}}}
		for range 2 {
			if w := sendWebhook(t, s, string(rune('a'+i)), p); w.Code != 200 {
				t.Fatalf("webhook %d: %s", w.Code, w.Body)
			}
		}
		want := 10000
		if i < 2 {
			want = 10050
		}
		if got := scalar[int](t, s, `SELECT sum(amount) FROM point_ledger_entry`); got != want {
			t.Fatalf("refund delivery %d balance %d, want %d", i, got, want)
		}
	}
	if balance := scalar[int](t, s, `SELECT sum(amount) FROM point_ledger_entry`); balance != 10000 {
		t.Fatalf("balance after full refund %d", balance)
	}
	if state := scalar[string](t, s, `SELECT status FROM razorpay_order`); state != "refunded" {
		t.Fatal(state)
	}
	if n := scalar[int](t, s, `SELECT count(*) FROM point_ledger_entry WHERE idempotency_key='purchase:pay_one'`); n != 1 {
		t.Fatalf("grants %d", n)
	}
	missing := payment(0)
	missing["order_id"] = "late_provider"
	missing["id"] = "pay_two"
	p := map[string]any{"event": "payment.captured", "payload": map[string]any{"payment": map[string]any{"entity": missing}}}
	if w := sendWebhook(t, s, "late", p); w.Code != 500 {
		t.Fatalf("missing order %d", w.Code)
	}
	if n := scalar[int](t, s, `SELECT count(*) FROM razorpay_webhook_event WHERE id='late' AND processed_at IS NULL`); n != 1 {
		t.Fatal("failed event was not durably saved")
	}
	// The stored payload is retried, even if the next delivery changes its body.
	execute(t, s, `INSERT INTO razorpay_order(id,razorpay_order_id,account_id,purchaser_user_id,pack_id,points,amount_inr) VALUES('late_order','late_provider','account','candidate','pack',101,100)`)
	if w := call(t, s, s.expire, nil, ""); w.Code != 200 {
		t.Fatalf("inbox recovery %d: %s", w.Code, w.Body)
	}
	if w := sendWebhook(t, s, "late", map[string]any{"event": "ignored"}); w.Code != 200 {
		t.Fatalf("retry %d: %s", w.Code, w.Body)
	}
	if n := scalar[int](t, s, `SELECT count(*) FROM point_ledger_entry WHERE idempotency_key='purchase:pay_two'`); n != 1 {
		t.Fatal("pending event was not applied")
	}
}

func TestBillingAuthorizationAndCallbackDoesNotGrant(t *testing.T) {
	s := testServer(t)
	seedOrder(t, s)
	s.economics.RazorpayKeySecret = "checkout-secret"
	for _, tc := range []struct {
		user, org string
		want      int
	}{{"owner", "", 403}, {"recruiter", "org", 403}, {"other", "org", 403}} {
		r := httptest.NewRequest("GET", "/api/billing/ledger?organization_id="+tc.org, nil)
		token, _ := auth.IssueToken(tc.user, s.settings.JWTSecret, time.Hour, time.Now())
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.ledger(w, r)
		if w.Code != tc.want {
			t.Fatalf("%s ledger %d", tc.user, w.Code)
		}
	}
	mac := hmac.New(sha256.New, []byte(s.economics.RazorpayKeySecret))
	mac.Write([]byte("order_provider|pay_callback"))
	payload := map[string]string{"razorpay_payment_id": "pay_callback", "razorpay_signature": hex.EncodeToString(mac.Sum(nil))}
	body, _ := json.Marshal(payload)
	for _, user := range []string{"other", "candidate"} {
		r := httptest.NewRequest("POST", "/", strings.NewReader(string(body)))
		r.SetPathValue("id", "order")
		token, _ := auth.IssueToken(user, s.settings.JWTSecret, time.Hour, time.Now())
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.verify(w, r)
		want := 404
		if user == "candidate" {
			want = 200
		}
		if w.Code != want {
			t.Fatalf("verify %s %d: %s", user, w.Code, w.Body)
		}
	}
	if n := scalar[int](t, s, `SELECT count(*) FROM point_ledger_entry`); n != 1 {
		t.Fatal("callback granted points")
	}
}

type fakeProvider struct {
	payments []map[string]any
	order    map[string]any
	amount   int
}

func (p *fakeProvider) CreateOrder(amount int, currency, receipt string, notes map[string]string) (map[string]any, error) {
	p.amount = amount
	return p.order, nil
}
func (p *fakeProvider) OrderPayments(string) ([]map[string]any, error) { return p.payments, nil }

func TestOrdersAndPurchaserReconciliation(t *testing.T) {
	s := testServer(t)
	provider := &fakeProvider{order: map[string]any{"id": "order_provider"}}
	s.provider = provider
	if w := call(t, s, s.order, map[string]string{"pack_id": "unknown"}, "candidate"); w.Code != 400 {
		t.Fatalf("invalid pack %d", w.Code)
	}
	w := call(t, s, s.order, map[string]string{"pack_id": "pack-500"}, "candidate")
	if w.Code != 201 {
		t.Fatalf("order %d: %s", w.Code, w.Body)
	}
	if provider.amount != 49900 {
		t.Fatalf("provider amount %d", provider.amount)
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	p := payment(0)
	p["amount"] = 49900
	provider.payments = []map[string]any{p}
	for _, user := range []string{"other", "candidate", "candidate"} {
		r := httptest.NewRequest("POST", "/", nil)
		r.SetPathValue("id", text(out["id"]))
		token, _ := auth.IssueToken(user, s.settings.JWTSecret, time.Hour, time.Now())
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		s.reconcile(w, r)
		want := 200
		if user == "other" {
			want = 404
		}
		if w.Code != want {
			t.Fatalf("reconcile %s %d: %s", user, w.Code, w.Body)
		}
	}
	if balance := scalar[int](t, s, `SELECT sum(amount) FROM point_ledger_entry`); balance != 500 {
		t.Fatalf("balance %d", balance)
	}
	provider.order = map[string]any{}
	if w := call(t, s, s.order, map[string]string{"pack_id": "pack-500"}, "candidate"); w.Code != 502 {
		t.Fatalf("malformed provider response %d", w.Code)
	}
}

func TestEnterpriseBypassAndConcurrentOverspending(t *testing.T) {
	s := testServer(t)
	fund(t, s)
	execute(t, s, `INSERT INTO organization_entitlement(id,organization_id) VALUES('entitlement','org')`)
	w := call(t, s, s.reserve, reserveInput{OrganizationID: "org", Kind: billing.EmployerQuote, IdempotencyKey: "submission:enterprise"}, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"enterprise":true`) {
		t.Fatalf("enterprise %d: %s", w.Code, w.Body)
	}
	execute(t, s, `UPDATE point_ledger_entry SET amount=110`)
	var wg sync.WaitGroup
	responses := make(chan *httptest.ResponseRecorder, 2)
	for range 2 {
		wg.Go(func() {
			responses <- call(t, s, s.reserve, reserveInput{UserID: "candidate", Kind: billing.IndependentQuote, IdempotencyKey: ids.New()}, "")
		})
	}
	wg.Wait()
	close(responses)
	counts := map[int]int{}
	for w := range responses {
		counts[w.Code]++
	}
	if counts[200] != 1 || counts[402] != 1 {
		t.Fatalf("concurrent spending status counts %v", counts)
	}
}

func TestRefundEntityLookupAndWebhookAuthentication(t *testing.T) {
	s := testServer(t)
	seedOrder(t, s)
	s.economics.RazorpayWebhookSecret = "webhook-secret"
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"event":"payment.captured"}`))
	r.Header.Set("X-Razorpay-Event-Id", "invalid")
	w := httptest.NewRecorder()
	s.webhook(w, r)
	if w.Code != 400 {
		t.Fatalf("invalid signature %d", w.Code)
	}
	if n := scalar[int](t, s, `SELECT count(*) FROM razorpay_webhook_event`); n != 0 {
		t.Fatal("unauthenticated event persisted")
	}
	capture := map[string]any{"event": "payment.captured", "payload": map[string]any{"payment": map[string]any{"entity": payment(0)}}}
	if w = sendWebhook(t, s, "capture", capture); w.Code != 200 {
		t.Fatal(w.Body)
	}
	s.provider = &fakeProvider{payments: []map[string]any{payment(10000)}}
	refund := map[string]any{"event": "refund.processed", "payload": map[string]any{"refund": map[string]any{"entity": map[string]any{"id": "refund_one", "payment_id": "pay_one", "amount": 10000, "status": "processed"}}}}
	if w = sendWebhook(t, s, "refund", refund); w.Code != 200 {
		t.Fatalf("refund-only webhook %d: %s", w.Code, w.Body)
	}
	if balance := scalar[int](t, s, `SELECT sum(amount) FROM point_ledger_entry`); balance != 10000 {
		t.Fatalf("balance %d", balance)
	}
}

func TestInternalRoutesRequireServiceSecret(t *testing.T) {
	mux := http.NewServeMux()
	Register(mux, nil, config.Settings{JWTSecret: "service-secret"})
	for _, path := range []string{"reservations", "reservations/activate", "reservations/release", "reservations/settle", "retention"} {
		r := httptest.NewRequest("POST", "/internal/billing/"+path, strings.NewReader(`{}`))
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != 404 {
			t.Fatalf("untrusted %s %d", path, w.Code)
		}
	}
}

func TestDemoCannotCreateOrders(t *testing.T) {
	s := testServer(t)
	if w := call(t, s, s.order, map[string]string{"pack_id": "pack-500"}, "demo"); w.Code != 403 {
		t.Fatalf("demo write %d: %s", w.Code, w.Body)
	}
}
