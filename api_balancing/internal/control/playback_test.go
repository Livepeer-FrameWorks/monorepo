package control

import (
	"context"
	"math"
	"strings"
	"testing"

	pb "frameworks/pkg/proto"
)

func TestExtractPublicHostFromOutputs(t *testing.T) {
	tests := []struct {
		name     string
		outputs  map[string]interface{}
		expected string
	}{
		{
			name: "hls_protocol_relative",
			outputs: map[string]interface{}{
				"HLS": "//localhost:18090/view/stream/index.m3u8",
			},
			expected: "localhost:18090",
		},
		{
			name: "http_array",
			outputs: map[string]interface{}{
				"HTTP": []interface{}{"http://media.example.com:8080/live/stream/index.m3u8"},
			},
			expected: "media.example.com:8080",
		},
		{
			name: "no_matches",
			outputs: map[string]interface{}{
				"WHEP": "https://example.com/webrtc/stream",
			},
			expected: "",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual := ExtractPublicHostFromOutputs(test.outputs)
			if actual != test.expected {
				t.Fatalf("expected %q, got %q", test.expected, actual)
			}
		})
	}
}

func TestBuildOutputsMap(t *testing.T) {
	rawOutputs := map[string]interface{}{
		"HLS":  "//public.example.com:18090/view/$/index.m3u8",
		"HTTP": "http://public.example.com:8080/live/$/index.m3u8",
		"RTMP": "rtmp://HOST:1935/live/$",
	}

	outputs := BuildOutputsMap("https://edge-egress.example.com/live", rawOutputs, "stream", false)

	if outputs["MIST_HTML"].Url != "https://edge-egress.example.com/live/stream.html" {
		t.Fatalf("unexpected MIST_HTML url: %q", outputs["MIST_HTML"].Url)
	}
	if outputs["PLAYER_JS"].Url != "https://edge-egress.example.com/live/player.js" {
		t.Fatalf("unexpected PLAYER_JS url: %q", outputs["PLAYER_JS"].Url)
	}
	if outputs["WHEP"].Url != "https://edge-egress.example.com/live/webrtc/stream" {
		t.Fatalf("unexpected WHEP url: %q", outputs["WHEP"].Url)
	}
	if outputs["HLS"].Url != "//public.example.com:18090/view/stream/index.m3u8" {
		t.Fatalf("unexpected HLS url: %q", outputs["HLS"].Url)
	}
	if outputs["RTMP"].Url != "rtmp://edge-egress.example.com/live:1935/live/stream" {
		t.Fatalf("unexpected RTMP url: %q", outputs["RTMP"].Url)
	}
}

func TestResolveTemplateURL(t *testing.T) {
	tests := []struct {
		name       string
		raw        interface{}
		baseURL    string
		streamName string
		expected   string
	}{
		{
			name:       "non_string_raw",
			raw:        map[string]interface{}{"url": "http://example.com"},
			baseURL:    "https://edge-egress.example.com/live",
			streamName: "stream",
			expected:   "",
		},
		{
			name:       "array_non_string",
			raw:        []interface{}{123},
			baseURL:    "https://edge-egress.example.com/live",
			streamName: "stream",
			expected:   "",
		},
		{
			name:       "host_placeholder_missing_base",
			raw:        "rtmp://HOST:1935/live/$",
			baseURL:    "",
			streamName: "stream",
			expected:   "",
		},
		{
			name:       "host_placeholder_valid_base",
			raw:        "rtmp://HOST:1935/live/$",
			baseURL:    "https://edge-egress.example.com/live",
			streamName: "stream",
			expected:   "rtmp://edge-egress.example.com/live:1935/live/stream",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual := ResolveTemplateURL(test.raw, test.baseURL, test.streamName)
			if actual != test.expected {
				t.Fatalf("expected %q, got %q", test.expected, actual)
			}
		})
	}
}

func TestSelectPrimaryArtifactOutputFallback(t *testing.T) {
	outputs := map[string]interface{}{
		"HLS":  []interface{}{123},
		"DASH": "https://cdn.example.com/dash/$/index.mpd",
	}

	protocol, url := selectPrimaryArtifactOutput(outputs, "https://edge-egress.example.com/live", "stream", "m3u8")
	if protocol != "dash" {
		t.Fatalf("expected protocol %q, got %q", "dash", protocol)
	}
	if url != "https://cdn.example.com/dash/stream/index.mpd" {
		t.Fatalf("expected url %q, got %q", "https://cdn.example.com/dash/stream/index.mpd", url)
	}
}

func TestBuildOutputCapabilities(t *testing.T) {
	tests := []struct {
		name             string
		protocol         string
		isLive           bool
		expectedSeek     bool
		expectedQuality  bool
		expectedHasAudio bool
		expectedHasVideo bool
	}{
		{
			name:             "live_default",
			protocol:         "HLS",
			isLive:           true,
			expectedSeek:     false,
			expectedQuality:  true,
			expectedHasAudio: true,
			expectedHasVideo: true,
		},
		{
			name:             "vod_mp4",
			protocol:         "MP4",
			isLive:           false,
			expectedSeek:     true,
			expectedQuality:  false,
			expectedHasAudio: true,
			expectedHasVideo: true,
		},
		{
			name:             "whep_live",
			protocol:         "WHEP",
			isLive:           true,
			expectedSeek:     false,
			expectedQuality:  false,
			expectedHasAudio: true,
			expectedHasVideo: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			caps := BuildOutputCapabilities(test.protocol, test.isLive)
			if caps.SupportsSeek != test.expectedSeek {
				t.Fatalf("expected SupportsSeek=%v got %v", test.expectedSeek, caps.SupportsSeek)
			}
			if caps.SupportsQualitySwitch != test.expectedQuality {
				t.Fatalf("expected SupportsQualitySwitch=%v got %v", test.expectedQuality, caps.SupportsQualitySwitch)
			}
			if caps.HasAudio != test.expectedHasAudio {
				t.Fatalf("expected HasAudio=%v got %v", test.expectedHasAudio, caps.HasAudio)
			}
			if caps.HasVideo != test.expectedHasVideo {
				t.Fatalf("expected HasVideo=%v got %v", test.expectedHasVideo, caps.HasVideo)
			}
		})
	}
}

func TestDeriveWHEPFromHTML(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "valid_html",
			input:    "https://example.com/live/stream.html",
			expected: "https://example.com/live/webrtc/stream",
		},
		{
			name:     "not_html",
			input:    "https://example.com/live/stream.m3u8",
			expected: "",
		},
		{
			name:     "invalid_url",
			input:    ":://bad-url",
			expected: "",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual := DeriveWHEPFromHTML(test.input)
			if actual != test.expected {
				t.Fatalf("expected %q, got %q", test.expected, actual)
			}
		})
	}
}

func TestCalculateGeoDistance(t *testing.T) {
	tests := []struct {
		name     string
		lat1     float64
		lon1     float64
		lat2     float64
		lon2     float64
		expected float64
		maxDelta float64
	}{
		{
			name:     "same_point",
			lat1:     0,
			lon1:     0,
			lat2:     0,
			lon2:     0,
			expected: 0,
			maxDelta: 0.0001,
		},
		{
			name:     "one_degree_equator",
			lat1:     0,
			lon1:     0,
			lat2:     0,
			lon2:     1,
			expected: 111.195,
			maxDelta: 0.1,
		},
		{
			name:     "half_earth",
			lat1:     0,
			lon1:     0,
			lat2:     0,
			lon2:     180,
			expected: 20015.086,
			maxDelta: 0.5,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual := CalculateGeoDistance(test.lat1, test.lon1, test.lat2, test.lon2)
			delta := math.Abs(actual - test.expected)
			if delta > test.maxDelta {
				t.Fatalf("expected %v +/- %v, got %v", test.expected, test.maxDelta, actual)
			}
		})
	}
}

func TestDeriveMistHTTPBase(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "default_port_from_4242",
			input:    "https://example.com:4242",
			expected: "https://example.com:8080",
		},
		{
			name:     "preserve_custom_port",
			input:    "http://example.com:3000",
			expected: "http://example.com:3000",
		},
		{
			name:     "no_scheme",
			input:    "example.com",
			expected: "http://example.com:8080",
		},
		{
			name:     "no_scheme_with_port",
			input:    "example.com:9090",
			expected: "http://example.com:8080",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual := DeriveMistHTTPBase(test.input)
			if actual != test.expected {
				t.Fatalf("expected %q, got %q", test.expected, actual)
			}
		})
	}
}

type stubPeerResolver struct{}

func (s stubPeerResolver) GetPeerAddr(clusterID string) string { return "foghorn.example:18019" }

type stubFedClient struct{}

func (s stubFedClient) PrepareArtifact(ctx context.Context, clusterID, addr string, req *pb.PrepareArtifactRequest) (*pb.PrepareArtifactResponse, error) {
	return &pb.PrepareArtifactResponse{Ready: true}, nil
}

func TestResolveRemoteArtifact_RejectsUnauthorizedOriginCluster(t *testing.T) {
	deps := &PlaybackDependencies{
		FedClient:      stubFedClient{},
		PeerResolver:   stubPeerResolver{},
		LocalClusterID: "cluster-local",
	}

	_, err := resolveRemoteArtifact(context.Background(), deps, "artifact-1", "cluster-other", "clip", "tenant-1", []*pb.TenantClusterPeer{{ClusterId: "cluster-allowed"}})
	if err == nil {
		t.Fatal("expected unauthorized origin cluster error")
	}
	if !strings.Contains(err.Error(), "not authorized") {
		t.Fatalf("expected not authorized error, got %v", err)
	}
}
