// Package joinpolicy guards organization join rules: company domains can
// be claimed, public mailbox providers cannot, and member emails match
// case-insensitively.
package joinpolicy

import (
	"errors"
	"regexp"
	"strings"
)

// ErrInvalidDomain marks a domain that cannot enter a join policy.
var ErrInvalidDomain = errors.New("Enter a valid company email domain; public email providers are not claimable")

// PublicEmailDomains can never be claimed by one organization.
var PublicEmailDomains = map[string]struct{}{
	"gmail.com": {}, "outlook.com": {}, "hotmail.com": {}, "live.com": {},
	"yahoo.com": {}, "icloud.com": {}, "me.com": {}, "aol.com": {},
	"proton.me": {}, "protonmail.com": {}, "gmx.com": {}, "zoho.com": {},
	"yandex.com": {},
}

var domainPattern = regexp.MustCompile(`^(?=.{1,253}$)(?!-)[a-z0-9-]{1,63}(?<!-)(\.(?!-)[a-z0-9-]{1,63}(?<!-))+$`)

// NormalizeDomain lowercases, strips a leading @, and rejects public or
// malformed domains.
func NormalizeDomain(domain string) (string, error) {
	normalized := strings.ToLower(strings.TrimSpace(domain))
	normalized = strings.TrimPrefix(normalized, "@")
	if normalized == "" {
		return "", ErrInvalidDomain
	}
	if _, public := PublicEmailDomains[normalized]; public {
		return "", ErrInvalidDomain
	}
	if !domainPattern.MatchString(normalized) {
		return "", ErrInvalidDomain
	}
	return normalized, nil
}

// EmailDomain returns the casefolded domain of an address.
func EmailDomain(email string) string {
	_, domain, _ := strings.Cut(email, "@")
	return strings.ToLower(domain)
}
