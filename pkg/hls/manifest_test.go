package hls

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParse_BasicLiveManifest(t *testing.T) {
	body := `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:8
#EXT-X-MEDIA-SEQUENCE:0
#EXT-X-PLAYLIST-TYPE:EVENT
#EXTINF:6.000,
segments/0001.ts
#EXTINF:6.000,
segments/0002.ts
`
	m, err := Parse(body)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if m.TargetDuration != 8 {
		t.Errorf("target = %d, want 8", m.TargetDuration)
	}
	if len(m.Segments) != 2 {
		t.Fatalf("segments = %d, want 2", len(m.Segments))
	}
	if m.Segments[0].Name != "0001.ts" || m.Segments[0].Duration != 6.0 {
		t.Errorf("segment 0 = %+v", m.Segments[0])
	}
}

func TestParse_StripsQueryAndPath(t *testing.T) {
	body := `#EXTM3U
#EXTINF:6.0,
foo/bar/0001.ts?x=1
`
	m, _ := Parse(body)
	if len(m.Segments) != 1 || m.Segments[0].Name != "0001.ts" {
		t.Fatalf("got %+v", m.Segments)
	}
}

func TestParse_EmptyError(t *testing.T) {
	if _, err := Parse(""); err == nil {
		t.Fatal("expected error for empty body")
	}
}

func TestBuildLive_AppendFinalize_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "live.m3u8")
	if err := os.WriteFile(path, []byte(BuildLive(6)), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := AppendSegment(path, "0001.ts", 6.0); err != nil {
		t.Fatalf("append 1: %v", err)
	}
	if err := AppendSegment(path, "0002.ts", 5.987); err != nil {
		t.Fatalf("append 2: %v", err)
	}
	if err := Finalize(path); err != nil {
		t.Fatalf("finalize: %v", err)
	}
	body, _ := os.ReadFile(path)
	got := string(body)
	for _, want := range []string{
		"#EXTM3U",
		"#EXT-X-TARGETDURATION:6",
		"segments/0001.ts",
		"segments/0002.ts",
		"#EXT-X-ENDLIST",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("manifest missing %q\nfull:\n%s", want, got)
		}
	}
}

func TestBuildVOD_Plain(t *testing.T) {
	segs := []FinalSegment{
		{Name: "0001.ts", Sequence: 1, DurationMs: 6000, MediaStartMs: 0, MediaEndMs: 6000},
		{Name: "0002.ts", Sequence: 2, DurationMs: 6000, MediaStartMs: 6000, MediaEndMs: 12000},
		{Name: "0003.ts", Sequence: 3, DurationMs: 6000, MediaStartMs: 12000, MediaEndMs: 18000},
	}
	m := BuildVOD(segs, BuildVODOptions{})
	if !strings.Contains(m, "#EXT-X-PLAYLIST-TYPE:VOD") {
		t.Error("missing VOD type")
	}
	if !strings.Contains(m, "#EXT-X-ENDLIST") {
		t.Error("missing ENDLIST")
	}
	if strings.Contains(m, "#EXT-X-GAP") {
		t.Error("unexpected GAP without lost segments")
	}
	if strings.Contains(m, "#EXT-X-DISCONTINUITY") {
		t.Error("unexpected DISCONTINUITY without media-time jumps")
	}
	if strings.Count(m, "#EXTINF:6.000,") != 3 {
		t.Error("expected 3 EXTINF entries")
	}
	if !strings.Contains(m, "#EXT-X-VERSION:6") {
		t.Errorf("expected VERSION:6 for clean manifest, got:\n%s", m)
	}
	if !strings.Contains(m, "#EXT-X-MEDIA-SEQUENCE:1") {
		t.Errorf("expected media sequence to match first segment sequence, got:\n%s", m)
	}
}

func TestBuildVOD_ProgramDateTime(t *testing.T) {
	segs := []FinalSegment{
		{
			Name:              "0001.ts",
			Sequence:          42,
			DurationMs:        6000,
			MediaStartMs:      0,
			MediaEndMs:        6000,
			ProgramDateTimeMs: 1762000000123,
		},
		{
			Name:              "0002.ts",
			Sequence:          43,
			DurationMs:        6000,
			MediaStartMs:      6000,
			MediaEndMs:        12000,
			ProgramDateTimeMs: 1762000006123,
			Lost:              true,
		},
	}
	m := BuildVOD(segs, BuildVODOptions{})
	if !strings.Contains(m, "#EXT-X-PROGRAM-DATE-TIME:2025-11-01T12:26:40.123Z\n#EXTINF:6.000,\nsegments/0001.ts") {
		t.Errorf("expected PROGRAM-DATE-TIME before reachable segment, got:\n%s", m)
	}
	if !strings.Contains(m, "#EXT-X-PROGRAM-DATE-TIME:2025-11-01T12:26:46.123Z\n#EXT-X-GAP\n#EXTINF:6.000,\nsegments/0002.ts") {
		t.Errorf("expected PROGRAM-DATE-TIME before GAP segment, got:\n%s", m)
	}
}

func TestBuildVOD_LostSegmentRendersGap(t *testing.T) {
	segs := []FinalSegment{
		{Name: "0001.ts", Sequence: 1, DurationMs: 6000, MediaStartMs: 0, MediaEndMs: 6000},
		{Name: "0002.ts", Sequence: 2, DurationMs: 6000, MediaStartMs: 6000, MediaEndMs: 12000, Lost: true},
		{Name: "0003.ts", Sequence: 3, DurationMs: 6000, MediaStartMs: 12000, MediaEndMs: 18000},
	}
	m := BuildVOD(segs, BuildVODOptions{})
	// Lost segment renders as GAP+EXTINF and the segment URI is still present.
	if !strings.Contains(m, "#EXT-X-GAP\n#EXTINF:6.000,\nsegments/0002.ts") {
		t.Errorf("expected GAP + EXTINF + URI for lost segment, got:\n%s", m)
	}
	// Adjacent media times match exactly between the lost segment and
	// neighbors, so no DISCONTINUITY is emitted.
	if strings.Contains(m, "#EXT-X-DISCONTINUITY") {
		t.Error("lost segment should not by itself force DISCONTINUITY")
	}
	// HLS v8 is required for #EXT-X-GAP per Apple HLS spec.
	if !strings.Contains(m, "#EXT-X-VERSION:8") {
		t.Errorf("expected VERSION:8 with GAP marker, got:\n%s", m)
	}
}

func TestBuildVOD_SegmentURIPrefix(t *testing.T) {
	segs := []FinalSegment{
		{Name: "0001.ts", DurationMs: 6000, MediaStartMs: 0, MediaEndMs: 6000},
	}
	// Chapter playlists live at chapters/{id}.m3u8 and need ../segments/
	// so HLS resolves segments at the artifact root's segments/ dir.
	m := BuildVOD(segs, BuildVODOptions{SegmentURIPrefix: "../segments/"})
	if !strings.Contains(m, "../segments/0001.ts") {
		t.Errorf("expected ../segments/0001.ts URI, got:\n%s", m)
	}
	if strings.Contains(m, "\nsegments/0001.ts") {
		t.Errorf("expected NO bare segments/0001.ts URI when prefix overridden, got:\n%s", m)
	}
}

func TestBuildVOD_EventShape(t *testing.T) {
	segs := []FinalSegment{
		{Name: "0001.ts", Sequence: 42, DurationMs: 6000, MediaStartMs: 0, MediaEndMs: 6000},
	}
	m := BuildVOD(segs, BuildVODOptions{Event: true})
	if !strings.Contains(m, "#EXT-X-PLAYLIST-TYPE:EVENT") {
		t.Errorf("expected EVENT playlist type, got:\n%s", m)
	}
	if strings.Contains(m, "#EXT-X-PLAYLIST-TYPE:VOD") {
		t.Errorf("did not expect VOD playlist type, got:\n%s", m)
	}
	if strings.Contains(m, "#EXT-X-ENDLIST") {
		t.Errorf("did not expect ENDLIST in EVENT manifest, got:\n%s", m)
	}
	if !strings.Contains(m, "#EXT-X-MEDIA-SEQUENCE:42") {
		t.Errorf("expected media sequence to match first segment sequence, got:\n%s", m)
	}
}

func TestBuildVOD_HasGapsForcesV8EvenWithoutLost(t *testing.T) {
	// A chapter rebuild may pass a segment slice that doesn't include the
	// lost rows (they fell outside the requested range). HasGaps is the
	// caller's hook to force v8 anyway.
	segs := []FinalSegment{
		{Name: "0001.ts", DurationMs: 6000, MediaStartMs: 0, MediaEndMs: 6000},
	}
	m := BuildVOD(segs, BuildVODOptions{HasGaps: true})
	if !strings.Contains(m, "#EXT-X-VERSION:8") {
		t.Errorf("expected VERSION:8 when HasGaps=true, got:\n%s", m)
	}
	if strings.Contains(m, "#EXT-X-GAP") {
		t.Error("HasGaps=true without any Lost segments should NOT emit GAP markers")
	}
}

func TestBuildVOD_MediaTimeJumpInsertsDiscontinuity(t *testing.T) {
	// Sidecar restart: segment 2 ended at 12000ms, segment 3 starts at 60000ms.
	// 48s gap >> 1.5x segment duration → DISCONTINUITY before segment 3.
	segs := []FinalSegment{
		{Name: "0001.ts", Sequence: 1, DurationMs: 6000, MediaStartMs: 0, MediaEndMs: 6000},
		{Name: "0002.ts", Sequence: 2, DurationMs: 6000, MediaStartMs: 6000, MediaEndMs: 12000},
		{Name: "0003.ts", Sequence: 3, DurationMs: 6000, MediaStartMs: 60000, MediaEndMs: 66000},
	}
	m := BuildVOD(segs, BuildVODOptions{})
	idxDisc := strings.Index(m, "#EXT-X-DISCONTINUITY")
	idxSeg3 := strings.Index(m, "0003.ts")
	if idxDisc < 0 || idxSeg3 < 0 || idxDisc >= idxSeg3 {
		t.Errorf("expected DISCONTINUITY before 0003.ts; got:\n%s", m)
	}
	// Only one DISCONTINUITY for the one jump.
	if c := strings.Count(m, "#EXT-X-DISCONTINUITY"); c != 1 {
		t.Errorf("DISCONTINUITY count = %d, want 1", c)
	}
}

func TestBuildVOD_TargetDurationFromMaxSegment(t *testing.T) {
	segs := []FinalSegment{
		{Name: "a.ts", DurationMs: 4500, MediaStartMs: 0, MediaEndMs: 4500},
		{Name: "b.ts", DurationMs: 7250, MediaStartMs: 4500, MediaEndMs: 11750},
	}
	m := BuildVOD(segs, BuildVODOptions{})
	if !strings.Contains(m, "#EXT-X-TARGETDURATION:8") {
		t.Errorf("expected target=8 from max 7.25s segment, got:\n%s", m)
	}
}
