// Package auth carries credential validation, bcrypt password hashing,
// and HS256 JWT issuance/verification. Claims mirror the Python service:
// issuer skillsignal-api with subject, issued-at, and expiry.
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
)

// JWTIssuer is the expected token issuer.
const JWTIssuer = "skillsignal-api"

// ErrInvalidCredentials marks a failed sign-in or token check.
var ErrInvalidCredentials = errors.New("invalid credentials")

// ErrCredentialValidation marks a rejected registration field.
type ErrCredentialValidation struct{ Message string }

func (e *ErrCredentialValidation) Error() string { return e.Message }

// ValidateEmail rejects malformed or overlong addresses.
func ValidateEmail(email string) error {
	if len(email) > 254 || strings.ContainsAny(email, " \t\r\n") {
		return &ErrCredentialValidation{Message: "Enter a valid email address"}
	}
	parsed, err := mail.ParseAddress(email)
	if err != nil || parsed.Name != "" || !strings.EqualFold(parsed.Address, email) {
		return &ErrCredentialValidation{Message: "Enter a valid email address"}
	}
	_, domain, found := strings.Cut(parsed.Address, "@")
	if !found || !strings.Contains(domain, ".") {
		return &ErrCredentialValidation{Message: "Enter a valid email address"}
	}
	return nil
}

// ValidateCredentials enforces name, email, and password bounds.
func ValidateCredentials(name, email, password string) error {
	if name == "" || len(name) > 100 {
		return &ErrCredentialValidation{Message: "Name must be between 1 and 100 characters"}
	}
	if err := ValidateEmail(email); err != nil {
		return err
	}
	length := len(password)
	if length < 8 || length > 72 {
		return &ErrCredentialValidation{Message: "Password must be between 8 and 72 characters"}
	}
	return nil
}

// HashPassword bcrypts with cost 10 like the Python service.
func HashPassword(password string) (string, error) {
	hash, err := bcrypt.GenerateFromPassword([]byte(password), 10)
	if err != nil {
		return "", err
	}
	return string(hash), nil
}

// CheckPassword reports whether the password matches; overlong or
// malformed hashes simply fail instead of raising.
func CheckPassword(password, hash string) bool {
	if len(password) > 72 {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
}

type claims struct {
	Issuer    string `json:"iss"`
	Subject   string `json:"sub"`
	IssuedAt  int64  `json:"iat"`
	ExpiresAt int64  `json:"exp"`
}

func signSegment(payload []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// IssueToken mints an HS256 token for the user id with the given TTL.
func IssueToken(userID, secret string, ttl time.Duration, now time.Time) (token string, expiresAt time.Time) {
	expiresAt = now.UTC().Add(ttl)
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	body, _ := json.Marshal(claims{
		Issuer:    JWTIssuer,
		Subject:   userID,
		IssuedAt:  now.UTC().Unix(),
		ExpiresAt: expiresAt.Unix(),
	})
	encoded := base64.RawURLEncoding.EncodeToString(body)
	unsigned := header + "." + encoded
	return unsigned + "." + signSegment([]byte(unsigned), secret), expiresAt
}

// Authenticate verifies issuer, signature, expiry, and subject.
func Authenticate(token, secret string, now time.Time) (string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return "", ErrInvalidCredentials
	}
	unsigned := parts[0] + "." + parts[1]
	if _, err := base64.RawURLEncoding.DecodeString(parts[2]); err != nil {
		return "", ErrInvalidCredentials
	}
	if !hmac.Equal([]byte(signSegment([]byte(unsigned), secret)), []byte(parts[2])) {
		return "", ErrInvalidCredentials
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || !utf8.Valid(raw) {
		return "", ErrInvalidCredentials
	}
	var parsed claims
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return "", ErrInvalidCredentials
	}
	if parsed.Issuer != JWTIssuer || parsed.Subject == "" {
		return "", ErrInvalidCredentials
	}
	if now.UTC().Unix() >= parsed.ExpiresAt {
		return "", ErrInvalidCredentials
	}
	return parsed.Subject, nil
}

// BearerToken splits an Authorization header into its bearer token.
func BearerToken(header string) string {
	scheme, token, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "bearer") || token == "" {
		return ""
	}
	return token
}

// HashToken returns the hex SHA-256 used for invitation token storage.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return fmt.Sprintf("%x", sum)
}
