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

// buildCursorPagination creates bidirectional cursor pagination from Relay-style params
func buildCursorPagination(first *int, after *string, last *int, before *string) *pb.CursorPaginationRequest {
	req := &pb.CursorPaginationRequest{}

	// Forward pagination (first/after)
	if first != nil {
		limit := *first
		if limit < 1 {
			limit = 1
		}
		if limit > infraMaxLimit {
			limit = infraMaxLimit
		}
		req.First = int32(limit)
	} else if last == nil {
		// Default to forward pagination with default limit
		req.First = int32(infraDefaultLimit)
	}

	if after != nil && *after != "" {
		req.After = after
	}

	// Backward pagination (last/before)
	if last != nil {
		limit := *last
		if limit < 1 {
			limit = 1
		}
		if limit > infraMaxLimit {
			limit = infraMaxLimit
		}
		req.Last = int32(limit)
	}

	if before != nil && *before != "" {
		req.Before = before
	}

	return req
}

// DoGetTenant returns tenant information
func (r *Resolver) DoGetTenant(ctx context.Context) (*pb.Tenant, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo tenant data")
		return demo.GenerateTenant(), nil
	}

	var tenantID string
	if v, ok := ctx.Value("tenant_id").(string); ok {
		tenantID = v
	}
	if tenantID == "" {
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
func (r *Resolver) DoGetClusters(ctx context.Context, first *int, after *string) ([]*pb.InfrastructureCluster, error) {
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
	clustersResp, err := r.Clients.Quartermaster.ListClusters(ctx, buildCursorPagination(first, after, nil, nil))
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
		clusters, _ := r.DoGetClusters(ctx, nil, nil)
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
func (r *Resolver) DoGetNodes(ctx context.Context, clusterID *string, status *model.NodeStatus, typeArg *string, tag *string, first *int, after *string) ([]*pb.InfrastructureNode, error) {
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
	nodesResp, err := r.Clients.Quartermaster.ListNodes(ctx, clusterFilter, typeFilter, "", buildCursorPagination(first, after, nil, nil))
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get nodes")
		return nil, fmt.Errorf("failed to get nodes: %w", err)
	}

	return nodesResp.Nodes, nil
}

// DoGetServiceInstances returns service instances
func (r *Resolver) DoGetServiceInstances(ctx context.Context, clusterID *string, nodeID *string, status *model.InstanceStatus, first *int, after *string) ([]*pb.ServiceInstance, error) {
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
	resp, err := r.Clients.Quartermaster.ListServiceInstances(ctx, clusterFilter, "", nodeFilter, buildCursorPagination(first, after, nil, nil))
	if err != nil {
		return nil, fmt.Errorf("failed to get service instances: %w", err)
	}

	return resp.Instances, nil
}

// DoGetNode returns a specific node by ID
func (r *Resolver) DoGetNode(ctx context.Context, id string) (*pb.InfrastructureNode, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo node data for ID", id)
		nodes, _ := r.DoGetNodes(ctx, nil, nil, nil, nil, nil, nil)
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
func (r *Resolver) DoDiscoverServices(ctx context.Context, serviceType string, clusterID *string, first *int, after *string) ([]*pb.ServiceInstance, error) {
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

	resp, err := r.Clients.Quartermaster.DiscoverServices(ctx, serviceType, clusterFilter, buildCursorPagination(first, after, nil, nil))
	if err != nil {
		return nil, fmt.Errorf("failed to discover services: %w", err)
	}
	if resp == nil {
		return []*pb.ServiceInstance{}, nil
	}
	return resp.Instances, nil
}

// DoGetDiscoverServicesConnection returns a Relay-style connection for service discovery.
// This is used to find running service instances by type (e.g., "foghorn", "helmsman") with pagination.
func (r *Resolver) DoGetDiscoverServicesConnection(ctx context.Context, serviceType string, clusterID *string, first *int, after *string, last *int, before *string) (*model.ServiceInstancesConnection, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo discover services connection for type", serviceType)
		allInstances := demo.GenerateServiceInstances()
		var filtered []*pb.ServiceInstance
		for _, inst := range allInstances {
			if inst.ServiceId == serviceType || inst.InstanceId == serviceType || serviceType == "" {
				filtered = append(filtered, inst)
			}
		}

		edges := make([]*model.ServiceInstanceEdge, len(filtered))
		for i, inst := range filtered {
			ts := time.Time{}
			if inst.StartedAt != nil {
				ts = inst.StartedAt.AsTime()
			}
			cursor := pagination.EncodeCursor(ts, inst.InstanceId)
			edges[i] = &model.ServiceInstanceEdge{
				Cursor: cursor,
				Node:   inst,
			}
		}
		pageInfo := &model.PageInfo{
			HasPreviousPage: false,
			HasNextPage:     false,
		}
		if len(edges) > 0 {
			pageInfo.StartCursor = &edges[0].Cursor
			pageInfo.EndCursor = &edges[len(edges)-1].Cursor
		}
		return &model.ServiceInstancesConnection{
			Edges:      edges,
			PageInfo:   pageInfo,
			TotalCount: len(filtered),
		}, nil
	}

	clusterFilter := ""
	if clusterID != nil {
		clusterFilter = *clusterID
	}

	// Fetch all instances (Quartermaster supports pagination)
	limit := pagination.DefaultLimit
	if first != nil {
		limit = pagination.ClampLimit(*first)
	}

	resp, err := r.Clients.Quartermaster.DiscoverServices(ctx, serviceType, clusterFilter, buildCursorPagination(&limit, after, last, before))
	if err != nil {
		return nil, fmt.Errorf("failed to discover services: %w", err)
	}

	instances := resp.Instances
	if instances == nil {
		instances = []*pb.ServiceInstance{}
	}

	// Build edges with keyset cursors
	edges := make([]*model.ServiceInstanceEdge, len(instances))
	for i, instance := range instances {
		ts := time.Time{}
		if instance.StartedAt != nil {
			ts = instance.StartedAt.AsTime()
		}
		cursor := pagination.EncodeCursor(ts, instance.InstanceId)
		edges[i] = &model.ServiceInstanceEdge{
			Cursor: cursor,
			Node:   instance,
		}
	}

	// Determine pagination info
	hasMore := len(instances) == limit

	pageInfo := &model.PageInfo{
		HasPreviousPage: after != nil && *after != "",
		HasNextPage:     hasMore,
	}
	if len(edges) > 0 {
		pageInfo.StartCursor = &edges[0].Cursor
		pageInfo.EndCursor = &edges[len(edges)-1].Cursor
	}

	return &model.ServiceInstancesConnection{
		Edges:      edges,
		PageInfo:   pageInfo,
		TotalCount: len(instances),
	}, nil
}

// DoGetClustersAccess returns clusters the current tenant can access
func (r *Resolver) DoGetClustersAccess(ctx context.Context, first *int, after *string) ([]*model.ClusterAccess, error) {
	if middleware.IsDemoMode(ctx) {
		// Simple demo response with a single shared cluster
		return []*model.ClusterAccess{
			{ClusterID: "cluster_demo_us_west", ClusterName: "US West (Oregon)", AccessLevel: "shared"},
		}, nil
	}

	var tenantID string
	if v, ok := ctx.Value("tenant_id").(string); ok {
		tenantID = v
	}
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	resp, err := r.Clients.Quartermaster.ListClustersForTenant(ctx, tenantID, buildCursorPagination(first, after, nil, nil))
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
func (r *Resolver) DoGetClustersAvailable(ctx context.Context, first *int, after *string) ([]*model.AvailableCluster, error) {
	if middleware.IsDemoMode(ctx) {
		return []*model.AvailableCluster{
			{ClusterID: "cluster_demo_us_west", ClusterName: "US West (Oregon)", Tiers: []string{"free"}, AutoEnroll: true},
		}, nil
	}

	resp, err := r.Clients.Quartermaster.ListClustersAvailable(ctx, buildCursorPagination(first, after, nil, nil))
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

	var tenantID string
	if v, ok := ctx.Value("tenant_id").(string); ok {
		tenantID = v
	}
	if tenantID == "" {
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

		if v := raw["primaryClusterId"]; v != nil {
			if val, ok := v.(string); ok && strings.TrimSpace(val) != "" {
				clusterID := strings.TrimSpace(val)
				updateReq.PrimaryClusterId = &clusterID
				updates++
			}
		}

		if v := raw["deploymentModel"]; v != nil {
			if val, ok := v.(string); ok && strings.TrimSpace(val) != "" {
				deployModel := strings.TrimSpace(val)
				updateReq.DeploymentModel = &deployModel
				updates++
			}
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

	var tenantID string
	if v, ok := ctx.Value("tenant_id").(string); ok {
		tenantID = v
	}

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
	if first != nil {
		limit = pagination.ClampLimit(*first)
	}

	clusters, err := r.DoGetClusters(ctx, &limit, after)
	if err != nil {
		return nil, err
	}

	// Build edges with keyset cursors (timestamp + ID for stable pagination)
	edges := make([]*model.ClusterEdge, len(clusters))
	for i, cluster := range clusters {
		cursor := pagination.EncodeCursor(cluster.CreatedAt.AsTime(), cluster.Id)
		edges[i] = &model.ClusterEdge{
			Cursor: cursor,
			Node:   cluster,
		}
	}

	// Determine hasNextPage (if we got a full page, there might be more)
	hasMore := len(clusters) == limit

	pageInfo := &model.PageInfo{
		HasPreviousPage: after != nil && *after != "",
		HasNextPage:     hasMore,
	}
	if len(edges) > 0 {
		pageInfo.StartCursor = &edges[0].Cursor
		pageInfo.EndCursor = &edges[len(edges)-1].Cursor
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
	if first != nil {
		limit = pagination.ClampLimit(*first)
	}

	nodes, err := r.DoGetNodes(ctx, clusterID, status, typeArg, nil, &limit, after)
	if err != nil {
		return nil, err
	}

	// Build edges with keyset cursors (timestamp + ID for stable pagination)
	edges := make([]*model.NodeEdge, len(nodes))
	for i, node := range nodes {
		cursor := pagination.EncodeCursor(node.CreatedAt.AsTime(), node.Id)
		edges[i] = &model.NodeEdge{
			Cursor: cursor,
			Node:   node,
		}
	}

	// Determine hasNextPage
	hasMore := len(nodes) == limit

	pageInfo := &model.PageInfo{
		HasPreviousPage: after != nil && *after != "",
		HasNextPage:     hasMore,
	}
	if len(edges) > 0 {
		pageInfo.StartCursor = &edges[0].Cursor
		pageInfo.EndCursor = &edges[len(edges)-1].Cursor
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
	if first != nil {
		limit = pagination.ClampLimit(*first)
	}

	instances, err := r.DoGetServiceInstances(ctx, clusterID, nodeID, status, &limit, after)
	if err != nil {
		return nil, err
	}

	// Build edges with keyset cursors (timestamp + ID for stable pagination)
	edges := make([]*model.ServiceInstanceEdge, len(instances))
	for i, instance := range instances {
		// Use StartedAt if available, otherwise use a zero time
		ts := time.Time{}
		if instance.StartedAt != nil {
			ts = instance.StartedAt.AsTime()
		}
		cursor := pagination.EncodeCursor(ts, instance.InstanceId)
		edges[i] = &model.ServiceInstanceEdge{
			Cursor: cursor,
			Node:   instance,
		}
	}

	// Determine hasNextPage
	hasMore := len(instances) == limit

	pageInfo := &model.PageInfo{
		HasPreviousPage: after != nil && *after != "",
		HasNextPage:     hasMore,
	}
	if len(edges) > 0 {
		pageInfo.StartCursor = &edges[0].Cursor
		pageInfo.EndCursor = &edges[len(edges)-1].Cursor
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
func (r *Resolver) DoListMySubscriptions(ctx context.Context, first *int, after *string) ([]*pb.InfrastructureCluster, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning demo subscribed clusters")
		return demo.GenerateMySubscriptions(), nil
	}

	tenantID := ""
	if user := middleware.GetUserFromContext(ctx); user != nil {
		tenantID = user.TenantID
	}
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	resp, err := r.Clients.Quartermaster.ListMySubscriptions(ctx, &pb.ListMySubscriptionsRequest{
		TenantId:   tenantID,
		Pagination: buildCursorPagination(first, after, nil, nil),
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

// ============================================================================
// CLUSTER MARKETPLACE OPERATIONS
// ============================================================================

// DoListMarketplaceClusters lists clusters in the marketplace
// Merges operational data from Quartermaster with pricing data from Purser
// Applies tier visibility filtering - only shows clusters the tenant can access
func (r *Resolver) DoListMarketplaceClusters(ctx context.Context, pricingModel *pb.ClusterPricingModel, first *int, after *string) ([]*pb.MarketplaceClusterEntry, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo marketplace clusters")
		return demo.GenerateMarketplaceClusters(), nil
	}

	tenantID := ""
	if user := middleware.GetUserFromContext(ctx); user != nil {
		tenantID = user.TenantID
	}

	// Get tenant's tier level for visibility filtering
	// Uses tier sort_order as the tier level (0=free, 1=supporter, etc.)
	var tenantTierLevel int32 = 0
	if tenantID != "" {
		subResp, err := r.Clients.Purser.GetSubscription(ctx, tenantID)
		if err == nil && subResp.Subscription != nil && subResp.Subscription.TierId != "" {
			tier, err := r.Clients.Purser.GetBillingTier(ctx, subResp.Subscription.TierId)
			if err == nil && tier != nil {
				// Use sort_order as proxy for tier level, or map tier_name
				tenantTierLevel = tier.SortOrder
				// Fallback: map tier_name to level if sort_order is 0
				if tenantTierLevel == 0 && tier.TierName != "" {
					tenantTierLevel = tierNameToLevel(tier.TierName)
				}
			}
		}
	}

	req := &pb.ListMarketplaceClustersRequest{
		TenantId: tenantID,
	}
	// Note: pricingModel filter is now handled client-side after merging with Purser data
	if first != nil {
		req.Pagination = &pb.CursorPaginationRequest{
			First: int32(*first),
		}
		if after != nil {
			req.Pagination.After = after
		}
	}

	resp, err := r.Clients.Quartermaster.ListMarketplaceClusters(ctx, req)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to list marketplace clusters")
		return nil, fmt.Errorf("failed to list marketplace clusters: %w", err)
	}

	// Collect cluster IDs for batch pricing fetch
	clusterIDs := make([]string, len(resp.Clusters))
	for i, cluster := range resp.Clusters {
		clusterIDs[i] = cluster.ClusterId
	}

	// Batch fetch all pricing data from Purser
	pricings, err := r.Clients.Purser.GetClustersPricingBatch(ctx, clusterIDs)
	if err != nil {
		r.Logger.WithError(err).Warn("Failed to batch fetch cluster pricing, using defaults")
		pricings = make(map[string]*pb.ClusterPricing)
	}

	// Enrich clusters with pricing data from Purser and apply tier visibility filter
	type enrichedCluster struct {
		cluster           *pb.MarketplaceClusterEntry
		requiredTierLevel int32
	}
	enrichedClusters := make([]enrichedCluster, 0, len(resp.Clusters))

	for _, cluster := range resp.Clusters {
		pricing, hasPricing := pricings[cluster.ClusterId]
		var requiredLevel int32 = 0
		if !hasPricing || pricing == nil {
			// Use defaults
			cluster.PricingModel = pb.ClusterPricingModel_CLUSTER_PRICING_FREE_UNMETERED
		} else {
			// Map pricing data to cluster entry
			cluster.PricingModel = pricingModelStringToProto(pricing.PricingModel)
			if pricing.BasePrice != "" {
				// Parse base_price string to cents
				var basePrice float64
				fmt.Sscanf(pricing.BasePrice, "%f", &basePrice)
				cluster.MonthlyPriceCents = int32(basePrice * 100)
			}
			requiredLevel = pricing.RequiredTierLevel
			if requiredLevel > 0 {
				tierName := tierLevelToName(requiredLevel)
				cluster.RequiredBillingTier = &tierName
			}
		}

		enrichedClusters = append(enrichedClusters, enrichedCluster{
			cluster:           cluster,
			requiredTierLevel: requiredLevel,
		})
	}

	// Apply tier visibility filter and pricing model filter
	result := make([]*pb.MarketplaceClusterEntry, 0)
	for _, ec := range enrichedClusters {
		// Skip clusters that require a higher tier than the tenant has
		if ec.requiredTierLevel > tenantTierLevel {
			continue
		}
		// Apply pricing model filter if specified
		if pricingModel != nil && *pricingModel != pb.ClusterPricingModel_CLUSTER_PRICING_UNSPECIFIED {
			if ec.cluster.PricingModel != *pricingModel {
				continue
			}
		}
		result = append(result, ec.cluster)
	}

	return result, nil
}

// DoGetMarketplaceCluster gets a marketplace cluster
// Merges operational data from Quartermaster with pricing data from Purser
func (r *Resolver) DoGetMarketplaceCluster(ctx context.Context, clusterID string, inviteToken *string) (*pb.MarketplaceClusterEntry, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo marketplace cluster")
		clusters := demo.GenerateMarketplaceClusters()
		for _, c := range clusters {
			if c.ClusterId == clusterID {
				return c, nil
			}
		}
		return nil, fmt.Errorf("marketplace cluster not found")
	}

	tenantID := ""
	if user := middleware.GetUserFromContext(ctx); user != nil {
		tenantID = user.TenantID
	}

	req := &pb.GetMarketplaceClusterRequest{
		ClusterId: clusterID,
		TenantId:  tenantID,
	}
	if inviteToken != nil {
		req.InviteToken = inviteToken
	}

	cluster, err := r.Clients.Quartermaster.GetMarketplaceCluster(ctx, req)
	if err != nil {
		return nil, err
	}

	// Enrich with pricing data from Purser
	pricing, err := r.Clients.Purser.GetClusterPricing(ctx, cluster.ClusterId)
	if err != nil {
		r.Logger.WithError(err).Warn("Failed to get cluster pricing, using defaults", "cluster", cluster.ClusterId)
		cluster.PricingModel = pb.ClusterPricingModel_CLUSTER_PRICING_FREE_UNMETERED
	} else {
		cluster.PricingModel = pricingModelStringToProto(pricing.PricingModel)
		if pricing.BasePrice != "" {
			var basePrice float64
			fmt.Sscanf(pricing.BasePrice, "%f", &basePrice)
			cluster.MonthlyPriceCents = int32(basePrice * 100)
		}
		if pricing.RequiredTierLevel > 0 {
			tierName := tierLevelToName(pricing.RequiredTierLevel)
			cluster.RequiredBillingTier = &tierName
		}
	}

	return cluster, nil
}

// pricingModelStringToProto converts Purser pricing model string to proto enum
func pricingModelStringToProto(s string) pb.ClusterPricingModel {
	switch s {
	case "free_unmetered":
		return pb.ClusterPricingModel_CLUSTER_PRICING_FREE_UNMETERED
	case "metered":
		return pb.ClusterPricingModel_CLUSTER_PRICING_METERED
	case "monthly":
		return pb.ClusterPricingModel_CLUSTER_PRICING_MONTHLY
	case "tier_inherit":
		return pb.ClusterPricingModel_CLUSTER_PRICING_TIER_INHERIT
	case "custom":
		return pb.ClusterPricingModel_CLUSTER_PRICING_CUSTOM
	default:
		return pb.ClusterPricingModel_CLUSTER_PRICING_FREE_UNMETERED
	}
}

// tierLevelToName converts tier level int to display name
func tierLevelToName(level int32) string {
	switch level {
	case 0:
		return "free"
	case 1:
		return "supporter"
	case 2:
		return "developer"
	case 3:
		return "production"
	case 4:
		return "enterprise"
	default:
		return fmt.Sprintf("tier_%d", level)
	}
}

// tierNameToLevel converts tier display name to level int
func tierNameToLevel(name string) int32 {
	switch name {
	case "free":
		return 0
	case "supporter":
		return 1
	case "developer":
		return 2
	case "production":
		return 3
	case "enterprise":
		return 4
	default:
		return 0
	}
}

// pricingModelProtoToString converts proto enum to Purser string
func pricingModelProtoToString(p pb.ClusterPricingModel) string {
	switch p {
	case pb.ClusterPricingModel_CLUSTER_PRICING_FREE_UNMETERED:
		return "free_unmetered"
	case pb.ClusterPricingModel_CLUSTER_PRICING_METERED:
		return "metered"
	case pb.ClusterPricingModel_CLUSTER_PRICING_MONTHLY:
		return "monthly"
	case pb.ClusterPricingModel_CLUSTER_PRICING_TIER_INHERIT:
		return "tier_inherit"
	case pb.ClusterPricingModel_CLUSTER_PRICING_CUSTOM:
		return "custom"
	default:
		return "tier_inherit"
	}
}

// DoCreatePrivateCluster creates a private cluster (self-hosted edge)
func (r *Resolver) DoCreatePrivateCluster(ctx context.Context, input model.CreatePrivateClusterInput) (model.CreatePrivateClusterResult, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning mock private cluster creation")
		demoField := "demo"
		return &model.ValidationError{
			Message: "Cannot create clusters in demo mode",
			Field:   &demoField,
		}, nil
	}

	tenantID := ""
	if user := middleware.GetUserFromContext(ctx); user != nil {
		tenantID = user.TenantID
	}
	if tenantID == "" {
		return &model.AuthError{Message: "Authentication required"}, nil
	}

	req := &pb.CreatePrivateClusterRequest{
		TenantId:    tenantID,
		ClusterName: input.ClusterName,
	}
	if input.Region != nil {
		req.Region = input.Region
	}

	resp, err := r.Clients.Quartermaster.CreatePrivateCluster(ctx, req)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to create private cluster")
		return &model.ValidationError{
			Message: fmt.Sprintf("Failed to create cluster: %v", err),
		}, nil
	}

	return resp, nil
}

// DoUpdateClusterMarketplace updates cluster marketplace settings
func (r *Resolver) DoUpdateClusterMarketplace(ctx context.Context, clusterID string, input model.UpdateClusterMarketplaceInput) (model.UpdateClusterResult, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning mock cluster update")
		demoField := "demo"
		return &model.ValidationError{
			Message: "Cannot update clusters in demo mode",
			Field:   &demoField,
		}, nil
	}

	tenantID := ""
	if user := middleware.GetUserFromContext(ctx); user != nil {
		tenantID = user.TenantID
	}
	if tenantID == "" {
		return &model.AuthError{Message: "Authentication required"}, nil
	}

	// Update pricing in Purser if any pricing fields are set
	hasPricingUpdate := input.PricingModel != nil || input.MonthlyPriceCents != nil || input.RequiredBillingTier != nil
	if hasPricingUpdate {
		pricingReq := &pb.SetClusterPricingRequest{
			ClusterId: clusterID,
		}
		if input.PricingModel != nil {
			pricingReq.PricingModel = pricingModelProtoToString(*input.PricingModel)
		}
		if input.MonthlyPriceCents != nil {
			basePrice := fmt.Sprintf("%.2f", float64(*input.MonthlyPriceCents)/100)
			pricingReq.BasePrice = &basePrice
		}
		if input.RequiredBillingTier != nil {
			tierLevel := tierNameToLevel(*input.RequiredBillingTier)
			pricingReq.RequiredTierLevel = &tierLevel
		}

		_, err := r.Clients.Purser.SetClusterPricing(ctx, pricingReq)
		if err != nil {
			r.Logger.WithError(err).Error("Failed to update cluster pricing in Purser")
			return &model.ValidationError{
				Message: fmt.Sprintf("Failed to update pricing: %v", err),
			}, nil
		}
	}

	// Update operational settings in Quartermaster
	req := &pb.UpdateClusterMarketplaceRequest{
		ClusterId: clusterID,
		TenantId:  tenantID,
	}
	if input.Visibility != nil {
		req.Visibility = input.Visibility
	}
	if input.RequiresApproval != nil {
		req.RequiresApproval = input.RequiresApproval
	}
	if input.ShortDescription != nil {
		req.ShortDescription = input.ShortDescription
	}

	resp, err := r.Clients.Quartermaster.UpdateClusterMarketplace(ctx, req)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to update cluster marketplace settings")
		return &model.ValidationError{
			Message: fmt.Sprintf("Failed to update cluster: %v", err),
		}, nil
	}

	// Enrich with pricing from Purser before returning
	cluster := resp.Cluster
	pricing, err := r.Clients.Purser.GetClusterPricing(ctx, clusterID)
	if err == nil {
		cluster.PricingModel = pricingModelStringToProto(pricing.PricingModel)
		if pricing.BasePrice != "" {
			var basePrice float64
			fmt.Sscanf(pricing.BasePrice, "%f", &basePrice)
			cluster.MonthlyPriceCents = int32(basePrice * 100)
		}
		if pricing.RequiredTierLevel > 0 {
			tierName := tierLevelToName(pricing.RequiredTierLevel)
			cluster.RequiredBillingTier = &tierName
		}
	}

	return cluster, nil
}

// DoCreateClusterInvite creates an invite to a cluster
func (r *Resolver) DoCreateClusterInvite(ctx context.Context, input model.CreateClusterInviteInput) (model.CreateClusterInviteResult, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning mock cluster invite creation")
		demoField := "demo"
		return &model.ValidationError{
			Message: "Cannot create invites in demo mode",
			Field:   &demoField,
		}, nil
	}

	tenantID := ""
	if user := middleware.GetUserFromContext(ctx); user != nil {
		tenantID = user.TenantID
	}
	if tenantID == "" {
		return &model.AuthError{Message: "Authentication required"}, nil
	}

	req := &pb.CreateClusterInviteRequest{
		ClusterId:       input.ClusterID,
		OwnerTenantId:   tenantID,
		InvitedTenantId: input.InvitedTenantID,
	}
	if input.AccessLevel != nil {
		req.AccessLevel = *input.AccessLevel
	}
	if input.ExpiresInDays != nil {
		req.ExpiresInDays = int32(*input.ExpiresInDays)
	}

	invite, err := r.Clients.Quartermaster.CreateClusterInvite(ctx, req)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to create cluster invite")
		return &model.ValidationError{
			Message: fmt.Sprintf("Failed to create invite: %v", err),
		}, nil
	}

	return invite, nil
}

// DoRevokeClusterInvite revokes a cluster invite
func (r *Resolver) DoRevokeClusterInvite(ctx context.Context, inviteID string) (model.RevokeClusterInviteResult, error) {
	if middleware.IsDemoMode(ctx) {
		return &model.AuthError{
			Message: "Cannot revoke invites in demo mode",
		}, nil
	}

	tenantID := ""
	if user := middleware.GetUserFromContext(ctx); user != nil {
		tenantID = user.TenantID
	}
	if tenantID == "" {
		return &model.AuthError{Message: "Authentication required"}, nil
	}

	err := r.Clients.Quartermaster.RevokeClusterInvite(ctx, &pb.RevokeClusterInviteRequest{
		InviteId:      inviteID,
		OwnerTenantId: tenantID,
	})
	if err != nil {
		r.Logger.WithError(err).Error("Failed to revoke cluster invite")
		return &model.NotFoundError{
			Message: fmt.Sprintf("Failed to revoke invite: %v", err),
		}, nil
	}

	return &model.DeleteSuccess{Success: true}, nil
}

// DoListClusterInvites lists invites for a cluster
func (r *Resolver) DoListClusterInvites(ctx context.Context, clusterID string) ([]*pb.ClusterInvite, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo cluster invites")
		return demo.GenerateClusterInvites(), nil
	}

	tenantID := ""
	if user := middleware.GetUserFromContext(ctx); user != nil {
		tenantID = user.TenantID
	}

	resp, err := r.Clients.Quartermaster.ListClusterInvites(ctx, &pb.ListClusterInvitesRequest{
		ClusterId:     clusterID,
		OwnerTenantId: tenantID,
	})
	if err != nil {
		r.Logger.WithError(err).Error("Failed to list cluster invites")
		return nil, fmt.Errorf("failed to list cluster invites: %w", err)
	}

	return resp.Invites, nil
}

// DoListMyClusterInvites lists pending invites for the current tenant
func (r *Resolver) DoListMyClusterInvites(ctx context.Context) ([]*pb.ClusterInvite, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo pending invites")
		return demo.GenerateMyClusterInvites(), nil
	}

	tenantID := ""
	if user := middleware.GetUserFromContext(ctx); user != nil {
		tenantID = user.TenantID
	}
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	resp, err := r.Clients.Quartermaster.ListMyClusterInvites(ctx, &pb.ListMyClusterInvitesRequest{
		TenantId: tenantID,
	})
	if err != nil {
		r.Logger.WithError(err).Error("Failed to list my cluster invites")
		return nil, fmt.Errorf("failed to list invites: %w", err)
	}

	return resp.Invites, nil
}

// DoRequestClusterSubscription requests subscription to a cluster
func (r *Resolver) DoRequestClusterSubscription(ctx context.Context, clusterID string, inviteToken *string) (model.ClusterSubscriptionResult, error) {
	if middleware.IsDemoMode(ctx) {
		return &model.ValidationError{
			Message: "Cannot request subscriptions in demo mode",
		}, nil
	}

	tenantID := ""
	if user := middleware.GetUserFromContext(ctx); user != nil {
		tenantID = user.TenantID
	}
	if tenantID == "" {
		return &model.AuthError{Message: "Authentication required"}, nil
	}

	req := &pb.RequestClusterSubscriptionRequest{
		ClusterId:   clusterID,
		TenantId:    tenantID,
		InviteToken: inviteToken, // Already *string
	}

	sub, err := r.Clients.Quartermaster.RequestClusterSubscription(ctx, req)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to request cluster subscription")
		return &model.ValidationError{
			Message: fmt.Sprintf("Failed to request subscription: %v", err),
		}, nil
	}

	return sub, nil
}

// DoAcceptClusterInvite accepts a cluster invite
func (r *Resolver) DoAcceptClusterInvite(ctx context.Context, inviteToken string) (model.ClusterSubscriptionResult, error) {
	if middleware.IsDemoMode(ctx) {
		return &model.ValidationError{
			Message: "Cannot accept invites in demo mode",
		}, nil
	}

	tenantID := ""
	if user := middleware.GetUserFromContext(ctx); user != nil {
		tenantID = user.TenantID
	}
	if tenantID == "" {
		return &model.AuthError{Message: "Authentication required"}, nil
	}

	sub, err := r.Clients.Quartermaster.AcceptClusterInvite(ctx, &pb.AcceptClusterInviteRequest{
		InviteToken: inviteToken,
		TenantId:    tenantID,
	})
	if err != nil {
		r.Logger.WithError(err).Error("Failed to accept cluster invite")
		return &model.ValidationError{
			Message: fmt.Sprintf("Failed to accept invite: %v", err),
		}, nil
	}

	return sub, nil
}

// DoListPendingSubscriptions lists pending subscription requests for a cluster
func (r *Resolver) DoListPendingSubscriptions(ctx context.Context, clusterID string) ([]*pb.ClusterSubscription, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo pending subscriptions")
		return demo.GeneratePendingSubscriptions(), nil
	}

	tenantID := ""
	if user := middleware.GetUserFromContext(ctx); user != nil {
		tenantID = user.TenantID
	}

	resp, err := r.Clients.Quartermaster.ListPendingSubscriptions(ctx, &pb.ListPendingSubscriptionsRequest{
		ClusterId:     clusterID,
		OwnerTenantId: tenantID,
	})
	if err != nil {
		r.Logger.WithError(err).Error("Failed to list pending subscriptions")
		return nil, fmt.Errorf("failed to list pending subscriptions: %w", err)
	}

	return resp.Subscriptions, nil
}

// DoApproveClusterSubscription approves a subscription request
func (r *Resolver) DoApproveClusterSubscription(ctx context.Context, subscriptionID string) (model.ClusterSubscriptionResult, error) {
	if middleware.IsDemoMode(ctx) {
		return &model.ValidationError{
			Message: "Cannot approve subscriptions in demo mode",
		}, nil
	}

	tenantID := ""
	if user := middleware.GetUserFromContext(ctx); user != nil {
		tenantID = user.TenantID
	}
	if tenantID == "" {
		return &model.AuthError{Message: "Authentication required"}, nil
	}

	sub, err := r.Clients.Quartermaster.ApproveClusterSubscription(ctx, &pb.ApproveClusterSubscriptionRequest{
		SubscriptionId: subscriptionID,
		OwnerTenantId:  tenantID,
	})
	if err != nil {
		r.Logger.WithError(err).Error("Failed to approve cluster subscription")
		return &model.ValidationError{
			Message: fmt.Sprintf("Failed to approve subscription: %v", err),
		}, nil
	}

	return sub, nil
}

// DoRejectClusterSubscription rejects a subscription request
func (r *Resolver) DoRejectClusterSubscription(ctx context.Context, subscriptionID string, reason *string) (model.ClusterSubscriptionResult, error) {
	if middleware.IsDemoMode(ctx) {
		return &model.ValidationError{
			Message: "Cannot reject subscriptions in demo mode",
		}, nil
	}

	tenantID := ""
	if user := middleware.GetUserFromContext(ctx); user != nil {
		tenantID = user.TenantID
	}
	if tenantID == "" {
		return &model.AuthError{Message: "Authentication required"}, nil
	}

	req := &pb.RejectClusterSubscriptionRequest{
		SubscriptionId: subscriptionID,
		OwnerTenantId:  tenantID,
		Reason:         reason, // Already *string
	}

	sub, err := r.Clients.Quartermaster.RejectClusterSubscription(ctx, req)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to reject cluster subscription")
		return &model.ValidationError{
			Message: fmt.Sprintf("Failed to reject subscription: %v", err),
		}, nil
	}

	return sub, nil
}

// ============================================================================
// Tier 2 Connection Implementations
// ============================================================================

// DoGetClustersAccessConnection returns a Relay-style connection for cluster access.
func (r *Resolver) DoGetClustersAccessConnection(ctx context.Context, first *int, after *string, last *int, before *string) (*model.ClusterAccessConnection, error) {
	// Use existing method to fetch data
	items, err := r.DoGetClustersAccess(ctx, first, after)
	if err != nil {
		return nil, err
	}

	totalCount := len(items)
	edges := make([]*model.ClusterAccessEdge, len(items))
	for i, item := range items {
		cursor := pagination.EncodeCursor(time.Now(), item.ClusterID)
		edges[i] = &model.ClusterAccessEdge{
			Cursor: cursor,
			Node:   item,
		}
	}

	pageInfo := &model.PageInfo{
		HasPreviousPage: after != nil && *after != "",
		HasNextPage:     false, // Single-page result from underlying method
	}
	if len(edges) > 0 {
		pageInfo.StartCursor = &edges[0].Cursor
		pageInfo.EndCursor = &edges[len(edges)-1].Cursor
	}

	return &model.ClusterAccessConnection{
		Edges:      edges,
		PageInfo:   pageInfo,
		TotalCount: totalCount,
	}, nil
}

// DoGetClustersAvailableConnection returns a Relay-style connection for available clusters.
func (r *Resolver) DoGetClustersAvailableConnection(ctx context.Context, first *int, after *string, last *int, before *string) (*model.AvailableClusterConnection, error) {
	items, err := r.DoGetClustersAvailable(ctx, first, after)
	if err != nil {
		return nil, err
	}

	totalCount := len(items)
	edges := make([]*model.AvailableClusterEdge, len(items))
	for i, item := range items {
		cursor := pagination.EncodeCursor(time.Now(), item.ClusterID)
		edges[i] = &model.AvailableClusterEdge{
			Cursor: cursor,
			Node:   item,
		}
	}

	pageInfo := &model.PageInfo{
		HasPreviousPage: after != nil && *after != "",
		HasNextPage:     false,
	}
	if len(edges) > 0 {
		pageInfo.StartCursor = &edges[0].Cursor
		pageInfo.EndCursor = &edges[len(edges)-1].Cursor
	}

	return &model.AvailableClusterConnection{
		Edges:      edges,
		PageInfo:   pageInfo,
		TotalCount: totalCount,
	}, nil
}

// DoGetMySubscriptionsConnection returns a Relay-style connection for user subscriptions.
func (r *Resolver) DoGetMySubscriptionsConnection(ctx context.Context, first *int, after *string, last *int, before *string) (*model.MySubscriptionsConnection, error) {
	items, err := r.DoListMySubscriptions(ctx, first, after)
	if err != nil {
		return nil, err
	}

	totalCount := len(items)
	edges := make([]*model.MySubscriptionEdge, len(items))
	for i, item := range items {
		cursor := pagination.EncodeCursor(time.Now(), item.ClusterId)
		edges[i] = &model.MySubscriptionEdge{
			Cursor: cursor,
			Node:   item,
		}
	}

	pageInfo := &model.PageInfo{
		HasPreviousPage: after != nil && *after != "",
		HasNextPage:     false,
	}
	if len(edges) > 0 {
		pageInfo.StartCursor = &edges[0].Cursor
		pageInfo.EndCursor = &edges[len(edges)-1].Cursor
	}

	return &model.MySubscriptionsConnection{
		Edges:      edges,
		PageInfo:   pageInfo,
		TotalCount: totalCount,
	}, nil
}

// DoGetMarketplaceClustersConnection returns a Relay-style connection for marketplace clusters.
func (r *Resolver) DoGetMarketplaceClustersConnection(ctx context.Context, pricingModel *pb.ClusterPricingModel, first *int, after *string, last *int, before *string) (*model.MarketplaceClusterConnection, error) {
	items, err := r.DoListMarketplaceClusters(ctx, pricingModel, first, after)
	if err != nil {
		return nil, err
	}

	totalCount := len(items)
	edges := make([]*model.MarketplaceClusterEdge, len(items))
	for i, item := range items {
		cursor := pagination.EncodeCursor(time.Now(), item.ClusterId)
		edges[i] = &model.MarketplaceClusterEdge{
			Cursor: cursor,
			Node:   item, // item is already *pb.MarketplaceClusterEntry which maps to MarketplaceCluster
		}
	}

	pageInfo := &model.PageInfo{
		HasPreviousPage: after != nil && *after != "",
		HasNextPage:     false,
	}
	if len(edges) > 0 {
		pageInfo.StartCursor = &edges[0].Cursor
		pageInfo.EndCursor = &edges[len(edges)-1].Cursor
	}

	return &model.MarketplaceClusterConnection{
		Edges:      edges,
		PageInfo:   pageInfo,
		TotalCount: totalCount,
	}, nil
}

// DoGetPendingSubscriptionsConnection returns a Relay-style connection for pending subscriptions.
func (r *Resolver) DoGetPendingSubscriptionsConnection(ctx context.Context, clusterID string, first *int, after *string, last *int, before *string) (*model.ClusterSubscriptionConnection, error) {
	items, err := r.DoListPendingSubscriptions(ctx, clusterID)
	if err != nil {
		return nil, err
	}

	totalCount := len(items)
	edges := make([]*model.ClusterSubscriptionEdge, len(items))
	for i, item := range items {
		cursor := pagination.EncodeCursor(item.CreatedAt.AsTime(), item.Id)
		edges[i] = &model.ClusterSubscriptionEdge{
			Cursor: cursor,
			Node:   item,
		}
	}

	pageInfo := &model.PageInfo{
		HasPreviousPage: after != nil && *after != "",
		HasNextPage:     false,
	}
	if len(edges) > 0 {
		pageInfo.StartCursor = &edges[0].Cursor
		pageInfo.EndCursor = &edges[len(edges)-1].Cursor
	}

	return &model.ClusterSubscriptionConnection{
		Edges:      edges,
		PageInfo:   pageInfo,
		TotalCount: totalCount,
	}, nil
}

// DoGetClusterInvitesConnection returns a Relay-style connection for cluster invites.
func (r *Resolver) DoGetClusterInvitesConnection(ctx context.Context, clusterID string, first *int, after *string, last *int, before *string) (*model.ClusterInviteConnection, error) {
	items, err := r.DoListClusterInvites(ctx, clusterID)
	if err != nil {
		return nil, err
	}

	totalCount := len(items)
	edges := make([]*model.ClusterInviteEdge, len(items))
	for i, item := range items {
		cursor := pagination.EncodeCursor(item.CreatedAt.AsTime(), item.Id)
		edges[i] = &model.ClusterInviteEdge{
			Cursor: cursor,
			Node:   item,
		}
	}

	pageInfo := &model.PageInfo{
		HasPreviousPage: after != nil && *after != "",
		HasNextPage:     false,
	}
	if len(edges) > 0 {
		pageInfo.StartCursor = &edges[0].Cursor
		pageInfo.EndCursor = &edges[len(edges)-1].Cursor
	}

	return &model.ClusterInviteConnection{
		Edges:      edges,
		PageInfo:   pageInfo,
		TotalCount: totalCount,
	}, nil
}

// DoGetMyClusterInvitesConnection returns a Relay-style connection for the user's cluster invites.
func (r *Resolver) DoGetMyClusterInvitesConnection(ctx context.Context, first *int, after *string, last *int, before *string) (*model.ClusterInviteConnection, error) {
	items, err := r.DoListMyClusterInvites(ctx)
	if err != nil {
		return nil, err
	}

	totalCount := len(items)
	edges := make([]*model.ClusterInviteEdge, len(items))
	for i, item := range items {
		cursor := pagination.EncodeCursor(item.CreatedAt.AsTime(), item.Id)
		edges[i] = &model.ClusterInviteEdge{
			Cursor: cursor,
			Node:   item,
		}
	}

	pageInfo := &model.PageInfo{
		HasPreviousPage: after != nil && *after != "",
		HasNextPage:     false,
	}
	if len(edges) > 0 {
		pageInfo.StartCursor = &edges[0].Cursor
		pageInfo.EndCursor = &edges[len(edges)-1].Cursor
	}

	return &model.ClusterInviteConnection{
		Edges:      edges,
		PageInfo:   pageInfo,
		TotalCount: totalCount,
	}, nil
}

