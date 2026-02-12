package resources

import (
	"context"
	"time"

	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/mcp/mcperrors"
	"frameworks/api_gateway/internal/resolvers"
	"frameworks/pkg/clients/periscope"
	"frameworks/pkg/ctxkeys"
	"frameworks/pkg/logging"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// RegisterAnalyticsResources registers analytics-related MCP resources.
func RegisterAnalyticsResources(server *mcp.Server, clients *clients.ServiceClients, resolver *resolvers.Resolver, logger logging.Logger) {
	// analytics://usage - Usage aggregates
	server.AddResource(&mcp.Resource{
		URI:         "analytics://usage",
		Name:        "Usage Analytics",
		Description: "Usage aggregates for streaming, storage, and processing.",
		MIMEType:    "application/json",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return handleUsageAnalytics(ctx, clients, logger)
	})

	// analytics://viewers - Viewer metrics
	server.AddResource(&mcp.Resource{
		URI:         "analytics://viewers",
		Name:        "Viewer Analytics",
		Description: "Viewer metrics and trends.",
		MIMEType:    "application/json",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return handleViewerAnalytics(ctx, clients, logger)
	})

	// analytics://geographic - Geographic distribution
	server.AddResource(&mcp.Resource{
		URI:         "analytics://geographic",
		Name:        "Geographic Distribution",
		Description: "Geographic distribution of viewers.",
		MIMEType:    "application/json",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return handleGeographicAnalytics(ctx, clients, logger)
	})

	// analytics://routing - Routing efficiency + cross-cluster breakdown
	server.AddResource(&mcp.Resource{
		URI:         "analytics://routing",
		Name:        "Routing Analytics",
		Description: "Routing efficiency, cross-cluster traffic breakdown, and top cluster pairs.",
		MIMEType:    "application/json",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return handleRoutingAnalytics(ctx, clients, logger)
	})

	// analytics://federation - Federation operations summary
	server.AddResource(&mcp.Resource{
		URI:         "analytics://federation",
		Name:        "Federation Analytics",
		Description: "Origin-pull stats, peer health, query counts, and loop prevention.",
		MIMEType:    "application/json",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return handleFederationAnalytics(ctx, clients, logger)
	})

	// analytics://network-topology - Cluster topology and peer connections
	server.AddResource(&mcp.Resource{
		URI:         "analytics://network-topology",
		Name:        "Network Topology",
		Description: "Cluster topology, node locations, and peer connections.",
		MIMEType:    "application/json",
	}, func(ctx context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return handleNetworkTopology(ctx, clients, logger)
	})
}

// UsageAnalytics represents the analytics://usage response.
type UsageAnalytics struct {
	Period     string          `json:"period"`
	StartTime  string          `json:"start_time"`
	EndTime    string          `json:"end_time"`
	Streaming  StreamingUsage  `json:"streaming"`
	Storage    StorageUsage    `json:"storage"`
	Processing ProcessingUsage `json:"processing"`
}

// StreamingUsage contains streaming-related usage.
type StreamingUsage struct {
	IngestMinutes   int64 `json:"ingest_minutes"`
	DeliveryMinutes int64 `json:"delivery_minutes"`
	ViewerHours     int64 `json:"viewer_hours"`
	PeakConcurrent  int   `json:"peak_concurrent_viewers"`
}

// StorageUsage contains storage-related usage.
type StorageUsage struct {
	TotalBytes int64 `json:"total_bytes"`
	ClipsBytes int64 `json:"clips_bytes"`
	DVRBytes   int64 `json:"dvr_bytes"`
	VODBytes   int64 `json:"vod_bytes"`
}

// ProcessingUsage contains processing-related usage.
type ProcessingUsage struct {
	TranscodeMinutes int64 `json:"transcode_minutes"`
	ClipMinutes      int64 `json:"clip_minutes"`
}

func handleUsageAnalytics(ctx context.Context, clients *clients.ServiceClients, logger logging.Logger) (*mcp.ReadResourceResult, error) {
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, mcperrors.AuthRequired()
	}

	now := time.Now()
	startOfMonth := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())

	usage := UsageAnalytics{
		Period:     "current_month",
		StartTime:  startOfMonth.Format("2006-01-02T15:04:05Z"),
		EndTime:    now.Format("2006-01-02T15:04:05Z"),
		Streaming:  StreamingUsage{},
		Storage:    StorageUsage{},
		Processing: ProcessingUsage{},
	}

	// Get live usage summary from Periscope
	timeRange := &periscope.TimeRangeOpts{
		StartTime: startOfMonth,
		EndTime:   now,
	}

	resp, err := clients.Periscope.GetLiveUsageSummary(ctx, tenantID, timeRange)
	if err != nil {
		logger.WithError(err).Debug("Failed to get usage summary")
	} else if resp.Summary != nil {
		s := resp.Summary
		usage.Streaming.ViewerHours = int64(s.ViewerHours)
		usage.Streaming.PeakConcurrent = int(s.MaxViewers)
		usage.Storage.TotalBytes = int64(s.AverageStorageGb * 1024 * 1024 * 1024)
		// Sum all transcoding (Livepeer + Native AV) in minutes
		totalTranscodeSeconds := s.LivepeerH264Seconds + s.LivepeerVp9Seconds + s.LivepeerAv1Seconds + s.LivepeerHevcSeconds +
			s.NativeAvH264Seconds + s.NativeAvVp9Seconds + s.NativeAvAv1Seconds + s.NativeAvHevcSeconds
		usage.Processing.TranscodeMinutes = int64(totalTranscodeSeconds / 60)
	}

	return marshalResourceResult("analytics://usage", usage)
}

// ViewerAnalytics represents the analytics://viewers response.
type ViewerAnalytics struct {
	Period        string          `json:"period"`
	TotalViewers  int64           `json:"total_viewers"`
	UniqueViewers int64           `json:"unique_viewers"`
	TotalHours    float64         `json:"total_viewer_hours"`
	AvgWatchTime  float64         `json:"avg_watch_time_minutes"`
	HourlyTrend   []HourlyViewers `json:"hourly_trend"`
}

// HourlyViewers represents viewer count for an hour.
type HourlyViewers struct {
	Hour    string `json:"hour"`
	Viewers int    `json:"viewers"`
}

func handleViewerAnalytics(ctx context.Context, clients *clients.ServiceClients, logger logging.Logger) (*mcp.ReadResourceResult, error) {
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, mcperrors.AuthRequired()
	}

	analytics := ViewerAnalytics{
		Period:      "last_24h",
		HourlyTrend: []HourlyViewers{},
	}

	// Get viewer metrics from Periscope
	now := time.Now()
	start := now.Add(-24 * time.Hour)

	timeRange := &periscope.TimeRangeOpts{
		StartTime: start,
		EndTime:   now,
	}

	pagination := &periscope.CursorPaginationOpts{
		First: 24,
	}

	resp, err := clients.Periscope.GetViewerHoursHourly(ctx, tenantID, nil, timeRange, pagination)
	if err != nil {
		logger.WithError(err).Debug("Failed to get viewer hourly data")
	} else {
		for _, record := range resp.Records {
			sessionHours := float64(record.TotalSessionSeconds) / 3600
			analytics.TotalHours += sessionHours
			analytics.TotalViewers += int64(record.UniqueViewers)
			analytics.HourlyTrend = append(analytics.HourlyTrend, HourlyViewers{
				Hour:    record.Hour.AsTime().Format("2006-01-02T15:00:00Z"),
				Viewers: int(record.UniqueViewers),
			})
		}
		analytics.UniqueViewers = analytics.TotalViewers // Approximation
		if analytics.TotalViewers > 0 {
			analytics.AvgWatchTime = (analytics.TotalHours * 60) / float64(analytics.TotalViewers)
		}
	}

	return marshalResourceResult("analytics://viewers", analytics)
}

// GeographicAnalytics represents the analytics://geographic response.
type GeographicAnalytics struct {
	Period     string           `json:"period"`
	Countries  []CountryViewers `json:"countries"`
	TotalCount int              `json:"total_count"`
}

// CountryViewers represents viewer count by country.
type CountryViewers struct {
	Country     string  `json:"country"`
	CountryCode string  `json:"country_code"`
	Viewers     int     `json:"viewers"`
	Percentage  float64 `json:"percentage"`
}

func handleGeographicAnalytics(ctx context.Context, clients *clients.ServiceClients, logger logging.Logger) (*mcp.ReadResourceResult, error) {
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, mcperrors.AuthRequired()
	}

	analytics := GeographicAnalytics{
		Period:    "last_24h",
		Countries: []CountryViewers{},
	}

	// Get geographic data from Periscope
	now := time.Now()
	start := now.Add(-24 * time.Hour)

	timeRange := &periscope.TimeRangeOpts{
		StartTime: start,
		EndTime:   now,
	}

	resp, err := clients.Periscope.GetGeographicDistribution(ctx, tenantID, nil, timeRange, 10)
	if err != nil {
		logger.WithError(err).Debug("Failed to get geographic data")
	} else {
		for _, c := range resp.TopCountries {
			analytics.Countries = append(analytics.Countries, CountryViewers{
				Country:     c.CountryCode, // CountryMetric only has code
				CountryCode: c.CountryCode,
				Viewers:     int(c.ViewerCount),
				Percentage:  float64(c.Percentage),
			})
		}
		analytics.TotalCount = int(resp.TotalViewers)
	}

	return marshalResourceResult("analytics://geographic", analytics)
}

// RoutingAnalytics represents the analytics://routing response.
type RoutingAnalytics struct {
	Period         string             `json:"period"`
	TotalDecisions int64              `json:"total_decisions"`
	SuccessRate    float64            `json:"success_rate"`
	AvgLatencyMs   float64            `json:"avg_latency_ms"`
	AvgDistanceKm  float64            `json:"avg_routing_distance_km"`
	ClusterPairs   []ClusterPairStats `json:"cluster_pairs"`
}

type ClusterPairStats struct {
	Source      string  `json:"source_cluster"`
	Remote      string  `json:"remote_cluster"`
	EventCount  uint64  `json:"event_count"`
	SuccessRate float64 `json:"success_rate"`
	AvgLatency  float64 `json:"avg_latency_ms"`
}

func handleRoutingAnalytics(ctx context.Context, svcClients *clients.ServiceClients, logger logging.Logger) (*mcp.ReadResourceResult, error) {
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, mcperrors.AuthRequired()
	}

	now := time.Now()
	start := now.Add(-24 * time.Hour)
	timeRange := &periscope.TimeRangeOpts{StartTime: start, EndTime: now}

	analytics := RoutingAnalytics{
		Period:       "last_24h",
		ClusterPairs: []ClusterPairStats{},
	}

	// Routing efficiency
	effResp, err := svcClients.Periscope.GetRoutingEfficiency(ctx, tenantID, nil, timeRange)
	if err != nil {
		logger.WithError(err).Debug("Failed to get routing efficiency")
	} else if effResp.Summary != nil {
		analytics.TotalDecisions = effResp.Summary.TotalDecisions
		analytics.SuccessRate = effResp.Summary.SuccessRate
		analytics.AvgLatencyMs = effResp.Summary.AvgLatencyMs
		analytics.AvgDistanceKm = effResp.Summary.AvgRoutingDistance
	}

	// Cluster traffic matrix
	matrixResp, err := svcClients.Periscope.GetClusterTrafficMatrix(ctx, tenantID, timeRange)
	if err != nil {
		logger.WithError(err).Debug("Failed to get cluster traffic matrix")
	} else {
		for _, pair := range matrixResp.Pairs {
			analytics.ClusterPairs = append(analytics.ClusterPairs, ClusterPairStats{
				Source:      pair.ClusterId,
				Remote:      pair.RemoteClusterId,
				EventCount:  pair.EventCount,
				SuccessRate: pair.SuccessRate,
				AvgLatency:  pair.AvgLatencyMs,
			})
		}
	}

	return marshalResourceResult("analytics://routing", analytics)
}

// FederationAnalytics represents the analytics://federation response.
type FederationAnalytics struct {
	Period             string               `json:"period"`
	TotalEvents        uint64               `json:"total_events"`
	OverallFailureRate float64              `json:"overall_failure_rate"`
	OverallAvgLatency  float64              `json:"overall_avg_latency_ms"`
	EventBreakdown     []EventTypeBreakdown `json:"event_breakdown"`
}

type EventTypeBreakdown struct {
	Type         string  `json:"event_type"`
	Count        uint64  `json:"count"`
	FailureCount uint64  `json:"failure_count"`
	AvgLatencyMs float64 `json:"avg_latency_ms"`
}

func handleFederationAnalytics(ctx context.Context, svcClients *clients.ServiceClients, logger logging.Logger) (*mcp.ReadResourceResult, error) {
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, mcperrors.AuthRequired()
	}

	now := time.Now()
	start := now.Add(-24 * time.Hour)
	timeRange := &periscope.TimeRangeOpts{StartTime: start, EndTime: now}

	analytics := FederationAnalytics{
		Period:         "last_24h",
		EventBreakdown: []EventTypeBreakdown{},
	}

	resp, err := svcClients.Periscope.GetFederationSummary(ctx, tenantID, timeRange)
	if err != nil {
		logger.WithError(err).Debug("Failed to get federation summary")
	} else if resp.Summary != nil {
		analytics.TotalEvents = resp.Summary.TotalEvents
		analytics.OverallFailureRate = resp.Summary.OverallFailureRate
		analytics.OverallAvgLatency = resp.Summary.OverallAvgLatencyMs
		for _, ec := range resp.Summary.EventCounts {
			analytics.EventBreakdown = append(analytics.EventBreakdown, EventTypeBreakdown{
				Type:         ec.EventType,
				Count:        ec.Count,
				FailureCount: ec.FailureCount,
				AvgLatencyMs: ec.AvgLatencyMs,
			})
		}
	}

	return marshalResourceResult("analytics://federation", analytics)
}

// NetworkTopology represents the analytics://network-topology response.
type NetworkTopology struct {
	Clusters        []TopologyCluster    `json:"clusters"`
	PeerConnections []TopologyConnection `json:"peer_connections"`
	TotalNodes      int                  `json:"total_nodes"`
	HealthyNodes    int                  `json:"healthy_nodes"`
}

type TopologyCluster struct {
	ID               string  `json:"cluster_id"`
	Name             string  `json:"name"`
	Region           string  `json:"region"`
	Latitude         float64 `json:"latitude"`
	Longitude        float64 `json:"longitude"`
	NodeCount        int     `json:"node_count"`
	HealthyNodeCount int     `json:"healthy_node_count"`
	PeerCount        int     `json:"peer_count"`
	Status           string  `json:"status"`
}

type TopologyConnection struct {
	Source    string `json:"source_cluster"`
	Target    string `json:"target_cluster"`
	Connected bool   `json:"connected"`
}

func handleNetworkTopology(ctx context.Context, _ *clients.ServiceClients, _ logging.Logger) (*mcp.ReadResourceResult, error) {
	tenantID := ctxkeys.GetTenantID(ctx)
	if tenantID == "" {
		return nil, mcperrors.AuthRequired()
	}

	// For now, return static topology from demo data.
	// In production, this would query PeerManager state or a topology table.
	topology := NetworkTopology{
		Clusters: []TopologyCluster{
			{ID: "central-primary", Name: "Central Primary", Region: "US-Central", Latitude: 41.8781, Longitude: -87.6298, NodeCount: 4, HealthyNodeCount: 4, PeerCount: 2, Status: "operational"},
			{ID: "us-east-edge", Name: "US East Edge", Region: "US-East", Latitude: 40.7128, Longitude: -74.0060, NodeCount: 3, HealthyNodeCount: 3, PeerCount: 1, Status: "operational"},
			{ID: "apac-edge", Name: "APAC Edge", Region: "AP-Northeast", Latitude: 35.6762, Longitude: 139.6503, NodeCount: 2, HealthyNodeCount: 2, PeerCount: 1, Status: "operational"},
		},
		PeerConnections: []TopologyConnection{
			{Source: "central-primary", Target: "us-east-edge", Connected: true},
			{Source: "central-primary", Target: "apac-edge", Connected: true},
		},
		TotalNodes:   9,
		HealthyNodes: 9,
	}

	return marshalResourceResult("analytics://network-topology", topology)
}
