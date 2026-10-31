package triggers

import (
	"context"
	"testing"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/federation"
	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func TestDirectViewerReopensClearedReplica(t *testing.T) {
	for _, mode := range []string{"push", "pull", "mist_native"} {
		t.Run(mode, func(t *testing.T) {
			ingestMode, err := control.IngestModeFromWire(mode)
			if err != nil {
				t.Fatal(err)
			}
			resetStateTrigHandlers(t)
			state.DefaultManager().SetNodeConnectionInfo(t.Context(), "edge", "", "", "us", nil)
			registry := control.NewStreamRegistry(nil, "us-cell", time.Minute)
			previous := control.StreamRegistryInstance
			control.SetStreamRegistry(registry)
			t.Cleanup(func() { control.SetStreamRegistry(previous) })
			processor := newTestProcessor(t)
			old, err := registry.RecordInboundPull(t.Context(), "stream", control.InboundPull{
				TenantID: "tenant", SourceClusterID: "eu-cell", SourceNodeID: "origin", DestClusterID: "us", DestNodeID: "edge", DTSCURL: "dtsc://origin/retired",
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = registry.ClearInboundPull(t.Context(), "stream", "edge", old.AttemptID); err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			pair := viewerAdapterPair(now)
			pair.Tenant.Authority.MediaPlacement.Serve.Preferences.Groups[0].MaxDistanceKm = 0
			pair.Object.Authority.GetLiveStream().IngestMode = mode
			preparations := 0
			adapter := &ViewerPlacementAdapter{
				Authority: viewerPlacementPairReader{pair: pair},
				Source: viewerSourceGenerationFunc(func(context.Context, balancer.PlacementAuthority) (string, time.Time, error) {
					return "generation", now.Add(10 * time.Second), nil
				}),
				Gate: &federation.PlacementPolicyGate{CellID: "us-cell", Authority: viewerPlacementPairReader{pair: pair}, Router: balancer.PlacementRouter{
					Observe: func(context.Context, balancer.PlacementCell, balancer.PlacementRouteRequest) (balancer.PlacementCellObservation, error) {
						return balancer.PlacementCellObservation{Complete: true, ObservedAt: now, ExpiresAt: now.Add(10 * time.Second), Candidates: []placement.Candidate{{
							TenantID: "tenant", ClusterID: "us", NodeID: "edge", OwnerTenantID: "tenant", AllowedVerbs: []placement.Verb{placement.Serve},
							ObservedAt: now, ExpiresAt: now.Add(10 * time.Second), Capacity: placement.CapacityAvailable, BWAvailable: 100, BWLimit: 1000,
							RAMUsed: 1, RAMMax: 100, Presence: placement.Present, SourceFeasible: true,
						}}}, nil
					},
					Prepare: func(ctx context.Context, cell balancer.PlacementCell, request balancer.PlacementPreparationRequest) (balancer.PlacementPreparationResult, error) {
						preparations++
						state.DefaultManager().ReservePendingViewer(request.AttemptID, "edge", "stream")
						if cell.ID != "us-cell" || request.Choice.ClusterID != "us" || request.Choice.NodeID != "edge" || request.Route.SourceGeneration != "generation" || request.Route.Protocol != "hls" {
							t.Fatal("viewer preparation lost its exact destination or source identity")
						}
						pull, recordErr := registry.RecordInboundPull(ctx, "stream", control.InboundPull{
							TenantID: "tenant", SourceClusterID: "eu-cell", SourceNodeID: "origin", DestClusterID: "us", DestNodeID: "edge", DTSCURL: "dtsc://origin/fresh",
						})
						if recordErr != nil {
							return balancer.PlacementPreparationResult{}, recordErr
						}
						if pull.AttemptID == old.AttemptID {
							t.Fatal("reopening revived the retired physical attempt")
						}
						admitPreparedSourceForTest(t, processor, "stream", pull)
						return viewerReopenPreparation(request), nil
					},
				}},
			}
			connection := ViewerPlacementConnection{TenantID: "tenant", InternalName: "stream", ClusterID: "us", NodeID: "edge", Connector: "HLS", ClientAddress: "203.0.113.1", PreSource: true}
			if _, err = adapter.AdmitViewer(t.Context(), connection); err != nil {
				t.Fatal(err)
			}
			if preparations != 1 {
				t.Fatalf("new direct viewer prepared %d sources after replica cleanup; want 1", preparations)
			}
			if node := state.DefaultManager().GetNodeState("edge"); node.PendingRedirects != 0 || node.AddBandwidth != 0 {
				t.Fatal("direct viewer preparation left a second pending redirect reservation")
			}
			response, abort, err := processor.handleStreamSource(&ipcpb.MistTrigger{NodeId: "edge", TriggerPayload: &ipcpb.MistTrigger_StreamSource{
				StreamSource: &ipcpb.StreamSourceTrigger{StreamName: control.RuntimeNameFor(ingestMode, "stream")},
			}})
			if err != nil || abort || response != "dtsc://origin/fresh" {
				t.Fatalf("reopened viewer has no fresh source: %q %t %v", response, abort, err)
			}
			connection.PreSource, connection.Connector = false, "HTTP,HLS"
			if _, err = adapter.AdmitViewer(t.Context(), connection); err != nil || preparations != 1 {
				t.Fatalf("final viewer admission prepared media again: preparations=%d error=%v", preparations, err)
			}
		})
	}
}

func viewerReopenPreparation(request balancer.PlacementPreparationRequest) balancer.PlacementPreparationResult {
	return balancer.PlacementPreparationResult{
		Outcome: balancer.PlacementAccepted, TenantID: request.Route.TenantID, ObjectID: request.Route.ObjectID, SourceGeneration: request.Route.SourceGeneration,
		ClusterID: request.Choice.ClusterID, NodeID: request.Choice.NodeID, Protocol: request.Route.Protocol, PolicyDigest: request.Route.PolicyDigest,
		PolicyRevision: request.Route.PolicyRevision, ParentRevision: request.Route.ParentRevision, AttemptID: request.AttemptID, ExpiresAt: request.ExpiresAt,
		Endpoint: "https://edge/view/hls/public/index.m3u8",
	}
}
