package provisioner

import "testing"

func TestParseDNSPort(t *testing.T) {
	tests := []struct {
		name string
		raw  any
		want int
	}{
		{name: "default for nil", raw: nil, want: 53},
		{name: "int", raw: 5300, want: 5300},
		{name: "string", raw: "5400", want: 5400},
		{name: "int32", raw: int32(5500), want: 5500},
		{name: "int64", raw: int64(5600), want: 5600},
		{name: "invalid string defaults", raw: "abc", want: 53},
		{name: "zero defaults", raw: 0, want: 53},
		{name: "negative defaults", raw: -1, want: 53},
		{name: "too large defaults", raw: 70000, want: 53},
		{name: "port 1 valid", raw: 1, want: 1},
		{name: "port 65535 valid", raw: 65535, want: 65535},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			if got := parseDNSPort(tc.raw); got != tc.want {
				t.Fatalf("parseDNSPort(%v) = %d, want %d", tc.raw, got, tc.want)
			}
		})
	}
}
