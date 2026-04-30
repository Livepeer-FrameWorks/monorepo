package resolvers

import (
	"testing"

	"frameworks/api_gateway/graph/model"
)

func TestPurserPaymentMethod(t *testing.T) {
	cases := []struct {
		name    string
		in      model.PaymentMethod
		want    string
		wantErr bool
	}{
		{name: "card", in: model.PaymentMethodCard, want: "card"},
		{name: "eth", in: model.PaymentMethod("CRYPTO_ETH"), want: "crypto_eth"},
		{name: "usdc", in: model.PaymentMethod("CRYPTO_USDC"), want: "crypto_usdc"},
		{name: "bank transfer", in: model.PaymentMethodBankTransfer, want: "bank_transfer"},
		{name: "generic crypto rejected", in: model.PaymentMethod("CRYPTO"), wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := purserPaymentMethod(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Fatalf("purserPaymentMethod(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
