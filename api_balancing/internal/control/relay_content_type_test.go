package control

import "testing"

// Intent: contentTypeForFormat echoes a usable Content-Type on the relay's
// first response. Pin the format->MIME table, the leading-dot trim, the
// case-insensitivity, and that an unknown format yields "" (no guess).
func TestContentTypeForFormat(t *testing.T) {
	cases := []struct {
		format string
		want   string
	}{
		{"mp4", "video/mp4"},
		{"mov", "video/mp4"},
		{"m4v", "video/mp4"},
		{".mp4", "video/mp4"}, // leading dot trimmed
		{"MP4", "video/mp4"},  // case-insensitive
		{"mkv", "video/x-matroska"},
		{"webm", "video/webm"},
		{"ts", "video/mp2t"},
		{"m2ts", "video/mp2t"},
		{"m3u8", "application/vnd.apple.mpegurl"},
		{"m3u", "application/vnd.apple.mpegurl"},
		{"unknown", ""},
		{"", ""},
	}
	for _, tc := range cases {
		if got := contentTypeForFormat(tc.format); got != tc.want {
			t.Fatalf("contentTypeForFormat(%q) = %q, want %q", tc.format, got, tc.want)
		}
	}
}
