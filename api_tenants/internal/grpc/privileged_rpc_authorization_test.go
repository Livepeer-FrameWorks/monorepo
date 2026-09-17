package grpc

import (
	"context"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestPrivilegedRPCsRejectTenantJWTBeforeStorage(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "jwt")
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, "tenant-a")
	ctx = context.WithValue(ctx, ctxkeys.KeyRole, "owner")
	s := &QuartermasterServer{}

	tests := []struct {
		name string
		call func() error
	}{
		{"CreateBootstrapToken", func() error {
			_, err := s.CreateBootstrapToken(ctx, &quartermasterpb.CreateBootstrapTokenRequest{})
			return err
		}},
		{"ListBootstrapTokens", func() error {
			_, err := s.ListBootstrapTokens(ctx, &quartermasterpb.ListBootstrapTokensRequest{})
			return err
		}},
		{"RevokeBootstrapToken", func() error {
			_, err := s.RevokeBootstrapToken(ctx, &quartermasterpb.RevokeBootstrapTokenRequest{})
			return err
		}},
		{"CreateNode", func() error { _, err := s.CreateNode(ctx, &quartermasterpb.CreateNodeRequest{}); return err }},
		{"ReportAliveNodes", func() error {
			_, err := s.ReportAliveNodes(ctx, &quartermasterpb.ReportAliveNodesRequest{})
			return err
		}},
		{"ResolveNodeFingerprint", func() error {
			_, err := s.ResolveNodeFingerprint(ctx, &quartermasterpb.ResolveNodeFingerprintRequest{})
			return err
		}},
		{"ListPeers", func() error { _, err := s.ListPeers(ctx, &quartermasterpb.ListPeersRequest{}); return err }},
		{"UpdateNodeHardware", func() error {
			_, err := s.UpdateNodeHardware(ctx, &quartermasterpb.UpdateNodeHardwareRequest{})
			return err
		}},
		{"GetServicePoolStatus", func() error {
			_, err := s.GetServicePoolStatus(ctx, &quartermasterpb.GetServicePoolStatusRequest{})
			return err
		}},
		{"AddToServicePool", func() error {
			_, err := s.AddToServicePool(ctx, &quartermasterpb.AddToServicePoolRequest{})
			return err
		}},
		{"ReassignClusterControlCell", func() error {
			_, err := s.ReassignClusterControlCell(ctx, &quartermasterpb.ReassignClusterControlCellRequest{})
			return err
		}},
		{"GetClusterControlCellReassignment", func() error {
			_, err := s.GetClusterControlCellReassignment(ctx, &quartermasterpb.GetClusterControlCellReassignmentRequest{})
			return err
		}},
		{"DrainServiceInstance", func() error {
			_, err := s.DrainServiceInstance(ctx, &quartermasterpb.DrainServiceInstanceRequest{})
			return err
		}},
		{"AssignServiceToCluster", func() error {
			_, err := s.AssignServiceToCluster(ctx, &quartermasterpb.AssignServiceToClusterRequest{})
			return err
		}},
		{"UnassignServiceFromCluster", func() error {
			_, err := s.UnassignServiceFromCluster(ctx, &quartermasterpb.UnassignServiceFromClusterRequest{})
			return err
		}},
		{"BootstrapService", func() error {
			_, err := s.BootstrapService(ctx, &quartermasterpb.BootstrapServiceRequest{})
			return err
		}},
		{"ValidateBootstrapToken", func() error {
			_, err := s.ValidateBootstrapToken(ctx, &quartermasterpb.ValidateBootstrapTokenRequest{})
			return err
		}},
		{"SyncMesh", func() error { _, err := s.SyncMesh(ctx, &quartermasterpb.InfrastructureSyncRequest{}); return err }},
		{"SetNodeEnrollmentOrigin", func() error {
			_, err := s.SetNodeEnrollmentOrigin(ctx, &quartermasterpb.SetNodeEnrollmentOriginRequest{})
			return err
		}},
		{"ListServices", func() error { _, err := s.ListServices(ctx, &quartermasterpb.ListServicesRequest{}); return err }},
		{"ListServicesHealth", func() error {
			_, err := s.ListServicesHealth(ctx, &quartermasterpb.ListServicesHealthRequest{})
			return err
		}},
		{"GetServiceHealth", func() error {
			_, err := s.GetServiceHealth(ctx, &quartermasterpb.GetServiceHealthRequest{})
			return err
		}},
		{"UpsertTLSBundle", func() error { _, err := s.UpsertTLSBundle(ctx, &quartermasterpb.UpsertTLSBundleRequest{}); return err }},
		{"ListTLSBundles", func() error { _, err := s.ListTLSBundles(ctx, &quartermasterpb.ListTLSBundlesRequest{}); return err }},
		{"UpsertIngressSite", func() error {
			_, err := s.UpsertIngressSite(ctx, &quartermasterpb.UpsertIngressSiteRequest{})
			return err
		}},
		{"ListIngressSites", func() error {
			_, err := s.ListIngressSites(ctx, &quartermasterpb.ListIngressSitesRequest{})
			return err
		}},
		{"ListActiveTenants", func() error {
			_, err := s.ListActiveTenants(ctx, &quartermasterpb.ListActiveTenantsRequest{})
			return err
		}},
		{"CreateTenant", func() error { _, err := s.CreateTenant(ctx, &quartermasterpb.CreateTenantRequest{}); return err }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(); status.Code(err) != codes.PermissionDenied {
				t.Fatalf("code = %s, want PermissionDenied (err=%v)", status.Code(err), err)
			}
		})
	}
}

func TestProviderDelegatedAPITokenRequiresScopeBeforeStorage(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "api_token")
	ctx = context.WithValue(ctx, ctxkeys.KeyTenantID, "00000000-0000-0000-0000-000000000001")
	ctx = context.WithValue(ctx, ctxkeys.KeyRole, "provider")
	s := &QuartermasterServer{}

	tests := []struct {
		name string
		call func() error
	}{
		{"CreateEnrollmentToken", func() error {
			_, err := s.CreateEnrollmentToken(ctx, &quartermasterpb.CreateEnrollmentTokenRequest{ClusterId: "cluster-1"})
			return err
		}},
		{"UpdateNodeStatus", func() error {
			_, err := s.UpdateNodeStatus(ctx, &quartermasterpb.UpdateNodeStatusRequest{NodeId: "edge-1", Status: "retired"})
			return err
		}},
		{"UpsertEdgeRelease", func() error {
			_, err := s.UpsertEdgeRelease(ctx, &quartermasterpb.UpsertEdgeReleaseRequest{})
			return err
		}},
		{"GetClusterReleaseTarget", func() error {
			_, err := s.GetClusterReleaseTarget(ctx, &quartermasterpb.GetClusterReleaseTargetRequest{ClusterId: "cluster-1"})
			return err
		}},
		{"ListClusterReleaseTargets", func() error {
			_, err := s.ListClusterReleaseTargets(ctx, &quartermasterpb.ListClusterReleaseTargetsRequest{})
			return err
		}},
		{"SetClusterReleaseTarget", func() error {
			_, err := s.SetClusterReleaseTarget(ctx, &quartermasterpb.SetClusterReleaseTargetRequest{
				Target: &quartermasterpb.ClusterReleaseTarget{ClusterId: "cluster-1"},
			})
			return err
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(); status.Code(err) != codes.PermissionDenied {
				t.Fatalf("code = %s, want PermissionDenied (err=%v)", status.Code(err), err)
			}
		})
	}
}
