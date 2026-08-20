package billing

import "testing"

func TestFiatTopupMinimumCents(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		provider string
		currency string
		want     int64
		wantErr  bool
	}{
		{name: "stripe eur", provider: "stripe", currency: "eur", want: 500},
		{name: "stripe usd", provider: "stripe", currency: "USD", want: 500},
		{name: "mollie eur", provider: "mollie", currency: "EUR", want: 500},
		{name: "mollie usd", provider: "mollie", currency: "usd", want: 500},
		{name: "unknown provider", provider: "cash", currency: "EUR", wantErr: true},
		{name: "unsupported currency", provider: "stripe", currency: "JPY", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := FiatTopupMinimumCents(tt.provider, tt.currency)
			if (err != nil) != tt.wantErr {
				t.Fatalf("FiatTopupMinimumCents() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("FiatTopupMinimumCents() = %d, want %d", got, tt.want)
			}
		})
	}
}
