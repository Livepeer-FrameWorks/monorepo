package balancer

import (
	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	"testing"
)

func TestRoutingRejectsEightyFivePercentCapacity(t *testing.T) {
	lb := freshLBWithWeights(t, 500, 500, 1000, 0, 0)
	s := goodSnap()
	s.BWLimit, s.UpSpeed, s.AddBandwidth, s.BWAvailable = 1000000, 900000, 50000, 50000
	if score, reason := lb.rateNodeWithReason(s, "", 0, 0, nil, false); score != 0 || reason != rejectBandwidthExhaust {
		t.Fatalf("95%% committed admitted: score=%d reason=%q", score, reason)
	}
	s.UpSpeed, s.AddBandwidth, s.EstBandwidthPerUser = 800000, 0, 60000
	if score, reason := lb.rateNodeWithReason(s, "", 0, 0, nil, false); score != 0 || reason != rejectBandwidthExhaust {
		t.Fatalf("projected 86%% admitted: score=%d reason=%q", score, reason)
	}
}

func TestPlacementHeadroomAppliesCapacityHeadroom(t *testing.T) {
	n := state.EnhancedBalancerNodeSnapshot{BWLimit: 1000000, UpSpeed: 950000, DownSpeed: 950000}
	for _, verb := range []placement.Verb{placement.Serve, placement.Ingest} {
		if got := placementHeadroom(n, verb); got != 0 {
			t.Errorf("%s headroom=%d, want zero", verb, got)
		}
	}
	req := placementObservationFixture()
	req.Snapshot.Nodes[0].UpSpeed, req.Snapshot.Nodes[0].AddBandwidth = 950, 0
	got := observeOne(t, req)
	if got.Capacity != placement.CapacityExhausted || got.BWAvailable != 50 {
		t.Fatalf("want exhausted with raw wire headroom 50, got %+v", got)
	}
}

func TestPlacementDirectionalBudgetsAndReservations(t *testing.T) {
	req := placementObservationFixture()
	node := &req.Snapshot.Nodes[0]
	node.Capacity = state.DefaultNodeCapacity()
	node.Capacity.EgressBudgetBytesPerSec, node.Capacity.IngressBudgetBytesPerSec = 1000, 2000
	node.IngressReservedBytesPerSec = 800
	if got := placementHeadroom(*node, placement.Ingest); got != 700 {
		t.Fatalf("ingress usable headroom=%d, want 700", got)
	}
	req.Verb, req.Protocol = placement.Ingest, "rtmp"
	got := observeOne(t, req)
	if got.BWLimit != 2000 || got.BWAvailable != 1000 {
		t.Fatalf("ingress wire capacity=%+v", got)
	}
	if got := placementHeadroom(*node, placement.Serve); got != 350 {
		t.Fatalf("egress usable headroom=%d, want 350", got)
	}
}
