package control

import (
	"testing"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// The pointer-helper family converts scalars to optional proto fields. The
// "IfNotEmpty"/"IfNonZero" variants must return nil for the zero value (so the
// field serializes as absent), while the unconditional ones always allocate.
func TestPointerHelpers(t *testing.T) {
	if stringPtrIfNotEmpty("") != nil {
		t.Error("stringPtrIfNotEmpty(\"\") should be nil")
	}
	if got := stringPtrIfNotEmpty("x"); got == nil || *got != "x" {
		t.Errorf("stringPtrIfNotEmpty(x) = %v", got)
	}
	if uint64PtrIfNonZero(0) != nil {
		t.Error("uint64PtrIfNonZero(0) should be nil")
	}
	if got := uint64PtrIfNonZero(7); got == nil || *got != 7 {
		t.Errorf("uint64PtrIfNonZero(7) = %v", got)
	}
	if got := uint32Ptr(3); got == nil || *got != 3 {
		t.Errorf("uint32Ptr(3) = %v", got)
	}
	if got := int32Ptr(-3); got == nil || *got != -3 {
		t.Errorf("int32Ptr(-3) = %v", got)
	}
	if got := int64Ptr(9); got == nil || *got != 9 {
		t.Errorf("int64Ptr(9) = %v", got)
	}
	if got := boolPtr(true); got == nil || !*got {
		t.Errorf("boolPtr(true) = %v", got)
	}
}

// IngestMode.String is the wire vocabulary in ResolveStreamContextResponse. Each
// typed mode maps to its exact wire token; an out-of-range value yields "invalid"
// so a corrupt mode never silently reads as a real one.
func TestIngestModeString(t *testing.T) {
	cases := map[IngestMode]string{
		IngestPush:       "push",
		IngestPull:       "pull",
		IngestMistNative: "mist_native",
		IngestMode(0):    "invalid",
		IngestMode(99):   "invalid",
	}
	for mode, want := range cases {
		if got := mode.String(); got != want {
			t.Errorf("IngestMode(%d).String() = %q, want %q", mode, got, want)
		}
	}
}

// playbackArtifactTypeToProto maps the persisted artifact_type string to the
// proto enum, case/space-insensitively; anything unrecognised is UNSPECIFIED.
func TestPlaybackArtifactTypeToProto(t *testing.T) {
	cases := map[string]ipcpb.ArtifactEvent_ArtifactType{
		"clip":    ipcpb.ArtifactEvent_ARTIFACT_TYPE_CLIP,
		" DVR ":   ipcpb.ArtifactEvent_ARTIFACT_TYPE_DVR,
		"Vod":     ipcpb.ArtifactEvent_ARTIFACT_TYPE_VOD,
		"unknown": ipcpb.ArtifactEvent_ARTIFACT_TYPE_UNSPECIFIED,
		"":        ipcpb.ArtifactEvent_ARTIFACT_TYPE_UNSPECIFIED,
	}
	for in, want := range cases {
		if got := playbackArtifactTypeToProto(in); got != want {
			t.Errorf("playbackArtifactTypeToProto(%q) = %v, want %v", in, got, want)
		}
	}
}

// isPlayableChapterState gates resolver entries: a chapter is playable only once
// it reaches finalized/frozen/reclaimed — the playback artifact hash is allocated
// earlier (at 'finalizing'), but the media file does not yet exist, so earlier
// states must not resolve.
func TestIsPlayableChapterState(t *testing.T) {
	for _, s := range []string{ChapterStateFinalized, ChapterStateFrozen, ChapterStateReclaimed} {
		if !isPlayableChapterState(s) {
			t.Errorf("state %q should be playable", s)
		}
	}
	for _, s := range []string{ChapterStateOpen, ChapterStateClosed, "finalizing", "", "bogus"} {
		if isPlayableChapterState(s) {
			t.Errorf("state %q should NOT be playable", s)
		}
	}
}

// chapterTerminalFailure decides retry-vs-fail from a Helmsman processing result.
// Only an affirmative source_missing signal (in outputs, or the error text) is
// terminal; everything else is transient (retry). A terminal source_missing
// carries a reason, defaulting when none is provided.
func TestChapterTerminalFailure(t *testing.T) {
	t.Run("source_missing via outputs is terminal with detail", func(t *testing.T) {
		terminal, reason := chapterTerminalFailure(map[string]string{
			"chapter_failure":        "source_missing",
			"chapter_failure_detail": "segments gone",
		}, "")
		if !terminal || reason != "segments gone" {
			t.Fatalf("got (%v,%q), want (true, segments gone)", terminal, reason)
		}
	})
	t.Run("source_missing without detail defaults the reason", func(t *testing.T) {
		terminal, reason := chapterTerminalFailure(map[string]string{"chapter_failure": "source_missing"}, "")
		if !terminal || reason == "" {
			t.Fatalf("got (%v,%q), want terminal with a default reason", terminal, reason)
		}
	})
	t.Run("source_missing in error text is terminal", func(t *testing.T) {
		terminal, _ := chapterTerminalFailure(nil, "pull failed: SOURCE_MISSING after retries")
		if !terminal {
			t.Fatal("source_missing in errMsg should be terminal")
		}
	})
	t.Run("other failure is transient", func(t *testing.T) {
		terminal, reason := chapterTerminalFailure(map[string]string{"chapter_failure": "timeout"}, "network blip")
		if terminal || reason != "" {
			t.Fatalf("got (%v,%q), want (false, \"\")", terminal, reason)
		}
	})
}
