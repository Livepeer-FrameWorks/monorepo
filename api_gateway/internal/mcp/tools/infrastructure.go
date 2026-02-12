package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

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
			Name: "create_enrollment_token",
			Description: `Generate an enrollment token for provisioning additional edge nodes into an existing private cluster.

Returns a single-use token valid for the specified TTL. Use it with the CLI to provision an edge:
  frameworks edge provision --enrollment-token <token> --ssh user@host

The token is shown once — save it securely. Each provisioned edge automatically gets a domain, TLS certificate, and joins the cluster's routing pool.

Requires an active subscription to the target cluster. Use create_private_cluster first if you don't have a cluster yet.`,
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args CreateEnrollmentTokenInput) (*mcp.CallToolResult, any, error) {
			return handleCreateEnrollmentToken(ctx, args, serviceClients, logger)
		},
	)

	mcp.AddTool(server,
		&mcp.Tool{
			Name: "get_node_info",
			Description: `Get registration info for a node from the control plane.

Returns the node's cluster, type, region, and hardware specs as stored in Quartermaster. This is static registration data — for real-time health and operational status, run the CLI directly on the edge node:
  frameworks edge status
  frameworks edge doctor

If you provisioned the edge, you're already local — no --ssh needed. Use --ssh user@host only when operating a remote node you didn't provision yourself.`,
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args GetNodeInfoInput) (*mcp.CallToolResult, any, error) {
			return handleGetNodeInfo(ctx, args, serviceClients, logger)
		},
	)

	mcp.AddTool(server,
		&mcp.Tool{
			Name: "manage_node",
			Description: `Guided workflow for managing edge node lifecycle.

Returns CLI commands for common node operations. All commands run on the edge node itself:
- Drain:       frameworks edge mode draining
- Maintenance: frameworks edge mode maintenance
- Restore:     frameworks edge mode normal
- Status:      frameworks edge status
- Diagnose:    frameworks edge doctor
- View logs:   frameworks edge logs

If you provisioned this edge, you're already local — run commands directly. For remote nodes, add --ssh user@host to any command.

Before setting maintenance, always drain first and wait for active viewers to reach 0. Mode changes flow through the Helmsman→Foghorn control stream — Foghorn validates and pushes the updated config back.

This tool returns node info and the relevant CLI commands. Execute them to perform the operation.`,
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args ManageNodeInput) (*mcp.CallToolResult, any, error) {
			return handleManageNode(ctx, args, serviceClients, logger)
		},
	)

	mcp.AddTool(server,
		&mcp.Tool{
			Name: "set_node_mode",
			Description: `Set a node's operational mode via the control plane (no SSH required).

Modes: normal (active routing), draining (no new viewers, existing finish), maintenance (fully isolated).

Always drain first and wait for active viewers to reach 0 before setting maintenance. Use get_node_health to check active_viewers count.

Mode changes propagate through the load balancer to the node's control stream. The routing effect is immediate — all balancer instances stop assigning new viewers to draining/maintenance nodes.`,
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args SetNodeModeInput) (*mcp.CallToolResult, any, error) {
			return handleSetNodeMode(ctx, args, serviceClients, logger)
		},
	)

	mcp.AddTool(server,
		&mcp.Tool{
			Name: "get_node_health",
			Description: `Get real-time health and routing state for a node from the load balancer.

Returns: operational mode, active viewers, active streams, CPU/memory/bandwidth, health status, last heartbeat.

Unlike get_node_info (static registration data from Quartermaster), this returns live operational state from Foghorn.`,
		},
		func(ctx context.Context, req *mcp.CallToolRequest, args GetNodeHealthInput) (*mcp.CallToolResult, any, error) {
			return handleGetNodeHealth(ctx, args, serviceClients, logger)
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

type CreateEnrollmentTokenInput struct {
	ClusterID string  `json:"cluster_id" jsonschema:"required" jsonschema_description:"The cluster to create an enrollment token for. Must have an active subscription."`
	Name      *string `json:"name,omitempty" jsonschema_description:"Display name for the token (e.g. 'us-east-edge-3')."`
	TTL       *string `json:"ttl,omitempty" jsonschema_description:"Token validity duration (e.g. '24h', '720h'). Defaults to 30 days."`
}

type GetNodeInfoInput struct {
	NodeID string `json:"node_id" jsonschema:"required" jsonschema_description:"The node ID to look up. Get IDs from the nodes://list resource."`
}

type ManageNodeInput struct {
	NodeID string `json:"node_id" jsonschema:"required" jsonschema_description:"The node ID. Get IDs from the nodes://list resource or get_node_info."`
	Action string `json:"action" jsonschema:"required" jsonschema_description:"The operation: drain, maintenance, restore, status, diagnose, logs."`
}

type SetNodeModeInput struct {
	NodeID string `json:"node_id" jsonschema:"required" jsonschema_description:"The node ID to change mode for."`
	Mode   string `json:"mode" jsonschema:"required" jsonschema_description:"Target mode: normal, draining, or maintenance."`
	Reason string `json:"reason,omitempty" jsonschema_description:"Reason for the mode change (for audit trail)."`
}

type GetNodeHealthInput struct {
	NodeID string `json:"node_id" jsonschema:"required" jsonschema_description:"The node ID to check health for."`
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

type EnrollmentTokenResult struct {
	Token     string `json:"token"`
	ClusterID string `json:"cluster_id"`
	Message   string `json:"message"`
}

type NodeInfoResult struct {
	NodeID        string `json:"node_id"`
	NodeName      string `json:"node_name"`
	NodeType      string `json:"node_type"`
	ClusterID     string `json:"cluster_id"`
	Region        string `json:"region,omitempty"`
	ExternalIP    string `json:"external_ip,omitempty"`
	CPUCores      int32  `json:"cpu_cores,omitempty"`
	MemoryGB      int32  `json:"memory_gb,omitempty"`
	DiskGB        int32  `json:"disk_gb,omitempty"`
	LastHeartbeat string `json:"last_heartbeat,omitempty"`
}

type ManageNodeResult struct {
	NodeID    string   `json:"node_id"`
	NodeName  string   `json:"node_name"`
	ClusterID string   `json:"cluster_id"`
	Action    string   `json:"action"`
	Commands  []string `json:"commands"`
	Message   string   `json:"message"`
}

type SetNodeModeResult struct {
	NodeID  string `json:"node_id"`
	Mode    string `json:"mode"`
	Message string `json:"message"`
}

type NodeHealthResult struct {
	NodeID            string   `json:"node_id"`
	Mode              string   `json:"operational_mode"`
	IsHealthy         bool     `json:"is_healthy"`
	ActiveViewers     int32    `json:"active_viewers"`
	ActiveStreams     int32    `json:"active_streams"`
	ClusterID         string   `json:"cluster_id"`
	LastHeartbeat     string   `json:"last_heartbeat,omitempty"`
	CPUPercent        float64  `json:"cpu_percent,omitempty"`
	RAMUsedMB         float64  `json:"ram_used_mb,omitempty"`
	RAMMaxMB          float64  `json:"ram_max_mb,omitempty"`
	BandwidthUpMbps   float64  `json:"bandwidth_up_mbps,omitempty"`
	BandwidthDownMbps float64  `json:"bandwidth_down_mbps,omitempty"`
	BWLimitMbps       float64  `json:"bw_limit_mbps,omitempty"`
	DiskTotalBytes    uint64   `json:"disk_total_bytes,omitempty"`
	DiskUsedBytes     uint64   `json:"disk_used_bytes,omitempty"`
	Location          string   `json:"location,omitempty"`
	Latitude          *float64 `json:"latitude,omitempty"`
	Longitude         *float64 `json:"longitude,omitempty"`
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

func handleCreateEnrollmentToken(ctx context.Context, args CreateEnrollmentTokenInput, serviceClients *clients.ServiceClients, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return toolError("Authentication required")
	}

	req := &pb.CreateEnrollmentTokenRequest{
		ClusterId: args.ClusterID,
		TenantId:  &tenantID,
	}
	if args.Name != nil {
		req.Name = args.Name
	}
	if args.TTL != nil {
		req.Ttl = args.TTL
	}

	resp, err := serviceClients.Quartermaster.CreateEnrollmentToken(ctx, req)
	if err != nil {
		return toolError(fmt.Sprintf("Failed to create enrollment token: %v", err))
	}

	token := ""
	if resp.Token != nil {
		token = resp.Token.Token
	}

	return infraToolSuccessJSON(EnrollmentTokenResult{
		Token:     token,
		ClusterID: args.ClusterID,
		Message:   fmt.Sprintf("Enrollment token created. Save it — shown once.\nProvision edges with:\n  frameworks edge provision --enrollment-token %s --ssh user@host", token),
	})
}

func handleGetNodeInfo(ctx context.Context, args GetNodeInfoInput, serviceClients *clients.ServiceClients, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return toolError("Authentication required")
	}

	resp, err := serviceClients.Quartermaster.GetNode(ctx, args.NodeID)
	if err != nil {
		return toolError(fmt.Sprintf("Node not found: %v", err))
	}

	node := resp.GetNode()
	if node == nil {
		return toolError("Node not found")
	}

	result := NodeInfoResult{
		NodeID:    node.NodeId,
		NodeName:  node.NodeName,
		NodeType:  node.NodeType,
		ClusterID: node.ClusterId,
	}
	if node.Region != nil {
		result.Region = *node.Region
	}
	if node.ExternalIp != nil {
		result.ExternalIP = *node.ExternalIp
	}
	if node.CpuCores != nil {
		result.CPUCores = *node.CpuCores
	}
	if node.MemoryGb != nil {
		result.MemoryGB = *node.MemoryGb
	}
	if node.DiskGb != nil {
		result.DiskGB = *node.DiskGb
	}
	if node.LastHeartbeat != nil {
		result.LastHeartbeat = node.LastHeartbeat.AsTime().Format(time.RFC3339)
	}

	return infraToolSuccessJSON(result)
}

func handleManageNode(ctx context.Context, args ManageNodeInput, serviceClients *clients.ServiceClients, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return toolError("Authentication required")
	}

	resp, err := serviceClients.Quartermaster.GetNode(ctx, args.NodeID)
	if err != nil {
		return toolError(fmt.Sprintf("Node not found: %v", err))
	}

	node := resp.GetNode()
	if node == nil {
		return toolError("Node not found")
	}

	// If agent provisioned this edge, it's local — no --ssh needed.
	// Include --ssh hint only as a comment for remote operators.
	sshHint := ""
	if node.ExternalIp != nil && *node.ExternalIp != "" {
		sshHint = fmt.Sprintf("  (remote: add --ssh user@%s)", *node.ExternalIp)
	}

	result := ManageNodeResult{
		NodeID:    node.NodeId,
		NodeName:  node.NodeName,
		ClusterID: node.ClusterId,
		Action:    args.Action,
	}

	switch args.Action {
	case "drain":
		result.Commands = []string{
			"frameworks edge mode draining",
			"frameworks edge status",
		}
		result.Message = fmt.Sprintf("Draining stops new viewer assignments. Existing viewers finish naturally. Check status to monitor active_viewers reaching 0 before setting maintenance.%s", sshHint)
	case "maintenance":
		result.Commands = []string{
			"frameworks edge mode maintenance",
		}
		result.Message = fmt.Sprintf("WARNING: Only set maintenance after draining completes (active_viewers = 0). Maintenance fully isolates the node from the routing pool.%s", sshHint)
	case "restore":
		result.Commands = []string{
			"frameworks edge mode normal",
		}
		result.Message = fmt.Sprintf("Restores the node to the active routing pool. New viewers and streams will be assigned.%s", sshHint)
	case "status":
		result.Commands = []string{
			"frameworks edge status",
		}
		result.Message = fmt.Sprintf("Shows real-time node status including active viewers, streams, and operational mode.%s", sshHint)
	case "diagnose":
		result.Commands = []string{
			"frameworks edge doctor",
			"frameworks edge logs",
		}
		result.Message = fmt.Sprintf("Doctor checks node health and connectivity. Logs show recent service output.%s", sshHint)
	case "logs":
		result.Commands = []string{
			"frameworks edge logs",
		}
		result.Message = fmt.Sprintf("Shows recent service logs from the edge node.%s", sshHint)
	default:
		return toolError(fmt.Sprintf("Unknown action: %s. Valid actions: drain, maintenance, restore, status, diagnose, logs", args.Action))
	}

	return infraToolSuccessJSON(result)
}

func handleSetNodeMode(ctx context.Context, args SetNodeModeInput, serviceClients *clients.ServiceClients, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return toolError("Authentication required")
	}

	mode := strings.ToLower(strings.TrimSpace(args.Mode))
	switch mode {
	case "normal", "draining", "maintenance":
	default:
		return toolError(fmt.Sprintf("Invalid mode: %s. Valid modes: normal, draining, maintenance", args.Mode))
	}

	reason := strings.TrimSpace(args.Reason)
	if reason == "" {
		reason = "mcp_tool"
	}

	resp, err := serviceClients.Commodore.SetNodeMode(ctx, &pb.SetNodeModeRequest{
		NodeId: args.NodeID,
		Mode:   mode,
		SetBy:  reason,
	})
	if err != nil {
		return toolError(fmt.Sprintf("Failed to set node mode: %v", err))
	}

	return infraToolSuccessJSON(SetNodeModeResult{
		NodeID:  resp.GetNodeId(),
		Mode:    resp.GetMode(),
		Message: resp.GetMessage(),
	})
}

func handleGetNodeHealth(ctx context.Context, args GetNodeHealthInput, serviceClients *clients.ServiceClients, logger logging.Logger) (*mcp.CallToolResult, any, error) {
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return toolError("Authentication required")
	}

	resp, err := serviceClients.Commodore.GetNodeHealth(ctx, &pb.GetNodeHealthRequest{
		NodeId: args.NodeID,
	})
	if err != nil {
		return toolError(fmt.Sprintf("Failed to get node health: %v", err))
	}

	result := NodeHealthResult{
		NodeID:            resp.GetNodeId(),
		Mode:              resp.GetOperationalMode(),
		IsHealthy:         resp.GetIsHealthy(),
		ActiveViewers:     resp.GetActiveViewers(),
		ActiveStreams:     resp.GetActiveStreams(),
		ClusterID:         resp.GetClusterId(),
		LastHeartbeat:     resp.GetLastHeartbeat(),
		CPUPercent:        resp.GetCpuPercent(),
		RAMUsedMB:         resp.GetRamUsedMb(),
		RAMMaxMB:          resp.GetRamMaxMb(),
		BandwidthUpMbps:   resp.GetBandwidthUpMbps(),
		BandwidthDownMbps: resp.GetBandwidthDownMbps(),
		BWLimitMbps:       resp.GetBwLimitMbps(),
		DiskTotalBytes:    resp.GetDiskTotalBytes(),
		DiskUsedBytes:     resp.GetDiskUsedBytes(),
		Location:          resp.GetLocation(),
	}
	if resp.Latitude != nil {
		lat := resp.GetLatitude()
		result.Latitude = &lat
	}
	if resp.Longitude != nil {
		lon := resp.GetLongitude()
		result.Longitude = &lon
	}
	return infraToolSuccessJSON(result)
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
