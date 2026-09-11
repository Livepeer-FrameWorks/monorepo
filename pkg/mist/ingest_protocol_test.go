package mist

import "testing"

func TestIngestProtocolMapsOnlyPublisherConnectors(t *testing.T) {
	for connector, want := range map[string]string{"RTMP": "rtmp", "rtmp": "rtmp", " TSSRT ": "srt", "WebRTC": "whip"} {
		got, err := IngestProtocol(connector)
		if err != nil || got != want {
			t.Fatalf("IngestProtocol(%q) = %q, %v; want %q", connector, got, err, want)
		}
	}
	for _, connector := range []string{"", "DTSC", "HLS", "HTTPTS", "TS", "TSRIST", "EBML", "JSON", "JSONLine", "RTSP", "rt\x00mp", "x" + string(make([]byte, 70))} {
		if got, err := IngestProtocol(connector); err == nil {
			t.Fatalf("IngestProtocol(%q) = %q, want refusal", connector, got)
		}
	}
}
