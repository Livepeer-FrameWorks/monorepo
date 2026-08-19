package grpc

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"frameworks/api_billing/internal/rating"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
)

// clusterScopedLineKey produces the stable per-cluster invoice line identity.
// Two different clusters MUST yield different keys (a collision would merge or
// double-count invoice rows), the key must stay ≤128 chars, and it must be
// deterministic across calls.
func TestClusterScopedLineKey(t *testing.T) {
	const maxLen = 128

	t.Run("short key is the plain join", func(t *testing.T) {
		got := clusterScopedLineKey("meter:egress_gb", "eu-central-1", "202606")
		if got != "meter:egress_gb:eu-central-1:202606" {
			t.Errorf("got %q", got)
		}
		if got2 := clusterScopedLineKey("meter:egress_gb", "eu-central-1", "202606"); got2 != got {
			t.Errorf("not deterministic: %q vs %q", got, got2)
		}
	})

	t.Run("oversized key hashes the cluster id and stays bounded", func(t *testing.T) {
		longCluster := strings.Repeat("a", 130) // forces candidate > 128
		got := clusterScopedLineKey("m", longCluster, "202606")
		if len(got) > maxLen {
			t.Errorf("len = %d, want <= %d (%q)", len(got), maxLen, got)
		}
		if !strings.Contains(got, ":cluster-") || !strings.HasSuffix(got, ":202606") {
			t.Errorf("expected hashed cluster form ending in period, got %q", got)
		}
	})

	t.Run("distinct clusters never collide even on the hash path", func(t *testing.T) {
		a := clusterScopedLineKey("m", strings.Repeat("a", 130), "202606")
		b := clusterScopedLineKey("m", strings.Repeat("b", 130), "202606")
		if a == b {
			t.Errorf("distinct clusters produced identical line keys: %q", a)
		}
	})

	t.Run("oversized base key is truncated to fit", func(t *testing.T) {
		got := clusterScopedLineKey(strings.Repeat("x", 200), strings.Repeat("c", 130), "202606")
		if len(got) > maxLen {
			t.Errorf("len = %d, want <= %d", len(got), maxLen)
		}
		if !strings.HasSuffix(got, ":202606") {
			t.Errorf("truncated key must still end with the period suffix, got %q", got)
		}
	})
}

// buildRatingInputForUsage filters syntactically-invalid aggregate meters,
// preserves dimensioned quantities, and wires the beta waiver flag.
func TestBuildRatingInputForUsage(t *testing.T) {
	usage := map[string]float64{
		"egress_gb":     100,
		"media_seconds": 200,
		"Bad Meter!":    50, // syntactically invalid → dropped
	}
	quantities := []rating.DimensionedQuantity{{Meter: rating.MeterMediaSeconds, Unit: "second", Dimensions: map[string]string{"output_codec": "h264"}, Quantity: decimal.NewFromInt(100)}}

	in := buildRatingInputForUsage(usage, quantities, "EUR", decimal.NewFromInt(10), nil)

	if in.Currency != "EUR" || !in.BasePrice.Equal(decimal.NewFromInt(10)) {
		t.Errorf("currency/base wrong: %s / %s", in.Currency, in.BasePrice)
	}
	if len(in.Usage) != 2 {
		t.Errorf("usage should drop the invalid meter, got %v", in.Usage)
	}
	if !in.Usage[rating.MeterEgressGB].Equal(decimal.NewFromFloat(100)) {
		t.Errorf("egress usage = %s, want 100", in.Usage[rating.MeterEgressGB])
	}
	if len(in.Quantities) != 1 || in.Quantities[0].Dimensions["output_codec"] != "h264" {
		t.Errorf("dimensioned quantity not preserved: %#v", in.Quantities)
	}
	if in.WaiveUsageCharges != config.WaiveUsageChargesEnabled() {
		t.Errorf("WaiveUsageCharges = %v, want config value", in.WaiveUsageCharges)
	}
}

// lineItemToProto must serialize every decimal as a string to preserve
// precision and carry the stable line_key and meter through verbatim.
func TestLineItemToProto(t *testing.T) {
	li := rating.LineItem{
		LineKey:          "meter:egress_gb:eu:202606",
		Meter:            rating.MeterEgressGB,
		Description:      "Egress",
		Quantity:         decimal.RequireFromString("150.5"),
		IncludedQuantity: decimal.RequireFromString("100"),
		BillableQuantity: decimal.RequireFromString("50.5"),
		UnitPrice:        decimal.RequireFromString("0.05"),
		Amount:           decimal.RequireFromString("2.53"),
		Currency:         "EUR",
		Unit:             "second",
		Dimensions:       map[string]string{"execution_backend": "livepeer_network", "output_codec": "h264"},
	}
	p := lineItemToProto(li)
	if p.GetLineKey() != "meter:egress_gb:eu:202606" || p.GetMeter() != "egress_gb" || p.GetDescription() != "Egress" {
		t.Errorf("identity fields wrong: %+v", p)
	}
	if p.GetQuantity() != "150.5" || p.GetIncludedQuantity() != "100" || p.GetBillableQuantity() != "50.5" {
		t.Errorf("quantity strings wrong: %+v", p)
	}
	if p.GetUnitPrice() != "0.05" || p.GetTotal() != "2.53" || p.GetCurrency() != "EUR" {
		t.Errorf("price/total/currency wrong: %+v", p)
	}
	if p.GetUnit() != "second" || p.GetDimensions().AsMap()["output_codec"] != "h264" {
		t.Errorf("unit/dimensions wrong: %+v", p)
	}

	// base_subscription line: empty meter must serialize as "".
	if p := lineItemToProto(rating.LineItem{LineKey: "base_subscription"}); p.GetMeter() != "" {
		t.Errorf("base line meter = %q, want empty", p.GetMeter())
	}
}
