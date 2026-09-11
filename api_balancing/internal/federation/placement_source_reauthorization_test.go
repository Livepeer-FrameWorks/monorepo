package federation

import (
	"context"
	"errors"
	"testing"
	"time"

	"frameworks/api_balancing/internal/balancer"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestSourceReauthorizationRenewsPermissionNotPhysicalPull(t *testing.T) {
	for _, reason := range []string{"expired", "reconnect", "authority-renewal"} {
		t.Run(reason, func(t *testing.T) {
			destination, media, fixture, _, fed, request := pushRuntimeFixture(t)
			store, redis, _ := placementReceiptFixture(t)
			now := store.Now()
			clock := func() time.Time { return now }
			store.Now, destination.Now, media.Now, media.Paths.Now, fixture.discovery.Now = clock, clock, clock, clock, clock
			destination.Receipts = store
			destination.Runtime.(*PolicyBoundPlacementRuntime).Policy.Now = clock
			destination.RetainPrepared = media.RetainPreparedSourceDemand
			fence := int64(9007199254740993)
			media.DestinationFence = func(context.Context, string, string) (int64, error) { return fence, nil }
			request.Query.ClientLocation = &placementpb.Coordinates{Latitude: 37, Longitude: -122}
			request.ExpiresAt = timestamppb.New(now.Add(time.Second))
			first, err := destination.PreparePlacement(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			input := pushSourceIdentity(request)
			before, err := media.ResolvePreparedSource(context.Background(), store, input)
			if err != nil {
				t.Fatal(err)
			}
			gate := destination.Runtime.(*PolicyBoundPlacementRuntime).Policy
			observe := gate.Router.Observe
			observations := 0
			gate.Router.Observe = func(ctx context.Context, cell balancer.PlacementCell, route balancer.PlacementRouteRequest) (balancer.PlacementCellObservation, error) {
				observations++
				if route.Location == nil || route.Location.Latitude != 37 || route.Location.Longitude != -122 {
					t.Fatal("fresh census lost original viewer geography")
				}
				return observe(ctx, cell, route)
			}
			if _, err = destination.ResolveOrReauthorizeSource(context.Background(), media, input); err != nil || observations != 0 {
				t.Fatal("valid evidence unnecessarily reran the federation census")
			}
			switch reason {
			case "expired":
				now = now.Add(2 * time.Second)
				redis.SetTime(now)
				redis.FastForward(2 * time.Second)
			case "reconnect":
				fence++
				input.DestinationFence = fence
			case "authority-renewal":
				fixture.pair.Tenant.Version++
				fixture.pair.Object.Version++
			}
			if _, err = media.ResolvePreparedSource(context.Background(), store, input); err == nil {
				t.Fatal("old evidence unexpectedly remained usable")
			}
			after, err := destination.ResolveOrReauthorizeSource(context.Background(), media, input)
			if err != nil || after.AttemptID != before.AttemptID || after.DTSCURL != before.DTSCURL || len(fed.calls) != 1 || observations == 0 {
				t.Fatalf("fresh admission restarted or lost source: %+v, %v, notifications=%d", after, err, len(fed.calls))
			}
			if reason == "expired" && !after.ExpiresAt.After(first.ExpiresAt.AsTime()) {
				t.Fatal("fresh evidence retained expired deadline")
			}
			pull, found, err := media.Registry.CurrentInboundPull(context.Background(), input.InternalName, input.NodeID)
			if err != nil || !found {
				t.Fatal("retained demand lost physical pull")
			}
			query := &placementpb.CandidateQuery{}
			if err = protojson.Unmarshal([]byte(pull.PlacementDemand), query); err != nil || !proto.Equal(query.ClientLocation, request.Query.ClientLocation) || query.Protocol != request.Query.Protocol {
				t.Fatal("reauthorization changed original viewer context")
			}
		})
	}
}

func TestSourceReauthorizationDoesNotTurnContextIntoPermission(t *testing.T) {
	for _, change := range []string{"missing-context", "foreign-context", "consent", "unready", "source-replaced", "cleared", "connection-changed", "store-unavailable", "inventory-unknown", "destination-unavailable", "protocol-withdrawn"} {
		t.Run(change, func(t *testing.T) {
			destination, media, fixture, source, fed, request := pushRuntimeFixture(t)
			destination.RetainPrepared = media.RetainPreparedSourceDemand
			ctx := context.Background()
			if _, err := destination.PreparePlacement(ctx, request); err != nil {
				t.Fatal(err)
			}
			input := pushSourceIdentity(request)
			fixture.pair.Object.Version++
			pull, _, err := media.Registry.CurrentInboundPull(ctx, input.InternalName, input.NodeID)
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "missing-context":
				if err = media.Registry.RecordInboundPlacementDemand(ctx, input.InternalName, pull, "{}"); err != nil {
					t.Fatal(err)
				}
			case "foreign-context":
				query := proto.CloneOf(request.Query)
				query.TenantId = "foreign"
				payload, _ := protojson.Marshal(query)
				if err = media.Registry.RecordInboundPlacementDemand(ctx, input.InternalName, pull, string(payload)); err != nil {
					t.Fatal(err)
				}
			case "consent":
				fixture.pair.Tenant.Authority.EffectiveClusterGrants[0].MediaConsent.AllowExternalSource = false
			case "unready":
				fixture.pair.Object.Ready = false
			case "source-replaced":
				location := source.entry.Locations["eu-cell"]
				location.EdgeCandidates[0].SourceGeneration = "replacement"
				location.EdgeCandidates[0].SourceRevision++
				source.entry.Locations["eu-cell"] = location
			case "cleared":
				if _, err = media.Registry.ClearInboundPull(ctx, input.InternalName, input.NodeID, pull.AttemptID); err != nil {
					t.Fatal(err)
				}
			case "connection-changed":
				input.DestinationFence++
			case "store-unavailable":
				_ = destination.Receipts.Client.Close()
			case "inventory-unknown":
				fixture.inventory.Complete = false
			case "destination-unavailable":
				fixture.inventory.Nodes[0].AdmissionEnabled = false
			case "protocol-withdrawn":
				fixture.snapshot.Nodes[0].Outputs = nil
			}
			if result, resolveErr := destination.ResolveOrReauthorizeSource(ctx, media, input); resolveErr == nil || result.DTSCURL != "" || len(fed.calls) != 1 {
				t.Fatalf("invalid demand renewed source: %+v, %v", result, resolveErr)
			}
		})
	}
}

func TestPreparedDemandRetentionFailureIsRetriedWithoutNewPull(t *testing.T) {
	destination, media, _, _, fed, request := pushRuntimeFixture(t)
	destination.RetainPrepared = func(context.Context, *placementpb.PreparePlacementRequest, PlacementReceipt) error {
		return errors.New("persistence unavailable")
	}
	if response, err := destination.PreparePlacement(context.Background(), request); err == nil || response != nil {
		t.Fatal("retention failure was acknowledged")
	}
	destination.RetainPrepared = media.RetainPreparedSourceDemand
	if response, err := destination.PreparePlacement(context.Background(), request); err != nil || response == nil || len(fed.calls) != 1 {
		t.Fatalf("replay failed durable retention without restart: %v, %v", response, err)
	}
}
