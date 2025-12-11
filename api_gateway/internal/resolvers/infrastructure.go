package resolvers

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/demo"
	"frameworks/api_gateway/internal/middleware"
	"frameworks/pkg/pagination"
	pb "frameworks/pkg/proto"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// timestamppbNew is a helper to create timestamppb.Timestamp from time.Time
func timestamppbNew(t time.Time) *timestamppb.Timestamp {
	return timestamppb.New(t)
}

// convertStringToNodeStatus converts a string status to NodeStatus enum
func convertStringToNodeStatus(status string) model.NodeStatus {
	switch status {
	case "healthy", "online", "active":
		return model.NodeStatusHealthy
	case "degraded", "warning":
		return model.NodeStatusDegraded
	case "unhealthy", "offline", "error", "failed":
		return model.NodeStatusUnhealthy
	default:
		return model.NodeStatusUnhealthy // Default to unhealthy for unknown statuses
	}
}

// Infrastructure pagination constants
const (
	infraDefaultLimit = 100
	infraMaxLimit     = 500
)

// convertToCursorPagination converts old PaginationInput to cursor-based pagination
// Note: This provides backwards compatibility - new code should use cursor pagination directly
func convertToCursorPagination(p *model.PaginationInput) *pb.CursorPaginationRequest {
	if p == nil {
		return nil
	}
	limit := infraDefaultLimit
	if p.Limit != nil {
		limit = *p.Limit
		if limit < 1 {
			limit = 1
		}
		if limit > infraMaxLimit {
			limit = infraMaxLimit
		}
	}
	return &pb.CursorPaginationRequest{
		First: int32(limit),
	}
}

// DoGetTenant returns tenant information
func (r *Resolver) DoGetTenant(ctx context.Context) (*pb.Tenant, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo tenant data")
		return demo.GenerateTenant(), nil
	}

	tenantID, ok := ctx.Value("tenant_id").(string)
	if !ok {
		return nil, fmt.Errorf("tenant context required")
	}

	r.Logger.WithField("tenant_id", tenantID).Info("Getting tenant info")

	// Get tenant from Quartermaster gRPC
	resp, err := r.Clients.Quartermaster.GetTenant(ctx, tenantID)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get tenant")
		return nil, fmt.Errorf("failed to get tenant: %w", err)
	}

	if resp.Tenant == nil {
		return nil, fmt.Errorf("tenant not found")
	}

	return resp.Tenant, nil
}

// DoGetClusters returns available clusters
func (r *Resolver) DoGetClusters(ctx context.Context, paginationInput *model.PaginationInput) ([]*pb.InfrastructureCluster, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo cluster data")
		now := time.Now()
		return []*pb.InfrastructureCluster{
			{
				Id:           "cluster_demo_us_west",
				ClusterId:    "cluster_demo_us_west",
				ClusterName:  "US West (Oregon)",
				ClusterType:  "us-west-2",
				HealthStatus: "HEALTHY",
				CreatedAt:    timestamppbNew(now.Add(-30 * 24 * time.Hour)),
			},
			{
				Id:           "cluster_demo_eu_west",
				ClusterId:    "cluster_demo_eu_west",
				ClusterName:  "EU West (Ireland)",
				ClusterType:  "eu-west-1",
				HealthStatus: "HEALTHY",
				CreatedAt:    timestamppbNew(now.Add(-45 * 24 * time.Hour)),
			},
		}, nil
	}

	r.Logger.Info("Getting clusters")

	// Get clusters from Quartermaster gRPC
	clustersResp, err := r.Clients.Quartermaster.ListClusters(ctx, convertToCursorPagination(paginationInput))
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get clusters")
		return nil, fmt.Errorf("failed to get clusters: %w", err)
	}

	return clustersResp.Clusters, nil
}

// DoGetCluster returns a specific cluster by ID
func (r *Resolver) DoGetCluster(ctx context.Context, id string) (*pb.InfrastructureCluster, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo cluster data for ID", id)
		clusters, _ := r.DoGetClusters(ctx, nil)
		for _, cluster := range clusters {
			if cluster.Id == id {
				return cluster, nil
			}
		}
		return nil, fmt.Errorf("demo cluster not found")
	}

	r.Logger.WithField("cluster_id", id).Info("Getting cluster")

	// Get cluster from Quartermaster gRPC
	clusterResp, err := r.Clients.Quartermaster.GetCluster(ctx, id)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get cluster")
		return nil, fmt.Errorf("failed to get cluster: %w", err)
	}

	return clusterResp.Cluster, nil
}

// DoGetNodes returns infrastructure nodes
func (r *Resolver) DoGetNodes(ctx context.Context, clusterID *string, status *model.NodeStatus, typeArg *string, tag *string, paginationInput *model.PaginationInput) ([]*pb.InfrastructureNode, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo node data")
		now := time.Now()
		region1 := "us-west-2a"
		region2 := "us-west-2b"
		ip1 := "10.0.1.15"
		ip2 := "10.0.2.23"
		return []*pb.InfrastructureNode{
			{
				Id:            "node_demo_us_west_01",
				NodeId:        "node_demo_us_west_01",
				ClusterId:     "cluster_demo_us_west",
				NodeName:      "streaming-node-01",
				NodeType:      "streaming",
				Status:        "HEALTHY",
				Region:        &region1,
				InternalIp:    &ip1,
				LastHeartbeat: timestamppbNew(now.Add(-2 * time.Minute)),
				CreatedAt:     timestamppbNew(now.Add(-30 * 24 * time.Hour)),
			},
			{
				Id:            "node_demo_us_west_02",
				NodeId:        "node_demo_us_west_02",
				ClusterId:     "cluster_demo_us_west",
				NodeName:      "transcoding-node-01",
				NodeType:      "transcoding",
				Status:        "HEALTHY",
				Region:        &region2,
				InternalIp:    &ip2,
				LastHeartbeat: timestamppbNew(now.Add(-1 * time.Minute)),
				CreatedAt:     timestamppbNew(now.Add(-25 * time.Hour)),
			},
		}, nil
	}

	r.Logger.Info("Getting nodes")

	// Build filter parameters for gRPC
	clusterFilter := ""
	if clusterID != nil {
		clusterFilter = *clusterID
	}
	typeFilter := ""
	if typeArg != nil {
		typeFilter = *typeArg
	}
	// Note: status and tag filters can be added when proto supports them

	// Get nodes from Quartermaster gRPC
	nodesResp, err := r.Clients.Quartermaster.ListNodes(ctx, clusterFilter, typeFilter, "", convertToCursorPagination(paginationInput))
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get nodes")
		return nil, fmt.Errorf("failed to get nodes: %w", err)
	}

	return nodesResp.Nodes, nil
}

// DoGetServiceInstances returns service instances
func (r *Resolver) DoGetServiceInstances(ctx context.Context, clusterID *string, nodeID *string, status *model.InstanceStatus, paginationInput *model.PaginationInput) ([]*pb.ServiceInstance, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo service instances")
		return demo.GenerateServiceInstances(), nil
	}

	// Build filter parameters for gRPC
	clusterFilter := ""
	if clusterID != nil {
		clusterFilter = *clusterID
	}
	nodeFilter := ""
	if nodeID != nil {
		nodeFilter = *nodeID
	}
	// Note: status filter can be added when proto supports it

	// Get service instances from Quartermaster gRPC
	resp, err := r.Clients.Quartermaster.ListServiceInstances(ctx, clusterFilter, "", nodeFilter, convertToCursorPagination(paginationInput))
	if err != nil {
		return nil, fmt.Errorf("failed to get service instances: %w", err)
	}

	return resp.Instances, nil
}

// DoGetNode returns a specific node by ID
func (r *Resolver) DoGetNode(ctx context.Context, id string) (*pb.InfrastructureNode, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo node data for ID", id)
		nodes, _ := r.DoGetNodes(ctx, nil, nil, nil, nil, nil)
		for _, node := range nodes {
			if node.Id == id {
				return node, nil
			}
		}
		return nil, fmt.Errorf("demo node not found")
	}

	r.Logger.WithField("node_id", id).Info("Getting node")

	// Get node from Quartermaster gRPC
	nodeResp, err := r.Clients.Quartermaster.GetNode(ctx, id)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get node")
		return nil, fmt.Errorf("failed to get node: %w", err)
	}

	return nodeResp.Node, nil
}

// DoDiscoverServices discovers running service instances by service type and optional cluster
func (r *Resolver) DoDiscoverServices(ctx context.Context, serviceType string, clusterID *string, paginationInput *model.PaginationInput) ([]*pb.ServiceInstance, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo discovered services for type", serviceType)
		// Filter demo service instances by service type
		allInstances := demo.GenerateServiceInstances()
		var filtered []*pb.ServiceInstance
		for _, inst := range allInstances {
			if inst.ServiceId == serviceType || inst.InstanceId == serviceType || serviceType == "" {
				filtered = append(filtered, inst)
			}
		}
		return filtered, nil
	}

	clusterFilter := ""
	if clusterID != nil {
		clusterFilter = *clusterID
	}

	resp, err := r.Clients.Quartermaster.DiscoverServices(ctx, serviceType, clusterFilter, convertToCursorPagination(paginationInput))
	if err != nil {
		return nil, fmt.Errorf("failed to discover services: %w", err)
	}
	if resp == nil {
		return []*pb.ServiceInstance{}, nil
	}
	return resp.Instances, nil
}

// DoGetClustersAccess returns clusters the current tenant can access
func (r *Resolver) DoGetClustersAccess(ctx context.Context, paginationInput *model.PaginationInput) ([]*model.ClusterAccess, error) {
	if middleware.IsDemoMode(ctx) {
		// Simple demo response with a single shared cluster
		return []*model.ClusterAccess{
			{ClusterID: "cluster_demo_us_west", ClusterName: "US West (Oregon)", AccessLevel: "shared"},
		}, nil
	}

	tenantID, ok := ctx.Value("tenant_id").(string)
	if !ok {
		return nil, fmt.Errorf("tenant context required")
	}

	resp, err := r.Clients.Quartermaster.ListClustersForTenant(ctx, tenantID, convertToCursorPagination(paginationInput))
	if err != nil {
		return nil, fmt.Errorf("failed to get clusters access: %w", err)
	}
	if resp == nil {
		return []*model.ClusterAccess{}, nil
	}

	out := make([]*model.ClusterAccess, 0, len(resp.Clusters))
	for _, c := range resp.Clusters {
		item := &model.ClusterAccess{
			ClusterID:   c.ClusterId,
			ClusterName: c.ClusterName,
			AccessLevel: c.AccessLevel,
		}
		// ResourceLimits: GraphQL JSON scalar maps to *string in generated models.
		// Serialize from proto struct if present
		if c.ResourceLimits != nil {
			if b, err := json.Marshal(c.ResourceLimits.AsMap()); err == nil {
				s := string(b)
				item.ResourceLimits = &s
			}
		}
		out = append(out, item)
	}
	return out, nil
}

// DoGetClustersAvailable returns clusters available for onboarding UX
func (r *Resolver) DoGetClustersAvailable(ctx context.Context, paginationInput *model.PaginationInput) ([]*model.AvailableCluster, error) {
	if middleware.IsDemoMode(ctx) {
		return []*model.AvailableCluster{
			{ClusterID: "cluster_demo_us_west", ClusterName: "US West (Oregon)", Tiers: []string{"free"}, AutoEnroll: true},
		}, nil
	}

	resp, err := r.Clients.Quartermaster.ListClustersAvailable(ctx, convertToCursorPagination(paginationInput))
	if err != nil {
		return nil, fmt.Errorf("failed to get clusters available: %w", err)
	}
	if resp == nil {
		return []*model.AvailableCluster{}, nil
	}

	out := make([]*model.AvailableCluster, 0, len(resp.Clusters))
	for _, c := range resp.Clusters {
		item := &model.AvailableCluster{
			ClusterID:   c.ClusterId,
			ClusterName: c.ClusterName,
			Tiers:       c.Tiers,
			AutoEnroll:  c.AutoEnroll,
		}
		out = append(out, item)
	}
	return out, nil
}

// DoUpdateTenant updates tenant settings
func (r *Resolver) DoUpdateTenant(ctx context.Context, input model.UpdateTenantInput) (*pb.Tenant, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo tenant update")
		tenant := demo.GenerateTenant()
		// Apply updates to demo tenant
		if input.Name != nil {
			tenant.Name = *input.Name
		}
		return tenant, nil
	}

	tenantID, ok := ctx.Value("tenant_id").(string)
	if !ok {
		return nil, fmt.Errorf("tenant context required")
	}

	r.Logger.WithField("tenant_id", tenantID).Info("Updating tenant")

	// Build gRPC update request
	updateReq := &pb.UpdateTenantRequest{
		TenantId: tenantID,
	}
	updates := 0

	if input.Name != nil {
		trimmed := strings.TrimSpace(*input.Name)
		if trimmed != "" {
			updateReq.Name = &trimmed
			updates++
		}
	}

	if input.Settings != nil {
		var raw map[string]interface{}
		if err := json.Unmarshal([]byte(*input.Settings), &raw); err != nil {
			return nil, fmt.Errorf("invalid settings JSON: %w", err)
		}

		if val, ok := raw["primaryClusterId"].(string); ok && strings.TrimSpace(val) != "" {
			clusterID := strings.TrimSpace(val)
			updateReq.PrimaryClusterId = &clusterID
			updates++
		}

		if val, ok := raw["deploymentModel"].(string); ok && strings.TrimSpace(val) != "" {
			deployModel := strings.TrimSpace(val)
			updateReq.DeploymentModel = &deployModel
			updates++
		}
	}

	if updates == 0 {
		r.Logger.WithField("tenant_id", tenantID).Debug("No tenant updates requested")
		return r.DoGetTenant(ctx)
	}

	_, err := r.Clients.Quartermaster.UpdateTenant(ctx, updateReq)
	if err != nil {
		r.Logger.WithError(err).WithField("tenant_id", tenantID).Error("Failed to update tenant")
		return nil, fmt.Errorf("failed to update tenant: %w", err)
	}

	return r.DoGetTenant(ctx)
}

// DoUpdateStream updates stream settings
func (r *Resolver) DoUpdateStream(ctx context.Context, id string, input model.UpdateStreamInput) (*pb.Stream, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo stream update")
		streams := demo.GenerateStreams()
		for _, stream := range streams {
			if stream.InternalName == id {
				// Apply updates to demo stream
				if input.Name != nil {
					stream.Title = *input.Name
				}
				if input.Description != nil {
					stream.Description = *input.Description
				}
				if input.Record != nil {
					stream.IsRecording = *input.Record
				}
				return stream, nil
			}
		}
		return nil, fmt.Errorf("demo stream not found")
	}

	tenantID, _ := ctx.Value("tenant_id").(string)

	r.Logger.WithField("tenant_id", tenantID).
		WithField("stream_id", id).
		Info("Updating stream")

	// Build gRPC request
	req := &pb.UpdateStreamRequest{
		StreamId: id,
	}

	// Handle optional fields
	if input.Name != nil {
		req.Name = input.Name
	}
	if input.Description != nil {
		req.Description = input.Description
	}
	if input.Record != nil {
		req.Record = input.Record
	}

	// Call Commodore gRPC (context metadata carries auth)
	stream, err := r.Clients.Commodore.UpdateStream(ctx, req)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to update stream")
		return nil, fmt.Errorf("failed to update stream: %w", err)
	}

	return stream, nil
}

// DoGetClustersConnection returns a Relay-style connection for clusters
func (r *Resolver) DoGetClustersConnection(ctx context.Context, first *int, after *string, last *int, before *string) (*model.ClustersConnection, error) {
	// TODO: Implement bidirectional keyset pagination once Quartermaster supports it
	_ = last
	_ = before

	limit := pagination.DefaultLimit
	offset := 0

	if first != nil {
		limit = pagination.ClampLimit(*first)
	}

	// Convert to pagination input format
	paginationInput := &model.PaginationInput{
		Limit:  &limit,
		Offset: &offset,
	}

	clusters, err := r.DoGetClusters(ctx, paginationInput)
	if err != nil {
		return nil, err
	}

	// Build edges
	edges := make([]*model.ClusterEdge, len(clusters))
	for i, cluster := range clusters {
		cursor := pagination.EncodeIndexCursor(offset + i)
		edges[i] = &model.ClusterEdge{
			Cursor: cursor,
			Node:   cluster,
		}
	}

	// Determine hasNextPage (if we got a full page, there might be more)
	hasMore := len(clusters) == limit

	pageInfo := &model.PageInfo{
		HasPreviousPage: offset > 0,
		HasNextPage:     hasMore,
	}
	if len(edges) > 0 {
		firstCursor := pagination.EncodeIndexCursor(offset)
		lastCursor := pagination.EncodeIndexCursor(offset + len(clusters) - 1)
		pageInfo.StartCursor = &firstCursor
		pageInfo.EndCursor = &lastCursor
	}

	return &model.ClustersConnection{
		Edges:      edges,
		PageInfo:   pageInfo,
		TotalCount: len(clusters),
	}, nil
}

// DoGetNodesConnection returns a Relay-style connection for nodes
func (r *Resolver) DoGetNodesConnection(ctx context.Context, clusterID *string, status *model.NodeStatus, typeArg *string, first *int, after *string, last *int, before *string) (*model.NodesConnection, error) {
	// TODO: Implement bidirectional keyset pagination once Quartermaster supports it
	_ = last
	_ = before

	limit := pagination.DefaultLimit
	offset := 0

	if first != nil {
		limit = pagination.ClampLimit(*first)
	}

	// Convert to pagination input format
	paginationInput := &model.PaginationInput{
		Limit:  &limit,
		Offset: &offset,
	}

	nodes, err := r.DoGetNodes(ctx, clusterID, status, typeArg, nil, paginationInput)
	if err != nil {
		return nil, err
	}

	// Build edges
	edges := make([]*model.NodeEdge, len(nodes))
	for i, node := range nodes {
		cursor := pagination.EncodeIndexCursor(offset + i)
		edges[i] = &model.NodeEdge{
			Cursor: cursor,
			Node:   node,
		}
	}

	// Determine hasNextPage
	hasMore := len(nodes) == limit

	pageInfo := &model.PageInfo{
		HasPreviousPage: offset > 0,
		HasNextPage:     hasMore,
	}
	if len(edges) > 0 {
		firstCursor := pagination.EncodeIndexCursor(offset)
		lastCursor := pagination.EncodeIndexCursor(offset + len(nodes) - 1)
		pageInfo.StartCursor = &firstCursor
		pageInfo.EndCursor = &lastCursor
	}

	return &model.NodesConnection{
		Edges:      edges,
		PageInfo:   pageInfo,
		TotalCount: len(nodes),
	}, nil
}

// DoGetServiceInstancesConnection returns a Relay-style connection for service instances
func (r *Resolver) DoGetServiceInstancesConnection(ctx context.Context, clusterID *string, nodeID *string, status *model.InstanceStatus, first *int, after *string, last *int, before *string) (*model.ServiceInstancesConnection, error) {
	// TODO: Implement bidirectional keyset pagination once Quartermaster supports it
	_ = last
	_ = before

	limit := pagination.DefaultLimit
	offset := 0

	if first != nil {
		limit = pagination.ClampLimit(*first)
	}

	// Convert to pagination input format
	paginationInput := &model.PaginationInput{
		Limit:  &limit,
		Offset: &offset,
	}

	instances, err := r.DoGetServiceInstances(ctx, clusterID, nodeID, status, paginationInput)
	if err != nil {
		return nil, err
	}

	// Build edges
	edges := make([]*model.ServiceInstanceEdge, len(instances))
	for i, instance := range instances {
		cursor := pagination.EncodeIndexCursor(offset + i)
		edges[i] = &model.ServiceInstanceEdge{
			Cursor: cursor,
			Node:   instance,
		}
	}

	// Determine hasNextPage
	hasMore := len(instances) == limit

	pageInfo := &model.PageInfo{
		HasPreviousPage: offset > 0,
		HasNextPage:     hasMore,
	}
	if len(edges) > 0 {
		firstCursor := pagination.EncodeIndexCursor(offset)
		lastCursor := pagination.EncodeIndexCursor(offset + len(instances) - 1)
		pageInfo.StartCursor = &firstCursor
		pageInfo.EndCursor = &lastCursor
	}

	return &model.ServiceInstancesConnection{
		Edges:      edges,
		PageInfo:   pageInfo,
		TotalCount: len(instances),
	}, nil
}

// DoSubscribeToCluster subscribes a tenant to a cluster
func (r *Resolver) DoSubscribeToCluster(ctx context.Context, clusterID string) (bool, error) {
	tenantID := ""
	if user := middleware.GetUserFromContext(ctx); user != nil {
		tenantID = user.TenantID
	}
	if tenantID == "" {
		return false, fmt.Errorf("tenant context required")
	}

	_, err := r.Clients.Quartermaster.SubscribeToCluster(ctx, &pb.SubscribeToClusterRequest{
		TenantId:  tenantID,
		ClusterId: clusterID,
	})
	if err != nil {
		return false, fmt.Errorf("failed to subscribe: %w", err)
	}
	return true, nil
}

// DoUnsubscribeFromCluster unsubscribes a tenant from a cluster
func (r *Resolver) DoUnsubscribeFromCluster(ctx context.Context, clusterID string) (bool, error) {
	tenantID := ""
	if user := middleware.GetUserFromContext(ctx); user != nil {
		tenantID = user.TenantID
	}
	if tenantID == "" {
		return false, fmt.Errorf("tenant context required")
	}

	_, err := r.Clients.Quartermaster.UnsubscribeFromCluster(ctx, &pb.UnsubscribeFromClusterRequest{
		TenantId:  tenantID,
		ClusterId: clusterID,
	})
	if err != nil {
		return false, fmt.Errorf("failed to unsubscribe: %w", err)
	}
	return true, nil
}

// DoListMySubscriptions lists clusters the tenant is subscribed to
func (r *Resolver) DoListMySubscriptions(ctx context.Context, paginationInput *model.PaginationInput) ([]*pb.InfrastructureCluster, error) {
	tenantID := ""
	if user := middleware.GetUserFromContext(ctx); user != nil {
		tenantID = user.TenantID
	}
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	resp, err := r.Clients.Quartermaster.ListMySubscriptions(ctx, &pb.ListMySubscriptionsRequest{
		TenantId:   tenantID,
		Pagination: convertToCursorPagination(paginationInput),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list subscriptions: %w", err)
	}
	return resp.Clusters, nil
}

// DoCheckIsSubscribed checks if the current tenant is subscribed to the cluster
func (r *Resolver) DoCheckIsSubscribed(ctx context.Context, cluster *pb.InfrastructureCluster) (bool, error) {
	tenantID := ""
	if user := middleware.GetUserFromContext(ctx); user != nil {
		tenantID = user.TenantID
	}
	if tenantID == "" {
		return false, nil
	}

	// If owner, not a subscriber
	if cluster.OwnerTenantId != nil && *cluster.OwnerTenantId == tenantID {
		return false, nil
	}

	// If visible (we have the object) and not owner, it implies subscription/access
	return true, nil
}
