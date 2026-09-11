package federation

import (
	"context"
	"errors"
	"math"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/api_balancing/internal/balancer"
	localauthority "frameworks/api_balancing/internal/mediaauthority"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
)

func TestPlacementAdmissionRebuildsAuthorityForDirectDestination(t *testing.T) {
	for _, verb := range []placement.Verb{placement.Serve, placement.Ingest} {
		for _, preferred := range []string{"available", "empty", "unreachable"} {
			t.Run(string(verb)+"/"+preferred, func(t *testing.T) {
				f := newDiscoveryFixture(t)
				f.pair.Tenant.Version, f.pair.Object.Version = 103, 105
				f.pair.Tenant.Authority.EffectiveClusterGrants[1].ControlCellId = "eu-cell"
				f.pair.Tenant.Authority.EffectiveClusterGrants[1].ClusterId = "eu"
				rules := &pb.Rules{SchemaVersion: 1, Preferences: &pb.Preferences{Groups: []*pb.Group{
					{Id: "own", Match: &pb.Selector{ClusterIds: []string{"eu"}}, Spillover: pb.Spillover_SPILLOVER_CAPACITY_ONLY},
					{Id: "official", Match: &pb.Selector{ClusterIds: []string{"us"}}},
				}}}
				f.pair.Tenant.Authority.MediaPlacement.Serve, f.pair.Tenant.Authority.MediaPlacement.Ingest = rules, rules
				input := PlacementAdmissionInput{TenantID: "tenant", ObjectID: "live_stream:stream", InternalName: "internal", ClusterID: "us", NodeID: "edge", Protocol: "hls", SourceGeneration: "source-generation", Verb: verb}
				if verb == placement.Ingest {
					input.Protocol, input.SourceGeneration = "whip", ""
				}
				var observations atomic.Int32
				gate := &PlacementPolicyGate{CellID: "us-cell", Authority: f.discovery.Authority, Now: func() time.Time { return f.now },
					IngestFence: placementFenceFunc(func(context.Context, string, string) (string, error) { return "", nil }),
					Router: balancer.PlacementRouter{Prepare: func(context.Context, balancer.PlacementCell, balancer.PlacementPreparationRequest) (balancer.PlacementPreparationResult, error) {
						t.Error("final admission attempted to start media")
						return balancer.PlacementPreparationResult{}, errors.New("unexpected preparation")
					}, Observe: func(_ context.Context, cell balancer.PlacementCell, route balancer.PlacementRouteRequest) (balancer.PlacementCellObservation, error) {
						observations.Add(1)
						if len(route.Cells) != 2 || route.PolicyRevision != 5 || route.ParentRevision != 3 || route.Verb != verb || route.Protocol != input.Protocol || route.SourceGeneration != input.SourceGeneration {
							t.Error("admission lost authoritative census, revisions or connection identity")
						}
						observation := balancer.PlacementCellObservation{Complete: true, ObservedAt: f.now, ExpiresAt: f.now.Add(10 * time.Second)}
						if cell.ID == "eu-cell" && preferred == "unreachable" {
							return observation, errors.New("peer unavailable")
						}
						if cell.ID == "eu-cell" && preferred == "empty" {
							return observation, nil
						}
						observation.Candidates = []placement.Candidate{{TenantID: "tenant", ClusterID: cell.ClusterIDs[0], NodeID: "edge", OwnerTenantID: "tenant",
							AllowedVerbs: []placement.Verb{verb}, ObservedAt: f.now, ExpiresAt: observation.ExpiresAt, Capacity: placement.CapacityAvailable,
							BWLimit: 1000, BWAvailable: 900, RAMMax: 100, RAMUsed: 10, Presence: placement.Present, SourceFeasible: true}}
						return observation, nil
					}},
				}
				result, err := gate.Admit(context.Background(), input)
				if observations.Load() != 2 {
					t.Fatalf("admission omitted a cell: %d", observations.Load())
				}
				if preferred != "empty" {
					if err == nil || result.NodeID != "" {
						t.Fatalf("preferred %s allowed spillover: %+v %v", preferred, result, err)
					}
					return
				}
				if err != nil || result.NodeID != "edge" || result.ClusterID != "us" || result.TenantID != input.TenantID || result.ObjectID != input.ObjectID ||
					result.Protocol != input.Protocol || result.SourceGeneration != input.SourceGeneration || result.PolicyRevision != 5 || result.ParentRevision != 3 ||
					result.TenantAuthorityVersion != 103 || result.ObjectAuthorityVersion != 105 ||
					result.PolicyDigest == "" || !result.ExpiresAt.Equal(f.now.Add(10*time.Second)) {
					t.Fatalf("incorrect final admission binding: %+v %v", result, err)
				}
			})
		}
	}
}

func TestPlacementAdmissionRejectsInvalidAndChangingAuthority(t *testing.T) {
	for _, scenario := range []string{"foreign tenant", "foreign object", "wrong cell", "wrong node", "unknown verb", "missing generation", "invalid geo", "protocol case", "not ready", "schema one", "policy changed", "tenant version changed", "object version changed", "source authority differs", "partial source authority", "negative source authority", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			f := newDiscoveryFixture(t)
			input := PlacementAdmissionInput{TenantID: "tenant", ObjectID: "live_stream:stream", InternalName: "internal", ClusterID: "us", NodeID: "node-00", Protocol: "hls", SourceGeneration: "source-generation", Verb: placement.Serve}
			transport := PlacementTransport{LocalCellID: "us-cell", Local: &PlacementDestination{Discovery: f.discovery}}
			reads := 0
			gate := &PlacementPolicyGate{CellID: "us-cell", Router: transport.Router(), Now: func() time.Time { return f.now },
				Authority: placementAuthorityReaderFunc(func(context.Context, string, string, string) (localauthority.PlacementPair, error) {
					reads++
					if scenario == "policy changed" && reads == 2 {
						f.pair.Object.Authority.MediaPlacement.Revision++
					}
					if scenario == "tenant version changed" && reads == 2 {
						f.pair.Tenant.Version++
					}
					if scenario == "object version changed" && reads == 2 {
						f.pair.Object.Version++
					}
					return f.pair, nil
				})}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch scenario {
			case "source authority differs":
				input.TenantAuthorityVersion, input.ObjectAuthorityVersion = 103, 105
			case "partial source authority":
				input.TenantAuthorityVersion = 3
			case "negative source authority":
				input.TenantAuthorityVersion, input.ObjectAuthorityVersion = -1, -1
			case "foreign tenant":
				input.TenantID = "another"
			case "foreign object":
				input.ObjectID = "live_stream:another"
			case "wrong cell":
				gate.CellID = "another"
			case "wrong node":
				input.NodeID = "another"
			case "unknown verb":
				input.Verb = "storage"
			case "missing generation":
				input.SourceGeneration = ""
			case "invalid geo":
				input.Location = &placement.Coordinates{Latitude: math.NaN()}
			case "protocol case":
				input.Protocol = "HLS"
			case "not ready":
				f.pair.Object.Ready = false
			case "schema one":
				f.pair.Object.Authority.SchemaVersion = 1
			case "canceled":
				cancel()
			}
			result, err := gate.Admit(ctx, input)
			if err == nil || result.NodeID != "" {
				t.Fatalf("invalid admission escaped: %+v %v", result, err)
			}
			if scenario == "canceled" && reads != 0 {
				t.Fatal("canceled admission performed authority reads")
			}
		})
	}
}
