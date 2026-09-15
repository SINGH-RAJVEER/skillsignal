// Package billing carries point quotes, the weekly free allowance, and
// Razorpay order/signature helpers shared by the API and the worker.
package billing

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"
)

// Quote kinds referenced by routes and the worker settlement path.
const (
	IndependentQuote = "independent_evaluation"
	EmployerQuote    = "employer_resume"
)

// TaskBudget caps the billable tokens for one model task.
type TaskBudget struct {
	Task            string
	MaxInputTokens  int
	MaxOutputTokens int
}

// RazorpayPack is a purchasable point bundle.
type RazorpayPack struct {
	ID        string
	Points    int
	AmountINR int
}

// Settings mirrors the deployment-configured point economics.
type Settings struct {
	PointsPerUSD             int
	MinIndependentPoints     int
	MinEmployerResumePoints  int
	PriceCeilingInputPerMil  float64
	PriceCeilingOutputPerMil float64
	IndependentBudgets       []TaskBudget
	EmployerBudgets          []TaskBudget
	Packs                    []RazorpayPack
	RazorpayKeyID            string
	RazorpayKeySecret        string
	RazorpayWebhookSecret    string
	AdminToken               string
	EnterpriseSalesEmail     string
}

// DefaultSettings returns the production point economics.
func DefaultSettings() Settings {
	return Settings{
		PointsPerUSD:             1000,
		MinIndependentPoints:     10,
		MinEmployerResumePoints:  5,
		PriceCeilingInputPerMil:  3.0,
		PriceCeilingOutputPerMil: 15.0,
		IndependentBudgets:       []TaskBudget{{Task: "extraction", MaxInputTokens: 16000, MaxOutputTokens: 4096}},
		EmployerBudgets: []TaskBudget{
			{Task: "extraction", MaxInputTokens: 16000, MaxOutputTokens: 4096},
			{Task: "assessment", MaxInputTokens: 24000, MaxOutputTokens: 4096},
			{Task: "embedding", MaxInputTokens: 32000, MaxOutputTokens: 0},
		},
		Packs: []RazorpayPack{
			{ID: "pack-500", Points: 500, AmountINR: 499},
			{ID: "pack-2000", Points: 2000, AmountINR: 1499},
		},
		EnterpriseSalesEmail: "sales@skillsignal.app",
	}
}

// Pack finds a bundle by id.
func (s Settings) Pack(id string) *RazorpayPack {
	for i := range s.Packs {
		if s.Packs[i].ID == id {
			return &s.Packs[i]
		}
	}
	return nil
}

// PointQuote is the maximum pre-charge for an evaluation kind.
type PointQuote struct {
	Kind              string
	LineItems         []TaskBudget
	CostCeilingPoints int
	MinimumPoints     int
	Points            int
}

// ErrUnknownQuoteKind marks an unrecognized quote kind.
var ErrUnknownQuoteKind = errors.New("unknown quote kind")

// PointQuote prices the token budgets at the configured ceilings so no
// usage can exceed the reserved amount.
func PointQuoteFor(kind string, s Settings) (PointQuote, error) {
	var budgets []TaskBudget
	var minimum int
	switch kind {
	case IndependentQuote:
		budgets, minimum = s.IndependentBudgets, s.MinIndependentPoints
	case EmployerQuote:
		budgets, minimum = s.EmployerBudgets, s.MinEmployerResumePoints
	default:
		return PointQuote{}, fmt.Errorf("%w: %s", ErrUnknownQuoteKind, kind)
	}
	costUSD := 0.0
	for _, budget := range budgets {
		costUSD += float64(budget.MaxInputTokens)*s.PriceCeilingInputPerMil/1_000_000 +
			float64(budget.MaxOutputTokens)*s.PriceCeilingOutputPerMil/1_000_000
	}
	ceiling := int(math.Ceil(costUSD * float64(s.PointsPerUSD)))
	points := minimum
	if ceiling > points {
		points = ceiling
	}
	return PointQuote{Kind: kind, LineItems: budgets, CostCeilingPoints: ceiling, MinimumPoints: minimum, Points: points}, nil
}

// SettlePoints computes the completion charge: the greater of the minimum
// or the ceiling-rounded reported cost.
func SettlePoints(reportedCostUSD *float64, kind string, s Settings) int {
	minimum := s.MinEmployerResumePoints
	if kind == IndependentQuote {
		minimum = s.MinIndependentPoints
	}
	if reportedCostUSD == nil {
		return minimum
	}
	charged := int(math.Ceil(*reportedCostUSD * float64(s.PointsPerUSD)))
	if charged < minimum {
		return minimum
	}
	return charged
}

// SettleFromUsage charges from token counts at the price ceilings when the
// provider reported no usable cost. Mirrors worker/billing.py.
func SettleFromUsage(promptTokens, completionTokens int, reportedCostUSD float64, kind string, s Settings) int {
	cost := reportedCostUSD
	if cost <= 0 {
		cost = (float64(promptTokens)*s.PriceCeilingInputPerMil +
			float64(completionTokens)*s.PriceCeilingOutputPerMil) / 1_000_000
	}
	return SettlePoints(&cost, kind, s)
}

// WeekStart returns the Monday 00:00 UTC boundary of the calendar week.
func WeekStart(now time.Time) time.Time {
	moment := now.UTC()
	weekday := (int(moment.Weekday()) + 6) % 7
	monday := moment.AddDate(0, 0, -weekday)
	return time.Date(monday.Year(), monday.Month(), monday.Day(), 0, 0, 0, 0, time.UTC)
}

// NextReset returns the next Monday 00:00 UTC after now.
func NextReset(now time.Time) time.Time {
	return WeekStart(now).AddDate(0, 0, 7)
}

// VerifyCheckoutSignature checks the browser callback signature.
func VerifyCheckoutSignature(orderID, paymentID, signature, keySecret string) bool {
	mac := hmac.New(sha256.New, []byte(keySecret))
	mac.Write([]byte(orderID + "|" + paymentID))
	return hmac.Equal([]byte(hex.EncodeToString(mac.Sum(nil))), []byte(signature))
}

// VerifyWebhookSignature checks the raw-body webhook signature.
func VerifyWebhookSignature(body []byte, signature, webhookSecret string) bool {
	mac := hmac.New(sha256.New, []byte(webhookSecret))
	mac.Write(body)
	return hmac.Equal([]byte(hex.EncodeToString(mac.Sum(nil))), []byte(signature))
}

// ErrRazorpay marks a provider-side failure.
type ErrRazorpay struct {
	Message     string
	Unavailable bool
}

func (e *ErrRazorpay) Error() string { return e.Message }

// Client talks to the Razorpay orders API with basic auth.
type Client struct {
	keyID     string
	keySecret string
	baseURL   string
	timeout   time.Duration
	http      *http.Client
}

// NewClient requires credentials like the Python client.
func NewClient(keyID, keySecret string) (*Client, error) {
	if keyID == "" || keySecret == "" {
		return nil, &ErrRazorpay{Message: "Razorpay credentials are not configured", Unavailable: true}
	}
	return &Client{
		keyID:     keyID,
		keySecret: keySecret,
		baseURL:   "https://api.razorpay.com/v1",
		timeout:   15 * time.Second,
		http:      &http.Client{Timeout: 15 * time.Second},
	}, nil
}

// CreateOrder opens a Razorpay order for a point pack.
func (c *Client) CreateOrder(amountPaise int, currency, receipt string, notes map[string]string) (map[string]any, error) {
	body, err := c.doRequest("POST", "/orders", map[string]any{
		"amount": amountPaise, "currency": currency, "receipt": receipt, "notes": notes,
	})
	if err != nil {
		return nil, err
	}
	if _, ok := body["id"].(string); !ok {
		return nil, &ErrRazorpay{Message: "Razorpay order response is missing an identifier"}
	}
	return body, nil
}

// OrderPayments lists payments against a Razorpay order.
func (c *Client) OrderPayments(razorpayOrderID string) ([]map[string]any, error) {
	body, err := c.doRequest("GET", "/orders/"+razorpayOrderID+"/payments", nil)
	if err != nil {
		return nil, err
	}
	items, ok := body["items"].([]any)
	if !ok {
		return nil, &ErrRazorpay{Message: "Razorpay payments response is malformed"}
	}
	var payments []map[string]any
	for _, item := range items {
		if payment, ok := item.(map[string]any); ok {
			payments = append(payments, payment)
		}
	}
	return payments, nil
}

func (c *Client) doRequest(method, path string, payload map[string]any) (map[string]any, error) {
	var reader io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, &ErrRazorpay{Message: "Razorpay request encoding failed"}
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, c.baseURL+path, reader)
	if err != nil {
		return nil, &ErrRazorpay{Message: "Razorpay request failed", Unavailable: true}
	}
	req.SetBasicAuth(c.keyID, c.keySecret)
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, &ErrRazorpay{Message: "Razorpay request failed", Unavailable: true}
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, &ErrRazorpay{Message: "Razorpay returned invalid JSON"}
	}
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, &ErrRazorpay{Message: fmt.Sprintf("Razorpay returned invalid JSON (%d)", resp.StatusCode)}
	}
	if resp.StatusCode >= 400 {
		description := ""
		if errObj, ok := body["error"].(map[string]any); ok {
			description, _ = errObj["description"].(string)
		}
		if description == "" {
			description = fmt.Sprintf("Razorpay returned HTTP %d", resp.StatusCode)
		}
		return nil, &ErrRazorpay{Message: description}
	}
	return body, nil
}

// PaymentEntity unwraps the nested webhook payment entity.
func PaymentEntity(payload map[string]any) map[string]any {
	if section, ok := payload["payload"].(map[string]any); ok {
		if payment, ok := section["payment"].(map[string]any); ok {
			if entity, ok := payment["entity"].(map[string]any); ok {
				return entity
			}
		}
	}
	return map[string]any{}
}

// RefundEntities lists refund dicts on a payment payload.
func RefundEntities(payment map[string]any) []map[string]any {
	items, ok := payment["refunds"].([]any)
	if !ok {
		return nil
	}
	var refunds []map[string]any
	for _, item := range items {
		if refund, ok := item.(map[string]any); ok {
			refunds = append(refunds, refund)
		}
	}
	return refunds
}
