package resolvers

import (
	"context"
	"fmt"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/periscope"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
)

// DoListOrchestrators returns the cluster-owner-tenant's known orchestrators
// (identity-level rows). Per-instance config (price/capabilities/hardware)
// lives on `OrchestratorInstance` and is resolved separately so the federation
// map's per-instance side-panel breakdown can show divergence between
// instances of the same orch — see docs/architecture/orchestrator-visibility.md.
func (r *Resolver) DoListOrchestrators(ctx context.Context, first *int, after *string, orchAddr *string) (*model.OrchestratorsConnection, error) {
	if err := middleware.RequirePermission(ctx, "analytics:read"); err != nil {
		return nil, err
	}
	tenantID := tenantIDFromContext(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	cacheKey := tenantID
	if orchAddr != nil {
		cacheKey += ":" + *orchAddr
	}
	val, err := r.fetchPeriscope(ctx, "orchestrators_list", []string{cacheKey}, func(ctx context.Context) (any, error) {
		return r.Clients.Periscope.ListOrchestrators(ctx, tenantID, orchAddr, &periscope.CursorPaginationOpts{
			First: deref32(first),
			After: after,
		})
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

// DoGetOrchestrator returns one orchestrator's identity + every known
// instance and per-(gateway, instance) vantage row. Side-panel data source.
func (r *Resolver) DoGetOrchestrator(ctx context.Context, orchAddr string) (*model.OrchestratorWithDetails, error) {
	if err := middleware.RequirePermission(ctx, "analytics:read"); err != nil {
		return nil, err
	}
	tenantID := tenantIDFromContext(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	val, err := r.fetchPeriscope(ctx, "orchestrator_detail", []string{tenantID, orchAddr}, func(ctx context.Context) (any, error) {
		return r.Clients.Periscope.GetOrchestrator(ctx, tenantID, orchAddr)
	})
	if err != nil {
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

// DoListOrchestratorInstances returns per-instance rows for the tenant —
// each carrying its own price/capabilities/hardware.
func (r *Resolver) DoListOrchestratorInstances(ctx context.Context, orchAddr *string) ([]*pb.OrchestratorInstance, error) {
	if err := middleware.RequirePermission(ctx, "analytics:read"); err != nil {
		return nil, err
	}
	tenantID := tenantIDFromContext(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}
	cacheKey := tenantID
	if orchAddr != nil {
		cacheKey += ":" + *orchAddr
	}
	val, err := r.fetchPeriscope(ctx, "orchestrator_instances", []string{cacheKey}, func(ctx context.Context) (any, error) {
		return r.Clients.Periscope.ListOrchestratorInstances(ctx, tenantID, orchAddr)
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

// DoListOrchestratorVantages returns per-(gateway, instance) observations.
// Federation map's primary data source for the orch layer — multi-IP /
// multi-region rows are intentional.
func (r *Resolver) DoListOrchestratorVantages(ctx context.Context, orchAddr *string) ([]*pb.OrchestratorVantage, error) {
	if err := middleware.RequirePermission(ctx, "analytics:read"); err != nil {
		return nil, err
	}
	tenantID := tenantIDFromContext(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}
	cacheKey := tenantID
	if orchAddr != nil {
		cacheKey += ":" + *orchAddr
	}
	val, err := r.fetchPeriscope(ctx, "orchestrator_vantages", []string{cacheKey}, func(ctx context.Context) (any, error) {
		return r.Clients.Periscope.ListOrchestratorVantages(ctx, tenantID, orchAddr)
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
	if err := middleware.RequirePermission(ctx, "analytics:read"); err != nil {
		return nil, err
	}
	tenantID := tenantIDFromContext(ctx)
	if tenantID == "" {
		return nil, fmt.Errorf("tenant context required")
	}

	intervalVal := "5m"
	if interval != nil && *interval != "" {
		intervalVal = *interval
	}
	cacheParts := []string{tenantID, orchAddr, intervalVal, timeKey(&timeRange.Start), timeKey(&timeRange.End)}
	if gatewayID != nil {
		cacheParts = append(cacheParts, "gw="+*gatewayID)
	}
	if resolvedIP != nil {
		cacheParts = append(cacheParts, "ip="+*resolvedIP)
	}

	val, err := r.fetchPeriscope(ctx, "orchestrator_performance", cacheParts, func(ctx context.Context) (any, error) {
		return r.Clients.Periscope.GetOrchestratorPerformanceSeries(
			ctx, tenantID, orchAddr,
			toTimeRangeOpts(&timeRange), interval, gatewayID, resolvedIP,
		)
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
