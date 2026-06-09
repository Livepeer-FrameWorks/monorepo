package handlers

import (
	"reflect"
	"strings"
	"testing"
)

// parseSegmentURLs decodes newline-separated "rel=url" pairs and skips garbage.
func TestParseSegmentURLs(t *testing.T) {
	if got := parseSegmentURLs(""); len(got) != 0 {
		t.Fatalf("empty input = %#v, want empty map", got)
	}
	got := parseSegmentURLs("seg0.ts=https://s3/seg0\nseg1.ts=https://s3/seg1\ngarbage-no-eq\nk=v=with=eq")
	want := map[string]string{
		"seg0.ts": "https://s3/seg0",
		"seg1.ts": "https://s3/seg1",
		"k":       "v=with=eq", // SplitN(2) keeps the rest of the value intact
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseSegmentURLs = %#v, want %#v", got, want)
	}
}

// rewriteHLSManifestBody is the playback-correctness core: every mapped segment
// and tag URI must be remapped to its presigned URL, unmapped references left
// verbatim, and tags without a URI / blank lines preserved.
func TestRewriteHLSManifestBody(t *testing.T) {
	manifest := strings.Join([]string{
		"#EXTM3U",
		`#EXT-X-MAP:URI="init.mp4"`,
		"#EXTINF:6.0,",
		"seg0.ts",
		"seg1.ts", // intentionally unmapped
		"",
	}, "\n")

	segmentURLs := map[string]string{
		"init.mp4": "https://s3/init",
		"seg0.ts":  "https://s3/seg0",
	}

	got := rewriteHLSManifestBody(manifest, segmentURLs)

	if !strings.Contains(got, `#EXT-X-MAP:URI="https://s3/init"`) {
		t.Errorf("tag URI not remapped:\n%s", got)
	}
	if !strings.Contains(got, "https://s3/seg0\n") {
		t.Errorf("mapped segment not remapped:\n%s", got)
	}
	if !strings.Contains(got, "seg1.ts\n") {
		t.Errorf("unmapped segment must be left verbatim:\n%s", got)
	}
	if !strings.Contains(got, "#EXTM3U\n") || !strings.Contains(got, "#EXTINF:6.0,\n") {
		t.Errorf("non-URI tags must be preserved:\n%s", got)
	}
	// The original local segment name must NOT survive once it has a mapping.
	if strings.Contains(got, "\nseg0.ts\n") {
		t.Errorf("mapped segment leaked its local name:\n%s", got)
	}
}
