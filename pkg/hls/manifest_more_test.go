package hls

import (
	"strings"
	"testing"
)

func TestBuildLive_TargetDurationBoundary(t *testing.T) {
	if got := BuildLive(0); !strings.Contains(got, "#EXT-X-TARGETDURATION:6") {
		t.Errorf("BuildLive(0) should default target to 6, got:\n%s", got)
	}
	if got := BuildLive(-3); !strings.Contains(got, "#EXT-X-TARGETDURATION:6") {
		t.Errorf("BuildLive(-3) should default target to 6, got:\n%s", got)
	}
	if got := BuildLive(8); !strings.Contains(got, "#EXT-X-TARGETDURATION:8") {
		t.Errorf("BuildLive(8) should preserve target 8, got:\n%s", got)
	}
}

func TestParse_MediaSequence(t *testing.T) {
	body := `#EXTM3U
#EXT-X-TARGETDURATION:6
#EXT-X-MEDIA-SEQUENCE:42
#EXTINF:6.0,
segments/0001.ts
`
	m, err := Parse(body)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if m.MediaSequence != 42 {
		t.Errorf("MediaSequence = %d, want 42", m.MediaSequence)
	}
}

func TestParse_OrphanURIWithoutExtinfSkipped(t *testing.T) {
	body := `#EXTM3U
#EXT-X-TARGETDURATION:6
orphan.ts
`
	m, err := Parse(body)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(m.Segments) != 0 {
		t.Errorf("segments = %d, want 0 (URI without preceding #EXTINF is skipped)", len(m.Segments))
	}
}

func TestParse_QueryOnlySegmentName(t *testing.T) {
	body := "#EXTM3U\n#EXTINF:6.0,\n?x=1\n"
	m, err := Parse(body)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(m.Segments) != 1 {
		t.Fatalf("segments = %d, want 1", len(m.Segments))
	}
	if m.Segments[0].Name != "?x=1" {
		t.Errorf("Name = %q, want %q (leading '?' is not a query separator)", m.Segments[0].Name, "?x=1")
	}
}

func TestBuildVOD_TargetDurationRounding(t *testing.T) {
	cases := []struct {
		name       string
		durationMs int64
		wantTarget string
	}{
		{"exact second", 6000, "#EXT-X-TARGETDURATION:6"},
		{"one ms over", 6001, "#EXT-X-TARGETDURATION:7"},
		{"one ms under next", 6999, "#EXT-X-TARGETDURATION:7"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			segs := []FinalSegment{{Name: "a.ts", DurationMs: c.durationMs, MediaStartMs: 0, MediaEndMs: c.durationMs}}
			m := BuildVOD(segs, BuildVODOptions{})
			if !strings.Contains(m, c.wantTarget) {
				t.Errorf("want %q, got:\n%s", c.wantTarget, m)
			}
		})
	}
}

func TestBuildVOD_ZeroDurationTargetDefaults(t *testing.T) {
	segs := []FinalSegment{{Name: "a.ts", DurationMs: 0, MediaStartMs: 0, MediaEndMs: 0}}
	m := BuildVOD(segs, BuildVODOptions{})
	if !strings.Contains(m, "#EXT-X-TARGETDURATION:6") {
		t.Errorf("want default target 6 for zero-duration segments, got:\n%s", m)
	}
}

func TestBuildVOD_EmptySegmentsNoPanic(t *testing.T) {
	m := BuildVOD(nil, BuildVODOptions{})
	if !strings.Contains(m, "#EXT-X-MEDIA-SEQUENCE:0") {
		t.Errorf("want media sequence 0 for empty segment list, got:\n%s", m)
	}
	if !strings.Contains(m, "#EXT-X-ENDLIST") {
		t.Errorf("want ENDLIST for empty VOD, got:\n%s", m)
	}
}

func TestBuildVOD_FirstSegmentNeverDiscontinuous(t *testing.T) {
	segs := []FinalSegment{
		{Name: "0001.ts", Sequence: 1, DurationMs: 6000, MediaStartMs: 60000, MediaEndMs: 66000},
		{Name: "0002.ts", Sequence: 2, DurationMs: 6000, MediaStartMs: 66000, MediaEndMs: 72000},
	}
	m := BuildVOD(segs, BuildVODOptions{})
	if strings.Contains(m, "#EXT-X-DISCONTINUITY") {
		t.Errorf("first segment must never carry DISCONTINUITY even with large MediaStartMs, got:\n%s", m)
	}
}

func TestBuildVOD_DefaultThresholdSmallGapNoDiscontinuity(t *testing.T) {
	segs := []FinalSegment{
		{Name: "0001.ts", Sequence: 1, DurationMs: 6000, MediaStartMs: 0, MediaEndMs: 6000},
		{Name: "0002.ts", Sequence: 2, DurationMs: 6000, MediaStartMs: 6100, MediaEndMs: 12100},
	}
	m := BuildVOD(segs, BuildVODOptions{})
	if strings.Contains(m, "#EXT-X-DISCONTINUITY") {
		t.Errorf("100ms gap is below default threshold (9000ms); no DISCONTINUITY expected, got:\n%s", m)
	}
}

func TestBuildVOD_DefaultThresholdMidGapBoundaries(t *testing.T) {
	// DurationMs 6000 → default threshold = max((6000*3)/2, 1000) = 9000.
	cases := []struct {
		name    string
		gap     int64
		wantDis bool
	}{
		{"gap 4000 below threshold", 4000, false},
		{"gap 12000 above threshold", 12000, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			segs := []FinalSegment{
				{Name: "0001.ts", Sequence: 1, DurationMs: 6000, MediaStartMs: 0, MediaEndMs: 6000},
				{Name: "0002.ts", Sequence: 2, DurationMs: 6000, MediaStartMs: 6000 + c.gap, MediaEndMs: 12000 + c.gap},
			}
			m := BuildVOD(segs, BuildVODOptions{})
			got := strings.Contains(m, "#EXT-X-DISCONTINUITY")
			if got != c.wantDis {
				t.Errorf("DISCONTINUITY = %v, want %v (gap %d, threshold 9000)\n%s", got, c.wantDis, c.gap, m)
			}
		})
	}
}

func TestBuildVOD_ExplicitThresholdExactBoundary(t *testing.T) {
	// Explicit threshold 5000; gap exactly 5000 must NOT trigger (strict >).
	segs := []FinalSegment{
		{Name: "0001.ts", Sequence: 1, DurationMs: 6000, MediaStartMs: 0, MediaEndMs: 6000},
		{Name: "0002.ts", Sequence: 2, DurationMs: 6000, MediaStartMs: 11000, MediaEndMs: 17000},
	}
	m := BuildVOD(segs, BuildVODOptions{DiscontinuityThresholdMs: 5000})
	if strings.Contains(m, "#EXT-X-DISCONTINUITY") {
		t.Errorf("gap exactly equal to threshold must not trigger DISCONTINUITY, got:\n%s", m)
	}
}

func TestBuildVOD_ExplicitThresholdJustOver(t *testing.T) {
	// Explicit threshold 5000; gap 6000 is over the explicit threshold but
	// below the default (9000) — proves the explicit value is honored.
	segs := []FinalSegment{
		{Name: "0001.ts", Sequence: 1, DurationMs: 6000, MediaStartMs: 0, MediaEndMs: 6000},
		{Name: "0002.ts", Sequence: 2, DurationMs: 6000, MediaStartMs: 12000, MediaEndMs: 18000},
	}
	m := BuildVOD(segs, BuildVODOptions{DiscontinuityThresholdMs: 5000})
	if !strings.Contains(m, "#EXT-X-DISCONTINUITY") {
		t.Errorf("gap 6000 over explicit threshold 5000 must trigger DISCONTINUITY, got:\n%s", m)
	}
}

func TestBuildVOD_ZeroPrevEndStillEvaluatesGap(t *testing.T) {
	// First segment ends at media time 0; the second jumps to 60000ms. prevEnd
	// becomes exactly 0, which is a valid previous boundary (>= 0), so the jump
	// must still insert a DISCONTINUITY.
	segs := []FinalSegment{
		{Name: "0001.ts", Sequence: 1, DurationMs: 6000, MediaStartMs: 0, MediaEndMs: 0},
		{Name: "0002.ts", Sequence: 2, DurationMs: 6000, MediaStartMs: 60000, MediaEndMs: 66000},
	}
	m := BuildVOD(segs, BuildVODOptions{})
	if !strings.Contains(m, "#EXT-X-DISCONTINUITY") {
		t.Errorf("jump after a zero MediaEndMs must still insert DISCONTINUITY, got:\n%s", m)
	}
}

func TestBuildVOD_ZeroProgramDateTimeOmitted(t *testing.T) {
	segs := []FinalSegment{
		{Name: "0001.ts", Sequence: 1, DurationMs: 6000, MediaStartMs: 0, MediaEndMs: 6000, ProgramDateTimeMs: 0},
	}
	m := BuildVOD(segs, BuildVODOptions{})
	if strings.Contains(m, "#EXT-X-PROGRAM-DATE-TIME") {
		t.Errorf("ProgramDateTimeMs 0 must not render a PROGRAM-DATE-TIME tag, got:\n%s", m)
	}
}
