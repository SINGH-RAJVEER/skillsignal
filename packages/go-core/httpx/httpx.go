// Package httpx carries the JSON error envelope and HTTP middleware shared
// by the Go services. Codes match the Python ErrorCode contract so the web
// client keeps working while routes are ported one group at a time.
package httpx

import (
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"
)

// Error codes mirrored from the API contract.
const (
	CodeAlreadyMember          = "ALREADY_MEMBER"
	CodeApplicationsClosed     = "APPLICATIONS_CLOSED"
	CodeDemoAccount            = "DEMO_ACCOUNT"
	CodeEmailAlreadyExists     = "EMAIL_ALREADY_EXISTS"
	CodeForbidden              = "FORBIDDEN"
	CodeInsufficientPoints     = "INSUFFICIENT_POINTS"
	CodeInternalError          = "INTERNAL_ERROR"
	CodeInvalidCredentials     = "INVALID_CREDENTIALS"
	CodeInvalidDocument        = "INVALID_DOCUMENT"
	CodeInvalidEmailOrPassword = "INVALID_EMAIL_OR_PASSWORD"
	CodeInvalidPack            = "INVALID_PACK"
	CodeInvalidRequest         = "INVALID_REQUEST"
	CodeInvalidSignature       = "INVALID_SIGNATURE"
	CodeInvitationRedeemed     = "INVITATION_REDEEMED"
	CodeMemberExists           = "MEMBER_EXISTS"
	CodeNotFound               = "NOT_FOUND"
	CodeOriginNotAllowed       = "ORIGIN_NOT_ALLOWED"
	CodeOwnerRequired          = "OWNER_REQUIRED"
	CodeRequirementsNotConfirm = "REQUIREMENTS_NOT_CONFIRMED"
	CodeRuleExists             = "RULE_EXISTS"
	CodeServiceUnavailable     = "SERVICE_UNAVAILABLE"
	CodeUnauthorized           = "UNAUTHORIZED"
)

// Error is the JSON body returned for every API failure.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// WriteError encodes the contract error body without exposing internals.
func WriteError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(Error{Code: code, Message: message})
}

// NotImplemented marks a route whose Go port has not landed yet.
func NotImplemented(w http.ResponseWriter, message string) {
	if message == "" {
		message = "Go backend port in progress"
	}
	WriteError(w, http.StatusServiceUnavailable, CodeServiceUnavailable, message)
}

const (
	maxBodyBytes          = 1 << 20
	maxMultipartBodyBytes = 20 << 20
)

// WithBodyLimit rejects oversized JSON and multipart bodies early.
func WithBodyLimit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		limit := int64(maxBodyBytes)
		if strings.Contains(r.Header.Get("Content-Type"), "multipart/form-data") {
			limit = int64(maxMultipartBodyBytes)
		}
		if r.ContentLength > limit {
			WriteError(w, http.StatusBadRequest, CodeInvalidRequest, "invalid JSON body")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, limit)
		next.ServeHTTP(w, r)
	})
}

// WithOriginGuard allows only the configured web origin and answers CORS preflights.
func WithOriginGuard(webURL string) func(http.Handler) http.Handler {
	allowed := strings.TrimSuffix(webURL, "/")
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := strings.TrimSuffix(r.Header.Get("Origin"), "/")
			if origin != "" && origin != allowed {
				WriteError(w, http.StatusForbidden, CodeOriginNotAllowed, "Origin is not allowed")
				return
			}
			if origin != "" {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Vary", "Origin")
			}
			if r.Method == http.MethodOptions {
				w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Max-Age", "600")
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// WithLogging records method, path, status and latency per request.
func WithLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		defer func() {
			log.Printf("%s %s %d %s", r.Method, r.URL.Path, recorder.status, time.Since(started).Truncate(time.Millisecond))
		}()
		next.ServeHTTP(recorder, r)
	})
}
