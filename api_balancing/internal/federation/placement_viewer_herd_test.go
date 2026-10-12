package federation

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"frameworks/api_balancing/internal/control"
	localauthority "frameworks/api_balancing/internal/mediaauthority"
	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// A viewer herd on one stream prepares the same destination node many times
// over, each viewer from its own location. Every preparation shares the one
// physical pull already arranged there, so the herd must be answered without
// rewriting that pull's record per viewer: concurrent rewrites of the stream's
// shared record exhaust the compare-and-set retries and refuse viewers while the
// node still has room.
func TestPushPreparationViewerHerdSharesThePullWithoutRewritingIt(t *testing.T) {
	destination, media, fixture, _, fed, request := pushRuntimeFixture(t)
	ctx := context.Background()
	// The fixture's readers record their calls for sequential tests; the herd
	// reads the same facts concurrently.
	authority := placementAuthorityReaderFunc(func(context.Context, string, string, string) (localauthority.PlacementPair, error) {
		return fixture.pair, nil
	})
	fixture.discovery.Authority, media.Authority = authority, authority
	destination.Runtime.(*PolicyBoundPlacementRuntime).Policy.Authority = authority
	fixture.discovery.Snapshot = func() *state.BalancerSnapshot { return fixture.snapshot }
	fixture.discovery.Inventory = placementInventoryReaderFunc(func(context.Context, *quartermasterpb.GetMediaPlacementInventoryRequest) (*quartermasterpb.MediaPlacementInventory, error) {
		return fixture.inventory, nil
	})
	if _, _, err := media.Registry.EnableRedisSync(t.Context(), control.NewRedisRegistryStore(media.Arrange.Cache.client, "cluster-local"), "replica-a", testLogger()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(media.Registry.DisableRedisSync)
	destination.RetainPrepared = media.RetainPreparedSourceDemand
	request.Query.ClientLocation = &placementpb.Coordinates{Latitude: 52.37, Longitude: 4.89}
	first, err := destination.PreparePlacement(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	changelog := "{cluster-local}:registry_changelog"
	before, err := media.Arrange.Cache.client.XLen(ctx, changelog).Result()
	if err != nil {
		t.Fatal(err)
	}

	const viewers = 160
	var wg sync.WaitGroup
	failures := make(chan error, viewers)
	start := make(chan struct{})
	for viewer := range viewers {
		next := proto.CloneOf(request)
		next.AttemptId, err = placement.NewPreparationAttemptID(destination.now())
		if err != nil {
			t.Fatal(err)
		}
		next.Query.ClientLocation = &placementpb.Coordinates{Latitude: 40 + float64(viewer%20)/10, Longitude: -3 + float64(viewer%13)/10}
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			response, prepareErr := destination.PreparePlacement(ctx, next)
			switch {
			case prepareErr != nil:
				failures <- prepareErr
			case response.GetOutcome() != placementpb.PreparationOutcome_PREPARATION_OUTCOME_ACCEPTED || response.GetEndpoint() != first.GetEndpoint():
				failures <- fmt.Errorf("viewer was not handed the shared pull: %v", response)
			}
		}()
	}
	close(start)
	wg.Wait()
	close(failures)
	failed := 0
	var firstFailure error
	for failure := range failures {
		if firstFailure == nil {
			firstFailure = failure
		}
		failed++
	}
	if failed > 0 {
		t.Fatalf("%d of %d herd viewers refused, first: %v", failed, viewers, firstFailure)
	}
	after, err := media.Arrange.Cache.client.XLen(ctx, changelog).Result()
	if err != nil {
		t.Fatal(err)
	}
	if writes := after - before; writes > 1 {
		t.Fatalf("herd rewrote the stream's pull record %d times, want at most 1", writes)
	}
	if len(fed.calls) != 1 {
		t.Fatalf("herd arranged %d source pulls, want the one shared pull", len(fed.calls))
	}
	pull, found, err := media.Registry.CurrentInboundPull(ctx, request.Query.InternalName, request.NodeId)
	if err != nil || !found || !pull.PlacementRequired || pull.PlacementDemand == "" {
		t.Fatalf("herd lost the retained pull state: %+v, %v", pull, err)
	}
}

// Contention that outlasts the record's compare-and-set retries refuses this
// attempt only for now. The calling cell must see a status it retries on another
// replica, not an internal failure.
func TestPreparedDemandContentionIsRetryable(t *testing.T) {
	destination, media, _, _, fed, request := pushRuntimeFixture(t)
	destination.RetainPrepared = func(context.Context, *placementpb.PreparePlacementRequest, PlacementReceipt) error {
		return fmt.Errorf("retain demand: %w", control.ErrReplicationContention)
	}
	response, err := destination.PreparePlacement(context.Background(), request)
	if response != nil || status.Code(err) != codes.Unavailable {
		t.Fatalf("exhausted contention = %v (%s), want Unavailable", err, status.Code(err))
	}
	server := &FederationServer{}
	if mapped := server.placementRPCError("prepare", "tenant", "internal", "us", "node-00", err); status.Code(mapped) != codes.Unavailable {
		t.Fatalf("federation boundary turned contention into %s", status.Code(mapped))
	}
	if control.ClassifyViewerPlacementError(err) != control.ViewerPlacementNotReady {
		t.Fatal("viewer path does not treat contention as a retryable refusal")
	}
	destination.RetainPrepared = media.RetainPreparedSourceDemand
	if response, err = destination.PreparePlacement(context.Background(), request); err != nil || response == nil || len(fed.calls) != 1 {
		t.Fatalf("replay after contention failed or restarted the pull: %v, %v", response, err)
	}
	if errors.Is(err, control.ErrReplicationConflict) {
		t.Fatal("contention surfaced as a superseded attempt")
	}
}
