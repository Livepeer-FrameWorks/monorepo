package resolvers

import (
	"context"
	"encoding/json"
	"fmt"

	"frameworks/api_gateway/graph/model"
	"frameworks/pkg/api/commodore"
	"frameworks/pkg/models"
)

// DoGetTenant returns tenant information
func (r *Resolver) DoGetTenant(ctx context.Context) (*models.Tenant, error) {
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
func (r *Resolver) DoGetClusters(ctx context.Context) ([]*model.Cluster, error) {
	r.Logger.Info("Getting clusters")

	// TODO: Add GetClusters method to Quartermaster client
	// For now, return empty slice
	return []*model.Cluster{}, nil
}

// DoGetCluster returns a specific cluster by ID
func (r *Resolver) DoGetCluster(ctx context.Context, id string) (*model.Cluster, error) {
	r.Logger.WithField("cluster_id", id).Info("Getting cluster")

	// TODO: Add GetCluster method to Quartermaster client
	return nil, fmt.Errorf("cluster not found")
}

// DoGetNodes returns infrastructure nodes
func (r *Resolver) DoGetNodes(ctx context.Context) ([]*model.Node, error) {
	r.Logger.Info("Getting nodes")

	// TODO: Add GetNodes method to Quartermaster client
	// For now, return empty slice
	return []*model.Node{}, nil
}

// DoGetNode returns a specific node by ID
func (r *Resolver) DoGetNode(ctx context.Context, id string) (*model.Node, error) {
	r.Logger.WithField("node_id", id).Info("Getting node")

	// TODO: Add GetNode method to Quartermaster client
	return nil, fmt.Errorf("node not found")
}

// DoUpdateTenant updates tenant settings
func (r *Resolver) DoUpdateTenant(ctx context.Context, input model.UpdateTenantInput) (*models.Tenant, error) {
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
