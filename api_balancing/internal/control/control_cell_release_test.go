package control

import (
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

func TestApplyServedClustersRefreshReleasesMovedClusterConnections(t *testing.T) {
	oldRegistry := registry
	oldLocal := localClusterID
	oldServed := servedClusters.Load()
	oldReleased := releasedControlClusters.Load()
	t.Cleanup(func() {
		registry = oldRegistry
		localClusterID = oldLocal
		servedClusters.Store(oldServed)
		releasedControlClusters.Store(oldReleased)
	})

	localClusterID = "cell-eu"
	moved := &conn{clusterID: "private-moved", release: make(chan struct{})}
	kept := &conn{clusterID: "private-kept", release: make(chan struct{})}
	registry = &Registry{
		conns: map[string]*conn{
			"edge-moved":           moved,
			"edge-moved-canonical": moved,
			"edge-kept":            kept,
		},
		log: logging.NewLogger(),
	}

	applyServedClustersRefresh([]string{"private-kept"}, []string{"private-moved", "private-kept", "cell-eu", ""})

	select {
	case <-moved.release:
	default:
		t.Fatal("connection of a released cluster was not released")
	}
	select {
	case <-kept.release:
		t.Fatal("connection of a served cluster was released")
	default:
	}
	if !isReleasedControlCluster("private-moved") {
		t.Fatal("released cluster is not refused at registration")
	}
	if isReleasedControlCluster("private-kept") || isReleasedControlCluster("cell-eu") {
		t.Fatal("a served cluster is marked released")
	}

	applyServedClustersRefresh([]string{"private-kept"}, nil)
	if isReleasedControlCluster("private-moved") {
		t.Fatal("released set was not replaced by the refresh")
	}
}
