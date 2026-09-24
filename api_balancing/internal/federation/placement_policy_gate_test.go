package federation

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	"frameworks/api_balancing/internal/balancer"
	localauthority "frameworks/api_balancing/internal/mediaauthority"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type placementFenceFunc func(context.Context, string, string) (string, error)

func (fn placementFenceFunc) ActiveIngestCluster(ctx context.Context, identity PlacementIngestIdentity) (string, error) {
	return fn(ctx, identity.TenantID, identity.InternalName)
}

func TestPlacementPolicyGateRebuildsGlobalCensusFromAuthority(t *testing.T) {
	for _, preferred := range []string{"available", "empty", "unreachable"} {
		t.Run(preferred, func(t *testing.T) {
			f := newDiscoveryFixture(t)
			f.pair.Tenant.Authority.EffectiveClusterGrants[1].ControlCellId = "eu-cell"
			f.pair.Tenant.Authority.EffectiveClusterGrants[1].ClusterId = "eu"
			f.pair.Tenant.Authority.MediaPlacement.Serve = &placementpb.Rules{SchemaVersion: placement.SchemaVersion, Preferences: &placementpb.Preferences{Groups: []*placementpb.Group{
				{Id: "own", Match: &placementpb.Selector{ClusterIds: []string{"eu"}}, Spillover: placementpb.Spillover_SPILLOVER_CAPACITY_ONLY},
				{Id: "official", Match: &placementpb.Selector{ClusterIds: []string{"us"}}},
			}}}
			authority, err := balancer.CompilePlacementAuthority(f.pair, placement.Serve, f.now)
			if err != nil {
				t.Fatal(err)
			}
			req := placementReceiptRequest(t, f.now)
			req.Query = f.query
			req.Query.ClusterIds = []string{"us"}
			req.Query.PolicyDigest = authority.PolicyDigest
			var observedMu sync.Mutex
			var observed []string
			reads := 0
			gate := &PlacementPolicyGate{CellID: "us-cell", Now: func() time.Time { return f.now },
				Authority: placementAuthorityReaderFunc(func(_ context.Context, tenant, object, internal string) (localauthority.PlacementPair, error) {
					reads++
					if tenant != req.Query.TenantId || object != req.Query.ObjectId || internal != req.Query.InternalName {
						t.Fatal("authority identity changed")
					}
					return f.pair, nil
				}),
				Router: balancer.PlacementRouter{Prepare: func(context.Context, balancer.PlacementCell, balancer.PlacementPreparationRequest) (balancer.PlacementPreparationResult, error) {
					t.Fatal("revalidation started preparation")
					return balancer.PlacementPreparationResult{}, nil
				}, Observe: func(_ context.Context, cell balancer.PlacementCell, route balancer.PlacementRouteRequest) (balancer.PlacementCellObservation, error) {
					observedMu.Lock()
					observed = append(observed, cell.ID)
					observedMu.Unlock()
					if len(route.Cells) != 2 || route.PolicyDigest != authority.PolicyDigest || route.SourceGeneration != req.Query.SourceGeneration {
						t.Error("global query lost authority or source binding")
					}
					observation := balancer.PlacementCellObservation{Complete: true, ObservedAt: f.now, ExpiresAt: f.now.Add(10 * time.Second)}
					if cell.ID == "eu-cell" && preferred == "unreachable" {
						return observation, errors.New("peer unavailable")
					}
					if cell.ID == "eu-cell" && preferred == "empty" {
						return observation, nil
					}
					observation.Candidates = []placement.Candidate{{TenantID: "tenant", ClusterID: cell.ClusterIDs[0], NodeID: "edge", OwnerTenantID: "tenant",
						AllowedVerbs: []placement.Verb{placement.Serve}, ObservedAt: f.now, ExpiresAt: observation.ExpiresAt,
						Capacity: placement.CapacityAvailable, BWLimit: 1000, BWAvailable: 900, CPUPercent: 10, RAMMax: 100, RAMUsed: 10,
						Presence: placement.Absent, SourceFeasible: true}}
					return observation, nil
				}},
			}
			result, err := gate.Validate(context.Background(), req)
			slices.Sort(observed)
			// An unreachable cell is observed a second time before the census is
			// accepted as incomplete.
			want := []string{"eu-cell", "us-cell"}
			if preferred == "unreachable" {
				want = []string{"eu-cell", "eu-cell", "us-cell"}
			}
			if !slices.Equal(observed, want) {
				t.Fatalf("caller destination list shrank census: %v", observed)
			}
			if preferred == "empty" {
				if err != nil || result.Choice.ClusterID != "us" || !result.ExpiresAt.Equal(f.now.Add(10*time.Second)) || reads != 2 {
					t.Fatalf("verified spill rejected: %v, %v, reads=%d", result, err, reads)
				}
			} else if err == nil {
				t.Fatalf("preferred %s authorized fallback: %v", preferred, result)
			}
		})
	}
}

func TestPlacementPolicyGateRefusesAuthorityChangesDuringObservation(t *testing.T) {
	for _, change := range []string{"consent", "owner", "charging", "policy", "expired", "tenant-version", "object-version"} {
		t.Run(change, func(t *testing.T) {
			f := newDiscoveryFixture(t)
			f.pair.Tenant.Authority.EffectiveClusterGrants[0].CommercialFacts = &placementpb.CommercialFacts{
				Charging: placementpb.Charging_CHARGING_RATED, Revision: "rates-1", ExpiresAt: timestamppb.New(f.now.Add(time.Minute)),
			}
			req := placementReceiptRequest(t, f.now)
			req.Query, req.NodeId = f.query, "node-00"
			reads := 0
			reader := placementAuthorityReaderFunc(func(context.Context, string, string, string) (localauthority.PlacementPair, error) {
				reads++
				if reads == 2 {
					switch change {
					case "tenant-version":
						f.pair.Tenant.Version++
					case "object-version":
						f.pair.Object.Version++
					case "consent":
						f.pair.Tenant.Authority.EffectiveClusterGrants[0].MediaConsent.AllowExternalSource = false
					case "owner":
						f.pair.Tenant.Authority.EffectiveClusterGrants[0].OwnerTenantId = "another-owner"
					case "charging":
						f.pair.Tenant.Authority.EffectiveClusterGrants[0].CommercialFacts.Charging = placementpb.Charging_CHARGING_PERMANENTLY_FREE
					case "policy":
						f.pair.Object.Authority.MediaPlacement.Revision++
					case "expired":
						f.pair.Object.ValidUntil = f.now
					}
				}
				return f.pair, nil
			})
			transport := PlacementTransport{LocalCellID: "us-cell", Local: &PlacementDestination{Discovery: f.discovery}}
			gate := &PlacementPolicyGate{CellID: "us-cell", Authority: reader, Router: transport.Router(), Now: func() time.Time { return f.now }}
			if result, err := gate.Validate(context.Background(), req); err == nil || result.Choice.NodeID != "" || reads != 2 {
				t.Fatalf("changed %s escaped revalidation: %v, %v, reads=%d", change, result, err, reads)
			}
		})
	}
}

func TestPlacementPolicyGateIngestRequiresCurrentOwnership(t *testing.T) {
	f := newDiscoveryFixture(t)
	f.query.Verb, f.query.Protocol, f.query.SourceGeneration = placementpb.Verb_VERB_INGEST, "whip", ""
	authority, err := balancer.CompilePlacementAuthority(f.pair, placement.Ingest, f.now)
	if err != nil {
		t.Fatal(err)
	}
	f.query.PolicyDigest = authority.PolicyDigest
	req := placementReceiptRequest(t, f.now)
	req.Query, req.NodeId = f.query, "node-00"
	gate := &PlacementPolicyGate{CellID: "us-cell", Authority: f.discovery.Authority, Now: func() time.Time { return f.now },
		Router: balancer.PlacementRouter{Observe: func(context.Context, balancer.PlacementCell, balancer.PlacementRouteRequest) (balancer.PlacementCellObservation, error) {
			return balancer.PlacementCellObservation{Complete: true, ObservedAt: f.now, ExpiresAt: f.now.Add(10 * time.Second), Candidates: []placement.Candidate{{
				TenantID: "tenant", ClusterID: "us", NodeID: "node-00", OwnerTenantID: "platform", Official: true, AllowedVerbs: []placement.Verb{placement.Ingest},
				ObservedAt: f.now, ExpiresAt: f.now.Add(10 * time.Second), Capacity: placement.CapacityAvailable, BWLimit: 1000, BWAvailable: 900, RAMMax: 100,
			}}}, nil
		}},
	}
	if _, err = gate.Validate(context.Background(), req); err == nil {
		t.Fatal("missing ownership reader treated as no active publisher")
	}
	for _, owner := range []string{"us", "", "empty", "unknown", "changed"} {
		calls := 0
		gate.IngestFence = placementFenceFunc(func(_ context.Context, tenant, internal string) (string, error) {
			calls++
			if tenant != "tenant" || internal != "internal" {
				t.Fatal("ownership lookup lost tenant scope")
			}
			if owner == "unknown" {
				return "", errors.New("ownership store unavailable")
			}
			if owner == "changed" {
				if calls == 1 {
					return "us", nil
				}
				return "empty", nil
			}
			return owner, nil
		})
		result, checkErr := gate.Validate(context.Background(), req)
		if owner == "us" || owner == "" {
			if checkErr != nil || result.Choice.NodeID != req.NodeId {
				t.Fatalf("active owner rejected: %v, %v", result, checkErr)
			}
		} else if checkErr == nil {
			t.Fatalf("ownership %s accepted placement", owner)
		}
	}
	req.ExpiresAt = timestamppb.New(f.now)
	if _, err = gate.Validate(context.Background(), req); err == nil {
		t.Fatal("expired decision revalidated")
	}
}
