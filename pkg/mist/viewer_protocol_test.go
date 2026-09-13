package mist

import "testing"

func TestViewerProtocolUsesConnectorEvidence(t *testing.T) {
	for input, want := range map[string]string{
		"HLS": "hls", "HTTP, HLS": "hls", "WebRTC,JSON": "webrtc", "WHEP": "whep", "CMAF": "cmaf", "DASH": "dash", "HSS": "smoothstreaming",
		"RTMP": "rtmp", "RTSP": "rtsp", "SRT": "srt", "DTSC": "dtsc", "DTSCQUIC": "dtscquic", "MP4": "mp4", "WEBM": "webm", "EBML": "mkv",
		"Raw/WS": "raw_ws", "WSRaw": "raw_ws", "HTTPTS,TS": "ts", "AAC": "aac", "H264": "h264", "FLV": "flv", "MP3": "mp3", "OPUS": "opus",
		"WebRTC/WS": "webrtc", "WebRTC/WS,JSON": "webrtc", "MP4/WS": "wsmp4", "EBML/WS": "mews_webm", "H264/WS": "h264_ws", "JSON/WS": "json_ws",
	} {
		t.Run(input, func(t *testing.T) {
			got, err := ViewerProtocol(input)
			if err != nil || got != want {
				t.Fatalf("protocol=%q want=%q err=%v", got, want, err)
			}
		})
	}
	for _, input := range []string{"", "HTTP", "HTTPS,JSON", "Raw/WS,info_json", "INPUT:RTMP", "OUTPUT:HLS", "HLS,WebRTC", "HLS,unknown", "ThumbVTT", "HLS,", "HLS\n", "https://edge/hls/stream"} {
		if got, err := ViewerProtocol(input); got != "" || err == nil {
			t.Fatalf("invalid connector %q produced %q: %v", input, got, err)
		}
	}
}

func TestPlayRewriteProtocolSeparatesMetadataFromMedia(t *testing.T) {
	for connector, want := range map[string]string{"HTTP": "mist_html", "HTTPS": "mist_html", "ThumbVTT": "mist_html", "JPG": "mist_html"} {
		protocol, err := PlayRewriteProtocol(connector)
		if err != nil || protocol != want {
			t.Fatalf("metadata connector %q: %q %v", connector, protocol, err)
		}
		if protocol, err = ViewerProtocol(connector); err == nil || protocol != "" {
			t.Fatalf("metadata connector became a media session: %q %v", protocol, err)
		}
	}
	for _, connector := range []string{"HTTP\n", "UNKNOWN", "HTTP,HLS,WebRTC", "Raw/WS,info_json"} {
		if protocol, err := PlayRewriteProtocol(connector); err == nil || protocol != "" {
			t.Fatalf("invalid pre-source connector %q: %q %v", connector, protocol, err)
		}
	}
	for _, connector := range []string{"WebRTC/WS", "MP4/WS", "EBML/WS", "H264/WS", "JSON/WS"} {
		if !IsPlaybackViewerRequest(connector, "https://edge/media?metaeverywhere=1") {
			t.Fatalf("WebSocket media connector bypassed viewer admission: %q", connector)
		}
	}
}
