package clientstest

// FakePeriscope analytics/MV-backed read methods (generated to match
// pkg/clients/periscope.Interface). Each unstubbed method panics so a test that
// forgot to stub a backend call fails loudly instead of getting a zero value.

import (
	"context"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/periscope"
	periscopepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/periscope"
)

func (f *FakePeriscope) GetAPIUsage(ctx context.Context, tenantID string, authType *string, operationType *string, operationName *string, timeRange *periscope.TimeRangeOpts, opts *periscope.CursorPaginationOpts, summaryOnly bool) (*periscopepb.GetAPIUsageResponse, error) {
	f.Calls++
	if f.GetAPIUsageFn == nil {
		panic("FakePeriscope.GetAPIUsage not stubbed")
	}
	return f.GetAPIUsageFn(ctx, tenantID, authType, operationType, operationName, timeRange, opts, summaryOnly)
}

func (f *FakePeriscope) GetArtifactState(ctx context.Context, tenantID string, requestID string) (*periscopepb.GetArtifactStateResponse, error) {
	f.Calls++
	if f.GetArtifactStateFn == nil {
		panic("FakePeriscope.GetArtifactState not stubbed")
	}
	return f.GetArtifactStateFn(ctx, tenantID, requestID)
}

func (f *FakePeriscope) GetArtifactStates(ctx context.Context, tenantID string, streamID *string, contentType *string, stage *string, opts *periscope.CursorPaginationOpts) (*periscopepb.GetArtifactStatesResponse, error) {
	f.Calls++
	if f.GetArtifactStatesFn == nil {
		panic("FakePeriscope.GetArtifactStates not stubbed")
	}
	return f.GetArtifactStatesFn(ctx, tenantID, streamID, contentType, stage, opts)
}

func (f *FakePeriscope) GetBufferEvents(ctx context.Context, tenantID string, streamID string, timeRange *periscope.TimeRangeOpts, opts *periscope.CursorPaginationOpts) (*periscopepb.GetBufferEventsResponse, error) {
	f.Calls++
	if f.GetBufferEventsFn == nil {
		panic("FakePeriscope.GetBufferEvents not stubbed")
	}
	return f.GetBufferEventsFn(ctx, tenantID, streamID, timeRange, opts)
}

func (f *FakePeriscope) GetClientQoeSummary(ctx context.Context, tenantID string, streamID *string, timeRange *periscope.TimeRangeOpts) (*periscopepb.GetClientQoeSummaryResponse, error) {
	f.Calls++
	if f.GetClientQoeSummaryFn == nil {
		panic("FakePeriscope.GetClientQoeSummary not stubbed")
	}
	return f.GetClientQoeSummaryFn(ctx, tenantID, streamID, timeRange)
}

func (f *FakePeriscope) GetClipEvents(ctx context.Context, tenantID string, streamID *string, stage *string, contentType *string, timeRange *periscope.TimeRangeOpts, opts *periscope.CursorPaginationOpts) (*periscopepb.GetClipEventsResponse, error) {
	f.Calls++
	if f.GetClipEventsFn == nil {
		panic("FakePeriscope.GetClipEvents not stubbed")
	}
	return f.GetClipEventsFn(ctx, tenantID, streamID, stage, contentType, timeRange, opts)
}

func (f *FakePeriscope) GetClusterBootOps(ctx context.Context, tenantID string, clusterIDs []string, timeRange *periscope.TimeRangeOpts) (*periscopepb.GetClusterBootOpsResponse, error) {
	f.Calls++
	if f.GetClusterBootOpsFn == nil {
		panic("FakePeriscope.GetClusterBootOps not stubbed")
	}
	return f.GetClusterBootOpsFn(ctx, tenantID, clusterIDs, timeRange)
}

func (f *FakePeriscope) GetClusterQoeOps(ctx context.Context, tenantID string, clusterIDs []string, timeRange *periscope.TimeRangeOpts) (*periscopepb.GetClusterQoeOpsResponse, error) {
	f.Calls++
	if f.GetClusterQoeOpsFn == nil {
		panic("FakePeriscope.GetClusterQoeOps not stubbed")
	}
	return f.GetClusterQoeOpsFn(ctx, tenantID, clusterIDs, timeRange)
}

func (f *FakePeriscope) GetClusterTrafficMatrix(ctx context.Context, tenantID string, timeRange *periscope.TimeRangeOpts) (*periscopepb.GetClusterTrafficMatrixResponse, error) {
	f.Calls++
	if f.GetClusterTrafficMatrixFn == nil {
		panic("FakePeriscope.GetClusterTrafficMatrix not stubbed")
	}
	return f.GetClusterTrafficMatrixFn(ctx, tenantID, timeRange)
}

func (f *FakePeriscope) GetFederationEvents(ctx context.Context, tenantID string, timeRange *periscope.TimeRangeOpts, eventType *string, limit int32) (*periscopepb.GetFederationEventsResponse, error) {
	f.Calls++
	if f.GetFederationEventsFn == nil {
		panic("FakePeriscope.GetFederationEvents not stubbed")
	}
	return f.GetFederationEventsFn(ctx, tenantID, timeRange, eventType, limit)
}

func (f *FakePeriscope) GetFederationSummary(ctx context.Context, tenantID string, timeRange *periscope.TimeRangeOpts) (*periscopepb.GetFederationSummaryResponse, error) {
	f.Calls++
	if f.GetFederationSummaryFn == nil {
		panic("FakePeriscope.GetFederationSummary not stubbed")
	}
	return f.GetFederationSummaryFn(ctx, tenantID, timeRange)
}

func (f *FakePeriscope) GetLiveUsageSummary(ctx context.Context, tenantID string, timeRange *periscope.TimeRangeOpts) (*periscopepb.GetLiveUsageSummaryResponse, error) {
	f.Calls++
	if f.GetLiveUsageSummaryFn == nil {
		panic("FakePeriscope.GetLiveUsageSummary not stubbed")
	}
	return f.GetLiveUsageSummaryFn(ctx, tenantID, timeRange)
}

func (f *FakePeriscope) GetNodeMetricsAggregated(ctx context.Context, tenantID string, nodeID *string, timeRange *periscope.TimeRangeOpts) (*periscopepb.GetNodeMetricsAggregatedResponse, error) {
	f.Calls++
	if f.GetNodeMetricsAggregatedFn == nil {
		panic("FakePeriscope.GetNodeMetricsAggregated not stubbed")
	}
	return f.GetNodeMetricsAggregatedFn(ctx, tenantID, nodeID, timeRange)
}

func (f *FakePeriscope) GetOrchestrator(ctx context.Context, tenantID, orchAddr string) (*periscopepb.GetOrchestratorResponse, error) {
	f.Calls++
	if f.GetOrchestratorFn == nil {
		panic("FakePeriscope.GetOrchestrator not stubbed")
	}
	return f.GetOrchestratorFn(ctx, tenantID, orchAddr)
}

func (f *FakePeriscope) GetOrchestratorPerformanceSeries(ctx context.Context, tenantID, orchAddr string, timeRange *periscope.TimeRangeOpts, interval *string, gatewayID, resolvedIP *string) (*periscopepb.GetOrchestratorPerformanceSeriesResponse, error) {
	f.Calls++
	if f.GetOrchestratorPerformanceSeriesFn == nil {
		panic("FakePeriscope.GetOrchestratorPerformanceSeries not stubbed")
	}
	return f.GetOrchestratorPerformanceSeriesFn(ctx, tenantID, orchAddr, timeRange, interval, gatewayID, resolvedIP)
}

func (f *FakePeriscope) GetPlayerBootSummary(ctx context.Context, tenantID string, streamID *string, artifactHash *string, timeRange *periscope.TimeRangeOpts) (*periscopepb.GetPlayerBootSummaryResponse, error) {
	f.Calls++
	if f.GetPlayerBootSummaryFn == nil {
		panic("FakePeriscope.GetPlayerBootSummary not stubbed")
	}
	return f.GetPlayerBootSummaryFn(ctx, tenantID, streamID, artifactHash, timeRange)
}

func (f *FakePeriscope) GetPlayerBootTimeSeries(ctx context.Context, tenantID string, streamID *string, artifactHash *string, timeRange *periscope.TimeRangeOpts, interval string) (*periscopepb.GetPlayerBootTimeSeriesResponse, error) {
	f.Calls++
	if f.GetPlayerBootTimeSeriesFn == nil {
		panic("FakePeriscope.GetPlayerBootTimeSeries not stubbed")
	}
	return f.GetPlayerBootTimeSeriesFn(ctx, tenantID, streamID, artifactHash, timeRange, interval)
}

func (f *FakePeriscope) GetProcessingUsage(ctx context.Context, tenantID string, streamID *string, processType *string, timeRange *periscope.TimeRangeOpts, opts *periscope.CursorPaginationOpts, summaryOnly bool) (*periscopepb.GetProcessingUsageResponse, error) {
	f.Calls++
	if f.GetProcessingUsageFn == nil {
		panic("FakePeriscope.GetProcessingUsage not stubbed")
	}
	return f.GetProcessingUsageFn(ctx, tenantID, streamID, processType, timeRange, opts, summaryOnly)
}

func (f *FakePeriscope) GetQualityTierDaily(ctx context.Context, tenantID string, streamID *string, timeRange *periscope.TimeRangeOpts, opts *periscope.CursorPaginationOpts) (*periscopepb.GetQualityTierDailyResponse, error) {
	f.Calls++
	if f.GetQualityTierDailyFn == nil {
		panic("FakePeriscope.GetQualityTierDaily not stubbed")
	}
	return f.GetQualityTierDailyFn(ctx, tenantID, streamID, timeRange, opts)
}

func (f *FakePeriscope) GetRoutingEfficiency(ctx context.Context, tenantID string, streamID *string, timeRange *periscope.TimeRangeOpts) (*periscopepb.GetRoutingEfficiencyResponse, error) {
	f.Calls++
	if f.GetRoutingEfficiencyFn == nil {
		panic("FakePeriscope.GetRoutingEfficiency not stubbed")
	}
	return f.GetRoutingEfficiencyFn(ctx, tenantID, streamID, timeRange)
}

func (f *FakePeriscope) GetSessionQoeSummary(ctx context.Context, tenantID string, streamID *string, artifactHash *string, timeRange *periscope.TimeRangeOpts) (*periscopepb.GetSessionQoeSummaryResponse, error) {
	f.Calls++
	if f.GetSessionQoeSummaryFn == nil {
		panic("FakePeriscope.GetSessionQoeSummary not stubbed")
	}
	return f.GetSessionQoeSummaryFn(ctx, tenantID, streamID, artifactHash, timeRange)
}

func (f *FakePeriscope) GetSessionQoeTimeSeries(ctx context.Context, tenantID string, streamID *string, artifactHash *string, timeRange *periscope.TimeRangeOpts, interval string) (*periscopepb.GetSessionQoeTimeSeriesResponse, error) {
	f.Calls++
	if f.GetSessionQoeTimeSeriesFn == nil {
		panic("FakePeriscope.GetSessionQoeTimeSeries not stubbed")
	}
	return f.GetSessionQoeTimeSeriesFn(ctx, tenantID, streamID, artifactHash, timeRange, interval)
}

func (f *FakePeriscope) GetStorageEvents(ctx context.Context, tenantID string, streamID *string, assetType *string, timeRange *periscope.TimeRangeOpts, opts *periscope.CursorPaginationOpts) (*periscopepb.GetStorageEventsResponse, error) {
	f.Calls++
	if f.GetStorageEventsFn == nil {
		panic("FakePeriscope.GetStorageEvents not stubbed")
	}
	return f.GetStorageEventsFn(ctx, tenantID, streamID, assetType, timeRange, opts)
}

func (f *FakePeriscope) GetStorageUsage(ctx context.Context, tenantID string, nodeID *string, storageScope *string, timeRange *periscope.TimeRangeOpts, opts *periscope.CursorPaginationOpts) (*periscopepb.GetStorageUsageResponse, error) {
	f.Calls++
	if f.GetStorageUsageFn == nil {
		panic("FakePeriscope.GetStorageUsage not stubbed")
	}
	return f.GetStorageUsageFn(ctx, tenantID, nodeID, storageScope, timeRange, opts)
}

func (f *FakePeriscope) GetStreamAnalyticsDaily(ctx context.Context, tenantID string, streamID *string, timeRange *periscope.TimeRangeOpts, opts *periscope.CursorPaginationOpts) (*periscopepb.GetStreamAnalyticsDailyResponse, error) {
	f.Calls++
	if f.GetStreamAnalyticsDailyFn == nil {
		panic("FakePeriscope.GetStreamAnalyticsDaily not stubbed")
	}
	return f.GetStreamAnalyticsDailyFn(ctx, tenantID, streamID, timeRange, opts)
}

func (f *FakePeriscope) GetStreamAnalyticsSummaries(ctx context.Context, tenantID string, timeRange *periscope.TimeRangeOpts, sortBy periscope.StreamSummarySortField, sortOrder periscope.SortOrder, opts *periscope.CursorPaginationOpts) (*periscopepb.GetStreamAnalyticsSummariesResponse, error) {
	f.Calls++
	if f.GetStreamAnalyticsSummariesFn == nil {
		panic("FakePeriscope.GetStreamAnalyticsSummaries not stubbed")
	}
	return f.GetStreamAnalyticsSummariesFn(ctx, tenantID, timeRange, sortBy, sortOrder, opts)
}

func (f *FakePeriscope) GetStreamConnectionHourly(ctx context.Context, tenantID string, streamID *string, timeRange *periscope.TimeRangeOpts, opts *periscope.CursorPaginationOpts) (*periscopepb.GetStreamConnectionHourlyResponse, error) {
	f.Calls++
	if f.GetStreamConnectionHourlyFn == nil {
		panic("FakePeriscope.GetStreamConnectionHourly not stubbed")
	}
	return f.GetStreamConnectionHourlyFn(ctx, tenantID, streamID, timeRange, opts)
}

func (f *FakePeriscope) GetTenantAnalyticsDaily(ctx context.Context, tenantID string, timeRange *periscope.TimeRangeOpts, opts *periscope.CursorPaginationOpts) (*periscopepb.GetTenantAnalyticsDailyResponse, error) {
	f.Calls++
	if f.GetTenantAnalyticsDailyFn == nil {
		panic("FakePeriscope.GetTenantAnalyticsDaily not stubbed")
	}
	return f.GetTenantAnalyticsDailyFn(ctx, tenantID, timeRange, opts)
}

func (f *FakePeriscope) GetTenantDailyStats(ctx context.Context, tenantID string, days int32) (*periscopepb.GetTenantDailyStatsResponse, error) {
	f.Calls++
	if f.GetTenantDailyStatsFn == nil {
		panic("FakePeriscope.GetTenantDailyStats not stubbed")
	}
	return f.GetTenantDailyStatsFn(ctx, tenantID, days)
}

func (f *FakePeriscope) GetTrackListEvents(ctx context.Context, tenantID string, streamID string, timeRange *periscope.TimeRangeOpts, opts *periscope.CursorPaginationOpts) (*periscopepb.GetTrackListEventsResponse, error) {
	f.Calls++
	if f.GetTrackListEventsFn == nil {
		panic("FakePeriscope.GetTrackListEvents not stubbed")
	}
	return f.GetTrackListEventsFn(ctx, tenantID, streamID, timeRange, opts)
}

func (f *FakePeriscope) GetViewerGeoHourly(ctx context.Context, tenantID string, streamID *string, timeRange *periscope.TimeRangeOpts, opts *periscope.CursorPaginationOpts) (*periscopepb.GetViewerGeoHourlyResponse, error) {
	f.Calls++
	if f.GetViewerGeoHourlyFn == nil {
		panic("FakePeriscope.GetViewerGeoHourly not stubbed")
	}
	return f.GetViewerGeoHourlyFn(ctx, tenantID, streamID, timeRange, opts)
}

func (f *FakePeriscope) GetViewerHoursHourly(ctx context.Context, tenantID string, streamID *string, timeRange *periscope.TimeRangeOpts, opts *periscope.CursorPaginationOpts) (*periscopepb.GetViewerHoursHourlyResponse, error) {
	f.Calls++
	if f.GetViewerHoursHourlyFn == nil {
		panic("FakePeriscope.GetViewerHoursHourly not stubbed")
	}
	return f.GetViewerHoursHourlyFn(ctx, tenantID, streamID, timeRange, opts)
}

func (f *FakePeriscope) GetVodRetention(ctx context.Context, tenantID string, artifactHash string, timeRange *periscope.TimeRangeOpts) (*periscopepb.GetVodRetentionResponse, error) {
	f.Calls++
	if f.GetVodRetentionFn == nil {
		panic("FakePeriscope.GetVodRetention not stubbed")
	}
	return f.GetVodRetentionFn(ctx, tenantID, artifactHash, timeRange)
}

func (f *FakePeriscope) ListOrchestratorInstances(ctx context.Context, tenantID string, orchAddr *string) (*periscopepb.ListOrchestratorInstancesResponse, error) {
	f.Calls++
	if f.ListOrchestratorInstancesFn == nil {
		panic("FakePeriscope.ListOrchestratorInstances not stubbed")
	}
	return f.ListOrchestratorInstancesFn(ctx, tenantID, orchAddr)
}

func (f *FakePeriscope) ListOrchestrators(ctx context.Context, tenantID string, orchAddr *string, opts *periscope.CursorPaginationOpts) (*periscopepb.ListOrchestratorsResponse, error) {
	f.Calls++
	if f.ListOrchestratorsFn == nil {
		panic("FakePeriscope.ListOrchestrators not stubbed")
	}
	return f.ListOrchestratorsFn(ctx, tenantID, orchAddr, opts)
}

func (f *FakePeriscope) ListOrchestratorVantages(ctx context.Context, tenantID string, orchAddr *string) (*periscopepb.ListOrchestratorVantagesResponse, error) {
	f.Calls++
	if f.ListOrchestratorVantagesFn == nil {
		panic("FakePeriscope.ListOrchestratorVantages not stubbed")
	}
	return f.ListOrchestratorVantagesFn(ctx, tenantID, orchAddr)
}

func (f *FakePeriscope) ListVodRetentionAssets(ctx context.Context, tenantID string, timeRange *periscope.TimeRangeOpts, opts *periscope.CursorPaginationOpts) (*periscopepb.ListVodRetentionAssetsResponse, error) {
	f.Calls++
	if f.ListVodRetentionAssetsFn == nil {
		panic("FakePeriscope.ListVodRetentionAssets not stubbed")
	}
	return f.ListVodRetentionAssetsFn(ctx, tenantID, timeRange, opts)
}

func (f *FakePeriscope) GetNodePerformance5m(ctx context.Context, tenantID string, nodeID *string, timeRange *periscope.TimeRangeOpts, opts *periscope.CursorPaginationOpts) (*periscopepb.GetNodePerformance5MResponse, error) {
	f.Calls++
	if f.GetNodePerformance5mFn == nil {
		panic("FakePeriscope.GetNodePerformance5m not stubbed")
	}
	return f.GetNodePerformance5mFn(ctx, tenantID, nodeID, timeRange, opts)
}
