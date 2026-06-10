package resolvers

import (
	"context"
	"errors"
	"testing"
	"time"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/clients/clientstest"

	periscope "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/periscope"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/globalid"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
)

// periR builds a resolver wired only to a FakePeriscope. Any other client stays
// nil and panics if a resolver unexpectedly reaches for it, which proves the
// Periscope-only path was exercised.
func periR(p *clientstest.FakePeriscope) *Resolver {
	return &Resolver{Clients: clientstest.Clients(clientstest.WithPeriscope(p)), Logger: clientstest.DiscardLogger()}
}

// authedNoTenantCtx is jwt-authenticated (passes RequirePermission) but carries
// no tenant ID, so it reaches the resolver's own "tenant required" guard rather
// than being rejected earlier by the permission gate.
func authedNoTenantCtx() context.Context {
	return context.WithValue(context.Background(), ctxkeys.KeyAuthType, "jwt")
}

func testRange() *model.TimeRangeInput {
	return &model.TimeRangeInput{
		Start: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		End:   time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC),
	}
}

// --- DoGetStreamAnalyticsSummary ---

func TestDoGetStreamAnalyticsSummary_NormalizesRelayIDAndConvertsRange(t *testing.T) {
	var gotStream string
	var gotTenant string
	var gotTR *periscope.TimeRangeOpts
	p := &clientstest.FakePeriscope{
		GetStreamAnalyticsSummaryFn: func(_ context.Context, tenantID, streamID string, tr *periscope.TimeRangeOpts) (*periscopepb.GetStreamAnalyticsSummaryResponse, error) {
			gotTenant, gotStream, gotTR = tenantID, streamID, tr
			return &periscopepb.GetStreamAnalyticsSummaryResponse{
				Summary: &periscopepb.StreamAnalyticsSummary{StreamId: streamID, RangeTotalViews: 42},
			}, nil
		},
	}
	tr := testRange()
	// Relay global ID must be decoded to the raw stream ID before reaching Periscope.
	relayID := globalid.Encode(globalid.TypeStream, "raw-stream-1")
	got, err := periR(p).DoGetStreamAnalyticsSummary(clientstest.AuthedCtx("t1"), relayID, tr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotStream != "raw-stream-1" {
		t.Fatalf("stream not normalized: got %q want raw-stream-1", gotStream)
	}
	if gotTenant != "t1" {
		t.Fatalf("tenant = %q want t1", gotTenant)
	}
	if gotTR == nil || !gotTR.StartTime.Equal(tr.Start) || !gotTR.EndTime.Equal(tr.End) {
		t.Fatalf("time range not converted: %+v", gotTR)
	}
	if got == nil || got.RangeTotalViews != 42 {
		t.Fatalf("summary not returned: %+v", got)
	}
}

func TestDoGetStreamAnalyticsSummary_BackendError(t *testing.T) {
	p := &clientstest.FakePeriscope{
		GetStreamAnalyticsSummaryFn: func(context.Context, string, string, *periscope.TimeRangeOpts) (*periscopepb.GetStreamAnalyticsSummaryResponse, error) {
			return nil, errors.New("boom")
		},
	}
	if _, err := periR(p).DoGetStreamAnalyticsSummary(clientstest.AuthedCtx("t1"), "s1", nil); err == nil {
		t.Fatal("expected error propagation")
	}
}

func TestDoGetStreamAnalyticsSummary_TenantGuard(t *testing.T) {
	p := &clientstest.FakePeriscope{}
	if _, err := periR(p).DoGetStreamAnalyticsSummary(authedNoTenantCtx(), "s1", nil); err == nil {
		t.Fatal("expected tenant required error")
	}
	if p.Calls != 0 {
		t.Fatalf("backend hit despite missing tenant: Calls=%d", p.Calls)
	}
}

// --- DoGetPlatformOverview ---

func TestDoGetPlatformOverview_SanitizesNonFiniteFloats(t *testing.T) {
	var gotTenant string
	var gotTR *periscope.TimeRangeOpts
	p := &clientstest.FakePeriscope{
		GetPlatformOverviewFn: func(_ context.Context, tenantID string, tr *periscope.TimeRangeOpts) (*periscopepb.GetPlatformOverviewResponse, error) {
			gotTenant, gotTR = tenantID, tr
			// Inf/NaN must be coerced to 0 for GraphQL float safety.
			return &periscopepb.GetPlatformOverviewResponse{
				AverageViewers: 5,
				PeakBandwidth:  inf(),
				StreamHours:    nan(),
				TotalStreams:   7,
			}, nil
		},
	}
	tr := testRange()
	got, err := periR(p).DoGetPlatformOverview(clientstest.AuthedCtx("t9"), tr)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotTenant != "t9" {
		t.Fatalf("tenant = %q", gotTenant)
	}
	if gotTR == nil || !gotTR.StartTime.Equal(tr.Start) {
		t.Fatalf("time range not converted: %+v", gotTR)
	}
	if got.AverageViewers != 5 || got.PeakBandwidth != 0 || got.StreamHours != 0 {
		t.Fatalf("non-finite floats not sanitized: %+v", got)
	}
	if got.TotalStreams != 7 {
		t.Fatalf("counts mangled: %+v", got)
	}
}

func TestDoGetPlatformOverview_TenantGuard(t *testing.T) {
	p := &clientstest.FakePeriscope{}
	if _, err := periR(p).DoGetPlatformOverview(authedNoTenantCtx(), nil); err == nil {
		t.Fatal("expected tenant required error")
	}
	if p.Calls != 0 {
		t.Fatalf("backend hit despite missing tenant: Calls=%d", p.Calls)
	}
}

// --- DoGetViewerCountTimeSeries ---

func TestDoGetViewerCountTimeSeries_DefaultIntervalAndNormalization(t *testing.T) {
	var gotInterval string
	var gotStream *string
	p := &clientstest.FakePeriscope{
		GetViewerCountTimeSeriesFn: func(_ context.Context, _ string, streamID *string, _ *periscope.TimeRangeOpts, interval string) (*periscopepb.GetViewerCountTimeSeriesResponse, error) {
			gotInterval, gotStream = interval, streamID
			return &periscopepb.GetViewerCountTimeSeriesResponse{
				Buckets: []*periscopepb.ViewerCountBucket{{ViewerCount: 3}},
			}, nil
		},
	}
	relayID := globalid.Encode(globalid.TypeStream, "raw-s")
	got, err := periR(p).DoGetViewerCountTimeSeries(clientstest.AuthedCtx("t1"), &relayID, testRange(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotInterval != "5m" {
		t.Fatalf("default interval not 5m: %q", gotInterval)
	}
	if gotStream == nil || *gotStream != "raw-s" {
		t.Fatalf("stream not normalized: %v", gotStream)
	}
	if len(got) != 1 || got[0].ViewerCount != 3 {
		t.Fatalf("buckets not returned: %+v", got)
	}
}

func TestDoGetViewerCountTimeSeries_ExplicitInterval(t *testing.T) {
	var gotInterval string
	p := &clientstest.FakePeriscope{
		GetViewerCountTimeSeriesFn: func(_ context.Context, _ string, _ *string, _ *periscope.TimeRangeOpts, interval string) (*periscopepb.GetViewerCountTimeSeriesResponse, error) {
			gotInterval = interval
			return &periscopepb.GetViewerCountTimeSeriesResponse{}, nil
		},
	}
	iv := "1h"
	if _, err := periR(p).DoGetViewerCountTimeSeries(clientstest.AuthedCtx("t1"), nil, nil, &iv); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotInterval != "1h" {
		t.Fatalf("explicit interval not forwarded: %q", gotInterval)
	}
}

// --- DoGetStreamHealthMetrics ---

func TestDoGetStreamHealthMetrics_PassesStreamPtrAndCopiesMetrics(t *testing.T) {
	var gotStream *string
	p := &clientstest.FakePeriscope{
		GetStreamHealthMetricsFn: func(_ context.Context, _ string, streamID *string, _ *periscope.TimeRangeOpts, _ *periscope.CursorPaginationOpts) (*periscopepb.GetStreamHealthMetricsResponse, error) {
			gotStream = streamID
			return &periscopepb.GetStreamHealthMetricsResponse{
				Metrics: []*periscopepb.StreamHealthMetric{{Id: "m1"}, {Id: "m2"}},
			}, nil
		},
	}
	relayID := globalid.Encode(globalid.TypeStream, "raw-h")
	got, err := periR(p).DoGetStreamHealthMetrics(clientstest.AuthedCtx("t1"), relayID, testRange())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotStream == nil || *gotStream != "raw-h" {
		t.Fatalf("stream not normalized into ptr: %v", gotStream)
	}
	if len(got) != 2 || got[0].Id != "m1" {
		t.Fatalf("metrics not returned: %+v", got)
	}
}

func TestDoGetStreamHealthMetrics_TenantGuard(t *testing.T) {
	p := &clientstest.FakePeriscope{}
	if _, err := periR(p).DoGetStreamHealthMetrics(authedNoTenantCtx(), "s1", nil); err == nil {
		t.Fatal("expected tenant required error")
	}
	if p.Calls != 0 {
		t.Fatalf("backend hit despite missing tenant: Calls=%d", p.Calls)
	}
}

// --- DoGetGeographicDistribution ---

func TestDoGetGeographicDistribution_DefaultTopNAndMapsModel(t *testing.T) {
	var gotTopN int32
	var gotStream *string
	p := &clientstest.FakePeriscope{
		GetGeographicDistributionFn: func(_ context.Context, _ string, streamID *string, _ *periscope.TimeRangeOpts, topN int32) (*periscopepb.GetGeographicDistributionResponse, error) {
			gotTopN, gotStream = topN, streamID
			return &periscopepb.GetGeographicDistributionResponse{
				UniqueCountries: 4,
				UniqueCities:    9,
				TotalViewers:    100,
			}, nil
		},
	}
	relayID := globalid.Encode(globalid.TypeStream, "raw-g")
	got, err := periR(p).DoGetGeographicDistribution(clientstest.AuthedCtx("t1"), &relayID, testRange(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotTopN != 10 {
		t.Fatalf("default topN not 10: %d", gotTopN)
	}
	if gotStream == nil || *gotStream != "raw-g" {
		t.Fatalf("stream not normalized: %v", gotStream)
	}
	if got.UniqueCountries != 4 || got.UniqueCities != 9 || got.TotalViewers != 100 {
		t.Fatalf("model not mapped: %+v", got)
	}
	if got.Stream == nil || *got.Stream != "raw-g" {
		t.Fatalf("stream not echoed into model: %v", got.Stream)
	}
}

func TestDoGetGeographicDistribution_ExplicitTopN(t *testing.T) {
	var gotTopN int32
	p := &clientstest.FakePeriscope{
		GetGeographicDistributionFn: func(_ context.Context, _ string, _ *string, _ *periscope.TimeRangeOpts, topN int32) (*periscopepb.GetGeographicDistributionResponse, error) {
			gotTopN = topN
			return &periscopepb.GetGeographicDistributionResponse{}, nil
		},
	}
	n := 25
	if _, err := periR(p).DoGetGeographicDistribution(clientstest.AuthedCtx("t1"), nil, nil, &n); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotTopN != 25 {
		t.Fatalf("explicit topN not forwarded: %d", gotTopN)
	}
}

// --- DoGetStreamHealthSummary ---

func TestDoGetStreamHealthSummary_MapsSummaryAndQualityTier(t *testing.T) {
	var gotTR *periscope.TimeRangeOpts
	p := &clientstest.FakePeriscope{
		GetStreamHealthSummaryFn: func(_ context.Context, _ string, _ *string, tr *periscope.TimeRangeOpts) (*periscopepb.GetStreamHealthSummaryResponse, error) {
			gotTR = tr
			return &periscopepb.GetStreamHealthSummaryResponse{
				Summary: &periscopepb.StreamHealthSummary{
					AvgBitrate:         12.5,
					TotalRebufferCount: 3,
					SampleCount:        50,
					HasActiveIssues:    true,
					CurrentQualityTier: "hd",
				},
			}, nil
		},
	}
	tr := testRange()
	got, err := periR(p).DoGetStreamHealthSummary(clientstest.AuthedCtx("t1"), nil, tr, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotTR == nil || !gotTR.StartTime.Equal(tr.Start) {
		t.Fatalf("time range not converted: %+v", gotTR)
	}
	if got.AvgBitrate != 12.5 || got.TotalRebufferCount != 3 || got.SampleCount != 50 || !got.HasActiveIssues {
		t.Fatalf("summary not mapped: %+v", got)
	}
	if got.CurrentQualityTier == nil || *got.CurrentQualityTier != "hd" {
		t.Fatalf("quality tier not mapped: %v", got.CurrentQualityTier)
	}
}

func TestDoGetStreamHealthSummary_NilSummaryReturnsZeroValue(t *testing.T) {
	p := &clientstest.FakePeriscope{
		GetStreamHealthSummaryFn: func(context.Context, string, *string, *periscope.TimeRangeOpts) (*periscopepb.GetStreamHealthSummaryResponse, error) {
			return &periscopepb.GetStreamHealthSummaryResponse{}, nil // absent summary
		},
	}
	got, err := periR(p).DoGetStreamHealthSummary(clientstest.AuthedCtx("t1"), nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Absent summary must yield a zero model, never a nil-deref panic.
	if got == nil || got.CurrentQualityTier != nil || got.SampleCount != 0 {
		t.Fatalf("expected zero summary, got %+v", got)
	}
}

func TestDoGetStreamHealthSummary_TenantGuard(t *testing.T) {
	p := &clientstest.FakePeriscope{}
	if _, err := periR(p).DoGetStreamHealthSummary(authedNoTenantCtx(), nil, nil, nil); err == nil {
		t.Fatal("expected tenant required error")
	}
	if p.Calls != 0 {
		t.Fatalf("backend hit despite missing tenant: Calls=%d", p.Calls)
	}
}

// --- DoGetRebufferingEventsConnection ---

func TestDoGetRebufferingEventsConnection_NormalizesStreamAndBuildsEdges(t *testing.T) {
	var gotStream *string
	var gotNode *string
	var gotTR *periscope.TimeRangeOpts
	p := &clientstest.FakePeriscope{
		GetRebufferingEventsFn: func(_ context.Context, _ string, streamID, nodeID *string, tr *periscope.TimeRangeOpts, _ *periscope.CursorPaginationOpts) (*periscopepb.GetRebufferingEventsResponse, error) {
			gotStream, gotNode, gotTR = streamID, nodeID, tr
			return &periscopepb.GetRebufferingEventsResponse{
				Events: []*periscopepb.RebufferingEvent{{Id: "rb1", StreamId: "raw-rb"}},
			}, nil
		},
	}
	relayID := globalid.Encode(globalid.TypeStream, "raw-rb")
	node := "node-7"
	tr := testRange()
	got, err := periR(p).DoGetRebufferingEventsConnection(clientstest.AuthedCtx("t1"), &relayID, &node, tr, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotStream == nil || *gotStream != "raw-rb" {
		t.Fatalf("stream not normalized: %v", gotStream)
	}
	if gotNode == nil || *gotNode != "node-7" {
		t.Fatalf("nodeID not forwarded: %v", gotNode)
	}
	if gotTR == nil || !gotTR.StartTime.Equal(tr.Start) {
		t.Fatalf("time range not converted: %+v", gotTR)
	}
	if len(got.Edges) != 1 || got.Edges[0].Node.Id != "rb1" {
		t.Fatalf("edges not built: %+v", got.Edges)
	}
	if len(got.Nodes) != 1 || got.Nodes[0].Id != "rb1" {
		t.Fatalf("nodes not built: %+v", got.Nodes)
	}
}

func TestDoGetRebufferingEventsConnection_TenantGuard(t *testing.T) {
	p := &clientstest.FakePeriscope{}
	if _, err := periR(p).DoGetRebufferingEventsConnection(authedNoTenantCtx(), nil, nil, nil, nil, nil, nil, nil, nil); err == nil {
		t.Fatal("expected tenant required error")
	}
	if p.Calls != 0 {
		t.Fatalf("backend hit despite missing tenant: Calls=%d", p.Calls)
	}
}

// --- DoGetStreamHealthMetricsConnection (via loadStreamHealthMetrics) ---

func TestDoGetStreamHealthMetricsConnection_NormalizesStreamAndDefaultsHasIssues(t *testing.T) {
	var gotStream *string
	p := &clientstest.FakePeriscope{
		GetStreamHealthMetricsFn: func(_ context.Context, _ string, streamID *string, _ *periscope.TimeRangeOpts, _ *periscope.CursorPaginationOpts) (*periscopepb.GetStreamHealthMetricsResponse, error) {
			gotStream = streamID
			return &periscopepb.GetStreamHealthMetricsResponse{
				Metrics: []*periscopepb.StreamHealthMetric{{Id: "h1"}}, // HasIssues nil → resolver normalizes to false
			}, nil
		},
	}
	relayID := globalid.Encode(globalid.TypeStream, "raw-shm")
	got, err := periR(p).DoGetStreamHealthMetricsConnection(clientstest.AuthedCtx("t1"), relayID, testRange(), nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotStream == nil || *gotStream != "raw-shm" {
		t.Fatalf("stream not normalized into filter: %v", gotStream)
	}
	if len(got.Edges) != 1 || got.Edges[0].Node.Id != "h1" {
		t.Fatalf("edges not built: %+v", got.Edges)
	}
	if got.Edges[0].Node.HasIssues == nil || *got.Edges[0].Node.HasIssues {
		t.Fatalf("HasIssues not defaulted to false: %v", got.Edges[0].Node.HasIssues)
	}
}

// --- DoGetStreamEventsConnection ---

func TestDoGetStreamEventsConnection_NormalizesAndMapsEvents(t *testing.T) {
	var gotStream string
	p := &clientstest.FakePeriscope{
		GetStreamEventsFn: func(_ context.Context, _ string, streamID string, _ *periscope.TimeRangeOpts, _ *periscope.CursorPaginationOpts) (*periscopepb.GetStreamEventsResponse, error) {
			gotStream = streamID
			return &periscopepb.GetStreamEventsResponse{
				Events: []*periscopepb.StreamEvent{{
					EventId:   "ev1",
					StreamId:  "raw-se",
					EventType: "STREAM_START",
					Status:    "LIVE",
				}},
			}, nil
		},
	}
	relayID := globalid.Encode(globalid.TypeStream, "raw-se")
	got, err := periR(p).DoGetStreamEventsConnection(clientstest.AuthedCtx("t1"), relayID, testRange(), nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotStream != "raw-se" {
		t.Fatalf("stream not normalized: %q", gotStream)
	}
	if len(got.Edges) != 1 {
		t.Fatalf("expected 1 edge: %+v", got.Edges)
	}
	node := got.Edges[0].Node
	if node.EventId != "ev1" || node.StreamId != "raw-se" {
		t.Fatalf("event not mapped: %+v", node)
	}
	if node.Type != model.StreamEventTypeStreamStart {
		t.Fatalf("event type not mapped: %v", node.Type)
	}
	if node.Status == nil || *node.Status != model.StreamStatusLive {
		t.Fatalf("status not mapped: %v", node.Status)
	}
}

func TestDoGetStreamEventsConnection_EmptyStreamRejected(t *testing.T) {
	p := &clientstest.FakePeriscope{}
	if _, err := periR(p).DoGetStreamEventsConnection(clientstest.AuthedCtx("t1"), "", nil, nil, nil, nil, nil, nil); err == nil {
		t.Fatal("expected streamId required error")
	}
	if p.Calls != 0 {
		t.Fatalf("backend hit despite empty stream: Calls=%d", p.Calls)
	}
}

// --- DoGetConnectionEventsConnection ---

func TestDoGetConnectionEventsConnection_NormalizesStreamAndBuildsEdges(t *testing.T) {
	var gotStream *string
	p := &clientstest.FakePeriscope{
		GetConnectionEventsFn: func(_ context.Context, _ string, streamID *string, _ *periscope.TimeRangeOpts, _ *periscope.CursorPaginationOpts) (*periscopepb.GetConnectionEventsResponse, error) {
			gotStream = streamID
			return &periscopepb.GetConnectionEventsResponse{
				Events: []*periscopepb.ConnectionEvent{{EventId: "c1", StreamId: "raw-ce"}},
			}, nil
		},
	}
	relayID := globalid.Encode(globalid.TypeStream, "raw-ce")
	got, err := periR(p).DoGetConnectionEventsConnection(clientstest.AuthedCtx("t1"), &relayID, testRange(), nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotStream == nil || *gotStream != "raw-ce" {
		t.Fatalf("stream not normalized: %v", gotStream)
	}
	if len(got.Edges) != 1 || got.Edges[0].Node.EventId != "c1" {
		t.Fatalf("edges not built: %+v", got.Edges)
	}
}

func TestDoGetConnectionEventsConnection_TenantGuard(t *testing.T) {
	p := &clientstest.FakePeriscope{}
	if _, err := periR(p).DoGetConnectionEventsConnection(authedNoTenantCtx(), nil, nil, nil, nil, nil, nil, nil); err == nil {
		t.Fatal("expected tenant required error")
	}
	if p.Calls != 0 {
		t.Fatalf("backend hit despite missing tenant: Calls=%d", p.Calls)
	}
}

// --- DoGetRoutingEventsConnection (no user in ctx → no Quartermaster call) ---

func TestDoGetRoutingEventsConnection_NormalizesStreamAndForwardsFilters(t *testing.T) {
	var gotStream *string
	var gotSubject *string
	var gotCluster *string
	p := &clientstest.FakePeriscope{
		GetRoutingEventsFn: func(_ context.Context, _ string, streamID *string, _ *periscope.TimeRangeOpts, _ *periscope.CursorPaginationOpts, _ []string, subjectTenantID, clusterID *string) (*periscopepb.GetRoutingEventsResponse, error) {
			gotStream, gotSubject, gotCluster = streamID, subjectTenantID, clusterID
			return &periscopepb.GetRoutingEventsResponse{
				Events: []*periscopepb.RoutingEvent{{Id: "r1", StreamId: "raw-re"}},
			}, nil
		},
	}
	relayID := globalid.Encode(globalid.TypeStream, "raw-re")
	subject := "subject-t"
	cluster := "cl-1"
	got, err := periR(p).DoGetRoutingEventsConnection(clientstest.AuthedCtx("t1"), &relayID, testRange(), &subject, &cluster, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotStream == nil || *gotStream != "raw-re" {
		t.Fatalf("stream not normalized: %v", gotStream)
	}
	if gotSubject == nil || *gotSubject != "subject-t" {
		t.Fatalf("subjectTenantID not forwarded: %v", gotSubject)
	}
	if gotCluster == nil || *gotCluster != "cl-1" {
		t.Fatalf("clusterID not forwarded: %v", gotCluster)
	}
	if len(got.Edges) != 1 || got.Edges[0].Node.Id != "r1" {
		t.Fatalf("edges not built: %+v", got.Edges)
	}
}

// helpers for non-finite floats kept local to avoid importing math at call sites.
func inf() float64 { var z float64; return 1 / z }
func nan() float64 { var z float64; return z / z }
