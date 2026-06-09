package grpc

import (
	"context"
	"testing"
	"time"

	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
)

func TestMapToProtoStruct(t *testing.T) {
	if got := mapToProtoStruct(nil); got != nil {
		t.Fatalf("nil map should yield nil Struct, got %+v", got)
	}

	got := mapToProtoStruct(map[string]any{"a": "x", "n": float64(3)})
	if got == nil {
		t.Fatal("valid map yielded nil Struct")
	}
	fields := got.GetFields()
	if fields["a"].GetStringValue() != "x" {
		t.Fatalf("field a = %q, want x", fields["a"].GetStringValue())
	}
	if fields["n"].GetNumberValue() != 3 {
		t.Fatalf("field n = %v, want 3", fields["n"].GetNumberValue())
	}

	// structpb.NewStruct rejects non-JSON values; the helper swallows the
	// error and returns nil rather than propagating it.
	if got := mapToProtoStruct(map[string]any{"bad": make(chan int)}); got != nil {
		t.Fatalf("unsupported value should yield nil, got %+v", got)
	}
}

func TestJSONToMap(t *testing.T) {
	got := jsonToMap([]byte(`{"k":"v","n":1}`))
	if got["k"] != "v" {
		t.Fatalf("k = %v, want v", got["k"])
	}

	// Invalid JSON must return a non-nil empty map: callers index into it
	// without a nil check.
	for _, in := range [][]byte{nil, []byte(""), []byte("not json"), []byte("[1,2]")} {
		got := jsonToMap(in)
		if got == nil {
			t.Fatalf("jsonToMap(%q) returned nil, want empty map", in)
		}
		if len(got) != 0 {
			t.Fatalf("jsonToMap(%q) = %v, want empty map", in, got)
		}
	}
}

func TestSQLNullString(t *testing.T) {
	empty := ""
	val := "abc"
	cases := []struct {
		name      string
		in        *string
		wantValid bool
		wantStr   string
	}{
		{"nil", nil, false, ""},
		{"empty", &empty, false, ""},
		{"value", &val, true, "abc"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := sqlNullString(c.in)
			if got.Valid != c.wantValid || got.String != c.wantStr {
				t.Fatalf("got {%q,%v}, want {%q,%v}", got.String, got.Valid, c.wantStr, c.wantValid)
			}
		})
	}
}

func TestDerefString(t *testing.T) {
	if got := derefString(nil); got != "" {
		t.Fatalf("nil deref = %q, want empty", got)
	}
	v := "hello"
	if got := derefString(&v); got != "hello" {
		t.Fatalf("deref = %q, want hello", got)
	}
}

func TestCurrentBillingPeriod(t *testing.T) {
	// Mid-month input: period must snap to the 1st at midnight and run to the
	// 1st of the next month.
	now := time.Date(2026, time.June, 17, 13, 45, 0, 0, time.UTC)
	start, end := currentBillingPeriod(now)
	if !start.Equal(time.Date(2026, time.June, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("start = %v, want 2026-06-01T00:00:00Z", start)
	}
	if !end.Equal(time.Date(2026, time.July, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("end = %v, want 2026-07-01T00:00:00Z", end)
	}
	if !end.After(start) {
		t.Fatal("end must be after start")
	}

	// Year boundary: December rolls into the next January.
	start, end = currentBillingPeriod(time.Date(2026, time.December, 31, 23, 0, 0, 0, time.UTC))
	if !start.Equal(time.Date(2026, time.December, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("Dec start = %v", start)
	}
	if !end.Equal(time.Date(2027, time.January, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("Dec end = %v, want 2027-01-01", end)
	}
}

func TestDefaultNetworkForAsset(t *testing.T) {
	cases := map[string]string{
		"ETH":  "arbitrum",
		"USDC": "arbitrum",
		"BTC":  "",
		"eth":  "", // case-sensitive
		"":     "",
	}
	for asset, want := range cases {
		if got := defaultNetworkForAsset(asset); got != want {
			t.Fatalf("defaultNetworkForAsset(%q) = %q, want %q", asset, got, want)
		}
	}
}

func TestGetMolliePaymentMethod(t *testing.T) {
	if got := getMolliePaymentMethod("ideal"); string(got) != "ideal" {
		t.Fatalf("got %q, want ideal", got)
	}
}

func TestHasArbitrumExplorerKey(t *testing.T) {
	t.Setenv("ARBISCAN_API_KEY", "")
	if hasArbitrumExplorerKey() {
		t.Fatal("empty key should report false")
	}
	t.Setenv("ARBISCAN_API_KEY", "abc123")
	if !hasArbitrumExplorerKey() {
		t.Fatal("set key should report true")
	}
}

func TestPricingLabelFor(t *testing.T) {
	cases := []struct {
		source, kind, want string
	}{
		{"tier", "", "Subscription tier"},
		{"cluster_metered", "third_party_marketplace", "Marketplace metered"},
		{"cluster_metered", "platform_official", "Cluster metered"},
		{"cluster_monthly", "", "Cluster monthly"},
		{"cluster_custom", "", "Custom contract"},
		{"free_unmetered", "", "Free (no charge)"},
		{"self_hosted", "", "Self-hosted (no charge)"},
		{"included_subscription", "", "Included in subscription"},
		{"beta_free", "", "Usage is on us during beta"},
		{"unknown_source", "", ""},
	}
	for _, c := range cases {
		if got := pricingLabelFor(c.source, c.kind); got != c.want {
			t.Fatalf("pricingLabelFor(%q,%q) = %q, want %q", c.source, c.kind, got, c.want)
		}
	}
}

func TestApplyEligibility(t *testing.T) {
	// nil pricing is a no-op (must not panic).
	applyEligibility("tenant", 1, nil)

	t.Run("empty tenant is always eligible", func(t *testing.T) {
		p := &purserpb.ClusterPricing{RequiredTierLevel: 5, DenialReason: new("stale")}
		applyEligibility("", 0, p)
		if !p.IsEligible || p.DenialReason != nil {
			t.Fatalf("empty tenant: eligible=%v reason=%v", p.IsEligible, p.DenialReason)
		}
	})

	t.Run("tier below required is denied", func(t *testing.T) {
		p := &purserpb.ClusterPricing{RequiredTierLevel: 3}
		applyEligibility("tenant", 2, p)
		if p.IsEligible || p.DenialReason == nil {
			t.Fatalf("low tier should be ineligible, got eligible=%v", p.IsEligible)
		}
	})

	t.Run("platform-official paid-only blocks free tier", func(t *testing.T) {
		p := &purserpb.ClusterPricing{IsPlatformOfficial: true, AllowFreeTier: false}
		applyEligibility("tenant", 0, p)
		if p.IsEligible || p.DenialReason == nil {
			t.Fatalf("free tier on paid-only cluster should be denied")
		}
	})

	t.Run("eligible clears a prior denial", func(t *testing.T) {
		p := &purserpb.ClusterPricing{RequiredTierLevel: 1, DenialReason: new("stale")}
		applyEligibility("tenant", 2, p)
		if !p.IsEligible || p.DenialReason != nil {
			t.Fatalf("should be eligible with reason cleared, got eligible=%v reason=%v", p.IsEligible, p.DenialReason)
		}
	})
}

func TestApplyCommercialEligibilityShortCircuits(t *testing.T) {
	s := &PurserServer{}

	// An already-denied pricing must be returned untouched; reaching the
	// quartermaster client (nil here) would panic, proving the short-circuit.
	denied := &purserpb.ClusterPricing{IsEligible: false, DenialReason: new("blocked")}
	s.applyCommercialEligibility(context.Background(), "tenant", 1, denied)
	if denied.IsEligible || denied.GetDenialReason() != "blocked" {
		t.Fatalf("pre-denied pricing was mutated: %+v", denied)
	}

	// Empty tenant returns after applyEligibility, before any cluster
	// classification (which would dereference the nil quartermaster client).
	open := &purserpb.ClusterPricing{ClusterId: "c1"}
	s.applyCommercialEligibility(context.Background(), "", 0, open)
	if !open.IsEligible {
		t.Fatalf("empty tenant should be eligible without classification")
	}
}

func TestValidatePricingOverrideRuleEdgeCases(t *testing.T) {
	// Branches not exercised by TestValidatePricingOverrideRule in
	// create_subscription_test.go: nil rule, an explicitly negative (vs
	// merely non-numeric) included_quantity, and a non-letter currency of the
	// correct length.
	cases := []struct {
		name    string
		rule    *purserpb.PricingRule
		wantErr bool
	}{
		{"nil rule", nil, true},
		{"negative included quantity", &purserpb.PricingRule{Meter: "delivered_minutes", Model: "all_usage", IncludedQuantity: "-1", UnitPrice: "1", ConfigJson: "{}"}, true},
		{"currency right length non-letter", &purserpb.PricingRule{Meter: "delivered_minutes", Model: "all_usage", Currency: "E1R", UnitPrice: "1", ConfigJson: "{}"}, true},
		{"currency lowercase normalizes ok", &purserpb.PricingRule{Meter: "delivered_minutes", Model: "all_usage", Currency: "eur", UnitPrice: "1", ConfigJson: "{}"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validatePricingOverrideRule(c.rule)
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, c.wantErr)
			}
		})
	}
}

func TestAsPGUUIDArray(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{nil, "{}"},
		{[]string{}, "{}"},
		{[]string{"a"}, "{a}"},
		{[]string{"a", "b", "c"}, "{a,b,c}"},
	}
	for _, c := range cases {
		if got := asPGUUIDArray(c.in); got != c.want {
			t.Fatalf("asPGUUIDArray(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestBillingOutboxConfig(t *testing.T) {
	cfg := billingOutboxConfig()
	if cfg.BatchSize != billingOutboxBatchSize {
		t.Fatalf("BatchSize = %d, want %d", cfg.BatchSize, billingOutboxBatchSize)
	}
	if cfg.BaseBackoff != billingOutboxBaseBackoff || cfg.MaxBackoff != billingOutboxMaxBackoff {
		t.Fatalf("backoff config not wired: %+v", cfg)
	}
	if cfg.PollPeriod != billingOutboxPollPeriod || cfg.Lease != billingOutboxLease {
		t.Fatalf("poll/lease config not wired: %+v", cfg)
	}
	if cfg.AlertAfterAttempts != billingOutboxAlertAfterAttempts {
		t.Fatalf("AlertAfterAttempts = %d, want %d", cfg.AlertAfterAttempts, billingOutboxAlertAfterAttempts)
	}
}

func TestJoinAndClauses(t *testing.T) {
	cases := []struct {
		in   []string
		want string
	}{
		{nil, "true"},
		{[]string{"a = 1"}, "a = 1"},
		{[]string{"a = 1", "b = 2", "c = 3"}, "a = 1 AND b = 2 AND c = 3"},
	}
	for _, c := range cases {
		if got := joinAndClauses(c.in); got != c.want {
			t.Fatalf("joinAndClauses(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}
