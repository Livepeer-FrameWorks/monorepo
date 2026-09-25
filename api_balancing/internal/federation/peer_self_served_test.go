package federation

import "testing"

// A virtual cluster this cell controls resolves to this Foghorn's own address.
// The PeerChannel server refuses a channel to its own cluster, so a runner for
// it reconnects forever; the leader must not reserve one, while keeping the
// peer entry for address lookups.
func TestLeaderDoesNotDialVirtualClusterItsOwnCellControls(t *testing.T) {
	pm := newTestPeerManager(t, "cell-a", nil, true)

	selfServed := &peerState{addr: "foghorn-a:18019", controlCellID: "cell-a"}
	pm.peers["tenant-virtual"] = selfServed
	if _, reserved := pm.reservePeerRunnerLocked("tenant-virtual", selfServed); reserved {
		t.Fatal("a cluster controlled by this cell must not get a PeerChannel runner")
	}

	remote := &peerState{addr: "foghorn-b:18019", controlCellID: "cell-b"}
	if _, reserved := pm.reservePeerRunnerLocked("cell-b", remote); !reserved {
		t.Fatal("a cluster controlled by another cell must still get a runner")
	}
	remoteVirtual := &peerState{addr: "foghorn-b:18019", controlCellID: "cell-b"}
	if _, reserved := pm.reservePeerRunnerLocked("b-virtual", remoteVirtual); !reserved {
		t.Fatal("another cell's virtual cluster must still get a runner")
	}
	uncontrolled := &peerState{addr: "foghorn-c:18019"}
	if _, reserved := pm.reservePeerRunnerLocked("cell-c", uncontrolled); !reserved {
		t.Fatal("a peer without a control cell must still get a runner")
	}
}
