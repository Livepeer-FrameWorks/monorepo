package catalogview

import (
	"testing"

	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
)

func strp(s string) *string { return &s }
func i32p(v int32) *int32   { return &v }

// Among original tracks, the tallest video supplies resolution/codec/bitrate and the first audio
// track supplies the audio codec.
func TestTrackSummary_OriginalVideoAndAudio(t *testing.T) {
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

func assertSummary(t *testing.T, tracks []*commodorepb.MediaTrack, wantRes, wantVideo, wantAudio string) {
	t.Helper()
	res, vc, ac, _ := TrackSummary(tracks)
	if got := deref(res); got != wantRes {
		t.Errorf("resolution: got %q, want %q", got, wantRes)
	}
	if got := deref(vc); got != wantVideo {
		t.Errorf("videoCodec: got %q, want %q", got, wantVideo)
	}
	if got := deref(ac); got != wantAudio {
		t.Errorf("audioCodec: got %q, want %q", got, wantAudio)
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// A video-only upload whose thumbnail sprite track is listed before the source video reports the
// source video, not the sprite.
func TestTrackSummary_SkipsSpriteListedFirst(t *testing.T) {
	assertSummary(t, []*commodorepb.MediaTrack{
		{Type: "video", Codec: "JPEG", Width: i32p(1600), Height: i32p(900), Resolution: strp("1600x900")},
		{Type: "meta", Codec: "thumbvtt"},
		{Type: "video", Codec: "H264", Width: i32p(1920), Height: i32p(1080), Resolution: strp("1920x1080"), BitrateKbps: i32p(4000)},
	}, "1920x1080", "H264", "")
}

// A URL import whose 160x90 JPEG preview track is listed first reports the source video.
func TestTrackSummary_SkipsJPEGPreview(t *testing.T) {
	assertSummary(t, []*commodorepb.MediaTrack{
		{Type: "video", Codec: "JPEG", Width: i32p(160), Height: i32p(90), Resolution: strp("160x90")},
		{Type: "audio", Codec: "AAC", Channels: i32p(2), SampleRate: i32p(48000)},
		{Type: "video", Codec: "H264", Width: i32p(1280), Height: i32p(720), Resolution: strp("1280x720")},
	}, "1280x720", "H264", "AAC")
}

// A 1080p AAC upload with a 720p rendition and an Opus transcode listed before the originals
// reports the 1080p source video and the AAC source audio.
func TestTrackSummary_PrefersSourceOverRenditions(t *testing.T) {
	assertSummary(t, []*commodorepb.MediaTrack{
		{Type: "video", Codec: "H264", Width: i32p(1280), Height: i32p(720), Resolution: strp("1280x720"), SourceTrack: strp("1")},
		{Type: "audio", Codec: "opus", Channels: i32p(2), SampleRate: i32p(48000), SourceTrack: strp("2")},
		{Type: "video", Codec: "H264", Width: i32p(1920), Height: i32p(1080), Resolution: strp("1920x1080")},
		{Type: "audio", Codec: "AAC", Channels: i32p(2), SampleRate: i32p(44100)},
		{Type: "video", Codec: "JPEG", Width: i32p(1600), Height: i32p(900), Resolution: strp("1600x900")},
	}, "1920x1080", "H264", "AAC")
}

// Catalog rows written before source tracks were recorded still resolve the video by height.
func TestTrackSummary_LargestHeightWithoutSourceTracks(t *testing.T) {
	assertSummary(t, []*commodorepb.MediaTrack{
		{Type: "video", Codec: "H264", Resolution: strp("1280x720")},
		{Type: "video", Codec: "H264", Resolution: strp("1920x1080")},
	}, "1920x1080", "H264", "")
}

// Only image tracks means no video summary.
func TestTrackSummary_ImagesOnly(t *testing.T) {
	assertSummary(t, []*commodorepb.MediaTrack{
		{Type: "video", Codec: "JPEG", Resolution: strp("160x90")},
	}, "", "", "")
}
