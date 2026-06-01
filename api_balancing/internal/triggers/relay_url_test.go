package triggers

import (
	"testing"
)

func TestSelectArtifactRelayFormatPrefersPersistedDescriptor(t *testing.T) {
	got := selectArtifactRelayFormat(artifactDescriptor{Format: "mp4"}, "webm")
	if got != "mp4" {
		t.Fatalf("expected persisted format mp4, got %q", got)
	}
}

func TestSelectArtifactRelayFormatFallsBackToWarmStateWhenDescriptorEmpty(t *testing.T) {
	got := selectArtifactRelayFormat(artifactDescriptor{}, "mkv")
	if got != "mkv" {
		t.Fatalf("expected warm format mkv, got %q", got)
	}
}

func TestRelayArtifactPath_ClipRequiresStream(t *testing.T) {
	// A clip with no stream must yield "" so the caller aborts STREAM_SOURCE —
	// Helmsman 404s a flat clip path.
	if got := relayArtifactPath("clip", "abc", ".mp4", ""); got != "" {
		t.Fatalf("clip with empty stream must return \"\", got %q", got)
	}
	// With a stream it nests, and escapes the stream segment.
	if got := relayArtifactPath("clip", "abc", ".mp4", "live+s1"); got != "/internal/artifact/clip/live+s1/abc.mp4" {
		t.Fatalf("nested clip path = %q", got)
	}
	if got := relayArtifactPath("clip", "abc", ".mp4", "a b/c"); got != "/internal/artifact/clip/a%20b%2Fc/abc.mp4" {
		t.Fatalf("clip stream segment must be path-escaped, got %q", got)
	}
}

func TestRelayArtifactPath_VODAndUploadAreFlat(t *testing.T) {
	if got := relayArtifactPath("vod", "abc", ".mp4", ""); got != "/internal/artifact/vod/abc.mp4" {
		t.Fatalf("vod path = %q", got)
	}
	// VOD ignores any stream value (flat layout).
	if got := relayArtifactPath("vod", "abc", ".mkv", "ignored"); got != "/internal/artifact/vod/abc.mkv" {
		t.Fatalf("vod path = %q", got)
	}
	if got := relayArtifactPath("upload", "h", ".mp4", ""); got != "/internal/artifact/upload/h.mp4" {
		t.Fatalf("upload path = %q", got)
	}
}

func TestNormalizeExtAddsLeadingDot(t *testing.T) {
	if got := normalizeExt("mkv"); got != ".mkv" {
		t.Fatalf("expected .mkv, got %q", got)
	}
	if got := normalizeExt(".MP4"); got != ".mp4" {
		t.Fatalf("expected .mp4, got %q", got)
	}
	if got := normalizeExt(""); got != "" {
		t.Fatalf("expected empty, got %q", got)
	}
}
