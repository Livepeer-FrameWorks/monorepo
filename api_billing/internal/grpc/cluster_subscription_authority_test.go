package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	clusterpeerpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/cluster_peer"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	tenantlimitspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/tenant_limits"
)

func TestTenantAdmissionQueryTimeoutLeavesMediaCallerHeadroom(t *testing.T) {
	const mediaAdmissionBudget = 500 * time.Millisecond
	if tenantAdmissionQueryTimeout >= mediaAdmissionBudget {
		t.Fatalf("Purser query timeout %v consumes the entire %v media admission budget", tenantAdmissionQueryTimeout, mediaAdmissionBudget)
	}
	if headroom := mediaAdmissionBudget - tenantAdmissionQueryTimeout; headroom < 100*time.Millisecond {
		t.Fatalf("Purser query timeout leaves only %v transport/orchestration headroom", headroom)
	}
}

type commercialQuartermasterStub struct {
	cluster      *quartermasterpb.InfrastructureCluster
	materialized *quartermasterpb.MaterializeClusterAccessRequest
	bootstrapped bool
}

func (s *commercialQuartermasterStub) GetCluster(context.Context, string) (*quartermasterpb.ClusterResponse, error) {
	return &quartermasterpb.ClusterResponse{Cluster: s.cluster}, nil
}

func (*commercialQuartermasterStub) ListClustersByOwner(context.Context, string, *commonpb.CursorPaginationRequest) (*quartermasterpb.ListClustersResponse, error) {
	return &quartermasterpb.ListClustersResponse{}, nil
}

func (s *commercialQuartermasterStub) BootstrapClusterAccess(context.Context, string, string, *tenantlimitspb.TenantResourceLimits) error {
	s.bootstrapped = true
	return nil
}

func (s *commercialQuartermasterStub) MaterializeClusterAccess(_ context.Context, req *quartermasterpb.MaterializeClusterAccessRequest) error {
	s.materialized = req
	return nil
}

func (*commercialQuartermasterStub) RevokeMaterializedClusterAccess(context.Context, *quartermasterpb.RevokeMaterializedClusterAccessRequest) error {
	return nil
}

func TestCreateClusterSubscriptionOwnerBypassesMarketplaceBilling(t *testing.T) {
	tenantID := "11111111-1111-1111-1111-111111111111"
	stub := &commercialQuartermasterStub{cluster: &quartermasterpb.InfrastructureCluster{
		ClusterId: "private-a", OwnerTenantId: &tenantID,
	}}
	server := &PurserServer{logger: logging.NewLogger(), quartermasterClient: stub}

	resp, err := server.CreateClusterSubscription(context.Background(), &purserpb.CreateClusterSubscriptionRequest{
		TenantId: tenantID, ClusterId: "private-a",
	})
	if err != nil {
		t.Fatalf("CreateClusterSubscription: %v", err)
	}
	if resp.GetStatus() != "active" {
		t.Fatalf("status = %q, want active", resp.GetStatus())
	}
	if stub.materialized == nil || stub.materialized.GetAccessSource() != clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_OWNER {
		t.Fatalf("owner materialization = %+v", stub.materialized)
	}
	if stub.bootstrapped {
		t.Fatal("owner cluster used platform-tier bootstrap")
	}
}

func TestGrantClusterAccessForKindUsesDistinctAuthorities(t *testing.T) {
	for _, tc := range []struct {
		name      string
		kind      commercialClusterKind
		bootstrap bool
		source    clusterpeerpb.TenantClusterAccessSource
	}{
		{name: "platform tier", kind: commercialKindPlatformOfficial, bootstrap: true},
		{name: "owner", kind: commercialKindTenantPrivate, source: clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_OWNER},
		{name: "marketplace", kind: commercialKindThirdParty, source: clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_MARKETPLACE_SUBSCRIPTION},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &commercialQuartermasterStub{}
			server := &PurserServer{quartermasterClient: stub}
			if err := server.grantClusterAccessForKind(context.Background(), "tenant", "cluster", tc.kind); err != nil {
				t.Fatalf("grantClusterAccessForKind: %v", err)
			}
			if stub.bootstrapped != tc.bootstrap {
				t.Fatalf("bootstrapped = %v, want %v", stub.bootstrapped, tc.bootstrap)
			}
			if tc.bootstrap {
				if stub.materialized != nil {
					t.Fatalf("platform tier unexpectedly materialized: %+v", stub.materialized)
				}
				return
			}
			if stub.materialized == nil || stub.materialized.GetAccessSource() != tc.source {
				t.Fatalf("materialized source = %v, want %v", stub.materialized.GetAccessSource(), tc.source)
			}
		})
	}
}

func TestMarketplaceApprovalRequestIsPendingAndNonAuthorizing(t *testing.T) {
	stub := &commercialQuartermasterStub{}
	server := &PurserServer{quartermasterClient: stub}
	if err := server.requestMarketplaceApproval(context.Background(), "tenant", "cluster"); err != nil {
		t.Fatalf("requestMarketplaceApproval: %v", err)
	}
	if stub.materialized == nil {
		t.Fatal("marketplace approval request was not persisted")
	}
	if got := stub.materialized.GetSubscriptionStatus(); got != "pending_approval" {
		t.Fatalf("subscription status = %q, want pending_approval", got)
	}
	if got := stub.materialized.GetAccessSource(); got != clusterpeerpb.TenantClusterAccessSource_TENANT_CLUSTER_ACCESS_SOURCE_MARKETPLACE_SUBSCRIPTION {
		t.Fatalf("access source = %v, want marketplace", got)
	}
}

func TestMarketplaceApprovalPolicy(t *testing.T) {
	for _, tc := range []struct {
		name             string
		requiresApproval bool
		pricingModel     string
		wantPending      bool
		wantError        bool
	}{
		{name: "custom", pricingModel: "custom", wantPending: true},
		{name: "free owner approval", requiresApproval: true, pricingModel: "free_unmetered", wantPending: true},
		{name: "metered owner approval", requiresApproval: true, pricingModel: "metered", wantPending: true},
		{name: "monthly checkout", pricingModel: "monthly"},
		{name: "monthly plus approval fails closed", requiresApproval: true, pricingModel: "monthly", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pending, err := marketplaceApprovalRequired(tc.requiresApproval, tc.pricingModel)
			if (err != nil) != tc.wantError {
				t.Fatalf("error = %v, wantError %v", err, tc.wantError)
			}
			if pending != tc.wantPending {
				t.Fatalf("pending = %v, want %v", pending, tc.wantPending)
			}
		})
	}
}
