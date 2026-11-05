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
