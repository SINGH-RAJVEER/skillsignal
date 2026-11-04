package domainhttp

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestInternalRejectsUntrustedRequests(t *testing.T) {
	for _, secret := range []string{"", "wrong"} {
		called := false
		handler := Internal("a-private-secret", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true }))
		request := httptest.NewRequest("POST", "/internal/test", nil)
		request.Header.Set("X-Internal-Secret", secret)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if called || response.Code != 404 {
			t.Fatalf("untrusted call reached handler: code %d", response.Code)
		}
	}
}

func TestAuthenticateRejectsBeforeCallingIdentity(t *testing.T) {
	client := &Client{secret: "secret"}
	if _, err := client.Authenticate(httptest.NewRequest("GET", "/api/test", nil)); err == nil {
		t.Fatal("missing bearer accepted")
	}
}
