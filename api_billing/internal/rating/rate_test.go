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
			// Cold S3 storage is the customer-facing storage product per
			// meter-contracts.md. Hot edge storage is operational (cache
			// optimization) and has no default rule.
			Meter: MeterStorageGBSecondsCld, Model: ModelAllUsage, Currency: "EUR",
			UnitPrice: dec("0.035000"),
		},
	}
}

func TestRate_Supporter_StorageNeverUsesRetentionDays(t *testing.T) {
	// Retention is an entitlement, not an allowance on the storage meter.
	// Storage is stored as GiB-seconds internally and rated as GiB-hours.
	// 100 GiB held for one hour = 360_000 GiB-seconds → 100 GiB-hours →
	// 100 × €0.035 = €3.50 (no retention-days subtraction).
	res, err := Rate(Input{
		Currency:  "EUR",
		BasePrice: dec("79.00"),
		Rules:     supporterRules(),
		Usage: map[Meter]decimal.Decimal{
			MeterStorageGBSecondsCld: dec("360000"), // 100 GB × 3600 s
		},
	})
	if err != nil {
		t.Fatalf("Rate: %v", err)
	}

	storage := findLine(t, res.UsageLines, "meter:storage_gb_seconds_cold")
	wantAmount := dec("3.50") // 100 GiB-hours × 0.035
	if !storage.Amount.Equal(wantAmount) {
		t.Errorf("storage amount = %s, want %s", storage.Amount, wantAmount)
	}
	if !storage.BillableQuantity.Equal(dec("100")) {
		t.Errorf("storage billable = %s GiB-hours, want 100", storage.BillableQuantity)
	}
}

func TestRate_Supporter_DeliveredMinutesSubtractsIncluded(t *testing.T) {
	// Included quantities are rating rules and reduce billable usage before
	// money is calculated. Supporter includes 120,000 delivered minutes.
	cases := []struct {
		name      string
		minutes   string
		wantUsage string // total UsageAmount
	}{
		{"under included", "60000", "0.000000"},
		{"at boundary", "120000", "0.000000"},
		{"two thousand over", "122000", "1.100000"}, // 2000 × 0.000550
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := Rate(Input{
				Currency:  "EUR",
				BasePrice: dec("79.00"),
				Rules:     supporterRules(),
				Usage: map[Meter]decimal.Decimal{
					MeterDeliveredMinutes: dec(tc.minutes),
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
			MeterDeliveredMinutes: dec("125000"), // 5000 over included
		},
	})
	if err != nil {
		t.Fatalf("Rate: %v", err)
	}
	if !res.BaseAmount.Equal(dec("79.00")) {
		t.Errorf("BaseAmount = %s, want 79.00", res.BaseAmount)
	}
	if !res.UsageAmount.Equal(dec("2.750000")) { // 5000 × 0.000550
		t.Errorf("UsageAmount = %s, want 2.750000", res.UsageAmount)
	}
	if !res.TotalAmount.Equal(dec("81.75")) {
		t.Errorf("TotalAmount = %s, want 81.75", res.TotalAmount)
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
		Meter: MeterMediaSeconds, Model: ModelCodecMultiplier, Currency: "EUR",
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
	h264 := findLine(t, res.UsageLines, "meter:media_seconds:codec:h264")
	if !h264.Amount.Equal(dec("0.06")) { // 60 min × 0.001 × 1.0
		t.Errorf("h264 amount = %s, want 0.06", h264.Amount)
	}
	if !h264.Quantity.Equal(dec("60")) {
		t.Errorf("h264 quantity = %s, want 60 (minutes)", h264.Quantity)
	}
	if !h264.BillableQuantity.Mul(h264.UnitPrice).Equal(h264.Amount) {
		t.Errorf("h264 audit invariant: %s × %s != %s", h264.BillableQuantity, h264.UnitPrice, h264.Amount)
	}
	av1 := findLine(t, res.UsageLines, "meter:media_seconds:codec:av1")
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

func TestRate_RejectsNegativePricesAndAllowances(t *testing.T) {
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
					Meter: MeterStorageGBSecondsHot, Model: ModelAllUsage, Currency: "EUR",
					UnitPrice: dec("-0.001"),
				}},
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

func TestRate_NegativeAllUsageCreatesCreditLine(t *testing.T) {
	res, err := Rate(Input{
		Currency:  "EUR",
		BasePrice: dec("0"),
		Rules: []Rule{{
			Meter:     MeterStorageGBSecondsCld,
			Model:     ModelAllUsage,
			Currency:  "EUR",
			UnitPrice: dec("0.035"),
		}},
		Usage: map[Meter]decimal.Decimal{MeterStorageGBSecondsCld: dec("-3600")},
	})
	if err != nil {
		t.Fatalf("Rate: %v", err)
	}
	line := findLine(t, res.UsageLines, "meter:storage_gb_seconds_cold")
	if !line.BillableQuantity.Equal(dec("-1")) {
		t.Errorf("billable = %s GiB-hours, want -1", line.BillableQuantity)
	}
	if !line.Amount.Equal(dec("-0.035")) {
		t.Errorf("amount = %s, want -0.035", line.Amount)
	}
}

func TestRate_NegativeGraduatedUsageCreatesCreditLine(t *testing.T) {
	res, err := Rate(Input{
		Currency:  "EUR",
		BasePrice: dec("0"),
		Rules: []Rule{{
			Meter:            MeterDeliveredMinutes,
			Model:            ModelTieredGraduated,
			Currency:         "EUR",
			IncludedQuantity: dec("120000"),
			UnitPrice:        dec("0.000550"),
		}},
		Usage: map[Meter]decimal.Decimal{MeterDeliveredMinutes: dec("-100")},
	})
	if err != nil {
		t.Fatalf("Rate: %v", err)
	}
	line := findLine(t, res.UsageLines, "meter:delivered_minutes")
	if !line.IncludedQuantity.IsZero() {
		t.Errorf("included = %s, want 0 for correction credit", line.IncludedQuantity)
	}
	if !line.BillableQuantity.Equal(dec("-100")) {
		t.Errorf("billable = %s, want -100", line.BillableQuantity)
	}
	if !line.Amount.Equal(dec("-0.055000")) {
		t.Errorf("amount = %s, want -0.055000", line.Amount)
	}
}

func TestRate_NegativeCodecBreakdownCreatesCreditLine(t *testing.T) {
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
		CodecSeconds: map[string]decimal.Decimal{"h264": dec("-60")},
	})
	if err != nil {
		t.Fatalf("Rate: %v", err)
	}
	line := findLine(t, res.UsageLines, "meter:media_seconds:codec:h264")
	if !line.Quantity.Equal(dec("-1")) {
		t.Errorf("quantity = %s minutes, want -1", line.Quantity)
	}
	if !line.Amount.Equal(dec("-0.001")) {
		t.Errorf("amount = %s, want -0.001", line.Amount)
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

func TestRate_CodecMultiplierRequiresMultipliers(t *testing.T) {
	_, err := Rate(Input{
		Currency:  "EUR",
		BasePrice: dec("0"),
		Rules: []Rule{{
			Meter:     MeterMediaSeconds,
			Model:     ModelCodecMultiplier,
			Currency:  "EUR",
			UnitPrice: dec("0.001"),
		}},
		CodecSeconds: map[string]decimal.Decimal{"h264": dec("60")},
	})
	if err == nil {
		t.Fatal("expected codec_multiplier config error")
	}
}

func TestRate_CustomMeterAndQuantityDivisor(t *testing.T) {
	res, err := Rate(Input{
		Currency:  "EUR",
		BasePrice: dec("0"),
		Rules: []Rule{{
			Meter:     Meter("ai_transcription_seconds"),
			Model:     ModelAllUsage,
			Currency:  "EUR",
			UnitPrice: dec("0.02"),
			Config:    map[string]any{"rated_quantity_divisor": "60", "description": "AI transcription"},
		}},
		Usage: map[Meter]decimal.Decimal{Meter("ai_transcription_seconds"): dec("180")},
	})
	if err != nil {
		t.Fatalf("Rate: %v", err)
	}
	line := findLine(t, res.UsageLines, "meter:ai_transcription_seconds")
	if line.Description != "AI transcription" {
		t.Errorf("description = %q, want AI transcription", line.Description)
	}
	if !line.Quantity.Equal(dec("3")) {
		t.Errorf("quantity = %s, want 3", line.Quantity)
	}
	if !line.Amount.Equal(dec("0.06")) {
		t.Errorf("amount = %s, want 0.06", line.Amount)
	}
}

func TestRate_CodecMultiplierUsesMeterScopedBreakdown(t *testing.T) {
	res, err := Rate(Input{
		Currency:  "EUR",
		BasePrice: dec("0"),
		Rules: []Rule{{
			Meter:     Meter("video_rendition_seconds"),
			Model:     ModelCodecMultiplier,
			Currency:  "EUR",
			UnitPrice: dec("0.10"),
			Config:    map[string]any{"codec_multipliers": map[string]any{"h264": 1.0, "av1": 3.0}},
		}},
		Usage: map[Meter]decimal.Decimal{Meter("video_rendition_seconds"): dec("240")},
		Breakdowns: map[Meter]map[string]decimal.Decimal{
			Meter("video_rendition_seconds"): {"h264": dec("120"), "av1": dec("60")},
		},
	})
	if err != nil {
		t.Fatalf("Rate: %v", err)
	}
	h264 := findLine(t, res.UsageLines, "meter:video_rendition_seconds:codec:h264")
	if !h264.Amount.Equal(dec("0.2")) {
		t.Errorf("h264 amount = %s, want 0.2", h264.Amount)
	}
	av1 := findLine(t, res.UsageLines, "meter:video_rendition_seconds:codec:av1")
	if !av1.Amount.Equal(dec("0.3")) {
		t.Errorf("av1 amount = %s, want 0.3", av1.Amount)
	}
}

func TestRate_Determinism(t *testing.T) {
	in := Input{
		Currency:  "EUR",
		BasePrice: dec("79.00"),
		Rules:     supporterRules(),
		Usage: map[Meter]decimal.Decimal{
			MeterDeliveredMinutes:    dec("200000"),
			MeterStorageGBSecondsCld: dec("360000"),
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

func TestRate_FreeTier_ZeroPricedUsageLine(t *testing.T) {
	res, err := Rate(Input{
		Currency:  "EUR",
		BasePrice: dec("0"),
		Rules: []Rule{{
			Meter:            MeterDeliveredMinutes,
			Model:            ModelTieredGraduated,
			Currency:         "EUR",
			IncludedQuantity: decimal.Zero,
			UnitPrice:        decimal.Zero,
		}},
		Usage: map[Meter]decimal.Decimal{MeterDeliveredMinutes: dec("5000")},
	})
	if err != nil {
		t.Fatalf("Rate: %v", err)
	}
	line := findLine(t, res.UsageLines, "meter:delivered_minutes")
	if !line.Quantity.Equal(dec("5000")) {
		t.Errorf("quantity = %s, want 5000", line.Quantity)
	}
	if !line.Amount.IsZero() {
		t.Errorf("line amount = %s, want 0", line.Amount)
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
