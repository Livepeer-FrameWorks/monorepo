package jobs

import (
	"time"

	"frameworks/api_balancing/internal/state"
)

const processingSourceReconnectGrace = time.Minute

// A control-stream drop does not remove the node's buffered media. Give an
// unannounced reconnect one minute; announced restarts retain their own bound.
func processingSourceNodePresent(node *state.NodeState, now time.Time) bool {
	if node == nil {
		return false
	}
	if node.PendingReconnectUntil.After(now) {
		return true
	}
	lastSeen := node.LastHeartbeat
	if lastSeen.IsZero() {
		lastSeen = node.DisconnectedAt
		if lastSeen.IsZero() {
			// Node state persisted before disconnect timestamps used LastUpdate.
			lastSeen = node.LastUpdate
		}
	}
	return !lastSeen.IsZero() && now.Before(lastSeen.Add(processingSourceReconnectGrace))
}
