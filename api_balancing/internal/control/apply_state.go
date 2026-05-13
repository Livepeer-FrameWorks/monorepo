package control

import (
	"context"
	"sync"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
)

// seedVersionCounter assigns monotonic seed_versions per node. Resets
// only on Foghorn process restart, which is fine: ACKs are tied to the
// live gRPC stream, so a restart invalidates all previous ACK state
// naturally.
type seedVersionCounter struct {
	mu  sync.Mutex
	cur map[string]uint64
}

func newSeedVersionCounter() *seedVersionCounter {
	return &seedVersionCounter{cur: make(map[string]uint64)}
}

func (c *seedVersionCounter) next(nodeID string) uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cur[nodeID]++
	return c.cur[nodeID]
}

var (
	seedVersions = newSeedVersionCounter()
)

// nextSeedVersion returns the next monotonic seed_version for nodeID.
// Foghorn calls this each time it composes a ConfigSeed for the node.
func nextSeedVersion(nodeID string) uint64 {
	return seedVersions.next(nodeID)
}

// reportApplyResultToNavigator forwards Helmsman's ConfigSeed ACK to
// Navigator. Navigator persists per-edge bundle readiness because DNS
// membership is derived from that state.
//
// Trust model: Helmsman is the authority on what bundles are actually
// active in Caddy. It demotes per-file successes to failures when the
// Caddy reload itself fails (see sendApplyResultLocked in
// api_sidecar/internal/config/manager.go), so we can take applied_
// bundle_ids at face value here. ack.Success reflects whether the apply
// was wholly clean. It is logged for diagnostics but does not change
// the per-bundle gating: a partial failure where caddyOK=true with two
// good bundles and one bad bundle correctly publishes the two good
// bundles for DNS.
func reportApplyResultToNavigator(ack *pb.ConfigSeedApplyResult, nodeID, clusterID string, log logging.Logger) {
	if ack == nil || navigatorClient == nil || nodeID == "" {
		return
	}
	appliedAt := time.Now().Unix()
	if ts := ack.GetAppliedAt(); ts != nil {
		appliedAt = ts.AsTime().Unix()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := navigatorClient.ReportConfigSeedApplyResult(ctx, &pb.ReportConfigSeedApplyResultRequest{
		NodeId:           nodeID,
		ClusterId:        clusterID,
		SeedVersion:      ack.GetSeedVersion(),
		AppliedBundleIds: ack.GetAppliedBundleIds(),
		FailedBundleIds:  ack.GetFailedBundleIds(),
		Success:          ack.GetSuccess(),
		Error:            ack.GetError(),
		AppliedAt:        appliedAt,
	}); err != nil {
		if log != nil {
			log.WithError(err).WithField("node_id", nodeID).Debug("Navigator apply ACK report failed")
		}
	}
}
