package federation

import (
	"context"
	"errors"
	"testing"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type viewerSourceReaderFunc func(context.Context, balancer.PlacementAuthority) (string, time.Time, error)

func (fn viewerSourceReaderFunc) ResolveSourceGeneration(ctx context.Context, authority balancer.PlacementAuthority) (string, time.Time, error) {
	return fn(ctx, authority)
}

func TestViewerPlacementResolverRevalidatesAuthorityAndSource(t *testing.T) {
	for _, change := range []string{"none", "shorter-source", "first-source-expiry", "playback", "authority", "generation", "expired-source", "source-error", "cancelled"} {
		t.Run(change, func(t *testing.T) {
			f := newDiscoveryFixture(t)
			f.now = time.Now()
			f.pair.Tenant.ValidUntil = f.now.Add(time.Minute)
			f.pair.Object.ValidUntil = f.now.Add(20 * time.Second)
			f.pair.Object.Authority.PlaybackId = "public"
			f.inventory.ObservedAt = timestamppb.New(f.now)
			f.paths.ObservedAt, f.paths.ExpiresAt = f.now, f.now.Add(15*time.Second)
			location := &placement.Coordinates{Latitude: 40.7, Longitude: -74}
			for i := range f.snapshot.Nodes {
				f.snapshot.Nodes[i].MetricsObservedAt = f.now
				f.snapshot.Nodes[i].LastHeartbeat = f.now
				f.snapshot.Nodes[i].OutputsObservedAt = f.now
			}
			f.discovery.Paths = placementPathReaderFunc(func(_ context.Context, _ balancer.PlacementAuthority, req *placementpb.CandidateQuery, _ *state.BalancerSnapshot) (PlacementPathObservation, error) {
				if req.GetProtocol() != "hls" || req.GetSourceGeneration() != "source-generation" || req.GetClientLocation().GetLatitude() != location.Latitude || req.GetClientLocation().GetLongitude() != location.Longitude {
					t.Error("viewer protocol, source or original location lost during discovery")
				}
				return f.paths, nil
			})
			sourceCalls, preparations := 0, 0
			sourceUntil := f.now.Add(10 * time.Second)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			rpc := placementRPCFixture{query: f.discovery.QueryPlacementCandidates, prepare: func(_ context.Context, req *placementpb.PreparePlacementRequest) (*placementpb.Preparation, error) {
				preparations++
				if change == "authority" {
					f.pair.Object.Version++
				}
				if change == "cancelled" {
					cancel()
				}
				return preparationWireResponse(req), nil
			}}
			resolver := ViewerPlacementResolver{Authority: f.discovery.Authority,
				Router: (PlacementTransport{LocalCellID: "us-cell", Local: rpc}).Router(),
				Source: viewerSourceReaderFunc(func(ctx context.Context, authority balancer.PlacementAuthority) (string, time.Time, error) {
					sourceCalls++
					if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 5*time.Second || authority.InternalName != "internal" {
						t.Error("source read lost total deadline or signed identity")
					}
					if sourceCalls == 2 {
						switch change {
						case "shorter-source":
							sourceUntil = f.now.Add(5 * time.Second)
						case "first-source-expiry":
							return "source-generation", f.now.Add(19 * time.Second), nil
						case "generation":
							return "replacement", sourceUntil, nil
						case "expired-source":
							return "source-generation", f.now.Add(-time.Second), nil
						case "source-error":
							return "", time.Time{}, errors.New("source owner unavailable")
						}
					}
					return "source-generation", sourceUntil, nil
				})}
			if change == "playback" {
				f.pair.Object.Authority.PlaybackId = "different"
			}
			got, err := resolver.PrepareViewer(ctx, control.ViewerPlacementRequest{TenantID: "tenant", StreamID: "stream", InternalName: "internal", PlaybackID: "public", Protocol: "hls", Location: location})
			switch change {
			case "none", "shorter-source", "first-source-expiry":
				if err != nil || got.ClusterID != "us" || got.NodeID == "" || !got.ExpiresAt.Equal(sourceUntil) || sourceCalls != 2 || preparations != 1 {
					t.Fatalf("prepared destination: %+v, %v, source=%d prepare=%d", got, err, sourceCalls, preparations)
				}
			default:
				if err == nil || got.Endpoint != "" {
					t.Fatalf("changed authority/source returned a destination: %+v, %v", got, err)
				}
				if change == "playback" && (sourceCalls != 0 || preparations != 0) {
					t.Fatal("foreign playback identity reached source preparation")
				}
			}
		})
	}
}
