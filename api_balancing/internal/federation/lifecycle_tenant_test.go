package federation

import (
	"testing"

	"frameworks/api_balancing/internal/state"
)

func TestLifecycleBroadcastRequiresTenant(t *testing.T) {
	pm := newTestPeerManager(t, "cell-a", nil, true)
	peer := &capturePeerChannelStream{}
	pm.peers["cell-b"] = &peerState{lifecycle: peerAlwaysOn, connected: true, stream: peer}
	flush := pm.wireTestWriters()
	if err := pm.BroadcastStreamLifecycle(t.Context(), "stream", "", 1, true); err == nil {
		t.Fatal("unattributed lifecycle was accepted for broadcast")
	}
	flush()
	if len(peer.sent) != 0 {
		t.Fatal("unattributed lifecycle reached a peer")
	}
}

func TestLifecycleRefreshUsesDurableMembershipTenant(t *testing.T) {
	pm := newTestPeerManager(t, "cell-a", nil, true)
	peer := &capturePeerChannelStream{}
	pm.peers["cell-b"] = &peerState{lifecycle: peerAlwaysOn, connected: true, stream: peer}
	pm.streamMemberships["stream"] = StreamPeerMembership{StreamName: "stream", TenantID: "tenant-a", SourceGeneration: "generation", SourceRevision: 7, Active: true}
	sm := state.NewStreamStateManager()
	t.Cleanup(sm.Shutdown)
	if err := sm.UpdateStreamFromBuffer("live+stream", "stream", "node", "", "FULL", "{}"); err != nil {
		t.Fatal(err)
	}
	flush := pm.wireTestWriters()
	pm.mu.RLock()
	pm.refreshRemoteLiveStreams(sm, "")
	pm.mu.RUnlock()
	flush()
	if len(peer.sent) != 1 || peer.sent[0].GetStreamLifecycle().GetTenantId() != "tenant-a" {
		t.Fatalf("refresh lost membership ownership: %v", peer.sent)
	}
}

func TestLifecycleBroadcastRejectsTenantConflict(t *testing.T) {
	pm := newTestPeerManager(t, "cell-a", nil, true)
	pm.streamMemberships["stream"] = StreamPeerMembership{StreamName: "stream", TenantID: "tenant-a", SourceGeneration: "generation", SourceRevision: 7, Active: true}
	if err := pm.BroadcastStreamLifecycle(t.Context(), "stream", "tenant-b", 7, true); err == nil {
		t.Fatal("conflicting tenant was allowed to publish a lifecycle")
	}
}
