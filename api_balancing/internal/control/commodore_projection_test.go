package control

import (
	"math"
	"strings"
	"testing"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// TestMarshalRecordingTracks pins the durable-track marshaling used by the completed
// processing-result path: it ALWAYS returns valid JSON ("[]" for empty — the caller decides
// replace-vs-preserve via tracks_present, not emptiness), and a real set → the durable
// MediaTrack JSON carrying only the persistent A/V descriptors.
func TestMarshalRecordingTracks(t *testing.T) {
	t.Run("empty returns [] (not nil)", func(t *testing.T) {
		got, err := marshalRecordingTracks(nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "[]" {
			t.Fatalf("expected [] for empty tracks, got %q", got)
		}
	})

	t.Run("maps durable A/V descriptors", func(t *testing.T) {
		w, h := int32(1920), int32(1080)
		res := "1920x1080"
		ch, sr := int32(2), int32(48000)
		tracks := []*ipcpb.StreamTrack{
			{TrackType: "video", Codec: "h264", Width: &w, Height: &h, Resolution: &res},
			{TrackType: "audio", Codec: "aac", Channels: &ch, SampleRate: &sr},
		}
		got, err := marshalRecordingTracks(tracks)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		for _, want := range []string{`"type":"video"`, `"codec":"h264"`, `"resolution":"1920x1080"`, `"type":"audio"`, `"codec":"aac"`} {
			if !strings.Contains(got, want) {
				t.Errorf("marshaled tracks %q missing %q", got, want)
			}
		}
	})
}

// A non-finite fps (NaN/±Inf) must NOT fail track serialization — encoding/json rejects non-finite
// floats, which would otherwise drop the whole authoritative track set at completion. The value is
// sanitized (omitted) and the rest of the track survives.
func TestMarshalRecordingTracks_NonFiniteFpsIsSanitized(t *testing.T) {
	inf := math.Inf(1)
	nan := math.NaN()
	for name, bad := range map[string]float64{"inf": inf, "nan": nan} {
		t.Run(name, func(t *testing.T) {
			fps := bad
			codec := "h264"
			got, err := marshalRecordingTracks([]*ipcpb.StreamTrack{
				{TrackType: "video", Codec: codec, Fps: &fps},
			})
			if err != nil {
				t.Fatalf("non-finite fps must not fail serialization, got: %v", err)
			}
			// The track is preserved (codec present); the bad fps is dropped, not serialized.
			if !strings.Contains(got, `"codec":"h264"`) {
				t.Fatalf("track dropped instead of sanitized: %q", got)
			}
			if strings.Contains(got, "Inf") || strings.Contains(got, "NaN") {
				t.Fatalf("non-finite fps leaked into JSON: %q", got)
			}
		})
	}
}
