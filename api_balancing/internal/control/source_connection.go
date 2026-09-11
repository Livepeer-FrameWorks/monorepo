package control

import (
	"context"
	"errors"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// LocalSourceConnectionFence reads authenticated registration identity, not
// telemetry or a node-supplied trigger field. The dispatcher must also fence
// its captured stream before and after source resolution.
func LocalSourceConnectionFence(nodeID, clusterID string) (int64, bool) {
	if registry == nil || nodeID == "" || clusterID == "" {
		return 0, false
	}
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	c := registry.conns[nodeID]
	if c == nil || c.stream == nil || c.superseded.Load() || c.fence <= 0 || c.clusterID != clusterID || c.session().NodeID() != nodeID {
		return 0, false
	}
	return c.fence, true
}

// DestinationConnectionFence uses shared ownership when configured: the selected
// destination can be connected to another Foghorn replica. A failed shared read
// must not fall back to a potentially superseded local registration.
// The caller independently validates the node's signed cluster membership.
func DestinationConnectionFence(ctx context.Context, nodeID, clusterID string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if nodeID == "" || clusterID == "" {
		return 0, errors.New("destination connection identity is required")
	}
	if store := GetRedisStore(); store != nil {
		owner, err := store.GetConnOwner(ctx, nodeID)
		if err != nil {
			return 0, err
		}
		if owner.InstanceID != "" && owner.Fence > 0 {
			return owner.Fence, nil
		}
	} else if fence, ok := LocalSourceConnectionFence(nodeID, clusterID); ok {
		return fence, nil
	}
	return 0, errors.New("destination connection ownership is unavailable")
}

// Admission cannot use the legacy untracked-connection allowance. Both
// checks use the captured registration, including its raw key and incarnation.
func sourceConnectionSessionCurrent(session NodeSession, stream ipcpb.HelmsmanControl_ConnectServer) bool {
	if registry == nil || stream == nil || session.NodeID() == "" || session.ClusterID == "" {
		return false
	}
	key := session.RawNodeID
	if key == "" {
		key = session.NodeID()
	}
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	c := registry.conns[key]
	return c != nil && c.stream == stream && !c.superseded.Load() && c.fence == session.Fence &&
		c.clusterID == session.ClusterID && c.session().NodeID() == session.NodeID()
}
