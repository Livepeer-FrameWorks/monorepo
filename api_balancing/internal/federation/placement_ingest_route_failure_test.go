package federation

import (
	"context"
	"errors"
	"strconv"
	"sync/atomic"
	"testing"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/control"
	localauthority "frameworks/api_balancing/internal/mediaauthority"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

func TestIngestPlacementResolverRechecksAuthorityAfterFailedObservation(t *testing.T) {
	for _, change := range []string{"once", "unchanged", "continuous"} {
		t.Run(change, func(t *testing.T) {
			_, route, _, resolvers := ingestRoutingCells(t, "near-us", "rtmp")
			resolver := resolvers["us-cell"]
			originalReader, originalObserve := resolver.Authority, resolver.Router.Observe
			var reads atomic.Int64
			resolver.Authority = placementAuthorityReaderFunc(func(ctx context.Context, tenant, object, internal string) (localauthority.PlacementPair, error) {
				pair, err := originalReader.Placement(ctx, tenant, object, internal)
				read := reads.Add(1)
				if read > 1 && change != "unchanged" {
					pair.Tenant.Authority = proto.CloneOf(pair.Tenant.Authority)
					owner := "advanced-owner"
					if change == "continuous" {
						owner += strconv.FormatInt(read, 10)
					}
					pair.Tenant.Authority.EffectiveClusterGrants[0].OwnerTenantId = owner
				}
				return pair, err
			})
			resolver.Router.Observe = func(ctx context.Context, cell balancer.PlacementCell, req balancer.PlacementRouteRequest) (balancer.PlacementCellObservation, error) {
				if reads.Load() == 1 || change != "once" {
					return balancer.PlacementCellObservation{}, status.Error(codes.FailedPrecondition, "placement authority does not match query")
				}
				return originalObserve(ctx, cell, req)
			}
			got, err := resolver.PrepareIngest(t.Context(), control.IngestPlacementRequest{
				TenantID: "tenant", StreamID: "stream", InternalName: "internal", Protocol: "rtmp", Location: route.Location,
			})
			switch change {
			case "once":
				if err != nil || got.Endpoint == "" || reads.Load() != 3 {
					t.Fatalf("failed route under advancing authority = %+v, %v, reads=%d; want recovery on the newer snapshot", got, err, reads.Load())
				}
			case "unchanged":
				if !errors.Is(err, balancer.ErrPlacementUnavailable) || got.Endpoint != "" || reads.Load() != 2 {
					t.Fatalf("unchanged failed route = %+v, %v, reads=%d; want refusal without another route attempt", got, err, reads.Load())
				}
			case "continuous":
				if !errors.Is(err, ErrPlacementAuthorityChanged) || got.Endpoint != "" || reads.Load() != 1+placementAuthorityRounds {
					t.Fatalf("continuously changing failed route = %+v, %v, reads=%d; want bounded authority conflict", got, err, reads.Load())
				}
			}
		})
	}
}
