package rating

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"
)

// dec is a brevity helper for tests.
func dec(s string) decimal.Decimal {
	d, err := decimal.NewFromString(s)
	if err != nil {
		panic(err)
	}
	return d
}

// supporterRules mirrors the Supporter tier from billing_tiers.yaml.
func supporterRules() []Rule {
	return []Rule{
		{
			Meter: MeterDeliveredMinutes, Model: ModelTieredGraduated, Currency: "EUR",
			IncludedQuantity: dec("120000"), UnitPrice: dec("0.000550"),
		},
		{
			Meter: MeterAverageStorageGB, Model: ModelAllUsage, Currency: "EUR",
			UnitPrice: dec("0.035000"),
		},
		{
			Meter: MeterAIGPUHours, Model: ModelTieredGraduated, Currency: "EUR",
			IncludedQuantity: dec("10"), UnitPrice: dec("1.500000"),
		},
	}
}

func TestRate_Supporter_StorageNeverUsesRetentionDays(t *testing.T) {
	// Retention is an entitlement, not an allowance on the storage meter.
	// 100 GB stored must bill 100 × €0.035, not 10 × €0.035.
	res, err := Rate(Input{
		Currency:  "EUR",
		BasePrice: dec("79.00"),
		Rules:     supporterRules(),
		Usage: map[Meter]decimal.Decimal{
			MeterAverageStorageGB: dec("100"),
		},
	})
	if err != nil {
		t.Fatalf("Rate: %v", err)
	}

	storage := findLine(t, res.UsageLines, "meter:average_storage_gb")
	wantAmount := dec("3.50") // 100 × 0.035
	if !storage.Amount.Equal(wantAmount) {
		t.Errorf("storage amount = %s, want %s", storage.Amount, wantAmount)
	}
	if !storage.BillableQuantity.Equal(dec("100")) {
		t.Errorf("storage billable = %s, want 100 (no retention-days subtraction)", storage.BillableQuantity)
	}
}

func TestRate_Supporter_GPUSubtractsIncluded(t *testing.T) {
	// Included quantities are rating rules and reduce billable usage before
	// money is calculated. Supporter includes 10 GPU hours.
	cases := []struct {
		name      string
		gpuHours  string
		wantUsage string // total UsageAmount
	}{
		{"under included", "5", "0.00"},
		{"at boundary", "10", "0.00"},
		{"two hours over", "12", "3.00"}, // 2 × 1.50
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Rate(Input{
				Currency:  "EUR",
				BasePrice: dec("79.00"),
				Rules:     supporterRules(),
				Usage: map[Meter]decimal.Decimal{
					MeterAIGPUHours: dec(tc.gpuHours),
				},
			})
			if err != nil {
				t.Fatalf("Rate: %v", err)
			}
			if !res.UsageAmount.Equal(dec(tc.wantUsage)) {
				t.Errorf("usage amount = %s, want %s", res.UsageAmount, tc.wantUsage)
			}
		})
	}
}

func TestRate_BaseAndUsageSplit(t *testing.T) {
	// Prepaid deduction must read UsageAmount only — never TotalAmount —
	// otherwise per-event deductions re-charge the monthly base fee.
	res, err := Rate(Input{
		Currency:  "EUR",
		BasePrice: dec("79.00"),
		Rules:     supporterRules(),
		Usage: map[Meter]decimal.Decimal{
			MeterAIGPUHours: dec("12"),
		},
	})
	if err != nil {
		t.Fatalf("Rate: %v", err)
	}
	if !res.BaseAmount.Equal(dec("79.00")) {
		t.Errorf("BaseAmount = %s, want 79.00", res.BaseAmount)
	}
	if !res.UsageAmount.Equal(dec("3.00")) {
		t.Errorf("UsageAmount = %s, want 3.00", res.UsageAmount)
	}
	if !res.TotalAmount.Equal(dec("82.00")) {
		t.Errorf("TotalAmount = %s, want 82.00", res.TotalAmount)
	}
	if res.BaseLine.LineKey != LineKeyBaseSubscription {
		t.Errorf("BaseLine.LineKey = %q, want %q", res.BaseLine.LineKey, LineKeyBaseSubscription)
	}
}

func TestRate_KeepsSubCentPrecision(t *testing.T) {
	res, err := Rate(Input{
		Currency:  "EUR",
		BasePrice: dec("0"),
		Rules: []Rule{{
			Meter:            MeterDeliveredMinutes,
			Model:            ModelTieredGraduated,
			Currency:         "EUR",
			IncludedQuantity: decimal.Zero,
			UnitPrice:        dec("0.000550"),
		}},
		Usage: map[Meter]decimal.Decimal{
			MeterDeliveredMinutes: dec("1"),
		},
	})
	if err != nil {
		t.Fatalf("Rate: %v", err)
	}
	if !res.UsageAmount.Equal(dec("0.000550")) {
		t.Errorf("UsageAmount = %s, want 0.000550 (prepaid micro-cent carry needs unrounded rating output)", res.UsageAmount)
	}
	line := findLine(t, res.UsageLines, "meter:delivered_minutes")
	if !line.Amount.Equal(dec("0.000550")) {
		t.Errorf("line amount = %s, want 0.000550", line.Amount)
	}
}

func TestRate_DeliveredMinutesGraduated(t *testing.T) {
	// 200,000 delivered minutes on Supporter: 80,000 billable × 0.00055 = 44.00
	res, err := Rate(Input{
		Currency:  "EUR",
		BasePrice: dec("79.00"),
		Rules:     supporterRules(),
		Usage: map[Meter]decimal.Decimal{
			MeterDeliveredMinutes: dec("200000"),
		},
	})
	if err != nil {
		t.Fatalf("Rate: %v", err)
	}
	dm := findLine(t, res.UsageLines, "meter:delivered_minutes")
	if !dm.Amount.Equal(dec("44.00")) {
		t.Errorf("delivered_minutes amount = %s, want 44.00", dm.Amount)
	}
	if !dm.IncludedQuantity.Equal(dec("120000")) {
		t.Errorf("included = %s, want 120000", dm.IncludedQuantity)
	}
	if !dm.BillableQuantity.Equal(dec("80000")) {
		t.Errorf("billable = %s, want 80000", dm.BillableQuantity)
	}
}

func TestRate_CodecMultiplier_PerCodecLineItems(t *testing.T) {
	rules := []Rule{{
		Meter: MeterProcessingSeconds, Model: ModelCodecMultiplier, Currency: "EUR",
		UnitPrice: dec("0.001"),
		Config: map[string]any{
			"codec_multipliers": map[string]any{
				"h264": 1.0, "hevc": 1.5, "vp9": 1.5, "av1": 2.0,
			},
		},
	}}
	res, err := Rate(Input{
		Currency:  "EUR",
		BasePrice: dec("0"),
		Rules:     rules,
		CodecSeconds: map[string]decimal.Decimal{
			"h264": dec("3600"), // 60 min
			"av1":  dec("3600"), // 60 min
		},
	})
	if err != nil {
		t.Fatalf("Rate: %v", err)
	}
	if got, want := len(res.UsageLines), 2; got != want {
		t.Fatalf("usage lines = %d, want %d", got, want)
	}
	h264 := findLine(t, res.UsageLines, "meter:processing_seconds:codec:h264")
	if !h264.Amount.Equal(dec("0.06")) { // 60 min × 0.001 × 1.0
		t.Errorf("h264 amount = %s, want 0.06", h264.Amount)
	}
	if !h264.Quantity.Equal(dec("60")) {
		t.Errorf("h264 quantity = %s, want 60 (minutes)", h264.Quantity)
	}
	if !h264.BillableQuantity.Mul(h264.UnitPrice).Equal(h264.Amount) {
		t.Errorf("h264 audit invariant: %s × %s != %s", h264.BillableQuantity, h264.UnitPrice, h264.Amount)
	}
	av1 := findLine(t, res.UsageLines, "meter:processing_seconds:codec:av1")
	if !av1.Amount.Equal(dec("0.12")) { // 60 min × 0.001 × 2.0
		t.Errorf("av1 amount = %s, want 0.12", av1.Amount)
	}
	if !av1.UnitPrice.Equal(dec("0.002")) { // 0.001 * 2.0 multiplier
		t.Errorf("av1 effective unit_price = %s, want 0.002", av1.UnitPrice)
	}
}

func TestRate_CurrencyMismatchRejected(t *testing.T) {
	rules := []Rule{{
		Meter: MeterDeliveredMinutes, Model: ModelTieredGraduated, Currency: "USD",
		UnitPrice: dec("0.001"),
	}}
	_, err := Rate(Input{
		Currency:  "EUR",
		BasePrice: dec("0"),
		Rules:     rules,
		Usage:     map[Meter]decimal.Decimal{MeterDeliveredMinutes: dec("1000")},
	})
	if !errors.Is(err, ErrCurrencyMismatch) {
		t.Errorf("err = %v, want ErrCurrencyMismatch", err)
	}
}

func TestRate_EmptyRuleCurrencyRejected(t *testing.T) {
	rules := []Rule{{
		Meter: MeterDeliveredMinutes, Model: ModelTieredGraduated,
		UnitPrice: dec("0.001"),
	}}
	_, err := Rate(Input{
		Currency:  "EUR",
		BasePrice: dec("0"),
		Rules:     rules,
		Usage:     map[Meter]decimal.Decimal{MeterDeliveredMinutes: dec("1000")},
	})
	if err == nil {
		t.Fatal("err = nil, want empty rule currency error")
	}
}

func TestRate_RejectsNegativeInputs(t *testing.T) {
	cases := []struct {
		name string
		in   Input
	}{
		{
			name: "negative base price",
			in: Input{
				Currency:  "EUR",
				BasePrice: dec("-1"),
			},
		},
		{
			name: "negative usage",
			in: Input{
				Currency:  "EUR",
				BasePrice: dec("0"),
				Rules:     supporterRules(),
				Usage:     map[Meter]decimal.Decimal{MeterAverageStorageGB: dec("-1")},
			},
		},
		{
			name: "negative included quantity",
			in: Input{
				Currency:  "EUR",
				BasePrice: dec("0"),
				Rules: []Rule{{
					Meter: MeterDeliveredMinutes, Model: ModelTieredGraduated, Currency: "EUR",
					IncludedQuantity: dec("-1"), UnitPrice: dec("0.001"),
				}},
			},
		},
		{
			name: "negative unit price",
			in: Input{
				Currency:  "EUR",
				BasePrice: dec("0"),
				Rules: []Rule{{
					Meter: MeterAverageStorageGB, Model: ModelAllUsage, Currency: "EUR",
					UnitPrice: dec("-0.001"),
				}},
			},
		},
		{
			name: "negative codec seconds",
			in: Input{
				Currency:  "EUR",
				BasePrice: dec("0"),
				Rules: []Rule{{
					Meter: MeterProcessingSeconds, Model: ModelCodecMultiplier, Currency: "EUR",
					UnitPrice: dec("0.001"),
					Config:    map[string]any{"codec_multipliers": map[string]any{"h264": 1.0}},
				}},
				CodecSeconds: map[string]decimal.Decimal{"h264": dec("-60")},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Rate(tc.in); err == nil {
				t.Fatal("Rate err = nil, want validation error")
			}
		})
	}
}

func TestRate_UnknownModelRejected(t *testing.T) {
	rules := []Rule{{
		Meter: MeterDeliveredMinutes, Model: "tiered_volume", Currency: "EUR",
		UnitPrice: dec("0.001"),
	}}
	_, err := Rate(Input{
		Currency:  "EUR",
		BasePrice: dec("0"),
		Rules:     rules,
		Usage:     map[Meter]decimal.Decimal{MeterDeliveredMinutes: dec("1000")},
	})
	if !errors.Is(err, ErrUnknownModel) {
		t.Errorf("err = %v, want ErrUnknownModel", err)
	}
}

func TestRate_Determinism(t *testing.T) {
	in := Input{
		Currency:  "EUR",
		BasePrice: dec("79.00"),
		Rules:     supporterRules(),
		Usage: map[Meter]decimal.Decimal{
			MeterDeliveredMinutes: dec("200000"),
			MeterAverageStorageGB: dec("100"),
			MeterAIGPUHours:       dec("12"),
		},
	}
	first, err := Rate(in)
	if err != nil {
		t.Fatalf("Rate: %v", err)
	}
	for i := range 5 {
		got, err := Rate(in)
		if err != nil {
			t.Fatalf("Rate iter %d: %v", i, err)
		}
		if !got.TotalAmount.Equal(first.TotalAmount) {
			t.Errorf("iter %d: total = %s, want %s", i, got.TotalAmount, first.TotalAmount)
		}
		if len(got.UsageLines) != len(first.UsageLines) {
			t.Errorf("iter %d: usage line count drifted", i)
		}
		for j, line := range got.UsageLines {
			if line.LineKey != first.UsageLines[j].LineKey {
				t.Errorf("iter %d line %d: key %q vs %q", i, j, line.LineKey, first.UsageLines[j].LineKey)
			}
		}
	}
}

func TestRate_FreeTier_ZeroAllUsage(t *testing.T) {
	// Free tier: metering disabled equivalent — no rules. Only base.
	res, err := Rate(Input{
		Currency:  "EUR",
		BasePrice: dec("0"),
		Rules:     nil,
		Usage:     map[Meter]decimal.Decimal{MeterDeliveredMinutes: dec("5000")},
	})
	if err != nil {
		t.Fatalf("Rate: %v", err)
	}
	if !res.UsageAmount.IsZero() {
		t.Errorf("UsageAmount = %s, want 0", res.UsageAmount)
	}
	if !res.TotalAmount.IsZero() {
		t.Errorf("TotalAmount = %s, want 0", res.TotalAmount)
	}
}

func findLine(t *testing.T, lines []LineItem, key string) LineItem {
	t.Helper()
	for _, l := range lines {
		if l.LineKey == key {
			return l
		}
	}
	t.Fatalf("line %q not found in %d lines", key, len(lines))
	return LineItem{}
}
