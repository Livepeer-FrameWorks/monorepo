package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/mcp/preflight"
	"frameworks/api_gateway/internal/resolvers"
	"frameworks/pkg/ctxkeys"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterInfrastructureTools registers cluster and marketplace MCP tools.
func RegisterInfrastructureTools(server *mcp.Server, serviceClients *clients.ServiceClients, resolver *resolvers.Resolver, checker *preflight.Checker, logger logging.Logger) {
	mcp.AddTool(server,
		&mcp.Tool{
			Name: "browse_marketplace",
			Description: `Browse available infrastructure clusters in the marketplace.

Returns clusters with pricing, capacity, utilization, and whether you can connect. Each cluster represents a geographic region or operator-provided infrastructure.

After finding a cluster, use subscribe_to_cluster to connect. Then use set_preferred_cluster to route your traffic there.

Pricing models: FREE_UNMETERED (no cost), TIER_INHERIT (follows billing tier), METERED (pay per use), MONTHLY (fixed fee).`,
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args BrowseMarketplaceInput) (*mcp.CallToolResult, any, error) {
			return handleBrowseMarketplace(ctx, args, resolver, logger)
		},
	)

	mcp.AddTool(server,
		&mcp.Tool{
			Name: "subscribe_to_cluster",
			Description: `Subscribe to a cluster for delivery or self-hosted edge enrollment.

Some clusters allow instant connection; others require approval from the operator. If approval is required, the status will be PENDING_APPROVAL until the operator approves.

After subscribing, use set_preferred_cluster to route your traffic to the new cluster. To provision self-hosted edges, use create_private_cluster or get an enrollment token from the cluster operator.`,
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args SubscribeToClusterInput) (*mcp.CallToolResult, any, error) {
			return handleSubscribeToCluster(ctx, args, resolver, serviceClients, logger)
		},
	)

	mcp.AddTool(server,
		&mcp.Tool{
			Name: "set_preferred_cluster",
			Description: `Set your preferred cluster for DNS steering.

All new streams will ingest to and play from this cluster's edges. Requires an active subscription to the target cluster.

Your preferred cluster maintains an always-on peering connection with your official (billing-tier) cluster. Other subscribed clusters peer on demand when streams trigger it.`,
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args SetPreferredClusterInput) (*mcp.CallToolResult, any, error) {
			return handleSetPreferredCluster(ctx, args, resolver, logger)
		},
	)

	mcp.AddTool(server,
		&mcp.Tool{
			Name: "create_private_cluster",
			Description: `Create a private cluster for self-hosted edge nodes.

Returns a bootstrap token for edge enrollment. Use it with the CLI:
  frameworks edge provision --enrollment-token <token> --ssh user@host

The token is shown once — save it securely. Each edge you provision joins this cluster and is automatically assigned a domain and TLS certificate.`,
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args CreatePrivateClusterInput) (*mcp.CallToolResult, any, error) {
			return handleCreatePrivateCluster(ctx, args, resolver, logger)
		},
	)
}

// --- Input types ---

type BrowseMarketplaceInput struct {
	PricingModel string `json:"pricing_model,omitempty" jsonschema_description:"Filter by pricing model: FREE_UNMETERED, TIER_INHERIT, METERED, MONTHLY. Leave empty for all."`
}

type SubscribeToClusterInput struct {
	ClusterID   string  `json:"cluster_id" jsonschema:"required" jsonschema_description:"The cluster ID to subscribe to. Get IDs from browse_marketplace."`
	InviteToken *string `json:"invite_token,omitempty" jsonschema_description:"Optional invite token if the cluster requires one."`
}

type SetPreferredClusterInput struct {
	ClusterID string `json:"cluster_id" jsonschema:"required" jsonschema_description:"The cluster ID to set as preferred. Must be a cluster you are subscribed to."`
}

type CreatePrivateClusterInput struct {
	ClusterName string  `json:"cluster_name" jsonschema:"required" jsonschema_description:"Human-readable name for the new cluster."`
	Region      *string `json:"region,omitempty" jsonschema_description:"Geographic region (e.g. us-east, eu-west). Affects DNS and default edge assignment."`
}

// --- Result types ---

type MarketplaceResult struct {
	Clusters []MarketplaceClusterResult `json:"clusters"`
	Count    int                        `json:"count"`
}

type MarketplaceClusterResult struct {
	ClusterID        string  `json:"cluster_id"`
	ClusterName      string  `json:"cluster_name"`
	Description      string  `json:"description,omitempty"`
	PricingModel     string  `json:"pricing_model"`
	MonthlyPrice     int32   `json:"monthly_price_cents,omitempty"`
	RequiresApproval bool    `json:"requires_approval"`
	IsSubscribed     bool    `json:"is_subscribed"`
	IsEligible       bool    `json:"is_eligible"`
	Utilization      float64 `json:"utilization,omitempty"`
	OwnerName        string  `json:"owner_name,omitempty"`
}

type SubscriptionResult struct {
	Status    string `json:"status"`
	Message   string `json:"message"`
	ClusterID string `json:"cluster_id,omitempty"`
}

type PreferredClusterResult struct {
	ClusterID   string `json:"cluster_id"`
	ClusterName string `json:"cluster_name"`
	Message     string `json:"message"`
}

type PrivateClusterResult struct {
	ClusterID      string `json:"cluster_id"`
	ClusterName    string `json:"cluster_name"`
	BootstrapToken string `json:"bootstrap_token"`
	Message        string `json:"message"`
}

// --- Handlers ---

func handleBrowseMarketplace(ctx context.Context, args BrowseMarketplaceInput, resolver *resolvers.Resolver, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	first := 50
	clusters, err := resolver.DoListMarketplaceClusters(ctx, &first, nil)
	if err != nil {
		return toolError(fmt.Sprintf("Failed to browse marketplace: %v", err))
	}

	results := make([]MarketplaceClusterResult, 0, len(clusters))
	for _, c := range clusters {
		if args.PricingModel != "" && c.PricingModel.String() != args.PricingModel {
			continue
		}
		r := MarketplaceClusterResult{
			ClusterID:        c.ClusterId,
			ClusterName:      c.ClusterName,
			PricingModel:     c.PricingModel.String(),
			MonthlyPrice:     c.MonthlyPriceCents,
			RequiresApproval: c.RequiresApproval,
			IsSubscribed:     c.IsSubscribed,
			IsEligible:       c.IsEligible,
		}
		if c.ShortDescription != nil {
			r.Description = *c.ShortDescription
		}
		if c.CurrentUtilization != nil {
			r.Utilization = *c.CurrentUtilization
		}
		if c.OwnerName != nil {
			r.OwnerName = *c.OwnerName
		}
		results = append(results, r)
	}

	return infraToolSuccessJSON(MarketplaceResult{
		Clusters: results,
		Count:    len(results),
	})
}

func handleSubscribeToCluster(ctx context.Context, args SubscribeToClusterInput, resolver *resolvers.Resolver, serviceClients *clients.ServiceClients, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return toolError("Authentication required")
	}

	result, err := resolver.DoRequestClusterSubscription(ctx, args.ClusterID, args.InviteToken)
	if err != nil {
		return toolError(fmt.Sprintf("Failed to subscribe: %v", err))
	}

	switch v := result.(type) {
	case *pb.ClusterSubscription:
		status := v.SubscriptionStatus.String()
		msg := "Subscription active"
		if v.SubscriptionStatus == pb.ClusterSubscriptionStatus_SUBSCRIPTION_STATUS_PENDING_APPROVAL {
			msg = "Subscription request submitted. Waiting for cluster operator approval."
		}
		return infraToolSuccessJSON(SubscriptionResult{
			Status:    status,
			Message:   msg,
			ClusterID: v.ClusterId,
		})
	case *model.ValidationError:
		return toolError(v.Message)
	case *model.AuthError:
		return toolError(v.Message)
	case *model.NotFoundError:
		return toolError(v.Message)
	default:
		return toolError("Unexpected response from subscription request")
	}
}

func handleSetPreferredCluster(ctx context.Context, args SetPreferredClusterInput, resolver *resolvers.Resolver, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return toolError("Authentication required")
	}

	result, err := resolver.DoSetPreferredCluster(ctx, args.ClusterID)
	if err != nil {
		return toolError(fmt.Sprintf("Failed to set preferred cluster: %v", err))
	}

	switch v := result.(type) {
	case *pb.InfrastructureCluster:
		return infraToolSuccessJSON(PreferredClusterResult{
			ClusterID:   v.ClusterId,
			ClusterName: v.ClusterName,
			Message:     fmt.Sprintf("Preferred cluster set to %s. DNS steering will route traffic to this cluster's edges. Your official cluster maintains always-on peering with this cluster.", v.ClusterName),
		})
	case *model.ValidationError:
		return toolError(v.Message)
	case *model.AuthError:
		return toolError(v.Message)
	case *model.NotFoundError:
		return toolError(v.Message)
	default:
		return toolError("Unexpected response")
	}
}

func handleCreatePrivateCluster(ctx context.Context, args CreatePrivateClusterInput, resolver *resolvers.Resolver, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return toolError("Authentication required")
	}

	input := model.CreatePrivateClusterInput{
		ClusterName: args.ClusterName,
		Region:      args.Region,
	}

	result, err := resolver.DoCreatePrivateCluster(ctx, input)
	if err != nil {
		return toolError(fmt.Sprintf("Failed to create cluster: %v", err))
	}

	switch v := result.(type) {
	case *pb.CreatePrivateClusterResponse:
		clusterID := ""
		clusterName := args.ClusterName
		if v.Cluster != nil {
			clusterID = v.Cluster.ClusterId
			if v.Cluster.ClusterName != "" {
				clusterName = v.Cluster.ClusterName
			}
		}
		token := ""
		if v.BootstrapToken != nil {
			token = v.BootstrapToken.Token
		}
		return infraToolSuccessJSON(PrivateClusterResult{
			ClusterID:      clusterID,
			ClusterName:    clusterName,
			BootstrapToken: token,
			Message:        fmt.Sprintf("Private cluster '%s' created. Save the bootstrap token — it is shown once. Provision edges with: frameworks edge provision --enrollment-token %s --ssh user@host", clusterName, token),
		})
	case *model.ValidationError:
		return toolError(v.Message)
	case *model.AuthError:
		return toolError(v.Message)
	default:
		return toolError("Unexpected response")
	}
}

func infraToolSuccessJSON(result interface{}) (*mcp.CallToolResult, any, error) {
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return toolError(fmt.Sprintf("Failed to format result: %v", err))
	}
	return &mcp.CallToolResult{
		Content: []mcp.Content{
			&mcp.TextContent{Text: string(data)},
		},
	}, result, nil
}
