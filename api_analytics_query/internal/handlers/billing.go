package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
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

// sanitizeFloat returns 0.0 if f is NaN or Inf, otherwise returns f
func sanitizeFloat(f float64) float64 {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	return f
}

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
	// We check stream_event_log (streaming), storage_snapshots (disk usage), and artifact_events (activity)
	rows, err := bs.clickhouse.QueryContext(context.Background(), `
		SELECT DISTINCT tenant_id FROM (
			SELECT tenant_id FROM periscope.stream_event_log
			WHERE timestamp >= NOW() - INTERVAL 7 DAY

			UNION ALL

			SELECT tenant_id FROM periscope.storage_snapshots
			WHERE timestamp >= NOW() - INTERVAL 7 DAY

			UNION ALL

			SELECT tenant_id FROM periscope.artifact_events
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

	// Derive viewer-based metrics from stream_event_log (total_viewers from Foghorn state snapshots)
	var maxViewers, totalStreams int
	var streamHours float64
	err = bs.clickhouse.QueryRowContext(ctx, `
		SELECT
			COALESCE(max(total_viewers), 0) as max_viewers,
			COALESCE(uniq(internal_name), 0) as total_streams,
			COALESCE(countDistinct(concat(internal_name, toString(toStartOfHour(timestamp)))), 0) as stream_hours
		FROM periscope.stream_event_log
		WHERE tenant_id = ?
		AND timestamp BETWEEN ? AND ?
		AND total_viewers IS NOT NULL
	`, tenantID, startTime, endTime).Scan(
		&maxViewers, &totalStreams, &streamHours,
	)
	if err != nil && err != database.ErrNoRows {
		return nil, fmt.Errorf("failed to query viewer metrics from ClickHouse: %w", err)
	}

	// Derive egress and viewer metrics from tenant_viewer_daily (pre-aggregated from viewer_connection_events)
	// Note: egress_gb comes from summed bytes_transferred in viewer_connection_events, not stream_health_samples
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

	// Derive peak bandwidth from client_qoe_5m (avg_bw_out is in bytes/sec)
	var peakBandwidth float64
	err = bs.clickhouse.QueryRowContext(ctx, `
		SELECT COALESCE(max(avg_bw_out) / (1024*1024), 0) as peak_bandwidth_mbps
		FROM periscope.client_qoe_5m
		WHERE tenant_id = ?
		AND timestamp_5m BETWEEN ? AND ?
	`, tenantID, startTime, endTime).Scan(&peakBandwidth)
	if err != nil && err != database.ErrNoRows {
		bs.logger.WithError(err).Info("Failed to query peak bandwidth from client_qoe_5m, defaulting to 0")
		peakBandwidth = 0
	}

	// Calculate Month-to-Date (MTD) Unique Users for correct MAX aggregation in Billing
	firstOfMonth := time.Date(startTime.Year(), startTime.Month(), 1, 0, 0, 0, 0, startTime.Location())
	var uniqueUsers int
	err = bs.clickhouse.QueryRowContext(ctx, `
		SELECT COALESCE(uniq(session_id), 0) as unique_users
		FROM periscope.viewer_connection_events
		WHERE tenant_id = ?
		AND timestamp BETWEEN ? AND ?
	`, tenantID, firstOfMonth, endTime).Scan(&uniqueUsers)
	if err != nil && err != database.ErrNoRows {
		bs.logger.WithError(err).Warn("Failed to query unique users (MTD) from ClickHouse, defaulting to 0")
		uniqueUsers = 0
	}

	// Query ClickHouse for average storage usage (using hourly MV for efficiency)
	var avgStorageGB float64
	err = bs.clickhouse.QueryRowContext(ctx, `
		SELECT COALESCE(avgMerge(avg_total_bytes) / (1024*1024*1024), 0) as avg_storage_gb
		FROM storage_usage_hourly
		WHERE tenant_id = ?
		AND hour BETWEEN ? AND ?
	`, tenantID, startTime, endTime).Scan(&avgStorageGB)
	if err != nil && err != database.ErrNoRows {
		bs.logger.WithError(err).Info("Failed to query storage_usage_hourly, defaulting to 0")
		avgStorageGB = 0
	}

	// Processing/transcoding usage from processing_daily (per-codec breakdown only)
	var livepeerH264Seconds, livepeerVP9Seconds, livepeerAV1Seconds, livepeerHEVCSeconds float64
	var nativeAvH264Seconds, nativeAvVP9Seconds, nativeAvAV1Seconds, nativeAvHEVCSeconds float64
	var nativeAvAACSeconds, nativeAvOpusSeconds float64

	err = bs.clickhouse.QueryRowContext(ctx, `
		SELECT
			COALESCE(sum(livepeer_h264_seconds), 0) as livepeer_h264_seconds,
			COALESCE(sum(livepeer_vp9_seconds), 0) as livepeer_vp9_seconds,
			COALESCE(sum(livepeer_av1_seconds), 0) as livepeer_av1_seconds,
			COALESCE(sum(livepeer_hevc_seconds), 0) as livepeer_hevc_seconds,
			COALESCE(sum(native_av_h264_seconds), 0) as native_av_h264_seconds,
			COALESCE(sum(native_av_vp9_seconds), 0) as native_av_vp9_seconds,
			COALESCE(sum(native_av_av1_seconds), 0) as native_av_av1_seconds,
			COALESCE(sum(native_av_hevc_seconds), 0) as native_av_hevc_seconds,
			COALESCE(sum(native_av_aac_seconds), 0) as native_av_aac_seconds,
			COALESCE(sum(native_av_opus_seconds), 0) as native_av_opus_seconds
		FROM processing_daily
		WHERE tenant_id = ?
		AND day BETWEEN toDate(?) AND toDate(?)
	`, tenantID, startTime, endTime).Scan(
		&livepeerH264Seconds, &livepeerVP9Seconds, &livepeerAV1Seconds, &livepeerHEVCSeconds,
		&nativeAvH264Seconds, &nativeAvVP9Seconds, &nativeAvAV1Seconds, &nativeAvHEVCSeconds,
		&nativeAvAACSeconds, &nativeAvOpusSeconds)
	if err != nil && err != database.ErrNoRows {
		bs.logger.WithError(err).Info("Failed to query processing usage, defaulting to 0")
	}

	// API usage aggregates from api_usage_hourly (Gateway API summaries)
	var apiRequests, apiErrors, apiDurationMs, apiComplexity float64
	var apiBreakdown []models.APIUsageBreakdown
	breakdownIndex := map[string]int{}
	apiRows, err := bs.clickhouse.QueryContext(ctx, `
		SELECT
			auth_type,
			operation_type,
			operation_name,
			COALESCE(sumMerge(total_requests), 0) as total_requests,
			COALESCE(sumMerge(total_errors), 0) as total_errors,
			COALESCE(sumMerge(total_duration_ms), 0) as total_duration_ms,
			COALESCE(sumMerge(total_complexity), 0) as total_complexity,
			COALESCE(uniqCombinedMerge(unique_users), 0) as unique_users,
			COALESCE(uniqCombinedMerge(unique_tokens), 0) as unique_tokens
		FROM api_usage_hourly
		WHERE tenant_id = ?
		AND hour BETWEEN ? AND ?
		GROUP BY auth_type, operation_type, operation_name
	`, tenantID, startTime, endTime)
	if err != nil && err != database.ErrNoRows {
		bs.logger.WithError(err).Warn("Failed to query API usage aggregates, defaulting to 0")
	} else if err == nil {
		defer apiRows.Close()
		for apiRows.Next() {
			var breakdown models.APIUsageBreakdown
			var operationName sql.NullString
			var uniqueUsers, uniqueTokens float64
			if scanErr := apiRows.Scan(
				&breakdown.AuthType,
				&breakdown.OperationType,
				&operationName,
				&breakdown.Requests,
				&breakdown.Errors,
				&breakdown.DurationMs,
				&breakdown.Complexity,
				&uniqueUsers,
				&uniqueTokens,
			); scanErr != nil {
				continue
			}
			if operationName.Valid {
				breakdown.OperationName = operationName.String
			}
			breakdown.Requests = sanitizeFloat(breakdown.Requests)
			breakdown.Errors = sanitizeFloat(breakdown.Errors)
			breakdown.DurationMs = sanitizeFloat(breakdown.DurationMs)
			breakdown.Complexity = sanitizeFloat(breakdown.Complexity)
			breakdown.UniqueUsers = sanitizeFloat(uniqueUsers)
			breakdown.UniqueTokens = sanitizeFloat(uniqueTokens)
			key := fmt.Sprintf("%s|%s|%s", breakdown.AuthType, breakdown.OperationType, breakdown.OperationName)
			breakdownIndex[key] = len(apiBreakdown)
			apiBreakdown = append(apiBreakdown, breakdown)
			apiRequests += breakdown.Requests
			apiErrors += breakdown.Errors
			apiDurationMs += breakdown.DurationMs
			apiComplexity += breakdown.Complexity
		}
	}

	hasUsage := streamHours != 0 ||
		egressGB != 0 ||
		viewerHours != 0 ||
		avgStorageGB != 0 ||
		peakBandwidth != 0 ||
		totalStreams != 0 ||
		maxViewers != 0 ||
		uniqueViewers != 0 ||
		uniqueUsers != 0 ||
		livepeerH264Seconds != 0 ||
		livepeerVP9Seconds != 0 ||
		livepeerAV1Seconds != 0 ||
		livepeerHEVCSeconds != 0 ||
		nativeAvH264Seconds != 0 ||
		nativeAvVP9Seconds != 0 ||
		nativeAvAV1Seconds != 0 ||
		nativeAvHEVCSeconds != 0 ||
		nativeAvAACSeconds != 0 ||
		nativeAvOpusSeconds != 0 ||
		apiRequests != 0 ||
		apiErrors != 0 ||
		apiDurationMs != 0 ||
		apiComplexity != 0

	// Skip if no usage data
	if !hasUsage {
		bs.logger.WithField("tenant_id", tenantID).Info("No usage data for tenant in period, skipping")
		return nil, nil
	}

	summary := &models.UsageSummary{
		TenantID:          tenantID,
		ClusterID:         clusterID,
		Period:            fmt.Sprintf("%s/%s", startTime.Format(time.RFC3339), endTime.Format(time.RFC3339)),
		StreamHours:       sanitizeFloat(streamHours),
		EgressGB:          sanitizeFloat(egressGB),
		PeakBandwidthMbps: sanitizeFloat(peakBandwidth),
		AverageStorageGB:  sanitizeFloat(avgStorageGB),
		// Per-codec breakdown: Livepeer
		LivepeerH264Seconds: sanitizeFloat(livepeerH264Seconds),
		LivepeerVP9Seconds:  sanitizeFloat(livepeerVP9Seconds),
		LivepeerAV1Seconds:  sanitizeFloat(livepeerAV1Seconds),
		LivepeerHEVCSeconds: sanitizeFloat(livepeerHEVCSeconds),
		// Per-codec breakdown: Native AV
		NativeAvH264Seconds: sanitizeFloat(nativeAvH264Seconds),
		NativeAvVP9Seconds:  sanitizeFloat(nativeAvVP9Seconds),
		NativeAvAV1Seconds:  sanitizeFloat(nativeAvAV1Seconds),
		NativeAvHEVCSeconds: sanitizeFloat(nativeAvHEVCSeconds),
		NativeAvAACSeconds:  sanitizeFloat(nativeAvAACSeconds),
		NativeAvOpusSeconds: sanitizeFloat(nativeAvOpusSeconds),
		// Viewer metrics
		TotalStreams: totalStreams,
		TotalViewers: uniqueViewers,
		ViewerHours:  sanitizeFloat(viewerHours),
		MaxViewers:   maxViewers,
		UniqueUsers:  uniqueUsers,
		Timestamp:    time.Now(),
		// API usage aggregates
		APIRequests:   sanitizeFloat(apiRequests),
		APIErrors:     sanitizeFloat(apiErrors),
		APIDurationMs: sanitizeFloat(apiDurationMs),
		APIComplexity: sanitizeFloat(apiComplexity),
		APIBreakdown:  apiBreakdown,
	}

	bs.logger.WithFields(logging.Fields{
		"tenant_id":      tenantID,
		"stream_hours":   streamHours,
		"egress_gb":      egressGB,
		"viewer_hours":   viewerHours,
		"unique_viewers": uniqueViewers,
		"max_viewers":    maxViewers,
		"total_streams":  totalStreams,
	}).Info("Generated usage summary for tenant")

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
	} else {
		// Advance cursor even when no usage summary is emitted
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
		}).Info("No usage summary emitted; cursor advanced")
	}

	return nil
}
