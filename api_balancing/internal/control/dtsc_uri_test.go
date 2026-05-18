package control

import (
	"testing"

	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// TestBuildDTSCURI pins the contract introduced when the helper became
// stream-name-prefix-agnostic so the active-DVR path could emit
// dtsc://<recording-node>/dvr+<dvr_internal_name> without a parallel
// builder. The function must:
//
//  1. Pass the stream name through verbatim (no implicit live+ prefix).
//  2. Return "" when the node has no DTSC output advertised — callers
//     fall back / abort rather than handing Mist a half-built URL.
func TestBuildDTSCURI(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	defer sm.Shutdown()

	const nodeID = "node-dtsc-1"
	const advertisedHost = "node-1.example.com"
	sm.SetNodeInfo(nodeID, "https://"+advertisedHost, true, nil, nil, "", "", map[string]any{
		"DTSC": "dtsc://HOST/$",
	})

	logger := logging.NewLogger()

	cases := []struct {
		name       string
		nodeID     string
		streamName string
		want       string
	}{
		{
			name:       "live stream prefix preserved",
			nodeID:     nodeID,
			streamName: "live+stream_abc",
			want:       "dtsc://" + advertisedHost + "/live+stream_abc",
		},
		{
			name:       "dvr stream prefix preserved (no implicit live+)",
			nodeID:     nodeID,
			streamName: "dvr+dvr_int_001",
			want:       "dtsc://" + advertisedHost + "/dvr+dvr_int_001",
		},
		{
			name:       "node without DTSC output returns empty",
			nodeID:     "node-no-dtsc",
			streamName: "live+stream_abc",
			want:       "",
		},
		{
			name:       "empty stream name returns empty",
			nodeID:     nodeID,
			streamName: "",
			want:       "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := BuildDTSCURI(tc.nodeID, tc.streamName, logger)
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
