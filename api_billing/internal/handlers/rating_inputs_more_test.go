package handlers

import (
	"testing"

	"github.com/shopspring/decimal"

	"frameworks/api_billing/internal/rating"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/models"
)

func TestNumericMapValues(t *testing.T) {
	// Native float map passes through unchanged.
	got := numericMapValues(map[string]float64{"h264": 1.5})
	if got["h264"] != 1.5 {
		t.Fatalf("float map: got %v", got)
	}

	// map[string]any coerces every supported numeric encoding; unsupported
	// types are silently dropped (not zero-filled).
	got = numericMapValues(map[string]any{
		"f":   float64(2),
		"i":   int(3),
		"i64": int64(4),
		"s":   "5.5",
		"bad": []int{1},
	})
	if got["f"] != 2 || got["i"] != 3 || got["i64"] != 4 || got["s"] != 5.5 {
		t.Fatalf("any map coercion wrong: %+v", got)
	}
	if _, ok := got["bad"]; ok {
		t.Fatalf("unsupported type should be dropped: %+v", got)
	}

	// Non-map input yields an empty map, never nil-deref.
	if len(numericMapValues("nope")) != 0 {
		t.Fatal("non-map should yield empty map")
	}
}

func TestAddCodecSecondsFromUsageDetails(t *testing.T) {
	// nil details: no-op.
	out := map[string]decimal.Decimal{}
	addCodecSecondsFromUsageDetails(out, nil, decimal.NewFromInt(5))
	if len(out) != 0 {
		t.Fatalf("nil details should add nothing: %+v", out)
	}

	// codec_seconds present: each entry added by key.
	out = map[string]decimal.Decimal{}
	addCodecSecondsFromUsageDetails(out, models.JSONB{
		"codec_seconds": map[string]any{"h264": float64(7), "transcode:h264": float64(7)},
	}, decimal.NewFromInt(99))
	if !out["h264"].Equal(decimal.NewFromInt(7)) || !out["transcode:h264"].Equal(decimal.NewFromInt(7)) {
		t.Fatalf("codec_seconds map not added: %+v", out)
	}

	// codec_seconds absent: fall back to output_codec (+ process_type compound key)
	// using the fallback quantity.
	out = map[string]decimal.Decimal{}
	addCodecSecondsFromUsageDetails(out, models.JSONB{
		"output_codec": "hevc",
		"process_type": "transcode",
	}, decimal.NewFromInt(12))
	if !out["hevc"].Equal(decimal.NewFromInt(12)) {
		t.Fatalf("fallback codec not added: %+v", out)
	}
	if !out["transcode:hevc"].Equal(decimal.NewFromInt(12)) {
		t.Fatalf("compound process:codec key not added: %+v", out)
	}
}

func TestAdjustmentDetailString(t *testing.T) {
	if adjustmentDetailString(nil, "k") != "" {
		t.Fatal("nil details -> empty")
	}
	if adjustmentDetailString(models.JSONB{"k": "v"}, "k") != "v" {
		t.Fatal("string value not returned")
	}
	// Non-string value returns empty, not a formatted number.
	if adjustmentDetailString(models.JSONB{"k": 5}, "k") != "" {
		t.Fatal("non-string value should be empty")
	}
}

func TestAdjustmentNestedDetailString(t *testing.T) {
	if adjustmentNestedDetailString(nil, "o", "i") != "" {
		t.Fatal("nil -> empty")
	}
	// map[string]any nesting.
	if got := adjustmentNestedDetailString(models.JSONB{"o": map[string]any{"i": "deep"}}, "o", "i"); got != "deep" {
		t.Fatalf("map[string]any nested: got %q", got)
	}
	// map[string]string nesting.
	if got := adjustmentNestedDetailString(models.JSONB{"o": map[string]string{"i": "deep2"}}, "o", "i"); got != "deep2" {
		t.Fatalf("map[string]string nested: got %q", got)
	}
	// Missing outer key.
	if adjustmentNestedDetailString(models.JSONB{"x": 1}, "o", "i") != "" {
		t.Fatal("missing outer -> empty")
	}
}

func TestCodecBreakdownsFromCluster(t *testing.T) {
	// rating.ValidMeter is a *syntactic* check (lowercase/digits/underscore), not
	// an allowlist — so only a malformed key like "Bad-Meter" is filtered here.
	in := map[string]map[string]float64{
		string(rating.MeterMediaSeconds): {"h264": 10, "hevc": 0}, // zero codec dropped
		"Bad-Meter":                      {"h264": 5},             // malformed meter dropped entirely
	}
	out := codecBreakdownsFromCluster(in)
	if _, ok := out["Bad-Meter"]; ok {
		t.Fatalf("malformed meter not filtered: %+v", out)
	}
	media := out[rating.MeterMediaSeconds]
	if media == nil || !media["h264"].Equal(decimal.NewFromInt(10)) {
		t.Fatalf("media_seconds h264 not mapped: %+v", media)
	}
	if _, ok := media["hevc"]; ok {
		t.Fatalf("zero-value codec should be dropped: %+v", media)
	}
}

func TestEmailPricingLabel(t *testing.T) {
	cases := []struct {
		source, kind, want string
	}{
		{"tier", "", "Subscription tier"},
		{"cluster_metered", "third_party_marketplace", "Marketplace metered"},
		{"cluster_metered", "private", "Cluster metered"},
		{"cluster_monthly", "", "Cluster monthly"},
		{"cluster_custom", "", "Custom contract"},
		{"free_unmetered", "", "Free (no charge)"},
		{"self_hosted", "", "Self-hosted (no charge)"},
		{"included_subscription", "", "Included in subscription"},
		{"beta_free", "", "Usage is on us during beta"},
		{"unknown_source", "", ""},
	}
	for _, tc := range cases {
		if got := emailPricingLabel(tc.source, tc.kind); got != tc.want {
			t.Fatalf("emailPricingLabel(%q,%q) = %q, want %q", tc.source, tc.kind, got, tc.want)
		}
	}
}

func TestTokenDecimals(t *testing.T) {
	for _, asset := range []string{"ETH", "LPT"} {
		if d, ok := TokenDecimals(asset); !ok || d != 18 {
			t.Fatalf("%s should be 18-decimal, got (%d,%v)", asset, d, ok)
		}
	}
	if d, ok := TokenDecimals("USDC"); !ok || d != 6 {
		t.Fatalf("USDC should be 6-decimal, got (%d,%v)", d, ok)
	}
	if d, ok := TokenDecimals("DOGE"); ok || d != 0 {
		t.Fatalf("unknown asset should be (0,false), got (%d,%v)", d, ok)
	}
}
