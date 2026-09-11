package grpc

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"frameworks/api_control/internal/placementpolicy"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	clusterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	placementpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_placement"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func placementOptionsFixture() (placementpolicy.Scope, *placementpb.GetOptionsRequest, *quartermasterpb.GetTenantEntitlementResponse) {
	scope := placementpolicy.Scope{TenantID: "10000000-0000-4000-8000-000000000071", Kind: "tenant", ID: "10000000-0000-4000-8000-000000000071"}
	req := &placementpb.GetOptionsRequest{Scope: &placementpb.Scope{Kind: placementpb.ScopeKind_SCOPE_KIND_TENANT}, First: 100}
	peer := func(id, class, owner, region string) *clusterpb.TenantClusterPeer {
		return &clusterpb.TenantClusterPeer{ClusterId: id, ClusterName: "Cluster " + id, ClusterClass: class, OwnerTenantId: owner, RegionId: region, ClusterType: "edge", AccessActive: true, SubscriptionStatus: "active", AccessSource: clusterpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_MARKETPLACE_SUBSCRIPTION,
			FoghornGrpcAddr: "private-grpc-address", S3Endpoint: "private-storage-address", MediaConsent: &placementpb.CapacityConsent{AllowIngest: true, AllowServe: true}}
	}
	peers := []*clusterpb.TenantClusterPeer{peer("official", "platform_official", "", "eu"), peer("owned", "third_party_marketplace", scope.TenantID, "eu"), peer("market", "third_party_marketplace", "operator", "us")}
	return scope, req, &quartermasterpb.GetTenantEntitlementResponse{AllowedClusterIds: []string{"official", "owned", "market"}, EffectiveAccess: peers}
}

func TestMediaPlacementOptionsProjectionAndRedaction(t *testing.T) {
	scope, req, entitlement := placementOptionsFixture()
	before := proto.CloneOf(entitlement)
	response, err := projectPlacementOptions(scope, req, entitlement, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(response.GetNodes()) != 7 || !proto.Equal(entitlement, before) {
		t.Fatal("selector projection lost targets or mutated entitlement")
	}
	for _, node := range response.GetNodes() {
		if node.GetKind() == placementpb.OptionKind_OPTION_KIND_CLUSTER && node.GetId() == "owned" && node.GetClusterClass() != placementpb.ClusterClass_CLUSTER_CLASS_TENANT_PRIVATE {
			t.Fatal("owner's marketplace cluster was not private relative to its owner")
		}
		if node.GetKind() == placementpb.OptionKind_OPTION_KIND_REGION && node.GetId() == "eu" && node.GetClusterClass() != placementpb.ClusterClass_CLUSTER_CLASS_UNSPECIFIED {
			t.Fatal("mixed region invented a single class")
		}
	}
	body, err := json.Marshal(response)
	if err != nil || strings.Contains(string(body), "private-grpc-address") || strings.Contains(string(body), "private-storage-address") {
		t.Fatal("selector catalogue exposed internal addresses")
	}
	req.Filter = &placementpb.OptionsFilter{Kind: placementpb.OptionKind_OPTION_KIND_CLUSTER, Classes: []placementpb.ClusterClass{placementpb.ClusterClass_CLUSTER_CLASS_TENANT_PRIVATE}, Query: " OWNED "}
	filtered, err := projectPlacementOptions(scope, req, entitlement, time.Now())
	if err != nil || len(filtered.GetNodes()) != 1 || filtered.GetNodes()[0].GetId() != "owned" {
		t.Fatalf("tenant-relative class/search filter failed: %v", err)
	}
	entitlement.EffectiveAccess[1].MediaConsent = nil
	filtered, err = projectPlacementOptions(scope, req, entitlement, time.Now())
	if err != nil || filtered.GetNodes()[0].GetEligible() || filtered.GetNodes()[0].GetReason() != "owner_consent_unknown" {
		t.Fatalf("missing consent became media permission: %v", err)
	}
}

func TestMediaPlacementOptionsPaginationIsScopedAndStable(t *testing.T) {
	scope, req, entitlement := placementOptionsFixture()
	req.First = 2
	first, err := projectPlacementOptions(scope, req, entitlement, time.Now())
	if err != nil || !first.GetHasNextPage() || first.GetHasPreviousPage() || len(first.GetNodes()) != 2 {
		t.Fatalf("first page failed: %v", err)
	}
	req.After = first.GetEndCursor()
	slices.Reverse(entitlement.EffectiveAccess)
	slices.Reverse(entitlement.AllowedClusterIds)
	next, err := projectPlacementOptions(scope, req, entitlement, time.Now())
	if err != nil || !next.GetHasPreviousPage() || len(next.GetNodes()) != 2 || next.GetNodes()[0].GetId() == first.GetNodes()[0].GetId() {
		t.Fatalf("stable second page failed: %v", err)
	}
	for _, scenario := range []string{"tenant", "scope", "filter", "revocation", "malformed"} {
		t.Run(scenario, func(t *testing.T) {
			otherScope, otherReq, changed := scope, proto.CloneOf(req), proto.CloneOf(entitlement)
			want := codes.InvalidArgument
			switch scenario {
			case "tenant":
				otherScope.TenantID = "10000000-0000-4000-8000-000000000072"
			case "scope":
				otherScope.Kind, otherScope.ID = "stream", "30000000-0000-4000-8000-000000000071"
			case "filter":
				otherReq.Filter = &placementpb.OptionsFilter{Kind: placementpb.OptionKind_OPTION_KIND_REGION}
			case "revocation":
				changed.EffectiveAccess = changed.EffectiveAccess[:2]
				changed.AllowedClusterIds = changed.AllowedClusterIds[:2]
				want = codes.Aborted
			case "malformed":
				otherReq.After = "invalid"
			}
			if _, err := projectPlacementOptions(otherScope, otherReq, changed, time.Now()); status.Code(err) != want {
				t.Fatalf("cursor accepted changed scope/catalogue: %v", err)
			}
		})
	}
}

func TestMediaPlacementOptionsRejectsIncompleteEntitlement(t *testing.T) {
	for _, scenario := range []string{"nil", "missing", "duplicate", "extra", "inactive", "pending", "unknown source", "expired", "unknown class", "unknown owner"} {
		t.Run(scenario, func(t *testing.T) {
			scope, req, entitlement := placementOptionsFixture()
			switch scenario {
			case "nil":
				entitlement = nil
			case "missing":
				entitlement.EffectiveAccess = entitlement.EffectiveAccess[:2]
			case "duplicate":
				entitlement.EffectiveAccess = append(entitlement.EffectiveAccess, entitlement.EffectiveAccess[0])
			case "extra":
				entitlement.EffectiveAccess[0].ClusterId = "unauthorized"
			case "inactive":
				entitlement.EffectiveAccess[0].AccessActive = false
			case "pending":
				entitlement.EffectiveAccess[0].SubscriptionStatus = "pending"
			case "unknown source":
				entitlement.EffectiveAccess[0].AccessSource = 999
			case "expired":
				entitlement.EffectiveAccess[0].AccessExpiresAt = timestamppb.New(time.Now().Add(-time.Second))
			case "unknown class":
				entitlement.EffectiveAccess[0].ClusterClass = "unknown"
			case "unknown owner":
				entitlement.EffectiveAccess[2].OwnerTenantId = ""
			}
			if _, err := projectPlacementOptions(scope, req, entitlement, time.Now()); status.Code(err) != codes.Unavailable {
				t.Fatalf("incomplete grant became a usable selector: %v", err)
			}
		})
	}
	scope, req, _ := placementOptionsFixture()
	result, err := projectPlacementOptions(scope, req, &quartermasterpb.GetTenantEntitlementResponse{}, time.Now())
	if err != nil || len(result.GetNodes()) != 0 || result.GetHasNextPage() {
		t.Fatalf("explicit empty entitlement failed: %v", err)
	}
}

func TestMediaPlacementOptionsRPCRequiresScopedIdentity(t *testing.T) {
	server := &CommodoreServer{}
	_, req, _ := placementOptionsFixture()
	if _, err := server.GetMediaPlacementOptions(context.Background(), req); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("anonymous options reached owner: %v", err)
	}
	ctx := ctxAs("20000000-0000-4000-8000-000000000071", "10000000-0000-4000-8000-000000000071", "viewer")
	ctx = context.WithValue(ctx, ctxkeys.KeyAuthType, "api_token")
	if _, err := server.GetMediaPlacementOptions(ctx, req); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("unscoped token read options: %v", err)
	}
}
