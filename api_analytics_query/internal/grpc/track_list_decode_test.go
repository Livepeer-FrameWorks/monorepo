package grpc

import "testing"

func TestDecodeStoredStreamTracksTypedJSON(t *testing.T) {
	tracks := decodeStoredStreamTracks(`[
		{
			"track_name": "video0",
			"track_type": "video",
			"codec": "H264",
			"bitrate_kbps": 4200,
			"width": 1920,
			"height": 1080,
			"fps": 24
		},
		{
			"trackName": "audio0",
			"trackType": "audio",
			"codec": "AAC",
			"sampleRate": 48000,
			"channels": 2
		}
	]`)

	if len(tracks) != 2 {
		t.Fatalf("expected 2 tracks, got %d", len(tracks))
	}
	video := tracks[0]
	if video.GetTrackName() != "video0" || video.GetTrackType() != "video" || video.GetCodec() != "H264" {
		t.Fatalf("video track = name %q type %q codec %q", video.GetTrackName(), video.GetTrackType(), video.GetCodec())
	}
	if video.GetWidth() != 1920 || video.GetHeight() != 1080 || video.GetFps() != 24 || video.GetBitrateKbps() != 4200 {
		t.Fatalf("video metrics = %dx%d %.1ffps %dkbps", video.GetWidth(), video.GetHeight(), video.GetFps(), video.GetBitrateKbps())
	}
	audio := tracks[1]
	if audio.GetTrackName() != "audio0" || audio.GetTrackType() != "audio" || audio.GetCodec() != "AAC" {
		t.Fatalf("audio track = name %q type %q codec %q", audio.GetTrackName(), audio.GetTrackType(), audio.GetCodec())
	}
	if audio.GetSampleRate() != 48000 || audio.GetChannels() != 2 {
		t.Fatalf("audio metrics = %dHz %dch", audio.GetSampleRate(), audio.GetChannels())
	}
}

func TestDecodeStoredStreamTracksMistObjectJSON(t *testing.T) {
	tracks := decodeStoredStreamTracks(`{
		"video_1": {
			"codec": "H264",
			"kbits": 6000,
			"width": 1280,
			"height": 720,
			"fpks": 30000
		},
		"audio_1": {
			"codec": "opus",
			"rate": 48000,
			"channels": 2
		}
	}`)

	if len(tracks) != 2 {
		t.Fatalf("expected 2 tracks, got %d", len(tracks))
	}
	byName := map[string]struct {
		typ   string
		codec string
		fps   float64
	}{}
	for _, track := range tracks {
		byName[track.GetTrackName()] = struct {
			typ   string
			codec string
			fps   float64
		}{typ: track.GetTrackType(), codec: track.GetCodec(), fps: track.GetFps()}
	}
	if byName["video_1"].typ != "video" || byName["video_1"].codec != "H264" || byName["video_1"].fps != 30 {
		t.Fatalf("video_1 decoded as %+v", byName["video_1"])
	}
	if byName["audio_1"].typ != "audio" || byName["audio_1"].codec != "opus" {
		t.Fatalf("audio_1 decoded as %+v", byName["audio_1"])
	}
}

func TestDecodeStoredStreamTracksGeneratedOutputsStayUnknownType(t *testing.T) {
	tracks := decodeStoredStreamTracks(`[
		{"track_name": "poster", "codec": "JPEG", "width": 1600, "height": 900},
		{"track_name": "thumbvtt", "codec": "thumbvtt"}
	]`)

	if len(tracks) != 2 {
		t.Fatalf("expected 2 tracks, got %d", len(tracks))
	}
	for _, track := range tracks {
		if track.GetTrackType() != "unknown" {
			t.Fatalf("generated output %q type = %q, want unknown", track.GetTrackName(), track.GetTrackType())
		}
	}
}
