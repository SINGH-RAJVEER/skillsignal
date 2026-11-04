package httpapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/skillsignal/backend/billing"
	"github.com/skillsignal/backend/config"
	"github.com/skillsignal/backend/domainhttp"
	"github.com/skillsignal/backend/httpx"
	"github.com/skillsignal/backend/ids"
	"github.com/skillsignal/backend/points"
)

type Server struct {
	pool      *pgxpool.Pool
	settings  config.Settings
	economics billing.Settings
	client    *domainhttp.Client
	provider  paymentProvider
}

type paymentProvider interface {
	CreateOrder(int, string, string, map[string]string) (map[string]any, error)
	OrderPayments(string) ([]map[string]any, error)
}

func Register(mux *http.ServeMux, pool *pgxpool.Pool, settings config.Settings) {
	e := billing.DefaultSettings()
	e.PointsPerUSD = settings.Billing.PointsPerUSD
	e.MinIndependentPoints = settings.Billing.MinIndependentPoints
	e.MinEmployerResumePoints = settings.Billing.MinEmployerResumePoints
	e.PriceCeilingInputPerMil = settings.Billing.PriceCeilingInputPerMil
	e.PriceCeilingOutputPerMil = settings.Billing.PriceCeilingOutputPerMil
	e.RazorpayKeyID = settings.Billing.RazorpayKeyID
	e.RazorpayKeySecret = settings.Billing.RazorpayKeySecret
	e.RazorpayWebhookSecret = settings.Billing.RazorpayWebhookSecret
	if raw := os.Getenv("RAZORPAY_PACKS"); raw != "" {
		var packs []struct {
			ID        string `json:"id"`
			Points    int    `json:"points"`
			AmountINR int    `json:"amountInr"`
		}
		if err := json.Unmarshal([]byte(raw), &packs); err != nil {
			panic("Invalid RAZORPAY_PACKS")
		}
		e.Packs = nil
		seen := map[string]bool{}
		for _, p := range packs {
			if p.ID == "" || p.Points <= 0 || p.Points > math.MaxInt32 || p.AmountINR <= 0 || p.AmountINR > math.MaxInt32/100 || seen[p.ID] {
				panic("Invalid Razorpay pack")
			}
			seen[p.ID] = true
			e.Packs = append(e.Packs, billing.RazorpayPack{ID: p.ID, Points: p.Points, AmountINR: p.AmountINR})
		}
	}
	provider, _ := billing.NewClient(e.RazorpayKeyID, e.RazorpayKeySecret)
	s := &Server{pool: pool, settings: settings, economics: e, client: domainhttp.New(settings)}
	if provider != nil {
		s.provider = provider
	}
	mux.HandleFunc("GET /api/billing/packs", s.packs)
	mux.HandleFunc("GET /api/billing/quote", s.quote)
	mux.HandleFunc("GET /api/me/points", s.summary)
	mux.HandleFunc("GET /api/billing/ledger", s.ledger)
	mux.HandleFunc("POST /api/billing/orders", s.order)
	mux.HandleFunc("POST /api/billing/orders/{id}/verify", s.verify)
	mux.HandleFunc("POST /api/billing/orders/{id}/reconcile", s.reconcile)
	mux.HandleFunc("POST /api/webhooks/razorpay", s.webhook)
	mux.HandleFunc("POST /api/payments/webhook", s.webhook)
	mux.HandleFunc("POST /api/admin/organizations/{id}/entitlement", s.entitlement)
	mux.HandleFunc("DELETE /api/admin/organizations/{id}/entitlement", s.entitlement)
	mux.HandleFunc("POST /api/admin/enterprise/{id}", s.entitlement)
	mux.HandleFunc("DELETE /api/admin/enterprise/{id}", s.entitlement)
	for path, handler := range map[string]http.HandlerFunc{"POST /internal/billing/reservations": s.reserve, "POST /internal/billing/reservations/release": s.release, "POST /internal/billing/reservations/activate": s.activate, "POST /internal/billing/retention": s.expire, "POST /internal/billing/reservations/settle": s.settle} {
		mux.Handle(path, domainhttp.Internal(settings.JWTSecret, handler))
	}
}
func reply(w http.ResponseWriter, status int, value any) { domainhttp.WriteJSON(w, status, value) }
func fail(w http.ResponseWriter, err error)              { domainhttp.WriteError(w, err) }
func bad(w http.ResponseWriter, message string) {
	httpx.WriteError(w, 400, httpx.CodeInvalidRequest, message)
}
func (s *Server) access(w http.ResponseWriter, r *http.Request, write bool) (domainhttp.Access, bool) {
	a, err := s.client.Authenticate(r)
	if err != nil {
		fail(w, err)
		return a, false
	}
	if write && a.DemoReadOnly {
		httpx.WriteError(w, 403, httpx.CodeDemoAccount, "Demo accounts are read-only")
		return a, false
	}
	return a, true
}
func member(a domainhttp.Access, org string, owner bool) bool {
	for _, m := range a.Memberships {
		if m.OrganizationID == org && (!owner || m.Role == "owner") {
			return true
		}
	}
	return false
}
func (s *Server) account(r *http.Request, tx pgx.Tx, a domainhttp.Access, org string, owner bool) (string, error) {
	if org != "" {
		if !member(a, org, owner) {
			return "", &domainhttp.Error{Status: 403, Code: httpx.CodeForbidden, Message: "Organization access required"}
		}
		return points.EnsureOrganizationAccount(r.Context(), tx, org)
	}
	if a.AccountType != "candidate" {
		return "", &domainhttp.Error{Status: 403, Code: httpx.CodeForbidden, Message: "Employer billing requires an organization"}
	}
	return points.EnsureUserAccount(r.Context(), tx, a.UserID)
}
func (s *Server) packs(w http.ResponseWriter, r *http.Request) {
	out := []map[string]any{}
	for _, p := range s.economics.Packs {
		out = append(out, map[string]any{"id": p.ID, "points": p.Points, "amountInr": p.AmountINR})
	}
	reply(w, 200, out)
}
func (s *Server) quote(w http.ResponseWriter, r *http.Request) {
	q, err := billing.PointQuoteFor(r.URL.Query().Get("kind"), s.economics)
	if err != nil {
		bad(w, "Unknown quote kind")
		return
	}
	items := []map[string]any{}
	for _, i := range q.LineItems {
		items = append(items, map[string]any{"task": i.Task, "maxInputTokens": i.MaxInputTokens, "maxOutputTokens": i.MaxOutputTokens})
	}
	reply(w, 200, map[string]any{"kind": q.Kind, "points": q.Points, "minimumPoints": q.MinimumPoints, "costCeilingPoints": q.CostCeilingPoints, "lineItems": items})
}

type ownerProof struct {
	Attached      *bool  `json:"attached"`
	State         string `json:"state"`
	ChargedPoints int    `json:"chargedPoints"`
}

func (s *Server) expireTx(r *http.Request, tx pgx.Tx) error {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	// Skip locked rows so recovery does not wait behind worker settlement.
	rows, err := tx.Query(r.Context(), `SELECT id,purpose,idempotency_key FROM point_reservation WHERE state='reserved' AND (expires_at <= now() OR (expires_at IS NULL AND updated_at <= now()-interval '10 minutes')) ORDER BY COALESCE(expires_at,updated_at) LIMIT 50 FOR UPDATE SKIP LOCKED`)
	if err != nil {
		return err
	}
	type pending struct{ id, kind, key string }
	var holds []pending
	for rows.Next() {
		var h pending
		if err = rows.Scan(&h.id, &h.kind, &h.key); err != nil {
			rows.Close()
			return err
		}
		holds = append(holds, h)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	for _, h := range holds {
		owner := "evaluation"
		if h.kind == billing.EmployerQuote {
			owner = "hiring"
		}
		var proof ownerProof
		err := s.client.Call(ctx, owner, "/internal/"+owner+"/holds/"+url.PathEscape(h.key), nil, &proof)
		if proof.Attached == nil {
			err = errors.New("owner proof is missing attached state")
		}
		if err == nil && proof.Attached != nil && !*proof.Attached {
			proof.State = "absent"
		}
		if err != nil || proof.State == "active" || (proof.State == "completed" && proof.ChargedPoints <= 0) {
			_, err = tx.Exec(r.Context(), `UPDATE point_reservation SET expires_at=now()+interval '10 minutes' WHERE id=$1`, h.id)
		} else {
			switch proof.State {
			case "absent", "failed":
				err = points.Release(r.Context(), tx, h.id)
			case "completed":
				err = points.Settle(r.Context(), tx, h.id, proof.ChargedPoints, "evaluation")
			default:
				_, err = tx.Exec(r.Context(), `UPDATE point_reservation SET expires_at=now()+interval '10 minutes' WHERE id=$1`, h.id)
			}
		}
		if err != nil {
			return err
		}
	}
	rows, err = tx.Query(r.Context(), `SELECT id,user_id,COALESCE(operation_key,'') FROM weekly_free_use WHERE expires_at <= now() ORDER BY expires_at LIMIT 50 FOR UPDATE SKIP LOCKED`)
	if err != nil {
		return err
	}
	holds = nil
	for rows.Next() {
		var h pending
		if err = rows.Scan(&h.id, &h.kind, &h.key); err != nil {
			rows.Close()
			return err
		}
		holds = append(holds, h)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		return err
	}
	for _, h := range holds {
		var proof ownerProof
		err := s.client.Call(ctx, "evaluation", "/internal/evaluation/holds/"+url.PathEscape(h.key), nil, &proof)
		if proof.Attached == nil {
			err = errors.New("owner proof is missing attached state")
		}
		if err == nil && proof.Attached != nil && !*proof.Attached {
			proof.State = "absent"
		}
		if err == nil && (proof.State == "absent" || proof.State == "failed") {
			_, err = tx.Exec(r.Context(), `DELETE FROM weekly_free_use WHERE id=$1`, h.id)
		} else if err == nil && proof.State == "completed" {
			_, err = tx.Exec(r.Context(), `UPDATE weekly_free_use SET expires_at=NULL WHERE id=$1`, h.id)
		} else {
			_, err = tx.Exec(r.Context(), `UPDATE weekly_free_use SET expires_at=now()+interval '10 minutes' WHERE id=$1`, h.id)
		}
		if err != nil {
			return err
		}
	}
	return nil
}
func (s *Server) summary(w http.ResponseWriter, r *http.Request) {
	a, ok := s.access(w, r, false)
	if !ok {
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	org := r.URL.Query().Get("organization_id")
	account, err := s.account(r, tx, a, org, false)
	if err != nil {
		fail(w, err)
		return
	}
	balance, err := points.Balance(r.Context(), tx, account)
	if err != nil {
		fail(w, err)
		return
	}
	available, err := points.Available(r.Context(), tx, account)
	if err != nil {
		fail(w, err)
		return
	}
	out := map[string]any{"scope": "user", "balance": balance, "available": available}
	if org != "" {
		var enterprise bool
		if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM organization_entitlement WHERE organization_id=$1)`, org).Scan(&enterprise); err != nil {
			fail(w, err)
			return
		}
		out["scope"] = "organization"
		out["organizationId"] = org
		out["enterprise"] = enterprise
	} else {
		var used bool
		if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM weekly_free_use WHERE user_id=$1 AND week_start=$2)`, a.UserID, billing.WeekStart(time.Now())).Scan(&used); err != nil {
			fail(w, err)
			return
		}
		out["allowance"] = map[string]any{"freeUsedThisWeek": used, "resetsAt": billing.NextReset(time.Now())}
	}
	if err = tx.Commit(r.Context()); err != nil {
		fail(w, err)
		return
	}
	reply(w, 200, out)
}
func (s *Server) ledger(w http.ResponseWriter, r *http.Request) {
	a, ok := s.access(w, r, false)
	if !ok {
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	account, err := s.account(r, tx, a, r.URL.Query().Get("organization_id"), true)
	if err != nil {
		fail(w, err)
		return
	}
	rows, err := tx.Query(r.Context(), `SELECT id,amount,reason,created_at FROM point_ledger_entry WHERE account_id=$1 ORDER BY created_at DESC,id LIMIT 200`, account)
	if err != nil {
		fail(w, err)
		return
	}
	out := []map[string]any{}
	for rows.Next() {
		var id, reason string
		var amount int
		var at time.Time
		if err = rows.Scan(&id, &amount, &reason, &at); err != nil {
			break
		}
		out = append(out, map[string]any{"id": id, "amount": amount, "reason": reason, "createdAt": at})
	}
	rows.Close()
	if err == nil {
		err = rows.Err()
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		fail(w, err)
		return
	}
	reply(w, 200, out)
}

type reserveInput struct {
	UserID         string `json:"userId"`
	OrganizationID string `json:"organizationId"`
	Kind           string `json:"kind"`
	IdempotencyKey string `json:"idempotencyKey"`
	FreeEligible   bool   `json:"freeEligible"`
}

func (s *Server) reserve(w http.ResponseWriter, r *http.Request) {
	var in reserveInput
	if json.NewDecoder(r.Body).Decode(&in) != nil || (in.UserID == "" && in.OrganizationID == "") || in.IdempotencyKey == "" || (in.OrganizationID != "" && in.Kind != billing.EmployerQuote) || (in.OrganizationID == "" && in.Kind != billing.IndependentQuote) {
		bad(w, "Invalid reservation")
		return
	}
	q, err := billing.PointQuoteFor(in.Kind, s.economics)
	if err != nil {
		bad(w, "Unknown quote kind")
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	var account string
	if in.OrganizationID != "" {
		var entitled bool
		err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM organization_entitlement WHERE organization_id=$1)`, in.OrganizationID).Scan(&entitled)
		if err != nil {
			fail(w, err)
			return
		}
		if entitled {
			reply(w, 200, map[string]any{"enterprise": true})
			return
		}
		account, err = points.EnsureOrganizationAccount(r.Context(), tx, in.OrganizationID)
	} else {
		account, err = points.EnsureUserAccount(r.Context(), tx, in.UserID)
	}
	if err != nil {
		fail(w, err)
		return
	}
	// Serialize retries before looking up the idempotency key in points.Reserve.
	if _, err = tx.Exec(r.Context(), `SELECT id FROM point_account WHERE id=$1 FOR UPDATE`, account); err != nil {
		fail(w, err)
		return
	}
	var hasReservation bool
	if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM point_reservation WHERE account_id=$1 AND idempotency_key=$2)`, account, in.IdempotencyKey).Scan(&hasReservation); err != nil {
		fail(w, err)
		return
	}
	if !hasReservation && in.FreeEligible && in.Kind == billing.IndependentQuote && in.OrganizationID == "" {
		week := billing.WeekStart(time.Now())
		var existingWeek time.Time
		err = tx.QueryRow(r.Context(), `SELECT week_start FROM weekly_free_use WHERE user_id=$1 AND operation_key=$2`, in.UserID, in.IdempotencyKey).Scan(&existingWeek)
		if err == nil {
			if err = tx.Commit(r.Context()); err != nil {
				fail(w, err)
				return
			}
			reply(w, 200, map[string]any{"freeWeekStart": existingWeek, "reservationId": "", "operationKey": in.IdempotencyKey})
			return
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			fail(w, err)
			return
		}
		var id string
		err = tx.QueryRow(r.Context(), `INSERT INTO weekly_free_use(id,user_id,week_start,operation_key,expires_at) VALUES($1,$2,$3,$4,now()+interval '10 minutes') ON CONFLICT(user_id,week_start) DO UPDATE SET user_id=EXCLUDED.user_id WHERE weekly_free_use.operation_key=EXCLUDED.operation_key RETURNING id`, ids.New(), in.UserID, week, in.IdempotencyKey).Scan(&id)
		if err == nil {
			if err = tx.Commit(r.Context()); err != nil {
				fail(w, err)
				return
			}
			reply(w, 200, map[string]any{"freeWeekStart": week, "reservationId": "", "operationKey": in.IdempotencyKey})
			return
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			fail(w, err)
			return
		}
	}
	hold, err := points.Reserve(r.Context(), tx, account, q.Points, in.Kind, in.IdempotencyKey)
	if err == nil && hold.State != "reserved" {
		err = &domainhttp.Error{Status: 409, Code: httpx.CodeInsufficientPoints, Message: "Reservation is no longer available"}
	}
	if errors.Is(err, points.ErrInsufficientPoints) {
		httpx.WriteError(w, 402, httpx.CodeInsufficientPoints, "Insufficient points for this evaluation")
		return
	}
	if err == nil {
		_, err = tx.Exec(r.Context(), `UPDATE point_reservation SET expires_at=COALESCE(expires_at,now()+interval '10 minutes') WHERE id=$1 AND created_at=updated_at`, hold.ID)
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		fail(w, err)
		return
	}
	reply(w, 200, map[string]any{"reservationId": hold.ID})
}

type holdInput struct {
	ReservationID string     `json:"reservationId"`
	UserID        string     `json:"userId"`
	FreeWeekStart *time.Time `json:"freeWeekStart"`
	ChargedPoints int        `json:"chargedPoints"`
	Reason        string     `json:"reason"`
	OperationKey  string     `json:"operationKey"`
}

func (s *Server) changeHold(w http.ResponseWriter, r *http.Request, action string) {
	var in holdInput
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		bad(w, "Invalid hold")
		return
	}
	if in.ChargedPoints < 0 || (in.ReservationID != "" && in.FreeWeekStart != nil) || (in.FreeWeekStart != nil && (in.UserID == "" || in.OperationKey == "")) || (action == "settle" && in.ReservationID != "" && in.ChargedPoints == 0) {
		bad(w, "Invalid hold operation")
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	if in.ReservationID != "" {
		switch action {
		case "activate":
			var state string
			err = tx.QueryRow(r.Context(), `SELECT state FROM point_reservation WHERE id=$1 FOR UPDATE`, in.ReservationID).Scan(&state)
			if err == nil && state == "released" {
				err = &domainhttp.Error{Status: 409, Code: httpx.CodeInsufficientPoints, Message: "Reservation is no longer available"}
			}
			if err == nil && state == "reserved" {
				_, err = tx.Exec(r.Context(), `UPDATE point_reservation SET expires_at=now()+interval '10 minutes',updated_at=now() WHERE id=$1`, in.ReservationID)
			}
		case "settle":
			var state string
			err = tx.QueryRow(r.Context(), `SELECT state FROM point_reservation WHERE id=$1 FOR UPDATE`, in.ReservationID).Scan(&state)
			if err == nil && state == "released" {
				err = &domainhttp.Error{Status: 409, Code: httpx.CodeInsufficientPoints, Message: "Reservation was released"}
			}
			if err == nil && state == "reserved" {
				err = points.Settle(r.Context(), tx, in.ReservationID, in.ChargedPoints, in.Reason)
			}
		default:
			err = points.Release(r.Context(), tx, in.ReservationID)
		}
	}
	if err == nil && in.UserID != "" && in.FreeWeekStart != nil {
		if action == "release" {
			_, err = tx.Exec(r.Context(), `DELETE FROM weekly_free_use WHERE user_id=$1 AND week_start=$2 AND operation_key=$3`, in.UserID, in.FreeWeekStart, in.OperationKey)
		} else {
			var id string
			err = tx.QueryRow(r.Context(), `UPDATE weekly_free_use SET expires_at=now()+interval '10 minutes' WHERE user_id=$1 AND week_start=$2 AND operation_key=$3 RETURNING id`, in.UserID, in.FreeWeekStart, in.OperationKey).Scan(&id)
			if errors.Is(err, pgx.ErrNoRows) {
				err = &domainhttp.Error{Status: 409, Code: httpx.CodeInsufficientPoints, Message: "Weekly allowance is no longer available"}
			}
		}
	}
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		fail(w, err)
		return
	}
	reply(w, 200, map[string]bool{"ok": true})
}
func (s *Server) release(w http.ResponseWriter, r *http.Request)  { s.changeHold(w, r, "release") }
func (s *Server) activate(w http.ResponseWriter, r *http.Request) { s.changeHold(w, r, "activate") }
func (s *Server) settle(w http.ResponseWriter, r *http.Request)   { s.changeHold(w, r, "settle") }
func (s *Server) expire(w http.ResponseWriter, r *http.Request) {
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	if err = s.expireTx(r, tx); err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		fail(w, err)
		return
	}
	rows, err := s.pool.Query(r.Context(), `SELECT id FROM razorpay_webhook_event WHERE processed_at IS NULL ORDER BY received_at LIMIT 50`)
	if err != nil {
		fail(w, err)
		return
	}
	var events []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			fail(w, err)
			return
		}
		events = append(events, id)
	}
	rows.Close()
	if err = rows.Err(); err != nil {
		fail(w, err)
		return
	}
	failed := 0
	for _, id := range events {
		if err = s.processWebhook(r, id); err != nil {
			failed++
		}
	}
	if failed > 0 {
		httpx.WriteError(w, 503, httpx.CodeServiceUnavailable, "Some payment events remain pending")
		return
	}
	reply(w, 200, map[string]bool{"ok": true})
}
func (s *Server) order(w http.ResponseWriter, r *http.Request) {
	a, ok := s.access(w, r, true)
	if !ok {
		return
	}
	var in struct {
		PackID         string `json:"pack_id"`
		OrganizationID string `json:"organization_id"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil {
		bad(w, "Invalid order")
		return
	}
	pack := s.economics.Pack(in.PackID)
	if pack == nil {
		httpx.WriteError(w, 400, httpx.CodeInvalidPack, "Unknown point pack")
		return
	}
	if s.provider == nil {
		httpx.WriteError(w, 503, httpx.CodeServiceUnavailable, "Payments are not configured")
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	account, err := s.account(r, tx, a, in.OrganizationID, true)
	if err != nil {
		fail(w, err)
		return
	}
	id := ids.New()
	providerOrder, err := s.provider.CreateOrder(pack.AmountINR*100, "INR", id, map[string]string{"account_id": account, "pack_id": pack.ID})
	if err != nil {
		httpx.WriteError(w, 502, httpx.CodeServiceUnavailable, "Payment provider could not create the order")
		return
	}
	providerID := text(providerOrder["id"])
	if providerID == "" {
		httpx.WriteError(w, 502, httpx.CodeServiceUnavailable, "Payment provider returned an invalid order")
		return
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO razorpay_order(id,razorpay_order_id,account_id,purchaser_user_id,pack_id,points,amount_inr) VALUES($1,$2,$3,$4,$5,$6,$7)`, id, providerID, account, a.UserID, pack.ID, pack.Points, pack.AmountINR)
	if err == nil {
		err = tx.Commit(r.Context())
	}
	if err != nil {
		fail(w, err)
		return
	}
	reply(w, 201, map[string]any{"id": id, "razorpayOrderId": providerID, "razorpayKeyId": s.economics.RazorpayKeyID, "amountInr": pack.AmountINR, "currency": "INR", "packId": pack.ID, "points": pack.Points})
}
func (s *Server) verify(w http.ResponseWriter, r *http.Request) {
	a, ok := s.access(w, r, true)
	if !ok {
		return
	}
	var in struct {
		PaymentID string `json:"razorpay_payment_id"`
		Signature string `json:"razorpay_signature"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.PaymentID == "" {
		bad(w, "Invalid checkout callback")
		return
	}
	var providerID string
	var amount int
	err := s.pool.QueryRow(r.Context(), `SELECT razorpay_order_id,amount_inr FROM razorpay_order WHERE id=$1 AND purchaser_user_id=$2`, r.PathValue("id"), a.UserID).Scan(&providerID, &amount)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			fail(w, err)
			return
		}
		httpx.WriteError(w, 404, httpx.CodeNotFound, "Order not found")
		return
	}
	if s.economics.RazorpayKeySecret == "" || !billing.VerifyCheckoutSignature(providerID, in.PaymentID, in.Signature, s.economics.RazorpayKeySecret) {
		httpx.WriteError(w, 400, httpx.CodeInvalidSignature, "Invalid checkout signature")
		return
	}
	tag, err := s.pool.Exec(r.Context(), `INSERT INTO razorpay_payment(id,razorpay_payment_id,order_row_id,status,amount_inr,signature_verified,source) VALUES($1,$2,$3,'verified',$4,true,'callback') ON CONFLICT(razorpay_payment_id) DO UPDATE SET signature_verified=true,updated_at=now() WHERE razorpay_payment.order_row_id=EXCLUDED.order_row_id`, ids.New(), in.PaymentID, r.PathValue("id"), amount)
	if err != nil {
		fail(w, err)
		return
	}
	if tag.RowsAffected() == 0 {
		httpx.WriteError(w, 409, httpx.CodeInvalidRequest, "Payment belongs to another order")
		return
	}
	reply(w, 200, map[string]bool{"verified": true})
}
func integer(value any) int {
	switch v := value.(type) {
	case nil:
		return 0
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) || v != math.Trunc(v) || v > math.MaxInt32 || v < 0 {
			return -1
		}
		return int(v)
	case int:
		return v
	case json.Number:
		i, err := strconv.Atoi(string(v))
		if err != nil {
			return -1
		}
		return i
	}
	return -1
}
func text(value any) string { v, _ := value.(string); return v }

// applyPayment runs under an order lock so capture and refund deliveries cannot double grant.
func (s *Server) applyPayment(r *http.Request, tx pgx.Tx, payment map[string]any) error {
	paymentID, providerOrder := text(payment["id"]), text(payment["order_id"])
	if paymentID == "" || providerOrder == "" {
		return errors.New("payment identifiers missing")
	}
	var orderID, account string
	var packPoints, amount int
	err := tx.QueryRow(r.Context(), `SELECT id,account_id,points,amount_inr FROM razorpay_order WHERE razorpay_order_id=$1 FOR UPDATE`, providerOrder).Scan(&orderID, &account, &packPoints, &amount)
	if err != nil {
		return err
	}
	if text(payment["currency"]) != "INR" || integer(payment["amount"]) != amount*100 {
		return errors.New("payment amount mismatch")
	}
	status := text(payment["status"])
	captured := status == "captured" || status == "refunded"
	if !captured {
		return nil
	}
	refunded := integer(payment["amount_refunded"])
	if refunded < 0 || refunded > amount*100 {
		return errors.New("invalid refund amount")
	}
	var existingOrder string
	err = tx.QueryRow(r.Context(), `SELECT order_row_id FROM razorpay_payment WHERE razorpay_payment_id=$1`, paymentID).Scan(&existingOrder)
	if err == nil && existingOrder != orderID {
		return errors.New("payment belongs to another order")
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	_, err = tx.Exec(r.Context(), `INSERT INTO razorpay_payment(id,razorpay_payment_id,order_row_id,status,method,amount_inr,refunded_inr,points_granted,source) VALUES($1,$2,$3,$4,$5,$6,$7,true,'provider') ON CONFLICT(razorpay_payment_id) DO UPDATE SET status=CASE WHEN razorpay_payment.status='refunded' THEN 'refunded' ELSE EXCLUDED.status END,refunded_inr=GREATEST(razorpay_payment.refunded_inr,EXCLUDED.refunded_inr),points_granted=true,updated_at=now()`, ids.New(), paymentID, orderID, status, payment["method"], amount, refunded/100)
	if err != nil {
		return err
	}
	if _, err = points.Grant(r.Context(), tx, account, packPoints, "point_purchase", "purchase:"+paymentID); err != nil {
		return err
	}
	if refunded > 0 {
		target := int(int64(packPoints) * int64(refunded) / (int64(amount) * 100))
		var previous int
		err = tx.QueryRow(r.Context(), `SELECT COALESCE(-SUM(amount),0) FROM point_ledger_entry WHERE account_id=$1 AND reason=$2`, account, "refund:"+paymentID).Scan(&previous)
		if err != nil {
			return err
		}
		if target > previous {
			_, err = tx.Exec(r.Context(), `INSERT INTO point_ledger_entry(id,account_id,amount,reason,idempotency_key) VALUES($1,$2,$3,$4,$5) ON CONFLICT(account_id,idempotency_key) DO NOTHING`, ids.New(), account, previous-target, "refund:"+paymentID, "refund:"+paymentID+":"+strconv.Itoa(target))
			if err != nil {
				return err
			}
		}
	}
	orderStatus := "paid"
	if refunded == amount*100 {
		orderStatus = "refunded"
	}
	_, err = tx.Exec(r.Context(), `UPDATE razorpay_order SET status=CASE WHEN status='refunded' THEN status ELSE $2 END,updated_at=now() WHERE id=$1`, orderID, orderStatus)
	return err
}
func (s *Server) reconcile(w http.ResponseWriter, r *http.Request) {
	a, ok := s.access(w, r, true)
	if !ok {
		return
	}
	if s.provider == nil {
		httpx.WriteError(w, 503, httpx.CodeServiceUnavailable, "Payments are not configured")
		return
	}
	var providerID string
	if err := s.pool.QueryRow(r.Context(), `SELECT razorpay_order_id FROM razorpay_order WHERE id=$1 AND purchaser_user_id=$2`, r.PathValue("id"), a.UserID).Scan(&providerID); err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			fail(w, err)
			return
		}
		httpx.WriteError(w, 404, httpx.CodeNotFound, "Order not found")
		return
	}
	payments, err := s.provider.OrderPayments(providerID)
	if err != nil {
		httpx.WriteError(w, 502, httpx.CodeServiceUnavailable, "Payment provider is unavailable")
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	for _, p := range payments {
		if text(p["order_id"]) != providerID {
			fail(w, errors.New("provider returned payment for another order"))
			return
		}
		if err = s.applyPayment(r, tx, p); err != nil {
			fail(w, err)
			return
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		fail(w, err)
		return
	}
	reply(w, 200, map[string]bool{"reconciled": true})
}
func (s *Server) webhook(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		bad(w, "Invalid webhook")
		return
	}
	if s.economics.RazorpayWebhookSecret == "" || !billing.VerifyWebhookSignature(body, r.Header.Get("X-Razorpay-Signature"), s.economics.RazorpayWebhookSecret) {
		httpx.WriteError(w, 400, httpx.CodeInvalidSignature, "Invalid webhook signature")
		return
	}
	var payload map[string]any
	if json.Unmarshal(body, &payload) != nil || text(payload["event"]) == "" {
		bad(w, "Invalid webhook")
		return
	}
	eventID := r.Header.Get("X-Razorpay-Event-Id")
	if eventID == "" {
		bad(w, "Webhook event identifier required")
		return
	}
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		fail(w, err)
		return
	}
	defer tx.Rollback(r.Context())
	_, err = tx.Exec(r.Context(), `INSERT INTO razorpay_webhook_event(id,event_type,payload) VALUES($1,$2,$3) ON CONFLICT(id) DO NOTHING`, eventID, text(payload["event"]), body)
	if err != nil {
		fail(w, err)
		return
	}
	if err = tx.Commit(r.Context()); err != nil {
		fail(w, err)
		return
	}
	if err = s.processWebhook(r, eventID); err != nil {
		fail(w, err)
		return
	}
	reply(w, 200, map[string]bool{"received": true})
}

func (s *Server) processWebhook(r *http.Request, eventID string) error {
	tx, err := s.pool.Begin(r.Context())
	if err != nil {
		return err
	}
	defer tx.Rollback(r.Context())
	var body []byte
	var processed *time.Time
	if err = tx.QueryRow(r.Context(), `SELECT payload,processed_at FROM razorpay_webhook_event WHERE id=$1 FOR UPDATE`, eventID).Scan(&body, &processed); err != nil {
		return err
	}
	if processed != nil {
		return nil
	}
	var payload map[string]any
	if err = json.Unmarshal(body, &payload); err != nil {
		return err
	}
	event := text(payload["event"])
	if event == "payment.captured" || event == "payment.refunded" || event == "refund.processed" {
		payment := billing.PaymentEntity(payload)
		if len(payment) == 0 && event == "refund.processed" {
			section, _ := payload["payload"].(map[string]any)
			refund, _ := section["refund"].(map[string]any)
			entity, _ := refund["entity"].(map[string]any)
			paymentID := text(entity["payment_id"])
			var orderID string
			if err = tx.QueryRow(r.Context(), `SELECT o.razorpay_order_id FROM razorpay_order o JOIN razorpay_payment p ON p.order_row_id=o.id WHERE p.razorpay_payment_id=$1`, paymentID).Scan(&orderID); err != nil {
				return err
			}
			if s.provider == nil {
				return errors.New("payment provider unavailable")
			}
			payments, err := s.provider.OrderPayments(orderID)
			if err != nil {
				return err
			}
			for _, candidate := range payments {
				if text(candidate["id"]) == paymentID && text(candidate["order_id"]) == orderID {
					payment = candidate
					break
				}
			}
		}
		if err = s.applyPayment(r, tx, payment); err != nil {
			return err
		}
	}
	if _, err = tx.Exec(r.Context(), `UPDATE razorpay_webhook_event SET processed_at=now() WHERE id=$1`, eventID); err != nil {
		return err
	}
	return tx.Commit(r.Context())
}
func (s *Server) entitlement(w http.ResponseWriter, r *http.Request) {
	token := r.Header.Get("X-Admin-Token")
	if token == "" {
		token = r.Header.Get("Authorization")
		if len(token) > 7 && token[:7] == "Bearer " {
			token = token[7:]
		}
	}
	if s.settings.Billing.AdminToken == "" || subtle.ConstantTimeCompare([]byte(token), []byte(s.settings.Billing.AdminToken)) != 1 {
		httpx.WriteError(w, 404, httpx.CodeNotFound, "Route not found")
		return
	}
	var err error
	if r.Method == http.MethodDelete {
		_, err = s.pool.Exec(r.Context(), `DELETE FROM organization_entitlement WHERE organization_id=$1`, r.PathValue("id"))
	} else {
		var in struct {
			Note          string `json:"note"`
			ProvisionedBy string `json:"provisioned_by"`
		}
		if json.NewDecoder(r.Body).Decode(&in) != nil {
			bad(w, "Invalid entitlement")
			return
		}
		_, err = s.pool.Exec(r.Context(), `INSERT INTO organization_entitlement(id,organization_id,note,provisioned_by) VALUES($1,$2,$3,$4) ON CONFLICT(organization_id) DO UPDATE SET note=EXCLUDED.note,provisioned_by=EXCLUDED.provisioned_by`, ids.New(), r.PathValue("id"), in.Note, in.ProvisionedBy)
	}
	if err != nil {
		fail(w, err)
		return
	}
	reply(w, 200, map[string]bool{"ok": true})
}
