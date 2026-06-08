package balancer

import (
	"testing"

	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// ScoreRemoteEdges clamps three out-of-range inputs before scoring: CPU% > 100,
// RAM used > max, and available bandwidth > the reference capacity. Without the
// clamps the cpu/ram fractions would go negative and the uint64 score math would
// wrap around to an enormous value (and bw would over-credit). This drives all
// three clamps at once and pins the exact resulting score, which is only correct
// if every clamp fires.
//
// With weights cpu=100 ram=100 bw=300 geo=100 and identical edge/viewer coords:
//
//	cpuScore = 100*(1-clamp(1.5))   = 0
//	ramScore = 100*(1-clamp(2.0))   = 0
//	bwScore  = clamp(2*ref)*300/ref = 300
//	geoScore = 100 - 100*dist(0)    = 100
//	raw = 400; final = 400 - crossClusterPenalty(200) = 200
func TestScoreRemoteEdges_ClampsOutOfRangeInputs(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	sm.SetWeights(100, 100, 300, 100, 0)

	lb := NewLoadBalancer(logging.NewLoggerWithService("test"))
	scored := lb.ScoreRemoteEdges([]RemoteEdgeCandidate{
		{
			ClusterID:   "remote",
			NodeID:      "node-clamp",
			BaseURL:     "edge.example.com",
			GeoLat:      10,
			GeoLon:      20,
			CPUPercent:  150, // > 100 -> clamp to 1.0
			RAMUsed:     200, // > RAMMax -> clamp to 1.0
			RAMMax:      100,
			BWAvailable: remoteBWRefCapacity * 2, // > ref -> clamp to ref
		},
	}, 10, 20)

	if len(scored) != 1 {
		t.Fatalf("expected the clamped candidate to still score, got %d results", len(scored))
	}
	if scored[0].Score != 200 {
		t.Fatalf("clamped score = %d, want 200 (cpu0 + ram0 + bw300 + geo100 - penalty200)", scored[0].Score)
	}
	if scored[0].ClusterID != "remote" || scored[0].NodeID != "node-clamp" {
		t.Fatalf("unexpected identity: %+v", scored[0])
	}
}
