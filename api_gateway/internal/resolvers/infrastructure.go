package resolvers

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/demo"
	"frameworks/api_gateway/internal/middleware"
	fhclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/foghorn"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	pkgdns "github.com/Livepeer-FrameWorks/monorepo/pkg/dns"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/globalid"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/models"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/pagination"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	dnspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/dns"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

// Infrastructure pagination constants
const (
	infraDefaultLimit = 100
	infraMaxLimit     = 500
)

// buildCursorPagination creates bidirectional cursor pagination from Relay-style params
func buildCursorPagination(first *int, after *string, last *int, before *string) *commonpb.CursorPaginationRequest {
	req := &commonpb.CursorPaginationRequest{}

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

// normalizeFilterID accepts either raw IDs or Relay global IDs for string filter args.
// Infrastructure filter arguments are schema-level String values, so callers may supply raw IDs.
func normalizeFilterID(id *string, expectedType string) (string, error) {
	if id == nil || strings.TrimSpace(*id) == "" {
		return "", nil
	}

	trimmed := strings.TrimSpace(*id)
	rawID, err := globalid.DecodeExpected(trimmed, expectedType)
	if err != nil {
		return "", err
	}
	return rawID, nil
}

// DoGetTenant returns tenant information
func (r *Resolver) DoGetTenant(ctx context.Context) (*quartermasterpb.Tenant, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo tenant data")
		return demo.GenerateTenant(), nil
	}

	tenantID := ctxkeys.GetTenantID(ctx)
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

// DoGetCustomDomainStatus calls Navigator for the BYO domain lifecycle state of
// the tenant in context. Returns nil when the tenant has not configured a
// custom domain or when Navigator reports `found=false` (so the field resolver
// can map to GraphQL null without surfacing an error to the dashboard).
func (r *Resolver) DoGetCustomDomainStatus(ctx context.Context, domain string) (*dnspb.GetCustomDomainStatusResponse, error) {
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, nil
	}
	if strings.TrimSpace(domain) == "" {
		return nil, nil
	}
	if r.Clients == nil || r.Clients.Navigator == nil {
		return nil, nil
	}
	resp, err := r.Clients.Navigator.GetCustomDomainStatus(ctx, &dnspb.GetCustomDomainStatusRequest{
		TenantId: tenantID,
		Domain:   domain,
	})
	if err != nil {
		return nil, err
	}
	if resp == nil || !resp.Found {
		return nil, nil
	}
	return resp, nil
}

// DoGetClusters returns clusters owned by the current tenant.
func (r *Resolver) DoGetClusters(ctx context.Context, first *int, after *string) ([]*quartermasterpb.InfrastructureCluster, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo cluster data")
		return demo.GenerateInfrastructureClusters(), nil
	}

	tenantID, _, err := r.requireClusterOperatorTenant(ctx)
	if err != nil {
		return nil, err
	}

	r.Logger.WithField("tenant_id", tenantID).Info("Getting owned clusters")

	clustersResp, err := r.Clients.Quartermaster.ListClustersByOwner(ctx, tenantID, buildCursorPagination(first, after, nil, nil))
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get clusters")
		return nil, fmt.Errorf("failed to get clusters: %w", err)
	}

	return clustersResp.Clusters, nil
}

// DoGetCluster returns a specific cluster by ID
func (r *Resolver) DoGetCluster(ctx context.Context, id string) (*quartermasterpb.InfrastructureCluster, error) {
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

	clusterResp, err := r.Clients.Quartermaster.GetCluster(ctx, id)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get cluster")
		return nil, fmt.Errorf("failed to get cluster: %w", err)
	}
	if err := r.requireOwnedCluster(ctx, clusterResp.GetCluster().GetClusterId()); err != nil {
		return nil, err
	}

	return clusterResp.Cluster, nil
}

// DoGetNodes returns infrastructure nodes
func (r *Resolver) DoGetNodes(ctx context.Context, clusterID *string, status *model.NodeStatus, typeArg *string, tag *string, first *int, after *string) ([]*quartermasterpb.InfrastructureNode, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo node data")
		return demo.GenerateInfrastructureNodes(), nil
	}

	clusterFilter, err := normalizeFilterID(clusterID, globalid.TypeCluster)
	if err != nil {
		return nil, err
	}
	if clusterFilter != "" {
		if accessErr := r.requireOwnedCluster(ctx, clusterFilter); accessErr != nil {
			return nil, accessErr
		}
	} else {
		if _, _, accessErr := r.requireClusterOperatorTenant(ctx); accessErr != nil {
			return nil, accessErr
		}
	}

	r.Logger.Info("Getting owned-cluster nodes")

	typeFilter := ""
	if typeArg != nil {
		typeFilter = *typeArg
	}

	if clusterFilter != "" {
		nodesResp, listErr := r.Clients.Quartermaster.ListNodes(ctx, clusterFilter, typeFilter, "", buildCursorPagination(first, after, nil, nil))
		if listErr != nil {
			r.Logger.WithError(listErr).Error("Failed to get nodes")
			return nil, fmt.Errorf("failed to get nodes: %w", listErr)
		}
		return nodesResp.Nodes, nil
	}

	owned, err := r.ownedClusterIDs(ctx)
	if err != nil {
		return nil, err
	}
	nodes := make([]*quartermasterpb.InfrastructureNode, 0)
	for ownedClusterID := range owned {
		nodesResp, listErr := r.Clients.Quartermaster.ListNodes(ctx, ownedClusterID, typeFilter, "", &commonpb.CursorPaginationRequest{First: infraMaxLimit})
		if listErr != nil {
			r.Logger.WithError(listErr).WithField("cluster_id", ownedClusterID).Error("Failed to get nodes")
			return nil, fmt.Errorf("failed to get nodes: %w", listErr)
		}
		nodes = append(nodes, nodesResp.GetNodes()...)
	}
	return nodes, nil
}

// DoGetServiceInstances returns service instances
func (r *Resolver) DoGetServiceInstances(ctx context.Context, clusterID *string, nodeID *string, status *model.InstanceStatus, first *int, after *string) ([]*quartermasterpb.ServiceInstance, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo service instances")
		return demo.GenerateServiceInstances(), nil
	}

	// Build filter parameters for gRPC
	clusterFilter, err := normalizeFilterID(clusterID, globalid.TypeCluster)
	if err != nil {
		return nil, err
	}
	nodeFilter, err := normalizeFilterID(nodeID, globalid.TypeInfrastructureNode)
	if err != nil {
		return nil, err
	}
	if clusterFilter != "" {
		if accessErr := r.requireOwnedCluster(ctx, clusterFilter); accessErr != nil {
			return nil, accessErr
		}
	} else if nodeFilter != "" {
		node, accessErr := r.requireOwnedNode(ctx, nodeFilter)
		if accessErr != nil {
			return nil, accessErr
		}
		clusterFilter = node.GetClusterId()
	} else {
		if _, _, accessErr := r.requireClusterOperatorTenant(ctx); accessErr != nil {
			return nil, accessErr
		}
	}

	if clusterFilter != "" {
		resp, listErr := r.Clients.Quartermaster.ListServiceInstances(ctx, clusterFilter, "", nodeFilter, buildCursorPagination(first, after, nil, nil))
		if listErr != nil {
			return nil, fmt.Errorf("failed to get service instances: %w", listErr)
		}
		return resp.Instances, nil
	}

	owned, err := r.ownedClusterIDs(ctx)
	if err != nil {
		return nil, err
	}
	instances := make([]*quartermasterpb.ServiceInstance, 0)
	for ownedClusterID := range owned {
		resp, listErr := r.Clients.Quartermaster.ListServiceInstances(ctx, ownedClusterID, "", "", &commonpb.CursorPaginationRequest{First: infraMaxLimit})
		if listErr != nil {
			return nil, fmt.Errorf("failed to get service instances: %w", listErr)
		}
		instances = append(instances, resp.GetInstances()...)
	}
	return instances, nil
}

// DoGetNode returns a specific node by ID
func (r *Resolver) DoGetNode(ctx context.Context, id string) (*quartermasterpb.InfrastructureNode, error) {
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

	node, err := r.requireOwnedNode(ctx, id)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get node")
		return nil, err
	}

	return node, nil
}

// DoDiscoverServices discovers running service instances by service type and optional cluster
func (r *Resolver) DoDiscoverServices(ctx context.Context, serviceType string, clusterID *string, first *int, after *string) ([]*quartermasterpb.ServiceInstance, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo discovered services for type", serviceType)
		// Filter demo service instances by service type
		allInstances := demo.GenerateServiceInstances()
		var filtered []*quartermasterpb.ServiceInstance
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
	if clusterFilter != "" {
		if err := r.requireOwnedCluster(ctx, clusterFilter); err != nil {
			return nil, err
		}
	} else if _, _, err := r.requireClusterOperatorTenant(ctx); err != nil {
		return nil, err
	}

	if clusterFilter != "" {
		resp, err := r.Clients.Quartermaster.DiscoverServices(ctx, serviceType, clusterFilter, buildCursorPagination(first, after, nil, nil))
		if err != nil {
			return nil, fmt.Errorf("failed to discover services: %w", err)
		}
		if resp == nil {
			return []*quartermasterpb.ServiceInstance{}, nil
		}
		return resp.Instances, nil
	}

	owned, err := r.ownedClusterIDs(ctx)
	if err != nil {
		return nil, err
	}
	instances := make([]*quartermasterpb.ServiceInstance, 0)
	for ownedClusterID := range owned {
		resp, err := r.Clients.Quartermaster.DiscoverServices(ctx, serviceType, ownedClusterID, &commonpb.CursorPaginationRequest{First: infraMaxLimit})
		if err != nil {
			return nil, fmt.Errorf("failed to discover services: %w", err)
		}
		instances = append(instances, resp.GetInstances()...)
	}
	return instances, nil
}

// DoGetDiscoverServicesConnection returns a Relay-style connection for service discovery.
// This is used to find running service instances by type (e.g., "foghorn", "helmsman") with pagination.
func (r *Resolver) DoGetDiscoverServicesConnection(ctx context.Context, serviceType string, clusterID *string, first *int, after *string, last *int, before *string) (*model.ServiceInstancesConnection, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo discover services connection for type", serviceType)
		allInstances := demo.GenerateServiceInstances()
		var filtered []*quartermasterpb.ServiceInstance
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
		edgeNodes := make([]*quartermasterpb.ServiceInstance, 0, len(edges))
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
	if clusterFilter != "" {
		if err := r.requireOwnedCluster(ctx, clusterFilter); err != nil {
			return nil, err
		}
	} else if _, _, err := r.requireClusterOperatorTenant(ctx); err != nil {
		return nil, err
	}

	// Fetch all instances (Quartermaster supports pagination)
	limit := pagination.DefaultLimit
	if first != nil {
		limit = pagination.ClampLimit(*first)
	}

	if clusterFilter == "" {
		instances, err := r.DoDiscoverServices(ctx, serviceType, nil, first, after)
		if err != nil {
			return nil, err
		}
		return r.buildServiceInstancesConnectionFromSlice(instances, first, after, last, before), nil
	}

	resp, err := r.Clients.Quartermaster.DiscoverServices(ctx, serviceType, clusterFilter, buildCursorPagination(&limit, after, last, before))
	if err != nil {
		return nil, fmt.Errorf("failed to discover services: %w", err)
	}
	instances := resp.Instances
	if instances == nil {
		instances = []*quartermasterpb.ServiceInstance{}
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

	edgeNodes := make([]*quartermasterpb.ServiceInstance, 0, len(edges))
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

	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	resp, err := r.Clients.Quartermaster.ListClustersForTenant(ctx, tenantID, buildCursorPagination(first, after, nil, nil))
	if err != nil {
		return nil, fmt.Errorf("failed to get clusters access: %w", err)
	}
	if resp == nil {
		resp = &quartermasterpb.ClustersAccessResponse{}
	}

	out := make([]*model.ClusterAccess, 0, len(resp.Clusters))
	byClusterID := make(map[string]*model.ClusterAccess)
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
		if item.ClusterID != "" {
			byClusterID[item.ClusterID] = item
			out = append(out, item)
		}
	}

	ownedResp, err := r.Clients.Quartermaster.ListClustersByOwner(ctx, tenantID, &commonpb.CursorPaginationRequest{First: infraMaxLimit})
	if err != nil {
		return nil, fmt.Errorf("failed to get owned clusters: %w", err)
	}
	for _, c := range ownedResp.GetClusters() {
		if c == nil || c.GetClusterId() == "" {
			continue
		}
		if item, ok := byClusterID[c.GetClusterId()]; ok {
			item.AccessLevel = "owner"
			if item.ClusterName == "" {
				item.ClusterName = c.GetClusterName()
			}
			continue
		}
		item := &model.ClusterAccess{
			ClusterID:   c.GetClusterId(),
			ClusterName: c.GetClusterName(),
			AccessLevel: "owner",
		}
		byClusterID[item.ClusterID] = item
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
func (r *Resolver) DoUpdateTenant(ctx context.Context, input model.UpdateTenantInput) (*quartermasterpb.Tenant, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo tenant update")
		tenant := demo.GenerateTenant()
		// Apply updates to demo tenant
		if input.Name != nil {
			tenant.Name = *input.Name
		}
		return tenant, nil
	}

	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	r.Logger.WithField("tenant_id", tenantID).Info("Updating tenant")

	// Build gRPC update request
	updateReq := &quartermasterpb.UpdateTenantRequest{
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

		if v, present := raw["subdomain"]; present {
			val, ok := v.(string)
			if !ok {
				val = ""
			}
			subdomain := strings.ToLower(strings.TrimSpace(val))
			updateReq.Subdomain = &subdomain
			updates++
			changedFields = append(changedFields, "subdomain")
		}

		if v, present := raw["customDomain"]; present {
			val, ok := v.(string)
			if !ok {
				val = ""
			}
			trimmed := strings.TrimSpace(val)
			updateReq.CustomDomain = &trimmed
			updates++
			changedFields = append(changedFields, "custom_domain")
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

	r.sendServiceEvent(ctx, &ipcpb.ServiceEvent{
		EventType:    apiEventTenantUpdated,
		ResourceType: "tenant",
		ResourceId:   tenantID,
		Payload: &ipcpb.ServiceEvent_TenantEvent{
			TenantEvent: &ipcpb.TenantEvent{
				TenantId:      tenantID,
				ChangedFields: changedFields,
			},
		},
	})

	return r.DoGetTenant(ctx)
}

// DoUpdateStream updates stream settings
func (r *Resolver) DoUpdateStream(ctx context.Context, id string, input model.UpdateStreamInput) (*commodorepb.Stream, error) {
	if err := middleware.RequirePermission(ctx, "streams:write"); err != nil {
		return nil, err
	}
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

	tenantID := ctxkeys.GetTenantID(ctx)

	r.Logger.WithField("tenant_id", tenantID).
		WithField("stream_id", id).
		Info("Updating stream")

	// Build gRPC request
	req := &commodorepb.UpdateStreamRequest{
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
	if input.IngestMode != nil {
		req.IngestMode = strPtr(string(*input.IngestMode))
	}
	if input.PullSource != nil {
		req.PullSource = input.PullSource
	}
	if input.DvrChapterMode != nil {
		mode := chapterModeToString(*input.DvrChapterMode)
		req.DvrChapterMode = &mode
	}
	if input.DvrChapterIntervalSeconds != nil {
		v := int32(*input.DvrChapterIntervalSeconds)
		req.DvrChapterIntervalSeconds = &v
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
	if input.IngestMode != nil {
		changedFields = append(changedFields, "ingest_mode")
	}
	if input.PullSource != nil {
		changedFields = append(changedFields, "pull_source")
	}
	r.sendServiceEvent(ctx, &ipcpb.ServiceEvent{
		EventType:    apiEventStreamUpdated,
		ResourceType: "stream",
		ResourceId:   id,
		Payload: &ipcpb.ServiceEvent_StreamChangeEvent{
			StreamChangeEvent: &ipcpb.StreamChangeEvent{
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

	tenantID, _, err := r.requireClusterOperatorTenant(ctx)
	if err != nil {
		return nil, err
	}

	paginationReq := buildCursorPagination(first, after, last, before)

	resp, err := r.Clients.Quartermaster.ListClustersByOwner(ctx, tenantID, paginationReq)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get clusters")
		return nil, fmt.Errorf("failed to get clusters: %w", err)
	}

	return r.buildClustersConnectionFromResponse(resp), nil
}

// buildClustersConnectionFromResponse constructs a connection from gRPC response
func (r *Resolver) buildClustersConnectionFromResponse(resp *quartermasterpb.ListClustersResponse) *model.ClustersConnection {
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

	edgeNodes := make([]*quartermasterpb.InfrastructureCluster, 0, len(edges))
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
func (r *Resolver) buildClustersConnectionFromSlice(clusters []*quartermasterpb.InfrastructureCluster, first *int, after *string, last *int, before *string) *model.ClustersConnection {
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

	edgeNodes := make([]*quartermasterpb.InfrastructureCluster, 0, len(edges))
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

	nodes, err := r.DoGetNodes(ctx, clusterID, status, typeArg, nil, nil, nil)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get nodes")
		return nil, fmt.Errorf("failed to get nodes: %w", err)
	}

	return r.buildNodesConnectionFromSlice(nodes, first, after, last, before), nil
}

// buildNodesConnectionFromSlice constructs a connection from a slice (demo mode)
func (r *Resolver) buildNodesConnectionFromSlice(nodes []*quartermasterpb.InfrastructureNode, first *int, after *string, last *int, before *string) *model.NodesConnection {
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

	edgeNodes := make([]*quartermasterpb.InfrastructureNode, 0, len(edges))
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

	instances, err := r.DoGetServiceInstances(ctx, clusterID, nodeID, status, nil, nil)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to get service instances")
		return nil, fmt.Errorf("failed to get service instances: %w", err)
	}

	return r.buildServiceInstancesConnectionFromSlice(instances, first, after, last, before), nil
}

// buildServiceInstancesConnectionFromSlice constructs a connection from a slice (demo mode)
func (r *Resolver) buildServiceInstancesConnectionFromSlice(instances []*quartermasterpb.ServiceInstance, first *int, after *string, last *int, before *string) *model.ServiceInstancesConnection {
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

	edgeNodes := make([]*quartermasterpb.ServiceInstance, 0, len(edges))
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

// DoSubscribeToCluster subscribes a tenant to a cluster.
//
// Routes through Purser's CreateClusterSubscription so the cluster's
// pricing model is honored: free_unmetered / tier_inherit / metered
// grant access immediately through the correct Quartermaster entitlement path;
// monthly returns a Stripe checkout URL; custom returns pending_approval.
//
// Caller surfaces non-active outcomes as structured errors with stable
// "status:..." prefixes so the UI can switch on them. A typed payload
// would be cleaner but requires a coordinated GraphQL/Houdini change.
func (r *Resolver) DoSubscribeToCluster(ctx context.Context, clusterID string) (bool, error) {
	if middleware.IsDemoMode(ctx) {
		return false, errDemoUnavailable("Cluster subscriptions")
	}
	tenantID := ""
	if user := middleware.GetUserFromContext(ctx); user != nil {
		tenantID = user.TenantID
	}
	if tenantID == "" {
		return false, fmt.Errorf("tenant context required")
	}

	resp, err := r.Clients.Purser.CreateClusterSubscription(ctx, tenantID, clusterID, "")
	if err != nil {
		return false, fmt.Errorf("failed to subscribe: %w", err)
	}

	switch resp.GetStatus() {
	case "pending_payment":
		// Monthly cluster: caller must redirect the user to checkout
		// before access is granted (the Stripe webhook then provisions).
		return false, fmt.Errorf("status:pending_payment checkout_url:%s", resp.GetCheckoutUrl())
	case "pending_approval":
		// Custom cluster: cluster owner must approve.
		return false, fmt.Errorf("status:pending_approval")
	}

	r.sendServiceEvent(ctx, &ipcpb.ServiceEvent{
		EventType:    apiEventTenantClusterAssigned,
		ResourceType: "cluster",
		ResourceId:   clusterID,
		Payload: &ipcpb.ServiceEvent_ClusterEvent{
			ClusterEvent: &ipcpb.ClusterEvent{
				ClusterId: clusterID,
				TenantId:  tenantID,
			},
		},
	})
	return true, nil
}

// DoUnsubscribeFromCluster unsubscribes a tenant from a cluster
func (r *Resolver) DoUnsubscribeFromCluster(ctx context.Context, clusterID string) (bool, error) {
	if middleware.IsDemoMode(ctx) {
		return false, errDemoUnavailable("Cluster subscriptions")
	}
	tenantID := ""
	if user := middleware.GetUserFromContext(ctx); user != nil {
		tenantID = user.TenantID
	}
	if tenantID == "" {
		return false, fmt.Errorf("tenant context required")
	}

	_, err := r.Clients.Quartermaster.UnsubscribeFromCluster(ctx, &quartermasterpb.UnsubscribeFromClusterRequest{
		TenantId:  tenantID,
		ClusterId: clusterID,
	})
	if err != nil {
		return false, fmt.Errorf("failed to unsubscribe: %w", err)
	}

	r.sendServiceEvent(ctx, &ipcpb.ServiceEvent{
		EventType:    apiEventTenantClusterUnassigned,
		ResourceType: "cluster",
		ResourceId:   clusterID,
		Payload: &ipcpb.ServiceEvent_ClusterEvent{
			ClusterEvent: &ipcpb.ClusterEvent{
				ClusterId: clusterID,
				TenantId:  tenantID,
			},
		},
	})
	return true, nil
}

// DoListMySubscriptions lists clusters the tenant is subscribed to
func (r *Resolver) DoListMySubscriptions(ctx context.Context, first *int, after *string) ([]*quartermasterpb.InfrastructureCluster, error) {
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

	resp, err := r.Clients.Quartermaster.ListMySubscriptions(ctx, &quartermasterpb.ListMySubscriptionsRequest{
		TenantId:   tenantID,
		Pagination: buildCursorPagination(first, after, nil, nil),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list subscriptions: %w", err)
	}
	return resp.Clusters, nil
}

// DoCheckIsSubscribed checks if the current tenant is subscribed to the cluster
func (r *Resolver) DoCheckIsSubscribed(ctx context.Context, cluster *quartermasterpb.InfrastructureCluster) (bool, error) {
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
func (r *Resolver) DoListMarketplaceClusters(ctx context.Context, first *int, after *string) ([]*quartermasterpb.MarketplaceClusterEntry, error) {
	conn, err := r.DoGetMarketplaceClustersConnection(ctx, first, after, nil, nil)
	if err != nil {
		return nil, err
	}
	return conn.Nodes, nil
}

// DoGetMarketplaceCluster gets a marketplace cluster.
// Uses Purser for pricing and Quartermaster for metadata (consistent with connection method).
func (r *Resolver) DoGetMarketplaceCluster(ctx context.Context, clusterID string, inviteToken *string) (*quartermasterpb.MarketplaceClusterEntry, error) {
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
	cluster, err := r.Clients.Quartermaster.GetMarketplaceCluster(ctx, &quartermasterpb.GetMarketplaceClusterRequest{
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
	f, err := strconv.ParseFloat(price, 64)
	if err != nil {
		return 0
	}
	return int(math.Round(f * 100))
}

// pricingModelStringToProto converts Purser pricing model string to proto enum
func pricingModelStringToProto(s string) quartermasterpb.ClusterPricingModel {
	switch s {
	case "free_unmetered":
		return quartermasterpb.ClusterPricingModel_CLUSTER_PRICING_FREE_UNMETERED
	case "metered":
		return quartermasterpb.ClusterPricingModel_CLUSTER_PRICING_METERED
	case "monthly":
		return quartermasterpb.ClusterPricingModel_CLUSTER_PRICING_MONTHLY
	case "tier_inherit":
		return quartermasterpb.ClusterPricingModel_CLUSTER_PRICING_TIER_INHERIT
	case "custom":
		return quartermasterpb.ClusterPricingModel_CLUSTER_PRICING_CUSTOM
	default:
		return quartermasterpb.ClusterPricingModel_CLUSTER_PRICING_FREE_UNMETERED
	}
}

// pricingModelProtoToString converts proto enum to Purser string
func pricingModelProtoToString(p quartermasterpb.ClusterPricingModel) string {
	switch p {
	case quartermasterpb.ClusterPricingModel_CLUSTER_PRICING_FREE_UNMETERED:
		return "free_unmetered"
	case quartermasterpb.ClusterPricingModel_CLUSTER_PRICING_METERED:
		return "metered"
	case quartermasterpb.ClusterPricingModel_CLUSTER_PRICING_MONTHLY:
		return "monthly"
	case quartermasterpb.ClusterPricingModel_CLUSTER_PRICING_TIER_INHERIT:
		return "tier_inherit"
	case quartermasterpb.ClusterPricingModel_CLUSTER_PRICING_CUSTOM:
		return "custom"
	default:
		return "tier_inherit"
	}
}

// DoCreateEdgeCluster creates an edge cluster with Foghorn assignment and enrollment token
func (r *Resolver) DoCreateEdgeCluster(ctx context.Context, input model.CreateEdgeClusterInput) (model.CreateEdgeClusterResult, error) {
	if middleware.IsDemoMode(ctx) {
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

	req := &quartermasterpb.EnableSelfHostingRequest{
		TenantId:         tenantID,
		ClusterName:      input.ClusterName,
		ClientIp:         requestClientIP(ctx),
		ControlClusterId: optionalStringField(input, "ControlClusterID"),
	}
	if input.ShortDescription != nil {
		req.ShortDescription = input.ShortDescription
	}

	resp, err := r.Clients.Quartermaster.EnableSelfHosting(ctx, req)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to create edge cluster")
		return &model.ValidationError{
			Message: fmt.Sprintf("Failed to create edge cluster: %v", err),
		}, nil
	}
	if resp == nil {
		r.Logger.Error("Quartermaster returned empty edge cluster response")
		return &model.ValidationError{Message: "Failed to create edge cluster"}, nil
	}

	if resp != nil && resp.Cluster != nil {
		clusterID := resp.Cluster.ClusterId
		if clusterID == "" {
			clusterID = resp.Cluster.Id
		}
		r.sendServiceEvent(ctx, &ipcpb.ServiceEvent{
			EventType:    apiEventClusterCreated,
			ResourceType: "cluster",
			ResourceId:   clusterID,
			Payload: &ipcpb.ServiceEvent_ClusterEvent{
				ClusterEvent: &ipcpb.ClusterEvent{
					ClusterId: clusterID,
					TenantId:  tenantID,
				},
			},
		})
	}

	return &model.CreateEdgeClusterResponse{
		Cluster:        resp.Cluster,
		BootstrapToken: resp.BootstrapToken,
		FoghornAddr:    resp.FoghornAddr,
	}, nil
}

type clientIPContext interface {
	ClientIP() string
}

func requestClientIP(ctx context.Context) string {
	if ip := strings.TrimSpace(ctxkeys.GetClientIP(ctx)); ip != "" {
		return ip
	}
	if ginCtx := ctx.Value(ctxkeys.KeyGinContext); ginCtx != nil {
		if c, ok := ginCtx.(clientIPContext); ok {
			return strings.TrimSpace(c.ClientIP())
		}
	}
	return ""
}

func optionalStringField(v any, fieldName string) string {
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Pointer {
		if rv.IsNil() {
			return ""
		}
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return ""
	}
	field := rv.FieldByName(fieldName)
	if !field.IsValid() {
		return ""
	}
	switch field.Kind() {
	case reflect.String:
		return strings.TrimSpace(field.String())
	case reflect.Pointer:
		if field.IsNil() || field.Elem().Kind() != reflect.String {
			return ""
		}
		return strings.TrimSpace(field.Elem().String())
	default:
		return ""
	}
}

// DoCreateEnrollmentToken creates an enrollment token for a cluster
func (r *Resolver) DoCreateEnrollmentToken(ctx context.Context, clusterID string, name *string, ttl *string) (model.CreateEnrollmentTokenResult, error) {
	if middleware.IsDemoMode(ctx) {
		demoField := "demo"
		return &model.ValidationError{
			Message: "Cannot create tokens in demo mode",
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

	req := &quartermasterpb.CreateEnrollmentTokenRequest{
		ClusterId: clusterID,
		TenantId:  &tenantID,
	}
	if name != nil {
		req.Name = name
	}
	if ttl != nil {
		req.Ttl = ttl
	}

	resp, err := r.Clients.Quartermaster.CreateEnrollmentToken(ctx, req)
	if err != nil {
		r.Logger.WithError(err).Error("Failed to create enrollment token")
		return &model.ValidationError{
			Message: fmt.Sprintf("Failed to create enrollment token: %v", err),
		}, nil
	}

	return &model.CreateEnrollmentTokenResponse{
		BootstrapToken: resp.Token,
	}, nil
}

// DoBootstrapEdge resolves a bootstrap token to its assigned cluster's
// Foghorn (via Quartermaster) and proxies a PreRegisterEdge call. The
// bootstrap token is itself the credential — no JWT is required. The
// Bridge auth middleware allowlists this single field for public access.
func (r *Resolver) DoBootstrapEdge(ctx context.Context, input model.BootstrapEdgeInput) (model.BootstrapEdgeResult, error) {
	if middleware.IsDemoMode(ctx) {
		demoField := "demo"
		return &model.ValidationError{
			Message: "Cannot bootstrap edges in demo mode",
			Field:   &demoField,
		}, nil
	}

	token := strings.TrimSpace(input.Token)
	if token == "" {
		field := "token"
		return &model.ValidationError{Message: "token required", Field: &field}, nil
	}

	val, err := r.Clients.Quartermaster.ValidateBootstrapToken(ctx, token)
	if err != nil {
		r.Logger.WithError(err).Error("ValidateBootstrapToken failed")
		return &model.ValidationError{Message: "Failed to validate bootstrap token"}, nil
	}
	if !val.GetValid() {
		reason := val.GetReason()
		if reason == "" {
			reason = "invalid"
		}
		return &model.ValidationError{Message: "Bootstrap token rejected: " + reason}, nil
	}
	if val.GetKind() != "edge_node" {
		return &model.ValidationError{Message: "Token is not an edge bootstrap token"}, nil
	}
	addr := val.GetFoghornGrpcAddr()
	if addr == "" {
		return &model.ValidationError{Message: "Cluster has no Foghorn assignment yet"}, nil
	}

	fh, err := fhclient.NewGRPCClient(fhclient.GRPCConfig{
		GRPCAddr: addr,
		Timeout:  30 * time.Second,
		Logger:   r.Logger,
	})
	if err != nil {
		r.Logger.WithError(err).WithField("foghorn_addr", addr).Error("dial Foghorn for bootstrapEdge")
		return &model.ValidationError{Message: "Failed to reach assigned Foghorn"}, nil
	}
	defer func() { _ = fh.Close() }()

	preReq := &foghornpb.PreRegisterEdgeRequest{EnrollmentToken: token}
	if input.ExternalIP != nil {
		preReq.ExternalIp = *input.ExternalIP
	}
	if input.PreferredNodeID != nil {
		preReq.PreferredNodeId = *input.PreferredNodeID
	}

	preResp, err := fh.PreRegisterEdge(ctx, preReq)
	if err != nil {
		r.Logger.WithError(err).Error("Foghorn PreRegisterEdge failed")
		return &model.ValidationError{Message: fmt.Sprintf("PreRegisterEdge failed: %v", err)}, nil
	}

	out := &model.BootstrapEdgeResponse{
		NodeID:          preResp.GetNodeId(),
		EdgeDomain:      preResp.GetEdgeDomain(),
		ClusterSlug:     preResp.GetClusterSlug(),
		ClusterID:       preResp.GetClusterId(),
		FoghornGrpcAddr: preResp.GetFoghornGrpcAddr(),
	}
	if v := preResp.GetPoolDomain(); v != "" {
		out.PoolDomain = &v
	}
	if v := string(preResp.GetInternalCaBundle()); v != "" {
		out.InternalCaBundle = &v
	}
	if t := preResp.GetTelemetry(); t != nil {
		setup := &model.EdgeTelemetrySetup{Enabled: t.GetEnabled()}
		if v := t.GetWriteUrl(); v != "" {
			setup.WriteURL = &v
		}
		if v := t.GetBearerToken(); v != "" {
			setup.BearerToken = &v
		}
		out.Telemetry = setup
	}
	return out, nil
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
		pricingReq := &purserpb.SetClusterPricingRequest{
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
	req := &quartermasterpb.UpdateClusterMarketplaceRequest{
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

	r.sendServiceEvent(ctx, &ipcpb.ServiceEvent{
		EventType:    apiEventClusterUpdated,
		ResourceType: "cluster",
		ResourceId:   clusterID,
		Payload: &ipcpb.ServiceEvent_ClusterEvent{
			ClusterEvent: &ipcpb.ClusterEvent{
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
			if basePrice, parseErr := strconv.ParseFloat(pricing.BasePrice, 64); parseErr == nil {
				cluster.MonthlyPriceCents = int32(basePrice * 100)
			}
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

	req := &quartermasterpb.CreateClusterInviteRequest{
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
		r.sendServiceEvent(ctx, &ipcpb.ServiceEvent{
			EventType:    apiEventClusterInviteCreated,
			ResourceType: "cluster_invite",
			ResourceId:   invite.Id,
			Payload: &ipcpb.ServiceEvent_ClusterEvent{
				ClusterEvent: &ipcpb.ClusterEvent{
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

	err := r.Clients.Quartermaster.RevokeClusterInvite(ctx, &quartermasterpb.RevokeClusterInviteRequest{
		InviteId:      inviteID,
		OwnerTenantId: tenantID,
	})
	if err != nil {
		r.Logger.WithError(err).Error("Failed to revoke cluster invite")
		return &model.NotFoundError{
			Message: fmt.Sprintf("Failed to revoke invite: %v", err),
		}, nil
	}

	r.sendServiceEvent(ctx, &ipcpb.ServiceEvent{
		EventType:    apiEventClusterInviteRevoked,
		ResourceType: "cluster_invite",
		ResourceId:   inviteID,
		Payload: &ipcpb.ServiceEvent_ClusterEvent{
			ClusterEvent: &ipcpb.ClusterEvent{
				TenantId: tenantID,
				InviteId: inviteID,
			},
		},
	})

	return &model.DeleteSuccess{Success: true}, nil
}

// DoListClusterInvites lists invites for a cluster
func (r *Resolver) DoListClusterInvites(ctx context.Context, clusterID string) ([]*quartermasterpb.ClusterInvite, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo cluster invites")
		return demo.GenerateClusterInvites(), nil
	}

	tenantID := ""
	if user := middleware.GetUserFromContext(ctx); user != nil {
		tenantID = user.TenantID
	}

	resp, err := r.Clients.Quartermaster.ListClusterInvites(ctx, &quartermasterpb.ListClusterInvitesRequest{
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
func (r *Resolver) DoListMyClusterInvites(ctx context.Context) ([]*quartermasterpb.ClusterInvite, error) {
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

	resp, err := r.Clients.Quartermaster.ListMyClusterInvites(ctx, &quartermasterpb.ListMyClusterInvitesRequest{
		TenantId: tenantID,
	})
	if err != nil {
		r.Logger.WithError(err).Error("Failed to list my cluster invites")
		return nil, fmt.Errorf("failed to list invites: %w", err)
	}

	return resp.Invites, nil
}

// DoRequestClusterSubscription requests subscription to a cluster.
//
// Defensive pricing precondition: monthly clusters require Stripe checkout
// before access can be granted. Letting an approval workflow short-circuit
// payment would create active access without a paid subscription, so we
// route monthly through the subscribeToCluster flow instead.
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

	if gate := r.requireCommercialAccessAllowed(ctx, tenantID, clusterID); gate != nil {
		return gate, nil
	}

	req := &quartermasterpb.RequestClusterSubscriptionRequest{
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
		r.sendServiceEvent(ctx, &ipcpb.ServiceEvent{
			EventType:    apiEventClusterSubscriptionRequested,
			ResourceType: "cluster_subscription",
			ResourceId:   sub.Id,
			Payload: &ipcpb.ServiceEvent_ClusterEvent{
				ClusterEvent: &ipcpb.ClusterEvent{
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

	clusterID, lookupFailure := r.clusterIDForInviteToken(ctx, tenantID, inviteToken)
	if lookupFailure != "" {
		return &model.ValidationError{Message: lookupFailure}, nil
	}
	if gate := r.requireCommercialAccessAllowed(ctx, tenantID, clusterID); gate != nil {
		return gate, nil
	}

	sub, err := r.Clients.Quartermaster.AcceptClusterInvite(ctx, &quartermasterpb.AcceptClusterInviteRequest{
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
		r.sendServiceEvent(ctx, &ipcpb.ServiceEvent{
			EventType:    apiEventClusterSubscriptionApproved,
			ResourceType: "cluster_subscription",
			ResourceId:   sub.Id,
			Payload: &ipcpb.ServiceEvent_ClusterEvent{
				ClusterEvent: &ipcpb.ClusterEvent{
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
func (r *Resolver) DoListPendingSubscriptions(ctx context.Context, clusterID string) ([]*quartermasterpb.ClusterSubscription, error) {
	if middleware.IsDemoMode(ctx) {
		r.Logger.Debug("Returning demo pending subscriptions")
		return demo.GeneratePendingSubscriptions(), nil
	}

	tenantID := ""
	if user := middleware.GetUserFromContext(ctx); user != nil {
		tenantID = user.TenantID
	}

	resp, err := r.Clients.Quartermaster.ListPendingSubscriptions(ctx, &quartermasterpb.ListPendingSubscriptionsRequest{
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

	sub, err := r.Clients.Quartermaster.ApproveClusterSubscription(ctx, &quartermasterpb.ApproveClusterSubscriptionRequest{
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
		r.sendServiceEvent(ctx, &ipcpb.ServiceEvent{
			EventType:    apiEventClusterSubscriptionApproved,
			ResourceType: "cluster_subscription",
			ResourceId:   sub.Id,
			Payload: &ipcpb.ServiceEvent_ClusterEvent{
				ClusterEvent: &ipcpb.ClusterEvent{
					ClusterId:      sub.ClusterId,
					TenantId:       tenantID,
					SubscriptionId: sub.Id,
				},
			},
		})
	}

	return sub, nil
}

func (r *Resolver) clusterIDForInviteToken(ctx context.Context, tenantID, inviteToken string) (string, string) {
	if r.Clients.Quartermaster == nil {
		return "", "cluster service unavailable"
	}
	resp, err := r.Clients.Quartermaster.ListMyClusterInvites(ctx, &quartermasterpb.ListMyClusterInvitesRequest{TenantId: tenantID})
	if err != nil {
		return "", fmt.Sprintf("failed to verify invite pricing: %v", err)
	}
	for _, invite := range resp.GetInvites() {
		if invite.GetInviteToken() == inviteToken {
			return invite.GetClusterId(), ""
		}
	}
	return "", "invalid or expired invite token"
}

// requireCommercialAccessAllowed asks Purser whether this tenant can enter a
// cluster access flow before Quartermaster writes tenant_cluster_access.
func (r *Resolver) requireCommercialAccessAllowed(ctx context.Context, tenantID, clusterID string) model.ClusterSubscriptionResult {
	if r.Clients.Purser == nil {
		return &model.ValidationError{Message: "Billing service unavailable; cannot verify cluster pricing"}
	}
	access, err := r.Clients.Purser.CheckClusterAccess(ctx, tenantID, clusterID)
	if err != nil {
		return &model.ValidationError{Message: fmt.Sprintf("Failed to verify cluster pricing: %v", err)}
	}
	if access.GetPricingModel() == "monthly" {
		return &model.ValidationError{
			Message: "This cluster requires a paid subscription. Use subscribeToCluster to start Stripe checkout.",
		}
	}
	if !access.GetAllowed() {
		reason := access.GetDenialReason()
		if reason == "" {
			reason = "Cluster is not available for this tenant"
		}
		return &model.ValidationError{Message: reason}
	}
	return nil
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

	req := &quartermasterpb.RejectClusterSubscriptionRequest{
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
		reasonCode := ipcpb.ClusterRejectReason_CLUSTER_REJECT_REASON_UNSPECIFIED
		if reason != nil {
			eventReason = truncateReason(*reason)
			reasonCode = parseRejectReasonCode(*reason)
		}
		r.sendServiceEvent(ctx, &ipcpb.ServiceEvent{
			EventType:    apiEventClusterSubscriptionRejected,
			ResourceType: "cluster_subscription",
			ResourceId:   sub.Id,
			TenantId:     tenantID,
			Payload: &ipcpb.ServiceEvent_ClusterEvent{
				ClusterEvent: &ipcpb.ClusterEvent{
					ClusterId:        sub.ClusterId,
					TenantId:         tenantID,
					SubscriptionId:   sub.Id,
					Reason:           eventReason,
					RejectReasonCode: reasonCode,
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

	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	limit := infraMaxLimit
	items, err := r.DoGetClustersAccess(ctx, &limit, nil)
	if err != nil {
		return nil, err
	}
	return buildClustersAccessConnectionFromSlice(items, first, after, last, before), nil
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
func buildClustersAvailableConnectionFromResponse(resp *quartermasterpb.ClustersAvailableResponse) *model.AvailableClusterConnection {
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

	resp, err := r.Clients.Quartermaster.ListMySubscriptions(ctx, &quartermasterpb.ListMySubscriptionsRequest{
		TenantId:   tenantID,
		Pagination: buildCursorPagination(first, after, last, before),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list subscriptions: %w", err)
	}

	return buildMySubscriptionsConnectionFromResponse(resp), nil
}

// buildMySubscriptionsConnectionFromResponse builds a connection from a ListClustersResponse.
func buildMySubscriptionsConnectionFromResponse(resp *quartermasterpb.ListClustersResponse) *model.MySubscriptionsConnection {
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

	nodes := make([]*quartermasterpb.InfrastructureCluster, 0, len(edges))
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
func buildMySubscriptionsConnectionFromSlice(items []*quartermasterpb.InfrastructureCluster, first *int, after *string, last *int, before *string) *model.MySubscriptionsConnection {
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

	nodes := make([]*quartermasterpb.InfrastructureCluster, 0, len(edges))
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
	qmResp, err := r.Clients.Quartermaster.ListMarketplaceClusters(ctx, &quartermasterpb.ListMarketplaceClustersRequest{
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
			Nodes:      []*quartermasterpb.MarketplaceClusterEntry{},
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
	nodes := make([]*quartermasterpb.MarketplaceClusterEntry, 0, len(clusters))

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
func buildMarketplaceClustersConnectionFromSlice(items []*quartermasterpb.MarketplaceClusterEntry, first *int, after *string, last *int, before *string) *model.MarketplaceClusterConnection {
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

	resp, err := r.Clients.Quartermaster.ListPendingSubscriptions(ctx, &quartermasterpb.ListPendingSubscriptionsRequest{
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
func buildPendingSubscriptionsConnectionFromResponse(resp *quartermasterpb.ListPendingSubscriptionsResponse) *model.ClusterSubscriptionConnection {
	edges := make([]*model.ClusterSubscriptionEdge, len(resp.Subscriptions))
	nodes := make([]*quartermasterpb.ClusterSubscription, len(resp.Subscriptions))
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
func buildPendingSubscriptionsConnectionFromSlice(items []*quartermasterpb.ClusterSubscription, first *int, after *string, last *int, before *string) *model.ClusterSubscriptionConnection {
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

	resp, err := r.Clients.Quartermaster.ListClusterInvites(ctx, &quartermasterpb.ListClusterInvitesRequest{
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
func buildClusterInvitesConnectionFromResponse(resp *quartermasterpb.ListClusterInvitesResponse) *model.ClusterInviteConnection {
	edges := make([]*model.ClusterInviteEdge, len(resp.Invites))
	nodes := make([]*quartermasterpb.ClusterInvite, len(resp.Invites))
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
func buildClusterInvitesConnectionFromSlice(items []*quartermasterpb.ClusterInvite, first *int, after *string, last *int, before *string) *model.ClusterInviteConnection {
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

// DoSetPreferredCluster sets the tenant's preferred cluster for DNS steering.
func (r *Resolver) DoSetPreferredCluster(ctx context.Context, clusterID string) (model.SetPreferredClusterResult, error) {
	if middleware.IsDemoMode(ctx) {
		demoField := "demo"
		return &model.ValidationError{
			Message: "Cannot set preferred cluster in demo mode",
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

	err := r.Clients.Quartermaster.UpdateTenantCluster(ctx, &quartermasterpb.UpdateTenantClusterRequest{
		TenantId:         tenantID,
		PrimaryClusterId: &clusterID,
	})
	if err != nil {
		r.Logger.WithError(err).Error("Failed to set preferred cluster")
		return &model.ValidationError{
			Message: fmt.Sprintf("Failed to set preferred cluster: %v", err),
		}, nil
	}

	cluster, err := r.DoGetCluster(ctx, clusterID)
	if err != nil {
		return &model.NotFoundError{ //nolint:nilerr // error encoded in typed response
			Message:      "Cluster updated but failed to fetch details",
			ResourceType: "cluster",
			ResourceID:   clusterID,
		}, nil
	}

	r.sendServiceEvent(ctx, &ipcpb.ServiceEvent{
		EventType:    apiEventTenantUpdated,
		ResourceType: "tenant",
		ResourceId:   tenantID,
		Payload: &ipcpb.ServiceEvent_TenantEvent{
			TenantEvent: &ipcpb.TenantEvent{
				TenantId:      tenantID,
				ChangedFields: []string{"primary_cluster_id"},
			},
		},
	})

	return cluster, nil
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

	resp, err := r.Clients.Quartermaster.ListMyClusterInvites(ctx, &quartermasterpb.ListMyClusterInvitesRequest{
		TenantId:   tenantID,
		Pagination: buildCursorPagination(first, after, last, before),
	})
	if err != nil {
		r.Logger.WithError(err).Error("Failed to list my cluster invites")
		return nil, fmt.Errorf("failed to list my cluster invites: %w", err)
	}

	return buildClusterInvitesConnectionFromResponse(resp), nil
}

// DoGetStreamingConfig returns cluster-aware streaming domains for the
// authenticated tenant. Returns nil (not error) when unavailable so the
// frontend falls back to VITE_* env vars.
func (r *Resolver) DoGetStreamingConfig(ctx context.Context) (*model.StreamingConfig, error) {
	if middleware.IsDemoMode(ctx) {
		return demo.GenerateStreamingConfig(), nil
	}

	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, nil
	}

	resp, err := r.Clients.Quartermaster.GetClusterRouting(ctx, &quartermasterpb.GetClusterRoutingRequest{TenantId: tenantID})
	if err != nil {
		r.Logger.WithError(err).Debug("streamingConfig: cluster routing unavailable, returning nil")
		return nil, nil
	}

	slug := resp.GetClusterSlug()
	baseURL := resp.GetBaseUrl()
	if slug == "" || baseURL == "" {
		return nil, nil
	}

	srtPort := config.GetEnvInt("STREAMING_SRT_PORT", 8889)
	rtmpPort := config.GetEnvInt("STREAMING_RTMP_PORT", 1935)

	cfg := &model.StreamingConfig{
		IngestDomain:   strPtr(streamingConfigDomain("edge-ingest", slug, baseURL)),
		EdgeDomain:     strPtr(streamingConfigDomain("edge-egress", slug, baseURL)),
		PlayDomain:     strPtr(streamingConfigDomain("foghorn", slug, baseURL)),
		ChandlerDomain: strPtr(streamingConfigDomain("chandler", slug, baseURL)),
		SrtPort:        &srtPort,
		RtmpPort:       &rtmpPort,
	}

	if name := resp.GetClusterName(); name != "" {
		cfg.PreferredClusterLabel = strPtr(name)
	}

	offSlug := resp.GetOfficialClusterSlug()
	offBase := resp.GetOfficialBaseUrl()
	if offSlug != "" && offBase != "" {
		cfg.OfficialIngestDomain = strPtr(streamingConfigDomain("edge-ingest", offSlug, offBase))
		cfg.OfficialEdgeDomain = strPtr(streamingConfigDomain("edge-egress", offSlug, offBase))
		cfg.OfficialPlayDomain = strPtr(streamingConfigDomain("foghorn", offSlug, offBase))
		cfg.OfficialChandlerDomain = strPtr(streamingConfigDomain("chandler", offSlug, offBase))
		if name := resp.GetOfficialClusterName(); name != "" {
			cfg.OfficialClusterLabel = strPtr(name)
		}
	}

	r.populateTieredStreamingDomains(ctx, cfg, baseURL)

	return cfg, nil
}

func (r *Resolver) populateTieredStreamingDomains(ctx context.Context, cfg *model.StreamingConfig, baseURL string) {
	rootDomain := normalizeStreamingBaseDomain(baseURL)
	if rootDomain == "" {
		rootDomain = strings.TrimSpace(config.GetEnv("BRAND_DOMAIN", ""))
	}
	if rootDomain == "" {
		return
	}
	cfg.GlobalIngestDomain = strPtr("edge-ingest." + rootDomain)
	cfg.GlobalEdgeDomain = strPtr("edge-egress." + rootDomain)
	cfg.GlobalPlayDomain = strPtr("foghorn." + rootDomain)
	cfg.GlobalChandlerDomain = strPtr("chandler." + rootDomain)
	cfg.GlobalLivepeerDomain = strPtr("livepeer." + rootDomain)

	if r == nil || r.Clients == nil || r.Clients.Navigator == nil {
		return
	}
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return
	}
	aliasCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	tenantResp, err := r.Clients.Quartermaster.GetTenant(aliasCtx, tenantID)
	if err != nil || tenantResp == nil || !tenantAliasEligibleForStreaming(tenantResp.GetTenant()) {
		return
	}
	alias, err := r.Clients.Navigator.GetTenantAliasStatus(aliasCtx, &dnspb.GetTenantAliasStatusRequest{TenantId: tenantID})
	if err != nil || alias == nil || !alias.GetFound() || alias.GetStatus() != "cert_issued" || !alias.GetDnsReady() {
		return
	}
	apex := alias.GetSubdomain() + "." + pkgdns.TenantAliasZoneLabel + "." + rootDomain
	cfg.TenantIngestDomain = strPtr("edge-ingest." + apex)
	cfg.TenantEdgeDomain = strPtr("edge-egress." + apex)
	cfg.TenantPlayDomain = strPtr("foghorn." + apex)
	cfg.TenantChandlerDomain = strPtr("chandler." + apex)
	cfg.TenantLivepeerDomain = strPtr("livepeer." + apex)
}

func tenantAliasEligibleForStreaming(tenant *quartermasterpb.Tenant) bool {
	if tenant == nil || !tenant.GetIsActive() {
		return false
	}
	return models.DeploymentTierAliasEligible(tenant.GetDeploymentTier())
}

func streamingConfigDomain(prefix, slug, baseURL string) string {
	baseDomain := normalizeStreamingBaseDomain(baseURL)
	if prefix == "" || slug == "" || baseDomain == "" {
		return ""
	}
	return fmt.Sprintf("%s.%s.%s", prefix, slug, baseDomain)
}

func normalizeStreamingBaseDomain(baseURL string) string {
	domain := strings.TrimSpace(baseURL)
	if domain == "" {
		return ""
	}
	if parsed, err := url.Parse(domain); err == nil && parsed.Host != "" {
		domain = parsed.Host
	}
	domain = strings.TrimPrefix(domain, "//")
	if before, _, ok := strings.Cut(domain, "/"); ok {
		domain = before
	}
	if before, _, ok := strings.Cut(domain, "?"); ok {
		domain = before
	}
	if before, _, ok := strings.Cut(domain, "#"); ok {
		domain = before
	}
	domain = strings.Trim(domain, ".")
	if host, port, ok := strings.Cut(domain, ":"); ok {
		if _, err := strconv.Atoi(port); err == nil {
			domain = host
		}
	}
	return strings.Trim(domain, ".")
}
