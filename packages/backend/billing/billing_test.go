package billing

import "testing"

func TestFreeModelSettlementUsesMinimumRatherThanQuoteCeilings(t *testing.T) {
	settings := DefaultSettings()
	for _, kind := range []string{IndependentQuote, EmployerQuote} {
		quote, err := PointQuoteFor(kind, settings)
		if err != nil {
			t.Fatal(err)
		}
		charged := SettleFromUsage(16000, 4096, 0, kind, settings)
		if charged != quote.MinimumPoints {
			t.Fatalf("%s charged %d for zero reported cost, want %d", kind, charged, quote.MinimumPoints)
		}
	}
	if charged := SettleFromUsage(1, 1, 0.0251, IndependentQuote, settings); charged != 26 {
		t.Fatalf("reported cost rounded to %d points, want 26", charged)
	}
}
