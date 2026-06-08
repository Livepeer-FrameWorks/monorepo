package resolvers

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/periscope"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func isLivepeerGatewayService(serviceID string) bool {
	serviceID = strings.TrimSpace(serviceID)
	return serviceID == "livepeer-gateway" || strings.HasPrefix(serviceID, "livepeer-gateway-")
}

// networkOrchestratorOwnerTenants derives public orchestrator scope from
// public topology gateway clusters plus signed-in tenant accessible clusters.
func (r *Resolver) networkOrchestratorOwnerTenants(ctx context.Context) ([]string, error) {
	// Every orchestrator root resolver funnels through here, so this is the one
	// place to fail demo requests closed: orchestrator data is real network
	// state with no demo representation.
	if middleware.IsDemoMode(ctx) {
		return nil, errDemoUnavailable("Orchestrator data")
	}
	tenantID := tenantIDFromContext(ctx)
	cacheParts := []string{"public"}
	if tenantID != "" {
		cacheParts = []string{"tenant", tenantID}
	}

	val, err := r.fetchPeriscope(ctx, "network_orchestrator_owner_tenants", cacheParts, func(ctx context.Context) (any, error) {
		clustersResp, publicCtx, err := r.listPublicNetworkClusters(ctx)
		if err != nil {
			return nil, err
		}

		clusterByID := make(map[string]*quartermasterpb.InfrastructureCluster)
		publicClusterIDs := make(map[string]struct{}, len(clustersResp.GetClusters()))
		addClusters := func(clusters []*quartermasterpb.InfrastructureCluster, publicTopology bool) {
			for _, cluster := range clusters {
				if cluster == nil || !cluster.GetIsActive() || cluster.GetClusterId() == "" {
					continue
				}
				clusterID := cluster.GetClusterId()
				clusterByID[clusterID] = cluster
				if publicTopology {
					publicClusterIDs[clusterID] = struct{}{}
				} else {
					delete(publicClusterIDs, clusterID)
				}
			}
		}
		addClusters(clustersResp.GetClusters(), true)

		if tenantID != "" {
			accessResp, accessErr := r.Clients.Quartermaster.ListMySubscriptions(ctx, &quartermasterpb.ListMySubscriptionsRequest{
				TenantId:   tenantID,
				Pagination: &commonpb.CursorPaginationRequest{First: 500},
			})
			if accessErr != nil {
				r.Logger.WithError(accessErr).Warn("orchestrator scope: failed to load tenant cluster access")
			} else {
				addClusters(accessResp.GetClusters(), false)
			}

			ownedResp, ownedErr := r.Clients.Quartermaster.ListClustersByOwner(ctx, tenantID, &commonpb.CursorPaginationRequest{First: 500})
			if ownedErr != nil {
				r.Logger.WithError(ownedErr).Warn("orchestrator scope: failed to load owned clusters")
			} else {
				addClusters(ownedResp.GetClusters(), false)
			}
		}

		seen := make(map[string]struct{})
		for clusterID, cluster := range clusterByID {
			readCtx := ctx
			if _, publicCluster := publicClusterIDs[clusterID]; publicCluster {
				readCtx = publicCtx
			}
			instancesResp, instanceErr := r.Clients.Quartermaster.ListServiceInstances(readCtx, clusterID, "", "", &commonpb.CursorPaginationRequest{First: 2000})
			if instanceErr != nil {
				r.Logger.WithError(instanceErr).WithField("cluster_id", clusterID).Warn("orchestrator scope: failed to load cluster service instances")
				continue
			}
			for _, instance := range instancesResp.GetInstances() {
				if instance == nil || !isLivepeerGatewayService(instance.GetServiceId()) {
					continue
				}
				ownerTenantID := strings.TrimSpace(cluster.GetOwnerTenantId())
				if ownerTenantID == "" {
					r.Logger.WithField("cluster_id", cluster.GetClusterId()).Warn("livepeer gateway cluster missing owner_tenant_id")
					continue
				}
				seen[ownerTenantID] = struct{}{}
			}
		}
		tenantIDs := make([]string, 0, len(seen))
		for tenantID := range seen {
			tenantIDs = append(tenantIDs, tenantID)
		}
		sort.Strings(tenantIDs)
		return tenantIDs, nil
	})
	if err != nil {
		return nil, fmt.Errorf("load network orchestrator tenant scope: %w", err)
	}
	tenantIDs, ok := val.([]string)
	if !ok {
		return nil, fmt.Errorf("unexpected network orchestrator tenant scope type: %T", val)
	}
	return tenantIDs, nil
}

func networkOrchestratorScopeKey(tenantIDs []string, parts ...string) []string {
	out := make([]string, 0, len(tenantIDs)+len(parts))
	out = append(out, tenantIDs...)
	out = append(out, parts...)
	return out
}

// DoListOrchestrators returns public network-orchestrator identity rows.
// Per-instance config lives on OrchestratorInstance.
func (r *Resolver) DoListOrchestrators(ctx context.Context, first *int, after *string, orchAddr *string) (*model.OrchestratorsConnection, error) {
	tenantIDs, err := r.networkOrchestratorOwnerTenants(ctx)
	if err != nil {
		return nil, err
	}

	cacheKey := ""
	if orchAddr != nil {
		cacheKey = *orchAddr
	}
	val, err := r.fetchPeriscope(ctx, "orchestrators_list", networkOrchestratorScopeKey(tenantIDs, cacheKey), func(ctx context.Context) (any, error) {
		var out []*periscopepb.Orchestrator
		for _, tenantID := range tenantIDs {
			periscopeResp, callErr := r.Clients.Periscope.ListOrchestrators(ctx, tenantID, orchAddr, &periscope.CursorPaginationOpts{
				First: deref32(first),
				After: after,
			})
			if callErr != nil {
				return nil, callErr
			}
			out = append(out, periscopeResp.GetOrchestrators()...)
		}
		return &periscopepb.ListOrchestratorsResponse{Orchestrators: out}, nil
	})
	if err != nil {
		return nil, err
	}
	resp, ok := val.(*periscopepb.ListOrchestratorsResponse)
	if !ok {
		return nil, fmt.Errorf("unexpected response type for ListOrchestrators: %T", val)
	}
	nodes := resp.GetOrchestrators()
	return &model.OrchestratorsConnection{
		Nodes:      nodes,
		TotalCount: len(nodes),
	}, nil
}

// DoGetOrchestrator returns one orchestrator's identity plus known instances
// and per-(gateway, instance) vantage rows for the public network topology.
func (r *Resolver) DoGetOrchestrator(ctx context.Context, orchAddr string) (*model.OrchestratorWithDetails, error) {
	tenantIDs, err := r.networkOrchestratorOwnerTenants(ctx)
	if err != nil {
		return nil, err
	}

	val, err := r.fetchPeriscope(ctx, "orchestrator_detail", networkOrchestratorScopeKey(tenantIDs, orchAddr), func(ctx context.Context) (any, error) {
		var lastNotFound error
		for _, tenantID := range tenantIDs {
			periscopeResp, callErr := r.Clients.Periscope.GetOrchestrator(ctx, tenantID, orchAddr)
			if callErr != nil {
				if status.Code(callErr) == codes.NotFound {
					lastNotFound = callErr
					continue
				}
				return nil, callErr
			}
			return periscopeResp, nil
		}
		if lastNotFound != nil {
			return nil, lastNotFound
		}
		return nil, nil
	})
	if err != nil {
		if status.Code(err) == codes.NotFound {
			return nil, nil
		}
		return nil, err
	}
	resp, ok := val.(*periscopepb.GetOrchestratorResponse)
	if !ok {
		return nil, fmt.Errorf("unexpected response type for GetOrchestrator: %T", val)
	}
	if resp == nil || resp.GetOrchestrator() == nil {
		return nil, nil
	}
	return &model.OrchestratorWithDetails{
		Orchestrator: resp.GetOrchestrator(),
		Instances:    resp.GetInstances(),
		Vantages:     resp.GetVantages(),
	}, nil
}

// DoListOrchestratorInstances returns public network per-instance rows.
func (r *Resolver) DoListOrchestratorInstances(ctx context.Context, orchAddr *string) ([]*periscopepb.OrchestratorInstance, error) {
	tenantIDs, err := r.networkOrchestratorOwnerTenants(ctx)
	if err != nil {
		return nil, err
	}
	cacheKey := ""
	if orchAddr != nil {
		cacheKey = *orchAddr
	}
	val, err := r.fetchPeriscope(ctx, "orchestrator_instances", networkOrchestratorScopeKey(tenantIDs, cacheKey), func(ctx context.Context) (any, error) {
		var out []*periscopepb.OrchestratorInstance
		for _, tenantID := range tenantIDs {
			periscopeResp, callErr := r.Clients.Periscope.ListOrchestratorInstances(ctx, tenantID, orchAddr)
			if callErr != nil {
				return nil, callErr
			}
			out = append(out, periscopeResp.GetInstances()...)
		}
		return &periscopepb.ListOrchestratorInstancesResponse{Instances: out}, nil
	})
	if err != nil {
		return nil, err
	}
	resp, ok := val.(*periscopepb.ListOrchestratorInstancesResponse)
	if !ok {
		return nil, fmt.Errorf("unexpected response type for ListOrchestratorInstances: %T", val)
	}
	return resp.GetInstances(), nil
}

// DoListOrchestratorVantages returns public per-(gateway, instance)
// observations for the federation map's orchestrator layer.
func (r *Resolver) DoListOrchestratorVantages(ctx context.Context, orchAddr *string) ([]*periscopepb.OrchestratorVantage, error) {
	tenantIDs, err := r.networkOrchestratorOwnerTenants(ctx)
	if err != nil {
		return nil, err
	}
	cacheKey := ""
	if orchAddr != nil {
		cacheKey = *orchAddr
	}
	val, err := r.fetchPeriscope(ctx, "orchestrator_vantages", networkOrchestratorScopeKey(tenantIDs, cacheKey), func(ctx context.Context) (any, error) {
		var out []*periscopepb.OrchestratorVantage
		for _, tenantID := range tenantIDs {
			periscopeResp, callErr := r.Clients.Periscope.ListOrchestratorVantages(ctx, tenantID, orchAddr)
			if callErr != nil {
				return nil, callErr
			}
			out = append(out, periscopeResp.GetVantages()...)
		}
		return &periscopepb.ListOrchestratorVantagesResponse{Vantages: out}, nil
	})
	if err != nil {
		return nil, err
	}
	resp, ok := val.(*periscopepb.ListOrchestratorVantagesResponse)
	if !ok {
		return nil, fmt.Errorf("unexpected response type for ListOrchestratorVantages: %T", val)
	}
	return resp.GetVantages(), nil
}

// DoGetOrchestratorPerformanceSeries returns discovery, transcode, and AI
// performance points from Periscope's orchestrator rollups.
func (r *Resolver) DoGetOrchestratorPerformanceSeries(ctx context.Context, orchAddr string, timeRange model.TimeRangeInput, interval *string, gatewayID *string, resolvedIP *string) ([]*periscopepb.OrchestratorPerformancePoint, error) {
	tenantIDs, err := r.networkOrchestratorOwnerTenants(ctx)
	if err != nil {
		return nil, err
	}

	intervalVal := "5m"
	if interval != nil && *interval != "" {
		intervalVal = *interval
	}
	cacheParts := networkOrchestratorScopeKey(tenantIDs, orchAddr, intervalVal, timeKey(&timeRange.Start), timeKey(&timeRange.End))
	if gatewayID != nil {
		cacheParts = append(cacheParts, "gw="+*gatewayID)
	}
	if resolvedIP != nil {
		cacheParts = append(cacheParts, "ip="+*resolvedIP)
	}

	val, err := r.fetchPeriscope(ctx, "orchestrator_performance", cacheParts, func(ctx context.Context) (any, error) {
		var out []*periscopepb.OrchestratorPerformancePoint
		for _, tenantID := range tenantIDs {
			periscopeResp, callErr := r.Clients.Periscope.GetOrchestratorPerformanceSeries(
				ctx, tenantID, orchAddr,
				toTimeRangeOpts(&timeRange), interval, gatewayID, resolvedIP,
			)
			if callErr != nil {
				return nil, callErr
			}
			out = append(out, periscopeResp.GetPoints()...)
		}
		return &periscopepb.GetOrchestratorPerformanceSeriesResponse{Points: out}, nil
	})
	if err != nil {
		return nil, err
	}
	resp, ok := val.(*periscopepb.GetOrchestratorPerformanceSeriesResponse)
	if !ok {
		return nil, fmt.Errorf("unexpected response type for GetOrchestratorPerformanceSeries: %T", val)
	}
	return resp.GetPoints(), nil
}

// deref32 turns a nullable *int into the int32 the periscope client wants.
// Zero (or nil) means "use server default".
func deref32(p *int) int32 {
	if p == nil {
		return 0
	}
	return int32(*p)
}
