package rating

import (
	"testing"

	"github.com/shopspring/decimal"
)

func TestRateCarriesRatedUnitsOnEveryInvoiceLine(t *testing.T) {
	result, err := Rate(Input{
		Currency:  "EUR",
		BasePrice: dec("10"),
		Rules: []Rule{
			{Meter: MeterDeliveredMinutes, Model: ModelTieredGraduated, Currency: "EUR", UnitPrice: dec("0.01")},
			{Meter: MeterStorageGBSecondsCld, Model: ModelAllUsage, Currency: "EUR", UnitPrice: dec("0.03")},
		},
		Usage: map[Meter]decimal.Decimal{
			MeterDeliveredMinutes:    dec("60"),
			MeterStorageGBSecondsCld: dec("7200"),
		},
		Quantities: []DimensionedQuantity{
			{Meter: MeterDeliveredMinutes, Unit: "minute", Quantity: dec("60")},
			{Meter: MeterStorageGBSecondsCld, Unit: "gibibyte_second", Dimensions: map[string]string{"storage_scope": "cold"}, Quantity: dec("7200")},
		},
	})
	if err != nil {
		t.Fatalf("Rate: %v", err)
	}
	if result.BaseLine.Unit != "subscription" {
		t.Fatalf("base unit = %q", result.BaseLine.Unit)
	}
	units := map[Meter]string{}
	quantities := map[Meter]string{}
	for _, line := range result.UsageLines {
		units[line.Meter] = line.Unit
		quantities[line.Meter] = line.Quantity.String()
	}
	if units[MeterDeliveredMinutes] != "minute" {
		t.Errorf("delivered unit = %q", units[MeterDeliveredMinutes])
	}
	if units[MeterStorageGBSecondsCld] != "gibibyte_hour" || quantities[MeterStorageGBSecondsCld] != "2" {
		t.Errorf("storage = %s %s, want 2 gibibyte_hour", quantities[MeterStorageGBSecondsCld], units[MeterStorageGBSecondsCld])
	}
}

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

func TestRate_UnpricedDimensionedQuantityRemainsVisible(t *testing.T) {
	res, err := Rate(Input{
		Currency:  "EUR",
		BasePrice: dec("0"),
		Quantities: []DimensionedQuantity{{
			Meter: Meter("ai_inference_units"), Unit: "unit",
			Dimensions: map[string]string{"model": "future-model", "provider": "local"},
			Quantity:   dec("3600"),
		}},
	})
	if err != nil {
		t.Fatalf("Rate: %v", err)
	}
	line := findDimensionLine(t, res.UsageLines, "model", "future-model")
	if !line.Amount.IsZero() {
		t.Errorf("amount = %s, want 0 for an observed meter without a pricing rule", line.Amount)
	}
}
