// Package ids mints collision-resistant identifiers for rows.
//
// Python uses secrets.token_urlsafe(18): 24 base64url characters from 18
// random bytes. This matches that encoding exactly.
package ids

import (
	"crypto/rand"
	"encoding/base64"
)

// New returns 24 base64url characters backed by 18 random bytes.
func New() string {
	var raw [18]byte
	if _, err := rand.Read(raw[:]); err != nil {
		panic("ids: entropy unavailable: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(raw[:])
}

// Passcode returns an 8-character uppercase alphanumeric invitation code.
func Passcode() string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	var raw [8]byte
	if _, err := rand.Read(raw[:]); err != nil {
		panic("ids: entropy unavailable: " + err.Error())
	}
	out := make([]byte, 8)
	for i, b := range raw {
		out[i] = alphabet[int(b)%len(alphabet)]
	}
	return string(out)
}
