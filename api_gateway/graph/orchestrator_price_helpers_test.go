package graph

import (
	"math"
	"testing"
)

// formatWeiAsEth renders an on-chain price as an exact ETH decimal: no
// trailing zeros, sign preserved, and no wei digit dropped, since Livepeer
// prices per pixel are far below a microether.
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
		{"one wei", 1, "0.000000000000000001"},
		{"a Livepeer price per pixel", 1200, "0.0000000000000012"},
		{"sub-microether", 999_999, "0.000000000000999999"},
		{"one microether", 1_000_000, "0.000000000001"},
		{"trailing zeros trimmed", eth + eth/10, "1.1"},
		{"whole and wei", eth + 3, "1.000000000000000003"},
		{"largest", math.MaxInt64, "9.223372036854775807"},
		{"smallest", math.MinInt64, "-9.223372036854775808"},
	}
	for _, tt := range tests {
		if got := formatWeiAsEth(tt.wei); got != tt.want {
			t.Errorf("%s: formatWeiAsEth(%d) = %q, want %q", tt.name, tt.wei, got, tt.want)
		}
	}
}
