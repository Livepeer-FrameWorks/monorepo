package handlers

import (
	"testing"
)

func TestParseHLSManifest_Standard(t *testing.T) {
	content := `#EXTM3U
#EXT-X-VERSION:3
#EXT-X-TARGETDURATION:6
#EXT-X-MEDIA-SEQUENCE:0
#EXTINF:6.000,
seg0.ts
#EXTINF:5.500,
seg1.ts
#EXTINF:4.200,
seg2.ts
#EXT-X-ENDLIST`

	m, err := parseHLSManifest(content)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Segments) != 3 {
		t.Fatalf("expected 3 segments, got %d", len(m.Segments))
	}
	if m.Segments[0].Name != "seg0.ts" {
		t.Fatalf("expected seg0.ts, got %s", m.Segments[0].Name)
	}
	if m.Segments[1].Duration != 5.5 {
		t.Fatalf("expected duration 5.5, got %f", m.Segments[1].Duration)
	}
	if m.Segments[2].Name != "seg2.ts" {
		t.Fatalf("expected seg2.ts, got %s", m.Segments[2].Name)
	}
}

func TestParseHLSManifest_Empty(t *testing.T) {
	m, err := parseHLSManifest("")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Segments) != 0 {
		t.Fatalf("expected 0 segments, got %d", len(m.Segments))
	}
	if m.TargetDuration != 6 {
		t.Fatalf("expected default target duration 6, got %d", m.TargetDuration)
	}
}

func TestParseHLSManifest_TargetDuration(t *testing.T) {
	content := `#EXTM3U
#EXT-X-TARGETDURATION:10
#EXTINF:9.000,
chunk.ts`

	m, err := parseHLSManifest(content)
	if err != nil {
		t.Fatal(err)
	}
	if m.TargetDuration != 10 {
		t.Fatalf("expected 10, got %d", m.TargetDuration)
	}
}

func TestParseHLSManifest_QueryParams(t *testing.T) {
	content := `#EXTM3U
#EXT-X-TARGETDURATION:6
#EXTINF:6.000,
seg0.ts?token=abc123&expires=999
#EXTINF:6.000,
seg1.ts?v=2`

	m, err := parseHLSManifest(content)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Segments) != 2 {
		t.Fatalf("expected 2 segments, got %d", len(m.Segments))
	}
	if m.Segments[0].Name != "seg0.ts" {
		t.Fatalf("expected query params stripped, got %s", m.Segments[0].Name)
	}
	if m.Segments[1].Name != "seg1.ts" {
		t.Fatalf("expected query params stripped, got %s", m.Segments[1].Name)
	}
}

func TestParseHLSManifest_SubdirPaths(t *testing.T) {
	content := `#EXTM3U
#EXT-X-TARGETDURATION:6
#EXTINF:6.000,
segments/chunk000.ts
#EXTINF:6.000,
segments/chunk001.ts`

	m, err := parseHLSManifest(content)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Segments) != 2 {
		t.Fatalf("expected 2, got %d", len(m.Segments))
	}
	if m.Segments[0].Name != "chunk000.ts" {
		t.Fatalf("expected base name extracted, got %s", m.Segments[0].Name)
	}
}
