package handlers

import "testing"

// extractHLSTagURI pulls the URI="..." value out of an HLS tag line. It is used
// while rewriting manifests to remap segment/key references.
func TestExtractHLSTagURI(t *testing.T) {
	cases := []struct {
		name string
		line string
		want string
	}{
		{name: "key tag", line: `#EXT-X-KEY:METHOD=AES-128,URI="key.bin"`, want: "key.bin"},
		{name: "path value", line: `#EXT-X-MAP:URI="init/a.mp4"`, want: "init/a.mp4"},
		{name: "no uri attribute", line: `#EXTINF:6.0,`, want: ""},
		{name: "empty uri", line: `#EXT-X-MAP:URI=""`, want: ""},
		{name: "unterminated quote", line: `#EXT-X-MAP:URI="oops`, want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := extractHLSTagURI(tc.line); got != tc.want {
				t.Fatalf("extractHLSTagURI(%q) = %q, want %q", tc.line, got, tc.want)
			}
		})
	}
}

// sourceDurationFromOutputs reads the authoritative source duration (ms) from a
// readiness outputs map, returning 0 for any missing/invalid/non-positive value.
func TestSourceDurationFromOutputs(t *testing.T) {
	cases := []struct {
		name    string
		outputs map[string]string
		want    int64
	}{
		{name: "valid", outputs: map[string]string{"duration_ms": "5000"}, want: 5000},
		{name: "missing key", outputs: map[string]string{}, want: 0},
		{name: "non-numeric", outputs: map[string]string{"duration_ms": "abc"}, want: 0},
		{name: "zero", outputs: map[string]string{"duration_ms": "0"}, want: 0},
		{name: "negative", outputs: map[string]string{"duration_ms": "-10"}, want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sourceDurationFromOutputs(tc.outputs); got != tc.want {
				t.Fatalf("sourceDurationFromOutputs(%v) = %d, want %d", tc.outputs, got, tc.want)
			}
		})
	}
}

// renditionHeightsClose absorbs codec/profile rounding while keeping distinct
// ladder rungs apart: tolerance = max(32px, 5% of expected). The boundary cases
// pin that the crossover and the keep-distinct guarantees both hold.
func TestRenditionHeightsClose(t *testing.T) {
	cases := []struct {
		name             string
		actual, expected int
		want             bool
	}{
		{name: "zero actual", actual: 0, expected: 720, want: false},
		{name: "zero expected", actual: 720, expected: 0, want: false},
		{name: "exact", actual: 720, expected: 720, want: true},
		{name: "within 5pct at 720", actual: 684, expected: 720, want: true},     // tol=36, diff=36
		{name: "beyond 5pct at 720", actual: 683, expected: 720, want: false},    // tol=36, diff=37
		{name: "floor tolerance at 640", actual: 608, expected: 640, want: true}, // tol=32, diff=32
		{name: "beyond floor at 640", actual: 607, expected: 640, want: false},   // tol=32, diff=33
		{name: "distinct rungs stay distinct", actual: 480, expected: 720, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := renditionHeightsClose(tc.actual, tc.expected); got != tc.want {
				t.Fatalf("renditionHeightsClose(%d, %d) = %v, want %v", tc.actual, tc.expected, got, tc.want)
			}
		})
	}
}

// visibleProcessingVideoTrackCount counts selectable video tracks: explicit
// tracks win; otherwise non-JPEG codecs are counted (JPEG is the thumbnail).
func TestVisibleProcessingVideoTrackCount(t *testing.T) {
	t.Run("explicit tracks take precedence", func(t *testing.T) {
		p := processingTrackPresence{videoTracks: []processingMetaVideoTrack{{height: 720}, {height: 1080}}}
		if got := visibleProcessingVideoTrackCount(p); got != 2 {
			t.Fatalf("count = %d, want 2", got)
		}
	})
	t.Run("jpeg codec excluded", func(t *testing.T) {
		p := processingTrackPresence{videoCodecs: map[string]bool{"H264": true, "JPEG": true}}
		if got := visibleProcessingVideoTrackCount(p); got != 1 {
			t.Fatalf("count = %d, want 1 (JPEG excluded)", got)
		}
	})
	t.Run("jpeg only is zero", func(t *testing.T) {
		p := processingTrackPresence{videoCodecs: map[string]bool{"JPEG": true}}
		if got := visibleProcessingVideoTrackCount(p); got != 0 {
			t.Fatalf("count = %d, want 0", got)
		}
	})
	t.Run("empty is zero", func(t *testing.T) {
		if got := visibleProcessingVideoTrackCount(processingTrackPresence{}); got != 0 {
			t.Fatalf("count = %d, want 0", got)
		}
	})
}

func TestVisibleProcessingVideoTracksReady(t *testing.T) {
	p := processingTrackPresence{videoCodecs: map[string]bool{"H264": true}}
	if !visibleProcessingVideoTracksReady(p, 0) {
		t.Fatal("want<=0 should always be ready")
	}
	if !visibleProcessingVideoTracksReady(p, 1) {
		t.Fatal("one visible track should satisfy want=1")
	}
	if visibleProcessingVideoTracksReady(p, 2) {
		t.Fatal("one visible track should not satisfy want=2")
	}
}
