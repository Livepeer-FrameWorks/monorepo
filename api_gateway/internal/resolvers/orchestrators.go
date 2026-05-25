package resolvers

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"frameworks/api_gateway/graph/model"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/periscope"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func isLivepeerGatewayService(serviceID string) bool {
	serviceID = strings.TrimSpace(serviceID)
	return serviceID == "livepeer-gateway" || strings.HasPrefix(serviceID, "livepeer-gateway-")
}

// networkOrchestratorOwnerTenants derives public orchestrator scope from
// platform-official gateway clusters.
func (r *Resolver) networkOrchestratorOwnerTenants(ctx context.Context) ([]string, error) {
	val, err := r.fetchPeriscope(ctx, "network_orchestrator_owner_tenants", []string{"current"}, func(ctx context.Context) (any, error) {
		clustersResp, err := r.Clients.Quartermaster.ListOfficialClusters(ctx)
		if err != nil {
			return nil, err
		}
		instancesResp, err := r.Clients.Quartermaster.ListServiceInstances(ctx, "", "", "", &pb.CursorPaginationRequest{First: 2000})
		if err != nil {
			return nil, err
		}
		clusterByID := make(map[string]*pb.InfrastructureCluster)
		for _, cluster := range clustersResp.GetClusters() {
			if cluster != nil && cluster.GetIsActive() {
				clusterByID[cluster.GetClusterId()] = cluster
			}
		}
		seen := make(map[string]struct{})
		for _, instance := range instancesResp.GetInstances() {
			if instance == nil || !isLivepeerGatewayService(instance.GetServiceId()) {
				continue
			}
			cluster := clusterByID[instance.GetClusterId()]
			if cluster == nil {
				continue
			}
			ownerTenantID := strings.TrimSpace(cluster.GetOwnerTenantId())
			if ownerTenantID == "" {
				r.Logger.WithField("cluster_id", cluster.GetClusterId()).Warn("livepeer gateway cluster missing owner_tenant_id")
				continue
			}
			seen[ownerTenantID] = struct{}{}
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
		var out []*pb.Orchestrator
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
		return &pb.ListOrchestratorsResponse{Orchestrators: out}, nil
	})
	if err != nil {
		return nil, err
	}
	resp, ok := val.(*pb.ListOrchestratorsResponse)
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
	resp, ok := val.(*pb.GetOrchestratorResponse)
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
func (r *Resolver) DoListOrchestratorInstances(ctx context.Context, orchAddr *string) ([]*pb.OrchestratorInstance, error) {
	tenantIDs, err := r.networkOrchestratorOwnerTenants(ctx)
	if err != nil {
		return nil, err
	}
	cacheKey := ""
	if orchAddr != nil {
		cacheKey = *orchAddr
	}
	val, err := r.fetchPeriscope(ctx, "orchestrator_instances", networkOrchestratorScopeKey(tenantIDs, cacheKey), func(ctx context.Context) (any, error) {
		var out []*pb.OrchestratorInstance
		for _, tenantID := range tenantIDs {
			periscopeResp, callErr := r.Clients.Periscope.ListOrchestratorInstances(ctx, tenantID, orchAddr)
			if callErr != nil {
				return nil, callErr
			}
			out = append(out, periscopeResp.GetInstances()...)
		}
		return &pb.ListOrchestratorInstancesResponse{Instances: out}, nil
	})
	if err != nil {
		return nil, err
	}
	resp, ok := val.(*pb.ListOrchestratorInstancesResponse)
	if !ok {
		return nil, fmt.Errorf("unexpected response type for ListOrchestratorInstances: %T", val)
	}
	return resp.GetInstances(), nil
}

// DoListOrchestratorVantages returns public per-(gateway, instance)
// observations for the federation map's orchestrator layer.
func (r *Resolver) DoListOrchestratorVantages(ctx context.Context, orchAddr *string) ([]*pb.OrchestratorVantage, error) {
	tenantIDs, err := r.networkOrchestratorOwnerTenants(ctx)
	if err != nil {
		return nil, err
	}
	cacheKey := ""
	if orchAddr != nil {
		cacheKey = *orchAddr
	}
	val, err := r.fetchPeriscope(ctx, "orchestrator_vantages", networkOrchestratorScopeKey(tenantIDs, cacheKey), func(ctx context.Context) (any, error) {
		var out []*pb.OrchestratorVantage
		for _, tenantID := range tenantIDs {
			periscopeResp, callErr := r.Clients.Periscope.ListOrchestratorVantages(ctx, tenantID, orchAddr)
			if callErr != nil {
				return nil, callErr
			}
			out = append(out, periscopeResp.GetVantages()...)
		}
		return &pb.ListOrchestratorVantagesResponse{Vantages: out}, nil
	})
	if err != nil {
		return nil, err
	}
	resp, ok := val.(*pb.ListOrchestratorVantagesResponse)
	if !ok {
		return nil, fmt.Errorf("unexpected response type for ListOrchestratorVantages: %T", val)
	}
	return resp.GetVantages(), nil
}

// DoGetOrchestratorPerformanceSeries returns discovery, transcode, and AI
// performance points from Periscope's orchestrator rollups.
func (r *Resolver) DoGetOrchestratorPerformanceSeries(ctx context.Context, orchAddr string, timeRange model.TimeRangeInput, interval *string, gatewayID *string, resolvedIP *string) ([]*pb.OrchestratorPerformancePoint, error) {
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
		var out []*pb.OrchestratorPerformancePoint
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
		return &pb.GetOrchestratorPerformanceSeriesResponse{Points: out}, nil
	})
	if err != nil {
		return nil, err
	}
	resp, ok := val.(*pb.GetOrchestratorPerformanceSeriesResponse)
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
