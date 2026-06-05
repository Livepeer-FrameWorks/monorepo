package graph

import "testing"

// formatWeiAsEth renders an on-chain price for display. The intent is a trimmed
// decimal: whole ETH with up to 6 significant fractional digits (frac is
// computed as wei/1e6), no trailing zeros, sign preserved.
func TestFormatWeiAsEth(t *testing.T) {
	const eth int64 = 1_000_000_000_000_000_000
	tests := []struct {
		name string
		wei  int64
		want string
	}{
		{"zero", 0, "0"},
		{"one eth", eth, "1"},
		{"one and a half", eth + eth/2, "1.5"},
		{"negative one", -eth, "-1"},
		{"negative fraction", -(eth / 2), "-0.5"},
		// frac is (wei%1e18)/1e6, so anything below 1e6 wei truncates to zero.
		{"sub-microether truncates to zero", 999_999, "0"},
		{"smallest representable fraction", 1_000_000, "0.000000000001"},
		{"trailing zeros trimmed", eth + eth/10, "1.1"},
	}
	for _, tt := range tests {
		if got := formatWeiAsEth(tt.wei); got != tt.want {
			t.Errorf("%s: formatWeiAsEth(%d) = %q, want %q", tt.name, tt.wei, got, tt.want)
		}
	}
}
