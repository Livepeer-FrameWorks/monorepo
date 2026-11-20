package state

import (
	"context"
	"time"
)

const virtualViewerClientSweep = 60 * time.Second

// RenewVirtualViewerSession renews only an already admitted, attached viewer.
func (sm *StreamStateManager) RenewVirtualViewerSession(nodeID, sessionID string) bool {
	if sessionID == "" {
		return false
	}
	sm.mu.Lock()
	defer sm.mu.Unlock()
	renewed := false
	for _, id := range sm.viewersByNode[nodeID] {
		if viewer := sm.virtualViewers[id]; viewer != nil && viewer.State == VirtualViewerActive && viewer.hasMistSessionID(sessionID) {
			viewer.LastClientSeen = time.Now()
			renewed = true
		}
	}
	return renewed
}

func (sm *StreamStateManager) expireVirtualViewerLeases(nodeID string) {
	type reservation struct{ id, node string }
	var expired []reservation
	sm.mu.Lock()
	now := time.Now()
	expire := func(node string) {
		for _, id := range sm.viewersByNode[node] {
			viewer := sm.virtualViewers[id]
			if viewer == nil || viewer.State != VirtualViewerActive {
				continue
			}
			last := viewer.ConnectTime
			if viewer.LastClientSeen.After(last) {
				last = viewer.LastClientSeen
			}
			if last.IsZero() || now.Sub(last) <= 2*virtualViewerClientSweep {
				continue
			}
			viewer.State = VirtualViewerDisconnected
			viewer.DisconnectTime = now
			expired = append(expired, reservation{id: viewer.ID, node: viewer.NodeID})
		}
	}
	if nodeID != "" {
		expire(nodeID)
	} else {
		for node := range sm.viewersByNode {
			expire(node)
		}
	}
	sm.mu.Unlock()
	if len(expired) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for _, entry := range expired {
		reportBandwidthError("expire viewer lease", entry.node, sm.ReleaseNodeBandwidth(ctx, entry.id, entry.node))
	}
}
