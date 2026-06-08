package handlers

import (
	"os"
	"path/filepath"
	"testing"

	"frameworks/api_sidecar/internal/leases"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// TestFindStagedProcessingSource pins the staged-input discovery contract: the
// HLS manifest (.m3u8) wins over the unsafe-wrapper formats when both exist
// (legacy precedence), a candidate must be a regular file with non-zero size
// (an empty placeholder is skipped), and a hash with nothing on disk returns "".
func TestFindStagedProcessingSource(t *testing.T) {
	const hash = "abc123"

	t.Run("missing returns empty", func(t *testing.T) {
		dir := t.TempDir()
		if got := findStagedProcessingSource(dir, hash); got != "" {
			t.Fatalf("got %q, want empty", got)
		}
	})

	t.Run("single flv is found", func(t *testing.T) {
		dir := t.TempDir()
		flv := filepath.Join(dir, hash+".flv")
		writeFile(t, flv, "data")
		if got := findStagedProcessingSource(dir, hash); got != flv {
			t.Fatalf("got %q, want %q", got, flv)
		}
	})

	t.Run("m3u8 wins over flv when both present", func(t *testing.T) {
		dir := t.TempDir()
		m3u8 := filepath.Join(dir, hash+".m3u8")
		writeFile(t, m3u8, "#EXTM3U")
		writeFile(t, filepath.Join(dir, hash+".flv"), "data")
		if got := findStagedProcessingSource(dir, hash); got != m3u8 {
			t.Fatalf("got %q, want %q (.m3u8 has priority)", got, m3u8)
		}
	})

	t.Run("zero-byte higher-priority candidate is skipped", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, hash+".m3u8"), "") // empty → not a usable source
		flv := filepath.Join(dir, hash+".flv")
		writeFile(t, flv, "data")
		if got := findStagedProcessingSource(dir, hash); got != flv {
			t.Fatalf("got %q, want %q (empty .m3u8 must be skipped)", got, flv)
		}
	})
}

// TestPreferredHeatPath pins where HeatTracker records LRU touches: a clip with
// both a stream name and a media extension resolves to the nested clip-writer
// layout (clips/<stream>/<hash><ext>); every other shape falls back to the
// first deterministic path, and an empty path list yields "".
func TestPreferredHeatPath(t *testing.T) {
	base := "/var/media"
	cases := []struct {
		name       string
		key        leases.AssetKey
		mediaExt   string
		streamName string
		paths      []string
		want       string
	}{
		{
			name:       "clip with stream and ext uses nested layout",
			key:        leases.AssetKey{Type: "clip", Hash: "h1"},
			mediaExt:   ".mp4",
			streamName: "live+s1",
			paths:      []string{"/var/media/flat/h1.mp4"},
			want:       filepath.Join(base, "clips", "live+s1", "h1.mp4"),
		},
		{
			name:       "clip without stream falls back to first path",
			key:        leases.AssetKey{Type: "clip", Hash: "h1"},
			mediaExt:   ".mp4",
			streamName: "",
			paths:      []string{"/var/media/flat/h1.mp4"},
			want:       "/var/media/flat/h1.mp4",
		},
		{
			name:     "non-clip falls back to first path",
			key:      leases.AssetKey{Type: "vod", Hash: "h2"},
			mediaExt: ".mp4",
			paths:    []string{"/var/media/flat/h2.mp4", "/var/media/other"},
			want:     "/var/media/flat/h2.mp4",
		},
		{
			name: "no paths yields empty",
			key:  leases.AssetKey{Type: "vod", Hash: "h3"},
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := preferredHeatPath(base, tc.key, tc.mediaExt, tc.streamName, tc.paths)
			if got != tc.want {
				t.Fatalf("preferredHeatPath = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestProcessingRecordingEndEvent pins the proto→struct field mapping for the
// RECORDING_END payload. A transposed field here would silently corrupt the
// downstream finalization decision, so every scalar is asserted against a
// distinct value and the nested track conversion is checked for arity.
func TestProcessingRecordingEndEvent(t *testing.T) {
	exitReason := "CLEAN_EOF"
	humanExitReason := "output finished cleanly"
	width := int32(1280)
	height := int32(720)

	rec := &ipcpb.RecordingCompleteTrigger{
		StreamName:      "live+s1",
		FilePath:        "/rec/s1.mkv",
		OutputProtocol:  "mkv",
		BytesWritten:    1234,
		SecondsWriting:  56,
		TimeStarted:     1000,
		TimeEnded:       1100,
		MediaDurationMs: 99000,
		ExitReason:      &exitReason,
		HumanExitReason: &humanExitReason,
		Tracks: []*ipcpb.StreamTrack{{
			TrackType: "video",
			Codec:     "H264",
			Width:     &width,
			Height:    &height,
		}},
	}

	got := processingRecordingEndEvent(rec)

	if got.StreamName != "live+s1" {
		t.Errorf("StreamName = %q", got.StreamName)
	}
	if got.FilePath != "/rec/s1.mkv" {
		t.Errorf("FilePath = %q", got.FilePath)
	}
	if got.OutputProtocol != "mkv" {
		t.Errorf("OutputProtocol = %q", got.OutputProtocol)
	}
	if got.BytesWritten != 1234 {
		t.Errorf("BytesWritten = %d", got.BytesWritten)
	}
	if got.SecondsWriting != 56 {
		t.Errorf("SecondsWriting = %d", got.SecondsWriting)
	}
	if got.TimeStarted != 1000 || got.TimeEnded != 1100 {
		t.Errorf("Time range = [%d,%d]", got.TimeStarted, got.TimeEnded)
	}
	if got.MediaDurationMs != 99000 {
		t.Errorf("MediaDurationMs = %d", got.MediaDurationMs)
	}
	if got.ExitReason != exitReason || got.HumanExitReason != humanExitReason {
		t.Errorf("exit reasons = %q / %q", got.ExitReason, got.HumanExitReason)
	}
	if len(got.Tracks) != 1 || got.Tracks[0].height != 720 {
		t.Fatalf("tracks not mapped: %+v", got.Tracks)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
