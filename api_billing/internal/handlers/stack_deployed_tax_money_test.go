//go:build stack_verify

package handlers_test

import (
	"testing"

	billingtax "frameworks/api_billing/internal/tax"
)

func TestStackCryptoTaxRetainedNativeMoney(t *testing.T) {
	usd := billingtax.AdvanceMoney{Currency: "USD", RateBPS: 2100, NetOriginal: 550, VATOriginal: 116, GrossOriginal: 666, NetEUR: 500, VATEUR: 106, GrossEUR: 606}
	wrongNative := usd
	wrongNative.VATOriginal--
	eur := billingtax.AdvanceMoney{Currency: "EUR", RateBPS: 2100, NetOriginal: 500, VATOriginal: 105, GrossOriginal: 605, NetEUR: 500, VATEUR: 105, GrossEUR: 605}
	zero := billingtax.AdvanceMoney{Currency: "USD", NetOriginal: 549, GrossOriginal: 549, NetEUR: 500, GrossEUR: 500}
	for _, tc := range []struct {
		name            string
		net, vat, gross int64
		money           *billingtax.AdvanceMoney
		units           string
		rate            int64
		wantError       bool
	}{
		{"native rounding is converted once", 500, 106, 606, &usd, "1.0989", 2100, false},
		{"EUR recomputation changes accepted tax", 500, 105, 605, &usd, "1.0989", 2100, true},
		{"native tax changed", 500, 106, 606, &wrongNative, "1.0989", 2100, true},
		{"FX changed", 500, 106, 606, &usd, "1.25", 2100, true},
		{"missing retained evidence", 500, 106, 606, nil, "1.0989", 2100, true},
		{"identity rate", 500, 105, 605, &eur, "1", 2100, false},
		{"zero tax", 500, 0, 500, &zero, "1.0989", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := checkStackCryptoTaxMoney(tc.net, tc.vat, tc.gross, tc.money, tc.units, tc.rate)
			if (err != nil) != tc.wantError {
				t.Fatalf("retained native-currency tax check: %v, want error=%v", err, tc.wantError)
			}
		})
	}
}
