package mist

import (
	"strings"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func joinPayload(lines ...string) []byte {
	return []byte(strings.Join(lines, "\n"))
}

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

func TestIsDurableTriggerType(t *testing.T) {
	durable := []TriggerType{
		TriggerUserEnd,
		TriggerStreamEnd,
		TriggerPushEnd,
		TriggerPushInputClose,
		TriggerRecordingEnd,
		TriggerRecordingSegment,
		TriggerLivepeerSegmentComplete,
		TriggerProcessAVSegmentComplete,
	}
	for _, triggerType := range durable {
		if !IsDurableTriggerType(string(triggerType)) {
			t.Fatalf("%s should be registered durable", triggerType)
		}
	}

	nonDurable := []TriggerType{
		TriggerPushRewrite,
		TriggerPlayRewrite,
		TriggerStreamSource,
		TriggerPushOutStart,
		TriggerStreamBuffer,
		TriggerUserNew,
		TriggerLiveTrackList,
		TriggerStreamProcess,
		TriggerThumbnailUpdated,
		TriggerStreamLifecycle,
		TriggerClientLifecycle,
		TriggerNodeLifecycle,
	}
	for _, triggerType := range nonDurable {
		if IsDurableTriggerType(string(triggerType)) {
			t.Fatalf("%s should not be registered durable", triggerType)
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

func TestParseTriggerToProtobufRecordingEnd(t *testing.T) {
	logger := logging.NewLogger()
	// Full RECORDING_END payload per Output::getExitTriggerPayload
	// (mistserver src/output/output.cpp): stream, target, output, bytes,
	// secondsWriting, timeStarted, timeEnded, mediaDurationMs, firstPacketTime,
	// lastPacketTime, machine exit reason, human exit reason, final JSON track
	// summary.
	payload := []byte("processing+job1\n/tmp/out.mkv\nMistOutMKV\n4096\n12\n1700000000\n1700000012\n12000\n0\n12000\nCLEAN_EOF\nclean end-of-file\n{\"tracks\":[{\"idx\":0,\"id\":7,\"selected\":true,\"type\":\"video\",\"codec\":\"H264\",\"width\":1280,\"height\":720,\"firstms\":0,\"lastms\":12000,\"bps\":800000,\"rate\":30}]}\n")

	trig, err := ParseTriggerToProtobuf(TriggerRecordingEnd, payload, "node-1", logger)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	got := trig.GetRecordingComplete()
	if got == nil {
		t.Fatalf("RecordingComplete payload missing")
	}
	if got.GetBytesWritten() != 4096 {
		t.Errorf("bytes_written=%d", got.GetBytesWritten())
	}
	if got.GetMediaDurationMs() != 12000 {
		t.Errorf("media_duration_ms=%d", got.GetMediaDurationMs())
	}
	if got.GetExitReason() != "CLEAN_EOF" {
		t.Errorf("exit_reason=%q", got.GetExitReason())
	}
	if got.GetHumanExitReason() != "clean end-of-file" {
		t.Errorf("human_exit_reason=%q", got.GetHumanExitReason())
	}
	if len(got.GetTracks()) != 1 {
		t.Fatalf("tracks=%d, want 1", len(got.GetTracks()))
	}
	track := got.GetTracks()[0]
	if track.GetTrackIndex() != 0 || track.GetTrackId() != 7 || track.GetTrackType() != "video" || track.GetCodec() != "H264" {
		t.Fatalf("track identity mismatch: %+v", track)
	}
	if track.GetWidth() != 1280 || track.GetHeight() != 720 || track.GetFirstMs() != 0 || track.GetLastMs() != 12000 {
		t.Fatalf("track media summary mismatch: %+v", track)
	}
	if !track.GetSelected() {
		t.Fatal("track selected=false, want true")
	}
	if !IsCleanExitReason(got.GetExitReason()) {
		t.Errorf("CLEAN_EOF should be a clean exit reason")
	}
	if IsCleanExitReason("WRITE_FAILURE") || IsCleanExitReason("") {
		t.Errorf("non-CLEAN_* reasons must not be clean")
	}

	oldPayload := []byte("processing+job1\n/tmp/out.mkv\nMistOutMKV\n4096\n12\n1700000000\n1700000012\n12000\n0\n12000\nCLEAN_EOF\nclean end-of-file\n")
	if _, err := ParseTriggerToProtobuf(TriggerRecordingEnd, oldPayload, "node-1", logger); err == nil {
		t.Fatal("expected RECORDING_END without track summary to fail")
	}

	// No "speed" object in the summary → no ProcessingSpeed on the trigger.
	if got.GetProcessingSpeed() != nil {
		t.Fatal("ProcessingSpeed should be nil without a speed object")
	}
}

func TestParseTriggerToProtobufRecordingEndSpeedStats(t *testing.T) {
	logger := logging.NewLogger()
	// Track summary enriched with the rate-controller speed object + drain_ms,
	// as emitted for process-controlled recordings.
	payload := []byte("processing+job1\n/tmp/out.mkv\nMistOutMKV\n4096\n12\n1700000000\n1700000012\n12000\n0\n12000\nCLEAN_EOF\nclean end-of-file\n" +
		`{"tracks":[{"idx":0,"id":7,"selected":true,"type":"video","codec":"H264","width":1280,"height":720,"firstms":0,"lastms":12000,"bps":800000,"rate":30}],` +
		`"speed":{"ticks":40,"min":1,"avg":6.5,"max":24,"hard_slow_ticks":3,"regular_slow_ticks":2,"ramp_ups":8,"lockout_ticks":10,"stale_hold_ticks":12},"drain_ms":30000}` + "\n")

	trig, err := ParseTriggerToProtobuf(TriggerRecordingEnd, payload, "node-1", logger)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	sp := trig.GetRecordingComplete().GetProcessingSpeed()
	if sp == nil {
		t.Fatal("ProcessingSpeed missing")
	}
	if sp.GetTicks() != 40 || sp.GetSpeedMin() != 1 || sp.GetSpeedAvg() != 6.5 || sp.GetSpeedMax() != 24 {
		t.Fatalf("speed stats mismatch: %+v", sp)
	}
	if sp.GetHardSlowTicks() != 3 || sp.GetRegularSlowTicks() != 2 || sp.GetRampUps() != 8 ||
		sp.GetLockoutTicks() != 10 || sp.GetStaleHoldTicks() != 12 {
		t.Fatalf("verdict counters mismatch: %+v", sp)
	}
	if sp.DrainMs == nil || sp.GetDrainMs() != 30000 {
		t.Fatalf("drain_ms mismatch: %+v", sp)
	}
}

// TestParseTriggerToProtobufParamMappings covers the positional-payload
// trigger types whose only logic is extracting fields from newline-separated
// params. Each case asserts the fields downstream consumers actually read and
// the blocking classification, so an off-by-one in the param indexing is
// caught rather than silently producing a misfielded trigger.
func TestParseTriggerToProtobufParamMappings(t *testing.T) {
	logger := logging.NewLogger()

	cases := []struct {
		name        string
		triggerType TriggerType
		payload     []byte
		wantBlock   bool
		validate    func(t *testing.T, trig *ipcpb.MistTrigger)
	}{
		{
			name:        "push rewrite",
			triggerType: TriggerPushRewrite,
			payload:     joinPayload("rtmp://in/app/key", "ingest.example.com", "live+stream_id"),
			wantBlock:   true,
			validate: func(t *testing.T, trig *ipcpb.MistTrigger) {
				got := trig.GetPushRewrite()
				if got == nil {
					t.Fatal("PushRewrite payload missing")
				}
				if got.GetPushUrl() != "rtmp://in/app/key" || got.GetHostname() != "ingest.example.com" || got.GetStreamName() != "live+stream_id" {
					t.Fatalf("push rewrite mismatch: %+v", got)
				}
			},
		},
		{
			name:        "play rewrite",
			triggerType: TriggerPlayRewrite,
			payload:     joinPayload("live+stream_id", "203.0.113.9", "HLS", "http://edge/view/hls/live+stream_id/index.m3u8"),
			wantBlock:   true,
			validate: func(t *testing.T, trig *ipcpb.MistTrigger) {
				got := trig.GetPlayRewrite()
				if got == nil {
					t.Fatal("PlayRewrite payload missing")
				}
				if got.GetRequestedStream() != "live+stream_id" || got.GetViewerHost() != "203.0.113.9" || got.GetOutputType() != "HLS" {
					t.Fatalf("play rewrite mismatch: %+v", got)
				}
				if got.GetRequestUrl() != "http://edge/view/hls/live+stream_id/index.m3u8" {
					t.Fatalf("play rewrite request_url=%q", got.GetRequestUrl())
				}
			},
		},
		{
			name:        "stream source",
			triggerType: TriggerStreamSource,
			payload:     joinPayload("live+stream_id"),
			wantBlock:   true,
			validate: func(t *testing.T, trig *ipcpb.MistTrigger) {
				if got := trig.GetStreamSource(); got == nil || got.GetStreamName() != "live+stream_id" {
					t.Fatalf("stream source mismatch: %+v", got)
				}
			},
		},
		{
			name:        "stream process",
			triggerType: TriggerStreamProcess,
			payload:     joinPayload("processing+job1"),
			wantBlock:   true,
			validate: func(t *testing.T, trig *ipcpb.MistTrigger) {
				if got := trig.GetStreamProcess(); got == nil || got.GetStreamName() != "processing+job1" {
					t.Fatalf("stream process mismatch: %+v", got)
				}
			},
		},
		{
			name:        "push out start",
			triggerType: TriggerPushOutStart,
			payload:     joinPayload("live+stream_id", "rtmp://youtube/live2/abc"),
			wantBlock:   true,
			validate: func(t *testing.T, trig *ipcpb.MistTrigger) {
				got := trig.GetPushOutStart()
				if got == nil || got.GetStreamName() != "live+stream_id" || got.GetPushTarget() != "rtmp://youtube/live2/abc" {
					t.Fatalf("push out start mismatch: %+v", got)
				}
			},
		},
		{
			name:        "push end",
			triggerType: TriggerPushEnd,
			payload:     joinPayload("9", "live+stream_id", "rtmp://t/before", "rtmp://t/after", "log lines", "DONE"),
			wantBlock:   false,
			validate: func(t *testing.T, trig *ipcpb.MistTrigger) {
				got := trig.GetPushEnd()
				if got == nil {
					t.Fatal("PushEnd payload missing")
				}
				if got.GetPushId() != 9 || got.GetStreamName() != "live+stream_id" {
					t.Fatalf("push end identity mismatch: %+v", got)
				}
				if got.GetTargetUriBefore() != "rtmp://t/before" || got.GetTargetUriAfter() != "rtmp://t/after" || got.GetLogMessages() != "log lines" || got.GetPushStatus() != "DONE" {
					t.Fatalf("push end body mismatch: %+v", got)
				}
			},
		},
		{
			name:        "user new",
			triggerType: TriggerUserNew,
			payload:     joinPayload("live+stream_id", "203.0.113.9", "tok123", "HLS", "http://edge/view/hls/live+stream_id/index.m3u8", "sess-1"),
			wantBlock:   true,
			validate: func(t *testing.T, trig *ipcpb.MistTrigger) {
				got := trig.GetViewerConnect()
				if got == nil {
					t.Fatal("ViewerConnect payload missing")
				}
				if got.GetStreamName() != "live+stream_id" || got.GetHost() != "203.0.113.9" || got.GetViewerToken() != "tok123" {
					t.Fatalf("user new identity mismatch: %+v", got)
				}
				if got.GetConnector() != "HLS" || got.GetSessionId() != "sess-1" {
					t.Fatalf("user new connector/session mismatch: %+v", got)
				}
			},
		},
		{
			name:        "stream end full",
			triggerType: TriggerStreamEnd,
			payload:     joinPayload("live+stream_id", "100", "200", "3", "1", "4", "3600"),
			wantBlock:   false,
			validate: func(t *testing.T, trig *ipcpb.MistTrigger) {
				got := trig.GetStreamEnd()
				if got == nil || got.GetStreamName() != "live+stream_id" {
					t.Fatalf("stream end name mismatch: %+v", got)
				}
				if got.GetDownloadedBytes() != 100 || got.GetUploadedBytes() != 200 {
					t.Fatalf("stream end bytes mismatch: %+v", got)
				}
				if got.GetTotalViewers() != 3 || got.GetTotalInputs() != 1 || got.GetTotalOutputs() != 4 || got.GetViewerSeconds() != 3600 {
					t.Fatalf("stream end counters mismatch: %+v", got)
				}
			},
		},
		{
			name:        "stream end name only",
			triggerType: TriggerStreamEnd,
			payload:     joinPayload("live+stream_id"),
			wantBlock:   false,
			validate: func(t *testing.T, trig *ipcpb.MistTrigger) {
				got := trig.GetStreamEnd()
				if got == nil || got.GetStreamName() != "live+stream_id" {
					t.Fatalf("stream end name mismatch: %+v", got)
				}
				// Below the 7-param threshold the numeric counters stay unset.
				if got.DownloadedBytes != nil || got.ViewerSeconds != nil {
					t.Fatalf("name-only STREAM_END must not populate counters: %+v", got)
				}
			},
		},
		{
			name:        "recording segment",
			triggerType: TriggerRecordingSegment,
			payload:     joinPayload("processing+job1", "/rec/seg-0.ts", "6000", "1700000000", "1700000006"),
			wantBlock:   false,
			validate: func(t *testing.T, trig *ipcpb.MistTrigger) {
				got := trig.GetRecordingSegment()
				if got == nil || got.GetStreamName() != "processing+job1" || got.GetFilePath() != "/rec/seg-0.ts" {
					t.Fatalf("recording segment identity mismatch: %+v", got)
				}
				if got.GetDurationMs() != 6000 || got.GetTimeStarted() != 1700000000 || got.GetTimeEnded() != 1700000006 {
					t.Fatalf("recording segment timing mismatch: %+v", got)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			trig, err := ParseTriggerToProtobuf(tc.triggerType, tc.payload, "node-1", logger)
			if err != nil {
				t.Fatalf("parse failed: %v", err)
			}
			if trig.GetTriggerType() != string(tc.triggerType) {
				t.Fatalf("trigger_type=%q, want %q", trig.GetTriggerType(), tc.triggerType)
			}
			if trig.Blocking != tc.wantBlock {
				t.Fatalf("blocking=%v, want %v", trig.Blocking, tc.wantBlock)
			}
			tc.validate(t, trig)
		})
	}
}

// TestParseTriggerToProtobufRejectsTruncatedPayloads pins the fail-loud
// contract: every positional parser rejects a payload shorter than its minimum
// rather than emitting a partial trigger.
func TestParseTriggerToProtobufRejectsTruncatedPayloads(t *testing.T) {
	logger := logging.NewLogger()
	cases := []struct {
		name        string
		triggerType TriggerType
		payload     []byte
	}{
		{"push rewrite", TriggerPushRewrite, joinPayload("url", "host")},
		{"play rewrite", TriggerPlayRewrite, joinPayload("stream", "ip", "connector")},
		{"stream source empty", TriggerStreamSource, []byte("")},
		{"stream process empty", TriggerStreamProcess, []byte("")},
		{"push out start", TriggerPushOutStart, joinPayload("stream")},
		{"push end", TriggerPushEnd, joinPayload("1", "stream", "before", "after", "logs")},
		{"user new", TriggerUserNew, joinPayload("stream", "host", "tok", "HLS", "url")},
		{"user end", TriggerUserEnd, joinPayload("sess", "stream", "HLS", "host", "1", "2", "3")},
		{"recording segment", TriggerRecordingSegment, joinPayload("stream", "/p", "1000", "5")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseTriggerToProtobuf(tc.triggerType, tc.payload, "node-1", logger); err == nil {
				t.Fatalf("expected error on truncated %s payload", tc.triggerType)
			}
		})
	}
}

// TestParseTriggerToProtobufUserEnd covers the most parameter-heavy parser:
// the trailing session_id override (params[11]) and the parallel
// comma-separated per-element time-share arrays MistServer appends for
// sessions that touched multiple streams/connectors/hosts.
func TestParseTriggerToProtobufUserEnd(t *testing.T) {
	logger := logging.NewLogger()

	t.Run("base eight params", func(t *testing.T) {
		payload := joinPayload("sess-orig", "live+a", "HLS", "h1", "120", "1000", "2000", "tagdata")
		trig, err := ParseTriggerToProtobuf(TriggerUserEnd, payload, "node-1", logger)
		if err != nil {
			t.Fatalf("parse failed: %v", err)
		}
		got := trig.GetViewerDisconnect()
		if got == nil {
			t.Fatal("ViewerDisconnect payload missing")
		}
		if got.GetSessionId() != "sess-orig" || got.GetStreamName() != "live+a" || got.GetConnector() != "HLS" || got.GetHost() != "h1" {
			t.Fatalf("user end identity mismatch: %+v", got)
		}
		if got.GetDuration() != 120 || got.GetUpBytes() != 1000 || got.GetDownBytes() != 2000 || got.GetTags() != "tagdata" {
			t.Fatalf("user end metrics mismatch: %+v", got)
		}
		// No override supplied: SessionIdentifier stays nil.
		if got.SessionIdentifier != nil {
			t.Fatalf("session_identifier should be nil without override, got %q", got.GetSessionIdentifier())
		}
		if got.GetStreamTimes() != nil {
			t.Fatalf("no time-share arrays supplied, got %+v", got.GetStreamTimes())
		}
	})

	t.Run("session override and time shares", func(t *testing.T) {
		// params[1..3] carry joined element lists; params[8..10] carry the
		// matching seconds; params[11] overrides the canonical session id.
		payload := joinPayload(
			"sess-orig", "live+a,live+b", "HLS,WebRTC", "h1,h2",
			"120", "1000", "2000", "tagdata",
			"70,50", "65,55", "80,40", "sess-canonical",
		)
		trig, err := ParseTriggerToProtobuf(TriggerUserEnd, payload, "node-1", logger)
		if err != nil {
			t.Fatalf("parse failed: %v", err)
		}
		got := trig.GetViewerDisconnect()
		if got.GetSessionId() != "sess-canonical" {
			t.Fatalf("session_id should use the override, got %q", got.GetSessionId())
		}
		if got.GetSessionIdentifier() != "sess-orig" {
			t.Fatalf("session_identifier should retain the original, got %q", got.GetSessionIdentifier())
		}
		// HostTimes pairs params[3] names with params[8] seconds.
		if shares := got.GetHostTimes(); len(shares) != 2 || shares[0].GetName() != "h1" || shares[0].GetSeconds() != 70 || shares[1].GetName() != "h2" || shares[1].GetSeconds() != 50 {
			t.Fatalf("host_times mismatch: %+v", shares)
		}
		// ConnectorTimes pairs params[2] names with params[9] seconds.
		if shares := got.GetConnectorTimes(); len(shares) != 2 || shares[0].GetName() != "HLS" || shares[1].GetName() != "WebRTC" || shares[0].GetSeconds() != 65 {
			t.Fatalf("connector_times mismatch: %+v", shares)
		}
		// StreamTimes pairs params[1] names with params[10] seconds.
		if shares := got.GetStreamTimes(); len(shares) != 2 || shares[0].GetName() != "live+a" || shares[0].GetSeconds() != 80 || shares[1].GetName() != "live+b" || shares[1].GetSeconds() != 40 {
			t.Fatalf("stream_times mismatch: %+v", shares)
		}
	})
}

// TestParseTriggerToProtobufStreamBuffer covers the health-JSON branch: the
// nested "health" wrapper is unwrapped, summary fields are extracted, and
// per-track entries become StreamTrack messages. A malformed health blob must
// degrade to a track-less trigger rather than fail the whole parse.
func TestParseTriggerToProtobufStreamBuffer(t *testing.T) {
	logger := logging.NewLogger()

	t.Run("with health json", func(t *testing.T) {
		health := `{"health":{"buffer":5000,"jitter":30,"issues":"keyframe gap","maxkeepaway":8000,"track_video":{"codec":"H264","type":"video","width":1920,"height":1080}}}`
		trig, err := ParseTriggerToProtobuf(TriggerStreamBuffer, joinPayload("live+x", "FULL", health), "node-1", logger)
		if err != nil {
			t.Fatalf("parse failed: %v", err)
		}
		got := trig.GetStreamBuffer()
		if got == nil || got.GetStreamName() != "live+x" || got.GetBufferState() != "FULL" {
			t.Fatalf("stream buffer identity mismatch: %+v", got)
		}
		if got.GetStreamBufferMs() != 5000 || got.GetStreamJitterMs() != 30 || got.GetMaxKeepawayMs() != 8000 {
			t.Fatalf("stream buffer health summary mismatch: %+v", got)
		}
		if got.GetMistIssues() != "keyframe gap" {
			t.Fatalf("mist_issues=%q", got.GetMistIssues())
		}
		if len(got.GetTracks()) != 1 || got.GetTracks()[0].GetCodec() != "H264" {
			t.Fatalf("stream buffer tracks mismatch: %+v", got.GetTracks())
		}
	})

	t.Run("malformed health json degrades", func(t *testing.T) {
		trig, err := ParseTriggerToProtobuf(TriggerStreamBuffer, joinPayload("live+x", "DRY", "{not json"), "node-1", logger)
		if err != nil {
			t.Fatalf("malformed health JSON must not fail the parse: %v", err)
		}
		got := trig.GetStreamBuffer()
		if got == nil || got.GetBufferState() != "DRY" {
			t.Fatalf("stream buffer identity mismatch: %+v", got)
		}
		if got.StreamBufferMs != nil || len(got.GetTracks()) != 0 {
			t.Fatalf("malformed health must leave summary/tracks empty: %+v", got)
		}
	})
}

// TestParseTriggerToProtobufLiveTrackList covers the track-list JSON branch.
func TestParseTriggerToProtobufLiveTrackList(t *testing.T) {
	logger := logging.NewLogger()
	tracks := `{"video_0":{"codec":"H264","type":"video","width":1280,"height":720},"audio_0":{"codec":"AAC","type":"audio","channels":2,"rate":48000}}`
	trig, err := ParseTriggerToProtobuf(TriggerLiveTrackList, joinPayload("live+x", tracks), "node-1", logger)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	got := trig.GetTrackList()
	if got == nil || got.GetStreamName() != "live+x" {
		t.Fatalf("track list identity mismatch: %+v", got)
	}
	if len(got.GetTracks()) != 2 {
		t.Fatalf("track list tracks=%d, want 2", len(got.GetTracks()))
	}
}

// TestParseTriggerToProtobufLifecycleJSON covers the three analytics triggers
// that unmarshal their whole raw payload as JSON straight into protobuf, plus
// the malformed-JSON failure each must surface.
func TestParseTriggerToProtobufLifecycleJSON(t *testing.T) {
	logger := logging.NewLogger()

	t.Run("stream lifecycle", func(t *testing.T) {
		payload := []byte(`{"node_id":"n1","internal_name":"abc","status":"live","total_viewers":5}`)
		trig, err := ParseTriggerToProtobuf(TriggerStreamLifecycle, payload, "node-1", logger)
		if err != nil {
			t.Fatalf("parse failed: %v", err)
		}
		got := trig.GetStreamLifecycleUpdate()
		if got == nil || got.GetNodeId() != "n1" || got.GetInternalName() != "abc" || got.GetStatus() != "live" || got.GetTotalViewers() != 5 {
			t.Fatalf("stream lifecycle mismatch: %+v", got)
		}
	})

	t.Run("client lifecycle", func(t *testing.T) {
		payload := []byte(`{"node_id":"n1","internal_name":"abc","action":"connect","protocol":"HLS"}`)
		trig, err := ParseTriggerToProtobuf(TriggerClientLifecycle, payload, "node-1", logger)
		if err != nil {
			t.Fatalf("parse failed: %v", err)
		}
		got := trig.GetClientLifecycleUpdate()
		if got == nil || got.GetAction() != "connect" || got.GetProtocol() != "HLS" {
			t.Fatalf("client lifecycle mismatch: %+v", got)
		}
	})

	t.Run("node lifecycle", func(t *testing.T) {
		payload := []byte(`{"node_id":"n1","cpu_tenths":500,"is_healthy":true}`)
		trig, err := ParseTriggerToProtobuf(TriggerNodeLifecycle, payload, "node-1", logger)
		if err != nil {
			t.Fatalf("parse failed: %v", err)
		}
		got := trig.GetNodeLifecycleUpdate()
		if got == nil || got.GetNodeId() != "n1" || got.GetCpuTenths() != 500 || !got.GetIsHealthy() {
			t.Fatalf("node lifecycle mismatch: %+v", got)
		}
	})

	t.Run("malformed json fails", func(t *testing.T) {
		for _, tt := range []TriggerType{TriggerStreamLifecycle, TriggerClientLifecycle, TriggerNodeLifecycle} {
			if _, err := ParseTriggerToProtobuf(tt, []byte("{not json"), "node-1", logger); err == nil {
				t.Fatalf("%s must fail on malformed JSON", tt)
			}
		}
	})
}

// TestParseTriggerToProtobufUnknownType pins the default branch.
func TestParseTriggerToProtobufUnknownType(t *testing.T) {
	logger := logging.NewLogger()
	if _, err := ParseTriggerToProtobuf(TriggerType("NOT_A_REAL_TRIGGER"), joinPayload("x"), "node-1", logger); err == nil {
		t.Fatal("expected error for unknown trigger type")
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

// TestParseTracksFromJSON exercises the codec→type classifier and the
// type-specific field extraction. This is the metadata that feeds viewer
// analytics, so each branch (video/audio/meta/unknown), the two bitrate
// encodings (kbits vs bps), the fps-from-fpks conversion, the derived
// resolution string, and the nested "keys" frame-timing object are all
// asserted explicitly.
func TestParseTracksFromJSON(t *testing.T) {
	tracksData := map[string]any{
		// Video classified by explicit type; carries kbits, fpks, keys.
		"video_0": map[string]any{
			"codec": "H264", "type": "video",
			"width": float64(1920), "height": float64(1080),
			"fpks": float64(30000), "kbits": float64(800), "bframes": true,
			"keys": map[string]any{"frames_max": float64(120), "frames_min": float64(60)},
		},
		// Audio classified by codec alone (no explicit type); carries bps.
		"audio_0": map[string]any{
			"codec":    "AAC",
			"channels": float64(2), "rate": float64(48000), "bps": float64(128000),
		},
		// Meta classified by codec JSON.
		"meta_0": map[string]any{"codec": "JSON"},
		// Unknown codec with no type/name hint falls through to "unknown".
		"weird": map[string]any{"codec": "FLACX"},
		// Not a track (no codec) — must be skipped, not turned into a track.
		"summary": map[string]any{"buffer": float64(5000)},
		// Not even a map — must be skipped.
		"scalar": float64(1),
	}

	tracks := parseTracksFromJSON(tracksData)
	byName := make(map[string]*ipcpb.StreamTrack, len(tracks))
	for _, tr := range tracks {
		byName[tr.GetTrackName()] = tr
	}
	if len(tracks) != 4 {
		t.Fatalf("expected 4 tracks (codec-bearing only), got %d: %v", len(tracks), byName)
	}

	video := byName["video_0"]
	if video == nil || video.GetTrackType() != "video" || video.GetCodec() != "H264" {
		t.Fatalf("video classification mismatch: %+v", video)
	}
	if video.GetWidth() != 1920 || video.GetHeight() != 1080 || video.GetResolution() != "1920x1080" {
		t.Fatalf("video dimensions mismatch: %+v", video)
	}
	if video.GetFps() != 30 {
		t.Fatalf("fps should be fpks/1000=30, got %v", video.GetFps())
	}
	if video.GetBitrateKbps() != 800 || video.GetBitrateBps() != 800000 {
		t.Fatalf("kbits bitrate should populate both kbps and bps: %+v", video)
	}
	if !video.GetHasBframes() {
		t.Fatalf("has_bframes should be true")
	}
	if video.GetFramesMax() != 120 || video.GetFramesMin() != 60 {
		t.Fatalf("keys frame-timing mismatch: %+v", video)
	}

	audio := byName["audio_0"]
	if audio == nil || audio.GetTrackType() != "audio" {
		t.Fatalf("audio classification mismatch: %+v", audio)
	}
	if audio.GetChannels() != 2 || audio.GetSampleRate() != 48000 {
		t.Fatalf("audio fields mismatch: %+v", audio)
	}
	// bps encoding populates bps directly and derives kbps via integer division.
	if audio.GetBitrateBps() != 128000 || audio.GetBitrateKbps() != 128 {
		t.Fatalf("bps bitrate should derive kbps=128: %+v", audio)
	}

	if meta := byName["meta_0"]; meta == nil || meta.GetTrackType() != "meta" {
		t.Fatalf("meta classification mismatch: %+v", meta)
	}
	if unknown := byName["weird"]; unknown == nil || unknown.GetTrackType() != "unknown" {
		t.Fatalf("unknown classification mismatch: %+v", unknown)
	}
}

// NOTE: pairSessionShares and int64FromAny already have dedicated coverage in
// purelogic_test.go and coercion_test.go respectively.

// TestParseTracksFromJSONTypeFromName covers classification by track-name
// prefix when the JSON omits an explicit type and the codec is ambiguous.
func TestParseTracksFromJSONTypeFromName(t *testing.T) {
	tracks := parseTracksFromJSON(map[string]any{
		"video_x": map[string]any{"codec": "VP9"},
		"audio_x": map[string]any{"codec": "opus"},
		"meta_x":  map[string]any{"codec": "subtitle"},
	})
	got := make(map[string]string, len(tracks))
	for _, tr := range tracks {
		got[tr.GetTrackName()] = tr.GetTrackType()
	}
	// VP9 is not in the explicit codec list; name prefix decides.
	if got["video_x"] != "video" {
		t.Fatalf("video_x type=%q, want video", got["video_x"])
	}
	if got["audio_x"] != "audio" {
		t.Fatalf("audio_x type=%q, want audio", got["audio_x"])
	}
	if got["meta_x"] != "meta" {
		t.Fatalf("meta_x type=%q, want meta", got["meta_x"])
	}
}
