package federation

import (
	"context"
	"testing"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/control"
	localauthority "frameworks/api_balancing/internal/mediaauthority"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ingestOwnershipGate builds an ingest admission gate over the discovery
// fixture. Both granted clusters of us-cell carry an ingest candidate, so only
// the ownership constraint decides which of them admits a publisher.
func ingestOwnershipGate(t *testing.T, f *discoveryFixture, reader PlacementAuthorityReader, fence PlacementIngestFenceReader) *PlacementPolicyGate {
	t.Helper()
	return &PlacementPolicyGate{CellID: "us-cell", Authority: reader, IngestFence: fence, Now: func() time.Time { return f.now },
		Router: balancer.PlacementRouter{Observe: func(_ context.Context, cell balancer.PlacementCell, _ balancer.PlacementRouteRequest) (balancer.PlacementCellObservation, error) {
			observation := balancer.PlacementCellObservation{Complete: true, ObservedAt: f.now, ExpiresAt: f.now.Add(10 * time.Second)}
			for _, cluster := range cell.ClusterIDs {
				observation.Candidates = append(observation.Candidates, placement.Candidate{TenantID: "tenant", ClusterID: cluster, NodeID: cluster + "-node", OwnerTenantID: "platform",
					Official: cluster == "us", AllowedVerbs: []placement.Verb{placement.Ingest}, ObservedAt: f.now, ExpiresAt: observation.ExpiresAt,
					Capacity: placement.CapacityAvailable, BWLimit: 1000, BWAvailable: 900, RAMMax: 100, RAMUsed: 10})
			}
			return observation, nil
		}},
	}
}

func commodoreDownFence(reads *int) *ConnectedPlacementIngestFence {
	return &ConnectedPlacementIngestFence{Client: placementStreamContextFunc(func(context.Context, string, string, string, string) (*commodorepb.ResolveStreamContextResponse, error) {
		*reads++
		return nil, status.Error(codes.Unavailable, "commodore is down")
	})}
}

// Authority that changes once while the census is observed is decided again,
// but publisher ownership is a per-request fact: only the local pair is read
// again, never Commodore.
func TestAssess_IngestOwnershipReadOnce(t *testing.T) {
	f := newDiscoveryFixture(t)
	f.query.Verb, f.query.Protocol, f.query.SourceGeneration = placementpb.Verb_VERB_INGEST, "whip", ""
	authority, err := balancer.CompilePlacementAuthority(f.pair, placement.Ingest, f.now)
	if err != nil {
		t.Fatal(err)
	}
	f.query.PolicyDigest = authority.PolicyDigest
	req := placementReceiptRequest(t, f.now)
	req.Query, req.ClusterId, req.NodeId = f.query, "us", "us-node"
	pairReads := 0
	reader := placementAuthorityReaderFunc(func(context.Context, string, string, string) (localauthority.PlacementPair, error) {
		pairReads++
		if pairReads == 2 {
			f.pair.Tenant.Authority.EffectiveClusterGrants[0].MediaConsent.Revision = 7
			f.pair.Tenant.Version++
		}
		return f.pair, nil
	})
	ownershipReads := 0
	fence := placementFenceFunc(func(context.Context, string, string) (string, error) {
		ownershipReads++
		return "us", nil
	})
	result, err := ingestOwnershipGate(t, f, reader, fence).Assess(context.Background(), req)
	if err != nil || result.Choice.ClusterID != "us" {
		t.Fatalf("Assess = %+v, %v", result, err)
	}
	if pairReads != 3 {
		t.Fatalf("pair reads = %d, want 3 (initial, changed, settled)", pairReads)
	}
	if ownershipReads != 1 {
		t.Fatalf("ownership reads = %d, want 1 per request", ownershipReads)
	}
}

// With Commodore down, ingest admission falls back to the pair's signed outage
// owner: the owner cluster admits, any other granted cluster is refused, and a
// pair without an outage owner fails closed.
func TestAdmit_CommodoreDown_OnlySignedOutageOwnerAdmits(t *testing.T) {
	for _, tc := range []struct {
		name, outageOwner, cluster string
		admit                      bool
	}{
		{"owner admits", "us", "us", true},
		{"non-owner cluster refused", "us", "empty", false},
		{"empty outage owner fails closed", "", "us", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDiscoveryFixture(t)
			f.pair.Object.Authority.GetLiveStream().OutageIngestClusterId = tc.outageOwner
			reader := placementAuthorityReaderFunc(func(context.Context, string, string, string) (localauthority.PlacementPair, error) {
				return f.pair, nil
			})
			reads := 0
			gate := ingestOwnershipGate(t, f, reader, commodoreDownFence(&reads))
			decision, err := gate.Admit(context.Background(), PlacementAdmissionInput{TenantID: "tenant", ObjectID: f.pair.Object.AuthorityID, InternalName: "internal",
				ClusterID: tc.cluster, NodeID: tc.cluster + "-node", Protocol: "rtmp", Verb: placement.Ingest})
			if tc.admit {
				if err != nil || decision.ClusterID != tc.cluster {
					t.Fatalf("signed outage owner refused while Commodore is down: %+v, %v", decision, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("admitted %s while Commodore is down with outage owner %q: %+v", tc.cluster, tc.outageOwner, decision)
			}
		})
	}
}

// The front door pins a publisher to the signed outage owner while Commodore
// is unreachable, even when another permitted cluster is nearer.
func TestIngestResolver_CommodoreDown_PinsSignedOutageOwner(t *testing.T) {
	for _, scenario := range []string{"commodore-down", "commodore-down-no-owner"} {
		t.Run(scenario, func(t *testing.T) {
			_, route, ownershipReads, resolvers := ingestRoutingCells(t, scenario, "rtmp")
			result, err := resolvers["us-cell"].PrepareIngest(t.Context(), control.IngestPlacementRequest{TenantID: "tenant", StreamID: "stream", InternalName: "internal",
				Protocol: "rtmp", Location: route.Location})
			if scenario == "commodore-down-no-owner" {
				if err == nil || result.Endpoint != "" {
					t.Fatalf("no signed outage owner still routed: %+v, %v", result, err)
				}
				return
			}
			if err != nil || result.ClusterID != "eu" || result.NodeID != "eu-node" {
				t.Fatalf("outage routing = %+v, %v; want the signed outage owner eu", result, err)
			}
			if ownershipReads.Load() != 1 {
				t.Fatalf("Commodore ownership reads = %d, want 1 before the outage backoff answers", ownershipReads.Load())
			}
		})
	}
}
