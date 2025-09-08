package resolvers

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/demo"
	"frameworks/api_gateway/internal/middleware"
	"frameworks/pkg/api/commodore"
	"frameworks/pkg/models"
)

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

// DoGetTenant returns tenant information
func (r *Resolver) DoGetTenant(ctx context.Context) (*models.Tenant, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo tenant data")
		return demo.GenerateTenant(), nil
	}

	tenantID, ok := ctx.Value("tenant_id").(string)
	if !ok {
		return nil, fmt.Errorf("tenant context required")
	}

	r.Logger.WithField("tenant_id", tenantID).Info("Getting tenant info")

	// Get tenant from Quartermaster
	resp, err := r.Clients.Quartermaster.GetTenant(ctx, tenantID)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get tenant")
		return nil, fmt.Errorf("failed to get tenant: %w", err)
	}

	if resp.Error != "" {
		return nil, fmt.Errorf("failed to get tenant: %s", resp.Error)
	}

	// Convert TenantInfo to models.Tenant
	if resp.Tenant == nil {
		return nil, fmt.Errorf("tenant not found")
	}

	tenant := &models.Tenant{
		ID:               resp.Tenant.ID,
		Name:             resp.Tenant.Name,
		IsActive:         resp.Tenant.IsActive,
		PrimaryClusterID: resp.Tenant.PrimaryClusterID,
	}

	return tenant, nil
}

// DoGetClusters returns available clusters
func (r *Resolver) DoGetClusters(ctx context.Context) ([]*models.InfrastructureCluster, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo cluster data")
		return []*models.InfrastructureCluster{
			{
				ID:           "cluster_demo_us_west",
				ClusterID:    "cluster_demo_us_west",
				ClusterName:  "US West (Oregon)",
				ClusterType:  "us-west-2",
				HealthStatus: "HEALTHY",
				CreatedAt:    time.Now().Add(-30 * 24 * time.Hour),
			},
			{
				ID:           "cluster_demo_eu_west",
				ClusterID:    "cluster_demo_eu_west",
				ClusterName:  "EU West (Ireland)",
				ClusterType:  "eu-west-1",
				HealthStatus: "HEALTHY",
				CreatedAt:    time.Now().Add(-45 * 24 * time.Hour),
			},
		}, nil
	}

	r.Logger.Info("Getting clusters")

	// Get clusters from Quartermaster
	clustersResp, err := r.Clients.Quartermaster.GetClusters(ctx)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get clusters")
		return nil, fmt.Errorf("failed to get clusters: %w", err)
	}

	// Return pointers to underlying models
	clusters := make([]*models.InfrastructureCluster, len(clustersResp.Clusters))
	for i := range clustersResp.Clusters {
		clusters[i] = &clustersResp.Clusters[i]
	}

	return clusters, nil
}

// DoGetCluster returns a specific cluster by ID
func (r *Resolver) DoGetCluster(ctx context.Context, id string) (*models.InfrastructureCluster, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo cluster data for ID", id)
		clusters, _ := r.DoGetClusters(ctx)
		for _, cluster := range clusters {
			if cluster.ID == id {
				return cluster, nil
			}
		}
		return nil, fmt.Errorf("demo cluster not found")
	}

	r.Logger.WithField("cluster_id", id).Info("Getting cluster")

	// Get cluster from Quartermaster
	clusterResp, err := r.Clients.Quartermaster.GetCluster(ctx, id)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get cluster")
		return nil, fmt.Errorf("failed to get cluster: %w", err)
	}

	// Return pointer to underlying model
	return &clusterResp.Cluster, nil
}

// DoGetNodes returns infrastructure nodes
func (r *Resolver) DoGetNodes(ctx context.Context) ([]*models.InfrastructureNode, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo node data")
		return []*models.InfrastructureNode{
			{
				ID:            "node_demo_us_west_01",
				NodeID:        "node_demo_us_west_01",
				ClusterID:     "cluster_demo_us_west",
				NodeName:      "streaming-node-01",
				NodeType:      "streaming",
				Status:        "HEALTHY",
				Region:        func() *string { s := "us-west-2a"; return &s }(),
				InternalIP:    func() *string { s := "10.0.1.15"; return &s }(),
				LastHeartbeat: func() *time.Time { t := time.Now().Add(-2 * time.Minute); return &t }(),
				CreatedAt:     time.Now().Add(-30 * 24 * time.Hour),
			},
			{
				ID:            "node_demo_us_west_02",
				NodeID:        "node_demo_us_west_02",
				ClusterID:     "cluster_demo_us_west",
				NodeName:      "transcoding-node-01",
				NodeType:      "transcoding",
				Status:        "HEALTHY",
				Region:        func() *string { s := "us-west-2b"; return &s }(),
				InternalIP:    func() *string { s := "10.0.2.23"; return &s }(),
				LastHeartbeat: func() *time.Time { t := time.Now().Add(-1 * time.Minute); return &t }(),
				CreatedAt:     time.Now().Add(-25 * time.Hour),
			},
		}, nil
	}

	r.Logger.Info("Getting nodes")

	// Get nodes from Quartermaster
	nodesResp, err := r.Clients.Quartermaster.GetNodes(ctx, nil)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get nodes")
		return nil, fmt.Errorf("failed to get nodes: %w", err)
	}

	// Return pointers to underlying models
	nodes := make([]*models.InfrastructureNode, len(nodesResp.Nodes))
	for i := range nodesResp.Nodes {
		nodes[i] = &nodesResp.Nodes[i]
	}

	return nodes, nil
}

// DoGetNode returns a specific node by ID
func (r *Resolver) DoGetNode(ctx context.Context, id string) (*models.InfrastructureNode, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo node data for ID", id)
		nodes, _ := r.DoGetNodes(ctx)
		for _, node := range nodes {
			if node.ID == id {
				return node, nil
			}
		}
		return nil, fmt.Errorf("demo node not found")
	}

	r.Logger.WithField("node_id", id).Info("Getting node")

	// Get node from Quartermaster
	nodeResp, err := r.Clients.Quartermaster.GetNode(ctx, id)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get node")
		return nil, fmt.Errorf("failed to get node: %w", err)
	}

	// Return pointer to underlying model
	return &nodeResp.Node, nil
}

// DoDiscoverServices discovers running service instances by service type and optional cluster
func (r *Resolver) DoDiscoverServices(ctx context.Context, serviceType string, clusterID *string) ([]*models.ServiceInstance, error) {
	if middleware.IsDemoMode(ctx) {
		// In demo mode, return empty list for discovery
		return []*models.ServiceInstance{}, nil
	}
	resp, err := r.Clients.Quartermaster.ServiceDiscovery(ctx, serviceType, clusterID)
	if err != nil {
		return nil, fmt.Errorf("failed to discover services: %w", err)
	}
	if resp == nil {
		return []*models.ServiceInstance{}, nil
	}
	out := make([]*models.ServiceInstance, 0, len(resp.Instances))
	for i := range resp.Instances {
		out = append(out, &resp.Instances[i])
	}
	return out, nil
}

// DoGetClustersAccess returns clusters the current tenant can access
func (r *Resolver) DoGetClustersAccess(ctx context.Context) ([]*model.ClusterAccess, error) {
	if middleware.IsDemoMode(ctx) {
		// Simple demo response with a single shared cluster
		return []*model.ClusterAccess{
			{ClusterID: "cluster_demo_us_west", ClusterName: "US West (Oregon)", AccessLevel: "shared"},
		}, nil
	}
	resp, err := r.Clients.Quartermaster.GetClustersAccess(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get clusters access: %w", err)
	}
	if resp == nil {
		return []*model.ClusterAccess{}, nil
	}
	out := make([]*model.ClusterAccess, 0, len(resp.Clusters))
	for i := range resp.Clusters {
		c := resp.Clusters[i]
		item := &model.ClusterAccess{
			ClusterID:   c.ClusterID,
			ClusterName: c.ClusterName,
			AccessLevel: c.AccessLevel,
		}
		// ResourceLimits: GraphQL JSON scalar maps to *string in generated models.
		// Serialize map → JSON string when present.
		if c.ResourceLimits != nil {
			if b, err := json.Marshal(c.ResourceLimits); err == nil {
				s := string(b)
				item.ResourceLimits = &s
			}
		}
		out = append(out, item)
	}
	return out, nil
}

// DoGetClustersAvailable returns clusters available for onboarding UX
func (r *Resolver) DoGetClustersAvailable(ctx context.Context) ([]*model.AvailableCluster, error) {
	if middleware.IsDemoMode(ctx) {
		return []*model.AvailableCluster{
			{ClusterID: "cluster_demo_us_west", ClusterName: "US West (Oregon)", Tiers: []string{"free"}, AutoEnroll: true},
		}, nil
	}
	resp, err := r.Clients.Quartermaster.GetClustersAvailable(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to get clusters available: %w", err)
	}
	if resp == nil {
		return []*model.AvailableCluster{}, nil
	}
	out := make([]*model.AvailableCluster, 0, len(resp.Clusters))
	for i := range resp.Clusters {
		c := resp.Clusters[i]
		item := &model.AvailableCluster{
			ClusterID:   c.ClusterID,
			ClusterName: c.ClusterName,
			Tiers:       c.Tiers,
			AutoEnroll:  c.AutoEnroll,
		}
		out = append(out, item)
	}
	return out, nil
}

// DoUpdateTenant updates tenant settings
func (r *Resolver) DoUpdateTenant(ctx context.Context, input model.UpdateTenantInput) (*models.Tenant, error) {
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

	// Handle JSON settings validation if provided
	if input.Settings != nil {
		var settings models.JSONB
		if err := json.Unmarshal([]byte(*input.Settings), &settings); err != nil {
			return nil, fmt.Errorf("invalid settings JSON: %w", err)
		}
	}

	// TODO: Add UpdateTenant method to Quartermaster client
	// For now, return current tenant
	return r.DoGetTenant(ctx)
}

// DoUpdateStream updates stream settings
func (r *Resolver) DoUpdateStream(ctx context.Context, id string, input model.UpdateStreamInput) (*models.Stream, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo stream update")
		streams := demo.GenerateStreams()
		for _, stream := range streams {
			if stream.ID == id {
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

	// Extract JWT token from context
	userToken, ok := ctx.Value("jwt_token").(string)
	if !ok {
		return nil, fmt.Errorf("user not authenticated")
	}

	tenantID, _ := ctx.Value("tenant_id").(string)

	r.Logger.WithField("tenant_id", tenantID).
		WithField("stream_id", id).
		Info("Updating stream")

	// Convert to Commodore request format
	req := &commodore.UpdateStreamRequest{}

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

	// Call Commodore to update stream
	streamResp, err := r.Clients.Commodore.UpdateStream(ctx, userToken, id, req)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to update stream")
		return nil, fmt.Errorf("failed to update stream: %w", err)
	}

	return streamResp, nil
}
