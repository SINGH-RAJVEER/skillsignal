package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/skillsignal/backend/auth"
)

const testSecret = "test-secret-that-is-at-least-32-characters"

type fakeStore struct {
	user         User
	passwordHash string
}

func (s *fakeStore) RegisterCandidate(_ context.Context, name, email, passwordHash string) (User, error) {
	s.passwordHash = passwordHash
	s.user = User{
		ID:          "user-1",
		Name:        name,
		Email:       email,
		AccountType: "candidate",
		CreatedAt:   time.Date(2026, time.September, 15, 12, 0, 0, 0, time.UTC),
		UpdatedAt:   time.Date(2026, time.September, 15, 12, 0, 0, 0, time.UTC),
	}
	return s.user, nil
}

func (s *fakeStore) Credentials(_ context.Context, _ string) (User, string, error) {
	return s.user, s.passwordHash, nil
}

func (s *fakeStore) User(_ context.Context, _ string) (User, error) {
	return s.user, nil
}

func TestCandidateSignUpCreatesAccountAndReturnsSession(t *testing.T) {
	store := &fakeStore{}
	handler := New(store, testSecret, time.Hour)
	request := httptest.NewRequest(
		http.MethodPost,
		"/api/auth/sign-up/email",
		strings.NewReader(`{"name":" Ada ","email":"ADA@example.com","password":"password123"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if store.user.Name != "Ada" || store.user.Email != "ada@example.com" {
		t.Fatalf("registered user = %#v", store.user)
	}
	if !auth.CheckPassword("password123", store.passwordHash) {
		t.Fatal("stored password was not hashed")
	}
	var body struct {
		User      User   `json:"user"`
		Token     string `json:"token"`
		TokenType string `json:"tokenType"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.TokenType != "Bearer" || body.User.ID != "user-1" {
		t.Fatalf("response = %#v", body)
	}
	if subject, err := auth.Authenticate(body.Token, testSecret, time.Now()); err != nil || subject != "user-1" {
		t.Fatalf("token subject = %q, error = %v", subject, err)
	}
}

func TestCandidateCanSignInAndLoadSession(t *testing.T) {
	passwordHash, err := auth.HashPassword("password123")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	store := &fakeStore{
		user: User{
			ID:          "user-1",
			Name:        "Ada",
			Email:       "ada@example.com",
			AccountType: "candidate",
			CreatedAt:   time.Date(2026, time.September, 15, 12, 0, 0, 0, time.UTC),
			UpdatedAt:   time.Date(2026, time.September, 15, 12, 0, 0, 0, time.UTC),
		},
		passwordHash: passwordHash,
	}
	handler := New(store, testSecret, time.Hour)
	signIn := httptest.NewRequest(
		http.MethodPost,
		"/api/auth/sign-in/email",
		strings.NewReader(`{"email":"ADA@example.com","password":"password123"}`),
	)
	signInResponse := httptest.NewRecorder()

	handler.ServeHTTP(signInResponse, signIn)

	if signInResponse.Code != http.StatusOK {
		t.Fatalf("sign-in status = %d, body = %s", signInResponse.Code, signInResponse.Body.String())
	}
	var authenticated authResponse
	if err := json.Unmarshal(signInResponse.Body.Bytes(), &authenticated); err != nil {
		t.Fatalf("decode sign-in response: %v", err)
	}
	session := httptest.NewRequest(http.MethodGet, "/api/auth/session", nil)
	session.Header.Set("Authorization", "Bearer "+authenticated.Token)
	sessionResponse := httptest.NewRecorder()

	handler.ServeHTTP(sessionResponse, session)

	if sessionResponse.Code != http.StatusOK {
		t.Fatalf("session status = %d, body = %s", sessionResponse.Code, sessionResponse.Body.String())
	}
	var body struct {
		User User `json:"user"`
	}
	if err := json.Unmarshal(sessionResponse.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode session response: %v", err)
	}
	if body.User.ID != "user-1" {
		t.Fatalf("session user = %#v", body.User)
	}
}
