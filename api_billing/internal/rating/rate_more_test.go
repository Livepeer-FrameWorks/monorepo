package rating

import (
	"testing"

	"github.com/shopspring/decimal"
)

// TestRate_UsageLinesSortedAscendingByLineKey pins the determinism sort to an
// ascending order. Rules are supplied in descending-key order so the natural
// append order is unsorted; a flipped comparator would leave them descending.
func TestRate_UsageLinesSortedAscendingByLineKey(t *testing.T) {
	rules := []Rule{
		{
			Meter: MeterStorageGBSecondsCld, Model: ModelAllUsage, Currency: "EUR",
			UnitPrice: dec("0.035000"),
		},
		{
			Meter: MeterDeliveredMinutes, Model: ModelTieredGraduated, Currency: "EUR",
			IncludedQuantity: decimal.Zero, UnitPrice: dec("0.000550"),
		},
	}
	res, err := Rate(Input{
		Currency:  "EUR",
		BasePrice: dec("0"),
		Rules:     rules,
		Usage: map[Meter]decimal.Decimal{
			MeterStorageGBSecondsCld: dec("3600"),
			MeterDeliveredMinutes:    dec("1000"),
		},
	})
	if err != nil {
		t.Fatalf("Rate: %v", err)
	}
	if len(res.UsageLines) != 2 {
		t.Fatalf("usage lines = %d, want 2", len(res.UsageLines))
	}
	want := []string{"meter:delivered_minutes", "meter:storage_gb_seconds_cold"}
	for i, w := range want {
		if res.UsageLines[i].LineKey != w {
			t.Errorf("UsageLines[%d].LineKey = %q, want %q (ascending order)", i, res.UsageLines[i].LineKey, w)
		}
	}
}

// TestRate_CodecBreakdownFallsBackToCodecSeconds verifies that when Breakdowns
// is non-nil but has no entry for the media meter, rating falls back to the
// CodecSeconds map. An empty per-meter breakdown must not shadow CodecSeconds.
func TestRate_CodecBreakdownFallsBackToCodecSeconds(t *testing.T) {
	res, err := Rate(Input{
		Currency:  "EUR",
		BasePrice: dec("0"),
		Rules: []Rule{{
			Meter:     MeterMediaSeconds,
			Model:     ModelCodecMultiplier,
			Currency:  "EUR",
			UnitPrice: dec("0.001"),
			Config:    map[string]any{"codec_multipliers": map[string]any{"h264": 1.0}},
		}},
		// Non-nil Breakdowns that does NOT contain the media meter.
		Breakdowns: map[Meter]map[string]decimal.Decimal{
			Meter("some_other_meter"): {"h264": dec("3600")},
		},
		CodecSeconds: map[string]decimal.Decimal{"h264": dec("3600")}, // 60 min
	})
	if err != nil {
		t.Fatalf("Rate: %v", err)
	}
	line := findLine(t, res.UsageLines, "meter:media_seconds:codec:h264")
	if !line.Amount.Equal(dec("0.06")) { // 60 min × 0.001 × 1.0
		t.Errorf("h264 amount = %s, want 0.06 (CodecSeconds fallback)", line.Amount)
	}
}
