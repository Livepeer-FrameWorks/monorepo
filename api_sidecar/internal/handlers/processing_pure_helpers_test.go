package handlers

import (
	"strconv"
	"testing"
	"time"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/mist"
)

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

// completeRenditionTracks is the load-bearing guard for the "complete ladder or
// source passthrough — never a partial rendition set" invariant. These exercise
// the matching algorithm directly (the selector tests only cover it indirectly).
func TestCompleteRenditionTracks(t *testing.T) {
	source := mist.SourceMediaInfo{Width: 1920, Height: 1080}

	t.Run("zero requested height is unsatisfiable", func(t *testing.T) {
		tracks := []processingMetaVideoTrack{chapterTrack(2, 1280, 720, 30000)}
		if _, ok := completeRenditionTracks([]int{0}, tracks, source, 30000); ok {
			t.Fatal("a zero/negative requested height must fail closed")
		}
	})

	t.Run("duplicate heights consume distinct tracks", func(t *testing.T) {
		tracks := []processingMetaVideoTrack{
			chapterTrack(2, 1280, 720, 30000),
			chapterTrack(3, 1280, 720, 30000),
		}
		got, ok := completeRenditionTracks([]int{720, 720}, tracks, source, 30000)
		if !ok || len(got) != 2 {
			t.Fatalf("two 720 requests should consume two distinct 720 tracks: ok=%v n=%d", ok, len(got))
		}
		if got[0].trackID == got[1].trackID {
			t.Fatal("the same track was matched twice")
		}
	})

	t.Run("duplicate heights with only one track fails", func(t *testing.T) {
		tracks := []processingMetaVideoTrack{chapterTrack(2, 1280, 720, 30000)}
		if _, ok := completeRenditionTracks([]int{720, 720}, tracks, source, 30000); ok {
			t.Fatal("two 720 requests cannot be satisfied by one track")
		}
	})

	t.Run("track without identity cannot satisfy a height", func(t *testing.T) {
		noID := chapterTrack(0, 1280, 720, 30000)
		noID.hasTrackID = false
		if _, ok := completeRenditionTracks([]int{720}, []processingMetaVideoTrack{noID}, source, 30000); ok {
			t.Fatal("a track with no selector identity must not satisfy a rendition")
		}
	})

	t.Run("span shortfall rejects a truncated rendition", func(t *testing.T) {
		short := chapterTrack(2, 1280, 720, 1000) // 29s short of a 30s span
		if _, ok := completeRenditionTracks([]int{720}, []processingMetaVideoTrack{short}, source, 30000); ok {
			t.Fatal("a rendition far short of the source span must be rejected")
		}
	})

	t.Run("source track is excluded from the candidate pool", func(t *testing.T) {
		// The only 1080 track is the source itself; a 1080 rendition request
		// must not be satisfiable by reusing the source track.
		src := chapterTrack(1, 1920, 1080, 30000)
		if _, ok := completeRenditionTracks([]int{1080}, []processingMetaVideoTrack{src}, source, 30000); ok {
			t.Fatal("the source track must not double as a rendition")
		}
	})

	t.Run("empty track list fails", func(t *testing.T) {
		if _, ok := completeRenditionTracks([]int{720}, nil, source, 30000); ok {
			t.Fatal("no tracks cannot satisfy any rendition")
		}
	})
}

func TestProcessingSourceVideoSelector(t *testing.T) {
	source := mist.SourceMediaInfo{Width: 1920, Height: 1080}

	withID := []processingMetaVideoTrack{chapterTrack(7, 1920, 1080, 30000)}
	if got := processingSourceVideoSelector(withID, source); got != "i7" {
		t.Errorf("source with identity = %q, want i7", got)
	}

	noID := chapterTrack(0, 1920, 1080, 30000)
	noID.hasTrackID = false
	if got := processingSourceVideoSelector([]processingMetaVideoTrack{noID}, source); got != "source" {
		t.Errorf("source without identity = %q, want \"source\"", got)
	}

	if got := processingSourceVideoSelector(nil, source); got != "source" {
		t.Errorf("no tracks = %q, want \"source\"", got)
	}
}

func TestProcessingMetaSelector(t *testing.T) {
	if got := processingMetaSelector(""); got != "all" {
		t.Errorf("no processes = %q, want all", got)
	}
	if got := processingMetaSelector(`[{"process":"AV","codec":"H264"}]`); got != "all" {
		t.Errorf("no thumbs = %q, want all", got)
	}
	if got := processingMetaSelector(`[{"process":"Thumbs"}]`); got != "all,thumbvtt" {
		t.Errorf("with thumbs = %q, want all,thumbvtt", got)
	}
}

// unsafeWrapperExt identifies wrapper formats Mist cannot open over HTTP, gating
// the local-staging download path.
func TestUnsafeWrapperExt(t *testing.T) {
	cases := map[string]string{
		"http://h/clip.avi":       ".avi",
		"http://h/clip.flv":       ".flv",
		"http://h/clip.m4v":       ".m4v",
		"http://h/clip.AVI":       ".avi",
		"http://h/clip.mp4":       "",
		"http://h/clip.mkv":       "",
		"http://h/clip":           "",
		"":                        "",
		"http://h/a.avi?x=1#frag": ".avi",
		"://bad url with spaces":  "",
	}
	for in, want := range cases {
		if got := unsafeWrapperExt(in); got != want {
			t.Errorf("unsafeWrapperExt(%q) = %q, want %q", in, got, want)
		}
	}
}

// deriveProcessingMistHTTPBase normalises any configured Mist URL to the HTTP
// API base, mapping the controller port (4242) to the HTTP port (8080).
func TestDeriveProcessingMistHTTPBase(t *testing.T) {
	cases := map[string]string{
		"http://localhost:4242": "http://localhost:8080",
		"http://localhost:8080": "http://localhost:8080",
		"https://mist:9000":     "https://mist:9000",
		"http://mist":           "http://mist:8080",
		"mist-host":             "http://mist-host:8080",
		"mist-host/extra/path":  "http://mist-host:8080",
	}
	for in, want := range cases {
		if got := deriveProcessingMistHTTPBase(in); got != want {
			t.Errorf("deriveProcessingMistHTTPBase(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestProcessingSourceExt(t *testing.T) {
	cases := map[string]string{
		"mp4":  ".mp4",
		"MOV":  ".mov",
		".mkv": ".mkv",
		"webm": ".webm",
		"ts":   ".ts",
		"avi":  ".mkv", // unknown/unsafe normalises to the safe default
		"":     ".mkv",
	}
	for format, want := range cases {
		req := &ipcpb.ProcessingJobRequest{Params: map[string]string{"source_format": format}}
		if got := processingSourceExt(req); got != want {
			t.Errorf("processingSourceExt(source_format=%q) = %q, want %q", format, got, want)
		}
	}
}

func TestIsClipProcessingSource(t *testing.T) {
	cases := []struct {
		kind, stream string
		want         bool
	}{
		{"live", "live+s", true},
		{"dvr_rolling", "dvr+s", true},
		{"chapter", "live+s", true},
		{"live", "", false},     // clip kinds require a source stream
		{"vod", "vod+s", false}, // VOD is not a live-artifact clip source
		{"", "live+s", false},
	}
	for _, tc := range cases {
		req := &ipcpb.ProcessingJobRequest{Params: map[string]string{
			"source_kind":        tc.kind,
			"source_stream_name": tc.stream,
		}}
		if got := isClipProcessingSource(req); got != tc.want {
			t.Errorf("isClipProcessingSource(kind=%q,stream=%q) = %v, want %v", tc.kind, tc.stream, got, tc.want)
		}
	}
}

// processingSourceStageTimeout scales the staging download budget to the clip
// span (clamped 2–15m) and uses a fixed 30m budget for full VOD sources.
func TestProcessingSourceStageTimeout(t *testing.T) {
	clip := func(start, stop int64) *ipcpb.ProcessingJobRequest {
		return &ipcpb.ProcessingJobRequest{Params: map[string]string{
			"source_kind":        "live",
			"source_stream_name": "live+s",
			"source_start_unix":  strconv.FormatInt(start, 10),
			"source_stop_unix":   strconv.FormatInt(stop, 10),
		}}
	}
	// 60s span + 30s headroom = 90s, below the 2m floor.
	if got := processingSourceStageTimeout(clip(1000, 1060)); got != 2*time.Minute {
		t.Errorf("short clip = %v, want 2m floor", got)
	}
	// 5m span + 30s headroom = 5m30s, between the bounds.
	if got := processingSourceStageTimeout(clip(1000, 1300)); got != 5*time.Minute+30*time.Second {
		t.Errorf("mid clip = %v, want 5m30s", got)
	}
	// 30m span exceeds the 15m ceiling.
	if got := processingSourceStageTimeout(clip(0, 1800)); got != 15*time.Minute {
		t.Errorf("long clip = %v, want 15m ceiling", got)
	}
	// Non-clip (VOD) source uses the fixed 30m budget.
	vod := &ipcpb.ProcessingJobRequest{Params: map[string]string{"source_kind": "vod"}}
	if got := processingSourceStageTimeout(vod); got != 30*time.Minute {
		t.Errorf("vod = %v, want 30m", got)
	}
}
