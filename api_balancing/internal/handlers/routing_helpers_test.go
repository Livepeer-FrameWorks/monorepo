package handlers

import (
	"testing"

	"frameworks/api_balancing/internal/state"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/pullsource"
)

// TestNormalizeProtocol pins the protocol-alias map that routing relies on to
// canonicalise the many spellings clients and MistServer use for the same
// delivery protocol. The contract: every known alias group folds to one
// canonical token, matching is case-insensitive, the empty/any/all wildcards
// resolve to "any", and an unknown protocol passes through lowercased (never
// dropped). A regression that silently loses an alias would misroute playback,
// so each alias is asserted explicitly.
func TestNormalizeProtocol(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		// Adaptive streaming
		{"m3u8", "hls"}, {"hls", "hls"},
		{"mpd", "dash"}, {"dash", "dash"},
		{"cmaf", "cmaf"}, {"llhls", "cmaf"}, {"ll-hls", "cmaf"},
		// Low latency
		{"webrtc", "webrtc"}, {"whep", "webrtc"},
		{"srt", "srt"},
		// Legacy streaming
		{"rtmp", "rtmp"},
		{"rtsp", "rtsp"},
		// Container formats
		{"mp4", "mp4"}, {"progressive", "mp4"},
		{"webm", "webm"},
		{"mkv", "mkv"}, {"matroska", "mkv"},
		{"ts", "ts"}, {"mpegts", "ts"}, {"mpeg-ts", "ts"},
		{"flv", "flv"}, {"flash", "flv"},
		{"aac", "aac"}, {"audio", "aac"},
		// Microsoft/Adobe
		{"smooth", "smoothstreaming"}, {"smoothstreaming", "smoothstreaming"}, {"hss", "smoothstreaming"},
		{"hds", "hds"}, {"f4m", "hds"}, {"dynamic", "hds"},
		// Other
		{"sdp", "sdp"},
		{"h264", "h264"}, {"rawh264", "h264"}, {"raw", "h264"},
		{"dtsc", "dtsc"}, {"mist", "dtsc"},
		{"wsmp4", "wsmp4"},
		{"wswebrtc", "wswebrtc"},
		// Wildcards
		{"any", "any"}, {"all", "any"}, {"", "any"},
		// Case-insensitivity
		{"HLS", "hls"}, {"WebRTC", "webrtc"}, {"Mpeg-TS", "ts"},
		// Unknown passes through lowercased, never dropped
		{"weirdformat", "weirdformat"}, {"FooBar", "foobar"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := normalizeProtocol(tc.in); got != tc.want {
				t.Fatalf("normalizeProtocol(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestGetTotalViewers pins the per-node viewer aggregation: the total is the sum
// of Viewers across every stream summary on the node, with an empty node
// summing to zero.
func TestGetTotalViewers(t *testing.T) {
	cases := []struct {
		name string
		node state.EnhancedBalancerNodeSnapshot
		want uint64
	}{
		{"no streams", state.EnhancedBalancerNodeSnapshot{}, 0},
		{
			name: "sums across streams",
			node: state.EnhancedBalancerNodeSnapshot{Streams: map[string]state.BalancerStreamSummary{
				"a": {Viewers: 3},
				"b": {Viewers: 5},
				"c": {Viewers: 0},
			}},
			want: 8,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := getTotalViewers(tc.node); got != tc.want {
				t.Fatalf("getTotalViewers = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestSummarizePullPlacementRejects pins the human-readable rendering of pull
// placement rejections: each reason code maps to its own phrasing (with the
// cluster id quoted where relevant), unknown reasons fall through to a generic
// "rejected" line, and multiple rejects join with "; ". An empty slice is "".
func TestSummarizePullPlacementRejects(t *testing.T) {
	cases := []struct {
		name    string
		rejects []pullsource.PlacementReject
		want    string
	}{
		{"empty", nil, ""},
		{
			name:    "empty for private",
			rejects: []pullsource.PlacementReject{{Reason: pullsource.PlacementRejectEmptyForPrivate}},
			want:    "private/multicast source has no allowed_cluster_ids configured",
		},
		{
			name:    "unknown cluster quotes id",
			rejects: []pullsource.PlacementReject{{ClusterID: "edge-1", Reason: pullsource.PlacementRejectUnknownCluster}},
			want:    `cluster "edge-1" is not in allowed_cluster_ids`,
		},
		{
			name:    "missing private capability quotes id",
			rejects: []pullsource.PlacementReject{{ClusterID: "edge-2", Reason: pullsource.PlacementRejectMissingPrivateCapability}},
			want:    `cluster "edge-2" does not allow private pull sources`,
		},
		{
			name:    "unknown reason falls through to generic",
			rejects: []pullsource.PlacementReject{{ClusterID: "edge-3", Reason: pullsource.PlacementRejectReason("weird")}},
			want:    `cluster "edge-3" rejected: weird`,
		},
		{
			name: "multiple joined with semicolon",
			rejects: []pullsource.PlacementReject{
				{ClusterID: "edge-1", Reason: pullsource.PlacementRejectUnknownCluster},
				{ClusterID: "edge-2", Reason: pullsource.PlacementRejectMissingPrivateCapability},
			},
			want: `cluster "edge-1" is not in allowed_cluster_ids; cluster "edge-2" does not allow private pull sources`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := summarizePullPlacementRejects(tc.rejects); got != tc.want {
				t.Fatalf("summarizePullPlacementRejects = %q, want %q", got, tc.want)
			}
		})
	}
}
