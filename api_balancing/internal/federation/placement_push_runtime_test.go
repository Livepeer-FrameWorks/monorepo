package federation

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	federationpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_federation"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/protobuf/proto"
)

func TestPushPreparationKeepsItsExplicitRegistry(t *testing.T) {
	for _, scenario := range []string{"missing-global", "foreign-global", "split-runtime"} {
		t.Run(scenario, func(t *testing.T) {
			destination, media, _, _, fed, request := pushRuntimeFixture(t)
			foreign := control.NewStreamRegistry(nil, "foreign-cell", time.Minute)
			switch scenario {
			case "missing-global":
				control.SetStreamRegistry(nil)
			case "foreign-global":
				control.SetStreamRegistry(foreign)
			case "split-runtime":
				media.Arrange.Registry = foreign
			}
			response, err := destination.PreparePlacement(t.Context(), request)
			if scenario == "split-runtime" {
				if err == nil || response != nil || len(fed.calls) != 0 {
					t.Fatal("split registry runtime started a source")
				}
				return
			}
			if err != nil || response.GetNodeId() != request.NodeId || len(fed.calls) != 1 {
				t.Fatalf("ambient registry changed preparation: %v, %v", response, err)
			}
			pull, found, err := media.Registry.CurrentInboundPull(t.Context(), request.Query.InternalName, request.NodeId)
			if err != nil || !found || !pull.PlacementRequired {
				t.Fatalf("runtime lost its marked physical pull: %+v, %v", pull, err)
			}
			if _, found, err := foreign.CurrentInboundPull(t.Context(), request.Query.InternalName, request.NodeId); err != nil || found {
				t.Fatal("preparation leaked into another registry")
			}
		})
	}
}

func pushRuntimeFixture(t *testing.T) (*PlacementDestination, *LivePushPreparationRuntime, *discoveryFixture, *livePathRegistry, *fakeNotifyFedClient, *placementpb.PreparePlacementRequest) {
	t.Helper()
	registry := freshRegistry(t)
	store, _, req := placementReceiptFixture(t)
	f, paths, source := livePathFixture(t)
	f.pair.Object.Authority.PlaybackId = "public-playback"
	f.discovery.Now = store.Now
	req.Query, req.NodeId = f.query, "node-00"
	fed := &fakeNotifyFedClient{mutateAck: func(ack *federationpb.OriginPullAck) { ack.DtscUrl = "dtsc://eu.example:14200/live+internal" }}
	media := &LivePushPreparationRuntime{
		DestinationFence: func(context.Context, string, string) (int64, error) { return 9007199254740993, nil },
		Authority:        f.discovery.Authority, Paths: paths, Registry: registry,
		Arrange: makeDepsAt(t, fed, map[string]string{"eu-cell": "peer:18009"}, store.Now), Now: store.Now,
	}
	destination := &PlacementDestination{Discovery: f.discovery, Receipts: store, Now: store.Now}
	transport := PlacementTransport{LocalCellID: store.CellID, Local: destination}
	destination.Runtime = &PolicyBoundPlacementRuntime{Policy: &PlacementPolicyGate{
		CellID: store.CellID, Authority: f.discovery.Authority, Router: transport.Router(), Now: store.Now,
	}, Media: media}
	return destination, media, f, source, fed, req
}

func TestPushPreparationArrangesExactNodeAndSharesPhysicalPull(t *testing.T) {
	destination, media, _, _, fed, req := pushRuntimeFixture(t)
	ctx := context.Background()
	first, err := destination.PreparePlacement(ctx, req)
	if err != nil || first.GetReady() || !strings.Contains(first.GetEndpoint(), "/public-playback/") || first.GetNodeId() != req.NodeId {
		t.Fatalf("exact materializing placement failed: %v, %v", first, err)
	}
	stored, found, err := media.Registry.CurrentInboundPull(ctx, req.Query.InternalName, req.NodeId)
	if err != nil || !found || stored.SourceNodeID != "eu-publisher" || stored.SourceMediaClusterID != "eu-ingest" || len(fed.calls) != 1 {
		t.Fatalf("source arrangement differs from selected path: %+v, %v", stored, err)
	}
	next := proto.CloneOf(req)
	next.AttemptId, err = placement.NewPreparationAttemptID(destination.now().Add(time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	second, err := destination.PreparePlacement(ctx, next)
	if err != nil || second.GetReady() || second.GetEndpoint() != first.GetEndpoint() || len(fed.calls) != 1 {
		t.Fatalf("warm viewer restarted source or claimed media readiness: %v, %v", second, err)
	}
	receipt, err := destination.Receipts.Begin(ctx, next)
	if err != nil || receipt.Pull == nil || receipt.Pull.AttemptID != stored.AttemptID {
		t.Fatalf("second viewer relabeled physical attempt: %+v, %v", receipt, err)
	}
	other := proto.CloneOf(req)
	other.NodeId = "node-01"
	other.AttemptId, err = placement.NewPreparationAttemptID(destination.now().Add(2 * time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	if response, prepareErr := destination.PreparePlacement(ctx, other); prepareErr != nil || response.GetNodeId() != "node-01" || len(fed.calls) != 2 {
		t.Fatalf("second destination reused another edge's pull: %v, %v", response, prepareErr)
	}
}

func TestPushPreparationReplayRequiresCurrentSourceAndPull(t *testing.T) {
	for _, change := range []string{"cleared", "generation", "listener", "consent", "ready-claim", "source-store-unavailable"} {
		t.Run(change, func(t *testing.T) {
			destination, media, f, source, fed, req := pushRuntimeFixture(t)
			ctx := context.Background()
			if _, err := destination.PreparePlacement(ctx, req); err != nil {
				t.Fatal(err)
			}
			switch change {
			case "source-store-unavailable":
				source.err = errors.New("shared source unavailable")
			case "cleared":
				pull, found, err := media.Registry.CurrentInboundPull(ctx, req.Query.InternalName, req.NodeId)
				if err != nil || !found {
					t.Fatal("prepared pull missing")
				}
				if _, err = media.Registry.ClearInboundPull(ctx, req.Query.InternalName, req.NodeId, pull.AttemptID); err != nil {
					t.Fatal(err)
				}
			case "generation":
				loc := source.entry.Locations["eu-cell"]
				loc.EdgeCandidates[0].SourceGeneration = "replacement"
				loc.EdgeCandidates[0].SourceRevision++
				source.entry.Locations["eu-cell"] = loc
			case "listener":
				f.snapshot.Nodes[0].Outputs = nil
			case "consent":
				f.pair.Tenant.Authority.EffectiveClusterGrants[0].MediaConsent.AllowExternalSource = false
			case "ready-claim":
				receipt, err := destination.Receipts.Begin(ctx, req)
				if err != nil {
					t.Fatal(err)
				}
				receipt.Response.Ready = true
				if err = media.Revalidate(ctx, req, receipt); err == nil {
					t.Fatal("pull receipt accepted as first-media evidence")
				}
				return
			}
			if response, err := destination.PreparePlacement(ctx, req); err == nil || response != nil || len(fed.calls) != 1 {
				t.Fatalf("invalid replay returned endpoint or restarted source: %v, %v", response, err)
			}
		})
	}
}

func TestPushPreparationReconcilesAmbiguousNotificationWithSamePhysicalAttempt(t *testing.T) {
	destination, _, _, _, fed, req := pushRuntimeFixture(t)
	fed.errs = []error{errors.New("source reply lost")}
	ctx := context.Background()
	if response, err := destination.PreparePlacement(ctx, req); err == nil || response != nil {
		t.Fatalf("ambiguous notification advertised endpoint: %v, %v", response, err)
	}
	pending, err := destination.Receipts.Begin(ctx, req)
	if err != nil || pending.Pull == nil || pending.Response != nil {
		t.Fatalf("ambiguous preparation lost pending physical identity: %+v, %v", pending, err)
	}
	if response, prepareErr := destination.PreparePlacement(ctx, req); prepareErr != nil || response == nil || len(fed.calls) != 2 || fed.calls[1].AttemptId != fed.calls[0].AttemptId {
		t.Fatalf("retry changed physical attempt: %v, %v", response, prepareErr)
	}
}

func TestPushPreparationOnPublisherNeedsNoSourceNotification(t *testing.T) {
	destination, media, f, source, fed, req := pushRuntimeFixture(t)
	source.entry.IngestMode = control.IngestPush
	source.entry.Locations = map[string]control.Location{"us-cell": {
		SourceActive: true, OwnerNodeID: req.NodeId, SourceGeneration: req.Query.SourceGeneration, SourceRevision: 9,
	}}
	f.snapshot.Nodes[0].Streams = map[string]state.BalancerStreamSummary{"internal": {
		TenantID: "tenant", Status: "live", BufferState: "FULL", Inputs: 1, ObservedAt: f.now,
	}}
	media.Arrange, media.Registry = nil, nil
	ctx := context.Background()
	response, err := destination.PreparePlacement(ctx, req)
	if err != nil || response == nil || response.GetReady() || len(fed.calls) != 0 {
		t.Fatalf("publisher placement required pull setup or invented readiness: %v, %v", response, err)
	}
	receipt, err := destination.Receipts.Begin(ctx, req)
	if err != nil || receipt.Pull != nil {
		t.Fatalf("publisher placement created a self-pull: %+v, %v", receipt, err)
	}
}
