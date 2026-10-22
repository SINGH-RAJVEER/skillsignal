package joinpolicy

import (
	"strings"
	"testing"
)

func TestNormalizeDomain(t *testing.T) {
	for _, input := range []string{"gmail.com", "-example.com", "example-.com", "a..com", "localhost", strings.Repeat("a", 64) + ".com", "example.com/path"} {
		if _, err := NormalizeDomain(input); err == nil {
			t.Errorf("accepted invalid or public domain %q", input)
		}
	}
	for input, want := range map[string]string{" @Example.COM ": "example.com", "a-b.example.org": "a-b.example.org"} {
		got, err := NormalizeDomain(input)
		if err != nil || got != want {
			t.Errorf("NormalizeDomain(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
}
