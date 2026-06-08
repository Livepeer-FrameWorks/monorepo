package bootstrap

import "testing"

// TestNumericEq pins the NUMERIC(20,6) drift comparison the reconciler uses to
// decide whether a stored column matches the desired float. Postgres renders a
// NUMERIC(20,6) with trailing zeros to scale, so the comparison must format the
// desired value to exactly 6 places — otherwise every reconcile sees spurious
// drift (e.g. stored "5.000000" vs naive "5") and re-writes unchanged rows.
func TestNumericEq(t *testing.T) {
	cases := []struct {
		current string
		desired float64
		want    bool
	}{
		{"5.000000", 5, true},
		{"5.000000", 5.0000004, true}, // rounds to 6 places → "5.000000"
		{"5.000001", 5.000001, true},
		{"5.000000", 5.00001, false}, // real drift at the 5th place
		{"5", 5, false},              // unscaled text is NOT how Postgres stores it
	}
	for _, tc := range cases {
		if got := numericEq(tc.current, tc.desired); got != tc.want {
			t.Errorf("numericEq(%q, %v) = %v, want %v", tc.current, tc.desired, got, tc.want)
		}
	}
}

// TestPriceEq pins the NUMERIC(20,9) price comparison. Prices are stored as
// decimal strings to avoid float artifacts, so equality is decimal-value based
// (trailing zeros and scale differences must compare equal), an empty desired
// is treated as "0", and unparseable text falls back to exact string equality
// rather than panicking.
func TestPriceEq(t *testing.T) {
	cases := []struct {
		name             string
		current, desired string
		want             bool
	}{
		{"scale-insensitive equality", "0.020000000", "0.02", true},
		{"trailing zeros equal", "1.000000000", "1", true},
		{"empty desired means zero", "0.000000000", "", true},
		{"genuine difference", "0.020000000", "0.03", false},
		{"unparseable current falls back to string compare", "n/a", "n/a", true},
		{"unparseable current vs real price differs", "n/a", "0.02", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := priceEq(tc.current, tc.desired); got != tc.want {
				t.Fatalf("priceEq(%q, %q) = %v, want %v", tc.current, tc.desired, got, tc.want)
			}
		})
	}
}
