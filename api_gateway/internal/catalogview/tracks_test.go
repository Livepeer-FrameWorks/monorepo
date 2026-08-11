package catalogview

import (
	"testing"

	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
)

func strp(s string) *string { return &s }
func i32p(v int32) *int32   { return &v }

// TrackSummary takes the FIRST video track for resolution/codec/bitrate and the FIRST audio track
// for the audio codec, leaving absent fields nil.
func TestTrackSummary_FirstVideoAndAudio(t *testing.T) {
	res, vc, ac, br := TrackSummary([]*commodorepb.MediaTrack{
		{Type: "video", Codec: "h264", Resolution: strp("1920x1080"), BitrateKbps: i32p(2500)},
		{Type: "video", Codec: "vp9", Resolution: strp("640x360"), BitrateKbps: i32p(800)},
		{Type: "audio", Codec: "aac"},
		{Type: "audio", Codec: "opus"},
	})
	if res == nil || *res != "1920x1080" {
		t.Fatalf("resolution: got %v", res)
	}
	if vc == nil || *vc != "h264" {
		t.Fatalf("videoCodec: got %v", vc)
	}
	if br == nil || *br != 2500 {
		t.Fatalf("bitrate: got %v", br)
	}
	if ac == nil || *ac != "aac" {
		t.Fatalf("audioCodec (first audio track): got %v", ac)
	}
}

func TestTrackSummary_EmptyAndAudioOnly(t *testing.T) {
	if res, vc, ac, br := TrackSummary(nil); res != nil || vc != nil || ac != nil || br != nil {
		t.Fatalf("empty tracks must yield all-nil, got %v %v %v %v", res, vc, ac, br)
	}
	res, vc, ac, br := TrackSummary([]*commodorepb.MediaTrack{{Type: "audio", Codec: "aac"}})
	if res != nil || vc != nil || br != nil {
		t.Fatalf("audio-only must leave video fields nil, got %v %v %v", res, vc, br)
	}
	if ac == nil || *ac != "aac" {
		t.Fatalf("audioCodec: got %v", ac)
	}
}
