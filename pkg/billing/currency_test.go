package billing

import "testing"

// TestDefaultCurrency pins the billing-ledger default: with no override the
// currency is EUR, and BILLING_CURRENCY overrides it. This is the value every
// ledger row inherits when none is specified, so the fallback must not drift.
func TestDefaultCurrency(t *testing.T) {
	t.Run("falls back to EUR", func(t *testing.T) {
		t.Setenv(defaultCurrencyEnv, "")
		if got := DefaultCurrency(); got != "EUR" {
			t.Fatalf("DefaultCurrency() = %q, want EUR", got)
		}
	})

	t.Run("env override wins", func(t *testing.T) {
		t.Setenv(defaultCurrencyEnv, "USD")
		if got := DefaultCurrency(); got != "USD" {
			t.Fatalf("DefaultCurrency() = %q, want USD", got)
		}
	})
}
