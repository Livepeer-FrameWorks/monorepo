package federation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"frameworks/api_balancing/internal/balancer"
	localauthority "frameworks/api_balancing/internal/mediaauthority"
	"frameworks/api_balancing/internal/state"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/placement"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type placementAuthorityReaderFunc func(context.Context, string, string, string) (localauthority.PlacementPair, error)

func (fn placementAuthorityReaderFunc) Placement(ctx context.Context, tenant, object, internal string) (localauthority.PlacementPair, error) {
	return fn(ctx, tenant, object, internal)
}

type placementInventoryReaderFunc func(context.Context, *quartermasterpb.GetMediaPlacementInventoryRequest) (*quartermasterpb.MediaPlacementInventory, error)

func (fn placementInventoryReaderFunc) GetMediaPlacementInventory(ctx context.Context, req *quartermasterpb.GetMediaPlacementInventoryRequest) (*quartermasterpb.MediaPlacementInventory, error) {
	return fn(ctx, req)
}

type placementPathReaderFunc func(context.Context, balancer.PlacementAuthority, *placementpb.CandidateQuery, *state.BalancerSnapshot) (PlacementPathObservation, error)

func (fn placementPathReaderFunc) ObservePlacementPaths(ctx context.Context, authority balancer.PlacementAuthority, req *placementpb.CandidateQuery, snapshot *state.BalancerSnapshot) (PlacementPathObservation, error) {
	return fn(ctx, authority, req, snapshot)
}

type discoveryFixture struct {
	discovery *PlacementDiscovery
	query     *placementpb.CandidateQuery
	pair      localauthority.PlacementPair
	inventory *quartermasterpb.MediaPlacementInventory
	snapshot  *state.BalancerSnapshot
	paths     PlacementPathObservation
	now       time.Time
	calls     []string
}

func newDiscoveryFixture(t *testing.T) *discoveryFixture {
	t.Helper()
	f := &discoveryFixture{now: time.Unix(1800000000, 0)}
	f.pair = localauthority.PlacementPair{
		Tenant: localauthority.TenantSnapshot{Version: 3, Ready: true, IngestReady: true, SourceReady: true, ValidUntil: f.now.Add(time.Minute),
			Authority: &mediaauthoritypb.TenantAuthority{SchemaVersion: sharedauthority.PlacementSchemaVersion, TenantId: "tenant",
				Lifecycle: mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE, BillingDecision: mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_ALLOW,
				MediaPlacement: &placementpb.PolicySet{Revision: 3},
				EffectiveClusterGrants: []*mediaauthoritypb.TenantClusterGrant{
					{ClusterId: "us", ControlCellId: "us-cell", ClusterClass: "platform_official", OwnerTenantId: "platform", SubscriptionStatus: "active",
						MediaConsent: &placementpb.CapacityConsent{AllowIngest: true, AllowServe: true, AllowExternalSource: true}},
					{ClusterId: "empty", ControlCellId: "us-cell", ClusterClass: "tenant_private", OwnerTenantId: "tenant", SubscriptionStatus: "active",
						MediaConsent: &placementpb.CapacityConsent{AllowIngest: true, AllowServe: true}},
				},
			}},
		Object: localauthority.MediaObjectSnapshot{AuthorityID: sharedauthority.LiveStreamAuthorityID("stream"), Version: 5, Ready: true, IngestReady: true, SourceReady: true, ValidUntil: f.now.Add(20 * time.Second),
			Authority: &mediaauthoritypb.MediaObjectAuthority{SchemaVersion: sharedauthority.PlacementSchemaVersion, TenantId: "tenant", InternalName: "internal",
				Lifecycle: mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE, ObjectKind: mediaauthoritypb.MediaObjectKind_MEDIA_OBJECT_KIND_LIVE_STREAM,
				MediaPlacement: &placementpb.PolicySet{Revision: 5}, PlacementTenantRevision: 3,
				Object: &mediaauthoritypb.MediaObjectAuthority_LiveStream{LiveStream: &mediaauthoritypb.LiveStreamAuthority{StreamId: "stream", IngestMode: "push"}}}},
	}
	authority, err := balancer.CompilePlacementAuthority(f.pair, placement.Serve, f.now)
	if err != nil {
		t.Fatal(err)
	}
	f.query = &placementpb.CandidateQuery{TenantId: authority.TenantID, ObjectId: authority.ObjectID, InternalName: authority.InternalName,
		Verb: placementpb.Verb_VERB_SERVE, Protocol: "hls", PolicyDigest: authority.PolicyDigest, PolicyRevision: authority.PolicyRevision, ParentRevision: authority.ParentRevision,
		SourceGeneration: "source-generation", ClusterIds: []string{"empty", "us"}}
	f.inventory = &quartermasterpb.MediaPlacementInventory{TenantId: "tenant", ControlCellId: "us-cell", ClusterIds: []string{"empty", "us"}, Complete: true, ObservedAt: timestamppb.New(f.now)}
	f.snapshot = &state.BalancerSnapshot{}
	f.paths = PlacementPathObservation{SourceGeneration: f.query.SourceGeneration, ObservedAt: f.now, ExpiresAt: f.now.Add(15 * time.Second), Paths: make(map[string]balancer.PlacementNodePath)}
	for i := range 12 {
		id := fmt.Sprintf("node-%02d", i)
		f.inventory.Nodes = append(f.inventory.Nodes, &quartermasterpb.MediaPlacementInventoryNode{ClusterId: "us", NodeId: id, AdmissionEnabled: true})
		f.snapshot.Nodes = append(f.snapshot.Nodes, state.EnhancedBalancerNodeSnapshot{NodeID: id, ClusterID: "us", TenantID: "untrusted-owner", Host: "https://" + id + ".example",
			IsActive: true, CapIngest: true, CapEdge: true, CPU: 10, RAMMax: 100, RAMCurrent: 10, BWLimit: 1000, MetricsObservedAt: f.now, LastHeartbeat: f.now})
		f.snapshot.Nodes[i].OutputsObservedAt = f.now
		f.snapshot.Nodes[i].Outputs = map[string]any{"HLS": "http://HOST:8080/hls/$/index.m3u8"}
		f.paths.Paths[id] = balancer.PlacementNodePath{Presence: placement.Absent, SourceFeasible: true}
	}
	f.discovery = &PlacementDiscovery{CellID: "us-cell", Now: func() time.Time { return f.now }, Snapshot: func() *state.BalancerSnapshot { f.calls = append(f.calls, "snapshot"); return f.snapshot }}
	f.discovery.Authority = placementAuthorityReaderFunc(func(ctx context.Context, tenant, object, internal string) (localauthority.PlacementPair, error) {
		f.calls = append(f.calls, "authority")
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > placementDiscoveryMaxTimeout || tenant != "tenant" || object != f.pair.Object.AuthorityID || internal != "internal" {
			t.Fatal("authority read lost identity or deadline")
		}
		return f.pair, nil
	})
	f.discovery.Inventory = placementInventoryReaderFunc(func(_ context.Context, req *quartermasterpb.GetMediaPlacementInventoryRequest) (*quartermasterpb.MediaPlacementInventory, error) {
		f.calls = append(f.calls, "inventory")
		if req.TenantId != "tenant" || req.ControlCellId != "us-cell" || strings.Join(req.ClusterIds, ",") != "empty,us" {
			t.Fatal("inventory lost exact tenant/cell membership")
		}
		return f.inventory, nil
	})
	f.discovery.Paths = placementPathReaderFunc(func(_ context.Context, authority balancer.PlacementAuthority, req *placementpb.CandidateQuery, snapshot *state.BalancerSnapshot) (PlacementPathObservation, error) {
		f.calls = append(f.calls, "paths")
		if authority.TenantID != "tenant" || !proto.Equal(req, f.query) || len(snapshot.Nodes) != len(f.inventory.Nodes) {
			t.Fatal("paths did not receive authorized exact inventory")
		}
		return f.paths, nil
	})
	return f
}

func TestPlacementDiscoveryPreservesAllColdAndUnavailableNodes(t *testing.T) {
	f := newDiscoveryFixture(t)
	f.snapshot.Nodes[1].IsActive = false
	response, err := f.discovery.QueryPlacementCandidates(context.Background(), f.query)
	if err != nil || !response.GetComplete() || len(response.GetCandidates()) != 12 {
		t.Fatalf("discovery truncated cold/unavailable nodes: %+v, %v", response, err)
	}
	if strings.Join(f.calls, ",") != "authority,inventory,snapshot,paths" {
		t.Fatalf("discovery order: %v", f.calls)
	}
	for i, candidate := range response.Candidates {
		if candidate.OwnerTenantId != "platform" || !candidate.Official || candidate.Presence != placementpb.Presence_PRESENCE_ABSENT || !candidate.SourceFeasible || !candidate.ExpiresAt.AsTime().Equal(f.paths.ExpiresAt) {
			t.Fatalf("lost destination facts: %+v", candidate)
		}
		want := placementpb.Capacity_CAPACITY_AVAILABLE
		if i == 1 {
			want = placementpb.Capacity_CAPACITY_UNAVAILABLE
		}
		if candidate.Capacity != want {
			t.Fatalf("candidate capacity: %+v", candidate)
		}
	}
	if response.PolicyDigest != f.query.PolicyDigest || response.PolicyRevision != 5 || response.ParentRevision != 3 || response.SourceGeneration != f.query.SourceGeneration {
		t.Fatal("observation lost policy/generation binding")
	}
}

func TestPlacementDiscoveryEmptyPoolHasBoundedCompletenessEnvelope(t *testing.T) {
	f := newDiscoveryFixture(t)
	f.inventory.Nodes, f.snapshot.Nodes, f.paths.Paths = nil, nil, nil
	f.inventory.ObservedAt = timestamppb.New(f.now.Add(-25 * time.Second))
	response, err := f.discovery.QueryPlacementCandidates(context.Background(), f.query)
	if err != nil || !response.GetComplete() || len(response.GetCandidates()) != 0 ||
		!response.GetObservedAt().AsTime().Equal(f.now) || !response.GetExpiresAt().AsTime().Equal(f.now.Add(5*time.Second)) {
		t.Fatalf("empty pool lost membership expiry: %+v, %v", response, err)
	}
}

func TestPlacementDiscoveryRejectsUnboundAuthorityBeforeInventory(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*discoveryFixture)
		code   codes.Code
	}{
		{"invalid_query", func(f *discoveryFixture) { f.query.PolicyDigest = "invalid" }, codes.InvalidArgument},
		{"policy_revision", func(f *discoveryFixture) { f.query.PolicyRevision++ }, codes.FailedPrecondition},
		{"parent_revision", func(f *discoveryFixture) { f.query.ParentRevision++ }, codes.FailedPrecondition},
		{"omit_cluster", func(f *discoveryFixture) { f.query.ClusterIds = []string{"us"} }, codes.FailedPrecondition},
		{"other_cell", func(f *discoveryFixture) { f.discovery.CellID = "eu-cell" }, codes.FailedPrecondition},
		{"not_ready", func(f *discoveryFixture) { f.pair.Object.Ready = false }, codes.FailedPrecondition},
		{"legacy", func(f *discoveryFixture) { f.pair.Tenant.Authority.SchemaVersion = 1 }, codes.FailedPrecondition},
		{"denied", func(f *discoveryFixture) {
			f.pair.Tenant.Authority.BillingDecision = mediaauthoritypb.TenantBillingDecision_TENANT_BILLING_DECISION_PAYMENT_REQUIRED
		}, codes.PermissionDenied},
		{"missing_dependency", func(f *discoveryFixture) { f.discovery.Paths = nil }, codes.Unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDiscoveryFixture(t)
			tc.change(f)
			got, err := f.discovery.QueryPlacementCandidates(context.Background(), f.query)
			if got != nil || status.Code(err) != tc.code || len(f.calls) > 1 {
				t.Fatalf("unbound query reached inventory: %+v, %v, %v", got, err, f.calls)
			}
		})
	}
}

func TestPlacementDiscoveryRejectsStaleOrForeignPathEvidence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*discoveryFixture)
	}{
		{"generation", func(f *discoveryFixture) { f.paths.SourceGeneration = "other-generation" }},
		{"expired", func(f *discoveryFixture) { f.paths.ExpiresAt = f.now }},
		{"future", func(f *discoveryFixture) { f.paths.ObservedAt = f.now.Add(time.Nanosecond) }},
		{"missing_stamp", func(f *discoveryFixture) { f.paths.ObservedAt = time.Time{} }},
		{"unbounded_lifetime", func(f *discoveryFixture) { f.paths.ExpiresAt = f.now.Add(time.Minute) }},
		{"foreign_node", func(f *discoveryFixture) {
			delete(f.paths.Paths, "node-00")
			f.paths.Paths["foreign"] = balancer.PlacementNodePath{}
		}},
		{"oversized_paths", func(f *discoveryFixture) { f.paths.Paths["foreign"] = balancer.PlacementNodePath{} }},
		{"expired_during_paths", func(f *discoveryFixture) {
			reader := f.discovery.Paths
			f.discovery.Paths = placementPathReaderFunc(func(ctx context.Context, a balancer.PlacementAuthority, q *placementpb.CandidateQuery, s *state.BalancerSnapshot) (PlacementPathObservation, error) {
				paths, err := reader.ObservePlacementPaths(ctx, a, q, s)
				f.now = f.pair.Object.ValidUntil
				return paths, err
			})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDiscoveryFixture(t)
			tc.change(f)
			if got, err := f.discovery.QueryPlacementCandidates(context.Background(), f.query); got != nil || status.Code(err) != codes.Unavailable {
				t.Fatalf("invalid evidence accepted: %+v, %v", got, err)
			}
		})
	}
}

func TestPlacementDiscoverySkewMissingPathsAndCancellation(t *testing.T) {
	f := newDiscoveryFixture(t)
	extra := f.snapshot.Nodes[0]
	extra.NodeID = "unregistered"
	f.snapshot.Nodes = append(f.snapshot.Nodes, extra)
	delete(f.paths.Paths, "node-00")
	response, err := f.discovery.QueryPlacementCandidates(context.Background(), f.query)
	if err != nil || response.GetComplete() || len(response.GetCandidates()) != 12 || response.Candidates[0].Capacity != placementpb.Capacity_CAPACITY_UNKNOWN {
		t.Fatalf("skew or missing protocol became complete/healthy: %+v, %v", response, err)
	}
	f.calls = nil
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := f.discovery.QueryPlacementCandidates(ctx, f.query); status.Code(err) != codes.Canceled || len(f.calls) != 0 {
		t.Fatalf("cancelled discovery performed I/O: %v, %v", err, f.calls)
	}
	f.discovery.Authority = placementAuthorityReaderFunc(func(context.Context, string, string, string) (localauthority.PlacementPair, error) {
		return localauthority.PlacementPair{}, errors.New("secret database path")
	})
	if _, err := f.discovery.QueryPlacementCandidates(context.Background(), f.query); status.Code(err) != codes.Unavailable || strings.Contains(err.Error(), "secret") {
		t.Fatalf("dependency detail leaked: %v", err)
	}
}

func TestPlacementDiscoveryCancellationStopsBetweenDependencies(t *testing.T) {
	for _, stage := range []string{"authority", "inventory", "paths"} {
		t.Run(stage, func(t *testing.T) {
			f := newDiscoveryFixture(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch stage {
			case "authority":
				reader := f.discovery.Authority
				f.discovery.Authority = placementAuthorityReaderFunc(func(ctx context.Context, tenant, object, internal string) (localauthority.PlacementPair, error) {
					pair, err := reader.Placement(ctx, tenant, object, internal)
					cancel()
					return pair, err
				})
			case "inventory":
				reader := f.discovery.Inventory
				f.discovery.Inventory = placementInventoryReaderFunc(func(ctx context.Context, req *quartermasterpb.GetMediaPlacementInventoryRequest) (*quartermasterpb.MediaPlacementInventory, error) {
					inventory, err := reader.GetMediaPlacementInventory(ctx, req)
					cancel()
					return inventory, err
				})
			case "paths":
				reader := f.discovery.Paths
				f.discovery.Paths = placementPathReaderFunc(func(ctx context.Context, a balancer.PlacementAuthority, q *placementpb.CandidateQuery, s *state.BalancerSnapshot) (PlacementPathObservation, error) {
					paths, err := reader.ObservePlacementPaths(ctx, a, q, s)
					cancel()
					return paths, err
				})
			}
			if _, err := f.discovery.QueryPlacementCandidates(ctx, f.query); status.Code(err) != codes.Canceled || f.calls[len(f.calls)-1] != stage {
				t.Fatalf("cancelled dependency chain continued: %v, calls=%v", err, f.calls)
			}
		})
	}
}
