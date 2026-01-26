package quartermaster

import (
	"context"
	"fmt"
	"time"

	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/emptypb"
)

// GRPCClient is the gRPC client for Quartermaster
type GRPCClient struct {
	conn            *grpc.ClientConn
	tenant          pb.TenantServiceClient
	cluster         pb.ClusterServiceClient
	node            pb.NodeServiceClient
	bootstrap       pb.BootstrapServiceClient
	mesh            pb.MeshServiceClient
	serviceRegistry pb.ServiceRegistryServiceClient
	logger          logging.Logger
}

// GRPCConfig represents the configuration for the gRPC client
type GRPCConfig struct {
	// GRPCAddr is the gRPC server address (host:port, no scheme)
	GRPCAddr string
	// Timeout for gRPC calls
	Timeout time.Duration
	// Logger for the client
	Logger logging.Logger
	// ServiceToken for service-to-service authentication (fallback when no user JWT)
	ServiceToken string
}

// authInterceptor propagates authentication to gRPC metadata.
// This reads user_id, tenant_id, and jwt_token from the Go context (set by Gateway middleware)
// and adds them to outgoing gRPC metadata for downstream services.
// If no user JWT is available, it falls back to the service token for service-to-service calls.
func authInterceptor(serviceToken string) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply interface{}, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		// Extract user context from Go context and add to gRPC metadata
		md := metadata.MD{}

		if userID, ok := ctx.Value("user_id").(string); ok && userID != "" {
			md.Set("x-user-id", userID)
		}
		if tenantID, ok := ctx.Value("tenant_id").(string); ok && tenantID != "" {
			md.Set("x-tenant-id", tenantID)
		}

		// Use user's JWT from context if available, otherwise fall back to service token
		if jwtToken, ok := ctx.Value("jwt_token").(string); ok && jwtToken != "" {
			md.Set("authorization", "Bearer "+jwtToken)
		} else if serviceToken != "" {
			md.Set("authorization", "Bearer "+serviceToken)
		}

		// Merge with existing outgoing metadata if any
		if existingMD, ok := metadata.FromOutgoingContext(ctx); ok {
			md = metadata.Join(existingMD, md)
		}

		ctx = metadata.NewOutgoingContext(ctx, md)
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

// NewGRPCClient creates a new gRPC client for Quartermaster
func NewGRPCClient(config GRPCConfig) (*GRPCClient, error) {
	if config.Timeout == 0 {
		config.Timeout = 30 * time.Second
	}

	// Connect to gRPC server with auth interceptor for user context and service token fallback
	conn, err := grpc.NewClient(
		config.GRPCAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithDefaultCallOptions(grpc.WaitForReady(true)),
		grpc.WithUnaryInterceptor(authInterceptor(config.ServiceToken)),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Quartermaster gRPC: %w", err)
	}

	return &GRPCClient{
		conn:            conn,
		tenant:          pb.NewTenantServiceClient(conn),
		cluster:         pb.NewClusterServiceClient(conn),
		node:            pb.NewNodeServiceClient(conn),
		bootstrap:       pb.NewBootstrapServiceClient(conn),
		mesh:            pb.NewMeshServiceClient(conn),
		serviceRegistry: pb.NewServiceRegistryServiceClient(conn),
		logger:          config.Logger,
	}, nil
}

// Close closes the gRPC connection
func (c *GRPCClient) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// ============================================================================
// TENANT OPERATIONS
// ============================================================================

// ValidateTenant validates a tenant
func (c *GRPCClient) ValidateTenant(ctx context.Context, tenantID, userID string) (*pb.ValidateTenantResponse, error) {
	return c.tenant.ValidateTenant(ctx, &pb.ValidateTenantRequest{
		TenantId: tenantID,
		UserId:   userID,
	})
}

// GetTenant gets a tenant by ID
func (c *GRPCClient) GetTenant(ctx context.Context, tenantID string) (*pb.GetTenantResponse, error) {
	return c.tenant.GetTenant(ctx, &pb.GetTenantRequest{
		TenantId: tenantID,
	})
}

// GetClusterRouting gets cluster routing for a tenant
func (c *GRPCClient) GetClusterRouting(ctx context.Context, req *pb.GetClusterRoutingRequest) (*pb.ClusterRoutingResponse, error) {
	return c.tenant.GetClusterRouting(ctx, req)
}

// ListTenants lists tenants with cursor pagination
func (c *GRPCClient) ListTenants(ctx context.Context, pagination *pb.CursorPaginationRequest) (*pb.ListTenantsResponse, error) {
	return c.tenant.ListTenants(ctx, &pb.ListTenantsRequest{
		Pagination: pagination,
	})
}

// CreateTenant creates a new tenant
func (c *GRPCClient) CreateTenant(ctx context.Context, req *pb.CreateTenantRequest) (*pb.CreateTenantResponse, error) {
	return c.tenant.CreateTenant(ctx, req)
}

// UpdateTenant updates a tenant
func (c *GRPCClient) UpdateTenant(ctx context.Context, req *pb.UpdateTenantRequest) (*pb.Tenant, error) {
	return c.tenant.UpdateTenant(ctx, req)
}

// ResolveTenant resolves tenant context from various identifiers
func (c *GRPCClient) ResolveTenant(ctx context.Context, req *pb.ResolveTenantRequest) (*pb.ResolveTenantResponse, error) {
	return c.tenant.ResolveTenant(ctx, req)
}

// ListActiveTenants returns all active tenant IDs for billing batch processing.
// Called by Purser billing job to avoid cross-service DB access.
func (c *GRPCClient) ListActiveTenants(ctx context.Context) ([]string, error) {
	resp, err := c.tenant.ListActiveTenants(ctx, &pb.ListActiveTenantsRequest{})
	if err != nil {
		return nil, err
	}
	return resp.TenantIds, nil
}

// ============================================================================
// CLUSTER OPERATIONS
// ============================================================================

// GetCluster gets a cluster by ID
func (c *GRPCClient) GetCluster(ctx context.Context, clusterID string) (*pb.ClusterResponse, error) {
	return c.cluster.GetCluster(ctx, &pb.GetClusterRequest{
		ClusterId: clusterID,
	})
}

// ListClusters lists clusters with cursor pagination
func (c *GRPCClient) ListClusters(ctx context.Context, pagination *pb.CursorPaginationRequest) (*pb.ListClustersResponse, error) {
	return c.cluster.ListClusters(ctx, &pb.ListClustersRequest{
		Pagination: pagination,
	})
}

// ListClustersByOwner lists clusters owned by a specific tenant.
func (c *GRPCClient) ListClustersByOwner(ctx context.Context, ownerTenantID string, pagination *pb.CursorPaginationRequest) (*pb.ListClustersResponse, error) {
	return c.cluster.ListClusters(ctx, &pb.ListClustersRequest{
		Pagination:    pagination,
		OwnerTenantId: &ownerTenantID,
	})
}

// CreateCluster creates a new cluster
func (c *GRPCClient) CreateCluster(ctx context.Context, req *pb.CreateClusterRequest) (*pb.ClusterResponse, error) {
	return c.cluster.CreateCluster(ctx, req)
}

// UpdateCluster updates a cluster
func (c *GRPCClient) UpdateCluster(ctx context.Context, req *pb.UpdateClusterRequest) (*pb.ClusterResponse, error) {
	return c.cluster.UpdateCluster(ctx, req)
}

// ListClustersForTenant lists clusters accessible to a tenant
func (c *GRPCClient) ListClustersForTenant(ctx context.Context, tenantID string, pagination *pb.CursorPaginationRequest) (*pb.ClustersAccessResponse, error) {
	return c.cluster.ListClustersForTenant(ctx, &pb.ListClustersForTenantRequest{
		TenantId:   tenantID,
		Pagination: pagination,
	})
}

// ListClustersAvailable lists all clusters available in the system
func (c *GRPCClient) ListClustersAvailable(ctx context.Context, pagination *pb.CursorPaginationRequest) (*pb.ClustersAvailableResponse, error) {
	return c.cluster.ListClustersAvailable(ctx, &pb.ListClustersAvailableRequest{
		Pagination: pagination,
	})
}

// GrantClusterAccess grants cluster access to a tenant
func (c *GRPCClient) GrantClusterAccess(ctx context.Context, req *pb.GrantClusterAccessRequest) error {
	_, err := c.cluster.GrantClusterAccess(ctx, req)
	return err
}

// SubscribeToCluster subscribes a tenant to a cluster
func (c *GRPCClient) SubscribeToCluster(ctx context.Context, req *pb.SubscribeToClusterRequest) (*emptypb.Empty, error) {
	return c.cluster.SubscribeToCluster(ctx, req)
}

// UnsubscribeFromCluster unsubscribes a tenant from a cluster
func (c *GRPCClient) UnsubscribeFromCluster(ctx context.Context, req *pb.UnsubscribeFromClusterRequest) (*emptypb.Empty, error) {
	return c.cluster.UnsubscribeFromCluster(ctx, req)
}

// ListMySubscriptions lists clusters the tenant is subscribed to
func (c *GRPCClient) ListMySubscriptions(ctx context.Context, req *pb.ListMySubscriptionsRequest) (*pb.ListClustersResponse, error) {
	return c.cluster.ListMySubscriptions(ctx, req)
}

// ============================================================================
// CLUSTER MARKETPLACE OPERATIONS
// ============================================================================

// ListMarketplaceClusters lists clusters in the marketplace (respects visibility + billing tier)
func (c *GRPCClient) ListMarketplaceClusters(ctx context.Context, req *pb.ListMarketplaceClustersRequest) (*pb.ListMarketplaceClustersResponse, error) {
	return c.cluster.ListMarketplaceClusters(ctx, req)
}

// GetMarketplaceCluster gets a marketplace cluster (with optional invite token for unlisted)
func (c *GRPCClient) GetMarketplaceCluster(ctx context.Context, req *pb.GetMarketplaceClusterRequest) (*pb.MarketplaceClusterEntry, error) {
	return c.cluster.GetMarketplaceCluster(ctx, req)
}

// UpdateClusterMarketplace updates cluster marketplace settings (owner only)
func (c *GRPCClient) UpdateClusterMarketplace(ctx context.Context, req *pb.UpdateClusterMarketplaceRequest) (*pb.ClusterResponse, error) {
	return c.cluster.UpdateClusterMarketplace(ctx, req)
}

// GetClusterMetadataBatch fetches cluster metadata for multiple clusters at once.
// Used by Gateway to enrich marketplace pricing data from Purser with cluster details.
func (c *GRPCClient) GetClusterMetadataBatch(ctx context.Context, req *pb.GetClusterMetadataBatchRequest) (*pb.GetClusterMetadataBatchResponse, error) {
	return c.cluster.GetClusterMetadataBatch(ctx, req)
}

// CreatePrivateCluster creates a private cluster (self-hosted edge)
func (c *GRPCClient) CreatePrivateCluster(ctx context.Context, req *pb.CreatePrivateClusterRequest) (*pb.CreatePrivateClusterResponse, error) {
	return c.cluster.CreatePrivateCluster(ctx, req)
}

// CreateClusterInvite creates an invite to a cluster (cluster owner)
func (c *GRPCClient) CreateClusterInvite(ctx context.Context, req *pb.CreateClusterInviteRequest) (*pb.ClusterInvite, error) {
	return c.cluster.CreateClusterInvite(ctx, req)
}

// RevokeClusterInvite revokes an invite to a cluster (cluster owner)
func (c *GRPCClient) RevokeClusterInvite(ctx context.Context, req *pb.RevokeClusterInviteRequest) error {
	_, err := c.cluster.RevokeClusterInvite(ctx, req)
	return err
}

// ListClusterInvites lists invites for a cluster (cluster owner)
func (c *GRPCClient) ListClusterInvites(ctx context.Context, req *pb.ListClusterInvitesRequest) (*pb.ListClusterInvitesResponse, error) {
	return c.cluster.ListClusterInvites(ctx, req)
}

// ListMyClusterInvites lists pending invites for the current tenant
func (c *GRPCClient) ListMyClusterInvites(ctx context.Context, req *pb.ListMyClusterInvitesRequest) (*pb.ListClusterInvitesResponse, error) {
	return c.cluster.ListMyClusterInvites(ctx, req)
}

// RequestClusterSubscription requests subscription to a cluster (with approval workflow)
func (c *GRPCClient) RequestClusterSubscription(ctx context.Context, req *pb.RequestClusterSubscriptionRequest) (*pb.ClusterSubscription, error) {
	return c.cluster.RequestClusterSubscription(ctx, req)
}

// AcceptClusterInvite accepts a cluster invite
func (c *GRPCClient) AcceptClusterInvite(ctx context.Context, req *pb.AcceptClusterInviteRequest) (*pb.ClusterSubscription, error) {
	return c.cluster.AcceptClusterInvite(ctx, req)
}

// ListPendingSubscriptions lists pending subscription requests (cluster owner)
func (c *GRPCClient) ListPendingSubscriptions(ctx context.Context, req *pb.ListPendingSubscriptionsRequest) (*pb.ListPendingSubscriptionsResponse, error) {
	return c.cluster.ListPendingSubscriptions(ctx, req)
}

// ApproveClusterSubscription approves a subscription request (cluster owner)
func (c *GRPCClient) ApproveClusterSubscription(ctx context.Context, req *pb.ApproveClusterSubscriptionRequest) (*pb.ClusterSubscription, error) {
	return c.cluster.ApproveClusterSubscription(ctx, req)
}

// RejectClusterSubscription rejects a subscription request (cluster owner)
func (c *GRPCClient) RejectClusterSubscription(ctx context.Context, req *pb.RejectClusterSubscriptionRequest) (*pb.ClusterSubscription, error) {
	return c.cluster.RejectClusterSubscription(ctx, req)
}

// ============================================================================
// NODE OPERATIONS
// ============================================================================

// GetNode gets a node by ID
func (c *GRPCClient) GetNode(ctx context.Context, nodeID string) (*pb.NodeResponse, error) {
	return c.node.GetNode(ctx, &pb.GetNodeRequest{
		NodeId: nodeID,
	})
}

// ListNodes lists nodes with filters and cursor pagination
func (c *GRPCClient) ListNodes(ctx context.Context, clusterID, nodeType, region string, pagination *pb.CursorPaginationRequest) (*pb.ListNodesResponse, error) {
	return c.node.ListNodes(ctx, &pb.ListNodesRequest{
		ClusterId:  clusterID,
		NodeType:   nodeType,
		Region:     region,
		Pagination: pagination,
	})
}

// CreateNode creates a new node
func (c *GRPCClient) CreateNode(ctx context.Context, req *pb.CreateNodeRequest) (*pb.NodeResponse, error) {
	return c.node.CreateNode(ctx, req)
}

// ResolveNodeFingerprint resolves a node fingerprint
func (c *GRPCClient) ResolveNodeFingerprint(ctx context.Context, req *pb.ResolveNodeFingerprintRequest) (*pb.ResolveNodeFingerprintResponse, error) {
	return c.node.ResolveNodeFingerprint(ctx, req)
}

// GetNodeOwner gets the owner tenant for a node
func (c *GRPCClient) GetNodeOwner(ctx context.Context, nodeID string) (*pb.NodeOwnerResponse, error) {
	return c.node.GetNodeOwner(ctx, &pb.GetNodeOwnerRequest{
		NodeId: nodeID,
	})
}

// GetNodeByLogicalName resolves a node by its logical name (node_id string like "edge-node-1")
// Returns the full node record including the database UUID (id field)
// Used by Foghorn to enrich subscription broadcasts with database UUID
func (c *GRPCClient) GetNodeByLogicalName(ctx context.Context, nodeID string) (*pb.InfrastructureNode, error) {
	resp, err := c.node.GetNodeByLogicalName(ctx, &pb.GetNodeByLogicalNameRequest{
		NodeId: nodeID,
	})
	if err != nil {
		return nil, err
	}
	return resp.GetNode(), nil
}

// UpdateNodeHardware updates hardware specs for a node (detected at startup by Helmsman)
// Called by Foghorn when processing Register message with hardware info
func (c *GRPCClient) UpdateNodeHardware(ctx context.Context, req *pb.UpdateNodeHardwareRequest) error {
	_, err := c.node.UpdateNodeHardware(ctx, req)
	return err
}

// ============================================================================
// BOOTSTRAP OPERATIONS
// ============================================================================

// BootstrapEdgeNode bootstraps an edge node
func (c *GRPCClient) BootstrapEdgeNode(ctx context.Context, req *pb.BootstrapEdgeNodeRequest) (*pb.BootstrapEdgeNodeResponse, error) {
	return c.bootstrap.BootstrapEdgeNode(ctx, req)
}

// BootstrapInfrastructureNode bootstraps an infrastructure node
func (c *GRPCClient) BootstrapInfrastructureNode(ctx context.Context, req *pb.BootstrapInfrastructureNodeRequest) (*pb.BootstrapInfrastructureNodeResponse, error) {
	return c.bootstrap.BootstrapInfrastructureNode(ctx, req)
}

// BootstrapService bootstraps a service
func (c *GRPCClient) BootstrapService(ctx context.Context, req *pb.BootstrapServiceRequest) (*pb.BootstrapServiceResponse, error) {
	return c.bootstrap.BootstrapService(ctx, req)
}

// DiscoverServices finds instances of a service type
func (c *GRPCClient) DiscoverServices(ctx context.Context, serviceType, clusterID string, pagination *pb.CursorPaginationRequest) (*pb.ServiceDiscoveryResponse, error) {
	return c.bootstrap.DiscoverServices(ctx, &pb.ServiceDiscoveryRequest{
		ServiceType: serviceType,
		ClusterId:   clusterID,
		Pagination:  pagination,
	})
}

// CreateBootstrapToken creates a bootstrap token
func (c *GRPCClient) CreateBootstrapToken(ctx context.Context, req *pb.CreateBootstrapTokenRequest) (*pb.CreateBootstrapTokenResponse, error) {
	return c.bootstrap.CreateBootstrapToken(ctx, req)
}

// ListBootstrapTokens lists bootstrap tokens
func (c *GRPCClient) ListBootstrapTokens(ctx context.Context, kind, tenantID string, pagination *pb.CursorPaginationRequest) (*pb.ListBootstrapTokensResponse, error) {
	return c.bootstrap.ListBootstrapTokens(ctx, &pb.ListBootstrapTokensRequest{
		Kind:       kind,
		TenantId:   tenantID,
		Pagination: pagination,
	})
}

// RevokeBootstrapToken revokes a bootstrap token
func (c *GRPCClient) RevokeBootstrapToken(ctx context.Context, tokenID string) error {
	_, err := c.bootstrap.RevokeBootstrapToken(ctx, &pb.RevokeBootstrapTokenRequest{
		TokenId: tokenID,
	})
	return err
}

// ============================================================================
// MESH OPERATIONS
// ============================================================================

// SyncMesh synchronizes WireGuard mesh configuration
func (c *GRPCClient) SyncMesh(ctx context.Context, req *pb.InfrastructureSyncRequest) (*pb.InfrastructureSyncResponse, error) {
	return c.mesh.SyncMesh(ctx, req)
}

// ============================================================================
// SERVICE REGISTRY OPERATIONS
// ============================================================================

// ListServices lists all registered services
func (c *GRPCClient) ListServices(ctx context.Context, pagination *pb.CursorPaginationRequest) (*pb.ListServicesResponse, error) {
	return c.serviceRegistry.ListServices(ctx, &pb.ListServicesRequest{
		Pagination: pagination,
	})
}

// ListClusterServices lists services for a specific cluster
func (c *GRPCClient) ListClusterServices(ctx context.Context, clusterID string, pagination *pb.CursorPaginationRequest) (*pb.ListClusterServicesResponse, error) {
	return c.serviceRegistry.ListClusterServices(ctx, &pb.ListClusterServicesRequest{
		ClusterId:  clusterID,
		Pagination: pagination,
	})
}

// ListServiceInstances lists instances of a service
func (c *GRPCClient) ListServiceInstances(ctx context.Context, clusterID, serviceID, nodeID string, pagination *pb.CursorPaginationRequest) (*pb.ListServiceInstancesResponse, error) {
	return c.serviceRegistry.ListServiceInstances(ctx, &pb.ListServiceInstancesRequest{
		ClusterId:  clusterID,
		ServiceId:  serviceID,
		NodeId:     nodeID,
		Pagination: pagination,
	})
}

// ListServicesHealth lists health status for all services
func (c *GRPCClient) ListServicesHealth(ctx context.Context, pagination *pb.CursorPaginationRequest) (*pb.ListServicesHealthResponse, error) {
	return c.serviceRegistry.ListServicesHealth(ctx, &pb.ListServicesHealthRequest{
		Pagination: pagination,
	})
}

// GetServiceHealth gets health status for a specific service
func (c *GRPCClient) GetServiceHealth(ctx context.Context, serviceID string) (*pb.ListServicesHealthResponse, error) {
	return c.serviceRegistry.GetServiceHealth(ctx, &pb.GetServiceHealthRequest{
		ServiceId: serviceID,
	})
}
