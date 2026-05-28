package control

import (
	"context"
	"database/sql"
	"math"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"frameworks/api_balancing/internal/balancer"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/pullsource"
)

func TestExtractPublicHostFromOutputs(t *testing.T) {
	tests := []struct {
		name     string
		outputs  map[string]any
		expected string
	}{
		{
			name: "hls_protocol_relative",
			outputs: map[string]any{
				"HLS": "//localhost:18090/view/stream/index.m3u8",
			},
			expected: "localhost:18090",
		},
		{
			name: "http_array",
			outputs: map[string]any{
				"HTTP": []any{"http://media.example.com:8080/live/stream/index.m3u8"},
			},
			expected: "media.example.com:8080",
		},
		{
			name: "no_matches",
			outputs: map[string]any{
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
	rawOutputs := map[string]any{
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
	if outputs["RTMP"].Url != "rtmp://public.example.com:1935/live/stream" {
		t.Fatalf("unexpected RTMP url: %q", outputs["RTMP"].Url)
	}
}

func TestBuildOutputsMapNormalizesMistMetricsOutputs(t *testing.T) {
	rawOutputs := map[string]any{
		"AAC":    "[\"https://mist-seattle.stronk.rocks/view/$.aac",
		"CMAF":   "[\"https://mist-seattle.stronk.rocks/view/cmaf/$/",
		"DTSC":   "dtsc://HOST/$",
		"EBML":   "[\"https://mist-seattle.stronk.rocks/view/$.webm",
		"H264":   "[\"https://mist-seattle.stronk.rocks/view/$.h264",
		"HLS":    "[\"https://mist-seattle.stronk.rocks/view/hls/$/index.m3u8",
		"HTTP":   "[\"https://mist-seattle.stronk.rocks/view/$.html",
		"HTTPTS": "[\"https://mist-seattle.stronk.rocks/view/$.ts",
		"RTMP":   "rtmp://HOST/play/$",
		"RTSP":   "rtsp://HOST:5554/$",
		"TSSRT":  "srt://HOST/?streamid=$",
		"WebRTC": "ws://HOST:18203/webrtc/$",
	}

	outputs := BuildOutputsMap("https://mist-seattle.stronk.rocks/view", rawOutputs, "live+titan", true)

	expected := map[string]string{
		"HLS":         "https://mist-seattle.stronk.rocks/view/hls/live+titan/index.m3u8",
		"HLS_CMAF":    "https://mist-seattle.stronk.rocks/view/cmaf/live+titan/",
		"WEBM":        "https://mist-seattle.stronk.rocks/view/live+titan.webm",
		"TS":          "https://mist-seattle.stronk.rocks/view/live+titan.ts",
		"AAC":         "https://mist-seattle.stronk.rocks/view/live+titan.aac",
		"H264":        "https://mist-seattle.stronk.rocks/view/live+titan.h264",
		"MIST_WEBRTC": "ws://mist-seattle.stronk.rocks:18203/webrtc/live+titan",
		"RTMP":        "rtmp://mist-seattle.stronk.rocks/play/live+titan",
		"RTSP":        "rtsp://mist-seattle.stronk.rocks:5554/live+titan",
		"SRT":         "srt://mist-seattle.stronk.rocks/?streamid=live+titan",
		"DTSC":        "dtsc://mist-seattle.stronk.rocks/live+titan",
	}
	for protocol, expectedURL := range expected {
		got, ok := outputs[protocol]
		if !ok {
			t.Fatalf("missing %s output", protocol)
		}
		if got.Url != expectedURL {
			t.Fatalf("unexpected %s url: %q", protocol, got.Url)
		}
	}
	if _, ok := outputs["TSSRT"]; ok {
		t.Fatal("TSSRT should be normalized to SRT")
	}
	if _, ok := outputs["HTTPTS"]; ok {
		t.Fatal("HTTPTS should be normalized to TS")
	}
	if _, ok := outputs["EBML"]; ok {
		t.Fatal("EBML should be normalized to WEBM")
	}
}

func TestBuildOutputsMapOmitsGenericHTTPForLive(t *testing.T) {
	rawOutputs := map[string]any{
		"HLS":  "http://public.example.com:18090/view/hls/$/index.m3u8",
		"HTTP": "http://public.example.com:18090/view/$.html",
	}

	outputs := BuildOutputsMap("http://public.example.com:18090/view", rawOutputs, "stream", true)

	if _, ok := outputs["HTTP"]; ok {
		t.Fatal("live outputs should not expose Mist HTTP HTML as progressive HTTP media")
	}
	if outputs["HLS"].Url != "http://public.example.com:18090/view/hls/stream/index.m3u8" {
		t.Fatalf("unexpected HLS url: %q", outputs["HLS"].Url)
	}
}

func TestFilterPullCandidatesByClassFiltersRemoteClusters(t *testing.T) {
	nodes := []balancer.NodeWithScore{
		{NodeID: "local-a", Score: 90},
		{NodeID: "remote-denied", ClusterID: "remote-denied", Score: 95},
		{NodeID: "remote-allowed", ClusterID: "remote-allowed", Score: 80},
	}
	allowed, err := filterPullCandidatesByClass(
		context.Background(),
		nodes,
		"pull-demo",
		"local-allowed",
		pullsource.ClassPrivate,
		[]string{"local-allowed", "remote-allowed"},
		func(_ context.Context, clusterID string) bool {
			return clusterID == "local-allowed" || clusterID == "remote-allowed"
		},
	)
	if err != nil {
		t.Fatalf("filterPullCandidatesByClass: %v", err)
	}
	got := make([]string, 0, len(allowed))
	for _, n := range allowed {
		got = append(got, n.NodeID)
	}
	if strings.Join(got, ",") != "local-a,remote-allowed" {
		t.Fatalf("allowed nodes = %q, want local-a,remote-allowed", strings.Join(got, ","))
	}
}

// TestFilterPullCandidatesByClassRefusesPrivateWithoutAllowedList locks the
// new placement invariant: a private pull source with an empty
// allowed_cluster_ids list refuses every candidate at the viewer-routing
// chokepoint, regardless of whether candidate clusters have the capability
// flag. No implicit fallback to "any opted-in cluster".
func TestFilterPullCandidatesByClassRefusesPrivateWithoutAllowedList(t *testing.T) {
	nodes := []balancer.NodeWithScore{
		{NodeID: "local-a", Score: 90},
		{NodeID: "remote-allowed", ClusterID: "remote-allowed", Score: 80},
	}
	_, err := filterPullCandidatesByClass(
		context.Background(),
		nodes,
		"pull-demo",
		"local-allowed",
		pullsource.ClassPrivate,
		nil, // empty allowed list — must refuse
		func(_ context.Context, _ string) bool { return true },
	)
	if err == nil {
		t.Fatal("private source with empty allowed_cluster_ids must error")
	}
	if !strings.Contains(err.Error(), "allowed_cluster_ids") {
		t.Fatalf("error %q does not name the placement constraint", err)
	}
}

// TestFilterPullCandidatesByClassPinsPublicSource verifies a public source
// with explicit allowed_cluster_ids is pinned to those clusters.
func TestFilterPullCandidatesByClassPinsPublicSource(t *testing.T) {
	nodes := []balancer.NodeWithScore{
		{NodeID: "local-a", Score: 90},
		{NodeID: "remote-denied", ClusterID: "remote-denied", Score: 95},
		{NodeID: "remote-allowed", ClusterID: "remote-allowed", Score: 80},
	}
	allowed, err := filterPullCandidatesByClass(
		context.Background(),
		nodes,
		"pull-public",
		"local-allowed",
		pullsource.ClassPublic,
		[]string{"local-allowed"},
		nil,
	)
	if err != nil {
		t.Fatalf("filterPullCandidatesByClass: %v", err)
	}
	if len(allowed) != 1 || allowed[0].NodeID != "local-a" {
		t.Fatalf("allowed = %+v, want only local-a", allowed)
	}
}

func TestBuildOutputsMapIncludesMistWebSocketOutputs(t *testing.T) {
	rawOutputs := map[string]any{
		"MP4":   "https://public.example.com:18090/view/$.mp4",
		"WEBM":  "//public.example.com:18090/view/$.webm",
		"WSRaw": "https://public.example.com:18090/view/$.raw",
		"H264":  "http://public.example.com:18090/view/$.h264",
	}

	outputs := BuildOutputsMap("https://edge-egress.example.com/view", rawOutputs, "stream", true)

	if outputs["MEWS"].Url != "wss://public.example.com:18090/view/stream.mp4" {
		t.Fatalf("unexpected MEWS url: %q", outputs["MEWS"].Url)
	}
	if outputs["MEWS_WEBM"].Url != "wss://public.example.com:18090/view/stream.webm" {
		t.Fatalf("unexpected MEWS_WEBM url: %q", outputs["MEWS_WEBM"].Url)
	}
	if outputs["RAW_WS"].Url != "wss://public.example.com:18090/view/stream.raw" {
		t.Fatalf("unexpected RAW_WS url: %q", outputs["RAW_WS"].Url)
	}
	if outputs["H264_WS"].Url != "ws://public.example.com:18090/view/stream.h264" {
		t.Fatalf("unexpected H264_WS url: %q", outputs["H264_WS"].Url)
	}
}

func TestBuildOutputsMapAcceptsMistDisplayOutputNames(t *testing.T) {
	rawOutputs := map[string]any{
		"HLS (TS)":                    "http://public.example.com:18090/view/hls/$/index.m3u8",
		"MP4 WebSocket":               "ws://public.example.com:18090/view/$.mp4",
		"Raw WebSocket":               "ws://public.example.com:18090/view/$.raw",
		"Annex B WebSocket":           "ws://public.example.com:18090/view/$.h264",
		"WebRTC with WHEP signalling": "http://public.example.com:18090/view/webrtc/$",
		"AAC progressive":             "http://public.example.com:18090/view/$.aac",
	}

	outputs := BuildOutputsMap("http://edge-egress.example.com/view", rawOutputs, "stream", true)

	if outputs["HLS"].Url != "http://public.example.com:18090/view/hls/stream/index.m3u8" {
		t.Fatalf("unexpected HLS url: %q", outputs["HLS"].Url)
	}
	if outputs["MEWS"].Url != "ws://public.example.com:18090/view/stream.mp4" {
		t.Fatalf("unexpected MEWS url: %q", outputs["MEWS"].Url)
	}
	if outputs["RAW_WS"].Url != "ws://public.example.com:18090/view/stream.raw" {
		t.Fatalf("unexpected RAW_WS url: %q", outputs["RAW_WS"].Url)
	}
	if outputs["H264_WS"].Url != "ws://public.example.com:18090/view/stream.h264" {
		t.Fatalf("unexpected H264_WS url: %q", outputs["H264_WS"].Url)
	}
	if outputs["WHEP"].Url != "http://public.example.com:18090/view/webrtc/stream" {
		t.Fatalf("unexpected WHEP url: %q", outputs["WHEP"].Url)
	}
	if outputs["AAC"].Url != "http://public.example.com:18090/view/stream.aac" {
		t.Fatalf("unexpected AAC url: %q", outputs["AAC"].Url)
	}
	if outputs["AAC"].Capabilities.HasVideo {
		t.Fatal("AAC output should not advertise video")
	}
}

func TestBuildViewerEndpointFromOutputsAcceptsMistDisplayHLSName(t *testing.T) {
	nodeOutputs := &NodeOutputs{
		NodeID:  "edge-1",
		BaseURL: "http://edge-egress.example.com/view",
		Outputs: map[string]any{
			"HLS (TS)": "http://public.example.com:18090/view/hls/$/index.m3u8",
		},
	}

	endpoint := BuildViewerEndpointFromOutputs("edge-1", nodeOutputs, "stream", true)
	if endpoint == nil {
		t.Fatal("expected endpoint")
	}
	if endpoint.Protocol != "hls" {
		t.Fatalf("protocol = %q, want hls", endpoint.Protocol)
	}
	if endpoint.Url != "http://public.example.com:18090/view/hls/stream/index.m3u8" {
		t.Fatalf("url = %q", endpoint.Url)
	}
	if endpoint.Outputs["HLS"].Url != endpoint.Url {
		t.Fatalf("canonical HLS output = %q, want %q", endpoint.Outputs["HLS"].Url, endpoint.Url)
	}
}

func TestBuildOutputsMapDerivesStandardMistPlaybackOutputs(t *testing.T) {
	outputs := BuildOutputsMap("http://public.example.com:18090/view", map[string]any{}, "stream", true)

	expected := map[string]string{
		"RAW_WS":  "ws://public.example.com:18090/view/stream.raw",
		"MEWS":    "ws://public.example.com:18090/view/stream.mp4",
		"H264_WS": "ws://public.example.com:18090/view/stream.h264",
		"MP4":     "http://public.example.com:18090/view/stream.mp4",
		"HLS":     "http://public.example.com:18090/view/hls/stream/index.m3u8",
	}
	for protocol, expectedURL := range expected {
		if outputs[protocol].Url != expectedURL {
			t.Fatalf("unexpected %s url: %q", protocol, outputs[protocol].Url)
		}
	}
}

func TestResolveTemplateURL(t *testing.T) {
	tests := []struct {
		name       string
		raw        any
		baseURL    string
		streamName string
		expected   string
	}{
		{
			name:       "non_string_raw",
			raw:        map[string]any{"url": "http://example.com"},
			baseURL:    "https://edge-egress.example.com/live",
			streamName: "stream",
			expected:   "",
		},
		{
			name:       "array_non_string",
			raw:        []any{123},
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
			expected:   "rtmp://edge-egress.example.com:1935/live/stream",
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
	outputs := map[string]any{
		"HLS":  []any{123},
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

func TestRemoteArtifactFiltering_ExcludesUnauthorizedPeers(t *testing.T) {
	remoteHits := []*RemoteArtifactInfo{
		{PeerCluster: "authorized-1", BaseURL: "https://a.example.com", GeoLat: 0, GeoLon: 0},
		{PeerCluster: "unauthorized", BaseURL: "https://b.example.com", GeoLat: 0, GeoLon: 0},
	}
	allowedClusters := []*pb.TenantClusterPeer{{ClusterId: "authorized-1"}}

	var authorizedHits []*RemoteArtifactInfo
	for _, h := range remoteHits {
		if isAuthorizedPeerCluster(h.PeerCluster, allowedClusters) {
			authorizedHits = append(authorizedHits, h)
		}
	}

	if len(authorizedHits) != 1 {
		t.Fatalf("expected 1 authorized hit, got %d", len(authorizedHits))
	}
	if authorizedHits[0].PeerCluster != "authorized-1" {
		t.Fatalf("expected authorized-1, got %s", authorizedHits[0].PeerCluster)
	}

	best := pickBestRemoteArtifact(authorizedHits, 0, 0)
	if best == nil {
		t.Fatal("expected non-nil best artifact")
	}
	if best.PeerCluster != "authorized-1" {
		t.Fatalf("expected authorized-1, got %s", best.PeerCluster)
	}
}

func TestRemoteArtifactFiltering_AllUnauthorizedYieldsNoHits(t *testing.T) {
	remoteHits := []*RemoteArtifactInfo{
		{PeerCluster: "unauthorized-1", BaseURL: "https://a.example.com"},
		{PeerCluster: "unauthorized-2", BaseURL: "https://b.example.com"},
	}
	allowedClusters := []*pb.TenantClusterPeer{{ClusterId: "some-other-cluster"}}

	var authorizedHits []*RemoteArtifactInfo
	for _, h := range remoteHits {
		if isAuthorizedPeerCluster(h.PeerCluster, allowedClusters) {
			authorizedHits = append(authorizedHits, h)
		}
	}

	if len(authorizedHits) != 0 {
		t.Fatalf("expected 0 authorized hits, got %d", len(authorizedHits))
	}
}

type stubPeerResolver struct{}

func (s stubPeerResolver) GetPeerAddr(clusterID string) string { return "foghorn.example:18029" }

type stubFedClient struct{}

func (s stubFedClient) PrepareArtifact(ctx context.Context, clusterID, addr string, req *pb.PrepareArtifactRequest) (*pb.PrepareArtifactResponse, error) {
	return &pb.PrepareArtifactResponse{Ready: true, InternalName: "stream-a", StreamInternalName: "source-stream-a", Format: "mp4"}, nil
}

func TestResolveRemoteArtifact_RejectsUnauthorizedOriginCluster(t *testing.T) {
	deps := &PlaybackDependencies{
		FedClient:      stubFedClient{},
		PeerResolver:   stubPeerResolver{},
		LocalClusterID: "cluster-local",
	}

	_, err := resolveRemoteArtifact(context.Background(), deps, "playback-1", "artifact-1", "cluster-other", "clip", "tenant-1", []*pb.TenantClusterPeer{{ClusterId: "cluster-allowed"}}, nil)
	if err == nil {
		t.Fatal("expected unauthorized origin cluster error")
	}
	if !strings.Contains(err.Error(), "not authorized") {
		t.Fatalf("expected not authorized error, got %v", err)
	}
}

func TestResolveRemoteArtifact_RejectsWhenTenantPeerDataMissing(t *testing.T) {
	deps := &PlaybackDependencies{
		FedClient:      stubFedClient{},
		PeerResolver:   stubPeerResolver{},
		LocalClusterID: "cluster-local",
	}

	_, err := resolveRemoteArtifact(context.Background(), deps, "playback-1", "artifact-1", "cluster-origin", "clip", "tenant-1", nil, nil)
	if err == nil {
		t.Fatal("expected authorization error when peer list is unavailable")
	}
	if !strings.Contains(err.Error(), "not authorized") {
		t.Fatalf("expected not authorized error, got %v", err)
	}
}

func TestResolveRemoteArtifact_AdoptionUpsertHealsMissingOriginMetadata(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer mockDB.Close()

	mock.ExpectExec("INSERT INTO foghorn.artifacts").
		WithArgs("artifact-1", "clip", "tenant-1", "stream-a", "source-stream-a", "mp4", "cluster-origin", sql.NullString{}).
		WillReturnResult(sqlmock.NewResult(0, 1))

	deps := &PlaybackDependencies{
		DB:             mockDB,
		FedClient:      stubFedClient{},
		PeerResolver:   stubPeerResolver{},
		LocalClusterID: "cluster-local",
	}

	_, err = resolveRemoteArtifact(context.Background(), deps, "playback-1", "artifact-1", "cluster-origin", "clip", "tenant-1", []*pb.TenantClusterPeer{{ClusterId: "cluster-origin"}}, nil)
	if err == nil {
		t.Fatal("expected error from recursed resolution after adoption (artifactResp nil)")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

// redirectFedClient simulates an origin cluster that redirects PrepareArtifact
// to a different storage cluster. The first call (to origin) returns a
// redirect; the second (to the redirect target) returns ready=true.
type redirectFedClient struct {
	originCluster   string
	redirectCluster string
	calls           int
}

func (s *redirectFedClient) PrepareArtifact(ctx context.Context, clusterID, addr string, req *pb.PrepareArtifactRequest) (*pb.PrepareArtifactResponse, error) {
	s.calls++
	if clusterID == s.originCluster {
		return &pb.PrepareArtifactResponse{RedirectClusterId: s.redirectCluster}, nil
	}
	return &pb.PrepareArtifactResponse{
		Ready:              true,
		InternalName:       "stream-a",
		StreamInternalName: "source-stream-a",
		Format:             "mp4",
	}, nil
}

// TestResolveRemoteArtifact_RedirectPreservesOriginCluster asserts that when
// PrepareArtifact returns redirect_cluster_id, the local adoption row keeps
// the original origin_cluster_id (provenance) and writes the redirect target
// to storage_cluster_id (where the bytes live). Without this distinction,
// later read sites cannot tell origin and storage apart.
func TestResolveRemoteArtifact_RedirectPreservesOriginCluster(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer mockDB.Close()

	mock.ExpectExec("INSERT INTO foghorn.artifacts").
		WithArgs(
			"artifact-1", "clip", "tenant-1",
			"stream-a", "source-stream-a", "mp4",
			"cluster-origin", // origin_cluster_id stays original
			sql.NullString{String: "cluster-storage", Valid: true}, // storage_cluster_id captures redirect
		).
		WillReturnResult(sqlmock.NewResult(0, 1))

	deps := &PlaybackDependencies{
		DB: mockDB,
		FedClient: &redirectFedClient{
			originCluster:   "cluster-origin",
			redirectCluster: "cluster-storage",
		},
		PeerResolver:   stubPeerResolver{},
		LocalClusterID: "cluster-local",
	}

	_, err = resolveRemoteArtifact(context.Background(), deps, "playback-1",
		"artifact-1", "cluster-origin", "clip", "tenant-1",
		[]*pb.TenantClusterPeer{
			{ClusterId: "cluster-origin"},
			{ClusterId: "cluster-storage"},
		}, nil)
	if err == nil {
		t.Fatal("expected error from recursed resolution after adoption (artifactResp nil)")
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestBuildThumbnailAssets(t *testing.T) {
	tests := []struct {
		name         string
		chandlerBase string
		assetKey     string
		want         *pb.ThumbnailAssets
	}{
		{
			name:         "live stream",
			chandlerBase: "https://chandler.example.com",
			assetKey:     "stream-uuid-123",
			want: &pb.ThumbnailAssets{
				PosterUrl:    "https://chandler.example.com/assets/stream-uuid-123/poster.jpg",
				SpriteVttUrl: "https://chandler.example.com/assets/stream-uuid-123/sprite.vtt",
				SpriteJpgUrl: "https://chandler.example.com/assets/stream-uuid-123/sprite.jpg",
				AssetKey:     "stream-uuid-123",
			},
		},
		{
			name:         "DVR artifact with trailing slash",
			chandlerBase: "https://chandler.example.com/",
			assetKey:     "abc123hash",
			want: &pb.ThumbnailAssets{
				PosterUrl:    "https://chandler.example.com/assets/abc123hash/poster.jpg",
				SpriteVttUrl: "https://chandler.example.com/assets/abc123hash/sprite.vtt",
				SpriteJpgUrl: "https://chandler.example.com/assets/abc123hash/sprite.jpg",
				AssetKey:     "abc123hash",
			},
		},
		{
			name:         "empty base returns nil",
			chandlerBase: "",
			assetKey:     "key",
			want:         nil,
		},
		{
			name:         "empty asset key returns nil",
			chandlerBase: "https://chandler.example.com",
			assetKey:     "",
			want:         nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildThumbnailAssets(tt.chandlerBase, tt.assetKey)
			if tt.want == nil {
				if got != nil {
					t.Fatalf("expected nil, got %+v", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("expected non-nil, got nil")
			}
			if got.PosterUrl != tt.want.PosterUrl {
				t.Errorf("PosterUrl: got %q, want %q", got.PosterUrl, tt.want.PosterUrl)
			}
			if got.SpriteVttUrl != tt.want.SpriteVttUrl {
				t.Errorf("SpriteVttUrl: got %q, want %q", got.SpriteVttUrl, tt.want.SpriteVttUrl)
			}
			if got.SpriteJpgUrl != tt.want.SpriteJpgUrl {
				t.Errorf("SpriteJpgUrl: got %q, want %q", got.SpriteJpgUrl, tt.want.SpriteJpgUrl)
			}
			if got.AssetKey != tt.want.AssetKey {
				t.Errorf("AssetKey: got %q, want %q", got.AssetKey, tt.want.AssetKey)
			}
		})
	}
}

func TestBuildPosterThumbnailAssets(t *testing.T) {
	got := buildPosterThumbnailAssets("https://chandler.example.com/", "stream-uuid-123")
	if got == nil {
		t.Fatal("expected poster thumbnail assets")
	}
	if got.PosterUrl != "https://chandler.example.com/assets/stream-uuid-123/poster.jpg" {
		t.Fatalf("PosterUrl: got %q", got.PosterUrl)
	}
	if got.AssetKey != "stream-uuid-123" {
		t.Fatalf("AssetKey: got %q", got.AssetKey)
	}
	if got.SpriteVttUrl != "" || got.SpriteJpgUrl != "" {
		t.Fatalf("expected poster-only assets, got spriteVtt=%q spriteJpg=%q", got.SpriteVttUrl, got.SpriteJpgUrl)
	}
}

func TestArtifactNodesFromDBBuildsWarmNodeFallback(t *testing.T) {
	mockDB, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer mockDB.Close()

	mock.ExpectQuery("SELECT an.node_id").
		WithArgs("hash-1", "vod").
		WillReturnRows(sqlmock.NewRows([]string{
			"node_id", "file_path", "size_bytes", "format", "stream_internal_name",
		}).AddRow("edge-node-1", "/recordings/vod/hash-1.mp4", int64(2048), "mp4", "source-stream"))

	nodes, err := artifactNodesFromDB(context.Background(), mockDB, "hash-1", "vod")
	if err != nil {
		t.Fatal(err)
	}
	if len(nodes) != 1 {
		t.Fatalf("expected one node, got %d", len(nodes))
	}
	got := nodes[0]
	if got.NodeID != "edge-node-1" {
		t.Fatalf("node id = %q", got.NodeID)
	}
	if got.Artifact.GetClipHash() != "hash-1" {
		t.Fatalf("artifact hash = %q", got.Artifact.GetClipHash())
	}
	if got.Artifact.GetFormat() != "mp4" {
		t.Fatalf("format = %q", got.Artifact.GetFormat())
	}
	if got.Artifact.GetArtifactType() != pb.ArtifactEvent_ARTIFACT_TYPE_VOD {
		t.Fatalf("artifact type = %v", got.Artifact.GetArtifactType())
	}

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
