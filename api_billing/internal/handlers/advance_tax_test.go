package handlers

import (
	"frameworks/api_billing/internal/fx"
	"github.com/shopspring/decimal"
	"testing"
	"time"
)

func TestAdvanceChargesGrossAndCreditsNet(t *testing.T) {
	for _, tc := range []struct {
		currency, units                  string
		net, rate, wantGross, wantNetEUR int64
	}{
		{"EUR", "1", 10000, 2100, 12100, 10000}, {"EUR", "1", 10000, 0, 10000, 10000}, {"USD", "1.25", 10000, 2100, 12100, 8000},
	} {
		a, cash, err := quoteAdvanceMoney(tc.net, int(tc.rate), fx.Rate{Currency: tc.currency, UnitsPerEUR: decimal.RequireFromString(tc.units), Source: "ecb", ReferenceDate: time.Now()})
		if err != nil || cash.OriginalMinor != tc.wantGross || a.NetEUR != tc.wantNetEUR || a.NetEUR+a.VATEUR != cash.EURMinor || a.NetOriginal+a.VATOriginal != a.GrossOriginal {
			t.Fatalf("advance=%+v cash=%+v error=%v", a, cash, err)
		}
	}
}

// The EUR figures of an advance paid in another currency are the tax figures: its VAT is the rate
// on its EUR net, rounded once, whatever rounding the payment-currency figures took, and the EUR
// gross the receipt is recorded at is their sum.
func TestAdvanceEURVATIsTheRateOnTheEURNet(t *testing.T) {
	for _, units := range []string{"1.1713", "1.0842", "1.25", "0.8571"} {
		rate := fx.Rate{Currency: "USD", UnitsPerEUR: decimal.RequireFromString(units), Source: "ecb", ReferenceDate: time.Now()}
		for net := int64(1); net <= 3000; net++ {
			a, cash, err := quoteAdvanceMoney(net, 2100, rate)
			if err != nil {
				continue
			}
			wantVAT, _ := exclusiveTax(a.NetEUR, 2100)
			if a.VATEUR != wantVAT || a.NetEUR+a.VATEUR != a.GrossEUR || cash.EURMinor != a.GrossEUR || cash.OriginalMinor != a.GrossOriginal ||
				a.NetOriginal+a.VATOriginal != a.GrossOriginal {
				t.Fatalf("USD %d at %s: advance=%+v cash=%+v, want VAT EUR %d on net EUR %d", net, units, a, cash, wantVAT, a.NetEUR)
			}
		}
	}
}
