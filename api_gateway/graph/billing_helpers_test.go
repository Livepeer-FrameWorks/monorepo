package graph

import (
	"testing"

	"frameworks/api_gateway/graph/model"
)

func TestEntitlementMapToEntries(t *testing.T) {
	// Empty/nil maps yield a non-nil empty slice so the GraphQL field is [] not null.
	if got := entitlementMapToEntries(nil); got == nil || len(got) != 0 {
		t.Errorf("nil map → %v, want non-nil empty slice", got)
	}
	if got := entitlementMapToEntries(map[string]string{}); got == nil || len(got) != 0 {
		t.Errorf("empty map → %v, want non-nil empty slice", got)
	}

	// Keys must come out sorted regardless of proto map iteration order — the
	// wire response has to be deterministic (snapshot/cache stability).
	in := map[string]string{
		"max_streams": "10",
		"can_record":  "true",
		"region":      "\"eu\"",
	}
	got := entitlementMapToEntries(in)
	wantKeys := []string{"can_record", "max_streams", "region"}
	if len(got) != len(wantKeys) {
		t.Fatalf("got %d entries, want %d", len(got), len(wantKeys))
	}
	for i, e := range got {
		if e.Key != wantKeys[i] {
			t.Errorf("entry %d key = %q, want %q", i, e.Key, wantKeys[i])
		}
		if e.Value != in[e.Key] {
			t.Errorf("entry %q value = %q, want %q", e.Key, e.Value, in[e.Key])
		}
	}
}

func TestPaymentMethodFromPurser(t *testing.T) {
	tests := []struct {
		in      string
		want    model.PaymentMethod
		wantErr bool
	}{
		{"card", model.PaymentMethodCard, false},
		{"CARD", model.PaymentMethodCard, false}, // case-insensitive
		{"crypto_eth", model.PaymentMethodCryptoEth, false},
		{"crypto_usdc", model.PaymentMethodCryptoUsdc, false},
		{"bank_transfer", model.PaymentMethodBankTransfer, false},
		{"paypal", "", true}, // unknown must error, never silently map
		{"", "", true},
	}
	for _, tt := range tests {
		got, err := paymentMethodFromPurser(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("paymentMethodFromPurser(%q) err = nil, want error", tt.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("paymentMethodFromPurser(%q) unexpected err: %v", tt.in, err)
		}
		if got != tt.want {
			t.Errorf("paymentMethodFromPurser(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
