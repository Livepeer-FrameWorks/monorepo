package storagecost

import (
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"math"
	"testing"
)

func TestProject(t *testing.T) {
	tests := []struct {
		name     string
		pricing  *purserpb.StoragePricing
		bytes    int64
		wantMo   float64
		wantDay  float64
		wantCurr string
	}{
		{
			name:    "nil pricing returns zero",
			pricing: nil,
			bytes:   1_000_000_000,
		},
		{
			name:     "zero bytes returns zero (currency preserved)",
			pricing:  &purserpb.StoragePricing{UnitPricePerGbHour: 0.035, Currency: "EUR"},
			bytes:    0,
			wantCurr: "EUR",
		},
		{
			name:     "zero unit price returns zero (free meter)",
			pricing:  &purserpb.StoragePricing{UnitPricePerGbHour: 0, Currency: "EUR"},
			bytes:    5_000_000_000,
			wantCurr: "EUR",
		},
		{
			name:     "1 GiB @ 0.035/GiB-hour",
			pricing:  &purserpb.StoragePricing{UnitPricePerGbHour: 0.035, Currency: "EUR"},
			bytes:    1024 * 1024 * 1024,
			wantMo:   0.035 * 24 * 30,
			wantDay:  0.035 * 24,
			wantCurr: "EUR",
		},
		{
			name:     "100 MiB @ 0.035/GiB-hour",
			pricing:  &purserpb.StoragePricing{UnitPricePerGbHour: 0.035, Currency: "EUR"},
			bytes:    100 * 1024 * 1024,
			wantMo:   (100.0 / 1024.0) * 0.035 * 24 * 30,
			wantDay:  (100.0 / 1024.0) * 0.035 * 24,
			wantCurr: "EUR",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Project(tc.pricing, tc.bytes)
			if !approx(got.PerMonth, tc.wantMo) {
				t.Errorf("PerMonth: got %v want %v", got.PerMonth, tc.wantMo)
			}
			if !approx(got.PerDay, tc.wantDay) {
				t.Errorf("PerDay: got %v want %v", got.PerDay, tc.wantDay)
			}
			if got.Currency != tc.wantCurr {
				t.Errorf("Currency: got %q want %q", got.Currency, tc.wantCurr)
			}
		})
	}
}

func approx(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}
