package mist

import (
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

func TestExtractInternalName(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		expected string
	}{
		{name: "live prefix", input: "live+stream_id", expected: "stream_id"},
		{name: "pull prefix", input: "pull+stream_id", expected: "stream_id"},
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
		{name: "subtitle vtt request", connector: "HTTP", requestURL: "http://edge/view/live+stream/subtitles.vtt", expected: true},
		{name: "subtitle webvtt request", connector: "HTTP", requestURL: "http://edge/view/live+stream/captions.webvtt", expected: true},
		{name: "subtitle srt request", connector: "HTTP", requestURL: "http://edge/view/live+stream/captions.srt", expected: true},
	}

	for _, tc := range cases {
		if got := IsPlaybackViewerRequest(tc.connector, tc.requestURL); got != tc.expected {
			t.Fatalf("%s: expected %v, got %v", tc.name, tc.expected, got)
		}
	}
}

func TestParseTriggerToProtobufPushInputClose(t *testing.T) {
	logger := logging.NewLogger()
	// 7-field payload matching MistServer
	// src/controller/controller_capabilities.cpp:457 order.
	payload := []byte("live+abc123\n203.0.113.7\nMistInRTMP\n42\nEOF\nupstream end-of-file\n{\"video_h264\":{\"codec\":\"H264\",\"type\":\"video\"}}\n")

	trig, err := ParseTriggerToProtobuf(TriggerPushInputClose, payload, "node-1", logger)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	got := trig.GetPushInputClose()
	if got == nil {
		t.Fatalf("PushInputClose payload missing")
	}
	if got.GetStreamName() != "live+abc123" {
		t.Errorf("stream_name=%q", got.GetStreamName())
	}
	if got.GetRemoteHost() != "203.0.113.7" {
		t.Errorf("remote_host=%q", got.GetRemoteHost())
	}
	if got.GetBinaryName() != "MistInRTMP" {
		t.Errorf("binary_name=%q", got.GetBinaryName())
	}
	if got.GetPid() != 42 {
		t.Errorf("pid=%d", got.GetPid())
	}
	if got.GetMachineReason() != "EOF" {
		t.Errorf("machine_reason=%q", got.GetMachineReason())
	}
	if got.GetHumanReason() != "upstream end-of-file" {
		t.Errorf("human_reason=%q", got.GetHumanReason())
	}
	if got.GetTracksJson() == "" {
		t.Errorf("tracks_json should be preserved as raw JSON")
	}
	if trig.Blocking {
		t.Errorf("PUSH_INPUT_CLOSE must be non-blocking (async)")
	}

	// Short payload must fail loudly, not produce a partial trigger.
	short := []byte("live+abc\n203.0.113.7\nMistInRTMP\n42\n")
	if _, err := ParseTriggerToProtobuf(TriggerPushInputClose, short, "node-1", logger); err == nil {
		t.Errorf("expected error on truncated payload")
	}
}

func TestParseTriggerToProtobufRequestIdUnique(t *testing.T) {
	logger := logging.NewLogger()
	payload := []byte("rtmp://example/app/stream\nexample.com\nlive+stream_id\n")

	a, err := ParseTriggerToProtobuf(TriggerPushRewrite, payload, "node-1", logger)
	if err != nil {
		t.Fatalf("first parse failed: %v", err)
	}
	b, err := ParseTriggerToProtobuf(TriggerPushRewrite, payload, "node-1", logger)
	if err != nil {
		t.Fatalf("second parse failed: %v", err)
	}

	if a.RequestId == "" {
		t.Fatalf("RequestId must be non-empty")
	}
	if a.RequestId == b.RequestId {
		t.Fatalf("RequestId collision: both triggers got %q", a.RequestId)
	}
}
