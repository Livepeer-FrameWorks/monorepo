package handlers

import "testing"

// normalizedProcessingCodec canonicalizes codec aliases so a format relabel
// (e.g. "h265" vs "hevc") groups into one billing bucket instead of being
// double-counted. It also lower-cases and trims so casing/whitespace never
// fragments a group.
func TestNormalizedProcessingCodec(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"h265", "hevc"},
		{"H265", "hevc"},
		{"  h265 ", "hevc"},
		{"hevc", "hevc"},
		{"H264", "h264"},
		{"  AV1 ", "av1"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := normalizedProcessingCodec(tt.in); got != tt.want {
			t.Errorf("normalizedProcessingCodec(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
