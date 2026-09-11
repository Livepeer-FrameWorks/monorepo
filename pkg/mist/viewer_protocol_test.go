package mist

import "testing"

func TestViewerProtocolUsesConnectorEvidence(t *testing.T) {
	for input, want := range map[string]string{
		"HLS": "hls", "HTTP, HLS": "hls", "WebRTC,JSON": "webrtc", "WHEP": "whep", "CMAF": "cmaf", "DASH": "dash", "HSS": "smoothstreaming",
		"RTMP": "rtmp", "RTSP": "rtsp", "SRT": "srt", "DTSC": "dtsc", "DTSCQUIC": "dtscquic", "MP4": "mp4", "WEBM": "webm", "EBML": "mkv",
		"Raw/WS": "raw_ws", "WSRaw": "raw_ws", "HTTPTS,TS": "ts", "AAC": "aac", "H264": "h264", "FLV": "flv", "MP3": "mp3", "OPUS": "opus",
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
