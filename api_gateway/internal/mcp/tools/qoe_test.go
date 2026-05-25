package tools

import (
	"testing"

	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
)

func TestSummarizeTrackInventoryIncludesExpectedDerivedTracks(t *testing.T) {
	trackCount := int32(5)
	primaryAudio := "aac"
	summary := summarizeTrackInventory([]*pb.StreamHealthMetric{
		{
			TrackCount:        &trackCount,
			PrimaryAudioCodec: &primaryAudio,
			TrackMetadata: `{"tracks":[
				{"track_type":"video","codec":"H264"},
				{"track_type":"audio","codec":"AAC"},
				{"track_type":"audio","codec":"Opus"},
				{"track_type":"video","codec":"JPEG"},
				{"track_type":"meta","codec":"thumbvtt"}
			]}`,
		},
	})

	for _, key := range []string{"video_codecs", "audio_codecs", "metadata_codecs", "expected_derived_codecs", "notes"} {
		if _, ok := summary[key]; !ok {
			t.Fatalf("summary missing %q: %#v", key, summary)
		}
	}
	if got := summary["max_track_count"]; got != int32(5) {
		t.Fatalf("max_track_count = %v, want 5", got)
	}
	assertContains(t, summary["expected_derived_codecs"].([]string), "AAC")
	assertContains(t, summary["expected_derived_codecs"].([]string), "JPEG")
	assertContains(t, summary["expected_derived_codecs"].([]string), "OPUS")
	assertContains(t, summary["expected_derived_codecs"].([]string), "THUMBVTT")
}

func assertContains(t *testing.T, values []string, want string) {
	t.Helper()
	for _, value := range values {
		if value == want {
			return
		}
	}
	t.Fatalf("%q not found in %#v", want, values)
}
