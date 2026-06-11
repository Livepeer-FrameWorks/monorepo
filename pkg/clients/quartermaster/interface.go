package quartermaster

import (
	"context"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	tenantlimitspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/tenant_limits"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
)

// Interface is the full method surface of the concrete client, extracted so
// that api_gateway can inject fakes for resolver real-path tests. The concrete
// client satisfies it (asserted below).
type Interface interface {
	Conn() *grpc.ClientConn
	Close() error
	CheckHealth(ctx context.Context) error
	ValidateTenant(ctx context.Context, tenantID, userID string) (*quartermasterpb.ValidateTenantResponse, error)
	GetTenant(ctx context.Context, tenantID string) (*quartermasterpb.GetTenantResponse, error)
	GetClusterRouting(ctx context.Context, req *quartermasterpb.GetClusterRoutingRequest) (*quartermasterpb.ClusterRoutingResponse, error)
	ResolveTenantAliases(ctx context.Context, aliases []string) (*quartermasterpb.ResolveTenantAliasesResponse, error)
	BootstrapClusterAccess(ctx context.Context, tenantID, clusterID string, resourceLimits *tenantlimitspb.TenantResourceLimits) error
	DeactivateClusterAccess(ctx context.Context, tenantID, clusterID, reason string) error
	ListTenantClusterAccess(ctx context.Context, tenantID string) (*quartermasterpb.ListTenantClusterAccessResponse, error)
	ListTenants(ctx context.Context, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ListTenantsResponse, error)
	GetTenantsByCluster(ctx context.Context, clusterID string, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.GetTenantsByClusterResponse, error)
	CreateTenant(ctx context.Context, req *quartermasterpb.CreateTenantRequest) (*quartermasterpb.CreateTenantResponse, error)
	UpdateTenant(ctx context.Context, req *quartermasterpb.UpdateTenantRequest) (*quartermasterpb.Tenant, error)
	ResolveTenant(ctx context.Context, req *quartermasterpb.ResolveTenantRequest) (*quartermasterpb.ResolveTenantResponse, error)
	ListActiveTenants(ctx context.Context) ([]string, error)
	GetCluster(ctx context.Context, clusterID string) (*quartermasterpb.ClusterResponse, error)
	ListAliasedTenantsForCluster(ctx context.Context, clusterID string) (*quartermasterpb.ListAliasedTenantsForClusterResponse, error)
	ListClusters(ctx context.Context, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ListClustersResponse, error)
	ListClustersByOwner(ctx context.Context, ownerTenantID string, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ListClustersResponse, error)
	ListOfficialClusters(ctx context.Context) (*quartermasterpb.ListClustersResponse, error)
	ListPublicTopologyClusters(ctx context.Context) (*quartermasterpb.ListClustersResponse, error)
	CreateCluster(ctx context.Context, req *quartermasterpb.CreateClusterRequest) (*quartermasterpb.ClusterResponse, error)
	UpdateCluster(ctx context.Context, req *quartermasterpb.UpdateClusterRequest) (*quartermasterpb.ClusterResponse, error)
	UpdateClusterMeshConfig(ctx context.Context, req *quartermasterpb.UpdateClusterMeshConfigRequest) (*quartermasterpb.UpdateClusterMeshConfigResponse, error)
	SetNodeEnrollmentOrigin(ctx context.Context, req *quartermasterpb.SetNodeEnrollmentOriginRequest) (*quartermasterpb.SetNodeEnrollmentOriginResponse, error)
	ListClustersForTenant(ctx context.Context, tenantID string, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ClustersAccessResponse, error)
	ListClustersAvailable(ctx context.Context, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ClustersAvailableResponse, error)
	GrantClusterAccess(ctx context.Context, req *quartermasterpb.GrantClusterAccessRequest) error
	SubscribeToCluster(ctx context.Context, req *quartermasterpb.SubscribeToClusterRequest) (*emptypb.Empty, error)
	UnsubscribeFromCluster(ctx context.Context, req *quartermasterpb.UnsubscribeFromClusterRequest) (*emptypb.Empty, error)
	ListMySubscriptions(ctx context.Context, req *quartermasterpb.ListMySubscriptionsRequest) (*quartermasterpb.ListClustersResponse, error)
	UpsertTLSBundle(ctx context.Context, bundle *quartermasterpb.TLSBundle) (*quartermasterpb.TLSBundleResponse, error)
	ListTLSBundles(ctx context.Context, clusterID string, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ListTLSBundlesResponse, error)
	UpsertIngressSite(ctx context.Context, site *quartermasterpb.IngressSite) (*quartermasterpb.IngressSiteResponse, error)
	ListIngressSites(ctx context.Context, clusterID, nodeID string, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ListIngressSitesResponse, error)
	ListMarketplaceClusters(ctx context.Context, req *quartermasterpb.ListMarketplaceClustersRequest) (*quartermasterpb.ListMarketplaceClustersResponse, error)
	GetMarketplaceCluster(ctx context.Context, req *quartermasterpb.GetMarketplaceClusterRequest) (*quartermasterpb.MarketplaceClusterEntry, error)
	UpdateClusterMarketplace(ctx context.Context, req *quartermasterpb.UpdateClusterMarketplaceRequest) (*quartermasterpb.ClusterResponse, error)
	GetClusterMetadataBatch(ctx context.Context, req *quartermasterpb.GetClusterMetadataBatchRequest) (*quartermasterpb.GetClusterMetadataBatchResponse, error)
	CreatePrivateCluster(ctx context.Context, req *quartermasterpb.CreatePrivateClusterRequest) (*quartermasterpb.CreatePrivateClusterResponse, error)
	CreateClusterInvite(ctx context.Context, req *quartermasterpb.CreateClusterInviteRequest) (*quartermasterpb.ClusterInvite, error)
	RevokeClusterInvite(ctx context.Context, req *quartermasterpb.RevokeClusterInviteRequest) error
	ListClusterInvites(ctx context.Context, req *quartermasterpb.ListClusterInvitesRequest) (*quartermasterpb.ListClusterInvitesResponse, error)
	ListMyClusterInvites(ctx context.Context, req *quartermasterpb.ListMyClusterInvitesRequest) (*quartermasterpb.ListClusterInvitesResponse, error)
	RequestClusterSubscription(ctx context.Context, req *quartermasterpb.RequestClusterSubscriptionRequest) (*quartermasterpb.ClusterSubscription, error)
	AcceptClusterInvite(ctx context.Context, req *quartermasterpb.AcceptClusterInviteRequest) (*quartermasterpb.ClusterSubscription, error)
	ListPendingSubscriptions(ctx context.Context, req *quartermasterpb.ListPendingSubscriptionsRequest) (*quartermasterpb.ListPendingSubscriptionsResponse, error)
	ApproveClusterSubscription(ctx context.Context, req *quartermasterpb.ApproveClusterSubscriptionRequest) (*quartermasterpb.ClusterSubscription, error)
	RejectClusterSubscription(ctx context.Context, req *quartermasterpb.RejectClusterSubscriptionRequest) (*quartermasterpb.ClusterSubscription, error)
	UpdateTenantCluster(ctx context.Context, req *quartermasterpb.UpdateTenantClusterRequest) error
	ListPeers(ctx context.Context, clusterID string) (*quartermasterpb.ListPeersResponse, error)
	GetNode(ctx context.Context, nodeID string) (*quartermasterpb.NodeResponse, error)
	ListNodes(ctx context.Context, clusterID, nodeType, region string, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ListNodesResponse, error)
	ListHealthyNodesForDNS(ctx context.Context, staleThresholdSeconds int, serviceType string) (*quartermasterpb.ListHealthyNodesForDNSResponse, error)
	ListHealthyNodesForDNSForCluster(ctx context.Context, staleThresholdSeconds int, serviceType, clusterID string) (*quartermasterpb.ListHealthyNodesForDNSResponse, error)
	CreateNode(ctx context.Context, req *quartermasterpb.CreateNodeRequest) (*quartermasterpb.NodeResponse, error)
	UpdateNodeStatus(ctx context.Context, req *quartermasterpb.UpdateNodeStatusRequest) (*quartermasterpb.NodeResponse, error)
	ResolveNodeFingerprint(ctx context.Context, req *quartermasterpb.ResolveNodeFingerprintRequest) (*quartermasterpb.ResolveNodeFingerprintResponse, error)
	GetNodeOwner(ctx context.Context, nodeID string) (*quartermasterpb.NodeOwnerResponse, error)
	GetNodeByLogicalName(ctx context.Context, nodeID string) (*quartermasterpb.InfrastructureNode, error)
	UpdateNodeHardware(ctx context.Context, req *quartermasterpb.UpdateNodeHardwareRequest) error
	ReportAliveNodes(ctx context.Context, nodes []*quartermasterpb.NodeAliveness) error
	BootstrapEdgeNode(ctx context.Context, req *quartermasterpb.BootstrapEdgeNodeRequest) (*quartermasterpb.BootstrapEdgeNodeResponse, error)
	BootstrapInfrastructureNode(ctx context.Context, req *quartermasterpb.BootstrapInfrastructureNodeRequest) (*quartermasterpb.BootstrapInfrastructureNodeResponse, error)
	BootstrapService(ctx context.Context, req *quartermasterpb.BootstrapServiceRequest) (*quartermasterpb.BootstrapServiceResponse, error)
	DiscoverServices(ctx context.Context, serviceType, clusterID string, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ServiceDiscoveryResponse, error)
	CreateBootstrapToken(ctx context.Context, req *quartermasterpb.CreateBootstrapTokenRequest) (*quartermasterpb.CreateBootstrapTokenResponse, error)
	ListBootstrapTokens(ctx context.Context, kind, tenantID string, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ListBootstrapTokensResponse, error)
	RevokeBootstrapToken(ctx context.Context, tokenID string) error
	ValidateBootstrapToken(ctx context.Context, token string) (*quartermasterpb.ValidateBootstrapTokenResponse, error)
	ValidateBootstrapTokenEx(ctx context.Context, req *quartermasterpb.ValidateBootstrapTokenRequest) (*quartermasterpb.ValidateBootstrapTokenResponse, error)
	GetServicePoolStatus(ctx context.Context, serviceType string) (*quartermasterpb.GetServicePoolStatusResponse, error)
	AddToServicePool(ctx context.Context, req *quartermasterpb.AddToServicePoolRequest) (*quartermasterpb.AddToServicePoolResponse, error)
	DrainServiceInstance(ctx context.Context, req *quartermasterpb.DrainServiceInstanceRequest) (*quartermasterpb.DrainServiceInstanceResponse, error)
	AssignServiceToCluster(ctx context.Context, req *quartermasterpb.AssignServiceToClusterRequest) error
	UnassignServiceFromCluster(ctx context.Context, req *quartermasterpb.UnassignServiceFromClusterRequest) error
	EnableSelfHosting(ctx context.Context, req *quartermasterpb.EnableSelfHostingRequest) (*quartermasterpb.EnableSelfHostingResponse, error)
	CreateEnrollmentToken(ctx context.Context, req *quartermasterpb.CreateEnrollmentTokenRequest) (*quartermasterpb.CreateBootstrapTokenResponse, error)
	ListEdgeReleases(ctx context.Context, req *quartermasterpb.ListEdgeReleasesRequest) (*quartermasterpb.ListEdgeReleasesResponse, error)
	UpsertEdgeRelease(ctx context.Context, req *quartermasterpb.UpsertEdgeReleaseRequest) (*quartermasterpb.EdgeReleaseResponse, error)
	GetClusterReleaseTarget(ctx context.Context, req *quartermasterpb.GetClusterReleaseTargetRequest) (*quartermasterpb.ClusterReleaseTargetResponse, error)
	ListClusterReleaseTargets(ctx context.Context, req *quartermasterpb.ListClusterReleaseTargetsRequest) (*quartermasterpb.ListClusterReleaseTargetsResponse, error)
	SetClusterReleaseTarget(ctx context.Context, req *quartermasterpb.SetClusterReleaseTargetRequest) (*quartermasterpb.ClusterReleaseTargetResponse, error)
	SyncMesh(ctx context.Context, req *quartermasterpb.InfrastructureSyncRequest) (*quartermasterpb.InfrastructureSyncResponse, error)
	ListServices(ctx context.Context, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ListServicesResponse, error)
	ListClusterServices(ctx context.Context, clusterID string, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ListClusterServicesResponse, error)
	ListServiceInstances(ctx context.Context, clusterID, serviceID, nodeID string, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ListServiceInstancesResponse, error)
	ListServiceInstancesByType(ctx context.Context, serviceType, clusterID string, staleThresholdSeconds int32) (*quartermasterpb.ListServiceInstancesByTypeResponse, error)
	ListServicesHealth(ctx context.Context, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ListServicesHealthResponse, error)
	GetServiceHealth(ctx context.Context, serviceID string) (*quartermasterpb.ListServicesHealthResponse, error)
}

var _ Interface = (*GRPCClient)(nil)
