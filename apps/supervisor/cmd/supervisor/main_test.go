package main

import (
	"slices"
	"strings"
	"testing"
)

func TestProcessEnvironmentsStayConfined(t *testing.T) {
	base := []string{"DATABASE_URL=postgres://db", "JWT_SECRET=secret", "RAZORPAY_KEY_SECRET=key", "WEB_URL=https://app"}
	peers := peerURLs()

	identity := domainEnvironment(base, "identity", 8081, peers)
	if slices.Contains(identity, "RAZORPAY_KEY_SECRET=key") {
		t.Fatal("identity received payment credentials")
	}
	if !slices.Contains(identity, "PORT=8081") || !slices.Contains(identity, "BILLING_URL=http://127.0.0.1:8085") {
		t.Fatalf("identity environment incomplete: %v", identity)
	}
	if !slices.Contains(domainEnvironment(base, "billing", 8085, peers), "RAZORPAY_KEY_SECRET=key") {
		t.Fatal("billing lost payment credentials")
	}

	gateway := gatewayEnvironment(base, peers)
	for _, entry := range gateway {
		if strings.HasPrefix(entry, "DATABASE_URL=") || strings.HasPrefix(entry, "JWT_SECRET=") || strings.HasPrefix(entry, "RAZORPAY_") {
			t.Fatalf("gateway received secret %s", entry)
		}
	}
	if !slices.Contains(gateway, "PORT=8000") || !slices.Contains(gateway, "WEB_URL=https://app") {
		t.Fatalf("gateway environment incomplete: %v", gateway)
	}
}
