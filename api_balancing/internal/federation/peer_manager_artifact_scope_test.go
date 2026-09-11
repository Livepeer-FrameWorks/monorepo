package federation

import (
	"testing"
	"time"

	"frameworks/api_balancing/internal/state"
	foghornfederationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func seedArtifact(t *testing.T, sm *state.StreamStateManager, nodeID, streamName, tenantID, hash string, seq int64) {
	t.Helper()
	seedFederationNodeAndStream(t, sm, nodeID, streamName, tenantID)
	if err := sm.SetNodeArtifacts(nodeID, []*ipcpb.StoredArtifact{{
		ClipHash:     hash,
		StreamName:   streamName,
		FilePath:     "/tmp/" + hash + ".mp4",
		SizeBytes:    123,
		LastAccessed: time.Now().Unix(),
		ArtifactType: ipcpb.ArtifactEvent_ARTIFACT_TYPE_CLIP,
	}}, state.ArtifactReportOrder{Fence: 1, Seq: seq}); err != nil {
		t.Fatalf("SetNodeArtifacts: %v", err)
	}
}

func advertisedHashes(t *testing.T, out *capturePeerChannelStream) map[string]bool {
	t.Helper()
	hashes := map[string]bool{}
	for _, msg := range out.sent {
		payload, ok := msg.Payload.(*foghornfederationpb.PeerMessage_ArtifactAd)
		if !ok || payload.ArtifactAd == nil {
			continue
		}
		for _, loc := range payload.ArtifactAd.Artifacts {
			hashes[loc.GetArtifactHash()] = true
		}
	}
	return hashes
}

// An artifact advertisement names where a tenant's media is cached. A peer
// scoped to a set of tenants must see only those tenants' artifacts: the
// existing coverage uses a single always-on peer, so the per-peer tenant filter
// never runs and would keep passing if it were deleted.
func TestPushArtifactsScopesAdvertisementsByPeerTenant(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	seedArtifact(t, sm, "node-a", "stream-a", "tenant-a", "clip-a", 1)
	seedArtifact(t, sm, "node-b", "stream-b", "tenant-b", "clip-b", 2)

	pm := newTestPeerManager(t, "cluster-a", nil, false)
	pm.pool = newFoghornPoolAdapter(newNoopPool(t))

	scopedToA := &capturePeerChannelStream{}
	scopedToB := &capturePeerChannelStream{}
	unscoped := &capturePeerChannelStream{}
	pm.mu.Lock()
	pm.peers["peer-a"] = &peerState{connected: true, stream: scopedToA, lifecycle: peerStreamScoped, tenantIDs: []string{"tenant-a"}}
	pm.peers["peer-b"] = &peerState{connected: true, stream: scopedToB, lifecycle: peerStreamScoped, tenantIDs: []string{"tenant-b"}}
	pm.peers["peer-any"] = &peerState{connected: true, stream: unscoped, lifecycle: peerAlwaysOn}
	pm.mu.Unlock()

	flush := pm.wireTestWriters()
	pm.pushArtifacts()
	flush()

	gotA := advertisedHashes(t, scopedToA)
	if !gotA["clip-a"] {
		t.Error("a peer entitled to tenant-a did not receive tenant-a's artifact")
	}
	if gotA["clip-b"] {
		t.Error("a peer scoped to tenant-a was told where tenant-b's media is cached")
	}

	gotB := advertisedHashes(t, scopedToB)
	if !gotB["clip-b"] {
		t.Error("a peer entitled to tenant-b did not receive tenant-b's artifact")
	}
	if gotB["clip-a"] {
		t.Error("a peer scoped to tenant-b was told where tenant-a's media is cached")
	}

	// An unscoped peer carries no tenant filter and legitimately sees both;
	// without this the two assertions above would also hold if pushArtifacts
	// simply sent nothing.
	gotAny := advertisedHashes(t, unscoped)
	if !gotAny["clip-a"] || !gotAny["clip-b"] {
		t.Errorf("an unscoped peer did not receive both artifacts: %v", gotAny)
	}
}

// A peer whose entitled tenants do not overlap any advertisable artifact must
// receive no frame at all, rather than an empty advertisement.
func TestPushArtifactsSendsNothingToAnUnentitledPeer(t *testing.T) {
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	seedArtifact(t, sm, "node-a", "stream-a", "tenant-a", "clip-a", 1)

	pm := newTestPeerManager(t, "cluster-a", nil, false)
	pm.pool = newFoghornPoolAdapter(newNoopPool(t))

	stranger := &capturePeerChannelStream{}
	pm.mu.Lock()
	pm.peers["peer-z"] = &peerState{connected: true, stream: stranger, lifecycle: peerStreamScoped, tenantIDs: []string{"tenant-z"}}
	pm.mu.Unlock()

	flush := pm.wireTestWriters()
	pm.pushArtifacts()
	flush()

	if len(stranger.sent) != 0 {
		t.Fatalf("an unentitled peer received %d frames: %+v", len(stranger.sent), stranger.sent)
	}
}
