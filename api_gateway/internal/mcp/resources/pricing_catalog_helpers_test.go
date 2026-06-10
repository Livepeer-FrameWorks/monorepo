package resources

import (
	"testing"

	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
)

// ---- effectivePricingRules / mergePricingRule ----

func TestEffectivePricingRules_NoSubscription_ReturnsTierRules(t *testing.T) {
	tier := []*purserpb.PricingRule{{Meter: "egress", UnitPrice: "10"}}
	if got := effectivePricingRules(tier, nil); len(got) != 1 || got[0].UnitPrice != "10" {
		t.Fatalf("nil subscription should pass tier rules through unchanged, got %+v", got)
	}
	// A subscription with no overrides is also a no-op.
	empty := &purserpb.TenantSubscription{}
	if got := effectivePricingRules(tier, empty); len(got) != 1 || got[0].UnitPrice != "10" {
		t.Fatalf("empty overrides should be a no-op, got %+v", got)
	}
}

func TestEffectivePricingRules_OverridesAndExtras(t *testing.T) {
	tier := []*purserpb.PricingRule{
		{Meter: "egress", UnitPrice: "10", Currency: "USD"},
		{Meter: "storage", UnitPrice: "5"},
		nil, // must be skipped, not panic
	}
	sub := &purserpb.TenantSubscription{
		PricingOverrides: []*purserpb.PricingRule{
			{Meter: "egress", UnitPrice: "7"},    // overrides an existing meter
			{Meter: "transcode", UnitPrice: "3"}, // an extra meter not in the tier
			{Meter: "", UnitPrice: "99"},         // blank meter ignored
			nil,                                  // nil ignored
		},
	}
	got := effectivePricingRules(tier, sub)

	byMeter := map[string]*purserpb.PricingRule{}
	for _, r := range got {
		byMeter[r.Meter] = r
	}
	if byMeter["egress"].UnitPrice != "7" {
		t.Errorf("egress price should be overridden to 7, got %q", byMeter["egress"].UnitPrice)
	}
	if byMeter["egress"].Currency != "USD" {
		t.Errorf("override should keep the tier currency when not overridden, got %q", byMeter["egress"].Currency)
	}
	if byMeter["storage"].UnitPrice != "5" {
		t.Errorf("un-overridden storage rule should be untouched, got %q", byMeter["storage"].UnitPrice)
	}
	if _, ok := byMeter["transcode"]; !ok {
		t.Error("extra override meter 'transcode' should be appended")
	}
	if _, ok := byMeter[""]; ok {
		t.Error("blank-meter override must be ignored")
	}
}

func TestMergePricingRule(t *testing.T) {
	// base nil → override is returned verbatim.
	ov := &purserpb.PricingRule{Meter: "egress", UnitPrice: "7"}
	if got := mergePricingRule(nil, ov); got != ov {
		t.Fatalf("nil base should return override, got %+v", got)
	}

	base := &purserpb.PricingRule{Meter: "egress", Model: "tiered", Currency: "USD", UnitPrice: "10", ConfigJson: `{"a":1}`}
	// Only non-empty override fields win; empty override fields keep base.
	merged := mergePricingRule(base, &purserpb.PricingRule{UnitPrice: "7", ConfigJson: "{}"})
	if merged.UnitPrice != "7" {
		t.Errorf("unit price should be overridden, got %q", merged.UnitPrice)
	}
	if merged.Currency != "USD" || merged.Model != "tiered" {
		t.Errorf("empty override fields should keep base, got %+v", merged)
	}
	if merged.ConfigJson != `{"a":1}` {
		t.Errorf(`override ConfigJson "{}" must be treated as empty and keep base, got %q`, merged.ConfigJson)
	}
}

// ---- entryID / splitEntryID ----

func TestEntryIDRoundTrip(t *testing.T) {
	id := entryID("query", "streams.edges")
	op, path := splitEntryID(id)
	if op != "query" || path != "streams.edges" {
		t.Fatalf("round-trip failed: op=%q path=%q", op, path)
	}
	// A field path may itself contain a colon; only the first separator splits.
	op, path = splitEntryID("mutation:weird:path")
	if op != "mutation" || path != "weird:path" {
		t.Fatalf("split should only break on the first colon: op=%q path=%q", op, path)
	}
	// Malformed → empty pair.
	if op, path := splitEntryID("nocolon"); op != "" || path != "" {
		t.Fatalf("malformed id should yield empty pair, got (%q,%q)", op, path)
	}
}

// ---- includeCatalogPath ----

func TestIncludeCatalogPath(t *testing.T) {
	// curated always wins, even for otherwise-blocked or deep paths.
	if !includeCatalogPath("a.b.c.d.e.edges", true) {
		t.Error("curated path should always be included")
	}
	cases := map[string]bool{
		"streams":          true,
		"streams.title":    true,
		"streams.edges":    false, // connection plumbing
		"streams.pageInfo": false,
		"a.__typename":     false,
		"a.b.c.d.e":        false, // too deep (>4 segments)
		"a.b.c.d":          true,  // exactly 4 is allowed
	}
	for path, want := range cases {
		if got := includeCatalogPath(path, false); got != want {
			t.Errorf("includeCatalogPath(%q,false) = %v, want %v", path, got, want)
		}
	}
}

// ---- dedupeStrings ----

func TestDedupeStrings(t *testing.T) {
	if got := dedupeStrings(nil); got != nil {
		t.Errorf("empty input should yield nil, got %v", got)
	}
	got := dedupeStrings([]string{" a ", "a", "", "  ", "b", "a"})
	if len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("expected trimmed unique [a b], got %v", got)
	}
}
