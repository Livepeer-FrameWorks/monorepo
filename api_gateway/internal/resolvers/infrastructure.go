package resolvers

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/demo"
	"frameworks/pkg/globalid"
	"frameworks/api_gateway/internal/middleware"
	"frameworks/pkg/pagination"
	pb "frameworks/pkg/proto"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// timestamppbNew is a helper to create timestamppb.Timestamp from time.Time
func timestamppbNew(t time.Time) *timestamppb.Timestamp {
	return timestamppb.New(t)
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
		return demo.GenerateInfrastructureClusters(), nil
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
		return demo.GenerateInfrastructureNodes(), nil
	}

	r.Logger.Info("Getting nodes")

	// Build filter parameters for gRPC
	clusterFilter := ""
	if clusterID != nil {
		rawID, err := globalid.DecodeExpected(*clusterID, globalid.TypeCluster)
		if err != nil {
			return nil, err
		}
		clusterFilter = rawID
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
		rawID, err := globalid.DecodeExpected(*clusterID, globalid.TypeCluster)
		if err != nil {
			return nil, err
		}
		clusterFilter = rawID
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
		edgeNodes := make([]*pb.ServiceInstance, 0, len(edges))
		for _, edge := range edges {
			if edge != nil {
				edgeNodes = append(edgeNodes, edge.Node)
			}
		}

		return &model.ServiceInstancesConnection{
			Edges:      edges,
			Nodes:      edgeNodes,
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

	edgeNodes := make([]*pb.ServiceInstance, 0, len(edges))
	for _, edge := range edges {
		if edge != nil {
			edgeNodes = append(edgeNodes, edge.Node)
		}
	}

	return &model.ServiceInstancesConnection{
		Edges:      edges,
		Nodes:      edgeNodes,
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
	changedFields := []string{}

	if input.Name != nil {
		trimmed := strings.TrimSpace(*input.Name)
		if trimmed != "" {
			updateReq.Name = &trimmed
			updates++
			changedFields = append(changedFields, "name")
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
				changedFields = append(changedFields, "primary_cluster_id")
			}
		}

		if v := raw["deploymentModel"]; v != nil {
			if val, ok := v.(string); ok && strings.TrimSpace(val) != "" {
				deployModel := strings.TrimSpace(val)
				updateReq.DeploymentModel = &deployModel
				updates++
				changedFields = append(changedFields, "deployment_model")
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

	r.sendServiceEvent(ctx, &pb.ServiceEvent{
		EventType:    apiEventTenantUpdated,
		ResourceType: "tenant",
		ResourceId:   tenantID,
		Payload: &pb.ServiceEvent_TenantEvent{
			TenantEvent: &pb.TenantEvent{
				TenantId:      tenantID,
				ChangedFields: changedFields,
			},
		},
	})

	return r.DoGetTenant(ctx)
}

// DoUpdateStream updates stream settings
func (r *Resolver) DoUpdateStream(ctx context.Context, id string, input model.UpdateStreamInput) (*pb.Stream, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo stream update")
		streams := demo.GenerateStreams()
		for _, stream := range streams {
			if stream.StreamId == id {
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

	changedFields := []string{}
	if input.Name != nil {
		changedFields = append(changedFields, "title")
	}
	if input.Description != nil {
		changedFields = append(changedFields, "description")
	}
	if input.Record != nil {
		changedFields = append(changedFields, "is_recording")
	}
	r.sendServiceEvent(ctx, &pb.ServiceEvent{
		EventType:    apiEventStreamUpdated,
		ResourceType: "stream",
		ResourceId:   id,
		Payload: &pb.ServiceEvent_StreamChangeEvent{
			StreamChangeEvent: &pb.StreamChangeEvent{
				StreamId:      id,
				ChangedFields: changedFields,
			},
		},
	})

	return stream, nil
}

// DoGetClustersConnection returns a Relay-style connection for clusters
func (r *Resolver) DoGetClustersConnection(ctx context.Context, first *int, after *string, last *int, before *string) (*model.ClustersConnection, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo clusters connection")
		clusters, _ := r.DoGetClusters(ctx, nil, nil)
		return r.buildClustersConnectionFromSlice(clusters, first, after, last, before), nil
	}

	// Build bidirectional pagination request
	paginationReq := buildCursorPagination(first, after, last, before)

	// Call Quartermaster with pagination
	resp, err := r.Clients.Quartermaster.ListClusters(ctx, paginationReq)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get clusters")
		return nil, fmt.Errorf("failed to get clusters: %w", err)
	}

	return r.buildClustersConnectionFromResponse(resp), nil
}

// buildClustersConnectionFromResponse constructs a connection from gRPC response
func (r *Resolver) buildClustersConnectionFromResponse(resp *pb.ListClustersResponse) *model.ClustersConnection {
	clusters := resp.GetClusters()
	edges := make([]*model.ClusterEdge, len(clusters))
	for i, cluster := range clusters {
		cursor := pagination.EncodeCursor(cluster.CreatedAt.AsTime(), cluster.Id)
		edges[i] = &model.ClusterEdge{
			Cursor: cursor,
			Node:   cluster,
		}
	}

	pag := resp.GetPagination()
	pageInfo := &model.PageInfo{
		HasPreviousPage: pag.GetHasPreviousPage(),
		HasNextPage:     pag.GetHasNextPage(),
	}
	if pag.GetStartCursor() != "" {
		sc := pag.GetStartCursor()
		pageInfo.StartCursor = &sc
	}
	if pag.GetEndCursor() != "" {
		ec := pag.GetEndCursor()
		pageInfo.EndCursor = &ec
	}

	edgeNodes := make([]*pb.InfrastructureCluster, 0, len(edges))
	for _, edge := range edges {
		if edge != nil {
			edgeNodes = append(edgeNodes, edge.Node)
		}
	}

	return &model.ClustersConnection{
		Edges:      edges,
		Nodes:      edgeNodes,
		PageInfo:   pageInfo,
		TotalCount: int(pag.GetTotalCount()),
	}
}

// buildClustersConnectionFromSlice constructs a connection from a slice (demo mode)
func (r *Resolver) buildClustersConnectionFromSlice(clusters []*pb.InfrastructureCluster, first *int, after *string, last *int, before *string) *model.ClustersConnection {
	total := len(clusters)

	limit := infraDefaultLimit
	if first != nil {
		limit = *first
		if limit > infraMaxLimit {
			limit = infraMaxLimit
		}
	} else if last != nil {
		limit = *last
		if limit > infraMaxLimit {
			limit = infraMaxLimit
		}
	}

	if limit > total {
		limit = total
	}

	paginatedClusters := clusters
	if len(clusters) > limit {
		paginatedClusters = clusters[:limit]
	}

	edges := make([]*model.ClusterEdge, len(paginatedClusters))
	for i, cluster := range paginatedClusters {
		cursor := pagination.EncodeCursor(cluster.CreatedAt.AsTime(), cluster.Id)
		edges[i] = &model.ClusterEdge{
			Cursor: cursor,
			Node:   cluster,
		}
	}

	pageInfo := &model.PageInfo{
		HasPreviousPage: after != nil && *after != "",
		HasNextPage:     len(clusters) > limit,
	}
	if len(edges) > 0 {
		pageInfo.StartCursor = &edges[0].Cursor
		pageInfo.EndCursor = &edges[len(edges)-1].Cursor
	}

	edgeNodes := make([]*pb.InfrastructureCluster, 0, len(edges))
	for _, edge := range edges {
		if edge != nil {
			edgeNodes = append(edgeNodes, edge.Node)
		}
	}

	return &model.ClustersConnection{
		Edges:      edges,
		Nodes:      edgeNodes,
		PageInfo:   pageInfo,
		TotalCount: total,
	}
}

// DoGetNodesConnection returns a Relay-style connection for nodes
func (r *Resolver) DoGetNodesConnection(ctx context.Context, clusterID *string, status *model.NodeStatus, typeArg *string, first *int, after *string, last *int, before *string) (*model.NodesConnection, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo nodes connection")
		nodes, _ := r.DoGetNodes(ctx, nil, nil, nil, nil, nil, nil)
		return r.buildNodesConnectionFromSlice(nodes, first, after, last, before), nil
	}

	// Decode global IDs
	decodedClusterID := ""
	if clusterID != nil {
		rawID, err := globalid.DecodeExpected(*clusterID, globalid.TypeCluster)
		if err != nil {
			return nil, err
		}
		decodedClusterID = rawID
	}

	nodeType := ""
	if typeArg != nil {
		nodeType = *typeArg
	}

	// Build bidirectional pagination request
	paginationReq := buildCursorPagination(first, after, last, before)

	// Call Quartermaster with pagination
	resp, err := r.Clients.Quartermaster.ListNodes(ctx, decodedClusterID, nodeType, "", paginationReq)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get nodes")
		return nil, fmt.Errorf("failed to get nodes: %w", err)
	}

	return r.buildNodesConnectionFromResponse(resp), nil
}

// buildNodesConnectionFromResponse constructs a connection from gRPC response
func (r *Resolver) buildNodesConnectionFromResponse(resp *pb.ListNodesResponse) *model.NodesConnection {
	nodes := resp.GetNodes()
	edges := make([]*model.NodeEdge, len(nodes))
	for i, node := range nodes {
		cursor := pagination.EncodeCursor(node.CreatedAt.AsTime(), node.Id)
		edges[i] = &model.NodeEdge{
			Cursor: cursor,
			Node:   node,
		}
	}

	pag := resp.GetPagination()
	pageInfo := &model.PageInfo{
		HasPreviousPage: pag.GetHasPreviousPage(),
		HasNextPage:     pag.GetHasNextPage(),
	}
	if pag.GetStartCursor() != "" {
		sc := pag.GetStartCursor()
		pageInfo.StartCursor = &sc
	}
	if pag.GetEndCursor() != "" {
		ec := pag.GetEndCursor()
		pageInfo.EndCursor = &ec
	}

	edgeNodes := make([]*pb.InfrastructureNode, 0, len(edges))
	for _, edge := range edges {
		if edge != nil {
			edgeNodes = append(edgeNodes, edge.Node)
		}
	}

	return &model.NodesConnection{
		Edges:      edges,
		Nodes:      edgeNodes,
		PageInfo:   pageInfo,
		TotalCount: int(pag.GetTotalCount()),
	}
}

// buildNodesConnectionFromSlice constructs a connection from a slice (demo mode)
func (r *Resolver) buildNodesConnectionFromSlice(nodes []*pb.InfrastructureNode, first *int, after *string, last *int, before *string) *model.NodesConnection {
	total := len(nodes)

	limit := infraDefaultLimit
	if first != nil {
		limit = *first
		if limit > infraMaxLimit {
			limit = infraMaxLimit
		}
	} else if last != nil {
		limit = *last
		if limit > infraMaxLimit {
			limit = infraMaxLimit
		}
	}

	if limit > total {
		limit = total
	}

	paginatedNodes := nodes
	if len(nodes) > limit {
		paginatedNodes = nodes[:limit]
	}

	edges := make([]*model.NodeEdge, len(paginatedNodes))
	for i, node := range paginatedNodes {
		cursor := pagination.EncodeCursor(node.CreatedAt.AsTime(), node.Id)
		edges[i] = &model.NodeEdge{
			Cursor: cursor,
			Node:   node,
		}
	}

	pageInfo := &model.PageInfo{
		HasPreviousPage: after != nil && *after != "",
		HasNextPage:     len(nodes) > limit,
	}
	if len(edges) > 0 {
		pageInfo.StartCursor = &edges[0].Cursor
		pageInfo.EndCursor = &edges[len(edges)-1].Cursor
	}

	edgeNodes := make([]*pb.InfrastructureNode, 0, len(edges))
	for _, edge := range edges {
		if edge != nil {
			edgeNodes = append(edgeNodes, edge.Node)
		}
	}

	return &model.NodesConnection{
		Edges:      edges,
		Nodes:      edgeNodes,
		PageInfo:   pageInfo,
		TotalCount: total,
	}
}

// DoGetServiceInstancesConnection returns a Relay-style connection for service instances
func (r *Resolver) DoGetServiceInstancesConnection(ctx context.Context, clusterID *string, nodeID *string, status *model.InstanceStatus, first *int, after *string, last *int, before *string) (*model.ServiceInstancesConnection, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo service instances connection")
		instances, _ := r.DoGetServiceInstances(ctx, nil, nil, nil, nil, nil)
		return r.buildServiceInstancesConnectionFromSlice(instances, first, after, last, before), nil
	}

	// Decode global IDs
	decodedClusterID := ""
	if clusterID != nil {
		rawID, err := globalid.DecodeExpected(*clusterID, globalid.TypeCluster)
		if err != nil {
			return nil, err
		}
		decodedClusterID = rawID
	}
	decodedNodeID := ""
	if nodeID != nil {
		rawID, err := globalid.DecodeExpected(*nodeID, globalid.TypeInfrastructureNode)
		if err != nil {
			return nil, err
		}
		decodedNodeID = rawID
	}

	// Build bidirectional pagination request
	paginationReq := buildCursorPagination(first, after, last, before)

	// Call Quartermaster with pagination
	resp, err := r.Clients.Quartermaster.ListServiceInstances(ctx, decodedClusterID, "", decodedNodeID, paginationReq)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get service instances")
		return nil, fmt.Errorf("failed to get service instances: %w", err)
	}

	return r.buildServiceInstancesConnectionFromResponse(resp), nil
}

// buildServiceInstancesConnectionFromResponse constructs a connection from gRPC response
func (r *Resolver) buildServiceInstancesConnectionFromResponse(resp *pb.ListServiceInstancesResponse) *model.ServiceInstancesConnection {
	instances := resp.GetInstances()
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

	pag := resp.GetPagination()
	pageInfo := &model.PageInfo{
		HasPreviousPage: pag.GetHasPreviousPage(),
		HasNextPage:     pag.GetHasNextPage(),
	}
	if pag.GetStartCursor() != "" {
		sc := pag.GetStartCursor()
		pageInfo.StartCursor = &sc
	}
	if pag.GetEndCursor() != "" {
		ec := pag.GetEndCursor()
		pageInfo.EndCursor = &ec
	}

	edgeNodes := make([]*pb.ServiceInstance, 0, len(edges))
	for _, edge := range edges {
		if edge != nil {
			edgeNodes = append(edgeNodes, edge.Node)
		}
	}

	return &model.ServiceInstancesConnection{
		Edges:      edges,
		Nodes:      edgeNodes,
		PageInfo:   pageInfo,
		TotalCount: int(pag.GetTotalCount()),
	}
}

// buildServiceInstancesConnectionFromSlice constructs a connection from a slice (demo mode)
func (r *Resolver) buildServiceInstancesConnectionFromSlice(instances []*pb.ServiceInstance, first *int, after *string, last *int, before *string) *model.ServiceInstancesConnection {
	total := len(instances)

	limit := infraDefaultLimit
	if first != nil {
		limit = *first
		if limit > infraMaxLimit {
			limit = infraMaxLimit
		}
	} else if last != nil {
		limit = *last
		if limit > infraMaxLimit {
			limit = infraMaxLimit
		}
	}

	if limit > total {
		limit = total
	}

	paginatedInstances := instances
	if len(instances) > limit {
		paginatedInstances = instances[:limit]
	}

	edges := make([]*model.ServiceInstanceEdge, len(paginatedInstances))
	for i, instance := range paginatedInstances {
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

	pageInfo := &model.PageInfo{
		HasPreviousPage: after != nil && *after != "",
		HasNextPage:     len(instances) > limit,
	}
	if len(edges) > 0 {
		pageInfo.StartCursor = &edges[0].Cursor
		pageInfo.EndCursor = &edges[len(edges)-1].Cursor
	}

	edgeNodes := make([]*pb.ServiceInstance, 0, len(edges))
	for _, edge := range edges {
		if edge != nil {
			edgeNodes = append(edgeNodes, edge.Node)
		}
	}

	return &model.ServiceInstancesConnection{
		Edges:      edges,
		Nodes:      edgeNodes,
		PageInfo:   pageInfo,
		TotalCount: total,
	}
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

	r.sendServiceEvent(ctx, &pb.ServiceEvent{
		EventType:    apiEventTenantClusterAssigned,
		ResourceType: "cluster",
		ResourceId:   clusterID,
		Payload: &pb.ServiceEvent_ClusterEvent{
			ClusterEvent: &pb.ClusterEvent{
				ClusterId: clusterID,
				TenantId:  tenantID,
			},
		},
	})
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

	r.sendServiceEvent(ctx, &pb.ServiceEvent{
		EventType:    apiEventTenantClusterUnassigned,
		ResourceType: "cluster",
		ResourceId:   clusterID,
		Payload: &pb.ServiceEvent_ClusterEvent{
			ClusterEvent: &pb.ClusterEvent{
				ClusterId: clusterID,
				TenantId:  tenantID,
			},
		},
	})
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

// DoListMarketplaceClusters lists clusters in the marketplace.
// Delegates to DoGetMarketplaceClustersConnection and extracts nodes.
func (r *Resolver) DoListMarketplaceClusters(ctx context.Context, first *int, after *string) ([]*pb.MarketplaceClusterEntry, error) {
	conn, err := r.DoGetMarketplaceClustersConnection(ctx, first, after, nil, nil)
	if err != nil {
		return nil, err
	}
	return conn.Nodes, nil
}

// DoGetMarketplaceCluster gets a marketplace cluster.
// Uses Purser for pricing and Quartermaster for metadata (consistent with connection method).
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

	// 1. Get visibility-filtered cluster from Quartermaster
	cluster, err := r.Clients.Quartermaster.GetMarketplaceCluster(ctx, &pb.GetMarketplaceClusterRequest{
		ClusterId: clusterID,
		TenantId:  tenantID,
	})
	if err != nil {
		return nil, fmt.Errorf("cluster not found or not available: %w", err)
	}

	// 2. Enrich with pricing + eligibility from Purser
	pricingResp, err := r.Clients.Purser.GetClustersPricingBatch(ctx, tenantID, []string{clusterID})
	if err != nil {
		return nil, fmt.Errorf("failed to get cluster pricing: %w", err)
	}

	pricingInfo := pricingResp[clusterID]
	cluster.IsEligible = true
	if pricingInfo != nil {
		cluster.PricingModel = pricingModelStringToProto(pricingInfo.PricingModel)
		if pricingInfo.BasePrice != "" {
			cluster.MonthlyPriceCents = int32(parsePriceToCents(pricingInfo.BasePrice))
		}
		cluster.IsEligible = pricingInfo.IsEligible
		if !pricingInfo.IsEligible && pricingInfo.DenialReason != nil && *pricingInfo.DenialReason != "" {
			denial := *pricingInfo.DenialReason
			cluster.DenialReason = &denial
		}
	}

	return cluster, nil
}

// parsePriceToCents converts a price string (e.g., "9.99") to cents.
func parsePriceToCents(price string) int {
	if price == "" {
		return 0
	}
	var f float64
	fmt.Sscanf(price, "%f", &f)
	return int(math.Round(f * 100))
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

	if resp != nil && resp.Cluster != nil {
		clusterID := resp.Cluster.ClusterId
		if clusterID == "" {
			clusterID = resp.Cluster.Id
		}
		r.sendServiceEvent(ctx, &pb.ServiceEvent{
			EventType:    apiEventClusterCreated,
			ResourceType: "cluster",
			ResourceId:   clusterID,
			Payload: &pb.ServiceEvent_ClusterEvent{
				ClusterEvent: &pb.ClusterEvent{
					ClusterId: clusterID,
					TenantId:  tenantID,
				},
			},
		})
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
	hasPricingUpdate := input.PricingModel != nil || input.MonthlyPriceCents != nil
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

	r.sendServiceEvent(ctx, &pb.ServiceEvent{
		EventType:    apiEventClusterUpdated,
		ResourceType: "cluster",
		ResourceId:   clusterID,
		Payload: &pb.ServiceEvent_ClusterEvent{
			ClusterEvent: &pb.ClusterEvent{
				ClusterId: clusterID,
				TenantId:  tenantID,
			},
		},
	})

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

	if invite != nil {
		r.sendServiceEvent(ctx, &pb.ServiceEvent{
			EventType:    apiEventClusterInviteCreated,
			ResourceType: "cluster_invite",
			ResourceId:   invite.Id,
			Payload: &pb.ServiceEvent_ClusterEvent{
				ClusterEvent: &pb.ClusterEvent{
					ClusterId: invite.ClusterId,
					TenantId:  tenantID,
					InviteId:  invite.Id,
				},
			},
		})
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

	r.sendServiceEvent(ctx, &pb.ServiceEvent{
		EventType:    apiEventClusterInviteRevoked,
		ResourceType: "cluster_invite",
		ResourceId:   inviteID,
		Payload: &pb.ServiceEvent_ClusterEvent{
			ClusterEvent: &pb.ClusterEvent{
				TenantId: tenantID,
				InviteId: inviteID,
			},
		},
	})

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

	if sub != nil {
		r.sendServiceEvent(ctx, &pb.ServiceEvent{
			EventType:    apiEventClusterSubscriptionRequested,
			ResourceType: "cluster_subscription",
			ResourceId:   sub.Id,
			Payload: &pb.ServiceEvent_ClusterEvent{
				ClusterEvent: &pb.ClusterEvent{
					ClusterId:      sub.ClusterId,
					TenantId:       tenantID,
					SubscriptionId: sub.Id,
				},
			},
		})
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

	if sub != nil {
		r.sendServiceEvent(ctx, &pb.ServiceEvent{
			EventType:    apiEventClusterSubscriptionApproved,
			ResourceType: "cluster_subscription",
			ResourceId:   sub.Id,
			Payload: &pb.ServiceEvent_ClusterEvent{
				ClusterEvent: &pb.ClusterEvent{
					ClusterId:      sub.ClusterId,
					TenantId:       tenantID,
					SubscriptionId: sub.Id,
				},
			},
		})
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

	if sub != nil {
		r.sendServiceEvent(ctx, &pb.ServiceEvent{
			EventType:    apiEventClusterSubscriptionApproved,
			ResourceType: "cluster_subscription",
			ResourceId:   sub.Id,
			Payload: &pb.ServiceEvent_ClusterEvent{
				ClusterEvent: &pb.ClusterEvent{
					ClusterId:      sub.ClusterId,
					TenantId:       tenantID,
					SubscriptionId: sub.Id,
				},
			},
		})
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

	if sub != nil {
		eventReason := ""
		if reason != nil {
			eventReason = *reason
		}
		r.sendServiceEvent(ctx, &pb.ServiceEvent{
			EventType:    apiEventClusterSubscriptionRejected,
			ResourceType: "cluster_subscription",
			ResourceId:   sub.Id,
			Payload: &pb.ServiceEvent_ClusterEvent{
				ClusterEvent: &pb.ClusterEvent{
					ClusterId:      sub.ClusterId,
					TenantId:       tenantID,
					SubscriptionId: sub.Id,
					Reason:         eventReason,
				},
			},
		})
	}

	return sub, nil
}

// ============================================================================
// Tier 2 Connection Implementations
// ============================================================================

// DoGetClustersAccessConnection returns a Relay-style connection for cluster access.
func (r *Resolver) DoGetClustersAccessConnection(ctx context.Context, first *int, after *string, last *int, before *string) (*model.ClusterAccessConnection, error) {
	if middleware.IsDemoMode(ctx) {
		items := []*model.ClusterAccess{
			{ClusterID: "cluster_demo_us_west", ClusterName: "US West (Oregon)", AccessLevel: "shared"},
		}
		return buildClustersAccessConnectionFromSlice(items, first, after, last, before), nil
	}

	var tenantID string
	if v, ok := ctx.Value("tenant_id").(string); ok {
		tenantID = v
	}
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	resp, err := r.Clients.Quartermaster.ListClustersForTenant(ctx, tenantID, buildCursorPagination(first, after, last, before))
	if err != nil {
		return nil, fmt.Errorf("failed to get clusters access: %w", err)
	}
	if resp == nil {
		return &model.ClusterAccessConnection{
			Edges:      []*model.ClusterAccessEdge{},
			Nodes:      []*model.ClusterAccess{},
			PageInfo:   &model.PageInfo{},
			TotalCount: 0,
		}, nil
	}

	return buildClustersAccessConnectionFromResponse(resp), nil
}

// buildClustersAccessConnectionFromResponse builds a connection from a ClustersAccessResponse.
func buildClustersAccessConnectionFromResponse(resp *pb.ClustersAccessResponse) *model.ClusterAccessConnection {
	edges := make([]*model.ClusterAccessEdge, len(resp.Clusters))
	nodes := make([]*model.ClusterAccess, len(resp.Clusters))
	for i, c := range resp.Clusters {
		item := &model.ClusterAccess{
			ClusterID:   c.ClusterId,
			ClusterName: c.ClusterName,
			AccessLevel: c.AccessLevel,
		}
		cursor := pagination.EncodeCursor(time.Now(), c.ClusterId) // Backend doesn't expose created_at for this type
		edges[i] = &model.ClusterAccessEdge{
			Cursor: cursor,
			Node:   item,
		}
		nodes[i] = item
	}

	pag := resp.GetPagination()
	pageInfo := &model.PageInfo{
		HasPreviousPage: pag.GetHasPreviousPage(),
		HasNextPage:     pag.GetHasNextPage(),
	}
	if pag.GetStartCursor() != "" {
		sc := pag.GetStartCursor()
		pageInfo.StartCursor = &sc
	}
	if pag.GetEndCursor() != "" {
		ec := pag.GetEndCursor()
		pageInfo.EndCursor = &ec
	}

	return &model.ClusterAccessConnection{
		Edges:      edges,
		Nodes:      nodes,
		PageInfo:   pageInfo,
		TotalCount: int(pag.GetTotalCount()),
	}
}

// buildClustersAccessConnectionFromSlice builds a connection from a slice (for demo mode).
func buildClustersAccessConnectionFromSlice(items []*model.ClusterAccess, first *int, after *string, last *int, before *string) *model.ClusterAccessConnection {
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
		HasNextPage:     false,
	}
	if len(edges) > 0 {
		pageInfo.StartCursor = &edges[0].Cursor
		pageInfo.EndCursor = &edges[len(edges)-1].Cursor
	}

	return &model.ClusterAccessConnection{
		Edges:      edges,
		Nodes:      items,
		PageInfo:   pageInfo,
		TotalCount: len(items),
	}
}

// DoGetClustersAvailableConnection returns a Relay-style connection for available clusters.
func (r *Resolver) DoGetClustersAvailableConnection(ctx context.Context, first *int, after *string, last *int, before *string) (*model.AvailableClusterConnection, error) {
	if middleware.IsDemoMode(ctx) {
		items := []*model.AvailableCluster{
			{ClusterID: "cluster_demo_us_west", ClusterName: "US West (Oregon)", Tiers: []string{"free"}, AutoEnroll: true},
		}
		return buildClustersAvailableConnectionFromSlice(items, first, after, last, before), nil
	}

	resp, err := r.Clients.Quartermaster.ListClustersAvailable(ctx, buildCursorPagination(first, after, last, before))
	if err != nil {
		return nil, fmt.Errorf("failed to get clusters available: %w", err)
	}
	if resp == nil {
		return &model.AvailableClusterConnection{
			Edges:      []*model.AvailableClusterEdge{},
			Nodes:      []*model.AvailableCluster{},
			PageInfo:   &model.PageInfo{},
			TotalCount: 0,
		}, nil
	}

	return buildClustersAvailableConnectionFromResponse(resp), nil
}

// buildClustersAvailableConnectionFromResponse builds a connection from a ClustersAvailableResponse.
func buildClustersAvailableConnectionFromResponse(resp *pb.ClustersAvailableResponse) *model.AvailableClusterConnection {
	edges := make([]*model.AvailableClusterEdge, len(resp.Clusters))
	nodes := make([]*model.AvailableCluster, len(resp.Clusters))
	for i, c := range resp.Clusters {
		item := &model.AvailableCluster{
			ClusterID:   c.ClusterId,
			ClusterName: c.ClusterName,
			Tiers:       c.Tiers,
			AutoEnroll:  c.AutoEnroll,
		}
		cursor := pagination.EncodeCursor(time.Now(), c.ClusterId)
		edges[i] = &model.AvailableClusterEdge{
			Cursor: cursor,
			Node:   item,
		}
		nodes[i] = item
	}

	pag := resp.GetPagination()
	pageInfo := &model.PageInfo{
		HasPreviousPage: pag.GetHasPreviousPage(),
		HasNextPage:     pag.GetHasNextPage(),
	}
	if pag.GetStartCursor() != "" {
		sc := pag.GetStartCursor()
		pageInfo.StartCursor = &sc
	}
	if pag.GetEndCursor() != "" {
		ec := pag.GetEndCursor()
		pageInfo.EndCursor = &ec
	}

	return &model.AvailableClusterConnection{
		Edges:      edges,
		Nodes:      nodes,
		PageInfo:   pageInfo,
		TotalCount: int(pag.GetTotalCount()),
	}
}

// buildClustersAvailableConnectionFromSlice builds a connection from a slice (for demo mode).
func buildClustersAvailableConnectionFromSlice(items []*model.AvailableCluster, first *int, after *string, last *int, before *string) *model.AvailableClusterConnection {
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
		Nodes:      items,
		PageInfo:   pageInfo,
		TotalCount: len(items),
	}
}

// DoGetMySubscriptionsConnection returns a Relay-style connection for user subscriptions.
func (r *Resolver) DoGetMySubscriptionsConnection(ctx context.Context, first *int, after *string, last *int, before *string) (*model.MySubscriptionsConnection, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Demo mode: returning demo subscribed clusters connection")
		items := demo.GenerateMySubscriptions()
		return buildMySubscriptionsConnectionFromSlice(items, first, after, last, before), nil
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
		Pagination: buildCursorPagination(first, after, last, before),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list subscriptions: %w", err)
	}

	return buildMySubscriptionsConnectionFromResponse(resp), nil
}

// buildMySubscriptionsConnectionFromResponse builds a connection from a ListClustersResponse.
func buildMySubscriptionsConnectionFromResponse(resp *pb.ListClustersResponse) *model.MySubscriptionsConnection {
	edges := make([]*model.MySubscriptionEdge, len(resp.Clusters))
	for i, item := range resp.Clusters {
		cursor := pagination.EncodeCursor(item.CreatedAt.AsTime(), item.ClusterId)
		edges[i] = &model.MySubscriptionEdge{
			Cursor: cursor,
			Node:   item,
		}
	}

	pag := resp.GetPagination()
	pageInfo := &model.PageInfo{
		HasPreviousPage: pag.GetHasPreviousPage(),
		HasNextPage:     pag.GetHasNextPage(),
	}
	if pag.GetStartCursor() != "" {
		sc := pag.GetStartCursor()
		pageInfo.StartCursor = &sc
	}
	if pag.GetEndCursor() != "" {
		ec := pag.GetEndCursor()
		pageInfo.EndCursor = &ec
	}

	nodes := make([]*pb.InfrastructureCluster, 0, len(edges))
	for _, edge := range edges {
		if edge != nil {
			nodes = append(nodes, edge.Node)
		}
	}

	return &model.MySubscriptionsConnection{
		Edges:      edges,
		Nodes:      nodes,
		PageInfo:   pageInfo,
		TotalCount: int(pag.GetTotalCount()),
	}
}

// buildMySubscriptionsConnectionFromSlice builds a connection from a slice (for demo mode).
func buildMySubscriptionsConnectionFromSlice(items []*pb.InfrastructureCluster, first *int, after *string, last *int, before *string) *model.MySubscriptionsConnection {
	edges := make([]*model.MySubscriptionEdge, len(items))
	for i, item := range items {
		cursor := pagination.EncodeCursor(item.CreatedAt.AsTime(), item.ClusterId)
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

	nodes := make([]*pb.InfrastructureCluster, 0, len(edges))
	for _, edge := range edges {
		if edge != nil {
			nodes = append(nodes, edge.Node)
		}
	}

	return &model.MySubscriptionsConnection{
		Edges:      edges,
		Nodes:      nodes,
		PageInfo:   pageInfo,
		TotalCount: len(items),
	}
}

// DoGetMarketplaceClustersConnection returns a Relay-style connection for marketplace clusters.
// Uses Quartermaster for paginated visibility/access and Purser for pricing enrichment.
func (r *Resolver) DoGetMarketplaceClustersConnection(ctx context.Context, first *int, after *string, last *int, before *string) (*model.MarketplaceClusterConnection, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo marketplace clusters connection")
		items := demo.GenerateMarketplaceClusters()
		return buildMarketplaceClustersConnectionFromSlice(items, first, after, last, before), nil
	}

	tenantID := ""
	if user := middleware.GetUserFromContext(ctx); user != nil {
		tenantID = user.TenantID
	}

	// 1. Get paginated clusters from Quartermaster (visibility/access)
	qmResp, err := r.Clients.Quartermaster.ListMarketplaceClusters(ctx, &pb.ListMarketplaceClustersRequest{
		TenantId:   tenantID,
		Pagination: buildCursorPagination(first, after, last, before),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list marketplace clusters: %w", err)
	}

	clusters := qmResp.GetClusters()
	if len(clusters) == 0 {
		return &model.MarketplaceClusterConnection{
			Edges:      []*model.MarketplaceClusterEdge{},
			Nodes:      []*pb.MarketplaceClusterEntry{},
			PageInfo:   &model.PageInfo{HasPreviousPage: false, HasNextPage: false},
			TotalCount: int(qmResp.GetPagination().GetTotalCount()),
		}, nil
	}

	// 2. Batch fetch pricing + eligibility from Purser
	clusterIDs := make([]string, len(clusters))
	for i, c := range clusters {
		clusterIDs[i] = c.ClusterId
	}

	pricingResp, err := r.Clients.Purser.GetClustersPricingBatch(ctx, tenantID, clusterIDs)
	if err != nil {
		return nil, fmt.Errorf("failed to get cluster pricing: %w", err)
	}

	edges := make([]*model.MarketplaceClusterEdge, 0, len(clusters))
	nodes := make([]*pb.MarketplaceClusterEntry, 0, len(clusters))

	for _, cluster := range clusters {
		cluster.IsEligible = true
		pricingInfo := pricingResp[cluster.ClusterId]
		if pricingInfo != nil {
			cluster.PricingModel = pricingModelStringToProto(pricingInfo.PricingModel)
			if pricingInfo.BasePrice != "" {
				cluster.MonthlyPriceCents = int32(parsePriceToCents(pricingInfo.BasePrice))
			}
			cluster.IsEligible = pricingInfo.IsEligible
			if !pricingInfo.IsEligible && pricingInfo.DenialReason != nil && *pricingInfo.DenialReason != "" {
				denial := *pricingInfo.DenialReason
				cluster.DenialReason = &denial
			}
		}

		var cursor string
		if cluster.CreatedAt != nil {
			cursor = pagination.EncodeCursor(cluster.CreatedAt.AsTime(), cluster.ClusterId)
		} else {
			cursor = pagination.EncodeCursor(time.Now(), cluster.ClusterId)
		}

		edges = append(edges, &model.MarketplaceClusterEdge{Cursor: cursor, Node: cluster})
		nodes = append(nodes, cluster)
	}

	pag := qmResp.GetPagination()
	pageInfo := &model.PageInfo{
		HasPreviousPage: pag.GetHasPreviousPage(),
		HasNextPage:     pag.GetHasNextPage(),
	}
	if pag.GetStartCursor() != "" {
		sc := pag.GetStartCursor()
		pageInfo.StartCursor = &sc
	}
	if pag.GetEndCursor() != "" {
		ec := pag.GetEndCursor()
		pageInfo.EndCursor = &ec
	}

	return &model.MarketplaceClusterConnection{
		Edges:      edges,
		Nodes:      nodes,
		PageInfo:   pageInfo,
		TotalCount: int(pag.GetTotalCount()),
	}, nil
}

// buildMarketplaceClustersConnectionFromSlice builds a connection from a slice (demo mode).
func buildMarketplaceClustersConnectionFromSlice(items []*pb.MarketplaceClusterEntry, first *int, after *string, last *int, before *string) *model.MarketplaceClusterConnection {
	edges := make([]*model.MarketplaceClusterEdge, len(items))
	for i, item := range items {
		cursor := pagination.EncodeCursor(time.Now(), item.ClusterId)
		edges[i] = &model.MarketplaceClusterEdge{
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

	return &model.MarketplaceClusterConnection{
		Edges:      edges,
		Nodes:      items,
		PageInfo:   pageInfo,
		TotalCount: len(items),
	}
}

// DoGetPendingSubscriptionsConnection returns a Relay-style connection for pending subscriptions.
func (r *Resolver) DoGetPendingSubscriptionsConnection(ctx context.Context, clusterID string, first *int, after *string, last *int, before *string) (*model.ClusterSubscriptionConnection, error) {
	if middleware.IsDemoMode(ctx) {
		items := demo.GeneratePendingSubscriptions()
		return buildPendingSubscriptionsConnectionFromSlice(items, first, after, last, before), nil
	}

	tenantID := ""
	if user := middleware.GetUserFromContext(ctx); user != nil {
		tenantID = user.TenantID
	}

	resp, err := r.Clients.Quartermaster.ListPendingSubscriptions(ctx, &pb.ListPendingSubscriptionsRequest{
		ClusterId:     clusterID,
		OwnerTenantId: tenantID,
		Pagination:    buildCursorPagination(first, after, last, before),
	})
	if err != nil {
		r.Logger.WithError(err).Error("Failed to list pending subscriptions")
		return nil, fmt.Errorf("failed to list pending subscriptions: %w", err)
	}

	return buildPendingSubscriptionsConnectionFromResponse(resp), nil
}

// buildPendingSubscriptionsConnectionFromResponse builds a connection from a ListPendingSubscriptionsResponse.
func buildPendingSubscriptionsConnectionFromResponse(resp *pb.ListPendingSubscriptionsResponse) *model.ClusterSubscriptionConnection {
	edges := make([]*model.ClusterSubscriptionEdge, len(resp.Subscriptions))
	nodes := make([]*pb.ClusterSubscription, len(resp.Subscriptions))
	for i, item := range resp.Subscriptions {
		cursor := pagination.EncodeCursor(item.CreatedAt.AsTime(), item.Id)
		edges[i] = &model.ClusterSubscriptionEdge{
			Cursor: cursor,
			Node:   item,
		}
		nodes[i] = item
	}

	pag := resp.GetPagination()
	pageInfo := &model.PageInfo{
		HasPreviousPage: pag.GetHasPreviousPage(),
		HasNextPage:     pag.GetHasNextPage(),
	}
	if pag.GetStartCursor() != "" {
		sc := pag.GetStartCursor()
		pageInfo.StartCursor = &sc
	}
	if pag.GetEndCursor() != "" {
		ec := pag.GetEndCursor()
		pageInfo.EndCursor = &ec
	}

	return &model.ClusterSubscriptionConnection{
		Edges:      edges,
		Nodes:      nodes,
		PageInfo:   pageInfo,
		TotalCount: int(pag.GetTotalCount()),
	}
}

// buildPendingSubscriptionsConnectionFromSlice builds a connection from a slice (for demo mode).
func buildPendingSubscriptionsConnectionFromSlice(items []*pb.ClusterSubscription, first *int, after *string, last *int, before *string) *model.ClusterSubscriptionConnection {
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
		Nodes:      items,
		PageInfo:   pageInfo,
		TotalCount: len(items),
	}
}

// DoGetClusterInvitesConnection returns a Relay-style connection for cluster invites.
func (r *Resolver) DoGetClusterInvitesConnection(ctx context.Context, clusterID string, first *int, after *string, last *int, before *string) (*model.ClusterInviteConnection, error) {
	if middleware.IsDemoMode(ctx) {
		items := demo.GenerateClusterInvites()
		return buildClusterInvitesConnectionFromSlice(items, first, after, last, before), nil
	}

	tenantID := ""
	if user := middleware.GetUserFromContext(ctx); user != nil {
		tenantID = user.TenantID
	}

	resp, err := r.Clients.Quartermaster.ListClusterInvites(ctx, &pb.ListClusterInvitesRequest{
		ClusterId:     clusterID,
		OwnerTenantId: tenantID,
		Pagination:    buildCursorPagination(first, after, last, before),
	})
	if err != nil {
		r.Logger.WithError(err).Error("Failed to list cluster invites")
		return nil, fmt.Errorf("failed to list cluster invites: %w", err)
	}

	return buildClusterInvitesConnectionFromResponse(resp), nil
}

// buildClusterInvitesConnectionFromResponse builds a connection from a ListClusterInvitesResponse.
func buildClusterInvitesConnectionFromResponse(resp *pb.ListClusterInvitesResponse) *model.ClusterInviteConnection {
	edges := make([]*model.ClusterInviteEdge, len(resp.Invites))
	nodes := make([]*pb.ClusterInvite, len(resp.Invites))
	for i, item := range resp.Invites {
		cursor := pagination.EncodeCursor(item.CreatedAt.AsTime(), item.Id)
		edges[i] = &model.ClusterInviteEdge{
			Cursor: cursor,
			Node:   item,
		}
		nodes[i] = item
	}

	pag := resp.GetPagination()
	pageInfo := &model.PageInfo{
		HasPreviousPage: pag.GetHasPreviousPage(),
		HasNextPage:     pag.GetHasNextPage(),
	}
	if pag.GetStartCursor() != "" {
		sc := pag.GetStartCursor()
		pageInfo.StartCursor = &sc
	}
	if pag.GetEndCursor() != "" {
		ec := pag.GetEndCursor()
		pageInfo.EndCursor = &ec
	}

	return &model.ClusterInviteConnection{
		Edges:      edges,
		Nodes:      nodes,
		PageInfo:   pageInfo,
		TotalCount: int(pag.GetTotalCount()),
	}
}

// buildClusterInvitesConnectionFromSlice builds a connection from a slice (for demo mode).
func buildClusterInvitesConnectionFromSlice(items []*pb.ClusterInvite, first *int, after *string, last *int, before *string) *model.ClusterInviteConnection {
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
		Nodes:      items,
		PageInfo:   pageInfo,
		TotalCount: len(items),
	}
}

// DoGetMyClusterInvitesConnection returns a Relay-style connection for the user's cluster invites.
func (r *Resolver) DoGetMyClusterInvitesConnection(ctx context.Context, first *int, after *string, last *int, before *string) (*model.ClusterInviteConnection, error) {
	if middleware.IsDemoMode(ctx) {
		items := demo.GenerateMyClusterInvites()
		return buildClusterInvitesConnectionFromSlice(items, first, after, last, before), nil
	}

	tenantID := ""
	if user := middleware.GetUserFromContext(ctx); user != nil {
		tenantID = user.TenantID
	}

	resp, err := r.Clients.Quartermaster.ListMyClusterInvites(ctx, &pb.ListMyClusterInvitesRequest{
		TenantId:   tenantID,
		Pagination: buildCursorPagination(first, after, last, before),
	})
	if err != nil {
		r.Logger.WithError(err).Error("Failed to list my cluster invites")
		return nil, fmt.Errorf("failed to list my cluster invites: %w", err)
	}

	return buildClusterInvitesConnectionFromResponse(resp), nil
}
