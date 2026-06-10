package resolvers

import (
	"context"
	"errors"
	"testing"

	"frameworks/api_gateway/graph/model"
	"frameworks/api_gateway/internal/clients/clientstest"
	"frameworks/api_gateway/internal/middleware"

	periscope "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/periscope"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/globalid"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	commonpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/common"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// periB wires a resolver to a FakePeriscope only; every other client stays nil
// and panics if a resolver reaches for it, proving the Periscope-only path ran.
func periB(p *clientstest.FakePeriscope) *Resolver {
	return &Resolver{Clients: clientstest.Clients(clientstest.WithPeriscope(p)), Logger: clientstest.DiscardLogger()}
}

// --- DoGetStorageEventsConnection ---

func TestDoGetStorageEventsConnection_NormalizesStreamAndForwardsFilters(t *testing.T) {
	var gotStream *string
	var gotAsset *string
	var gotTR *periscope.TimeRangeOpts
	p := &clientstest.FakePeriscope{
		GetStorageEventsFn: func(_ context.Context, _ string, streamID, assetType *string, tr *periscope.TimeRangeOpts, _ *periscope.CursorPaginationOpts) (*periscopepb.GetStorageEventsResponse, error) {
			gotStream, gotAsset, gotTR = streamID, assetType, tr
			return &periscopepb.GetStorageEventsResponse{
				Events: []*periscopepb.StorageEvent{{Id: "se1", StreamId: "raw-st"}},
			}, nil
		},
	}
	relayID := globalid.Encode(globalid.TypeStream, "raw-st")
	asset := "clip"
	tr := testRange()
	got, err := periB(p).DoGetStorageEventsConnection(clientstest.AuthedCtx("t1"), &relayID, &asset, tr, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotStream == nil || *gotStream != "raw-st" {
		t.Fatalf("stream not normalized: %v", gotStream)
	}
	if gotAsset == nil || *gotAsset != "clip" {
		t.Fatalf("assetType not forwarded: %v", gotAsset)
	}
	if gotTR == nil || !gotTR.StartTime.Equal(tr.Start) {
		t.Fatalf("time range not converted: %+v", gotTR)
	}
	if len(got.Edges) != 1 || got.Edges[0].Node.Id != "se1" {
		t.Fatalf("edges not built: %+v", got.Edges)
	}
	if len(got.Nodes) != 1 || got.Nodes[0].Id != "se1" {
		t.Fatalf("nodes not built: %+v", got.Nodes)
	}
}

func TestDoGetStorageEventsConnection_BackendError(t *testing.T) {
	p := &clientstest.FakePeriscope{
		GetStorageEventsFn: func(context.Context, string, *string, *string, *periscope.TimeRangeOpts, *periscope.CursorPaginationOpts) (*periscopepb.GetStorageEventsResponse, error) {
			return nil, errors.New("boom")
		},
	}
	if _, err := periB(p).DoGetStorageEventsConnection(clientstest.AuthedCtx("t1"), nil, nil, nil, nil, nil, nil, nil, nil); err == nil {
		t.Fatal("expected error propagation")
	}
}

func TestDoGetStorageEventsConnection_TenantGuard(t *testing.T) {
	p := &clientstest.FakePeriscope{}
	if _, err := periB(p).DoGetStorageEventsConnection(authedNoTenantCtx(), nil, nil, nil, nil, nil, nil, nil, nil); err == nil {
		t.Fatal("expected tenant required error")
	}
	if p.Calls != 0 {
		t.Fatalf("backend hit despite missing tenant: Calls=%d", p.Calls)
	}
}

// --- DoGetStorageUsageConnection ---

func TestDoGetStorageUsageConnection_ForwardsFiltersAndBuildsEdges(t *testing.T) {
	var gotNode *string
	var gotScope *string
	p := &clientstest.FakePeriscope{
		GetStorageUsageFn: func(_ context.Context, _ string, nodeID, storageScope *string, _ *periscope.TimeRangeOpts, _ *periscope.CursorPaginationOpts) (*periscopepb.GetStorageUsageResponse, error) {
			gotNode, gotScope = nodeID, storageScope
			return &periscopepb.GetStorageUsageResponse{
				Records: []*periscopepb.StorageUsageRecord{{Id: "su1"}},
			}, nil
		},
	}
	node := "node-1"
	scope := "hot"
	got, err := periB(p).DoGetStorageUsageConnection(clientstest.AuthedCtx("t1"), &node, &scope, testRange(), nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotNode == nil || *gotNode != "node-1" {
		t.Fatalf("nodeID not forwarded: %v", gotNode)
	}
	if gotScope == nil || *gotScope != "hot" {
		t.Fatalf("storageScope not forwarded: %v", gotScope)
	}
	if len(got.Edges) != 1 || got.Edges[0].Node.Id != "su1" {
		t.Fatalf("edges not built: %+v", got.Edges)
	}
}

func TestDoGetStorageUsageConnection_TenantGuard(t *testing.T) {
	p := &clientstest.FakePeriscope{}
	if _, err := periB(p).DoGetStorageUsageConnection(authedNoTenantCtx(), nil, nil, nil, nil, nil, nil, nil, nil); err == nil {
		t.Fatal("expected tenant required error")
	}
	if p.Calls != 0 {
		t.Fatalf("backend hit despite missing tenant: Calls=%d", p.Calls)
	}
}

// --- DoGetStreamAnalyticsDailyConnection ---

func TestDoGetStreamAnalyticsDailyConnection_NormalizesStreamAndBuildsEdges(t *testing.T) {
	var gotStream *string
	p := &clientstest.FakePeriscope{
		GetStreamAnalyticsDailyFn: func(_ context.Context, _ string, streamID *string, _ *periscope.TimeRangeOpts, _ *periscope.CursorPaginationOpts) (*periscopepb.GetStreamAnalyticsDailyResponse, error) {
			gotStream = streamID
			return &periscopepb.GetStreamAnalyticsDailyResponse{
				Records: []*periscopepb.StreamAnalyticsDaily{{Id: "d1", StreamId: "raw-sad", TotalViews: 9}},
			}, nil
		},
	}
	relayID := globalid.Encode(globalid.TypeStream, "raw-sad")
	got, err := periB(p).DoGetStreamAnalyticsDailyConnection(clientstest.AuthedCtx("t1"), &relayID, testRange(), nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotStream == nil || *gotStream != "raw-sad" {
		t.Fatalf("stream not normalized: %v", gotStream)
	}
	if len(got.Edges) != 1 || got.Edges[0].Node.StreamId != "raw-sad" || got.Edges[0].Node.TotalViews != 9 {
		t.Fatalf("edges not built/mapped: %+v", got.Edges)
	}
}

func TestDoGetStreamAnalyticsDailyConnection_TenantGuard(t *testing.T) {
	p := &clientstest.FakePeriscope{}
	if _, err := periB(p).DoGetStreamAnalyticsDailyConnection(authedNoTenantCtx(), nil, nil, nil, nil, nil, nil, nil); err == nil {
		t.Fatal("expected tenant required error")
	}
	if p.Calls != 0 {
		t.Fatalf("backend hit despite missing tenant: Calls=%d", p.Calls)
	}
}

// --- DoGetStreamAnalyticsSummariesConnection ---

func TestDoGetStreamAnalyticsSummariesConnection_MapsSortEnumsAndPage(t *testing.T) {
	var gotSortBy periscope.StreamSummarySortField
	var gotSortOrder periscope.SortOrder
	var gotFirst int32
	p := &clientstest.FakePeriscope{
		GetStreamAnalyticsSummariesFn: func(_ context.Context, _ string, _ *periscope.TimeRangeOpts, sortBy periscope.StreamSummarySortField, sortOrder periscope.SortOrder, opts *periscope.CursorPaginationOpts) (*periscopepb.GetStreamAnalyticsSummariesResponse, error) {
			gotSortBy, gotSortOrder = sortBy, sortOrder
			if opts != nil {
				gotFirst = opts.First
			}
			return &periscopepb.GetStreamAnalyticsSummariesResponse{
				Summaries: []*periscopepb.StreamAnalyticsSummary{{StreamId: "raw-sum", RangeTotalViews: 7}},
			}, nil
		},
	}
	first := 12
	page := &model.ConnectionInput{First: &first}
	sortBy := periscopepb.StreamSummarySortField_STREAM_SUMMARY_SORT_FIELD_TOTAL_VIEWS
	sortOrder := commonpb.SortOrder_SORT_ORDER_ASC
	got, err := periB(p).DoGetStreamAnalyticsSummariesConnection(clientstest.AuthedCtx("t1"), page, testRange(), &sortBy, &sortOrder)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Proto sort enum must map to the client string enum.
	if gotSortBy != periscope.StreamSummarySortFieldTotalViews {
		t.Fatalf("sortBy not mapped: %q", gotSortBy)
	}
	if gotSortOrder != periscope.SortOrderAsc {
		t.Fatalf("sortOrder not mapped: %q", gotSortOrder)
	}
	if gotFirst != 12 {
		t.Fatalf("page.First not forwarded: %d", gotFirst)
	}
	if len(got.Edges) != 1 || got.Edges[0].Node.StreamId != "raw-sum" {
		t.Fatalf("edges not built: %+v", got.Edges)
	}
}

func TestDoGetStreamAnalyticsSummariesConnection_DefaultSortIsEgressDesc(t *testing.T) {
	var gotSortBy periscope.StreamSummarySortField
	var gotSortOrder periscope.SortOrder
	p := &clientstest.FakePeriscope{
		GetStreamAnalyticsSummariesFn: func(_ context.Context, _ string, _ *periscope.TimeRangeOpts, sortBy periscope.StreamSummarySortField, sortOrder periscope.SortOrder, _ *periscope.CursorPaginationOpts) (*periscopepb.GetStreamAnalyticsSummariesResponse, error) {
			gotSortBy, gotSortOrder = sortBy, sortOrder
			return &periscopepb.GetStreamAnalyticsSummariesResponse{}, nil
		},
	}
	// Nil sortBy/sortOrder must default to egress-GB descending.
	if _, err := periB(p).DoGetStreamAnalyticsSummariesConnection(clientstest.AuthedCtx("t1"), nil, nil, nil, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotSortBy != periscope.StreamSummarySortFieldEgressGB {
		t.Fatalf("default sortBy not egress: %q", gotSortBy)
	}
	if gotSortOrder != periscope.SortOrderDesc {
		t.Fatalf("default sortOrder not desc: %q", gotSortOrder)
	}
}

func TestDoGetStreamAnalyticsSummariesConnection_TenantGuard(t *testing.T) {
	p := &clientstest.FakePeriscope{}
	if _, err := periB(p).DoGetStreamAnalyticsSummariesConnection(authedNoTenantCtx(), nil, nil, nil, nil); err == nil {
		t.Fatal("expected tenant required error")
	}
	if p.Calls != 0 {
		t.Fatalf("backend hit despite missing tenant: Calls=%d", p.Calls)
	}
}

// --- DoGetStreamConnectionHourlyConnection ---

func TestDoGetStreamConnectionHourlyConnection_NormalizesStreamAndConvertsRange(t *testing.T) {
	var gotStream *string
	var gotTR *periscope.TimeRangeOpts
	p := &clientstest.FakePeriscope{
		GetStreamConnectionHourlyFn: func(_ context.Context, _ string, streamID *string, tr *periscope.TimeRangeOpts, _ *periscope.CursorPaginationOpts) (*periscopepb.GetStreamConnectionHourlyResponse, error) {
			gotStream, gotTR = streamID, tr
			return &periscopepb.GetStreamConnectionHourlyResponse{
				Records: []*periscopepb.StreamConnectionHourly{{Id: "sch1", StreamId: "raw-sch"}},
			}, nil
		},
	}
	relayID := globalid.Encode(globalid.TypeStream, "raw-sch")
	tr := testRange()
	got, err := periB(p).DoGetStreamConnectionHourlyConnection(clientstest.AuthedCtx("t1"), &relayID, tr, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotStream == nil || *gotStream != "raw-sch" {
		t.Fatalf("stream not normalized: %v", gotStream)
	}
	if gotTR == nil || !gotTR.StartTime.Equal(tr.Start) || !gotTR.EndTime.Equal(tr.End) {
		t.Fatalf("time range not converted: %+v", gotTR)
	}
	if len(got.Edges) != 1 || got.Edges[0].Node.StreamId != "raw-sch" {
		t.Fatalf("edges not built: %+v", got.Edges)
	}
}

func TestDoGetStreamConnectionHourlyConnection_TenantGuard(t *testing.T) {
	p := &clientstest.FakePeriscope{}
	if _, err := periB(p).DoGetStreamConnectionHourlyConnection(authedNoTenantCtx(), nil, nil, nil, nil, nil, nil, nil); err == nil {
		t.Fatal("expected tenant required error")
	}
	if p.Calls != 0 {
		t.Fatalf("backend hit despite missing tenant: Calls=%d", p.Calls)
	}
}

// --- DoGetStreamHealthConnection (Stream-resolver variant via loadStreamHealthMetrics) ---

func TestDoGetStreamHealthConnection_FiltersByParentStreamID(t *testing.T) {
	var gotStream *string
	p := &clientstest.FakePeriscope{
		GetStreamHealthMetricsFn: func(_ context.Context, _ string, streamID *string, _ *periscope.TimeRangeOpts, _ *periscope.CursorPaginationOpts) (*periscopepb.GetStreamHealthMetricsResponse, error) {
			gotStream = streamID
			return &periscopepb.GetStreamHealthMetricsResponse{
				Metrics: []*periscopepb.StreamHealthMetric{{Id: "shc1"}},
			}, nil
		},
	}
	// Parent Stream.StreamId is the already-raw filter applied to the health query.
	obj := &commodorepb.Stream{StreamId: "raw-shc"}
	got, err := periB(p).DoGetStreamHealthConnection(clientstest.AuthedCtx("t1"), obj, testRange(), nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotStream == nil || *gotStream != "raw-shc" {
		t.Fatalf("stream filter not taken from parent: %v", gotStream)
	}
	if len(got.Edges) != 1 || got.Edges[0].Node.Id != "shc1" {
		t.Fatalf("edges not built: %+v", got.Edges)
	}
}

func TestDoGetStreamHealthConnection_TenantGuard(t *testing.T) {
	p := &clientstest.FakePeriscope{}
	obj := &commodorepb.Stream{StreamId: "s1"}
	if _, err := periB(p).DoGetStreamHealthConnection(authedNoTenantCtx(), obj, nil, nil, nil); err == nil {
		t.Fatal("expected tenant required error")
	}
	if p.Calls != 0 {
		t.Fatalf("backend hit despite missing tenant: Calls=%d", p.Calls)
	}
}

// --- DoGetTenantAnalyticsDailyConnection ---

func TestDoGetTenantAnalyticsDailyConnection_ConvertsRangeAndBuildsEdges(t *testing.T) {
	var gotTenant string
	var gotTR *periscope.TimeRangeOpts
	p := &clientstest.FakePeriscope{
		GetTenantAnalyticsDailyFn: func(_ context.Context, tenantID string, tr *periscope.TimeRangeOpts, _ *periscope.CursorPaginationOpts) (*periscopepb.GetTenantAnalyticsDailyResponse, error) {
			gotTenant, gotTR = tenantID, tr
			return &periscopepb.GetTenantAnalyticsDailyResponse{
				Records: []*periscopepb.TenantAnalyticsDaily{{Id: "tad1", TotalViews: 5}},
			}, nil
		},
	}
	tr := testRange()
	got, err := periB(p).DoGetTenantAnalyticsDailyConnection(clientstest.AuthedCtx("t1"), tr, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotTenant != "t1" {
		t.Fatalf("tenant = %q", gotTenant)
	}
	if gotTR == nil || !gotTR.StartTime.Equal(tr.Start) {
		t.Fatalf("time range not converted: %+v", gotTR)
	}
	if len(got.Edges) != 1 || got.Edges[0].Node.TotalViews != 5 {
		t.Fatalf("edges not built/mapped: %+v", got.Edges)
	}
}

func TestDoGetTenantAnalyticsDailyConnection_TenantGuard(t *testing.T) {
	p := &clientstest.FakePeriscope{}
	if _, err := periB(p).DoGetTenantAnalyticsDailyConnection(authedNoTenantCtx(), nil, nil, nil, nil, nil, nil); err == nil {
		t.Fatal("expected tenant required error")
	}
	if p.Calls != 0 {
		t.Fatalf("backend hit despite missing tenant: Calls=%d", p.Calls)
	}
}

// --- DoGetTrackListEventsConnection ---

func TestDoGetTrackListEventsConnection_ForwardsStreamAndBuildsEdges(t *testing.T) {
	var gotStream string
	p := &clientstest.FakePeriscope{
		GetTrackListEventsFn: func(_ context.Context, _ string, streamID string, _ *periscope.TimeRangeOpts, _ *periscope.CursorPaginationOpts) (*periscopepb.GetTrackListEventsResponse, error) {
			gotStream = streamID
			return &periscopepb.GetTrackListEventsResponse{
				Events: []*periscopepb.TrackListEvent{{Id: "tl1", StreamId: "raw-tl", TrackCount: 2}},
			}, nil
		},
	}
	got, err := periB(p).DoGetTrackListEventsConnection(clientstest.AuthedCtx("t1"), "raw-tl", testRange(), nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotStream != "raw-tl" {
		t.Fatalf("stream not forwarded: %q", gotStream)
	}
	if len(got.Edges) != 1 || got.Edges[0].Node.Id != "tl1" || got.Edges[0].Node.TrackCount != 2 {
		t.Fatalf("edges not built/mapped: %+v", got.Edges)
	}
}

func TestDoGetTrackListEventsConnection_EmptyStreamRejected(t *testing.T) {
	p := &clientstest.FakePeriscope{}
	if _, err := periB(p).DoGetTrackListEventsConnection(clientstest.AuthedCtx("t1"), "", nil, nil, nil, nil, nil, nil); err == nil {
		t.Fatal("expected stream_id required error")
	}
	if p.Calls != 0 {
		t.Fatalf("backend hit despite empty stream: Calls=%d", p.Calls)
	}
}

// --- DoGetViewerGeographicsConnection (loadConnectionEvents) ---

func TestDoGetViewerGeographicsConnection_NormalizesStreamAndWrapsConnectionEvents(t *testing.T) {
	var gotStream *string
	p := &clientstest.FakePeriscope{
		GetConnectionEventsFn: func(_ context.Context, _ string, streamID *string, _ *periscope.TimeRangeOpts, _ *periscope.CursorPaginationOpts) (*periscopepb.GetConnectionEventsResponse, error) {
			gotStream = streamID
			return &periscopepb.GetConnectionEventsResponse{
				Events: []*periscopepb.ConnectionEvent{{EventId: "vg1", StreamId: "raw-vg"}},
			}, nil
		},
	}
	relayID := globalid.Encode(globalid.TypeStream, "raw-vg")
	got, err := periB(p).DoGetViewerGeographicsConnection(clientstest.AuthedCtx("t1"), &relayID, testRange(), nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotStream == nil || *gotStream != "raw-vg" {
		t.Fatalf("stream not normalized: %v", gotStream)
	}
	// Geographics wraps the same ConnectionEvent node it queried.
	if len(got.Edges) != 1 || got.Edges[0].Node.EventId != "vg1" {
		t.Fatalf("edges not built: %+v", got.Edges)
	}
}

func TestDoGetViewerGeographicsConnection_TenantGuard(t *testing.T) {
	p := &clientstest.FakePeriscope{}
	if _, err := periB(p).DoGetViewerGeographicsConnection(authedNoTenantCtx(), nil, nil, nil, nil, nil, nil); err == nil {
		t.Fatal("expected tenant required error")
	}
	if p.Calls != 0 {
		t.Fatalf("backend hit despite missing tenant: Calls=%d", p.Calls)
	}
}

// --- DoGetViewerGeoHourlyConnection ---

func TestDoGetViewerGeoHourlyConnection_ConvertsRangeAndBuildsEdges(t *testing.T) {
	var gotTR *periscope.TimeRangeOpts
	p := &clientstest.FakePeriscope{
		GetViewerGeoHourlyFn: func(_ context.Context, _ string, _ *string, tr *periscope.TimeRangeOpts, _ *periscope.CursorPaginationOpts) (*periscopepb.GetViewerGeoHourlyResponse, error) {
			gotTR = tr
			return &periscopepb.GetViewerGeoHourlyResponse{
				Records: []*periscopepb.ViewerGeoHourly{{Id: "vgh1", CountryCode: "US", ViewerCount: 3}},
			}, nil
		},
	}
	tr := testRange()
	got, err := periB(p).DoGetViewerGeoHourlyConnection(clientstest.AuthedCtx("t1"), tr, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotTR == nil || !gotTR.StartTime.Equal(tr.Start) {
		t.Fatalf("time range not converted: %+v", gotTR)
	}
	if len(got.Edges) != 1 || got.Edges[0].Node.CountryCode != "US" || got.Edges[0].Node.ViewerCount != 3 {
		t.Fatalf("edges not built/mapped: %+v", got.Edges)
	}
}

func TestDoGetViewerGeoHourlyConnection_TenantGuard(t *testing.T) {
	p := &clientstest.FakePeriscope{}
	if _, err := periB(p).DoGetViewerGeoHourlyConnection(authedNoTenantCtx(), nil, nil, nil, nil, nil, nil); err == nil {
		t.Fatal("expected tenant required error")
	}
	if p.Calls != 0 {
		t.Fatalf("backend hit despite missing tenant: Calls=%d", p.Calls)
	}
}

// --- DoGetViewerHoursHourlyConnection ---

func TestDoGetViewerHoursHourlyConnection_NormalizesStreamAndBuildsEdges(t *testing.T) {
	var gotStream *string
	p := &clientstest.FakePeriscope{
		GetViewerHoursHourlyFn: func(_ context.Context, _ string, streamID *string, _ *periscope.TimeRangeOpts, _ *periscope.CursorPaginationOpts) (*periscopepb.GetViewerHoursHourlyResponse, error) {
			gotStream = streamID
			return &periscopepb.GetViewerHoursHourlyResponse{
				Records: []*periscopepb.ViewerHoursHourly{{Id: "vhh1", StreamId: "raw-vhh", UniqueViewers: 4}},
			}, nil
		},
	}
	relayID := globalid.Encode(globalid.TypeStream, "raw-vhh")
	got, err := periB(p).DoGetViewerHoursHourlyConnection(clientstest.AuthedCtx("t1"), &relayID, testRange(), nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotStream == nil || *gotStream != "raw-vhh" {
		t.Fatalf("stream not normalized: %v", gotStream)
	}
	if len(got.Edges) != 1 || got.Edges[0].Node.StreamId != "raw-vhh" || got.Edges[0].Node.UniqueViewers != 4 {
		t.Fatalf("edges not built/mapped: %+v", got.Edges)
	}
}

func TestDoGetViewerHoursHourlyConnection_TenantGuard(t *testing.T) {
	p := &clientstest.FakePeriscope{}
	if _, err := periB(p).DoGetViewerHoursHourlyConnection(authedNoTenantCtx(), nil, nil, nil, nil, nil, nil, nil); err == nil {
		t.Fatal("expected tenant required error")
	}
	if p.Calls != 0 {
		t.Fatalf("backend hit despite missing tenant: Calls=%d", p.Calls)
	}
}

// --- DoGetViewerSessionsConnection (GetViewerMetrics) ---

func TestDoGetViewerSessionsConnection_NormalizesStreamAndBuildsEdges(t *testing.T) {
	var gotStream *string
	p := &clientstest.FakePeriscope{
		GetViewerMetricsFn: func(_ context.Context, _ string, streamID *string, _ *periscope.TimeRangeOpts, _ *periscope.CursorPaginationOpts) (*periscopepb.GetViewerMetricsResponse, error) {
			gotStream = streamID
			return &periscopepb.GetViewerMetricsResponse{
				Sessions: []*periscopepb.ViewerSession{{SessionId: "vs1", StreamId: "raw-vs"}},
			}, nil
		},
	}
	relayID := globalid.Encode(globalid.TypeStream, "raw-vs")
	got, err := periB(p).DoGetViewerSessionsConnection(clientstest.AuthedCtx("t1"), &relayID, testRange(), nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotStream == nil || *gotStream != "raw-vs" {
		t.Fatalf("stream not normalized: %v", gotStream)
	}
	if len(got.Edges) != 1 || got.Edges[0].Node.SessionId != "vs1" {
		t.Fatalf("edges not built: %+v", got.Edges)
	}
}

func TestDoGetViewerSessionsConnection_TenantGuard(t *testing.T) {
	p := &clientstest.FakePeriscope{}
	if _, err := periB(p).DoGetViewerSessionsConnection(authedNoTenantCtx(), nil, nil, nil, nil, nil, nil, nil); err == nil {
		t.Fatal("expected tenant required error")
	}
	if p.Calls != 0 {
		t.Fatalf("backend hit despite missing tenant: Calls=%d", p.Calls)
	}
}

// --- DoGetViewerTimeSeriesConnection (GetViewerCountTimeSeries + client-side paging) ---

func TestDoGetViewerTimeSeriesConnection_DefaultIntervalAndClientPaging(t *testing.T) {
	var gotInterval string
	var gotStream *string
	p := &clientstest.FakePeriscope{
		GetViewerCountTimeSeriesFn: func(_ context.Context, _ string, streamID *string, _ *periscope.TimeRangeOpts, interval string) (*periscopepb.GetViewerCountTimeSeriesResponse, error) {
			gotInterval, gotStream = interval, streamID
			return &periscopepb.GetViewerCountTimeSeriesResponse{
				Buckets: []*periscopepb.ViewerCountBucket{
					{StreamId: "raw-vts", ViewerCount: 1, Timestamp: timestamppb.New(testRange().Start)},
					{StreamId: "raw-vts", ViewerCount: 2, Timestamp: timestamppb.New(testRange().End)},
				},
			}, nil
		},
	}
	relayID := globalid.Encode(globalid.TypeStream, "raw-vts")
	got, err := periB(p).DoGetViewerTimeSeriesConnection(clientstest.AuthedCtx("t1"), relayID, testRange(), nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Default interval is 5m and the relay ID is decoded to raw before the query.
	if gotInterval != "5m" {
		t.Fatalf("default interval not 5m: %q", gotInterval)
	}
	if gotStream == nil || *gotStream != "raw-vts" {
		t.Fatalf("stream not normalized: %v", gotStream)
	}
	// TotalCount reflects the full pre-pagination bucket set.
	if got.TotalCount != 2 || len(got.Edges) != 2 {
		t.Fatalf("buckets not paged correctly: total=%d edges=%d", got.TotalCount, len(got.Edges))
	}
}

func TestDoGetViewerTimeSeriesConnection_ExplicitInterval(t *testing.T) {
	var gotInterval string
	p := &clientstest.FakePeriscope{
		GetViewerCountTimeSeriesFn: func(_ context.Context, _ string, _ *string, _ *periscope.TimeRangeOpts, interval string) (*periscopepb.GetViewerCountTimeSeriesResponse, error) {
			gotInterval = interval
			return &periscopepb.GetViewerCountTimeSeriesResponse{}, nil
		},
	}
	iv := "1h"
	if _, err := periB(p).DoGetViewerTimeSeriesConnection(clientstest.AuthedCtx("t1"), "raw", nil, &iv, nil, nil, nil, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotInterval != "1h" {
		t.Fatalf("explicit interval not forwarded: %q", gotInterval)
	}
}

func TestDoGetViewerTimeSeriesConnection_TenantGuard(t *testing.T) {
	p := &clientstest.FakePeriscope{}
	if _, err := periB(p).DoGetViewerTimeSeriesConnection(authedNoTenantCtx(), "s1", nil, nil, nil, nil, nil, nil); err == nil {
		t.Fatal("expected tenant required error")
	}
	if p.Calls != 0 {
		t.Fatalf("backend hit despite missing tenant: Calls=%d", p.Calls)
	}
}

// --- DoGetVodRetention ---

func TestDoGetVodRetention_ForwardsHashAndReturnsRetention(t *testing.T) {
	var gotHash string
	var gotTR *periscope.TimeRangeOpts
	p := &clientstest.FakePeriscope{
		GetVodRetentionFn: func(_ context.Context, _ string, artifactHash string, tr *periscope.TimeRangeOpts) (*periscopepb.GetVodRetentionResponse, error) {
			gotHash, gotTR = artifactHash, tr
			return &periscopepb.GetVodRetentionResponse{
				Retention: &periscopepb.VodRetention{TotalSessions: 11, AssetDurationS: 120},
			}, nil
		},
	}
	tr := testRange()
	got, err := periB(p).DoGetVodRetention(clientstest.AuthedCtx("t1"), "hash-1", tr, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotHash != "hash-1" {
		t.Fatalf("artifactHash not forwarded: %q", gotHash)
	}
	if gotTR == nil || !gotTR.StartTime.Equal(tr.Start) {
		t.Fatalf("time range not converted: %+v", gotTR)
	}
	if got.TotalSessions != 11 || got.AssetDurationS != 120 {
		t.Fatalf("retention not returned: %+v", got)
	}
}

func TestDoGetVodRetention_NilRetentionReturnsZeroValue(t *testing.T) {
	p := &clientstest.FakePeriscope{
		GetVodRetentionFn: func(context.Context, string, string, *periscope.TimeRangeOpts) (*periscopepb.GetVodRetentionResponse, error) {
			return &periscopepb.GetVodRetentionResponse{}, nil // absent retention
		},
	}
	got, err := periB(p).DoGetVodRetention(clientstest.AuthedCtx("t1"), "hash-1", nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Absent retention must yield a zero message, never a nil-deref.
	if got == nil || got.TotalSessions != 0 {
		t.Fatalf("expected zero retention, got %+v", got)
	}
}

func TestDoGetVodRetention_EmptyHashRejected(t *testing.T) {
	p := &clientstest.FakePeriscope{}
	if _, err := periB(p).DoGetVodRetention(clientstest.AuthedCtx("t1"), "", nil, nil); err == nil {
		t.Fatal("expected artifactHash required error")
	}
	if p.Calls != 0 {
		t.Fatalf("backend hit despite empty hash: Calls=%d", p.Calls)
	}
}

func TestDoGetVodRetention_TenantGuard(t *testing.T) {
	p := &clientstest.FakePeriscope{}
	if _, err := periB(p).DoGetVodRetention(authedNoTenantCtx(), "hash-1", nil, nil); err == nil {
		t.Fatal("expected tenant required error")
	}
	if p.Calls != 0 {
		t.Fatalf("backend hit despite missing tenant: Calls=%d", p.Calls)
	}
}

// --- DoListVodRetentionAssets (Periscope list + Commodore catalog enrichment) ---

func TestDoListVodRetentionAssets_MapsAssetsAndHydratesCatalog(t *testing.T) {
	var gotTenant string
	p := &clientstest.FakePeriscope{
		ListVodRetentionAssetsFn: func(_ context.Context, tenantID string, _ *periscope.TimeRangeOpts, _ *periscope.CursorPaginationOpts) (*periscopepb.ListVodRetentionAssetsResponse, error) {
			gotTenant = tenantID
			return &periscopepb.ListVodRetentionAssetsResponse{
				Assets: []*periscopepb.VodRetentionAsset{{ArtifactHash: "ah1", TotalSessions: 6, DurationS: 90}},
			}, nil
		},
	}
	var gotHash string
	c := &clientstest.FakeCommodore{
		GetVodAssetFn: func(_ context.Context, _ string, artifactHash string) (*sharedpb.VodAssetInfo, error) {
			gotHash = artifactHash
			pb := "pb-1"
			return &sharedpb.VodAssetInfo{Title: "My VOD", PlaybackId: &pb}, nil
		},
	}
	r := &Resolver{
		Clients: clientstest.Clients(clientstest.WithPeriscope(p), clientstest.WithCommodore(c)),
		Logger:  clientstest.DiscardLogger(),
	}
	got, err := r.DoListVodRetentionAssets(clientstest.AuthedCtx("t1"), nil, nil, nil, nil, testRange(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotTenant != "t1" {
		t.Fatalf("tenant = %q", gotTenant)
	}
	// The Periscope artifact_hash must drive the Commodore catalog lookup.
	if gotHash != "ah1" {
		t.Fatalf("catalog not hydrated by artifact_hash: %q", gotHash)
	}
	if len(got.Nodes) != 1 {
		t.Fatalf("expected 1 node: %+v", got.Nodes)
	}
	n := got.Nodes[0]
	if n.ArtifactHash != "ah1" || n.TotalSessions != 6 || n.DurationS != 90 {
		t.Fatalf("stats not mapped: %+v", n)
	}
	if n.Title == nil || *n.Title != "My VOD" || n.PlaybackID == nil || *n.PlaybackID != "pb-1" {
		t.Fatalf("catalog title/playbackId not composed: %+v", n)
	}
}

func TestDoListVodRetentionAssets_EmptyListSkipsCatalog(t *testing.T) {
	p := &clientstest.FakePeriscope{
		ListVodRetentionAssetsFn: func(context.Context, string, *periscope.TimeRangeOpts, *periscope.CursorPaginationOpts) (*periscopepb.ListVodRetentionAssetsResponse, error) {
			return &periscopepb.ListVodRetentionAssetsResponse{}, nil
		},
	}
	// No assets means the enrichment fan-out never touches Commodore (nil client
	// must not be dereferenced).
	got, err := periB(p).DoListVodRetentionAssets(clientstest.AuthedCtx("t1"), nil, nil, nil, nil, nil, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got.Nodes) != 0 || got.TotalCount != 0 {
		t.Fatalf("expected empty connection: %+v", got)
	}
}

func TestDoListVodRetentionAssets_TenantGuard(t *testing.T) {
	p := &clientstest.FakePeriscope{}
	if _, err := periB(p).DoListVodRetentionAssets(authedNoTenantCtx(), nil, nil, nil, nil, nil, nil); err == nil {
		t.Fatal("expected tenant required error")
	}
	if p.Calls != 0 {
		t.Fatalf("backend hit despite missing tenant: Calls=%d", p.Calls)
	}
}

// --- DoGetLiveNodeState (Periscope GetLiveNodes + Quartermaster ListMySubscriptions) ---

func liveNodeCtx(tenantID string) context.Context {
	ctx := clientstest.AuthedCtx(tenantID)
	// Resolver reads the user (not just the tenant key) to scope related tenants.
	return context.WithValue(ctx, ctxkeys.KeyUser, &middleware.UserContext{TenantID: tenantID})
}

func TestDoGetLiveNodeState_ReturnsFirstMatchingNode(t *testing.T) {
	var gotNodeID *string
	var gotRelated []string
	p := &clientstest.FakePeriscope{
		GetLiveNodesFn: func(_ context.Context, _ string, nodeID *string, relatedTenantIDs []string) (*periscopepb.GetLiveNodesResponse, error) {
			gotNodeID, gotRelated = nodeID, relatedTenantIDs
			return &periscopepb.GetLiveNodesResponse{
				Nodes: []*periscopepb.LiveNode{{NodeId: "node-1"}},
			}, nil
		},
	}
	owner := "owner-t"
	qm := &clientstest.FakeQuartermaster{
		ListMySubscriptionsFn: func(_ context.Context, _ *quartermasterpb.ListMySubscriptionsRequest) (*quartermasterpb.ListClustersResponse, error) {
			// A subscribed cluster owned by another tenant widens infra visibility.
			return &quartermasterpb.ListClustersResponse{
				Clusters: []*quartermasterpb.InfrastructureCluster{{OwnerTenantId: &owner}},
			}, nil
		},
	}
	r := &Resolver{
		Clients: clientstest.Clients(clientstest.WithPeriscope(p), clientstest.WithQuartermaster(qm)),
		Logger:  clientstest.DiscardLogger(),
	}
	got, err := r.DoGetLiveNodeState(liveNodeCtx("t1"), "node-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotNodeID == nil || *gotNodeID != "node-1" {
		t.Fatalf("nodeID not forwarded: %v", gotNodeID)
	}
	if len(gotRelated) != 1 || gotRelated[0] != "owner-t" {
		t.Fatalf("related tenant IDs not derived from subscriptions: %v", gotRelated)
	}
	if got == nil || got.NodeId != "node-1" {
		t.Fatalf("node not returned: %+v", got)
	}
}

func TestDoGetLiveNodeState_NoMatchReturnsNil(t *testing.T) {
	p := &clientstest.FakePeriscope{
		GetLiveNodesFn: func(context.Context, string, *string, []string) (*periscopepb.GetLiveNodesResponse, error) {
			return &periscopepb.GetLiveNodesResponse{}, nil // offline / not reporting
		},
	}
	qm := &clientstest.FakeQuartermaster{
		ListMySubscriptionsFn: func(context.Context, *quartermasterpb.ListMySubscriptionsRequest) (*quartermasterpb.ListClustersResponse, error) {
			return &quartermasterpb.ListClustersResponse{}, nil
		},
	}
	r := &Resolver{
		Clients: clientstest.Clients(clientstest.WithPeriscope(p), clientstest.WithQuartermaster(qm)),
		Logger:  clientstest.DiscardLogger(),
	}
	got, err := r.DoGetLiveNodeState(liveNodeCtx("t1"), "node-x")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil node for no match, got %+v", got)
	}
}

func TestDoGetLiveNodeState_NoUserRejected(t *testing.T) {
	p := &clientstest.FakePeriscope{}
	// authedNoTenantCtx passes RequirePermission but has no UserContext.
	if _, err := periB(p).DoGetLiveNodeState(authedNoTenantCtx(), "node-1"); err == nil {
		t.Fatal("expected tenant context required error")
	}
	if p.Calls != 0 {
		t.Fatalf("backend hit despite missing user: Calls=%d", p.Calls)
	}
}
