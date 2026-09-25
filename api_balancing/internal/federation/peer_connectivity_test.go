package federation

import (
	"context"
	"testing"
)

// Only the cell leader holds PeerChannels. Every other replica must still
// route cross-cell playback, so it answers IsPeerConnected from the leader's
// published snapshot instead of its own (always empty) channel set.
func TestNonLeaderSeesLeaderPeerConnectivity(t *testing.T) {
	cache, _ := setupTestCache(t)
	const addr = "foghorn.us.example.com:18029"

	leader := newTestPeerManager(t, "staging-media-eu", cache, true)
	leader.instanceID = "foghorn-eu-2"
	leader.peers["staging-media-us"] = &peerState{addr: addr, connected: true}

	follower := newTestPeerManager(t, "staging-media-eu", cache, false)
	follower.instanceID = "foghorn-eu-1"
	follower.peers["staging-media-us"] = &peerState{addr: addr}

	if follower.IsPeerConnected("staging-media-us") {
		t.Fatal("follower claimed connectivity before any leader snapshot")
	}

	leader.publishPeerConnectivity()
	if err := follower.loadPeerConnectivity(); err != nil {
		t.Fatalf("loadPeerConnectivity: %v", err)
	}
	if !follower.IsPeerConnected("staging-media-us") {
		t.Fatal("follower does not see the leader's live PeerChannel")
	}

	// A different endpoint than the one this replica would dial is not proof
	// that its own address works.
	follower.peers["staging-media-us"].addr = "foghorn.other.example.com:18029"
	if follower.IsPeerConnected("staging-media-us") {
		t.Fatal("follower trusted a snapshot for a different peer address")
	}
	follower.peers["staging-media-us"].addr = addr

	// The leader losing the channel propagates on the next publish.
	leader.peers["staging-media-us"].connected = false
	leader.publishPeerConnectivity()
	if err := follower.loadPeerConnectivity(); err != nil {
		t.Fatalf("loadPeerConnectivity: %v", err)
	}
	if follower.IsPeerConnected("staging-media-us") {
		t.Fatal("follower still reports a peer the leader disconnected")
	}
}

// A replica that is not the leader must never publish: its channel set is
// empty and would erase the leader's view.
func TestOnlyLeaderPublishesPeerConnectivity(t *testing.T) {
	cache, _ := setupTestCache(t)
	follower := newTestPeerManager(t, "staging-media-eu", cache, false)
	follower.peers["staging-media-us"] = &peerState{addr: "a:1", connected: true}

	follower.publishPeerConnectivity()
	if _, ok, err := cache.GetPeerConnectivity(context.Background()); err != nil || ok {
		t.Fatalf("non-leader published a snapshot (ok=%v err=%v)", ok, err)
	}
}
