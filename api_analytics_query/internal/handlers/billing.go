package handlers

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"frameworks/pkg/api/periscope"
	"frameworks/pkg/api/purser"
	pclient "frameworks/pkg/clients/purser"
	qmclient "frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/database"
	"frameworks/pkg/logging"
	"frameworks/pkg/models"

	"github.com/gin-gonic/gin"
)

// BillingSummarizer handles usage summarization for billing
type BillingSummarizer struct {
	yugaDB              database.PostgresConn
	clickhouse          database.ClickHouseConn
	logger              logging.Logger
	purserClient        *pclient.Client
	quartermasterClient *qmclient.Client
}

// NewBillingSummarizer creates a new billing summarizer instance
func NewBillingSummarizer(yugaDB database.PostgresConn, clickhouse database.ClickHouseConn, logger logging.Logger) *BillingSummarizer {
	purserURL := os.Getenv("PURSER_URL")
	if purserURL == "" {
		purserURL = "http://localhost:18003"
	}

	quartermasterURL := os.Getenv("QUARTERMASTER_URL")
	if quartermasterURL == "" {
		quartermasterURL = "http://localhost:18002"
	}

	serviceToken := os.Getenv("SERVICE_TOKEN")

	purserClient := pclient.NewClient(pclient.Config{
		BaseURL:      purserURL,
		ServiceToken: serviceToken,
		Timeout:      30 * time.Second,
		Logger:       logger,
	})

	quartermasterClient := qmclient.NewClient(qmclient.Config{
		BaseURL:      quartermasterURL,
		ServiceToken: serviceToken,
		Timeout:      10 * time.Second,
		Logger:       logger,
	})

	return &BillingSummarizer{
		yugaDB:              yugaDB,
		clickhouse:          clickhouse,
		logger:              logger,
		purserClient:        purserClient,
		quartermasterClient: quartermasterClient,
	}
}

// SummarizeUsageForPeriod aggregates usage data for all tenants for a given time period
func (bs *BillingSummarizer) SummarizeUsageForPeriod(startTime, endTime time.Time) error {
	bs.logger.WithFields(logging.Fields{
		"start_time": startTime,
		"end_time":   endTime,
	}).Info("Starting usage summarization for period")

	// Get all active tenants
	tenants, err := bs.getActiveTenants()
	if err != nil {
		return fmt.Errorf("failed to get active tenants: %w", err)
	}

	var summaries []models.UsageSummary

	// Generate usage summary for each tenant
	for _, tenantID := range tenants {
		summary, err := bs.generateTenantUsageSummary(tenantID, startTime, endTime)
		if err != nil {
			bs.logger.WithError(err).WithField("tenant_id", tenantID).Error("Failed to generate usage summary for tenant")
			continue
		}

		if summary != nil {
			summaries = append(summaries, *summary)
		}
	}

	// Send summaries to Purser
	if len(summaries) > 0 {
		err = bs.sendUsageToPurser(summaries)
		if err != nil {
			return fmt.Errorf("failed to send usage to Purser: %w", err)
		}

		bs.logger.WithField("summary_count", len(summaries)).Info("Successfully sent usage summaries to Purser")
	}

	return nil
}

// getActiveTenants retrieves all active tenant IDs from the analytics data
func (bs *BillingSummarizer) getActiveTenants() ([]string, error) {
	// Query ClickHouse for active tenants
	rows, err := bs.clickhouse.QueryContext(context.Background(), `
		SELECT DISTINCT tenant_id
		FROM viewer_metrics
		WHERE timestamp >= NOW() - INTERVAL 7 DAY
		AND tenant_id IS NOT NULL
		AND tenant_id != '00000000-0000-0000-0000-000000000000'
		ORDER BY tenant_id
	`)

	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tenants []string
	for rows.Next() {
		var tenantID string
		if err := rows.Scan(&tenantID); err != nil {
			bs.logger.WithError(err).Error("Failed to scan tenant ID")
			continue
		}
		tenants = append(tenants, tenantID)
	}

	return tenants, nil
}

// generateTenantUsageSummary creates a usage summary for a specific tenant and time period
func (bs *BillingSummarizer) generateTenantUsageSummary(tenantID string, startTime, endTime time.Time) (*models.UsageSummary, error) {
	ctx := context.Background()

	// Get tenant's primary cluster ID from Quartermaster API (not direct DB access!)
	clusterID, err := bs.getTenantPrimaryCluster(tenantID)
	if err != nil {
		bs.logger.WithError(err).WithField("tenant_id", tenantID).Warn("Failed to get tenant cluster info, using default")
		clusterID = "global-primary" // Default fallback
	}

	// Derive viewer-based metrics from viewer_metrics
	var maxViewers, totalStreams int
	var streamHours float64
	err = bs.clickhouse.QueryRowContext(ctx, `
		SELECT 
			COALESCE(max(viewer_count), 0) as max_viewers,
			COALESCE(uniq(internal_name), 0) as total_streams,
			COALESCE(countDistinct(concat(internal_name, toString(toStartOfHour(timestamp)))), 0) as stream_hours
		FROM viewer_metrics 
		WHERE tenant_id = $1 
		AND timestamp BETWEEN $2 AND $3
	`, tenantID, startTime, endTime).Scan(
		&maxViewers, &totalStreams, &streamHours,
	)
	if err != nil && err != database.ErrNoRows {
		return nil, fmt.Errorf("failed to query viewer metrics from ClickHouse: %w", err)
	}

	// Derive bandwidth metrics from stream_health_metrics
	var egressGB, peakBandwidth float64
	err = bs.clickhouse.QueryRowContext(ctx, `
		SELECT 
			COALESCE(sum(bandwidth_out) / (1024*1024*1024), 0) as egress_gb,
			COALESCE(max(bandwidth_out) / (1024*1024), 0) as peak_bandwidth_mbps
		FROM stream_health_metrics 
		WHERE tenant_id = $1 
		AND timestamp BETWEEN $2 AND $3
	`, tenantID, startTime, endTime).Scan(&egressGB, &peakBandwidth)
	if err != nil && err != database.ErrNoRows {
		return nil, fmt.Errorf("failed to query health metrics from ClickHouse: %w", err)
	}

	// Query ClickHouse for unique users (from connection events)
	var uniqueUsers int
	err = bs.clickhouse.QueryRowContext(ctx, `
		SELECT COALESCE(uniq(user_id), 0) as unique_users
		FROM connection_events 
		WHERE tenant_id = $1 
		AND timestamp BETWEEN $2 AND $3
	`, tenantID, startTime, endTime).Scan(&uniqueUsers)
	if err != nil && err != database.ErrNoRows {
		bs.logger.WithError(err).Warn("Failed to query unique users from ClickHouse, defaulting to 0")
		uniqueUsers = 0
	}

	// Derive clip storage additions from clip_events (stage='done')
	var clipStorageAddedGB float64
	var clipsAdded int
	err = bs.clickhouse.QueryRowContext(ctx, `
		SELECT 
			COALESCE(sumIf(size_bytes, stage = 'done') / (1024*1024*1024), 0) as clip_storage_added_gb,
			COALESCE(countIf(stage = 'done'), 0) as clips_added
		FROM clip_events 
		WHERE tenant_id = $1 
		AND timestamp BETWEEN $2 AND $3
	`, tenantID, startTime, endTime).Scan(&clipStorageAddedGB, &clipsAdded)
	if err != nil && err != database.ErrNoRows {
		bs.logger.WithError(err).Debug("Failed to query clip events for storage additions, defaulting to 0")
		clipStorageAddedGB = 0
		clipsAdded = 0
	}

	// Clip deletions and current storage footprint require delete events or storage index; default to 0 when absent
	clipsDeleted := 0
	clipStorageDeletedGB := float64(0)
	storageGB := float64(0)

	// Derive recording usage from stream_events (recording_lifecycle) in ClickHouse
	var recordingGB float64
	err = bs.clickhouse.QueryRowContext(ctx, `
		SELECT COALESCE(sum(file_size) / (1024*1024*1024), 0) as recording_gb
		FROM stream_events 
		WHERE tenant_id = $1 
		AND event_type = 'recording-lifecycle'
		AND timestamp BETWEEN $2 AND $3
	`, tenantID, startTime, endTime).Scan(&recordingGB)
	if err != nil && err != database.ErrNoRows {
		bs.logger.WithError(err).Debug("Failed to query recording data from ClickHouse, defaulting to 0")
		recordingGB = 0
	}

	// Skip if no usage data
	if streamHours == 0 && egressGB == 0 && maxViewers == 0 && totalStreams == 0 {
		bs.logger.WithField("tenant_id", tenantID).Debug("No usage data for tenant in period, skipping")
		return nil, nil
	}

	summary := &models.UsageSummary{
		TenantID:          tenantID,
		ClusterID:         clusterID,
		Period:            fmt.Sprintf("%s/%s", startTime.Format(time.RFC3339), endTime.Format(time.RFC3339)),
		StreamHours:       streamHours,
		EgressGB:          egressGB,
		MaxViewers:        maxViewers,
		TotalStreams:      totalStreams,
		PeakBandwidthMbps: peakBandwidth,
		UniqueUsers:       uniqueUsers,
		RecordingGB:       recordingGB,
		// Storage and clip lifecycle
		StorageGB:            storageGB,
		ClipsAdded:           clipsAdded,
		ClipsDeleted:         clipsDeleted,
		ClipStorageAddedGB:   clipStorageAddedGB,
		ClipStorageDeletedGB: clipStorageDeletedGB,
		Timestamp:            time.Now(),
	}

	bs.logger.WithFields(logging.Fields{
		"tenant_id":     tenantID,
		"stream_hours":  streamHours,
		"egress_gb":     egressGB,
		"max_viewers":   maxViewers,
		"total_streams": totalStreams,
	}).Debug("Generated usage summary for tenant")

	return summary, nil
}

// getTenantPrimaryCluster gets tenant's primary cluster by calling Quartermaster API
func (bs *BillingSummarizer) getTenantPrimaryCluster(tenantID string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tenantResp, err := bs.quartermasterClient.GetTenant(ctx, tenantID)
	if err != nil {
		return "", fmt.Errorf("failed to call Quartermaster: %w", err)
	}

	if tenantResp.Error != "" {
		return "", fmt.Errorf("Quartermaster returned error: %s", tenantResp.Error)
	}

	if tenantResp.Tenant != nil && tenantResp.Tenant.PrimaryClusterID != nil && *tenantResp.Tenant.PrimaryClusterID != "" {
		return *tenantResp.Tenant.PrimaryClusterID, nil
	}

	return "global-primary", nil // Default fallback when no primary cluster is set
}

// sendUsageToPurser sends usage summaries to the Purser billing service
func (bs *BillingSummarizer) sendUsageToPurser(summaries []models.UsageSummary) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req := &purser.UsageIngestRequest{
		UsageSummaries: summaries,
		Source:         "periscope",
		Timestamp:      time.Now().Unix(),
	}

	resp, err := bs.purserClient.IngestUsage(ctx, req)
	if err != nil {
		return fmt.Errorf("failed to send usage to Purser: %w", err)
	}

	if !resp.Success {
		return fmt.Errorf("Purser rejected usage data: %s", resp.Error)
	}

	bs.logger.WithFields(logging.Fields{
		"summary_count":   len(summaries),
		"processed_count": resp.ProcessedCount,
	}).Info("Successfully sent usage summaries to Purser")

	return nil
}

// RunHourlyUsageSummary runs usage summarization for the previous hour
func (bs *BillingSummarizer) RunHourlyUsageSummary() error {
	now := time.Now()
	endTime := now.Truncate(time.Hour)
	startTime := endTime.Add(-time.Hour)

	bs.logger.WithFields(logging.Fields{
		"period_type": "hourly",
		"start_time":  startTime,
		"end_time":    endTime,
	}).Info("Running hourly usage summarization")

	return bs.SummarizeUsageForPeriod(startTime, endTime)
}

// RunDailyUsageSummary runs usage summarization for the previous day
func (bs *BillingSummarizer) RunDailyUsageSummary() error {
	now := time.Now()
	endTime := now.Truncate(24 * time.Hour)
	startTime := endTime.Add(-24 * time.Hour)

	bs.logger.WithFields(logging.Fields{
		"period_type": "daily",
		"start_time":  startTime,
		"end_time":    endTime,
	}).Info("Running daily usage summarization")

	return bs.SummarizeUsageForPeriod(startTime, endTime)
}

// GetPlatformMetrics returns platform-wide metrics from ClickHouse
func GetPlatformMetrics(c *gin.Context) {
	tenantID := c.GetString("tenant_id")
	if tenantID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Tenant context required"})
		return
	}

	// Parse time range from query params
	startTime := c.DefaultQuery("start_time", time.Now().Add(-24*time.Hour).Format(time.RFC3339))
	endTime := c.DefaultQuery("end_time", time.Now().Format(time.RFC3339))

	// Query ClickHouse for platform metrics
	var metrics struct {
		TotalStreamHours float64 `json:"total_stream_hours"`
		TotalEgressGB    float64 `json:"total_egress_gb"`
		TotalViewers     int     `json:"total_viewers"`
		UniqueViewers    int     `json:"unique_viewers"`
		PeakViewers      int     `json:"peak_viewers"`
		AvgStreamHealth  float32 `json:"avg_stream_health"`
		TotalStreams     int     `json:"total_streams"`
		ActiveStreams    int     `json:"active_streams"`
	}

	err := clickhouse.QueryRowContext(c.Request.Context(), `
        SELECT 
            COALESCE(countDistinct(concat(internal_name, toString(toStartOfHour(timestamp)))), 0) as total_stream_hours,
            COALESCE((SELECT sum(bandwidth_out) / (1024*1024*1024) FROM stream_health_metrics WHERE tenant_id = $1 AND timestamp BETWEEN $2 AND $3), 0) as total_egress_gb,
            COALESCE(SUM(viewer_count), 0) as total_viewers,
            COALESCE(uniq(user_id), 0) as unique_viewers,
            COALESCE(MAX(viewer_count), 0) as peak_viewers,
            COALESCE(AVG(buffer_health), 0) as avg_stream_health,
            COALESCE(uniq(internal_name), 0) as total_streams,
            COALESCE(uniqIf(internal_name, viewer_count > 0), 0) as active_streams
        FROM viewer_metrics
        WHERE tenant_id = $1
        AND timestamp BETWEEN $2 AND $3
    `, tenantID, startTime, endTime).Scan(
		&metrics.TotalStreamHours,
		&metrics.TotalEgressGB,
		&metrics.TotalViewers,
		&metrics.UniqueViewers,
		&metrics.PeakViewers,
		&metrics.AvgStreamHealth,
		&metrics.TotalStreams,
		&metrics.ActiveStreams,
	)

	if err != nil {
		logger.WithError(err).Error("Failed to fetch platform metrics from ClickHouse")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch platform metrics"})
		return
	}

	c.JSON(http.StatusOK, metrics)
}

// GetPlatformEvents returns platform-wide events from ClickHouse
func GetPlatformEvents(c *gin.Context) {
	tenantID := c.GetString("tenant_id")
	if tenantID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Tenant context required"})
		return
	}

	// Parse time range from query params
	startTime := c.DefaultQuery("start_time", time.Now().Add(-24*time.Hour).Format(time.RFC3339))
	endTime := c.DefaultQuery("end_time", time.Now().Format(time.RFC3339))

	// Query ClickHouse for platform events
	rows, err := clickhouse.QueryContext(c.Request.Context(), `
        SELECT 
            timestamp,
            internal_name,
            node_id,
            viewer_count,
            connection_type,
            buffer_health,
            connection_quality,
            country_code,
            city
        FROM viewer_metrics
        WHERE tenant_id = $1
        AND timestamp BETWEEN $2 AND $3
        ORDER BY timestamp DESC
        LIMIT 1000
    `, tenantID, startTime, endTime)

	if err != nil {
		logger.WithError(err).Error("Failed to fetch platform events from ClickHouse")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to fetch platform events"})
		return
	}
	defer rows.Close()

	var events []periscope.PlatformEvent
	for rows.Next() {
		var e periscope.PlatformEvent

		if err := rows.Scan(
			&e.Timestamp,
			&e.InternalName,
			&e.NodeID,
			&e.ViewerCount,
			&e.ConnectionType,
			&e.BufferHealth,
			&e.ConnectionQuality,
			&e.CountryCode,
			&e.City,
		); err != nil {
			logger.WithError(err).Error("Failed to scan platform event")
			continue
		}

		events = append(events, e)
	}

	response := periscope.PlatformEventsResponse{
		Events: events,
		Count:  len(events),
	}
	c.JSON(http.StatusOK, response)
}

// GetUsageSummary returns usage summary for the tenant
func GetUsageSummary(c *gin.Context) {
	tenantID := c.GetString("tenant_id")
	if tenantID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Tenant context required"})
		return
	}

	// Parse time range from query params
	startTimeStr := c.DefaultQuery("start_time", time.Now().Add(-24*time.Hour).Format(time.RFC3339))
	endTimeStr := c.DefaultQuery("end_time", time.Now().Format(time.RFC3339))

	startTime, err := time.Parse(time.RFC3339, startTimeStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid start_time format"})
		return
	}

	endTime, err := time.Parse(time.RFC3339, endTimeStr)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid end_time format"})
		return
	}

	// Create billing summarizer
	bs := NewBillingSummarizer(yugaDB, clickhouse, logger)

	// Generate usage summary
	summary, err := bs.generateTenantUsageSummary(tenantID, startTime, endTime)
	if err != nil {
		logger.WithError(err).Error("Failed to generate usage summary")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to generate usage summary"})
		return
	}

	c.JSON(http.StatusOK, summary)
}

// TriggerHourlySummary triggers hourly usage summarization
func TriggerHourlySummary(c *gin.Context) {
	tenantID := c.GetString("tenant_id")
	if tenantID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Tenant context required"})
		return
	}

	bs := NewBillingSummarizer(yugaDB, clickhouse, logger)
	if err := bs.RunHourlyUsageSummary(); err != nil {
		logger.WithError(err).Error("Failed to run hourly usage summarization")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to run hourly summarization"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Hourly usage summarization completed"})
}

// TriggerDailySummary triggers daily usage summarization
func TriggerDailySummary(c *gin.Context) {
	tenantID := c.GetString("tenant_id")
	if tenantID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Tenant context required"})
		return
	}

	bs := NewBillingSummarizer(yugaDB, clickhouse, logger)
	if err := bs.RunDailyUsageSummary(); err != nil {
		logger.WithError(err).Error("Failed to run daily usage summarization")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to run daily summarization"})
		return
	}

	c.JSON(http.StatusOK, gin.H{"message": "Daily usage summarization completed"})
}
