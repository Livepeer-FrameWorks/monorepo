package resources

import (
	"context"
	"fmt"
	"strings"

	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/mcp/mcperrors"
	"frameworks/api_gateway/internal/resolvers"
	"frameworks/pkg/ctxkeys"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterClusterResources registers cluster/marketplace-related MCP resources.
func RegisterClusterResources(server *mcp.Server, clients *clients.ServiceClients, resolver *resolvers.Resolver, logger logging.Logger) {
	// clusters://list - Tenant's subscribed clusters
	server.AddResource(&mcp.Resource{
		URI:         "clusters://list",
		Name:        "My Clusters",
		Description: "Clusters you are subscribed to. Shows health status, type, and whether each is your preferred (default) cluster.",
		MIMEType:    "application/json",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return handleClustersList(ctx, resolver, logger)
	})

	// clusters://{id} - Cluster details
	server.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: "clusters://{id}",
		Name:        "Cluster Details",
		Description: "Details for a specific cluster including capacity, pricing, and subscription status.",
		MIMEType:    "application/json",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return handleClusterByID(ctx, req.Params.URI, resolver, logger)
	})

	// clusters://marketplace - Available marketplace clusters
	server.AddResource(&mcp.Resource{
		URI:         "clusters://marketplace",
		Name:        "Cluster Marketplace",
		Description: "Available infrastructure clusters for subscription. Shows pricing, capacity, eligibility, and subscription status.",
		MIMEType:    "application/json",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return handleClustersMarketplace(ctx, resolver, logger)
	})
}

// ClusterInfo represents a subscribed cluster for MCP.
type ClusterInfo struct {
	ClusterID    string `json:"cluster_id"`
	ClusterName  string `json:"cluster_name"`
	ClusterType  string `json:"cluster_type,omitempty"`
	HealthStatus string `json:"health_status,omitempty"`
	IsDefault    bool   `json:"is_default"`
	IsSubscribed bool   `json:"is_subscribed"`
}

// ClustersListResponse represents the clusters://list response.
type ClustersListResponse struct {
	Clusters []ClusterInfo `json:"clusters"`
}

func handleClustersList(ctx context.Context, resolver *resolvers.Resolver, logger logging.Logger) (*mcp.ReadResourceResult, error) {
	if ctxkeys.GetTenantID(ctx) == "" {
		return nil, mcperrors.AuthRequired()
	}

	clusters, err := resolver.DoListMySubscriptions(ctx, nil, nil)
	if err != nil {
		logger.WithError(err).Debug("Failed to list subscribed clusters")
		return nil, fmt.Errorf("failed to list clusters: %w", err)
	}

	items := make([]ClusterInfo, 0, len(clusters))
	for _, c := range clusters {
		info := ClusterInfo{
			ClusterID:    c.ClusterId,
			ClusterName:  c.ClusterName,
			ClusterType:  c.ClusterType,
			HealthStatus: c.HealthStatus,
			IsDefault:    c.IsDefaultCluster,
			IsSubscribed: true,
		}
		items = append(items, info)
	}

	return marshalResourceResult("clusters://list", ClustersListResponse{Clusters: items})
}

// ClusterDetailInfo represents detailed cluster info for MCP.
type ClusterDetailInfo struct {
	ClusterID          string  `json:"cluster_id"`
	ClusterName        string  `json:"cluster_name"`
	ClusterType        string  `json:"cluster_type,omitempty"`
	Description        string  `json:"description,omitempty"`
	HealthStatus       string  `json:"health_status,omitempty"`
	PricingModel       string  `json:"pricing_model,omitempty"`
	MonthlyPriceCents  int64   `json:"monthly_price_cents,omitempty"`
	RequiresApproval   bool    `json:"requires_approval"`
	MaxStreams         int32   `json:"max_streams,omitempty"`
	MaxViewers         int32   `json:"max_viewers,omitempty"`
	Utilization        float64 `json:"current_utilization,omitempty"`
	IsSubscribed       bool    `json:"is_subscribed"`
	SubscriptionStatus string  `json:"subscription_status,omitempty"`
	IsEligible         bool    `json:"is_eligible"`
	OwnerName          string  `json:"owner_name,omitempty"`
}

func handleClusterByID(ctx context.Context, uri string, resolver *resolvers.Resolver, logger logging.Logger) (*mcp.ReadResourceResult, error) {
	if ctxkeys.GetTenantID(ctx) == "" {
		return nil, mcperrors.AuthRequired()
	}

	clusterID := strings.TrimPrefix(uri, "clusters://")
	if clusterID == "" || clusterID == "list" || clusterID == "marketplace" {
		return nil, fmt.Errorf("invalid cluster ID")
	}

	cluster, err := resolver.DoGetMarketplaceCluster(ctx, clusterID, nil)
	if err != nil {
		return nil, fmt.Errorf("cluster not found: %w", err)
	}

	info := marketplaceEntryToDetail(cluster)
	return marshalResourceResult(uri, info)
}

// MarketplaceResponse represents the clusters://marketplace response.
type MarketplaceResponse struct {
	Clusters []ClusterDetailInfo `json:"clusters"`
	HasMore  bool                `json:"has_more"`
}

func handleClustersMarketplace(ctx context.Context, resolver *resolvers.Resolver, logger logging.Logger) (*mcp.ReadResourceResult, error) {
	if ctxkeys.GetTenantID(ctx) == "" {
		return nil, mcperrors.AuthRequired()
	}

	first := 50
	clusters, err := resolver.DoListMarketplaceClusters(ctx, &first, nil)
	if err != nil {
		logger.WithError(err).Debug("Failed to list marketplace clusters")
		return nil, fmt.Errorf("failed to list marketplace: %w", err)
	}

	items := make([]ClusterDetailInfo, 0, len(clusters))
	for _, c := range clusters {
		items = append(items, marketplaceEntryToDetail(c))
	}

	return marshalResourceResult("clusters://marketplace", MarketplaceResponse{
		Clusters: items,
		HasMore:  len(clusters) >= 50,
	})
}

func marketplaceEntryToDetail(c *pb.MarketplaceClusterEntry) ClusterDetailInfo {
	info := ClusterDetailInfo{
		ClusterID:         c.ClusterId,
		ClusterName:       c.ClusterName,
		ClusterType:       c.Visibility.String(),
		PricingModel:      c.PricingModel.String(),
		MonthlyPriceCents: int64(c.MonthlyPriceCents),
		RequiresApproval:  c.RequiresApproval,
		MaxStreams:        c.MaxConcurrentStreams,
		MaxViewers:        c.MaxConcurrentViewers,
		IsSubscribed:      c.IsSubscribed,
		IsEligible:        c.IsEligible,
	}
	if c.ShortDescription != nil {
		info.Description = *c.ShortDescription
	}
	if c.CurrentUtilization != nil {
		info.Utilization = *c.CurrentUtilization
	}
	if c.SubscriptionStatus != 0 {
		info.SubscriptionStatus = c.SubscriptionStatus.String()
	}
	if c.DenialReason != nil && *c.DenialReason != "" {
		info.IsEligible = false
	}
	if c.OwnerName != nil {
		info.OwnerName = *c.OwnerName
	}
	return info
}
