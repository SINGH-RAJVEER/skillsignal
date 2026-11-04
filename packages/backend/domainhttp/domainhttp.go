// Package domainhttp authenticates service calls and resolves identity access.
package domainhttp

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/skillsignal/backend/auth"
	"github.com/skillsignal/backend/config"
	"github.com/skillsignal/backend/httpx"
)

type Membership struct {
	OrganizationID string `json:"organizationId"`
	Role           string `json:"role"`
	RetentionDays  int    `json:"retentionDays"`
}

type Access struct {
	UserID       string       `json:"userId"`
	AccountType  string       `json:"accountType"`
	DemoReadOnly bool         `json:"demoReadOnly"`
	Memberships  []Membership `json:"memberships"`
}

func (a Access) Role(organizationID string) string {
	for _, membership := range a.Memberships {
		if membership.OrganizationID == organizationID {
			return membership.Role
		}
	}
	return ""
}

type Error struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Message }

type Client struct {
	secret string
	urls   map[string]string
	http   *http.Client
}

func New(settings config.Settings) *Client {
	urls := make(map[string]string)
	for _, domain := range []string{"identity", "hiring", "documents", "evaluation", "billing"} {
		address := os.Getenv(strings.ToUpper(domain) + "_URL")
		if address == "" {
			address = "http://" + domain + ":8080"
		}
		urls[domain] = strings.TrimRight(address, "/")
	}
	return &Client{secret: settings.JWTSecret, urls: urls, http: &http.Client{Timeout: 30 * time.Second}}
}

func (c *Client) Authenticate(r *http.Request) (Access, error) {
	userID, err := auth.Authenticate(auth.BearerToken(r.Header.Get("Authorization")), c.secret, time.Now())
	if err != nil {
		return Access{}, &Error{Status: 401, Code: httpx.CodeUnauthorized, Message: "Sign in to continue"}
	}
	var access Access
	if err := c.Call(r.Context(), "identity", "/internal/identity/users/"+url.PathEscape(userID)+"/access", nil, &access); err != nil {
		return Access{}, err
	}
	if access.UserID != userID {
		return Access{}, &Error{Status: 401, Code: httpx.CodeUnauthorized, Message: "Session is no longer available"}
	}
	return access, nil
}

func (c *Client) Call(ctx context.Context, domain, path string, payload, destination any) error {
	method := http.MethodGet
	if payload != nil {
		method = http.MethodPost
	}
	return c.Request(ctx, method, domain, path, payload, destination)
}

func (c *Client) Request(ctx context.Context, method, domain, path string, payload, destination any) error {
	base, exists := c.urls[domain]
	if !exists || !strings.HasPrefix(path, "/internal/") {
		return &Error{Status: 503, Code: httpx.CodeServiceUnavailable, Message: "Invalid service operation"}
	}
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("X-Internal-Secret", c.secret)
	req.Header.Set("Content-Type", "application/json")
	response, err := c.http.Do(req)
	if err != nil {
		return &Error{Status: 503, Code: httpx.CodeServiceUnavailable, Message: domain + " service is unavailable"}
	}
	defer response.Body.Close()
	reader := io.LimitReader(response.Body, 32<<20)
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		failure := &Error{Status: response.StatusCode, Code: httpx.CodeServiceUnavailable, Message: domain + " operation failed"}
		_ = json.NewDecoder(reader).Decode(failure)
		return failure
	}
	if destination == nil {
		return nil
	}
	if err := json.NewDecoder(reader).Decode(destination); err != nil {
		return &Error{Status: 503, Code: httpx.CodeServiceUnavailable, Message: domain + " returned an invalid response"}
	}
	return nil
}

func Internal(secret string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provided := r.Header.Get("X-Internal-Secret")
		if secret == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(secret)) != 1 {
			httpx.WriteError(w, 404, httpx.CodeNotFound, "Route not found")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func WriteJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if status != http.StatusNoContent {
		_ = json.NewEncoder(w).Encode(value)
	}
}

func DecodeJSON(w http.ResponseWriter, r *http.Request, destination any) bool {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		httpx.WriteError(w, 400, httpx.CodeInvalidRequest, "Invalid request body")
		return false
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		httpx.WriteError(w, 400, httpx.CodeInvalidRequest, "Invalid request body")
		return false
	}
	return true
}

func WriteError(w http.ResponseWriter, err error) {
	var failure *Error
	if errors.As(err, &failure) {
		httpx.WriteError(w, failure.Status, failure.Code, failure.Message)
		return
	}
	httpx.WriteError(w, 500, httpx.CodeInternalError, "Internal server error")
}
