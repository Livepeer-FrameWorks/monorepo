package control

import (
	"context"
	"testing"
	"time"

	localauthority "frameworks/api_balancing/internal/mediaauthority"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
)

func withLocalPlacementEnforced(t *testing.T, enforced bool) {
	t.Helper()
	previous := localauthority.PlacementEnforced()
	localauthority.SetPlacementEnforced(enforced)
	t.Cleanup(func() { localauthority.SetPlacementEnforced(previous) })
}

func schema1SourcePair(now time.Time) localauthority.PlacementPair {
	pair := nodePolicySourcePair(now, allowNodes("node-1"))
	pair.Tenant.Authority.SchemaVersion, pair.Object.Authority.SchemaVersion = 1, 1
	pair.Tenant.Authority.MediaPlacement, pair.Object.Authority.MediaPlacement = nil, nil
	return pair
}

// A schema-1 pair carries no residency rules. On a replica that enforces
// placement it is a tenant whose activation has not reached the cell yet, so
// dialing its source waits; a replica that does not enforce keeps serving it.
func TestSourceDial_Schema1Refused(t *testing.T) {
	now := time.Now()
	withLocalPlacementEnforced(t, true)
	if got := SourceDialNodePlacement(schema1SourcePair(now), "source", "node-2", now); got != SourceDialUnavailable {
		t.Fatalf("enforcing replica dialed a schema-1 source: verdict %d, want unavailable", got)
	}
	withLocalPlacementEnforced(t, false)
	if got := SourceDialNodePlacement(schema1SourcePair(now), "source", "node-2", now); got != SourceDialPermitted {
		t.Fatalf("non-enforcing replica refused a schema-1 source: verdict %d", got)
	}
}

func TestManagedPlacement_Schema1OnEnforcingReplicaIsTransient(t *testing.T) {
	now := time.Now()
	row := &commodorepb.ManagedStreamRow{TenantId: "tenant", StreamId: "stream", InternalName: "internal", IngestMode: "mist_native", SourceSpec: "file:/media/input.ts", SourceKind: "file", AllowedClusterIds: []string{"source"}, PlacementCount: 1}
	streamCtx := &commodorepb.ResolveStreamContextResponse{Admitted: true, TenantId: "tenant", StreamId: "stream", InternalName: "internal", IngestMode: "mist_native"}
	nodes := []eligibleNode{{nodeID: "node-1", clusterID: "source"}, {nodeID: "node-2", clusterID: "source"}}
	f := &managedPlacementFixture{t: t, pair: schema1SourcePair(now), secret: &mediapb.LiveStreamSecret{}}

	withLocalPlacementEnforced(t, true)
	var admission *ipcpb.ManagedStreamAdmission
	if got := checkManagedPlacementAdmission(context.Background(), f, "source", "node-1", row, streamCtx, &admission); got != materializeTransient || admission != nil {
		t.Fatalf("enforcing replica materialized a schema-1 stream: %v (admission %v)", got, admission)
	}
	if got, status := filterManagedNodesByPlacement(context.Background(), f, row, nodes, now); status != placementTransient || got != nil {
		t.Fatalf("enforcing replica elected a schema-1 stream: %v (%v)", got, status)
	}

	withLocalPlacementEnforced(t, false)
	if got := checkManagedPlacementAdmission(context.Background(), f, "source", "node-1", row, streamCtx, &admission); got != materializeOK || admission != nil {
		t.Fatalf("non-enforcing replica refused a schema-1 stream: %v (admission %v)", got, admission)
	}
	if got, status := filterManagedNodesByPlacement(context.Background(), f, row, nodes, now); status != placementOK || len(got) != 2 {
		t.Fatalf("non-enforcing replica narrowed a schema-1 election: %v (%v)", got, status)
	}
}
