package mist

import (
	"encoding/json"
	"testing"
)

func TestInt64FromAny(t *testing.T) {
	// Intent: trigger JSON arrives with numbers of varying concrete Go types
	// (json decodes to float64, but maps may carry int/int64/json.Number too).
	// int64FromAny must accept each numeric kind and signal failure (ok=false,
	// 0) for anything it cannot interpret, so callers never silently coerce a
	// non-number to a track index.
	t.Run("accepts numeric kinds", func(t *testing.T) {
		cases := []struct {
			name string
			in   any
			want int64
		}{
			{"int", int(7), 7},
			{"int32", int32(8), 8},
			{"int64", int64(9), 9},
			{"float64 truncates", float64(10.9), 10},
			{"json.Number", json.Number("11"), 11},
		}
		for _, tt := range cases {
			t.Run(tt.name, func(t *testing.T) {
				got, ok := int64FromAny(tt.in)
				if !ok || got != tt.want {
					t.Fatalf("int64FromAny(%v) = (%d, %v), want (%d, true)", tt.in, got, ok, tt.want)
				}
			})
		}
	})

	t.Run("rejects non-numeric and bad json.Number", func(t *testing.T) {
		cases := []struct {
			name string
			in   any
		}{
			{"string", "12"},
			{"bool", true},
			{"nil", nil},
			{"unparsable json.Number", json.Number("not-a-number")},
		}
		for _, tt := range cases {
			t.Run(tt.name, func(t *testing.T) {
				if got, ok := int64FromAny(tt.in); ok || got != 0 {
					t.Fatalf("int64FromAny(%v) = (%d, %v), want (0, false)", tt.in, got, ok)
				}
			})
		}
	})
}

func TestLivepeerProfileToCodec(t *testing.T) {
	// Intent: map a Livepeer profile name to the MistProcAV codec by prefix.
	// H265 and HEVC both fold to "H265", and an unrecognized profile passes
	// through unchanged (rather than defaulting to a wrong codec).
	cases := []struct {
		profile, want string
	}{
		{"H264ConstrainedHigh", "H264"},
		{"VP9", "VP9"},
		{"VP8", "VP8"},
		{"AV1", "AV1"},
		{"H265Main", "H265"},
		{"HEVCMain", "H265"},
		{"SomethingElse", "SomethingElse"},
		{"", ""},
	}
	for _, tt := range cases {
		t.Run(tt.profile, func(t *testing.T) {
			if got := livepeerProfileToCodec(tt.profile); got != tt.want {
				t.Fatalf("livepeerProfileToCodec(%q) = %q, want %q", tt.profile, got, tt.want)
			}
		})
	}
}
