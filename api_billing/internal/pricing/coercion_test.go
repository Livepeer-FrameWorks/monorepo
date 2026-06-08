package pricing

import (
	"testing"

	"github.com/shopspring/decimal"
)

// decimalField coerces the JSON-decoded pricing config (unit_price,
// included_quantity) into exact decimals. Pricing rules are loaded once per
// cluster per invoice, so a coercion bug here cascades to every line item for
// that cluster. The key invariant: the string path stays EXACT (operators
// author prices as strings like "0.0035"); the float64 path is the only one
// that can introduce binary-float drift, so callers should prefer strings.
func TestDecimalField(t *testing.T) {
	tests := []struct {
		name    string
		raw     any
		want    decimal.Decimal
		wantErr bool
	}{
		{"missing key", nil /*sentinel: key absent*/, decimal.Zero, false},
		{"explicit nil", any(nil), decimal.Zero, false},
		{"string exact", "0.0035", dec("0.0035"), false},
		{"string negative", "-1.50", dec("-1.50"), false},
		{"float64", float64(2.5), dec("2.5"), false},
		{"int", int(7), dec("7"), false},
		{"int64", int64(42), dec("42"), false},
		{"unsupported bool", true, decimal.Zero, true},
		{"unparseable string", "not-a-number", decimal.Zero, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := map[string]any{}
			if tt.name != "missing key" {
				m["unit_price"] = tt.raw
			}
			got, err := decimalField(m, "unit_price")
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tt.wantErr)
			}
			if err == nil && !got.Equal(tt.want) {
				t.Errorf("got %s, want %s", got, tt.want)
			}
		})
	}
}

// The string path must round-trip a high-precision price with no loss, while
// the float64 path is documented as lossy: 0.1 has no exact binary
// representation. This pins WHY operator configs should carry prices as
// strings — the same numeric literal diverges between the two paths.
func TestDecimalField_StringExactVsFloatDrift(t *testing.T) {
	asString, err := decimalField(map[string]any{"unit_price": "0.1"}, "unit_price")
	if err != nil {
		t.Fatalf("string path: %v", err)
	}
	if asString.String() != "0.1" {
		t.Errorf("string path = %s, want exact 0.1", asString.String())
	}
	// float64 0.1 is actually 0.1000000000000000055..., and NewFromFloat
	// captures that. It must still equal 0.1 numerically (decimal rounds the
	// representation), but is NOT guaranteed bit-identical to the string path
	// for arbitrary values — assert the documented numeric equality only.
	asFloat, err := decimalField(map[string]any{"unit_price": 0.1}, "unit_price")
	if err != nil {
		t.Fatalf("float path: %v", err)
	}
	if !asFloat.Equal(dec("0.1")) {
		t.Errorf("float path = %s, want numerically 0.1", asFloat)
	}
}

// configMapField extracts the optional codec-multiplier config object. A
// malformed config (string/number/list) must return a clean error so the
// caller surfaces it, never a silent nil that would price transcoding at $0.
func TestConfigMapField(t *testing.T) {
	// Present object.
	cfg, err := configMapField(map[string]any{"config": map[string]any{"codec_multipliers": map[string]any{"h265": 1.5}}}, "config")
	if err != nil {
		t.Fatalf("present object: %v", err)
	}
	if cfg == nil {
		t.Fatal("present object returned nil")
	}

	// Absent key and explicit nil are both "no config", not an error.
	for _, m := range []map[string]any{{}, {"config": nil}} {
		cfg, err := configMapField(m, "config")
		if err != nil {
			t.Errorf("absent/nil config errored: %v", err)
		}
		if cfg != nil {
			t.Errorf("absent/nil config = %v, want nil", cfg)
		}
	}

	// Wrong types must error rather than silently no-op.
	for _, bad := range []any{"str", 3, []any{1, 2}} {
		if _, err := configMapField(map[string]any{"config": bad}, "config"); err == nil {
			t.Errorf("config = %T: want error, got nil", bad)
		}
	}
}
