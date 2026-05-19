package triggers

import "testing"

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
