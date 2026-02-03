package resources

import (
	"context"
	"fmt"
	"strings"

	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/resolvers"
	"frameworks/pkg/ctxkeys"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterNodeResources registers node/infrastructure-related MCP resources.
func RegisterNodeResources(server *mcp.Server, clients *clients.ServiceClients, resolver *resolvers.Resolver, logger logging.Logger) {
	// nodes://list - List infrastructure nodes
	server.AddResource(&mcp.Resource{
		URI:         "nodes://list",
		Name:        "Infrastructure Nodes",
		Description: "List of infrastructure nodes in your clusters.",
		MIMEType:    "application/json",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return handleNodesList(ctx, clients, logger)
	})

	// nodes://{id} - Node details
	server.AddResourceTemplate(&mcp.ResourceTemplate{
		URITemplate: "nodes://{id}",
		Name:        "Node Details",
		Description: "Details for a specific infrastructure node by ID.",
		MIMEType:    "application/json",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return HandleNodeByID(ctx, req.Params.URI, clients, logger)
	})
}

// NodeInfo represents an infrastructure node.
type NodeInfo struct {
	ID      string `json:"id"`
	NodeID  string `json:"node_id"`
	Name    string `json:"name"`
	Type    string `json:"type,omitempty"` // origin, edge, transcoder
	Region  string `json:"region,omitempty"`
	Cluster string `json:"cluster_id,omitempty"`
}

// NodesListResponse represents the nodes://list response.
type NodesListResponse struct {
	Nodes   []NodeInfo `json:"nodes"`
	HasMore bool       `json:"has_more"`
}

func handleNodesList(ctx context.Context, clients *clients.ServiceClients, logger logging.Logger) (*mcp.ReadResourceResult, error) {
	if ctxkeys.GetTenantID(ctx) == "" {
		return nil, fmt.Errorf("not authenticated")
	}

	// Build pagination request
	pagination := &pb.CursorPaginationRequest{
		First: 50,
	}

	// Get nodes from Quartermaster (empty strings for no filter)
	resp, err := clients.Quartermaster.ListNodes(ctx, "", "", "", pagination)
	if err != nil {
		logger.WithError(err).Debug("Failed to list nodes")
		return nil, fmt.Errorf("failed to list nodes: %w", err)
	}

	nodes := make([]NodeInfo, 0, len(resp.Nodes))
	for _, n := range resp.Nodes {
		info := NodeInfo{
			ID:      n.Id,
			NodeID:  n.NodeId,
			Name:    n.NodeName,
			Type:    n.NodeType,
			Cluster: n.ClusterId,
		}
		if n.Region != nil {
			info.Region = *n.Region
		}
		nodes = append(nodes, info)
	}

	hasMore := resp.Pagination != nil && resp.Pagination.HasNextPage
	return marshalResourceResult("nodes://list", NodesListResponse{
		Nodes:   nodes,
		HasMore: hasMore,
	})
}

// HandleNodeByID handles requests for nodes://{id} resources.
func HandleNodeByID(ctx context.Context, uri string, clients *clients.ServiceClients, logger logging.Logger) (*mcp.ReadResourceResult, error) {
	if ctxkeys.GetTenantID(ctx) == "" {
		return nil, fmt.Errorf("not authenticated")
	}

	// Extract node ID from URI: nodes://{id}
	nodeID := strings.TrimPrefix(uri, "nodes://")
	if nodeID == "" || nodeID == "list" {
		return nil, fmt.Errorf("invalid node ID")
	}

	// Get node details from Quartermaster
	node, err := clients.Quartermaster.GetNode(ctx, nodeID)
	if err != nil {
		return nil, fmt.Errorf("node not found: %w", err)
	}

	// GetNode returns *pb.NodeResponse which has a Node field
	if node.Node == nil {
		return nil, fmt.Errorf("node not found")
	}

	info := NodeInfo{
		ID:      node.Node.Id,
		NodeID:  node.Node.NodeId,
		Name:    node.Node.NodeName,
		Type:    node.Node.NodeType,
		Cluster: node.Node.ClusterId,
	}
	if node.Node.Region != nil {
		info.Region = *node.Node.Region
	}

	return marshalResourceResult(uri, info)
}
