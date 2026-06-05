package rating

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

func TestHumanizeMeter(t *testing.T) {
	// Intent: turn a snake_case meter key into a title-cased label for
	// display when no friendlier name is known. It is a last-resort
	// fallback, so it must never panic and must title-case each segment.
	cases := []struct {
		in   Meter
		want string
	}{
		{"media_seconds", "Media Seconds"},
		{"egress_gb", "Egress Gb"},
		{"bandwidth", "Bandwidth"},
		{"", ""},
		// Leading/double underscores produce empty segments that survive the
		// join as blank words. This is cosmetic and not worth guarding, but
		// pinning it documents the behavior so a future change is deliberate.
		{"_lead", " Lead"},
		{"a__b", "A  B"},
	}
	for _, tc := range cases {
		if got := humanizeMeter(tc.in); got != tc.want {
			t.Errorf("humanizeMeter(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDescribeMeter(t *testing.T) {
	// A non-empty config description always wins (operator-authored label).
	r := Rule{Meter: MeterEgressGB, Config: map[string]any{"description": "  Custom egress  "}}
	if got := describeMeter(r); got != "Custom egress" {
		t.Errorf("config description should win and be trimmed, got %q", got)
	}

	// Whitespace-only description must NOT win — it falls through to the
	// canonical per-meter label.
	r = Rule{Meter: MeterEgressGB, Config: map[string]any{"description": "   "}}
	if got := describeMeter(r); got != "Bandwidth" {
		t.Errorf("blank description should fall through to canonical label, got %q", got)
	}

	// Known meters get curated names.
	known := map[Meter]string{
		MeterDeliveredMinutes:    "Delivered minutes",
		MeterEgressGB:            "Bandwidth",
		MeterStorageGBSecondsHot: "Hot storage",
		MeterStorageGBSecondsCld: "Cold storage",
		MeterMediaSeconds:        "Media processing",
	}
	for m, want := range known {
		if got := describeMeter(Rule{Meter: m}); got != want {
			t.Errorf("describeMeter(%q) = %q, want %q", m, got, want)
		}
	}

	// Unknown meter with no description falls back to humanizeMeter. This is
	// the path that keeps new marketplace meters self-describing without a
	// code change (per the Meter doc comment).
	if got := describeMeter(Rule{Meter: "ai_inference_seconds"}); got != "Ai Inference Seconds" {
		t.Errorf("unknown meter should humanize, got %q", got)
	}
}

func TestDecimalFromAny(t *testing.T) {
	cases := []struct {
		name   string
		in     any
		wantOK bool
		want   string // decimal string; only checked when wantOK
	}{
		{"decimal", decimal.RequireFromString("1.25"), true, "1.25"},
		{"float64", float64(2.5), true, "2.5"},
		{"float32", float32(0.5), true, "0.5"},
		{"int", 7, true, "7"},
		{"int64", int64(-3), true, "-3"},
		{"string valid", "10.5", true, "10.5"},
		{"string invalid", "not-a-number", false, ""},
		{"nil", nil, false, ""},
		{"unsupported type", []int{1}, false, ""},
		{"bool unsupported", true, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := decimalFromAny(tc.in)
			if ok != tc.wantOK {
				t.Fatalf("decimalFromAny(%v) ok = %v, want %v", tc.in, ok, tc.wantOK)
			}
			if !tc.wantOK {
				// Contract: failure returns decimal.Zero.
				if !got.IsZero() {
					t.Errorf("failed conversion should return zero, got %s", got)
				}
				return
			}
			if !got.Equal(decimal.RequireFromString(tc.want)) {
				t.Errorf("decimalFromAny(%v) = %s, want %s", tc.in, got, tc.want)
			}
		})
	}
}

func TestValidMeter(t *testing.T) {
	cases := []struct {
		in   Meter
		want bool
	}{
		{"delivered_minutes", true},
		{"a", true},
		{"egress_gb2", true},
		{"", false},        // empty
		{"Foo", false},     // must start lowercase letter
		{"1foo", false},    // must not start with a digit
		{"_foo", false},    // must not start with underscore
		{"foo-bar", false}, // hyphen not allowed
		{"foo bar", false}, // space not allowed
		{Meter(strings.Repeat("a", 64)), true},
		{Meter(strings.Repeat("a", 65)), false}, // length cap
	}
	for _, tc := range cases {
		if got := ValidMeter(tc.in); got != tc.want {
			t.Errorf("ValidMeter(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestValidateRuleShape(t *testing.T) {
	base := func() Rule {
		return Rule{Meter: MeterEgressGB, Model: ModelAllUsage, UnitPrice: dec("0.01")}
	}

	t.Run("valid all_usage", func(t *testing.T) {
		if err := ValidateRuleShape(base()); err != nil {
			t.Fatalf("expected valid, got %v", err)
		}
	})

	t.Run("invalid meter", func(t *testing.T) {
		r := base()
		r.Meter = "Bad-Meter"
		if err := ValidateRuleShape(r); err == nil {
			t.Fatal("expected error for invalid meter")
		}
	})

	t.Run("invalid model", func(t *testing.T) {
		r := base()
		r.Model = "made_up"
		if err := ValidateRuleShape(r); err == nil {
			t.Fatal("expected error for invalid model")
		}
	})

	t.Run("negative included quantity", func(t *testing.T) {
		r := base()
		r.IncludedQuantity = dec("-1")
		if err := ValidateRuleShape(r); err == nil {
			t.Fatal("expected error for negative included quantity")
		}
	})

	t.Run("negative unit price", func(t *testing.T) {
		r := base()
		r.UnitPrice = dec("-0.01")
		if err := ValidateRuleShape(r); err == nil {
			t.Fatal("expected error for negative unit price")
		}
	})

	t.Run("codec_multiplier requires multipliers", func(t *testing.T) {
		r := base()
		r.Model = ModelCodecMultiplier
		// No config at all.
		if err := ValidateRuleShape(r); err == nil {
			t.Fatal("expected error when codec_multipliers missing")
		}
		// Present but empty.
		r.Config = map[string]any{"codec_multipliers": map[string]any{}}
		if err := ValidateRuleShape(r); err == nil {
			t.Fatal("expected error when codec_multipliers empty")
		}
	})

	t.Run("codec_multiplier rejects bad entries", func(t *testing.T) {
		r := base()
		r.Model = ModelCodecMultiplier

		// Empty key.
		r.Config = map[string]any{"codec_multipliers": map[string]any{"": 2.0}}
		if err := ValidateRuleShape(r); err == nil {
			t.Fatal("expected error for empty codec key")
		}

		// Non-numeric multiplier.
		r.Config = map[string]any{"codec_multipliers": map[string]any{"h264": "fast"}}
		if err := ValidateRuleShape(r); err == nil {
			t.Fatal("expected error for non-numeric multiplier")
		}

		// Non-positive multiplier.
		r.Config = map[string]any{"codec_multipliers": map[string]any{"h264": 0.0}}
		if err := ValidateRuleShape(r); err == nil {
			t.Fatal("expected error for non-positive multiplier")
		}
	})

	t.Run("valid codec_multiplier", func(t *testing.T) {
		r := base()
		r.Model = ModelCodecMultiplier
		r.Config = map[string]any{"codec_multipliers": map[string]any{"h264": 1.0, "av1": 2.5}}
		if err := ValidateRuleShape(r); err != nil {
			t.Fatalf("expected valid codec_multiplier rule, got %v", err)
		}
	})
}
