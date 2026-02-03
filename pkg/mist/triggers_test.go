package mist

import "testing"

func TestExtractInternalName(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "live prefix", input: "live+stream_id", expected: "stream_id"},
		{name: "vod prefix", input: "vod+asset_hash", expected: "asset_hash"},
		{name: "plain", input: "plain_stream", expected: "plain_stream"},
		{name: "plus in name", input: "stream+with+plus", expected: "stream+with+plus"},
	}

	for _, tc := range cases {
		if got := ExtractInternalName(tc.input); got != tc.expected {
			t.Fatalf("%s: expected %q, got %q", tc.name, tc.expected, got)
		}
	}
}
