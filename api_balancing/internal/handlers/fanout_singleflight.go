package handlers

import (
	"context"
	"time"

	"frameworks/api_balancing/internal/balancer"

	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
)

// fanOutMemoTTL bounds how long a fan-out result (including an empty one) is
// reused. Below the peers' 15s EdgeSummary push cadence, so the memo never
// outlives the cache it is standing in for; the cost is candidate sets up to
// 5s stale on a cold stream, which local re-scoring absorbs.
const fanOutMemoTTL = 5 * time.Second

// fanOutTimeout caps the detached shared fan-out. Per-peer QueryStream RPCs
// are bounded at 3s; this is the whole-round ceiling.
const fanOutTimeout = 5 * time.Second

// fanOutShared deduplicates concurrent cold fan-outs per (tenant, stream):
// during a dead-peer window every /play for the stream would otherwise pay
// its own multi-second QueryStream fan-out. Shared results use the FIRST
// caller's lat/lon for remote-side candidate pruning. That bias is acceptable:
// ScoreRemoteEdges re-ranks with each viewer's actual geo locally.
var fanOutShared = balancer.NewSharedFanOut(fanOutMemoTTL)

// queryStreamFanOutShared wraps queryStreamFanOut with the shared
// singleflight + memo. The fan-out runs on a context detached from the
// triggering viewer's cancellation (context.WithoutCancel + own timeout):
// the result is shared with concurrent waiters and memoized for everyone,
// so an abandoned first request must not poison the window with an empty
// candidate set.
func queryStreamFanOutShared(ctx context.Context, internalName, tenantID string, lat, lon float64, peers []*clusterpeerpb.TenantClusterPeer) []balancer.RemoteEdgeCandidate {
	return fanOutShared.Do(tenantID+"/"+internalName, func() []balancer.RemoteEdgeCandidate {
		fanCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), fanOutTimeout)
		defer cancel()
		return queryStreamFanOut(fanCtx, internalName, tenantID, lat, lon, peers)
	})
}
