package handlers

import "testing"

func TestFormatBytes(t *testing.T) {
	cases := []struct {
		name     string
		input    uint64
		expected string
	}{
		{name: "zero", input: 0, expected: "0 B"},
		{name: "bytes", input: 500, expected: "500 B"},
		{name: "one-kb", input: 1024, expected: "1.0 KB"},
		{name: "one-and-half-kb", input: 1536, expected: "1.5 KB"},
		{name: "one-mb", input: 1048576, expected: "1.0 MB"},
		{name: "one-gb", input: 1073741824, expected: "1.0 GB"},
		{name: "one-tb", input: 1099511627776, expected: "1.0 TB"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			actual := formatBytes(tc.input)
			if actual != tc.expected {
				t.Fatalf("formatBytes(%d) = %q, want %q", tc.input, actual, tc.expected)
			}
		})
	}
}

func TestFormatBitsPerSec(t *testing.T) {
	cases := []struct {
		name     string
		input    uint64
		expected string
	}{
		{name: "zero", input: 0, expected: "0 bps"},
		{name: "bps", input: 500, expected: "500 bps"},
		{name: "one-kbps", input: 1000, expected: "1.0 Kbps"},
		{name: "one-mbps", input: 1000000, expected: "1.0 Mbps"},
		{name: "one-gbps", input: 1000000000, expected: "1.0 Gbps"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			actual := formatBitsPerSec(tc.input)
			if actual != tc.expected {
				t.Fatalf("formatBitsPerSec(%d) = %q, want %q", tc.input, actual, tc.expected)
			}
		})
	}
}

func TestFormatBytesPerSec(t *testing.T) {
	cases := []struct {
		name     string
		input    uint64
		expected string
	}{
		{name: "zero", input: 0, expected: "0 bps"},
		{name: "125-bytes-is-1kbps", input: 125, expected: "1.0 Kbps"},
		{name: "125000-bytes-is-1mbps", input: 125000, expected: "1.0 Mbps"},
		{name: "1-mebibyte-is-about-8mbps", input: 1048576, expected: "8.4 Mbps"},
		{name: "125000000-bytes-is-1gbps", input: 125000000, expected: "1.0 Gbps"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			actual := formatBytesPerSec(tc.input)
			if actual != tc.expected {
				t.Fatalf("formatBytesPerSec(%d) = %q, want %q", tc.input, actual, tc.expected)
			}
		})
	}
}
