package resolvers

import (
	"testing"

	"frameworks/api_gateway/graph/model"
)

// chapterModeToString and ChapterModeFromString must round-trip for the two
// concrete modes; "none" is the canonical empty wire value.
func TestChapterModeStringRoundTrip(t *testing.T) {
	tests := []struct {
		mode model.DVRChapterMode
		wire string
	}{
		{model.DVRChapterModeWindowSized, "window_sized_chapters"},
		{model.DVRChapterModeFixedInterval, "fixed_interval"},
		{model.DVRChapterModeNone, ""},
	}
	for _, tt := range tests {
		if got := chapterModeToString(tt.mode); got != tt.wire {
			t.Errorf("chapterModeToString(%v) = %q, want %q", tt.mode, got, tt.wire)
		}
		if got := chapterModeFromString(tt.wire); got != tt.mode {
			t.Errorf("chapterModeFromString(%q) = %v, want %v", tt.wire, got, tt.mode)
		}
	}
	// Unknown wire string falls back to None, not a concrete mode.
	if got := chapterModeFromString("bogus"); got != model.DVRChapterModeNone {
		t.Errorf("chapterModeFromString(bogus) = %v, want None", got)
	}
}

func TestChapterStateFromString(t *testing.T) {
	tests := map[string]model.DVRChapterState{
		"open":                  model.DVRChapterStateOpen,
		"closed":                model.DVRChapterStateClosed,
		"finalizing":            model.DVRChapterStateFinalizing,
		"finalized":             model.DVRChapterStateFinalized,
		"frozen":                model.DVRChapterStateFrozen,
		"reclaimed":             model.DVRChapterStateReclaimed,
		"failed_source_missing": model.DVRChapterStateFailedSourceMissing,
		"failed_permanent":      model.DVRChapterStateFailedPermanent,
	}
	for wire, want := range tests {
		if got := chapterStateFromString(wire); got != want {
			t.Errorf("chapterStateFromString(%q) = %v, want %v", wire, got, want)
		}
	}
	// Unknown state defaults to Open (treated as not-yet-playable, the safe default).
	if got := chapterStateFromString("???"); got != model.DVRChapterStateOpen {
		t.Errorf("chapterStateFromString(unknown) = %v, want Open", got)
	}
}

// chapterPlayableNow gates whether a chapter can be served: only terminal states
// where bytes are durably available may be played. A false-positive here would
// hand the player a chapter that isn't finished writing.
func TestChapterPlayableNow(t *testing.T) {
	playable := []model.DVRChapterState{
		model.DVRChapterStateFinalized,
		model.DVRChapterStateFrozen,
		model.DVRChapterStateReclaimed,
	}
	for _, s := range playable {
		if !chapterPlayableNow(s) {
			t.Errorf("chapterPlayableNow(%v) = false, want true", s)
		}
	}
	notPlayable := []model.DVRChapterState{
		model.DVRChapterStateOpen,
		model.DVRChapterStateClosed,
		model.DVRChapterStateFinalizing,
		model.DVRChapterStateFailedSourceMissing,
		model.DVRChapterStateFailedPermanent,
	}
	for _, s := range notPlayable {
		if chapterPlayableNow(s) {
			t.Errorf("chapterPlayableNow(%v) = true, want false", s)
		}
	}
}
