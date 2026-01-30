package resources

import (
	"context"
	"fmt"
	"time"

	"frameworks/api_gateway/internal/clients"
	"frameworks/api_gateway/internal/resolvers"
	"frameworks/pkg/clients/periscope"
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
	tenantID, ok := ctx.Value("tenant_id").(string)
	if !ok || tenantID == "" {
		return nil, fmt.Errorf("not authenticated")
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
	tenantID, ok := ctx.Value("tenant_id").(string)
	if !ok || tenantID == "" {
		return nil, fmt.Errorf("not authenticated")
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
	tenantID, ok := ctx.Value("tenant_id").(string)
	if !ok || tenantID == "" {
		return nil, fmt.Errorf("not authenticated")
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
