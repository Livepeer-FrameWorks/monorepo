package control

import "testing"

// isArtifactHashCandidate gates whether a playback input is routed through the
// DVR-hash resolver. It must accept ONLY 32-char hex: a false positive sends a
// real playback_id down the hash path (404), a false negative skips a valid
// hash. Length and the hex alphabet (both cases) are the whole contract.
func TestIsArtifactHashCandidate(t *testing.T) {
	const hash32 = "0123456789abcdef0123456789abcdef"
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"32 lowercase hex", hash32, true},
		{"32 uppercase hex", "0123456789ABCDEF0123456789ABCDEF", true},
		{"31 chars too short", hash32[:31], false},
		{"33 chars too long", hash32 + "a", false},
		{"32 chars with non-hex g", "0123456789abcdef0123456789abcdeg", false},
		{"empty", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isArtifactHashCandidate(tt.in); got != tt.want {
				t.Errorf("isArtifactHashCandidate(%q) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// RoutingInternalName injects the pull+ namespace prefix for pull-mode streams
// so downstream resolvers see the Mist identity. It must inject ONLY for pull
// mode, ONLY when no namespace prefix is already present, and never panic on a
// nil resolution. Getting this wrong routes a pull stream as if it were a
// concrete config (no remote-origin discovery).
func TestRoutingInternalName(t *testing.T) {
	tests := []struct {
		name string
		r    *ContentResolution
		want string
	}{
		{"nil receiver", nil, ""},
		{"pull bare name injects prefix", &ContentResolution{IngestMode: "pull", InternalName: "stream1"}, "pull+stream1"},
		{"pull already prefixed unchanged", &ContentResolution{IngestMode: "pull", InternalName: "pull+stream1"}, "pull+stream1"},
		{"pull with live+ prefix unchanged", &ContentResolution{IngestMode: "pull", InternalName: "live+stream1"}, "live+stream1"},
		{"push mode unchanged", &ContentResolution{IngestMode: "push", InternalName: "stream1"}, "stream1"},
		{"pull mode case-insensitive injects", &ContentResolution{IngestMode: "PULL", InternalName: "stream1"}, "pull+stream1"},
		{"pull empty name stays empty", &ContentResolution{IngestMode: "pull", InternalName: "  "}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.r.RoutingInternalName(); got != tt.want {
				t.Errorf("RoutingInternalName() = %q, want %q", got, tt.want)
			}
		})
	}
}

// ResolvePlaybackPolicyTarget trims its inputs and, absent a chapter mapping,
// passes them through unchanged. With no Commodore client and no DB wired, the
// chapter-rewrite hops are no-ops, so this pins the non-chapter contract: the
// policy target is the (trimmed) input, never silently rewritten. Chapter
// rewriting is covered separately where Commodore is mocked.
func TestResolvePlaybackPolicyTarget_NonChapterPassthrough(t *testing.T) {
	// The chapter hops read package-level CommodoreClient/db; force the
	// no-mapping path and restore prior wiring so we don't disturb other tests.
	origCommodore, origDB := CommodoreClient, db
	CommodoreClient, db = nil, nil
	t.Cleanup(func() { CommodoreClient, db = origCommodore, origDB })

	got := ResolvePlaybackPolicyTarget(t.Context(), "  pid-123  ", "\tvod+stream1\n")
	if got.ContentID != "pid-123" {
		t.Errorf("ContentID = %q, want trimmed pid-123", got.ContentID)
	}
	if got.InternalName != "vod+stream1" {
		t.Errorf("InternalName = %q, want trimmed vod+stream1", got.InternalName)
	}
}
