package triggers

import (
	"context"
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	logrustest "github.com/sirupsen/logrus/hooks/test"
)

type lifecyclePullFixture struct {
	processor *Processor
	registry  *control.StreamRegistry
	state     *state.StreamStateManager
}

func newLifecyclePullFixture(t *testing.T) lifecyclePullFixture {
	t.Helper()
	sm := state.ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	sm.SetNodeConnectionInfo(context.Background(), "edge", "", "owner", "dest-cluster", nil)
	registry := control.NewStreamRegistry(nil, "cell", time.Minute)
	previous := control.StreamRegistryInstance
	control.SetStreamRegistry(registry)
	t.Cleanup(func() { control.SetStreamRegistry(previous) })
	return lifecyclePullFixture{processor: newTestProcessor(t), registry: registry, state: sm}
}

func (f lifecyclePullFixture) arrange(t *testing.T) control.InboundPull {
	t.Helper()
	return f.arrangeAt(t, "edge", time.Time{})
}

// arrangeAt records the destination's pull as created at createdAt (now when
// zero) and admits it for STREAM_SOURCE.
func (f lifecyclePullFixture) arrangeAt(t *testing.T, nodeID string, createdAt time.Time) control.InboundPull {
	t.Helper()
	pull, err := f.registry.RecordInboundPull(context.Background(), "stream", control.InboundPull{
		TenantID: "tenant", SourceClusterID: "source-cell", SourceNodeID: "publisher",
		DestClusterID: "dest-cluster", DestNodeID: nodeID, DTSCURL: "dtsc://source/live+stream",
		CreatedAt: createdAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	if nodeID == "edge" {
		admitPreparedSourceForTest(t, f.processor, "live+stream", pull)
	}
	return pull
}

func (f lifecyclePullFixture) snapshot(t *testing.T, streams map[string]*ipcpb.StreamData) {
	t.Helper()
	f.snapshotFrom(t, "edge", streams)
}

func (f lifecyclePullFixture) snapshotFrom(t *testing.T, nodeID string, streams map[string]*ipcpb.StreamData) {
	t.Helper()
	if _, _, err := f.processor.handleNodeLifecycleUpdate(nodeLifecycleTrigger(&ipcpb.NodeLifecycleUpdate{
		NodeId: nodeID, IsHealthy: true, EventType: "node_lifecycle_update",
		Timestamp: time.Now().Unix(), Streams: streams,
	})); err != nil {
		t.Fatal(err)
	}
}

// abandonedPullAge is well past the start grace a destination has to begin a
// source pull after it was arranged.
const abandonedPullAge = 10 * time.Minute

func (f lifecyclePullFixture) streamSource(t *testing.T) string {
	t.Helper()
	response, abort, err := f.processor.handleStreamSource(&ipcpb.MistTrigger{NodeId: "edge",
		TriggerPayload: &ipcpb.MistTrigger_StreamSource{StreamSource: &ipcpb.StreamSourceTrigger{StreamName: "live+stream"}}})
	if err != nil || abort {
		t.Fatalf("STREAM_SOURCE failed: %q, %v, %v", response, abort, err)
	}
	return response
}

func replicatedStreamSnapshot() map[string]*ipcpb.StreamData {
	return map[string]*ipcpb.StreamData{"live+stream": {Total: 1, Inputs: 1, Replicated: true}}
}

// A viewer is redirected to a node right after its pull is arranged. The
// node's next lifecycle snapshot was sampled before Mist asked for the source,
// so it does not list the stream yet; that absence must not tombstone the pull
// the viewer's STREAM_SOURCE is about to read.
func TestLifecycleSnapshotKeepsPullArrangedForRedirectedViewer(t *testing.T) {
	f := newLifecyclePullFixture(t)
	pull := f.arrange(t)
	// PLAY_REWRITE viewer bookkeeping creates the node's stream instance.
	f.state.UpdateUserConnection("stream", "edge", "tenant", 1)

	f.snapshot(t, nil)

	if got := f.streamSource(t); got != pull.DTSCURL {
		t.Fatalf("STREAM_SOURCE after a pre-start snapshot = %q, want the arranged pull %q", got, pull.DTSCURL)
	}
}

// Presence the node reported before the pull existed belongs to an earlier
// copy; its disappearance says nothing about the new attempt.
func TestLifecycleSnapshotKeepsPullArrangedAfterLastObservedPresence(t *testing.T) {
	f := newLifecyclePullFixture(t)
	f.snapshot(t, replicatedStreamSnapshot())
	time.Sleep(time.Millisecond)
	pull := f.arrange(t)

	f.snapshot(t, nil)

	if got := f.streamSource(t); got != pull.DTSCURL {
		t.Fatalf("STREAM_SOURCE after absence of an older copy = %q, want the arranged pull %q", got, pull.DTSCURL)
	}
}

// A node that carried the pulled stream and then stopped listing it has a
// stale replica: the pull is cleared and STREAM_SOURCE refuses it.
func TestLifecycleSnapshotClearsPullTheNodeStoppedCarrying(t *testing.T) {
	f := newLifecyclePullFixture(t)
	f.arrange(t)
	time.Sleep(time.Millisecond)
	f.snapshot(t, replicatedStreamSnapshot())

	f.snapshot(t, nil)

	if _, active := f.registry.InboundPullForNode("stream", "edge"); active {
		t.Fatal("pull the node stopped carrying is still active")
	}
	if got := f.streamSource(t); got != control.OfflineNotPlaced {
		t.Fatalf("STREAM_SOURCE after the node dropped its replica = %q, want %q", got, control.OfflineNotPlaced)
	}
}

// Node-local trigger bookkeeping can mark the instance offline and
// non-replicated before the snapshot that no longer lists the stream: Mist's
// lifecycle report does not flag a pulled stream as replicated, and the
// node-local STREAM_END zeroes its counters. The listing the node already
// reported still decides that the pull went away, and it is cleared as a
// stopped replica on that snapshot, not later as one never started.
func TestLifecycleSnapshotClearsStoppedPullAfterNodeLocalEnd(t *testing.T) {
	f := newLifecyclePullFixture(t)
	logs := logrustest.NewLocal(f.processor.logger)
	f.arrange(t)
	time.Sleep(time.Millisecond)
	f.snapshot(t, map[string]*ipcpb.StreamData{"live+stream": {Total: 1, Inputs: 1}})
	f.state.UpdateNodeStats("stream", "edge", 1, 1, 0, 0, false)
	f.state.SetOffline("stream", "edge")

	f.snapshot(t, nil)

	if _, active := f.registry.InboundPullForNode("stream", "edge"); active {
		t.Fatal("pull the node listed and then stopped listing is still active")
	}
	var stopped, neverStarted bool
	for _, entry := range logs.AllEntries() {
		switch entry.Message {
		case "Node lifecycle cleared stale replicated stream":
			stopped = true
		case "Node lifecycle cleared origin pull the destination never started":
			neverStarted = true
		}
	}
	if !stopped || neverStarted {
		t.Fatalf("clear logged stopped=%v neverStarted=%v, want only the stopped-replica clear", stopped, neverStarted)
	}
}

// A destination that never lists the stream long after its pull was arranged
// never started it; the pull is abandoned and a later snapshot clears it.
func TestLifecycleSnapshotClearsPullTheDestinationNeverStarted(t *testing.T) {
	f := newLifecyclePullFixture(t)
	f.arrangeAt(t, "edge", time.Now().Add(-abandonedPullAge))

	f.snapshot(t, nil)

	if _, active := f.registry.InboundPullForNode("stream", "edge"); active {
		t.Fatal("pull the destination never started outlived its start grace")
	}
	if got := f.streamSource(t); got != control.OfflineNotPlaced {
		t.Fatalf("STREAM_SOURCE after the abandoned pull was cleared = %q, want %q", got, control.OfflineNotPlaced)
	}
}

// An old pull the destination still lists is running, whatever its age.
func TestLifecycleSnapshotKeepsListedPullPastStartGrace(t *testing.T) {
	f := newLifecyclePullFixture(t)
	pull := f.arrangeAt(t, "edge", time.Now().Add(-abandonedPullAge))

	f.snapshot(t, replicatedStreamSnapshot())
	f.snapshot(t, replicatedStreamSnapshot())

	if got := f.streamSource(t); got != pull.DTSCURL {
		t.Fatalf("STREAM_SOURCE for a listed pull = %q, want %q", got, pull.DTSCURL)
	}
}

// Each destination's snapshot is judged against its own pull: a stream Mist
// does not flag as replicated is still a replica on every node pulling it.
func TestLifecycleSnapshotMarksReplicatedPerDestinationNode(t *testing.T) {
	f := newLifecyclePullFixture(t)
	f.state.SetNodeConnectionInfo(context.Background(), "edge-a", "", "owner", "dest-cluster", nil)
	f.state.SetNodeConnectionInfo(context.Background(), "edge-b", "", "owner", "dest-cluster", nil)
	f.arrangeAt(t, "edge-a", time.Time{})
	f.arrangeAt(t, "edge-b", time.Time{})

	for _, node := range []string{"edge-a", "edge-b"} {
		f.snapshotFrom(t, node, map[string]*ipcpb.StreamData{"live+stream": {Total: 1, Inputs: 1}})
	}

	instances := f.state.GetStreamInstances("stream")
	for _, node := range []string{"edge-a", "edge-b"} {
		if !instances[node].Replicated {
			t.Fatalf("%s instance = %+v, want replicated from its own pull", node, instances[node])
		}
	}
}
