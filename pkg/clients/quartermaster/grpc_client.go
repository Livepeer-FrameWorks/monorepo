package quartermaster

import (
	"context"
	"fmt"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/grpcutil"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	tenantlimitspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/tenant_limits"

	"google.golang.org/grpc"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/emptypb"
)

const DefaultServerName = "quartermaster.internal"

// GRPCClient is the gRPC client for Quartermaster
type GRPCClient struct {
	conn            *grpc.ClientConn
	tenant          quartermasterpb.TenantServiceClient
	cluster         quartermasterpb.ClusterServiceClient
	node            quartermasterpb.NodeServiceClient
	bootstrap       quartermasterpb.BootstrapServiceClient
	mesh            quartermasterpb.MeshServiceClient
	serviceRegistry quartermasterpb.ServiceRegistryServiceClient
	ingress         quartermasterpb.IngressServiceClient
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
	// PreferServiceToken sends ServiceToken even when the context carries a user JWT.
	PreferServiceToken bool
	AllowInsecure      bool
	CACertFile         string
	CACertPEM          string
	ServerName         string
}

// authInterceptor propagates authentication to gRPC metadata.
// This reads user_id, tenant_id, and jwt_token from the Go context (set by Gateway middleware)
// and adds them to outgoing gRPC metadata for downstream services.
// If no user JWT is available, it falls back to the service token for service-to-service calls.
func authInterceptor(serviceToken string, preferServiceToken bool) grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoker grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		// Extract user context from Go context and add to gRPC metadata
		md := metadata.MD{}

		if userID := ctxkeys.GetUserID(ctx); userID != "" {
			md.Set("x-user-id", userID)
		}
		if tenantID := ctxkeys.GetTenantID(ctx); tenantID != "" {
			md.Set("x-tenant-id", tenantID)
		}
		if ctxkeys.IsDemoMode(ctx) {
			md.Set("x-demo-mode", "true")
		}

		if token := outgoingAuthToken(ctx, serviceToken, preferServiceToken); token != "" {
			md.Set("authorization", "Bearer "+token)
		}

		// Merge with existing outgoing metadata if any
		if existingMD, ok := metadata.FromOutgoingContext(ctx); ok {
			md = metadata.Join(existingMD, md)
		}

		ctx = metadata.NewOutgoingContext(ctx, md)
		return invoker(ctx, method, req, reply, cc, opts...)
	}
}

func outgoingAuthToken(ctx context.Context, configuredServiceToken string, preferServiceToken bool) string {
	if preferServiceToken && configuredServiceToken != "" {
		return configuredServiceToken
	}
	if jwtToken := ctxkeys.GetJWTToken(ctx); jwtToken != "" {
		return jwtToken
	}
	if contextServiceToken := ctxkeys.GetServiceToken(ctx); contextServiceToken != "" {
		return contextServiceToken
	}
	return configuredServiceToken
}

// NewGRPCClient creates a new gRPC client for Quartermaster
func NewGRPCClient(config GRPCConfig) (*GRPCClient, error) {
	if config.Timeout == 0 {
		config.Timeout = 30 * time.Second
	}

	tlsCfg := grpcutil.ClientTLSConfig{
		CACertFile:        config.CACertFile,
		CACertPEM:         config.CACertPEM,
		ServerName:        config.ServerName,
		DefaultServerName: DefaultServerName,
		AllowInsecure:     config.AllowInsecure,
	}
	transport, err := grpcutil.ClientTLS(tlsCfg, config.Logger)
	if err != nil {
		return nil, fmt.Errorf("configure Quartermaster gRPC TLS: %w", err)
	}

	// Connect to gRPC server with auth interceptor for user context and service token fallback
	conn, err := grpc.NewClient(
		config.GRPCAddr,
		transport,
		grpc.WithDefaultCallOptions(grpc.WaitForReady(true)),
		grpc.WithChainUnaryInterceptor(
			authInterceptor(config.ServiceToken, config.PreferServiceToken),
			clients.FailsafeUnaryInterceptor("quartermaster", config.Logger),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to Quartermaster gRPC: %w", err)
	}

	return &GRPCClient{
		conn:            conn,
		tenant:          quartermasterpb.NewTenantServiceClient(conn),
		cluster:         quartermasterpb.NewClusterServiceClient(conn),
		node:            quartermasterpb.NewNodeServiceClient(conn),
		bootstrap:       quartermasterpb.NewBootstrapServiceClient(conn),
		mesh:            quartermasterpb.NewMeshServiceClient(conn),
		serviceRegistry: quartermasterpb.NewServiceRegistryServiceClient(conn),
		ingress:         quartermasterpb.NewIngressServiceClient(conn),
		logger:          config.Logger,
	}, nil
}

// Conn exposes the underlying connection for satellite clients (e.g. the
// service-event producer in pkg/clients/quartermaster/events) to share.
func (c *GRPCClient) Conn() *grpc.ClientConn {
	return c.conn
}

// Close closes the gRPC connection
func (c *GRPCClient) Close() error {
	if c.conn != nil {
		return c.conn.Close()
	}
	return nil
}

// CheckHealth verifies that the Quartermaster gRPC endpoint is reachable and
// serving through this client's configured transport and interceptors.
func (c *GRPCClient) CheckHealth(ctx context.Context) error {
	resp, err := healthpb.NewHealthClient(c.conn).Check(ctx, &healthpb.HealthCheckRequest{}, grpc.WaitForReady(false))
	if err != nil {
		return err
	}
	if resp.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		return fmt.Errorf("quartermaster health status is %s", resp.GetStatus().String())
	}
	return nil
}

// ============================================================================
// TENANT OPERATIONS
// ============================================================================

// ValidateTenant validates a tenant
func (c *GRPCClient) ValidateTenant(ctx context.Context, tenantID, userID string) (*quartermasterpb.ValidateTenantResponse, error) {
	return c.tenant.ValidateTenant(ctx, &quartermasterpb.ValidateTenantRequest{
		TenantId: tenantID,
		UserId:   userID,
	})
}

// GetTenant gets a tenant by ID
func (c *GRPCClient) GetTenant(ctx context.Context, tenantID string) (*quartermasterpb.GetTenantResponse, error) {
	return c.tenant.GetTenant(ctx, &quartermasterpb.GetTenantRequest{
		TenantId: tenantID,
	})
}

// GetClusterRouting gets cluster routing for a tenant
func (c *GRPCClient) GetClusterRouting(ctx context.Context, req *quartermasterpb.GetClusterRoutingRequest) (*quartermasterpb.ClusterRoutingResponse, error) {
	return c.tenant.GetClusterRouting(ctx, req)
}

// ResolveTenantAliases asks Quartermaster to map bootstrap aliases to tenant
// UUIDs. Used by sibling services' bootstrap subcommands so they don't read
// quartermaster.bootstrap_tenant_aliases directly. Aliases without a mapping
// arrive in resp.Unknown rather than as an error so the caller can render a
// precise message naming every missing alias.
func (c *GRPCClient) ResolveTenantAliases(ctx context.Context, aliases []string) (*quartermasterpb.ResolveTenantAliasesResponse, error) {
	return c.tenant.ResolveTenantAliases(ctx, &quartermasterpb.ResolveTenantAliasesRequest{Aliases: aliases})
}

// BootstrapClusterAccess grants a tenant access to a platform-official cluster
// from a service-token caller. Used by sibling services' bootstrap subcommands
// (Purser today) instead of SubscribeToCluster, which is tenant-context-only.
// Idempotent at the server: re-running upserts the same row.
//
// resourceLimits is optional; when non-nil, Quartermaster seeds an
// access-specific override onto tenant_cluster_access.resource_limits via
// COALESCE. Plan-level Free caps are resolved by Purser tier entitlements, so
// normal platform bootstrap passes nil.
func (c *GRPCClient) BootstrapClusterAccess(ctx context.Context, tenantID, clusterID string, resourceLimits *tenantlimitspb.TenantResourceLimits) error {
	_, err := c.cluster.BootstrapClusterAccess(ctx, &quartermasterpb.BootstrapClusterAccessRequest{
		TenantId:       tenantID,
		ClusterId:      clusterID,
		ResourceLimits: resourceLimits,
	})
	return err
}

// DeactivateClusterAccess soft-suspends a tenant_cluster_access row. Used by
// Purser's tier reconciliation on downgrade. Idempotent.
func (c *GRPCClient) DeactivateClusterAccess(ctx context.Context, tenantID, clusterID, reason string) error {
	_, err := c.cluster.DeactivateClusterAccess(ctx, &quartermasterpb.DeactivateClusterAccessRequest{
		TenantId:  tenantID,
		ClusterId: clusterID,
		Reason:    reason,
	})
	return err
}

// ListTenantClusterAccess returns every tenant_cluster_access row with the
// fields needed for tier reconciliation diffs. Service-token only; distinct
// from ListClustersForTenant which is a user-facing minimal-entry RPC.
func (c *GRPCClient) ListTenantClusterAccess(ctx context.Context, tenantID string) (*quartermasterpb.ListTenantClusterAccessResponse, error) {
	return c.cluster.ListTenantClusterAccess(ctx, &quartermasterpb.ListTenantClusterAccessRequest{
		TenantId: tenantID,
	})
}

// ListTenants lists tenants with cursor pagination
func (c *GRPCClient) ListTenants(ctx context.Context, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ListTenantsResponse, error) {
	return c.tenant.ListTenants(ctx, &quartermasterpb.ListTenantsRequest{
		Pagination: pagination,
	})
}

// GetTenantsByCluster lists the tenants assigned to a cluster.
func (c *GRPCClient) GetTenantsByCluster(ctx context.Context, clusterID string, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.GetTenantsByClusterResponse, error) {
	return c.tenant.GetTenantsByCluster(ctx, &quartermasterpb.GetTenantsByClusterRequest{
		ClusterId:  clusterID,
		Pagination: pagination,
	})
}

// CreateTenant creates a new tenant
func (c *GRPCClient) CreateTenant(ctx context.Context, req *quartermasterpb.CreateTenantRequest) (*quartermasterpb.CreateTenantResponse, error) {
	return c.tenant.CreateTenant(ctx, req)
}

// UpdateTenant updates a tenant
func (c *GRPCClient) UpdateTenant(ctx context.Context, req *quartermasterpb.UpdateTenantRequest) (*quartermasterpb.Tenant, error) {
	return c.tenant.UpdateTenant(ctx, req)
}

// ResolveTenant resolves tenant context from various identifiers
func (c *GRPCClient) ResolveTenant(ctx context.Context, req *quartermasterpb.ResolveTenantRequest) (*quartermasterpb.ResolveTenantResponse, error) {
	return c.tenant.ResolveTenant(ctx, req)
}

// ListActiveTenants returns all active tenant IDs for billing batch processing.
// Called by Purser billing job to avoid cross-service DB access.
func (c *GRPCClient) ListActiveTenants(ctx context.Context) ([]string, error) {
	resp, err := c.tenant.ListActiveTenants(ctx, &quartermasterpb.ListActiveTenantsRequest{})
	if err != nil {
		return nil, err
	}
	return resp.GetTenantIds(), nil
}

// ListActiveTenantsWithMonitoring returns active tenants paired with their
// tenant-wide Skipper monitoring switch. Called by Skipper's heartbeat so the
// master switch rides the same per-cycle call that yields the tenant set.
func (c *GRPCClient) ListActiveTenantsWithMonitoring(ctx context.Context) ([]*quartermasterpb.ActiveTenant, error) {
	resp, err := c.tenant.ListActiveTenants(ctx, &quartermasterpb.ListActiveTenantsRequest{})
	if err != nil {
		return nil, err
	}
	if len(resp.GetTenantIds()) != len(resp.GetTenants()) {
		return nil, fmt.Errorf("quartermaster ListActiveTenants response mismatch: tenant_ids=%d tenants=%d", len(resp.GetTenantIds()), len(resp.GetTenants()))
	}
	return resp.GetTenants(), nil
}

// ============================================================================
// CLUSTER OPERATIONS
// ============================================================================

// GetCluster gets a cluster by ID
func (c *GRPCClient) GetCluster(ctx context.Context, clusterID string) (*quartermasterpb.ClusterResponse, error) {
	return c.cluster.GetCluster(ctx, &quartermasterpb.GetClusterRequest{
		ClusterId: clusterID,
	})
}

// ListAliasedTenantsForCluster returns paid-tier tenants with active
// access + a subdomain in this cluster. Used by Foghorn to know which
// per-tenant TLS bundles to include in ConfigSeed for edges in the
// cluster.
func (c *GRPCClient) ListAliasedTenantsForCluster(ctx context.Context, clusterID string) (*quartermasterpb.ListAliasedTenantsForClusterResponse, error) {
	return c.tenant.ListAliasedTenantsForCluster(ctx, &quartermasterpb.ListAliasedTenantsForClusterRequest{
		ClusterId: clusterID,
	})
}

// ListClusters lists clusters with cursor pagination
func (c *GRPCClient) ListClusters(ctx context.Context, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ListClustersResponse, error) {
	return c.cluster.ListClusters(ctx, &quartermasterpb.ListClustersRequest{
		Pagination: pagination,
	})
}

// ListClustersByOwner lists clusters owned by a specific tenant.
func (c *GRPCClient) ListClustersByOwner(ctx context.Context, ownerTenantID string, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ListClustersResponse, error) {
	return c.cluster.ListClusters(ctx, &quartermasterpb.ListClustersRequest{
		Pagination:    pagination,
		OwnerTenantId: &ownerTenantID,
	})
}

// ListOfficialClusters lists all platform-official clusters.
func (c *GRPCClient) ListOfficialClusters(ctx context.Context) (*quartermasterpb.ListClustersResponse, error) {
	t := true
	return c.cluster.ListClusters(ctx, &quartermasterpb.ListClustersRequest{
		IsPlatformOfficial: &t,
	})
}

// ListPublicTopologyClusters lists clusters visible on public topology maps.
func (c *GRPCClient) ListPublicTopologyClusters(ctx context.Context) (*quartermasterpb.ListClustersResponse, error) {
	t := true
	return c.cluster.ListClusters(ctx, &quartermasterpb.ListClustersRequest{
		PublicTopology: &t,
	})
}

// CreateCluster creates a new cluster
func (c *GRPCClient) CreateCluster(ctx context.Context, req *quartermasterpb.CreateClusterRequest) (*quartermasterpb.ClusterResponse, error) {
	return c.cluster.CreateCluster(ctx, req)
}

// UpdateCluster updates a cluster
func (c *GRPCClient) UpdateCluster(ctx context.Context, req *quartermasterpb.UpdateClusterRequest) (*quartermasterpb.ClusterResponse, error) {
	return c.cluster.UpdateCluster(ctx, req)
}

// UpdateClusterMeshConfig stores the cluster's WireGuard mesh CIDR and
// default listen port in Quartermaster. Sourced from the manifest during
// cluster provision; used later by BootstrapInfrastructureNode.
func (c *GRPCClient) UpdateClusterMeshConfig(ctx context.Context, req *quartermasterpb.UpdateClusterMeshConfigRequest) (*quartermasterpb.UpdateClusterMeshConfigResponse, error) {
	return c.cluster.UpdateClusterMeshConfig(ctx, req)
}

// SetNodeEnrollmentOrigin flips a node's enrollment_origin. Used by
// `frameworks mesh reconcile` to promote enrolled nodes into GitOps.
func (c *GRPCClient) SetNodeEnrollmentOrigin(ctx context.Context, req *quartermasterpb.SetNodeEnrollmentOriginRequest) (*quartermasterpb.SetNodeEnrollmentOriginResponse, error) {
	return c.node.SetNodeEnrollmentOrigin(ctx, req)
}

// ListClustersForTenant lists clusters accessible to a tenant
func (c *GRPCClient) ListClustersForTenant(ctx context.Context, tenantID string, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ClustersAccessResponse, error) {
	return c.cluster.ListClustersForTenant(ctx, &quartermasterpb.ListClustersForTenantRequest{
		TenantId:   tenantID,
		Pagination: pagination,
	})
}

// ListClustersAvailable lists all clusters available in the system
func (c *GRPCClient) ListClustersAvailable(ctx context.Context, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ClustersAvailableResponse, error) {
	return c.cluster.ListClustersAvailable(ctx, &quartermasterpb.ListClustersAvailableRequest{
		Pagination: pagination,
	})
}

// GrantClusterAccess grants cluster access to a tenant
func (c *GRPCClient) GrantClusterAccess(ctx context.Context, req *quartermasterpb.GrantClusterAccessRequest) error {
	_, err := c.cluster.GrantClusterAccess(ctx, req)
	return err
}

// SubscribeToCluster subscribes a tenant to a cluster
func (c *GRPCClient) SubscribeToCluster(ctx context.Context, req *quartermasterpb.SubscribeToClusterRequest) (*emptypb.Empty, error) {
	return c.cluster.SubscribeToCluster(ctx, req)
}

// UnsubscribeFromCluster unsubscribes a tenant from a cluster
func (c *GRPCClient) UnsubscribeFromCluster(ctx context.Context, req *quartermasterpb.UnsubscribeFromClusterRequest) (*emptypb.Empty, error) {
	return c.cluster.UnsubscribeFromCluster(ctx, req)
}

// ListMySubscriptions lists clusters the tenant is subscribed to
func (c *GRPCClient) ListMySubscriptions(ctx context.Context, req *quartermasterpb.ListMySubscriptionsRequest) (*quartermasterpb.ListClustersResponse, error) {
	return c.cluster.ListMySubscriptions(ctx, req)
}

// UpsertTLSBundle creates or updates desired ingress TLS state.
func (c *GRPCClient) UpsertTLSBundle(ctx context.Context, bundle *quartermasterpb.TLSBundle) (*quartermasterpb.TLSBundleResponse, error) {
	return c.ingress.UpsertTLSBundle(ctx, &quartermasterpb.UpsertTLSBundleRequest{Bundle: bundle})
}

// ListTLSBundles lists desired ingress TLS bundles.
func (c *GRPCClient) ListTLSBundles(ctx context.Context, clusterID string, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ListTLSBundlesResponse, error) {
	return c.ingress.ListTLSBundles(ctx, &quartermasterpb.ListTLSBundlesRequest{
		ClusterId:  clusterID,
		Pagination: pagination,
	})
}

// UpsertIngressSite creates or updates a desired ingress site.
func (c *GRPCClient) UpsertIngressSite(ctx context.Context, site *quartermasterpb.IngressSite) (*quartermasterpb.IngressSiteResponse, error) {
	return c.ingress.UpsertIngressSite(ctx, &quartermasterpb.UpsertIngressSiteRequest{Site: site})
}

// ListIngressSites lists desired ingress sites.
func (c *GRPCClient) ListIngressSites(ctx context.Context, clusterID, nodeID string, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ListIngressSitesResponse, error) {
	return c.ingress.ListIngressSites(ctx, &quartermasterpb.ListIngressSitesRequest{
		ClusterId:  clusterID,
		NodeId:     nodeID,
		Pagination: pagination,
	})
}

// ============================================================================
// CLUSTER MARKETPLACE OPERATIONS
// ============================================================================

// ListMarketplaceClusters lists clusters in the marketplace (respects visibility + billing tier)
func (c *GRPCClient) ListMarketplaceClusters(ctx context.Context, req *quartermasterpb.ListMarketplaceClustersRequest) (*quartermasterpb.ListMarketplaceClustersResponse, error) {
	return c.cluster.ListMarketplaceClusters(ctx, req)
}

// GetMarketplaceCluster gets a marketplace cluster (with optional invite token for unlisted)
func (c *GRPCClient) GetMarketplaceCluster(ctx context.Context, req *quartermasterpb.GetMarketplaceClusterRequest) (*quartermasterpb.MarketplaceClusterEntry, error) {
	return c.cluster.GetMarketplaceCluster(ctx, req)
}

// UpdateClusterMarketplace updates cluster marketplace settings (owner only)
func (c *GRPCClient) UpdateClusterMarketplace(ctx context.Context, req *quartermasterpb.UpdateClusterMarketplaceRequest) (*quartermasterpb.ClusterResponse, error) {
	return c.cluster.UpdateClusterMarketplace(ctx, req)
}

// GetClusterMetadataBatch fetches cluster metadata for multiple clusters at once.
// Used by Gateway to enrich marketplace pricing data from Purser with cluster details.
func (c *GRPCClient) GetClusterMetadataBatch(ctx context.Context, req *quartermasterpb.GetClusterMetadataBatchRequest) (*quartermasterpb.GetClusterMetadataBatchResponse, error) {
	return c.cluster.GetClusterMetadataBatch(ctx, req)
}

// CreatePrivateCluster creates a private cluster (self-hosted edge)
func (c *GRPCClient) CreatePrivateCluster(ctx context.Context, req *quartermasterpb.CreatePrivateClusterRequest) (*quartermasterpb.CreatePrivateClusterResponse, error) {
	return c.cluster.CreatePrivateCluster(ctx, req)
}

// CreateClusterInvite creates an invite to a cluster (cluster owner)
func (c *GRPCClient) CreateClusterInvite(ctx context.Context, req *quartermasterpb.CreateClusterInviteRequest) (*quartermasterpb.ClusterInvite, error) {
	return c.cluster.CreateClusterInvite(ctx, req)
}

// RevokeClusterInvite revokes an invite to a cluster (cluster owner)
func (c *GRPCClient) RevokeClusterInvite(ctx context.Context, req *quartermasterpb.RevokeClusterInviteRequest) error {
	_, err := c.cluster.RevokeClusterInvite(ctx, req)
	return err
}

// ListClusterInvites lists invites for a cluster (cluster owner)
func (c *GRPCClient) ListClusterInvites(ctx context.Context, req *quartermasterpb.ListClusterInvitesRequest) (*quartermasterpb.ListClusterInvitesResponse, error) {
	return c.cluster.ListClusterInvites(ctx, req)
}

// ListMyClusterInvites lists pending invites for the current tenant
func (c *GRPCClient) ListMyClusterInvites(ctx context.Context, req *quartermasterpb.ListMyClusterInvitesRequest) (*quartermasterpb.ListClusterInvitesResponse, error) {
	return c.cluster.ListMyClusterInvites(ctx, req)
}

// RequestClusterSubscription requests subscription to a cluster (with approval workflow)
func (c *GRPCClient) RequestClusterSubscription(ctx context.Context, req *quartermasterpb.RequestClusterSubscriptionRequest) (*quartermasterpb.ClusterSubscription, error) {
	return c.cluster.RequestClusterSubscription(ctx, req)
}

// AcceptClusterInvite accepts a cluster invite
func (c *GRPCClient) AcceptClusterInvite(ctx context.Context, req *quartermasterpb.AcceptClusterInviteRequest) (*quartermasterpb.ClusterSubscription, error) {
	return c.cluster.AcceptClusterInvite(ctx, req)
}

// ListPendingSubscriptions lists pending subscription requests (cluster owner)
func (c *GRPCClient) ListPendingSubscriptions(ctx context.Context, req *quartermasterpb.ListPendingSubscriptionsRequest) (*quartermasterpb.ListPendingSubscriptionsResponse, error) {
	return c.cluster.ListPendingSubscriptions(ctx, req)
}

// ApproveClusterSubscription approves a subscription request (cluster owner)
func (c *GRPCClient) ApproveClusterSubscription(ctx context.Context, req *quartermasterpb.ApproveClusterSubscriptionRequest) (*quartermasterpb.ClusterSubscription, error) {
	return c.cluster.ApproveClusterSubscription(ctx, req)
}

// RejectClusterSubscription rejects a subscription request (cluster owner)
func (c *GRPCClient) RejectClusterSubscription(ctx context.Context, req *quartermasterpb.RejectClusterSubscriptionRequest) (*quartermasterpb.ClusterSubscription, error) {
	return c.cluster.RejectClusterSubscription(ctx, req)
}

// UpdateTenantCluster updates tenant cluster assignment (e.g. preferred cluster)
func (c *GRPCClient) UpdateTenantCluster(ctx context.Context, req *quartermasterpb.UpdateTenantClusterRequest) error {
	_, err := c.tenant.UpdateTenantCluster(ctx, req)
	return err
}

// ListPeers returns clusters that share tenants with the given cluster.
func (c *GRPCClient) ListPeers(ctx context.Context, clusterID string) (*quartermasterpb.ListPeersResponse, error) {
	return c.cluster.ListPeers(ctx, &quartermasterpb.ListPeersRequest{
		ClusterId: clusterID,
	})
}

// ============================================================================
// NODE OPERATIONS
// ============================================================================

// GetNode gets a node by ID
func (c *GRPCClient) GetNode(ctx context.Context, nodeID string) (*quartermasterpb.NodeResponse, error) {
	return c.node.GetNode(ctx, &quartermasterpb.GetNodeRequest{
		NodeId: nodeID,
	})
}

// ListNodes lists nodes with filters and cursor pagination
func (c *GRPCClient) ListNodes(ctx context.Context, clusterID, nodeType, region string, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ListNodesResponse, error) {
	return c.node.ListNodes(ctx, &quartermasterpb.ListNodesRequest{
		ClusterId:  clusterID,
		NodeType:   nodeType,
		Region:     region,
		Pagination: pagination,
	})
}

// ListHealthyNodesForDNS lists healthy nodes for DNS sync by service type.
// Quartermaster resolves edge queries (service_type "edge" or "edge-*") via
// node_type + heartbeat; all other queries use the service_instance join.
func (c *GRPCClient) ListHealthyNodesForDNS(ctx context.Context, staleThresholdSeconds int, serviceType string) (*quartermasterpb.ListHealthyNodesForDNSResponse, error) {
	return c.ListHealthyNodesForDNSForCluster(ctx, staleThresholdSeconds, serviceType, "")
}

// ListHealthyNodesForDNSForCluster lists DNS-eligible nodes for one cluster.
// Empty clusterID leaves the request unscoped.
func (c *GRPCClient) ListHealthyNodesForDNSForCluster(ctx context.Context, staleThresholdSeconds int, serviceType, clusterID string) (*quartermasterpb.ListHealthyNodesForDNSResponse, error) {
	req := &quartermasterpb.ListHealthyNodesForDNSRequest{
		StaleThresholdSeconds: int32(staleThresholdSeconds),
	}
	if serviceType != "" {
		req.ServiceType = &serviceType
	}
	if clusterID != "" {
		req.ClusterId = &clusterID
	}
	return c.node.ListHealthyNodesForDNS(ctx, req)
}

// CreateNode creates a new node
func (c *GRPCClient) CreateNode(ctx context.Context, req *quartermasterpb.CreateNodeRequest) (*quartermasterpb.NodeResponse, error) {
	return c.node.CreateNode(ctx, req)
}

// UpdateNodeStatus changes a node's routing-visible registry status.
func (c *GRPCClient) UpdateNodeStatus(ctx context.Context, req *quartermasterpb.UpdateNodeStatusRequest) (*quartermasterpb.NodeResponse, error) {
	return c.node.UpdateNodeStatus(ctx, req)
}

// ResolveNodeFingerprint resolves a node fingerprint
func (c *GRPCClient) ResolveNodeFingerprint(ctx context.Context, req *quartermasterpb.ResolveNodeFingerprintRequest) (*quartermasterpb.ResolveNodeFingerprintResponse, error) {
	return c.node.ResolveNodeFingerprint(ctx, req)
}

// GetNodeOwner gets the owner tenant for a node
func (c *GRPCClient) GetNodeOwner(ctx context.Context, nodeID string) (*quartermasterpb.NodeOwnerResponse, error) {
	return c.node.GetNodeOwner(ctx, &quartermasterpb.GetNodeOwnerRequest{
		NodeId: nodeID,
	})
}

// GetNodeByLogicalName resolves a node by its logical name (node_id string like "edge-node-1")
// Returns the full node record including the database UUID (id field)
// Used by Foghorn to enrich subscription broadcasts with database UUID
func (c *GRPCClient) GetNodeByLogicalName(ctx context.Context, nodeID string) (*quartermasterpb.InfrastructureNode, error) {
	resp, err := c.node.GetNodeByLogicalName(ctx, &quartermasterpb.GetNodeByLogicalNameRequest{
		NodeId: nodeID,
	})
	if err != nil {
		return nil, err
	}
	return resp.GetNode(), nil
}

// UpdateNodeHardware updates hardware specs for a node (detected at startup by Helmsman)
// Called by Foghorn when processing Register message with hardware info
func (c *GRPCClient) UpdateNodeHardware(ctx context.Context, req *quartermasterpb.UpdateNodeHardwareRequest) error {
	_, err := c.node.UpdateNodeHardware(ctx, req)
	return err
}

// ReportAliveNodes pushes the current per-node DNS-relevant state (health,
// capabilities, cluster, external IP) for every connected edge. Quartermaster
// refreshes infrastructure_nodes.last_heartbeat, syncs service_instances
// health_status from capabilities, and fires Navigator wakeups for any
// (cluster, edge-service) pair whose DNS-visible set changes.
func (c *GRPCClient) ReportAliveNodes(ctx context.Context, nodes []*quartermasterpb.NodeAliveness) error {
	_, err := c.node.ReportAliveNodes(ctx, &quartermasterpb.ReportAliveNodesRequest{
		Nodes: nodes,
	})
	return err
}

// ============================================================================
// BOOTSTRAP OPERATIONS
// ============================================================================

// BootstrapEdgeNode bootstraps an edge node
func (c *GRPCClient) BootstrapEdgeNode(ctx context.Context, req *quartermasterpb.BootstrapEdgeNodeRequest) (*quartermasterpb.BootstrapEdgeNodeResponse, error) {
	return c.bootstrap.BootstrapEdgeNode(ctx, req)
}

// BootstrapInfrastructureNode bootstraps an infrastructure node
func (c *GRPCClient) BootstrapInfrastructureNode(ctx context.Context, req *quartermasterpb.BootstrapInfrastructureNodeRequest) (*quartermasterpb.BootstrapInfrastructureNodeResponse, error) {
	return c.bootstrap.BootstrapInfrastructureNode(ctx, req)
}

// BootstrapService bootstraps a service
func (c *GRPCClient) BootstrapService(ctx context.Context, req *quartermasterpb.BootstrapServiceRequest) (*quartermasterpb.BootstrapServiceResponse, error) {
	return c.bootstrap.BootstrapService(ctx, req)
}

// DiscoverServices finds instances of a service type
func (c *GRPCClient) DiscoverServices(ctx context.Context, serviceType, clusterID string, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ServiceDiscoveryResponse, error) {
	return c.bootstrap.DiscoverServices(ctx, &quartermasterpb.ServiceDiscoveryRequest{
		ServiceType: serviceType,
		ClusterId:   clusterID,
		Pagination:  pagination,
	})
}

// CreateBootstrapToken creates a bootstrap token
func (c *GRPCClient) CreateBootstrapToken(ctx context.Context, req *quartermasterpb.CreateBootstrapTokenRequest) (*quartermasterpb.CreateBootstrapTokenResponse, error) {
	return c.bootstrap.CreateBootstrapToken(ctx, req)
}

// ListBootstrapTokens lists bootstrap tokens
func (c *GRPCClient) ListBootstrapTokens(ctx context.Context, kind, tenantID string, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ListBootstrapTokensResponse, error) {
	return c.bootstrap.ListBootstrapTokens(ctx, &quartermasterpb.ListBootstrapTokensRequest{
		Kind:       kind,
		TenantId:   tenantID,
		Pagination: pagination,
	})
}

// RevokeBootstrapToken revokes a bootstrap token
func (c *GRPCClient) RevokeBootstrapToken(ctx context.Context, tokenID string) error {
	_, err := c.bootstrap.RevokeBootstrapToken(ctx, &quartermasterpb.RevokeBootstrapTokenRequest{
		TokenId: tokenID,
	})
	return err
}

// ValidateBootstrapToken performs a read-only check on a bootstrap token.
// Returns validity status, kind, cluster_id, and tenant_id without consuming the token.
func (c *GRPCClient) ValidateBootstrapToken(ctx context.Context, token string) (*quartermasterpb.ValidateBootstrapTokenResponse, error) {
	return c.bootstrap.ValidateBootstrapToken(ctx, &quartermasterpb.ValidateBootstrapTokenRequest{
		Token: token,
	})
}

// ValidateBootstrapTokenEx validates with IP binding and optional consumption.
func (c *GRPCClient) ValidateBootstrapTokenEx(ctx context.Context, req *quartermasterpb.ValidateBootstrapTokenRequest) (*quartermasterpb.ValidateBootstrapTokenResponse, error) {
	return c.bootstrap.ValidateBootstrapToken(ctx, req)
}

// ============================================================================
// SERVICE POOL MANAGEMENT
// ============================================================================

func (c *GRPCClient) GetServicePoolStatus(ctx context.Context, serviceType string) (*quartermasterpb.GetServicePoolStatusResponse, error) {
	return c.bootstrap.GetServicePoolStatus(ctx, &quartermasterpb.GetServicePoolStatusRequest{ServiceType: serviceType})
}

func (c *GRPCClient) AddToServicePool(ctx context.Context, req *quartermasterpb.AddToServicePoolRequest) (*quartermasterpb.AddToServicePoolResponse, error) {
	return c.bootstrap.AddToServicePool(ctx, req)
}

func (c *GRPCClient) DrainServiceInstance(ctx context.Context, req *quartermasterpb.DrainServiceInstanceRequest) (*quartermasterpb.DrainServiceInstanceResponse, error) {
	return c.bootstrap.DrainServiceInstance(ctx, req)
}

func (c *GRPCClient) AssignServiceToCluster(ctx context.Context, req *quartermasterpb.AssignServiceToClusterRequest) error {
	_, err := c.cluster.AssignServiceToCluster(ctx, req)
	return err
}

func (c *GRPCClient) UnassignServiceFromCluster(ctx context.Context, req *quartermasterpb.UnassignServiceFromClusterRequest) error {
	_, err := c.cluster.UnassignServiceFromCluster(ctx, req)
	return err
}

func (c *GRPCClient) EnableSelfHosting(ctx context.Context, req *quartermasterpb.EnableSelfHostingRequest) (*quartermasterpb.EnableSelfHostingResponse, error) {
	return c.cluster.EnableSelfHosting(ctx, req)
}

func (c *GRPCClient) CreateEnrollmentToken(ctx context.Context, req *quartermasterpb.CreateEnrollmentTokenRequest) (*quartermasterpb.CreateBootstrapTokenResponse, error) {
	return c.cluster.CreateEnrollmentToken(ctx, req)
}

func (c *GRPCClient) ListEdgeReleases(ctx context.Context, req *quartermasterpb.ListEdgeReleasesRequest) (*quartermasterpb.ListEdgeReleasesResponse, error) {
	return c.cluster.ListEdgeReleases(ctx, req)
}

func (c *GRPCClient) UpsertEdgeRelease(ctx context.Context, req *quartermasterpb.UpsertEdgeReleaseRequest) (*quartermasterpb.EdgeReleaseResponse, error) {
	return c.cluster.UpsertEdgeRelease(ctx, req)
}

func (c *GRPCClient) GetClusterReleaseTarget(ctx context.Context, req *quartermasterpb.GetClusterReleaseTargetRequest) (*quartermasterpb.ClusterReleaseTargetResponse, error) {
	return c.cluster.GetClusterReleaseTarget(ctx, req)
}

func (c *GRPCClient) ListClusterReleaseTargets(ctx context.Context, req *quartermasterpb.ListClusterReleaseTargetsRequest) (*quartermasterpb.ListClusterReleaseTargetsResponse, error) {
	return c.cluster.ListClusterReleaseTargets(ctx, req)
}

func (c *GRPCClient) SetClusterReleaseTarget(ctx context.Context, req *quartermasterpb.SetClusterReleaseTargetRequest) (*quartermasterpb.ClusterReleaseTargetResponse, error) {
	return c.cluster.SetClusterReleaseTarget(ctx, req)
}

// ============================================================================
// MESH OPERATIONS
// ============================================================================

// SyncMesh synchronizes WireGuard mesh configuration
func (c *GRPCClient) SyncMesh(ctx context.Context, req *quartermasterpb.InfrastructureSyncRequest) (*quartermasterpb.InfrastructureSyncResponse, error) {
	return c.mesh.SyncMesh(ctx, req)
}

// ============================================================================
// SERVICE REGISTRY OPERATIONS
// ============================================================================

// ListServices lists all registered services
func (c *GRPCClient) ListServices(ctx context.Context, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ListServicesResponse, error) {
	return c.serviceRegistry.ListServices(ctx, &quartermasterpb.ListServicesRequest{
		Pagination: pagination,
	})
}

// ListClusterServices lists services for a specific cluster
func (c *GRPCClient) ListClusterServices(ctx context.Context, clusterID string, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ListClusterServicesResponse, error) {
	return c.serviceRegistry.ListClusterServices(ctx, &quartermasterpb.ListClusterServicesRequest{
		ClusterId:  clusterID,
		Pagination: pagination,
	})
}

// ListServiceInstances lists instances of a service
func (c *GRPCClient) ListServiceInstances(ctx context.Context, clusterID, serviceID, nodeID string, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ListServiceInstancesResponse, error) {
	return c.serviceRegistry.ListServiceInstances(ctx, &quartermasterpb.ListServiceInstancesRequest{
		ClusterId:  clusterID,
		ServiceId:  serviceID,
		NodeId:     nodeID,
		Pagination: pagination,
	})
}

// ListServiceInstancesByType lists the physical instances of a service type,
// each carrying its node external IP and synthesized infra endpoint. Used by
// Navigator to publish per-node infra DNS records (no SCA grouping).
func (c *GRPCClient) ListServiceInstancesByType(ctx context.Context, serviceType, clusterID string, staleThresholdSeconds int32) (*quartermasterpb.ListServiceInstancesByTypeResponse, error) {
	return c.serviceRegistry.ListServiceInstancesByType(ctx, &quartermasterpb.ListServiceInstancesByTypeRequest{
		ServiceType:           serviceType,
		ClusterId:             clusterID,
		StaleThresholdSeconds: staleThresholdSeconds,
	})
}

// ListServicesHealth lists health status for all services
func (c *GRPCClient) ListServicesHealth(ctx context.Context, pagination *commonpb.CursorPaginationRequest) (*quartermasterpb.ListServicesHealthResponse, error) {
	return c.serviceRegistry.ListServicesHealth(ctx, &quartermasterpb.ListServicesHealthRequest{
		Pagination: pagination,
	})
}

// GetServiceHealth gets health status for a specific service
func (c *GRPCClient) GetServiceHealth(ctx context.Context, serviceID string) (*quartermasterpb.ListServicesHealthResponse, error) {
	return c.serviceRegistry.GetServiceHealth(ctx, &quartermasterpb.GetServiceHealthRequest{
		ServiceId: serviceID,
	})
}
