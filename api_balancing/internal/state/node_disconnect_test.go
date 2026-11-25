package state

import (
	"testing"
	"time"
)

func TestPeerSnapshotsPreserveLatestNodeConnection(t *testing.T) {
	now := time.Now()
	connected := func(at time.Time) NodeState {
		return NodeState{NodeID: "source", LastHeartbeat: at, LastUpdate: at, IsHealthy: true}
	}
	disconnected := func(at time.Time) NodeState {
		return NodeState{NodeID: "source", DisconnectedAt: at, LastUpdate: at, IsStale: true}
	}
	staleDisconnect := disconnected(now.Add(-10 * time.Second))
	staleDisconnect.LastUpdate = now.Add(time.Second)
	unknown := NodeState{NodeID: "source", LastUpdate: now.Add(time.Second)}
	for _, tc := range []struct {
		name            string
		local, incoming NodeState
		want            NodeState
	}{
		{"metadata does not renew an old disconnect", connected(now), staleDisconnect, connected(now)},
		{"old heartbeat cannot undo disconnect", disconnected(now), connected(now.Add(-time.Second)), disconnected(now)},
		{"old disconnect cannot shorten new grace", disconnected(now), disconnected(now.Add(-time.Minute)), disconnected(now)},
		{"new heartbeat clears disconnect", disconnected(now), connected(now.Add(time.Second)), connected(now.Add(time.Second))},
		{"new disconnect replaces heartbeat", connected(now), disconnected(now.Add(time.Second)), disconnected(now.Add(time.Second))},
		{"identity metadata cannot reject first heartbeat", unknown, connected(now), connected(now)},
		{"unstamped metadata cannot erase disconnect", disconnected(now), unknown, disconnected(now)},
		{"identity metadata cannot reject disconnect", unknown, disconnected(now), disconnected(now)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mergeIncomingNode(&tc.incoming, &tc.local)
			if !tc.incoming.LastHeartbeat.Equal(tc.want.LastHeartbeat) || !tc.incoming.DisconnectedAt.Equal(tc.want.DisconnectedAt) || tc.incoming.IsHealthy != tc.want.IsHealthy || tc.incoming.IsStale != tc.want.IsStale {
				t.Fatalf("peer changed connection observation: heartbeat=%s disconnected=%s healthy=%v stale=%v; want heartbeat=%s disconnected=%s healthy=%v stale=%v",
					tc.incoming.LastHeartbeat, tc.incoming.DisconnectedAt, tc.incoming.IsHealthy, tc.incoming.IsStale,
					tc.want.LastHeartbeat, tc.want.DisconnectedAt, tc.want.IsHealthy, tc.want.IsStale)
			}
		})
	}
}
