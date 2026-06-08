package grpc

import (
	"testing"

	"frameworks/api_balancing/internal/state"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
)

// i64 is declared in clip_lifecycle_data_test.go (same package).

// resolveClipAbsoluteRangeMs normalizes the five ClipMode request shapes into an
// absolute [startMs, endMs) window, or errors when the inputs are incomplete.
// Each mode has its own required-field contract; this pins the success arithmetic
// and every required-field rejection. Modes that anchor to media time
// (RELATIVE, DURATION+start_ms) need the stream's recorded StartedAt and must
// error when it's absent. The final guard rejects any non-positive range.
func TestResolveClipAbsoluteRangeMs(t *testing.T) {
	t.Run("ABSOLUTE start+stop", func(t *testing.T) {
		start, end, err := resolveClipAbsoluteRangeMs(&sharedpb.CreateClipRequest{
			Mode: sharedpb.ClipMode_CLIP_MODE_ABSOLUTE, StartUnix: i64(1000), StopUnix: i64(1030),
		}, "s")
		if err != nil || start != 1000_000 || end != 1030_000 {
			t.Fatalf("got (%d,%d,%v), want (1000000,1030000,nil)", start, end, err)
		}
	})
	t.Run("ABSOLUTE start+duration", func(t *testing.T) {
		start, end, err := resolveClipAbsoluteRangeMs(&sharedpb.CreateClipRequest{
			Mode: sharedpb.ClipMode_CLIP_MODE_ABSOLUTE, StartUnix: i64(1000), DurationSec: i64(15),
		}, "s")
		if err != nil || start != 1000_000 || end != 1015_000 {
			t.Fatalf("got (%d,%d,%v), want (1000000,1015000,nil)", start, end, err)
		}
	})
	t.Run("ABSOLUTE missing start_unix", func(t *testing.T) {
		if _, _, err := resolveClipAbsoluteRangeMs(&sharedpb.CreateClipRequest{Mode: sharedpb.ClipMode_CLIP_MODE_ABSOLUTE}, "s"); err == nil {
			t.Fatal("want error for missing start_unix")
		}
	})
	t.Run("ABSOLUTE missing stop and duration", func(t *testing.T) {
		if _, _, err := resolveClipAbsoluteRangeMs(&sharedpb.CreateClipRequest{Mode: sharedpb.ClipMode_CLIP_MODE_ABSOLUTE, StartUnix: i64(1000)}, "s"); err == nil {
			t.Fatal("want error for missing stop/duration")
		}
	})

	t.Run("RELATIVE anchors to StartedAt", func(t *testing.T) {
		anchor := seedLiveStreamStart(t, "live+rel")
		start, end, err := resolveClipAbsoluteRangeMs(&sharedpb.CreateClipRequest{
			Mode: sharedpb.ClipMode_CLIP_MODE_RELATIVE, StartMs: i64(5), StopMs: i64(20),
		}, "live+rel")
		if err != nil || start != anchor+5_000 || end != anchor+20_000 {
			t.Fatalf("got (%d,%d,%v), want (%d,%d,nil)", start, end, err, anchor+5_000, anchor+20_000)
		}
	})
	t.Run("RELATIVE start+duration", func(t *testing.T) {
		anchor := seedLiveStreamStart(t, "live+rel2")
		start, end, err := resolveClipAbsoluteRangeMs(&sharedpb.CreateClipRequest{
			Mode: sharedpb.ClipMode_CLIP_MODE_RELATIVE, StartMs: i64(5), DurationSec: i64(10),
		}, "live+rel2")
		if err != nil || start != anchor+5_000 || end-start != 10_000 {
			t.Fatalf("got (%d,%d,%v), want start=%d dur=10000", start, end, err, anchor+5_000)
		}
	})
	t.Run("RELATIVE missing start_ms", func(t *testing.T) {
		if _, _, err := resolveClipAbsoluteRangeMs(&sharedpb.CreateClipRequest{Mode: sharedpb.ClipMode_CLIP_MODE_RELATIVE}, "s"); err == nil {
			t.Fatal("want error for missing start_ms")
		}
	})
	t.Run("RELATIVE without recorded StartedAt errors", func(t *testing.T) {
		state.ResetDefaultManagerForTests() // no stream seeded → no StartedAt
		if _, _, err := resolveClipAbsoluteRangeMs(&sharedpb.CreateClipRequest{Mode: sharedpb.ClipMode_CLIP_MODE_RELATIVE, StartMs: i64(5), StopMs: i64(20)}, "missing"); err == nil {
			t.Fatal("want error when stream has no StartedAt")
		}
	})
	t.Run("RELATIVE missing stop and duration", func(t *testing.T) {
		anchor := seedLiveStreamStart(t, "live+rel3")
		_ = anchor
		if _, _, err := resolveClipAbsoluteRangeMs(&sharedpb.CreateClipRequest{Mode: sharedpb.ClipMode_CLIP_MODE_RELATIVE, StartMs: i64(5)}, "live+rel3"); err == nil {
			t.Fatal("want error for missing stop_ms/duration")
		}
	})

	t.Run("DURATION with start_unix", func(t *testing.T) {
		start, end, err := resolveClipAbsoluteRangeMs(&sharedpb.CreateClipRequest{
			Mode: sharedpb.ClipMode_CLIP_MODE_DURATION, StartUnix: i64(2000), DurationSec: i64(12),
		}, "s")
		if err != nil || start != 2000_000 || end != 2012_000 {
			t.Fatalf("got (%d,%d,%v), want (2000000,2012000,nil)", start, end, err)
		}
	})
	t.Run("DURATION with start_ms anchors to media", func(t *testing.T) {
		anchor := seedLiveStreamStart(t, "live+dur")
		start, end, err := resolveClipAbsoluteRangeMs(&sharedpb.CreateClipRequest{
			Mode: sharedpb.ClipMode_CLIP_MODE_DURATION, StartMs: i64(3), DurationSec: i64(8),
		}, "live+dur")
		if err != nil || start != anchor+3_000 || end-start != 8_000 {
			t.Fatalf("got (%d,%d,%v), want start=%d dur=8000", start, end, err, anchor+3_000)
		}
	})
	t.Run("DURATION non-positive duration errors", func(t *testing.T) {
		if _, _, err := resolveClipAbsoluteRangeMs(&sharedpb.CreateClipRequest{Mode: sharedpb.ClipMode_CLIP_MODE_DURATION, StartUnix: i64(1), DurationSec: i64(0)}, "s"); err == nil {
			t.Fatal("want error for zero duration")
		}
	})
	t.Run("DURATION missing start errors", func(t *testing.T) {
		if _, _, err := resolveClipAbsoluteRangeMs(&sharedpb.CreateClipRequest{Mode: sharedpb.ClipMode_CLIP_MODE_DURATION, DurationSec: i64(5)}, "s"); err == nil {
			t.Fatal("want error for missing start_unix/start_ms")
		}
	})
	t.Run("DURATION start_ms without StartedAt errors", func(t *testing.T) {
		state.ResetDefaultManagerForTests()
		if _, _, err := resolveClipAbsoluteRangeMs(&sharedpb.CreateClipRequest{Mode: sharedpb.ClipMode_CLIP_MODE_DURATION, StartMs: i64(3), DurationSec: i64(8)}, "missing"); err == nil {
			t.Fatal("want error when start_ms has no media anchor")
		}
	})

	t.Run("CLIP_NOW default offset spans duration", func(t *testing.T) {
		start, end, err := resolveClipAbsoluteRangeMs(&sharedpb.CreateClipRequest{
			Mode: sharedpb.ClipMode_CLIP_MODE_CLIP_NOW, DurationSec: i64(30),
		}, "s")
		if err != nil || end-start != 30_000 {
			t.Fatalf("got (%d,%d,%v), want a 30000ms window ending ~now", start, end, err)
		}
	})
	t.Run("CLIP_NOW requires duration", func(t *testing.T) {
		if _, _, err := resolveClipAbsoluteRangeMs(&sharedpb.CreateClipRequest{Mode: sharedpb.ClipMode_CLIP_MODE_CLIP_NOW}, "s"); err == nil {
			t.Fatal("want error for missing duration")
		}
	})

	t.Run("UNSPECIFIED legacy start+stop", func(t *testing.T) {
		start, end, err := resolveClipAbsoluteRangeMs(&sharedpb.CreateClipRequest{StartUnix: i64(500), StopUnix: i64(560)}, "s")
		if err != nil || start != 500_000 || end != 560_000 {
			t.Fatalf("got (%d,%d,%v), want (500000,560000,nil)", start, end, err)
		}
	})
	t.Run("UNSPECIFIED legacy start+duration", func(t *testing.T) {
		start, end, err := resolveClipAbsoluteRangeMs(&sharedpb.CreateClipRequest{StartUnix: i64(500), DurationSec: i64(20)}, "s")
		if err != nil || start != 500_000 || end != 520_000 {
			t.Fatalf("got (%d,%d,%v), want (500000,520000,nil)", start, end, err)
		}
	})
	t.Run("UNSPECIFIED missing stop and duration", func(t *testing.T) {
		if _, _, err := resolveClipAbsoluteRangeMs(&sharedpb.CreateClipRequest{StartUnix: i64(500)}, "s"); err == nil {
			t.Fatal("want error for legacy missing stop/duration")
		}
	})
	t.Run("UNSPECIFIED missing start errors", func(t *testing.T) {
		if _, _, err := resolveClipAbsoluteRangeMs(&sharedpb.CreateClipRequest{}, "s"); err == nil {
			t.Fatal("want error when no mode and no start_unix")
		}
	})

	t.Run("non-positive range after normalization", func(t *testing.T) {
		// stop before start → guard rejects.
		if _, _, err := resolveClipAbsoluteRangeMs(&sharedpb.CreateClipRequest{
			Mode: sharedpb.ClipMode_CLIP_MODE_ABSOLUTE, StartUnix: i64(1000), StopUnix: i64(900),
		}, "s"); err == nil {
			t.Fatal("want error for stop < start")
		}
	})
}

// seedLiveStreamStart registers a live stream so GetStreamState(...).StartedAt is
// populated, and returns the recorded anchor in Unix ms for assertions.
func seedLiveStreamStart(t *testing.T, internalName string) int64 {
	t.Helper()
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	if err := sm.UpdateStreamFromBuffer(internalName, internalName, "node-1", "tenant-1", "FULL", ""); err != nil {
		t.Fatalf("seed stream: %v", err)
	}
	ss := sm.GetStreamState(internalName)
	if ss == nil || ss.StartedAt == nil {
		t.Fatalf("expected StartedAt to be recorded for %s", internalName)
	}
	return ss.StartedAt.UnixMilli()
}

// mapArtifactStatusToVodStatus maps the persisted artifact status string onto the
// VOD proto enum. Unknown/empty statuses must fall through to UNSPECIFIED rather
// than masquerade as a known state.
func TestMapArtifactStatusToVodStatus(t *testing.T) {
	cases := map[string]sharedpb.VodStatus{
		"uploading":  sharedpb.VodStatus_VOD_STATUS_UPLOADING,
		"requested":  sharedpb.VodStatus_VOD_STATUS_UPLOADING,
		"processing": sharedpb.VodStatus_VOD_STATUS_PROCESSING,
		"completed":  sharedpb.VodStatus_VOD_STATUS_READY,
		"complete":   sharedpb.VodStatus_VOD_STATUS_READY,
		"done":       sharedpb.VodStatus_VOD_STATUS_READY,
		"ready":      sharedpb.VodStatus_VOD_STATUS_READY,
		"synced":     sharedpb.VodStatus_VOD_STATUS_READY,
		"failed":     sharedpb.VodStatus_VOD_STATUS_FAILED,
		"deleted":    sharedpb.VodStatus_VOD_STATUS_DELETED,
		"":           sharedpb.VodStatus_VOD_STATUS_UNSPECIFIED,
		"bogus":      sharedpb.VodStatus_VOD_STATUS_UNSPECIFIED,
	}
	for in, want := range cases {
		if got := mapArtifactStatusToVodStatus(in); got != want {
			t.Errorf("mapArtifactStatusToVodStatus(%q) = %v, want %v", in, got, want)
		}
	}
}

// vodUploadLastErrorCode classifies an artifact error message by VOD status. An
// empty message means "no error" (empty code) regardless of status; otherwise the
// code is failed/deleted-specific, with a generic fallback.
func TestVodUploadLastErrorCode(t *testing.T) {
	if got := vodUploadLastErrorCode(sharedpb.VodStatus_VOD_STATUS_FAILED, ""); got != "" {
		t.Errorf("empty message must yield empty code, got %q", got)
	}
	if got := vodUploadLastErrorCode(sharedpb.VodStatus_VOD_STATUS_FAILED, "boom"); got != "processing_failed" {
		t.Errorf("failed = %q, want processing_failed", got)
	}
	if got := vodUploadLastErrorCode(sharedpb.VodStatus_VOD_STATUS_DELETED, "gone"); got != "deleted" {
		t.Errorf("deleted = %q, want deleted", got)
	}
	if got := vodUploadLastErrorCode(sharedpb.VodStatus_VOD_STATUS_PROCESSING, "huh"); got != "artifact_error" {
		t.Errorf("other = %q, want artifact_error", got)
	}
}
