package pullsource

import "testing"

func TestValidateURI(t *testing.T) {
	tests := []struct {
		name string
		uri  string
	}{
		{name: "hls", uri: "https://ntv1.akamaized.net/hls/live/2014075/NASA-NTV1-HLS/master.m3u8"},
		{name: "rtsp", uri: "rtsp://example.com/live"},
		{name: "srt", uri: "srt://example.com:9000"},
		{name: "rist", uri: "rist://example.com:8000"},
		{name: "dtsc", uri: "dtsc://origin.example.com:4200"},
		{name: "ts", uri: "https://example.com/live/stream.ts"},
		{name: "tsudp", uri: "tsudp://example.com:9000"},
		{name: "tsudp private unicast", uri: "tsudp://10.0.0.5:9000"},
		{name: "tsudp multicast", uri: "tsudp://239.1.2.3:9000"},
		{name: "ebml", uri: "https://example.com/live/stream.webm"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := ValidateURI(tt.uri); err != nil {
				t.Fatalf("ValidateURI: %v", err)
			}
		})
	}
}

func TestValidateURIRejectsUnsupportedOrUnsafe(t *testing.T) {
	tests := []string{
		"https://example.com/live",
		"ftp://example.com/live.m3u8",
		"https://localhost/live.m3u8",
		"https://127.0.0.1/live.m3u8",
		"https://10.0.0.1/live.m3u8",
		"https://169.254.169.254/latest/meta-data/live.m3u8",
		"tsudp://127.0.0.1:9000",
		"tsudp://224.0.0.1:9000",
		"rist://localhost:8000",
	}
	for _, uri := range tests {
		t.Run(uri, func(t *testing.T) {
			if err := ValidateURI(uri); err == nil {
				t.Fatal("ValidateURI succeeded, want error")
			}
		})
	}
}

func TestRedact(t *testing.T) {
	got := Redact("rtsp://user:pass@example.com/live")
	if got != "rtsp://example.com" {
		t.Fatalf("Redact = %q", got)
	}
}
