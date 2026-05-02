package mist

import "testing"

func TestExtractInternalName(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "live prefix", input: "live+stream_id", expected: "stream_id"},
		{name: "vod prefix", input: "vod+asset_hash", expected: "asset_hash"},
		{name: "plain", input: "plain_stream", expected: "plain_stream"},
		{name: "plus in name", input: "stream+with+plus", expected: "stream+with+plus"},
	}

	for _, tc := range cases {
		if got := ExtractInternalName(tc.input); got != tc.expected {
			t.Fatalf("%s: expected %q, got %q", tc.name, tc.expected, got)
		}
	}
}

func TestIsPlaybackViewerConnector(t *testing.T) {
	cases := []struct {
		name      string
		connector string
		expected  bool
	}{
		{name: "hls viewer", connector: "HLS", expected: true},
		{name: "webrtc viewer", connector: "WebRTC", expected: true},
		{name: "raw websocket viewer", connector: "Raw/WS", expected: true},
		{name: "srt viewer", connector: "SRT", expected: true},
		{name: "mixed playback and metadata", connector: "WebRTC,JSON", expected: true},
		{name: "thumbnail vtt", connector: "ThumbVTT", expected: false},
		{name: "internal input", connector: "INPUT:RTMP", expected: false},
		{name: "internal output", connector: "OUTPUT:Thumbs", expected: false},
		{name: "plain http asset", connector: "HTTP", expected: false},
		{name: "info json websocket", connector: "Raw/WS,info_json", expected: false},
		{name: "image snapshot", connector: "JPG", expected: false},
		{name: "sprite sheet", connector: "spritesheet", expected: false},
		{name: "empty connector", connector: "", expected: false},
	}

	for _, tc := range cases {
		if got := IsPlaybackViewerConnector(tc.connector); got != tc.expected {
			t.Fatalf("%s: expected %v, got %v", tc.name, tc.expected, got)
		}
	}
}

func TestIsPlaybackViewerRequest(t *testing.T) {
	cases := []struct {
		name       string
		connector  string
		requestURL string
		expected   bool
	}{
		{name: "hls manifest", connector: "HLS", requestURL: "http://edge/view/hls/live+stream/index.m3u8", expected: true},
		{name: "http hls manifest", connector: "HTTP", requestURL: "http://edge/view/hls/live+stream/index.m3u8", expected: true},
		{name: "mime hls manifest", connector: "html5/application/vnd.apple.mpegurl", requestURL: "http://edge/view/hls/live+stream/index.m3u8", expected: true},
		{name: "http mp4 playback", connector: "HTTP", requestURL: "http://edge/view/live+stream.mp4", expected: true},
		{name: "webrtc session", connector: "WebRTC", requestURL: "http://edge/view/webrtc/live+stream", expected: true},
		{name: "json websocket", connector: "Raw/WS", requestURL: "ws://edge/json_live+stream.js?metaeverywhere=1", expected: false},
		{name: "poster request", connector: "HTTP", requestURL: "http://edge/assets/stream/poster.jpg", expected: false},
		{name: "sprite request", connector: "HTTP", requestURL: "http://edge/assets/stream/sprite.jpg", expected: false},
		{name: "thumb vtt request", connector: "ThumbVTT", requestURL: "http://edge/view/live+stream.thumbvtt", expected: false},
	}

	for _, tc := range cases {
		if got := IsPlaybackViewerRequest(tc.connector, tc.requestURL); got != tc.expected {
			t.Fatalf("%s: expected %v, got %v", tc.name, tc.expected, got)
		}
	}
}
