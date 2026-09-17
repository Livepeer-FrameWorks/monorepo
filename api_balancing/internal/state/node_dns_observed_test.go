package state

import (
	"testing"
	"time"
)

func TestNodeDNSSnapshotObservationTimeIsNotADelta(t *testing.T) {
	first := NodeDNSSnapshot{NodeID: "edge-1", IsHealthy: true, ClusterID: "private-eu", ObservedAt: time.Unix(10, 0)}
	heartbeat := first
	heartbeat.ObservedAt = time.Unix(20, 0)
	if !first.equals(heartbeat) {
		t.Fatal("a newer observation alone must not emit a DNS delta")
	}
	moved := heartbeat
	moved.IsHealthy = false
	if first.equals(moved) {
		t.Fatal("a health change must still emit a DNS delta")
	}
}
