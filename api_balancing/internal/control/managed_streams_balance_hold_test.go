package control

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	localauthority "frameworks/api_balancing/internal/mediaauthority"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

const (
	holdCluster = "source"
	holdTenant  = "tenant-spent"
	holdStream  = "stream-spent"
	holdName    = "live-spent"
	holdSource  = "ts-exec:cat /dev/null"
)

type recordedPlacement struct {
	streamID string
	active   bool
}

type recordingPlacementWriter struct{ enqueued []recordedPlacement }

func (w *recordingPlacementWriter) Enqueue(_ context.Context, streamID, _, _ string, active bool) error {
	w.enqueued = append(w.enqueued, recordedPlacement{streamID: streamID, active: active})
	return nil
}

type holdFixture struct {
	stream *mockStream
	writer *recordingPlacementWriter
}

func newHoldFixture(t *testing.T, nodes ...string) *holdFixture {
	t.Helper()
	holdControlConnection(t, nodes...)
	fixture := &holdFixture{stream: &mockStream{}, writer: &recordingPlacementWriter{}}
	for _, nodeID := range nodes {
		registry.conns[nodeID] = &conn{stream: fixture.stream, canonicalID: nodeID, clusterID: holdCluster}
	}
	priorRegistry, priorWriter, priorEffects := StreamRegistryInstance, managedStreamPlacementWriter, managedStreamMaterializer
	SetStreamRegistry(NewStreamRegistry(nil, "cell", time.Minute))
	SetManagedStreamPlacementWriter(fixture.writer)
	SetManagedStreamMaterializer(deniedManagedEffects{t: t})
	t.Cleanup(func() {
		SetStreamRegistry(priorRegistry)
		SetManagedStreamPlacementWriter(priorWriter)
		SetManagedStreamMaterializer(priorEffects)
	})
	return fixture
}

func (f *holdFixture) applyToMist(nodeID, streamID, name string, retired bool) {
	snapshot := managedStreamSnapshot{sourceSpec: holdSource, alwaysOn: true, ingestMode: "mist_native", internalName: name, tenantID: holdTenant, retired: retired}
	StreamRegistryInstance.ManagedSetLastSent(holdCluster, nodeID, streamID, snapshot)
	StreamRegistryInstance.ManagedSetVerifiedFromHeartbeat(nodeID, []*ipcpb.AppliedManagedStream{
		{StreamId: streamID, Name: name, Source: holdSource, AlwaysOn: true, IngestMode: "mist_native", TenantId: holdTenant},
	})
}

func (f *holdFixture) applied() []string {
	var names []string
	for _, message := range f.stream.sent {
		if apply := message.GetApplyManagedStream(); apply != nil {
			names = append(names, apply.GetName())
		}
	}
	return names
}

func (f *holdFixture) retracted() []string {
	var names []string
	for _, message := range f.stream.sent {
		if retract := message.GetRetractManagedStream(); retract != nil {
			names = append(names, retract.GetName())
		}
	}
	return names
}

func holdRow(streamID, name string, clusters ...string) *commodorepb.ManagedStreamRow {
	if len(clusters) == 0 {
		clusters = []string{holdCluster}
	}
	return &commodorepb.ManagedStreamRow{
		StreamId: streamID, PlaybackId: name, InternalName: name, TenantId: holdTenant, IngestMode: "mist_native",
		SourceSpec: holdSource, AlwaysOn: true, PlacementCount: 1, AllowedClusterIds: clusters,
	}
}

func commodoreRejects(t *testing.T, reason commodorepb.StreamKeyRejectionReason) {
	t.Helper()
	startFakeCommodoreServer(t, &fakeCommodoreInternal{
		streamContext: func(_ context.Context, req *commodorepb.ResolveStreamContextRequest) (*commodorepb.ResolveStreamContextResponse, error) {
			return &commodorepb.ResolveStreamContextResponse{
				Admitted: false, RejectionReason: reason, StreamId: req.GetStreamId(), InternalName: holdName, TenantId: holdTenant, IngestMode: "mist_native",
			}, nil
		},
	})
}

// electedNode is the node placement picks for streamID from nodes.
func electedNode(t *testing.T, streamID string, nodes ...string) string {
	t.Helper()
	eligible := make([]eligibleNode, 0, len(nodes))
	for _, nodeID := range nodes {
		eligible = append(eligible, eligibleNode{nodeID: nodeID, clusterID: holdCluster})
	}
	return placementPickWithCluster(streamID, eligible, 1)[0].nodeID
}

// A spent balance refuses new streams while everything already running
// continues, wherever the stream runs: a spent tenant's signed placement denies
// every node, so its row reaches the retract pass unelected, and a topology
// change can elect a node other than the one running it.
func TestManagedReconcile_SpentBalanceKeepsAppliedStream(t *testing.T) {
	const balanceNegative = commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_BALANCE_NEGATIVE
	nodes := []string{"edge-a", "edge-b"}
	elected := electedNode(t, holdStream, nodes...)
	other := nodes[0]
	if other == elected {
		other = nodes[1]
	}
	for name, tc := range map[string]struct {
		running  string
		clusters []string
	}{
		"on its elected node":  {running: elected},
		"off its elected node": {running: other},
		"with no node elected": {running: elected, clusters: []string{"cluster-with-no-nodes"}},
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newHoldFixture(t, nodes...)
			commodoreRejects(t, balanceNegative)
			fixture.applyToMist(tc.running, holdStream, holdName, false)

			reconcileClusterManagedStreams(t.Context(), logging.NewLogger(), holdCluster, []*commodorepb.ManagedStreamRow{holdRow(holdStream, holdName, tc.clusters...)})

			if got := fixture.retracted(); len(got) != 0 {
				t.Fatalf("spent balance retracted a running stream: %v", got)
			}
			if got := fixture.applied(); len(got) != 0 {
				t.Fatalf("spent balance applied a stream: %v", got)
			}
			if _, ok := StreamRegistryInstance.ManagedGetLastSent(holdCluster, tc.running, holdStream); !ok {
				t.Fatal("held stream lost its applied record")
			}
			if !slices.Contains(fixture.writer.enqueued, recordedPlacement{streamID: holdStream, active: true}) {
				t.Fatalf("held stream stopped renewing its active placement: %+v", fixture.writer.enqueued)
			}
		})
	}
}

func TestManagedReconcile_SpentBalanceStartsNothing(t *testing.T) {
	fixture := newHoldFixture(t, "edge-a")
	commodoreRejects(t, commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_BALANCE_NEGATIVE)

	reconcileClusterManagedStreams(t.Context(), logging.NewLogger(), holdCluster, []*commodorepb.ManagedStreamRow{holdRow(holdStream, holdName)})

	if got := fixture.applied(); len(got) != 0 {
		t.Fatalf("spent tenant started a managed stream: %v", got)
	}
	if got := fixture.retracted(); len(got) != 0 {
		t.Fatalf("nothing was running to retract: %v", got)
	}
}

func TestManagedReconcile_SuspensionStillRetracts(t *testing.T) {
	for name, clusters := range map[string][]string{"elected": nil, "unelected": {"cluster-with-no-nodes"}} {
		t.Run(name, func(t *testing.T) {
			fixture := newHoldFixture(t, "edge-a")
			commodoreRejects(t, commodorepb.StreamKeyRejectionReason_STREAM_KEY_REJECTION_TENANT_SUSPENDED)
			fixture.applyToMist("edge-a", holdStream, holdName, false)

			reconcileClusterManagedStreams(t.Context(), logging.NewLogger(), holdCluster, []*commodorepb.ManagedStreamRow{holdRow(holdStream, holdName, clusters...)})

			if got := fixture.retracted(); !slices.Equal(got, []string{holdName}) {
				t.Fatalf("suspended tenant's stream retractions = %v", got)
			}
		})
	}
}

// Without an answer from Commodore the stream's balance is unknown, and only a
// positive denial stops a source that is running.
func TestManagedReconcile_UnelectedStreamSurvivesUnknownAdmission(t *testing.T) {
	fixture := newHoldFixture(t, "edge-a")
	startFakeCommodoreServer(t, &fakeCommodoreInternal{
		streamContext: func(context.Context, *commodorepb.ResolveStreamContextRequest) (*commodorepb.ResolveStreamContextResponse, error) {
			return nil, errors.New("commodore down")
		},
	})
	fixture.applyToMist("edge-a", holdStream, holdName, false)

	reconcileClusterManagedStreams(t.Context(), logging.NewLogger(), holdCluster, []*commodorepb.ManagedStreamRow{holdRow(holdStream, holdName, "cluster-with-no-nodes")})

	if got := fixture.retracted(); len(got) != 0 {
		t.Fatalf("unknown admission retracted a running stream: %v", got)
	}
}

type fixedManagedSet struct {
	set localauthority.ManagedStreamSet
}

func (r fixedManagedSet) ManagedStreams(context.Context, string) (localauthority.ManagedStreamSet, error) {
	return r.set, nil
}

// One spent tenant does not take a cell's offline reconciliation away from
// everyone else: with Commodore down the local set still starts a healthy
// tenant's stream and retracts what it omits, and the spent stream runs on.
func TestReconcileServedCluster_SpentTenantLeavesCellReconcilingOffline(t *testing.T) {
	const healthyStream, healthyName = "stream-healthy", "live-healthy"
	const removedStream, removedName = "stream-removed", "live-removed"
	fixture := newHoldFixture(t, "edge-a")
	SetManagedStreamMaterializer(nil)
	var resolved []string
	startFakeCommodoreServer(t, &fakeCommodoreInternal{
		streamContext: func(_ context.Context, req *commodorepb.ResolveStreamContextRequest) (*commodorepb.ResolveStreamContextResponse, error) {
			resolved = append(resolved, req.GetStreamId())
			return nil, errors.New("commodore down")
		},
	})
	fixture.applyToMist("edge-a", holdStream, holdName, false)
	StreamRegistryInstance.ManagedSetLastSent(holdCluster, "edge-a", removedStream, managedStreamSnapshot{sourceSpec: holdSource, ingestMode: "mist_native", internalName: removedName, tenantID: holdTenant})

	reconcileServedCluster(t.Context(), logging.NewLogger(), holdCluster, fixedManagedSet{set: localauthority.ManagedStreamSet{
		Marked: true, Complete: true,
		Rows: []*commodorepb.ManagedStreamRow{holdRow(healthyStream, healthyName)},
		Held: map[string]*commodorepb.ManagedStreamRow{holdStream: holdRow(holdStream, holdName)},
		Contexts: map[string]*commodorepb.ResolveStreamContextResponse{
			healthyStream: {Admitted: true, StreamId: healthyStream, InternalName: healthyName, TenantId: "tenant-healthy", IngestMode: "mist_native"},
		},
	}})

	if got := fixture.applied(); !slices.Equal(got, []string{healthyName}) {
		t.Fatalf("offline applies = %v, want the healthy tenant's stream only", got)
	}
	if got := fixture.retracted(); !slices.Equal(got, []string{removedName}) {
		t.Fatalf("offline retractions = %v, want the omitted stream only", got)
	}
	if len(resolved) != 0 {
		t.Fatalf("a complete local set asked Commodore about %v", resolved)
	}
}

// A retired placement no longer runs anything, so a hold has nothing to keep and
// its cleanup proceeds.
func TestManagedReconcile_HoldDoesNotBlockRetiredCleanup(t *testing.T) {
	fixture := newHoldFixture(t, "edge-a")
	fixture.applyToMist("edge-a", holdStream, holdName, true)

	reconcileClusterManagedStreamsWithContexts(t.Context(), logging.NewLogger(), holdCluster, nil,
		map[string]*commodorepb.ResolveStreamContextResponse{},
		map[string]*commodorepb.ManagedStreamRow{holdStream: holdRow(holdStream, holdName)})

	if got := fixture.retracted(); !slices.Equal(got, []string{holdName}) {
		t.Fatalf("retired placement retractions = %v", got)
	}
}
