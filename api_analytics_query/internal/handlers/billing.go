package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	qmclient "frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/config"
	"frameworks/pkg/database"
	"frameworks/pkg/kafka"
	"frameworks/pkg/logging"
	"frameworks/pkg/models"

	"github.com/sirupsen/logrus"
)

// BillingSummarizer handles usage summarization for billing
type BillingSummarizer struct {
	yugaDB              database.PostgresConn
	clickhouse          database.ClickHouseConn
	logger              logging.Logger
	kafkaProducer       *kafka.KafkaProducer
	quartermasterClient *qmclient.GRPCClient
	billingTopic        string
}

// NewBillingSummarizer creates a new billing summarizer instance
func NewBillingSummarizer(yugaDB database.PostgresConn, clickhouse database.ClickHouseConn, logger logging.Logger) *BillingSummarizer {
	quartermasterGRPCAddr := config.GetEnv("QUARTERMASTER_GRPC_ADDR", "quartermaster:19002")
	serviceToken := config.RequireEnv("SERVICE_TOKEN")

	// Initialize Kafka producer
	brokers := strings.Split(config.RequireEnv("KAFKA_BROKERS"), ",")
	billingTopic := config.GetEnv("BILLING_KAFKA_TOPIC", "billing.usage_reports")
	// Assuming logger is compatible or creating a new one for the client
	kLogger := logrus.New()

	kafkaProducer, err := kafka.NewKafkaProducer(brokers, billingTopic, "periscope-query", kLogger)
	if err != nil {
		logger.WithError(err).Fatal("Failed to create Kafka producer for billing")
	}

	quartermasterClient, err := qmclient.NewGRPCClient(qmclient.GRPCConfig{
		GRPCAddr:     quartermasterGRPCAddr,
		ServiceToken: serviceToken,
		Timeout:      10 * time.Second,
		Logger:       logger,
	})
	if err != nil {
		logger.WithError(err).Fatal("Failed to create Quartermaster gRPC client for billing")
	}

	return &BillingSummarizer{
		yugaDB:              yugaDB,
		clickhouse:          clickhouse,
		logger:              logger,
		kafkaProducer:       kafkaProducer,
		quartermasterClient: quartermasterClient,
		billingTopic:        billingTopic,
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
	// Query ClickHouse for active tenants across all relevant tables
	// We check stream_events (streaming), storage_snapshots (disk usage), and clip_events (activity)
	rows, err := bs.clickhouse.QueryContext(context.Background(), `
		SELECT DISTINCT tenant_id FROM (
			SELECT tenant_id FROM periscope.stream_events
			WHERE timestamp >= NOW() - INTERVAL 7 DAY

			UNION ALL

			SELECT tenant_id FROM periscope.storage_snapshots
			WHERE timestamp >= NOW() - INTERVAL 7 DAY

			UNION ALL

			SELECT tenant_id FROM periscope.clip_events
			WHERE timestamp >= NOW() - INTERVAL 7 DAY
		)
		WHERE tenant_id IS NOT NULL
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

	// Derive viewer-based metrics from stream_events (total_viewers from Foghorn state snapshots)
	var maxViewers, totalStreams int
	var streamHours float64
	err = bs.clickhouse.QueryRowContext(ctx, `
		SELECT
			COALESCE(max(total_viewers), 0) as max_viewers,
			COALESCE(uniq(internal_name), 0) as total_streams,
			COALESCE(countDistinct(concat(internal_name, toString(toStartOfHour(timestamp)))), 0) as stream_hours
		FROM periscope.stream_events
		WHERE tenant_id = ?
		AND timestamp BETWEEN ? AND ?
		AND total_viewers IS NOT NULL
	`, tenantID, startTime, endTime).Scan(
		&maxViewers, &totalStreams, &streamHours,
	)
	if err != nil && err != database.ErrNoRows {
		return nil, fmt.Errorf("failed to query viewer metrics from ClickHouse: %w", err)
	}

	// Derive egress and viewer metrics from tenant_viewer_daily (pre-aggregated from connection_events)
	// Note: egress_gb comes from summed bytes_transferred in connection_events, not stream_health_metrics
	var egressGB, viewerHours float64
	var uniqueViewers int
	err = bs.clickhouse.QueryRowContext(ctx, `
		SELECT
			COALESCE(sum(egress_gb), 0) as egress_gb,
			COALESCE(sum(viewer_hours), 0) as viewer_hours,
			COALESCE(sum(unique_viewers), 0) as unique_viewers
		FROM periscope.tenant_viewer_daily
		WHERE tenant_id = ?
		AND day BETWEEN toDate(?) AND toDate(?)
	`, tenantID, startTime, endTime).Scan(&egressGB, &viewerHours, &uniqueViewers)
	if err != nil && err != database.ErrNoRows {
		return nil, fmt.Errorf("failed to query egress/viewer metrics from ClickHouse: %w", err)
	}

	// Derive peak bandwidth from client_metrics_5m (avg_bw_out is in bytes/sec)
	var peakBandwidth float64
	err = bs.clickhouse.QueryRowContext(ctx, `
		SELECT COALESCE(max(avg_bw_out) / (1024*1024), 0) as peak_bandwidth_mbps
		FROM periscope.client_metrics_5m
		WHERE tenant_id = ?
		AND timestamp BETWEEN ? AND ?
	`, tenantID, startTime, endTime).Scan(&peakBandwidth)
	if err != nil && err != database.ErrNoRows {
		bs.logger.WithError(err).Debug("Failed to query peak bandwidth from client_metrics_5m, defaulting to 0")
		peakBandwidth = 0
	}

	// Calculate Month-to-Date (MTD) Unique Users for correct MAX aggregation in Billing
	firstOfMonth := time.Date(startTime.Year(), startTime.Month(), 1, 0, 0, 0, 0, startTime.Location())
	var uniqueUsers int
	err = bs.clickhouse.QueryRowContext(ctx, `
		SELECT COALESCE(uniq(session_id), 0) as unique_users
		FROM connection_events 
		WHERE tenant_id = $1 
		AND timestamp BETWEEN $2 AND $3
	`, tenantID, firstOfMonth, endTime).Scan(&uniqueUsers)
	if err != nil && err != database.ErrNoRows {
		bs.logger.WithError(err).Warn("Failed to query unique users (MTD) from ClickHouse, defaulting to 0")
		uniqueUsers = 0
	}

	// Query ClickHouse for average storage usage (snapshots)
	var avgStorageGB float64
	err = bs.clickhouse.QueryRowContext(ctx, `
		SELECT COALESCE(avg(total_bytes) / (1024*1024*1024), 0) as avg_storage_gb
		FROM storage_snapshots
		WHERE tenant_id = $1
		AND timestamp BETWEEN $2 AND $3
	`, tenantID, startTime, endTime).Scan(&avgStorageGB)
	if err != nil && err != database.ErrNoRows {
		bs.logger.WithError(err).Debug("Failed to query storage snapshots, defaulting to 0")
		avgStorageGB = 0
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
    AND event_type = 'recording-complete'
		AND timestamp BETWEEN $2 AND $3
	`, tenantID, startTime, endTime).Scan(&recordingGB)
	if err != nil && err != database.ErrNoRows {
		bs.logger.WithError(err).Debug("Failed to query recording data from ClickHouse, defaulting to 0")
		recordingGB = 0
	}

	// Query ClickHouse for geo breakdown (top 10 countries with rich metrics)
	// Returns: country_code, viewer_count, viewer_hours, egress_gb, percentage
	var geoBreakdown []models.CountryMetrics
	geoRows, err := bs.clickhouse.QueryContext(ctx, `
		WITH totals AS (
			SELECT sum(viewer_count) as total_viewers
			FROM periscope.viewer_geo_hourly
			WHERE tenant_id = ?
			AND hour BETWEEN ? AND ?
		)
		SELECT
			country_code,
			sum(viewer_count) as total_viewer_count,
			sum(viewer_hours) as viewer_hours,
			sum(egress_gb) as egress_gb,
			if((SELECT total_viewers FROM totals) > 0,
			   sum(viewer_count) * 100.0 / (SELECT total_viewers FROM totals),
			   0) as percentage
		FROM periscope.viewer_geo_hourly
		WHERE tenant_id = ?
		AND hour BETWEEN ? AND ?
		AND country_code != '' AND country_code != '--'
		GROUP BY country_code
		ORDER BY total_viewer_count DESC
		LIMIT 10
	`, tenantID, startTime, endTime, tenantID, startTime, endTime)

	if err != nil && err != database.ErrNoRows {
		bs.logger.WithError(err).Warn("Failed to query geo breakdown")
	} else if err == nil {
		defer geoRows.Close()
		for geoRows.Next() {
			var cm models.CountryMetrics
			if err := geoRows.Scan(&cm.CountryCode, &cm.ViewerCount, &cm.ViewerHours, &cm.EgressGB, &cm.Percentage); err == nil {
				geoBreakdown = append(geoBreakdown, cm)
			}
		}
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
		TotalViewers:      uniqueViewers, // Unique viewers for the period from tenant_viewer_daily
		PeakBandwidthMbps: peakBandwidth,
		UniqueUsers:       uniqueUsers, // MTD unique sessions from connection_events
		ViewerHours:       viewerHours,
		RecordingGB:       recordingGB,
		// Storage and clip lifecycle
		StorageGB:            storageGB,
		AverageStorageGB:     avgStorageGB,
		ClipsAdded:           clipsAdded,
		ClipsDeleted:         clipsDeleted,
		ClipStorageAddedGB:   clipStorageAddedGB,
		ClipStorageDeletedGB: clipStorageDeletedGB,
		Timestamp:            time.Now(),
		GeoBreakdown:         geoBreakdown,
	}

	bs.logger.WithFields(logging.Fields{
		"tenant_id":      tenantID,
		"stream_hours":   streamHours,
		"egress_gb":      egressGB,
		"viewer_hours":   viewerHours,
		"unique_viewers": uniqueViewers,
		"max_viewers":    maxViewers,
		"total_streams":  totalStreams,
	}).Debug("Generated usage summary for tenant")

	return summary, nil
}

// getTenantPrimaryCluster gets tenant's primary cluster by calling Quartermaster gRPC API
func (bs *BillingSummarizer) getTenantPrimaryCluster(tenantID string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	tenantResp, err := bs.quartermasterClient.GetTenant(ctx, tenantID)
	if err != nil {
		return "", fmt.Errorf("failed to call Quartermaster: %w", err)
	}

	if tenantResp.GetError() != "" {
		return "", fmt.Errorf("Quartermaster returned error: %s", tenantResp.GetError())
	}

	pbTenant := tenantResp.GetTenant()
	if pbTenant != nil && pbTenant.GetPrimaryClusterId() != "" {
		return pbTenant.GetPrimaryClusterId(), nil
	}

	return "global-primary", nil // Default fallback when no primary cluster is set
}

// sendUsageToPurser sends usage summaries to the Purser billing service via Kafka
func (bs *BillingSummarizer) sendUsageToPurser(summaries []models.UsageSummary) error {
	if bs.kafkaProducer == nil {
		return fmt.Errorf("kafka producer not initialized")
	}

	successCount := 0
	for _, summary := range summaries {
		// Marshal summary to JSON
		payload, err := json.Marshal(summary)
		if err != nil {
			bs.logger.WithError(err).WithField("tenant_id", summary.TenantID).Error("Failed to marshal usage summary")
			continue
		}

		// Produce to Kafka topic "billing.usage_reports"
		// Key = TenantID (ensures ordering per tenant)
		err = bs.kafkaProducer.ProduceMessage(
			bs.billingTopic,
			[]byte(summary.TenantID),
			payload,
			map[string]string{
				"source": "periscope-query",
				"type":   "usage_summary",
			},
		)

		if err != nil {
			bs.logger.WithError(err).WithField("tenant_id", summary.TenantID).Error("Failed to produce usage report to Kafka")
			continue
		}
		successCount++
	}

	bs.logger.WithFields(logging.Fields{
		"summary_count":   len(summaries),
		"processed_count": successCount,
	}).Info("Successfully produced usage summaries to Kafka")

	if successCount < len(summaries) {
		return fmt.Errorf("failed to send some summaries")
	}

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

// ProcessPendingUsage processes all pending usage since the last cursor
func (bs *BillingSummarizer) ProcessPendingUsage(ctx context.Context) error {
	bs.logger.Info("Processing pending usage for all tenants")

	// Get all active tenants
	tenants, err := bs.getActiveTenants()
	if err != nil {
		return fmt.Errorf("failed to get active tenants: %w", err)
	}

	for _, tenantID := range tenants {
		if err := bs.processTenantPendingUsage(ctx, tenantID); err != nil {
			bs.logger.WithError(err).WithField("tenant_id", tenantID).Error("Failed to process pending usage for tenant")
			// Continue to next tenant
		}
	}
	return nil
}

func (bs *BillingSummarizer) processTenantPendingUsage(ctx context.Context, tenantID string) error {
	// Get last processed timestamp from cursor
	var lastProcessed time.Time
	err := bs.yugaDB.QueryRowContext(ctx, `
		SELECT last_processed_at FROM periscope.billing_cursors WHERE tenant_id = $1
	`, tenantID).Scan(&lastProcessed)

	if err == sql.ErrNoRows {
		// Default to 24 hours ago for new tenants/first run
		// This avoids reprocessing history forever if we add a new tenant
		lastProcessed = time.Now().Add(-24 * time.Hour)
		// Insert initial cursor
		_, err = bs.yugaDB.ExecContext(ctx, `
			INSERT INTO periscope.billing_cursors (tenant_id, last_processed_at, updated_at)
			VALUES ($1, $2, NOW())
		`, tenantID, lastProcessed)
		if err != nil {
			return fmt.Errorf("failed to initialize cursor: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("failed to query cursor: %w", err)
	}

	// Target end time is Now (truncated to minute for stability)
	targetEnd := time.Now().Truncate(time.Minute)

	// If no new data to process (e.g. run too frequent), skip
	if targetEnd.Sub(lastProcessed) < 1*time.Minute {
		return nil
	}

	// Generate summary for the incremental period
	summary, err := bs.generateTenantUsageSummary(tenantID, lastProcessed, targetEnd)
	if err != nil {
		return fmt.Errorf("failed to generate summary: %w", err)
	}

	if summary != nil {
		// Send to Purser
		// TODO: Switch to Kafka Producer here
		if err := bs.sendUsageToPurser([]models.UsageSummary{*summary}); err != nil {
			return fmt.Errorf("failed to send usage to Purser: %w", err)
		}

		// Update cursor ONLY after successful send
		_, err = bs.yugaDB.ExecContext(ctx, `
			UPDATE periscope.billing_cursors 
			SET last_processed_at = $1, updated_at = NOW()
			WHERE tenant_id = $2
		`, targetEnd, tenantID)
		if err != nil {
			return fmt.Errorf("failed to update cursor: %w", err)
		}

		bs.logger.WithFields(logging.Fields{
			"tenant_id": tenantID,
			"start":     lastProcessed,
			"end":       targetEnd,
		}).Info("Successfully processed pending usage")
	}

	return nil
}
