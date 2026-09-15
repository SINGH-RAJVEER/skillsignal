package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/skillsignal/backend/auth"
	"github.com/skillsignal/backend/httpx"
)

type User struct {
	ID            string    `json:"id"`
	Name          string    `json:"name"`
	Email         string    `json:"email"`
	AccountType   string    `json:"accountType"`
	EmailVerified bool      `json:"emailVerified"`
	Image         *string   `json:"image"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

type Store interface {
	RegisterCandidate(context.Context, string, string, string) (User, error)
	Credentials(context.Context, string) (User, string, error)
	User(context.Context, string) (User, error)
}

type handler struct {
	store     Store
	jwtSecret string
	jwtTTL    time.Duration
	clock     func() time.Time
}

type signUpRequest struct {
	Name     string `json:"name"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

type signInRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type authResponse struct {
	User      User      `json:"user"`
	Token     string    `json:"token"`
	TokenType string    `json:"tokenType"`
	ExpiresAt time.Time `json:"expiresAt"`
}

func New(store Store, jwtSecret string, jwtTTL time.Duration) http.Handler {
	mux := http.NewServeMux()
	Register(mux, store, jwtSecret, jwtTTL)
	return mux
}

func Register(mux *http.ServeMux, store Store, jwtSecret string, jwtTTL time.Duration) {
	h := &handler{store: store, jwtSecret: jwtSecret, jwtTTL: jwtTTL, clock: time.Now}
	mux.HandleFunc("POST /api/auth/sign-up/email", h.signUp)
	mux.HandleFunc("POST /api/auth/sign-in/email", h.signIn)
	mux.HandleFunc("POST /api/auth/sign-out", h.signOut)
	mux.HandleFunc("GET /api/auth/session", h.session)
}

func (h *handler) signUp(w http.ResponseWriter, r *http.Request) {
	var input signUpRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	name := strings.TrimSpace(input.Name)
	email := strings.ToLower(strings.TrimSpace(input.Email))
	if err := auth.ValidateCredentials(name, email, input.Password); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeInvalidCredentials, err.Error())
		return
	}
	passwordHash, err := auth.HashPassword(input.Password)
	if err != nil {
		h.internalError(w, err)
		return
	}
	user, err := h.store.RegisterCandidate(r.Context(), name, email, passwordHash)
	if errors.Is(err, ErrEmailAlreadyUsed) {
		httpx.WriteError(w, http.StatusConflict, httpx.CodeEmailAlreadyExists, "Email is already registered")
		return
	}
	if err != nil {
		h.internalError(w, err)
		return
	}
	h.writeAuth(w, http.StatusCreated, user)
}

func (h *handler) signIn(w http.ResponseWriter, r *http.Request) {
	var input signInRequest
	if !decodeJSON(w, r, &input) {
		return
	}
	email := strings.ToLower(strings.TrimSpace(input.Email))
	user, passwordHash, err := h.store.Credentials(r.Context(), email)
	if err != nil && !errors.Is(err, ErrNotFound) {
		h.internalError(w, err)
		return
	}
	if err != nil || user.AccountType != "candidate" || !auth.CheckPassword(input.Password, passwordHash) {
		httpx.WriteError(w, http.StatusUnauthorized, httpx.CodeInvalidEmailOrPassword, "Invalid email or password")
		return
	}
	h.writeAuth(w, http.StatusOK, user)
}

func (h *handler) signOut(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func (h *handler) session(w http.ResponseWriter, r *http.Request) {
	token := auth.BearerToken(r.Header.Get("Authorization"))
	userID, err := auth.Authenticate(token, h.jwtSecret, h.clock())
	if err != nil {
		writeJSON(w, http.StatusOK, nil)
		return
	}
	user, err := h.store.User(r.Context(), userID)
	if errors.Is(err, ErrNotFound) {
		writeJSON(w, http.StatusOK, nil)
		return
	}
	if err != nil {
		h.internalError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]User{"user": user})
}

func (h *handler) writeAuth(w http.ResponseWriter, status int, user User) {
	token, expiresAt := auth.IssueToken(user.ID, h.jwtSecret, h.jwtTTL, h.clock())
	writeJSON(w, status, authResponse{
		User:      user,
		Token:     token,
		TokenType: "Bearer",
		ExpiresAt: expiresAt,
	})
}

func (h *handler) internalError(w http.ResponseWriter, err error) {
	log.Printf("identity request failed: %T", err)
	httpx.WriteError(w, http.StatusInternalServerError, httpx.CodeInternalError, "Internal server error")
}

func decodeJSON(w http.ResponseWriter, r *http.Request, destination any) bool {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeInvalidRequest, "invalid JSON body")
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		httpx.WriteError(w, http.StatusBadRequest, httpx.CodeInvalidRequest, "invalid JSON body")
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
