package state

import (
	"testing"

	"github.com/sirupsen/logrus/hooks/test"
)

// captureStateLogs installs a capturing logger as the package stateLogger for
// the duration of the test and restores the previous one afterwards.
func captureStateLogs(t *testing.T) *test.Hook {
	t.Helper()
	logger, hook := test.NewNullLogger()
	prev := stateLogger
	SetLogger(logger)
	t.Cleanup(func() { stateLogger = prev })
	return hook
}

// TestExtractTracksFromDetails_WellFormed pins the happy path: a correctly typed
// MistServer track is parsed field-for-field and produces no warnings.
func TestExtractTracksFromDetails_WellFormed(t *testing.T) {
	hook := captureStateLogs(t)

	tracks := extractTracksFromDetails(map[string]any{
		"video_0": map[string]any{
			"codec":  "H264",
			"kbits":  float64(4500),
			"fpks":   float64(30000),
			"height": float64(1080),
			"width":  float64(1920),
		},
		"issues": "none here", // top-level non-track key, skipped
	})

	if len(tracks) != 1 {
		t.Fatalf("tracks = %d, want 1", len(tracks))
	}
	tr := tracks[0]
	if tr.Codec != "H264" || tr.Type != "video" || tr.Bitrate != 4500 || tr.Height != 1080 || tr.Width != 1920 {
		t.Fatalf("track decoded wrong: %+v", tr)
	}
	if tr.FPS != 30.0 {
		t.Fatalf("FPS = %v, want 30", tr.FPS)
	}
	if len(hook.Entries) != 0 {
		t.Fatalf("well-formed input must not warn, got %d warnings: %+v", len(hook.Entries), hook.Entries)
	}
}

// TestExtractTracksFromDetails_AbsentOptionalNoWarn confirms that missing
// optional fields are not treated as anomalies — a track carrying only a codec
// parses cleanly and produces no warnings.
func TestExtractTracksFromDetails_AbsentOptionalNoWarn(t *testing.T) {
	hook := captureStateLogs(t)

	tracks := extractTracksFromDetails(map[string]any{
		"audio_0": map[string]any{"codec": "AAC"},
	})

	if len(tracks) != 1 || tracks[0].Type != "audio" {
		t.Fatalf("unexpected tracks: %+v", tracks)
	}
	if len(hook.Entries) != 0 {
		t.Fatalf("absent optional fields must not warn, got %+v", hook.Entries)
	}
}

// TestExtractTracksFromDetails_WrongTypeWarns pins the hardening contract: a
// field that is present but of the wrong type is dropped AND surfaced as a
// warning rather than silently swallowed. The track is still emitted with the
// malformed fields left at zero.
func TestExtractTracksFromDetails_WrongTypeWarns(t *testing.T) {
	hook := captureStateLogs(t)

	tracks := extractTracksFromDetails(map[string]any{
		"video_0": map[string]any{
			"codec": float64(264),  // should be string
			"kbits": "4500",        // should be float64
			"width": float64(1920), // valid
		},
	})

	if len(tracks) != 1 {
		t.Fatalf("tracks = %d, want 1 (malformed fields dropped, track kept)", len(tracks))
	}
	tr := tracks[0]
	if tr.Codec != "" || tr.Bitrate != 0 {
		t.Fatalf("malformed fields should be zero, got %+v", tr)
	}
	if tr.Width != 1920 {
		t.Fatalf("valid field should survive, Width = %d", tr.Width)
	}
	if len(hook.Entries) != 2 {
		t.Fatalf("expected 2 malformed-field warnings (codec, kbits), got %d: %+v", len(hook.Entries), hook.Entries)
	}
}

// TestExtractTracksFromDetails_MalformedTrackObjectWarns confirms that a
// top-level non-"issues" key whose value is not a track object is reported, not
// silently skipped.
func TestExtractTracksFromDetails_MalformedTrackObjectWarns(t *testing.T) {
	hook := captureStateLogs(t)

	tracks := extractTracksFromDetails(map[string]any{
		"video_0": "not-an-object",
	})

	if len(tracks) != 0 {
		t.Fatalf("malformed track object should yield no tracks, got %+v", tracks)
	}
	if len(hook.Entries) != 1 {
		t.Fatalf("expected 1 warning for malformed track object, got %d: %+v", len(hook.Entries), hook.Entries)
	}
}

// TestDetailString pins the absent/correct/wrong-type contract of the typed
// reader used for both track fields and the top-level "issues" field: absent is
// silent, correct returns the value, wrong-type returns false AND warns.
func TestDetailString(t *testing.T) {
	hook := captureStateLogs(t)
	m := map[string]any{"present": "ok", "wrong": float64(1)}

	if v, ok := detailString(m, "absent", "stream"); ok || v != "" {
		t.Fatalf("absent: got (%q, %v)", v, ok)
	}
	if v, ok := detailString(m, "present", "stream"); !ok || v != "ok" {
		t.Fatalf("present: got (%q, %v)", v, ok)
	}
	if len(hook.Entries) != 0 {
		t.Fatalf("absent/correct must not warn yet, got %+v", hook.Entries)
	}
	if v, ok := detailString(m, "wrong", "stream"); ok || v != "" {
		t.Fatalf("wrong-type: got (%q, %v)", v, ok)
	}
	if len(hook.Entries) != 1 {
		t.Fatalf("wrong-type must warn once, got %d", len(hook.Entries))
	}
}
