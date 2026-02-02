package graph

import (
	"frameworks/api_gateway/graph/generated"
	"frameworks/api_gateway/graph/model"
)

// DefaultPageSize is the default pagination size when none is specified.
// Matches the schema default of 50.
const DefaultPageSize = 50

// ConnectionBaseCost is the fixed cost for any connection field (Shopify uses 2).
const ConnectionBaseCost = 2

// getPageMultiplier extracts the pagination size from ConnectionInput.
// Returns DefaultPageSize if page is nil or neither first/last is set.
func getPageMultiplier(page *model.ConnectionInput) int {
	if page == nil {
		return DefaultPageSize
	}
	if page.First != nil && *page.First > 0 {
		return *page.First
	}
	if page.Last != nil && *page.Last > 0 {
		return *page.Last
	}
	return DefaultPageSize
}

// connectionComplexity calculates Shopify-style complexity for connection fields.
// Formula: ConnectionBaseCost + pageSize + childComplexity
// This is additive (not multiplicative) because childComplexity includes both
// per-item fields AND fixed fields (pageInfo, totalCount). Multiplying would
// over-count the fixed fields.
func connectionComplexity(childComplexity int, page *model.ConnectionInput) int {
	multiplier := getPageMultiplier(page)
	return ConnectionBaseCost + multiplier + childComplexity
}

// SetupComplexity configures pagination-aware complexity functions on the given
// ComplexityRoot. This follows Shopify's model where connections cost:
// base + (requested_items × child_complexity).
//
// See: https://shopify.engineering/rate-limiting-graphql-apis-calculating-query-complexity
func SetupComplexity(c *generated.ComplexityRoot) {
	// APIUsage connections
	c.APIUsage.APIUsageConnection = func(childComplexity int, page *model.ConnectionInput, _ *string, _ *string, _ *model.TimeRangeInput, _ *bool) int {
		return connectionComplexity(childComplexity, page)
	}

	// ProcessingUsage connections
	c.ProcessingUsage.ProcessingUsageConnection = func(childComplexity int, page *model.ConnectionInput, _ *string, _ *string, _ *model.TimeRangeInput, _ *bool) int {
		return connectionComplexity(childComplexity, page)
	}

	// StorageUsage connections
	c.StorageUsage.StorageUsageConnection = func(childComplexity int, page *model.ConnectionInput, _ *string, _ *string, _ *model.TimeRangeInput, _ *bool) int {
		return connectionComplexity(childComplexity, page)
	}

	// StreamingUsage connections
	c.StreamingUsage.QualityTierDailyConnection = func(childComplexity int, page *model.ConnectionInput, _ *string, _ *model.TimeRangeInput, _ *bool) int {
		return connectionComplexity(childComplexity, page)
	}
	c.StreamingUsage.StreamAnalyticsDailyConnection = func(childComplexity int, page *model.ConnectionInput, _ *string, _ *model.TimeRangeInput, _ *bool) int {
		return connectionComplexity(childComplexity, page)
	}
	c.StreamingUsage.StreamConnectionHourlyConnection = func(childComplexity int, page *model.ConnectionInput, _ *string, _ *model.TimeRangeInput, _ *bool) int {
		return connectionComplexity(childComplexity, page)
	}
	c.StreamingUsage.TenantAnalyticsDailyConnection = func(childComplexity int, page *model.ConnectionInput, _ *model.TimeRangeInput, _ *bool) int {
		return connectionComplexity(childComplexity, page)
	}
	c.StreamingUsage.ViewerGeoHourlyConnection = func(childComplexity int, page *model.ConnectionInput, _ *model.TimeRangeInput, _ *bool) int {
		return connectionComplexity(childComplexity, page)
	}
	c.StreamingUsage.ViewerGeographicsConnection = func(childComplexity int, page *model.ConnectionInput, _ *string, _ *model.TimeRangeInput) int {
		return connectionComplexity(childComplexity, page)
	}
	c.StreamingUsage.ViewerHoursHourlyConnection = func(childComplexity int, page *model.ConnectionInput, _ *string, _ *model.TimeRangeInput, _ *bool) int {
		return connectionComplexity(childComplexity, page)
	}
	c.StreamingUsage.ViewerTimeSeriesConnection = func(childComplexity int, page *model.ConnectionInput, _ string, _ *model.TimeRangeInput, _ *string) int {
		return connectionComplexity(childComplexity, page)
	}

	// AnalyticsHealth connections
	c.AnalyticsHealth.ClientQoeConnection = func(childComplexity int, page *model.ConnectionInput, _ *string, _ *string, _ *model.TimeRangeInput, _ *bool) int {
		return connectionComplexity(childComplexity, page)
	}
	c.AnalyticsHealth.RebufferingEventsConnection = func(childComplexity int, page *model.ConnectionInput, _ *string, _ *string, _ *model.TimeRangeInput, _ *bool) int {
		return connectionComplexity(childComplexity, page)
	}
	c.AnalyticsHealth.StreamHealth5mConnection = func(childComplexity int, page *model.ConnectionInput, _ string, _ *model.TimeRangeInput, _ *bool) int {
		return connectionComplexity(childComplexity, page)
	}
	c.AnalyticsHealth.StreamHealthConnection = func(childComplexity int, page *model.ConnectionInput, _ *string, _ *model.TimeRangeInput, _ *bool) int {
		return connectionComplexity(childComplexity, page)
	}

	// AnalyticsInfra connections
	c.AnalyticsInfra.NodeMetrics1hConnection = func(childComplexity int, page *model.ConnectionInput, _ *model.TimeRangeInput, _ *string, _ *bool) int {
		return connectionComplexity(childComplexity, page)
	}
	c.AnalyticsInfra.NodeMetricsConnection = func(childComplexity int, page *model.ConnectionInput, _ *string, _ *model.TimeRangeInput, _ *bool) int {
		return connectionComplexity(childComplexity, page)
	}
	c.AnalyticsInfra.NodePerformance5mConnection = func(childComplexity int, page *model.ConnectionInput, _ *string, _ *model.TimeRangeInput, _ *bool) int {
		return connectionComplexity(childComplexity, page)
	}
	c.AnalyticsInfra.RoutingEventsConnection = func(childComplexity int, page *model.ConnectionInput, _ *string, _ *model.TimeRangeInput, _ *string, _ *string, _ *bool) int {
		return connectionComplexity(childComplexity, page)
	}
	c.AnalyticsInfra.ServiceInstancesConnection = func(childComplexity int, page *model.ConnectionInput, _ *string, _ *string, _ *model.InstanceStatus) int {
		return connectionComplexity(childComplexity, page)
	}

	// AnalyticsLifecycle connections
	c.AnalyticsLifecycle.ArtifactEventsConnection = func(childComplexity int, page *model.ConnectionInput, _ *string, _ *string, _ *string, _ *model.TimeRangeInput, _ *bool) int {
		return connectionComplexity(childComplexity, page)
	}
	c.AnalyticsLifecycle.ArtifactStatesConnection = func(childComplexity int, page *model.ConnectionInput, _ *string, _ *string, _ *string) int {
		return connectionComplexity(childComplexity, page)
	}
	c.AnalyticsLifecycle.BufferEventsConnection = func(childComplexity int, page *model.ConnectionInput, _ string, _ *model.TimeRangeInput, _ *bool) int {
		return connectionComplexity(childComplexity, page)
	}
	c.AnalyticsLifecycle.ConnectionEventsConnection = func(childComplexity int, page *model.ConnectionInput, _ *string, _ *model.TimeRangeInput, _ *bool) int {
		return connectionComplexity(childComplexity, page)
	}
	c.AnalyticsLifecycle.StorageEventsConnection = func(childComplexity int, page *model.ConnectionInput, _ *string, _ *string, _ *model.TimeRangeInput, _ *bool) int {
		return connectionComplexity(childComplexity, page)
	}
	c.AnalyticsLifecycle.StreamEventsConnection = func(childComplexity int, page *model.ConnectionInput, _ string, _ *model.TimeRangeInput, _ *bool) int {
		return connectionComplexity(childComplexity, page)
	}
	c.AnalyticsLifecycle.TrackListConnection = func(childComplexity int, page *model.ConnectionInput, _ string, _ *model.TimeRangeInput, _ *bool) int {
		return connectionComplexity(childComplexity, page)
	}
	c.AnalyticsLifecycle.ViewerSessionsConnection = func(childComplexity int, page *model.ConnectionInput, _ *string, _ *model.TimeRangeInput, _ *bool) int {
		return connectionComplexity(childComplexity, page)
	}

	// Query connections (root-level)
	c.Query.BalanceTransactionsConnection = func(childComplexity int, page *model.ConnectionInput, _ *string, _ *model.TimeRangeInput) int {
		return connectionComplexity(childComplexity, page)
	}
	c.Query.BootstrapTokensConnection = func(childComplexity int, page *model.ConnectionInput, _ *string) int {
		return connectionComplexity(childComplexity, page)
	}
	c.Query.ClipsConnection = func(childComplexity int, page *model.ConnectionInput, _ *string) int {
		return connectionComplexity(childComplexity, page)
	}
	c.Query.ClusterInvitesConnection = func(childComplexity int, page *model.ConnectionInput, _ string) int {
		return connectionComplexity(childComplexity, page)
	}
	c.Query.ClustersAccessConnection = func(childComplexity int, page *model.ConnectionInput) int {
		return connectionComplexity(childComplexity, page)
	}
	c.Query.ClustersAvailableConnection = func(childComplexity int, page *model.ConnectionInput) int {
		return connectionComplexity(childComplexity, page)
	}
	c.Query.ClustersConnection = func(childComplexity int, page *model.ConnectionInput) int {
		return connectionComplexity(childComplexity, page)
	}
	c.Query.ConversationsConnection = func(childComplexity int, page *model.ConnectionInput) int {
		return connectionComplexity(childComplexity, page)
	}
	c.Query.DeveloperTokensConnection = func(childComplexity int, page *model.ConnectionInput) int {
		return connectionComplexity(childComplexity, page)
	}
	c.Query.DiscoverServicesConnection = func(childComplexity int, page *model.ConnectionInput, _ string, _ *string) int {
		return connectionComplexity(childComplexity, page)
	}
	c.Query.DvrRecordingsConnection = func(childComplexity int, page *model.ConnectionInput, _ *string) int {
		return connectionComplexity(childComplexity, page)
	}
	c.Query.InvoicesConnection = func(childComplexity int, page *model.ConnectionInput) int {
		return connectionComplexity(childComplexity, page)
	}
	c.Query.MarketplaceClustersConnection = func(childComplexity int, page *model.ConnectionInput) int {
		return connectionComplexity(childComplexity, page)
	}
	c.Query.MessagesConnection = func(childComplexity int, _ string, page *model.ConnectionInput) int {
		return connectionComplexity(childComplexity, page)
	}
	c.Query.MyClusterInvitesConnection = func(childComplexity int, page *model.ConnectionInput) int {
		return connectionComplexity(childComplexity, page)
	}
	c.Query.MySubscriptionsConnection = func(childComplexity int, page *model.ConnectionInput) int {
		return connectionComplexity(childComplexity, page)
	}
	c.Query.NodesConnection = func(childComplexity int, page *model.ConnectionInput, _ *string, _ *model.NodeStatus, _ *string) int {
		return connectionComplexity(childComplexity, page)
	}
	c.Query.PendingSubscriptionsConnection = func(childComplexity int, page *model.ConnectionInput, _ string) int {
		return connectionComplexity(childComplexity, page)
	}
	c.Query.StreamKeysConnection = func(childComplexity int, page *model.ConnectionInput, _ string) int {
		return connectionComplexity(childComplexity, page)
	}
	c.Query.StreamsConnection = func(childComplexity int, page *model.ConnectionInput) int {
		return connectionComplexity(childComplexity, page)
	}
	c.Query.UsageRecordsConnection = func(childComplexity int, page *model.ConnectionInput, _ *model.TimeRangeInput) int {
		return connectionComplexity(childComplexity, page)
	}
	c.Query.VodAssetsConnection = func(childComplexity int, page *model.ConnectionInput) int {
		return connectionComplexity(childComplexity, page)
	}

	// Cluster.nodesConnection
	c.Cluster.NodesConnection = func(childComplexity int, page *model.ConnectionInput) int {
		return connectionComplexity(childComplexity, page)
	}

	// InfrastructureNode.metrics connections
	c.InfrastructureNode.Metrics1hConnection = func(childComplexity int, page *model.ConnectionInput, _ *model.TimeRangeInput) int {
		return connectionComplexity(childComplexity, page)
	}
	c.InfrastructureNode.MetricsConnection = func(childComplexity int, page *model.ConnectionInput, _ *model.TimeRangeInput) int {
		return connectionComplexity(childComplexity, page)
	}
}
