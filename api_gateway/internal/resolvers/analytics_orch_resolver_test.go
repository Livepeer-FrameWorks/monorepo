package resolvers

import (
	"context"
	"errors"
	"testing"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/clients/clientstest"

	periscope "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/periscope"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/globalid"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

// periO is the analytics/orchestrator-resolver seam: a resolver wired only to a
// FakePeriscope. Anything else stays nil and panics if a resolver reaches for it.
func periO(p *clientstest.FakePeriscope) *Resolver {
	return &Resolver{Clients: clientstest.Clients(clientstest.WithPeriscope(p)), Logger: clientstest.DiscardLogger()}
}

// --- DoGetCurrentStreamHealth (analytics.go) ---
// Delegates to DoGetStreamHealthMetrics with a synthetic 5-minute range and
// returns the most-recent (last) metric.

func TestDoGetCurrentStreamHealth_ReturnsMostRecentMetric(t *testing.T) {
	var gotStream *string
	p := &clientstest.FakePeriscope{
		GetStreamHealthMetricsFn: func(_ context.Context, _ string, streamID *string, _ *periscope.TimeRangeOpts, _ *periscope.CursorPaginationOpts) (*periscopepb.GetStreamHealthMetricsResponse, error) {
			gotStream = streamID
			return &periscopepb.GetStreamHealthMetricsResponse{
				Metrics: []*periscopepb.StreamHealthMetric{{Id: "old"}, {Id: "newest"}},
			}, nil
		},
	}
	relayID := globalid.Encode(globalid.TypeStream, "raw-cur")
	got, err := periO(p).DoGetCurrentStreamHealth(clientstest.AuthedCtx("t1"), relayID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotStream == nil || *gotStream != "raw-cur" {
		t.Fatalf("stream not normalized into ptr: %v", gotStream)
	}
	// Most recent == last element of the metrics slice.
	if got == nil || got.Id != "newest" {
		t.Fatalf("expected most-recent metric, got %+v", got)
	}
}

func TestDoGetCurrentStreamHealth_NoMetricsReturnsNil(t *testing.T) {
	p := &clientstest.FakePeriscope{
		GetStreamHealthMetricsFn: func(context.Context, string, *string, *periscope.TimeRangeOpts, *periscope.CursorPaginationOpts) (*periscopepb.GetStreamHealthMetricsResponse, error) {
			return &periscopepb.GetStreamHealthMetricsResponse{}, nil
		},
	}
	got, err := periO(p).DoGetCurrentStreamHealth(clientstest.AuthedCtx("t1"), "s1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Empty metrics → nil, never an index-out-of-range panic.
	if got != nil {
		t.Fatalf("expected nil for empty metrics, got %+v", got)
	}
}

func TestDoGetCurrentStreamHealth_BackendErrorPropagates(t *testing.T) {
	p := &clientstest.FakePeriscope{
		GetStreamHealthMetricsFn: func(context.Context, string, *string, *periscope.TimeRangeOpts, *periscope.CursorPaginationOpts) (*periscopepb.GetStreamHealthMetricsResponse, error) {
			return nil, errors.New("boom")
		},
	}
	if _, err := periO(p).DoGetCurrentStreamHealth(clientstest.AuthedCtx("t1"), "s1"); err == nil {
		t.Fatal("expected error propagation")
	}
}

func TestDoGetCurrentStreamHealth_TenantGuard(t *testing.T) {
	p := &clientstest.FakePeriscope{}
	if _, err := periO(p).DoGetCurrentStreamHealth(authedNoTenantCtx(), "s1"); err == nil {
		t.Fatal("expected tenant required error")
	}
	if p.Calls != 0 {
		t.Fatalf("backend hit despite missing tenant: Calls=%d", p.Calls)
	}
}

// --- DoGetViewerGeographics (analytics.go) ---

func TestDoGetViewerGeographics_NormalizesStreamAndFiltersByStream(t *testing.T) {
	var gotStream *string
	var gotTR *periscope.TimeRangeOpts
	p := &clientstest.FakePeriscope{
		GetConnectionEventsFn: func(_ context.Context, _ string, streamID *string, tr *periscope.TimeRangeOpts, _ *periscope.CursorPaginationOpts) (*periscopepb.GetConnectionEventsResponse, error) {
			gotStream, gotTR = streamID, tr
			return &periscopepb.GetConnectionEventsResponse{
				Events: []*periscopepb.ConnectionEvent{
					{EventId: "match", StreamId: "raw-geo"},
					{EventId: "other", StreamId: "different"}, // filtered out
				},
			}, nil
		},
	}
	relayID := globalid.Encode(globalid.TypeStream, "raw-geo")
	tr := testRange()
	got, err := periO(p).DoGetViewerGeographics(clientstest.AuthedCtx("t1"), &relayID, tr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotStream == nil || *gotStream != "raw-geo" {
		t.Fatalf("stream not normalized: %v", gotStream)
	}
	if gotTR == nil || !gotTR.StartTime.Equal(tr.Start) || !gotTR.EndTime.Equal(tr.End) {
		t.Fatalf("time range not converted: %+v", gotTR)
	}
	// Events whose StreamId mismatches the requested stream are dropped.
	if len(got) != 1 || got[0].EventId != "match" {
		t.Fatalf("stream filter not applied: %+v", got)
	}
}

func TestDoGetViewerGeographics_NoStreamReturnsAllEvents(t *testing.T) {
	p := &clientstest.FakePeriscope{
		GetConnectionEventsFn: func(_ context.Context, _ string, streamID *string, _ *periscope.TimeRangeOpts, _ *periscope.CursorPaginationOpts) (*periscopepb.GetConnectionEventsResponse, error) {
			if streamID != nil {
				t.Fatalf("expected nil stream filter, got %v", *streamID)
			}
			return &periscopepb.GetConnectionEventsResponse{
				Events: []*periscopepb.ConnectionEvent{{EventId: "a", StreamId: "s-a"}, {EventId: "b", StreamId: "s-b"}},
			}, nil
		},
	}
	got, err := periO(p).DoGetViewerGeographics(clientstest.AuthedCtx("t1"), nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// No stream filter → every event is returned.
	if len(got) != 2 {
		t.Fatalf("expected all events with no stream filter, got %+v", got)
	}
}

func TestDoGetViewerGeographics_BackendErrorPropagates(t *testing.T) {
	p := &clientstest.FakePeriscope{
		GetConnectionEventsFn: func(context.Context, string, *string, *periscope.TimeRangeOpts, *periscope.CursorPaginationOpts) (*periscopepb.GetConnectionEventsResponse, error) {
			return nil, errors.New("boom")
		},
	}
	if _, err := periO(p).DoGetViewerGeographics(clientstest.AuthedCtx("t1"), nil, nil); err == nil {
		t.Fatal("expected error propagation")
	}
}

func TestDoGetViewerGeographics_TenantGuard(t *testing.T) {
	p := &clientstest.FakePeriscope{}
	if _, err := periO(p).DoGetViewerGeographics(authedNoTenantCtx(), nil, nil); err == nil {
		t.Fatal("expected tenant required error")
	}
	if p.Calls != 0 {
		t.Fatalf("backend hit despite missing tenant: Calls=%d", p.Calls)
	}
}

// --- DoGetTenantDailyStats (analytics.go) ---

func TestDoGetTenantDailyStats_DefaultsDaysAndReturnsStats(t *testing.T) {
	var gotTenant string
	var gotDays int32
	p := &clientstest.FakePeriscope{
		GetTenantDailyStatsFn: func(_ context.Context, tenantID string, days int32) (*periscopepb.GetTenantDailyStatsResponse, error) {
			gotTenant, gotDays = tenantID, days
			return &periscopepb.GetTenantDailyStatsResponse{
				Stats: []*periscopepb.TenantDailyStat{{Id: "d1", TotalViews: 5}},
			}, nil
		},
	}
	got, err := periO(p).DoGetTenantDailyStats(clientstest.AuthedCtx("t1"), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotTenant != "t1" {
		t.Fatalf("tenant = %q want t1", gotTenant)
	}
	// nil days → server default of 7.
	if gotDays != 7 {
		t.Fatalf("default days not 7: %d", gotDays)
	}
	if len(got) != 1 || got[0].Id != "d1" || got[0].TotalViews != 5 {
		t.Fatalf("stats not returned: %+v", got)
	}
}

func TestDoGetTenantDailyStats_ExplicitDaysForwarded(t *testing.T) {
	var gotDays int32
	p := &clientstest.FakePeriscope{
		GetTenantDailyStatsFn: func(_ context.Context, _ string, days int32) (*periscopepb.GetTenantDailyStatsResponse, error) {
			gotDays = days
			return &periscopepb.GetTenantDailyStatsResponse{}, nil
		},
	}
	d := 30
	if _, err := periO(p).DoGetTenantDailyStats(clientstest.AuthedCtx("t1"), &d); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotDays != 30 {
		t.Fatalf("explicit days not forwarded: %d", gotDays)
	}
}

func TestDoGetTenantDailyStats_BackendErrorPropagates(t *testing.T) {
	p := &clientstest.FakePeriscope{
		GetTenantDailyStatsFn: func(context.Context, string, int32) (*periscopepb.GetTenantDailyStatsResponse, error) {
			return nil, errors.New("boom")
		},
	}
	if _, err := periO(p).DoGetTenantDailyStats(clientstest.AuthedCtx("t1"), nil); err == nil {
		t.Fatal("expected error propagation")
	}
}

func TestDoGetTenantDailyStats_TenantGuard(t *testing.T) {
	p := &clientstest.FakePeriscope{}
	if _, err := periO(p).DoGetTenantDailyStats(authedNoTenantCtx(), nil); err == nil {
		t.Fatal("expected tenant required error")
	}
	if p.Calls != 0 {
		t.Fatalf("backend hit despite missing tenant: Calls=%d", p.Calls)
	}
}

// --- orchestrators.go scaffolding ---
//
// Every orchestrator root resolver funnels through networkOrchestratorOwnerTenants,
// which derives owner-tenant scope from Quartermaster topology (ListPublicTopologyClusters)
// BEFORE touching Periscope.

// orchScopeQM returns a FakeQuartermaster whose topology resolves to a single
// public livepeer-gateway cluster owned by ownerTenant, so the orchestrator
// owner-tenant scope is exactly [ownerTenant].
func orchScopeQM(ownerTenant string) *clientstest.FakeQuartermaster {
	cluster := &quartermasterpb.InfrastructureCluster{
		ClusterId:     "cl-1",
		OwnerTenantId: &ownerTenant,
		IsActive:      true,
	}
	return &clientstest.FakeQuartermaster{
		ListPublicTopologyClustersFn: func(context.Context) (*quartermasterpb.ListClustersResponse, error) {
			return &quartermasterpb.ListClustersResponse{Clusters: []*quartermasterpb.InfrastructureCluster{cluster}}, nil
		},
		ListServiceInstancesFn: func(context.Context, string, string, string, *commonpb.CursorPaginationRequest) (*quartermasterpb.ListServiceInstancesResponse, error) {
			return &quartermasterpb.ListServiceInstancesResponse{
				Instances: []*quartermasterpb.ServiceInstance{{ServiceId: "livepeer-gateway"}},
			}, nil
		},
		// Signed-in tenant scope augmentation; empty so scope stays public-only.
		ListMySubscriptionsFn: func(context.Context, *quartermasterpb.ListMySubscriptionsRequest) (*quartermasterpb.ListClustersResponse, error) {
			return &quartermasterpb.ListClustersResponse{}, nil
		},
		ListClustersByOwnerFn: func(context.Context, string, *commonpb.CursorPaginationRequest) (*quartermasterpb.ListClustersResponse, error) {
			return &quartermasterpb.ListClustersResponse{}, nil
		},
	}
}

// orchO wires both a FakePeriscope (orchestrator data) and the topology-scope
// FakeQuartermaster into a resolver.
func orchO(p *clientstest.FakePeriscope, q *clientstest.FakeQuartermaster) *Resolver {
	return &Resolver{
		Clients: clientstest.Clients(clientstest.WithPeriscope(p), clientstest.WithQuartermaster(q)),
		Logger:  clientstest.DiscardLogger(),
	}
}

// orchCtx is jwt-authed with a tenant so networkOrchestratorOwnerTenants takes
// the tenant-scoped branch (and is not demo mode, which fails orchestrators closed).
func orchCtx() context.Context {
	return clientstest.AuthedCtx("t1")
}

// --- DoListOrchestrators (orchestrators.go) ---

func TestDoListOrchestrators_ScopesToOwnerTenantAndCounts(t *testing.T) {
	var gotTenant string
	var gotOrchAddr *string
	var gotFirst int32
	p := &clientstest.FakePeriscope{
		ListOrchestratorsFn: func(_ context.Context, tenantID string, orchAddr *string, opts *periscope.CursorPaginationOpts) (*periscopepb.ListOrchestratorsResponse, error) {
			gotTenant, gotOrchAddr = tenantID, orchAddr
			if opts != nil {
				gotFirst = opts.First
			}
			return &periscopepb.ListOrchestratorsResponse{
				Orchestrators: []*periscopepb.Orchestrator{{OrchAddr: "0xabc"}, {OrchAddr: "0xdef"}},
			}, nil
		},
	}
	addr := "0xabc"
	first := 50
	got, err := orchO(p, orchScopeQM("owner-t")).DoListOrchestrators(orchCtx(), &first, nil, &addr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Periscope is queried under the cluster's owner tenant, not the caller's tenant.
	if gotTenant != "owner-t" {
		t.Fatalf("not scoped to owner tenant: got %q", gotTenant)
	}
	if gotOrchAddr == nil || *gotOrchAddr != "0xabc" {
		t.Fatalf("orchAddr filter not forwarded: %v", gotOrchAddr)
	}
	if gotFirst != 50 {
		t.Fatalf("first not forwarded as int32: %d", gotFirst)
	}
	if len(got.Nodes) != 2 || got.TotalCount != 2 {
		t.Fatalf("nodes/count not mapped: %+v", got)
	}
}

func TestDoListOrchestrators_BackendErrorPropagates(t *testing.T) {
	p := &clientstest.FakePeriscope{
		ListOrchestratorsFn: func(context.Context, string, *string, *periscope.CursorPaginationOpts) (*periscopepb.ListOrchestratorsResponse, error) {
			return nil, errors.New("boom")
		},
	}
	if _, err := orchO(p, orchScopeQM("owner-t")).DoListOrchestrators(orchCtx(), nil, nil, nil); err == nil {
		t.Fatal("expected error propagation")
	}
}

func TestDoListOrchestrators_DemoFailsClosed(t *testing.T) {
	p := &clientstest.FakePeriscope{}
	// Orchestrator data has no demo representation; the scope helper rejects demo.
	ctx := context.WithValue(context.Background(), ctxkeys.KeyDemoMode, true)
	if _, err := orchO(p, orchScopeQM("owner-t")).DoListOrchestrators(ctx, nil, nil, nil); err == nil {
		t.Fatal("expected demo-unavailable error")
	}
	if p.Calls != 0 {
		t.Fatalf("Periscope hit in demo mode: Calls=%d", p.Calls)
	}
}

// --- DoGetOrchestrator (orchestrators.go) ---

func TestDoGetOrchestrator_MapsDetailModel(t *testing.T) {
	var gotTenant, gotAddr string
	p := &clientstest.FakePeriscope{
		GetOrchestratorFn: func(_ context.Context, tenantID, orchAddr string) (*periscopepb.GetOrchestratorResponse, error) {
			gotTenant, gotAddr = tenantID, orchAddr
			return &periscopepb.GetOrchestratorResponse{
				Orchestrator: &periscopepb.Orchestrator{OrchAddr: orchAddr},
				Instances:    []*periscopepb.OrchestratorInstance{{ResolvedIp: "1.2.3.4"}},
				Vantages:     []*periscopepb.OrchestratorVantage{{GatewayId: "gw-1"}},
			}, nil
		},
	}
	got, err := orchO(p, orchScopeQM("owner-t")).DoGetOrchestrator(orchCtx(), "0xabc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotTenant != "owner-t" || gotAddr != "0xabc" {
		t.Fatalf("scope/addr not forwarded: tenant=%q addr=%q", gotTenant, gotAddr)
	}
	if got == nil || got.Orchestrator.GetOrchAddr() != "0xabc" {
		t.Fatalf("orchestrator not mapped: %+v", got)
	}
	if len(got.Instances) != 1 || got.Instances[0].GetResolvedIp() != "1.2.3.4" {
		t.Fatalf("instances not mapped: %+v", got.Instances)
	}
	if len(got.Vantages) != 1 || got.Vantages[0].GetGatewayId() != "gw-1" {
		t.Fatalf("vantages not mapped: %+v", got.Vantages)
	}
}

func TestDoGetOrchestrator_NilOrchestratorReturnsNil(t *testing.T) {
	p := &clientstest.FakePeriscope{
		GetOrchestratorFn: func(context.Context, string, string) (*periscopepb.GetOrchestratorResponse, error) {
			return &periscopepb.GetOrchestratorResponse{}, nil // present resp, absent orchestrator
		},
	}
	got, err := orchO(p, orchScopeQM("owner-t")).DoGetOrchestrator(orchCtx(), "0xabc")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Absent orchestrator → nil detail, never a nil-deref.
	if got != nil {
		t.Fatalf("expected nil for absent orchestrator, got %+v", got)
	}
}

// --- DoListOrchestratorInstances (orchestrators.go) ---

func TestDoListOrchestratorInstances_ScopesAndReturns(t *testing.T) {
	var gotTenant string
	var gotAddr *string
	p := &clientstest.FakePeriscope{
		ListOrchestratorInstancesFn: func(_ context.Context, tenantID string, orchAddr *string) (*periscopepb.ListOrchestratorInstancesResponse, error) {
			gotTenant, gotAddr = tenantID, orchAddr
			return &periscopepb.ListOrchestratorInstancesResponse{
				Instances: []*periscopepb.OrchestratorInstance{{ResolvedIp: "9.9.9.9"}},
			}, nil
		},
	}
	addr := "0xabc"
	got, err := orchO(p, orchScopeQM("owner-t")).DoListOrchestratorInstances(orchCtx(), &addr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotTenant != "owner-t" {
		t.Fatalf("not scoped to owner tenant: %q", gotTenant)
	}
	if gotAddr == nil || *gotAddr != "0xabc" {
		t.Fatalf("orchAddr not forwarded: %v", gotAddr)
	}
	if len(got) != 1 || got[0].GetResolvedIp() != "9.9.9.9" {
		t.Fatalf("instances not returned: %+v", got)
	}
}

func TestDoListOrchestratorInstances_BackendErrorPropagates(t *testing.T) {
	p := &clientstest.FakePeriscope{
		ListOrchestratorInstancesFn: func(context.Context, string, *string) (*periscopepb.ListOrchestratorInstancesResponse, error) {
			return nil, errors.New("boom")
		},
	}
	if _, err := orchO(p, orchScopeQM("owner-t")).DoListOrchestratorInstances(orchCtx(), nil); err == nil {
		t.Fatal("expected error propagation")
	}
}

// --- DoListOrchestratorVantages (orchestrators.go) ---

func TestDoListOrchestratorVantages_ScopesAndReturns(t *testing.T) {
	var gotTenant string
	p := &clientstest.FakePeriscope{
		ListOrchestratorVantagesFn: func(_ context.Context, tenantID string, _ *string) (*periscopepb.ListOrchestratorVantagesResponse, error) {
			gotTenant = tenantID
			return &periscopepb.ListOrchestratorVantagesResponse{
				Vantages: []*periscopepb.OrchestratorVantage{{GatewayId: "gw-x"}},
			}, nil
		},
	}
	got, err := orchO(p, orchScopeQM("owner-t")).DoListOrchestratorVantages(orchCtx(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotTenant != "owner-t" {
		t.Fatalf("not scoped to owner tenant: %q", gotTenant)
	}
	if len(got) != 1 || got[0].GetGatewayId() != "gw-x" {
		t.Fatalf("vantages not returned: %+v", got)
	}
}

func TestDoListOrchestratorVantages_BackendErrorPropagates(t *testing.T) {
	p := &clientstest.FakePeriscope{
		ListOrchestratorVantagesFn: func(context.Context, string, *string) (*periscopepb.ListOrchestratorVantagesResponse, error) {
			return nil, errors.New("boom")
		},
	}
	if _, err := orchO(p, orchScopeQM("owner-t")).DoListOrchestratorVantages(orchCtx(), nil); err == nil {
		t.Fatal("expected error propagation")
	}
}

// --- DoGetOrchestratorPerformanceSeries (orchestrators.go) ---

func TestDoGetOrchestratorPerformanceSeries_ForwardsRangeIntervalAndFilters(t *testing.T) {
	var gotTenant, gotAddr string
	var gotTR *periscope.TimeRangeOpts
	var gotInterval, gotGateway, gotIP *string
	p := &clientstest.FakePeriscope{
		GetOrchestratorPerformanceSeriesFn: func(_ context.Context, tenantID, orchAddr string, tr *periscope.TimeRangeOpts, interval, gatewayID, resolvedIP *string) (*periscopepb.GetOrchestratorPerformanceSeriesResponse, error) {
			gotTenant, gotAddr, gotTR = tenantID, orchAddr, tr
			gotInterval, gotGateway, gotIP = interval, gatewayID, resolvedIP
			return &periscopepb.GetOrchestratorPerformanceSeriesResponse{
				Points: []*periscopepb.OrchestratorPerformancePoint{{GatewayId: "gw-7", Attempts: 11}},
			}, nil
		},
	}
	tr := *testRange()
	iv := "15m"
	gw := "gw-7"
	ip := "5.6.7.8"
	got, err := orchO(p, orchScopeQM("owner-t")).DoGetOrchestratorPerformanceSeries(orchCtx(), "0xabc", tr, &iv, &gw, &ip)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotTenant != "owner-t" || gotAddr != "0xabc" {
		t.Fatalf("scope/addr not forwarded: tenant=%q addr=%q", gotTenant, gotAddr)
	}
	if gotTR == nil || !gotTR.StartTime.Equal(tr.Start) || !gotTR.EndTime.Equal(tr.End) {
		t.Fatalf("time range not converted: %+v", gotTR)
	}
	// The raw interval pointer is forwarded to the client (cache key uses a default).
	if gotInterval == nil || *gotInterval != "15m" {
		t.Fatalf("interval not forwarded: %v", gotInterval)
	}
	if gotGateway == nil || *gotGateway != "gw-7" || gotIP == nil || *gotIP != "5.6.7.8" {
		t.Fatalf("gateway/ip filters not forwarded: gw=%v ip=%v", gotGateway, gotIP)
	}
	if len(got) != 1 || got[0].GetGatewayId() != "gw-7" || got[0].GetAttempts() != 11 {
		t.Fatalf("points not returned: %+v", got)
	}
}

func TestDoGetOrchestratorPerformanceSeries_BackendErrorPropagates(t *testing.T) {
	p := &clientstest.FakePeriscope{
		GetOrchestratorPerformanceSeriesFn: func(context.Context, string, string, *periscope.TimeRangeOpts, *string, *string, *string) (*periscopepb.GetOrchestratorPerformanceSeriesResponse, error) {
			return nil, errors.New("boom")
		},
	}
	if _, err := orchO(p, orchScopeQM("owner-t")).DoGetOrchestratorPerformanceSeries(orchCtx(), "0xabc", *testRange(), nil, nil, nil); err == nil {
		t.Fatal("expected error propagation")
	}
}

// compile-time guard: model wrappers carry the proto rows verbatim.
var (
	_ = model.OrchestratorsConnection{}
	_ = model.OrchestratorWithDetails{}
)
