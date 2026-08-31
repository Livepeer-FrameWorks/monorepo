package artifacts

import "testing"

func TestCanonicalByteKind(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"chapter", "vod"}, {" Chapter ", "vod"}, {"vod", "vod"}, {"CLIP", "clip"}, {"", ""},
	} {
		if got := CanonicalByteKind(tc.in); got != tc.want {
			t.Fatalf("CanonicalByteKind(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
	if !SameByteKind("chapter", "vod") || SameByteKind("chapter", "clip") || SameByteKind("", "") {
		t.Fatal("byte-kind comparison does not preserve chapter/VOD storage identity")
	}
}
