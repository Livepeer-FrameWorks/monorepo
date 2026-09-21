package state

import (
	"sync/atomic"
	"time"
)

// Announced-restart reconnect window. Helmsman sends a "node_restarting"
// lifecycle event before a planned exit (systemd restart, self-update); the
// data plane (MistServer, Caddy) keeps serving meanwhile, so the node's
// health is held steady until the deadline instead of publishing unhealthy
// to DNS on the stream disconnect. A crash never announces, so unannounced
// disconnects keep the immediate-unhealthy path. The window is in-memory
// only: the conn-owner instance that received the announce owns it.

// EventNodeRestarting is the NodeLifecycleUpdate event_type Helmsman sends
// before a planned exit. Any other lifecycle event — including
// "node_shutdown" with is_healthy=false — takes the immediate unhealthy
// path.
const EventNodeRestarting = "node_restarting"

const defaultRestartReconnectWindow = 20 * time.Second

// restartReconnectWindow holds the configured window in nanoseconds; zero
// means SetRestartReconnectWindowSeconds has not been called.
var restartReconnectWindow atomic.Int64

// SetRestartReconnectWindowSeconds sets the window RestartReconnectWindow
// reports, clamped to 5-30s: long enough for a systemd restart +
// control-stream reconnect, short enough that a poweroff (which also
// announces — SIGTERM can't tell the difference) stays well inside the DNS
// reconciler's 60s tick.
func SetRestartReconnectWindowSeconds(seconds int) {
	seconds = min(max(seconds, 5), 30)
	restartReconnectWindow.Store(int64(time.Duration(seconds) * time.Second))
}

// RestartReconnectWindow is how long an announced restart holds node health
// before the disconnect is finalized as unhealthy: the value set with
// SetRestartReconnectWindowSeconds, or 20s when none was set.
func RestartReconnectWindow() time.Duration {
	if window := restartReconnectWindow.Load(); window > 0 {
		return time.Duration(window)
	}
	return defaultRestartReconnectWindow
}

// SetNodePendingReconnect arms the reconnect window for an announced restart.
func (sm *StreamStateManager) SetNodePendingReconnect(nodeID string, deadline time.Time) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	n := sm.nodes[nodeID]
	if n == nil {
		n = newNodeState(nodeID)
		sm.nodes[nodeID] = n
	}
	n.PendingReconnectUntil = deadline
	n.PendingReconnectSetAt = time.Now()
}

// NodePendingReconnect reports the armed reconnect deadline, if any.
func (sm *StreamStateManager) NodePendingReconnect(nodeID string) (time.Time, bool) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()
	n := sm.nodes[nodeID]
	if n == nil || n.PendingReconnectUntil.IsZero() {
		return time.Time{}, false
	}
	return n.PendingReconnectUntil, true
}

// ClearNodePendingReconnect disarms the reconnect window. Called only from
// the control Register path (the node provably reconnected) and from window
// finalization — never from heartbeats, which the pre-restart process can
// still emit after announcing.
func (sm *StreamStateManager) ClearNodePendingReconnect(nodeID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if n := sm.nodes[nodeID]; n != nil {
		n.PendingReconnectUntil = time.Time{}
		n.PendingReconnectSetAt = time.Time{}
	}
}
