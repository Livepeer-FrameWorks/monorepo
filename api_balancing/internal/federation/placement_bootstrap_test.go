package federation

import (
	"context"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/protobuf/proto"
)

func bootstrapFixture(t *testing.T) (*PlacementDestination, LivePlacementDependencies, *discoveryFixture, *fakeNotifyFedClient, *placementpb.PreparePlacementRequest) {
	t.Helper()
	original, media, fixture, _, fed, request := pushRuntimeFixture(t)
	media.Arrange.LocalSource = &FederationServer{controlCellID: fixture.discovery.CellID, clusterID: "registry-alias"}
	destination := &PlacementDestination{Discovery: original.Discovery, Now: original.Now}
	deps := LivePlacementDependencies{Redis: original.Receipts.Client, Registry: media.Registry, Arrange: media.Arrange,
		IngestFence: placementFenceFunc(func(context.Context, string, string) (string, error) { return "", nil }),
	}
	return destination, deps, fixture, fed, request
}

func TestConfigureLivePlacementDestinationPreparesAndReusesServingPull(t *testing.T) {
	destination, deps, fixture, fed, request := bootstrapFixture(t)
	before := len(fixture.calls)
	if err := ConfigureLivePlacementDestination(destination, deps); err != nil {
		t.Fatal(err)
	}
	if len(fixture.calls) != before || len(fed.calls) != 0 {
		t.Fatal("startup performed placement or source I/O")
	}
	// The marker must already exist when the receipt completes, before the
	// separate retention hook writes any reauthorization request context.
	destination.RetainPrepared = nil
	runtime := destination.Runtime.(*PolicyBoundPlacementRuntime)
	media := runtime.Media.(*PlacementMediaRuntime)
	dispatch := media.Serve.(*MediaServePreparationRuntime)
	serve := dispatch.Push
	serve.DestinationFence = func(context.Context, string, string) (int64, error) { return 9007199254740993, nil }
	if serve.Registry != deps.Registry || serve.Arrange != deps.Arrange || PlacementPathReader(dispatch.Paths) != destination.Discovery.Paths ||
		serve.Paths != dispatch.Paths.Push ||
		destination.Receipts.Client != deps.Redis || runtime.Policy.IngestFence == nil || media.Ingest == nil {
		t.Fatal("startup split the destination dependencies")
	}
	first, err := destination.PreparePlacement(context.Background(), request)
	if err != nil || first.GetNodeId() != request.NodeId || first.GetReady() || len(fed.calls) != 1 {
		t.Fatalf("bootstrapped serving preparation: %v, %v, notifications=%d", first, err, len(fed.calls))
	}
	pull, found, err := deps.Registry.CurrentInboundPull(context.Background(), request.Query.InternalName, request.NodeId)
	if err != nil || !found || !pull.PlacementRequired || pull.PlacementDemand != "" {
		t.Fatalf("prepared URL escaped without its source admission marker: %+v, %v", pull, err)
	}
	for _, fresh := range []bool{false, true} {
		next := proto.CloneOf(request)
		if fresh {
			next.AttemptId, err = placement.NewPreparationAttemptID(destination.now().Add(time.Millisecond))
			if err != nil {
				t.Fatal(err)
			}
		}
		response, prepareErr := destination.PreparePlacement(context.Background(), next)
		if prepareErr != nil || response.GetEndpoint() != first.GetEndpoint() || len(fed.calls) != 1 {
			t.Fatalf("replay/fresh viewer did not reuse physical pull: %v, %v", response, prepareErr)
		}
	}
}

func TestConfigureLivePlacementDestinationPreparesIngest(t *testing.T) {
	for _, protocol := range []string{"rtmp", "srt", "whip"} {
		t.Run(protocol, func(t *testing.T) {
			original, _, fixture, request := ingestRuntimeFixture(t, protocol)
			registry := freshRegistry(t)
			fixture.discovery.Paths = &MediaPlacementPaths{Push: &LivePushPlacementPaths{CellID: fixture.discovery.CellID, Registry: registry, Snapshot: fixture.discovery.Snapshot, Now: original.Now}}
			fed := &fakeNotifyFedClient{}
			arrange := makeDeps(t, fed, nil)
			arrange.LocalSource = &FederationServer{controlCellID: fixture.discovery.CellID}
			destination := &PlacementDestination{Discovery: fixture.discovery, Now: original.Now}
			if err := ConfigureLivePlacementDestination(destination, LivePlacementDependencies{
				Redis: original.Receipts.Client, Registry: registry, Arrange: arrange,
				IngestFence: original.Runtime.(*PolicyBoundPlacementRuntime).Policy.IngestFence,
			}); err != nil {
				t.Fatal(err)
			}
			response, err := destination.PreparePlacement(context.Background(), request)
			if err != nil || response.GetNodeId() != request.NodeId || response.GetOutcome() != placementpb.PreparationOutcome_PREPARATION_OUTCOME_ACCEPTED || len(fed.calls) != 0 {
				t.Fatalf("bootstrapped ingest preparation: %v, %v", response, err)
			}
		})
	}
}

func TestConfigureLivePlacementDestinationRejectsIncompleteConfigurationAtomically(t *testing.T) {
	for _, missing := range []string{"discovery", "cell", "authority", "inventory", "snapshot", "paths", "path-cell", "path-registry", "path-snapshot", "redis", "registry", "arrange-registry", "arrange", "cache", "source", "source-cell", "fence"} {
		t.Run(missing, func(t *testing.T) {
			destination, deps, _, _, _ := bootstrapFixture(t)
			dispatch := destination.Discovery.Paths.(*MediaPlacementPaths)
			paths := dispatch.Push
			switch missing {
			case "discovery":
				destination.Discovery = nil
			case "cell":
				destination.Discovery.CellID = ""
			case "authority":
				destination.Discovery.Authority = nil
			case "inventory":
				destination.Discovery.Inventory = nil
			case "snapshot":
				destination.Discovery.Snapshot = nil
			case "paths":
				destination.Discovery.Paths = nil
			case "path-cell":
				paths.CellID = "foreign-cell"
			case "path-registry":
				paths.Registry = nil
			case "path-snapshot":
				paths.Snapshot = nil
			case "redis":
				deps.Redis = nil
			case "registry":
				deps.Registry = nil
			case "arrange-registry":
				deps.Arrange.Registry = nil
			case "arrange":
				deps.Arrange = nil
			case "cache":
				deps.Arrange.Cache = nil
			case "source":
				deps.Arrange.LocalSource = nil
			case "source-cell":
				deps.Arrange.LocalSource.controlCellID = "foreign-cell"
			case "fence":
				deps.IngestFence = nil
			}
			if err := ConfigureLivePlacementDestination(destination, deps); err == nil || destination.Runtime != nil || destination.Receipts != nil {
				t.Fatal("invalid bootstrap partially installed preparation")
			}
		})
	}
	if err := ConfigureLivePlacementDestination(nil, LivePlacementDependencies{}); err == nil {
		t.Fatal("nil destination accepted")
	}
}

func TestConfigureLivePlacementDestinationDoesNotReplaceOrPromote(t *testing.T) {
	destination, deps, fixture, fed, request := bootstrapFixture(t)
	fixture.pair.Tenant.Ready = false
	fixture.pair.Object.Ready = false
	if err := ConfigureLivePlacementDestination(destination, deps); err != nil {
		t.Fatal(err)
	}
	runtime, receipts := destination.Runtime, destination.Receipts
	if err := ConfigureLivePlacementDestination(destination, deps); err == nil || destination.Runtime != runtime || destination.Receipts != receipts {
		t.Fatal("reconfiguration replaced live preparation state")
	}
	response, err := destination.PreparePlacement(context.Background(), request)
	if err == nil || response != nil || len(fed.calls) != 0 || fixture.pair.Tenant.Ready || fixture.pair.Object.Ready {
		t.Fatalf("bootstrap promoted unready authority: %v, %v", response, err)
	}
}

func TestConfiguredDestinationExposesItsPushPreparation(t *testing.T) {
	destination, deps, _, _, _ := bootstrapFixture(t)
	// Source admission binds to this accessor at startup, so an unconfigured
	// destination must report nothing rather than a half-built runtime.
	if destination.PushPreparation() != nil {
		t.Fatal("unconfigured destination exposed a push preparation runtime")
	}
	if err := ConfigureLivePlacementDestination(destination, deps); err != nil {
		t.Fatal(err)
	}
	push := destination.PushPreparation()
	if push == nil || push.Registry != deps.Registry || push.Arrange != deps.Arrange ||
		push.Paths != destination.Discovery.Paths.(*MediaPlacementPaths).Push {
		t.Fatalf("configured destination did not expose its own push preparation: %+v", push)
	}
	// A serving runtime that is not the destination's own dispatcher cannot
	// satisfy the accessor either.
	destination.Runtime.(*PolicyBoundPlacementRuntime).Media.(*PlacementMediaRuntime).Serve = &MediaServePreparationRuntime{}
	if destination.PushPreparation() != nil {
		t.Fatal("foreign serving runtime satisfied the push preparation accessor")
	}
}
