package federation

import (
	"context"
	"time"

	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

// peerWatchRetryInterval paces reconnection after a broken subscription. The
// periodic reconciliation still runs, so a retry gap costs freshness, never
// correctness.
const peerWatchRetryInterval = 15 * time.Second

// clusterPeerWatcher is the optional streaming half of peer discovery. A
// discovery source without it keeps working on periodic reconciliation alone.
type clusterPeerWatcher interface {
	WatchPeers(ctx context.Context, clusterID string) (quartermasterpb.ClusterService_WatchPeersClient, error)
}

// watchPeers keeps this cell's peer set current from the moment it changes
// instead of at the next reconciliation. Only the leader subscribes, so one
// cell holds one stream regardless of how many replicas it runs, and the media
// cell is the client: the control plane never dials a cell and needs no address
// for one. The periodic refresh continues underneath as the backstop, so a
// dropped stream degrades to the slower path rather than to a stale peer set.
func (pm *PeerManager) watchPeers(ctx context.Context) {
	watcher, ok := pm.peerDiscovery.(clusterPeerWatcher)
	if !ok || watcher == nil {
		return
	}
	for {
		if ctx.Err() != nil {
			return
		}
		stream, err := watcher.WatchPeers(ctx, pm.clusterID)
		if err != nil {
			pm.logger.WithError(err).Warn("Federation peer subscription unavailable; reconciliation continues")
		} else {
			for {
				resp, recvErr := stream.Recv()
				if recvErr != nil {
					if ctx.Err() == nil {
						pm.logger.WithError(recvErr).Warn("Federation peer subscription ended; reconciliation continues")
					}
					break
				}
				// Leadership exit cancels this context and then waits here before
				// revoking the cell's contribution, so a cancelled watch must not
				// begin another publish: applying one runs Redis work on its own
				// timeouts that the cancellation cannot preempt, delaying the
				// revoke behind it. The leadership check stays as the contract
				// for a replica that loses the lease by any other route.
				if ctx.Err() != nil || !pm.IsLeader() {
					return
				}
				pm.applyQuartermasterPeers(resp)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-pm.done:
			return
		case <-time.After(peerWatchRetryInterval):
		}
	}
}
