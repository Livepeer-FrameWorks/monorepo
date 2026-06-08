package handlers

import "testing"

// mapMolliePaymentStatus is the provider→internal status projection that gates
// invoice settlement side-effects. An unknown status must NOT default to a
// terminal state (it returns ok=false so the caller ignores it rather than
// flipping the invoice).
func TestMapMolliePaymentStatus(t *testing.T) {
	tests := []struct {
		status     string
		wantMapped string
		wantOK     bool
	}{
		{status: "paid", wantMapped: "confirmed", wantOK: true},
		{status: "failed", wantMapped: "failed", wantOK: true},
		{status: "cancelled", wantMapped: "failed", wantOK: true},
		{status: "expired", wantMapped: "failed", wantOK: true},
		{status: "pending", wantMapped: "pending", wantOK: true},
		{status: "open", wantMapped: "pending", wantOK: true},
		{status: "authorized", wantMapped: "", wantOK: false},
		{status: "", wantMapped: "", wantOK: false},
	}
	for _, tc := range tests {
		t.Run(tc.status, func(t *testing.T) {
			mapped, ok := mapMolliePaymentStatus(tc.status)
			if mapped != tc.wantMapped || ok != tc.wantOK {
				t.Errorf("mapMolliePaymentStatus(%q) = (%q, %v), want (%q, %v)", tc.status, mapped, ok, tc.wantMapped, tc.wantOK)
			}
		})
	}
}

func TestCurrencyMinorUnitExponent(t *testing.T) {
	tests := []struct {
		currency string
		want     int
	}{
		{currency: "EUR", want: 2},
		{currency: "usd", want: 2},
		{currency: "JPY", want: 0},
		{currency: "krw", want: 0},
		{currency: "BHD", want: 3},
		{currency: "KWD", want: 3},
		{currency: "ZZZ", want: 2}, // unknown defaults to 2
	}
	for _, tc := range tests {
		t.Run(tc.currency, func(t *testing.T) {
			if got := currencyMinorUnitExponent(tc.currency); got != tc.want {
				t.Errorf("currencyMinorUnitExponent(%q) = %d, want %d", tc.currency, got, tc.want)
			}
		})
	}
}

// centsToDecimalString must render minor units exactly (no float intermediate)
// at the currency's own exponent so values round-trip into NUMERIC columns.
func TestCentsToDecimalString(t *testing.T) {
	tests := []struct {
		name     string
		cents    int64
		currency string
		want     string
	}{
		{name: "EUR sub-unit", cents: 995, currency: "EUR", want: "9.95"},
		{name: "EUR whole", cents: 10000, currency: "EUR", want: "100.00"},
		{name: "EUR zero", cents: 0, currency: "EUR", want: "0.00"},
		{name: "negative reversal", cents: -250, currency: "EUR", want: "-2.50"},
		{name: "JPY zero exponent", cents: 1235, currency: "JPY", want: "1235"},
		{name: "BHD three exponent", cents: 1234, currency: "BHD", want: "1.234"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := centsToDecimalString(tc.cents, tc.currency); got != tc.want {
				t.Errorf("centsToDecimalString(%d, %q) = %q, want %q", tc.cents, tc.currency, got, tc.want)
			}
		})
	}
}
