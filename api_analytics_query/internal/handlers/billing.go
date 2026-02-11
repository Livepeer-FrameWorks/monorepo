package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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
		tenantSummaries, summaryErr := bs.generateTenantUsageSummary(tenantID, startTime, endTime)
		if summaryErr != nil {
			bs.logger.WithError(summaryErr).WithField("tenant_id", tenantID).Error("Failed to generate usage summary for tenant")
			continue
		}

		for _, s := range tenantSummaries {
			summaries = append(summaries, *s)
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
		AND tenant_id NOT IN (
			'00000000-0000-0000-0000-000000000000',
			'00000000-0000-0000-0000-000000000001',
			'00000000-0000-0000-0000-000000000002'
		)
		ORDER BY tenant_id
	`)

	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

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

// generateTenantUsageSummary creates usage summaries for a specific tenant and time period.
// Returns one UsageSummary per cluster that has viewer/egress data. Non-cluster-scoped metrics
// (storage, processing, API) are attributed to the primary cluster.
func (bs *BillingSummarizer) generateTenantUsageSummary(tenantID string, startTime, endTime time.Time) ([]*models.UsageSummary, error) {
	ctx := context.Background()

	// Get tenant's primary cluster ID from Quartermaster API (not direct DB access!)
	primaryClusterID, err := bs.getTenantPrimaryCluster(tenantID)
	if err != nil {
		bs.logger.WithError(err).WithField("tenant_id", tenantID).Warn("Failed to get tenant cluster info, using default")
		primaryClusterID = "global-primary"
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
	if err != nil && !errors.Is(err, database.ErrNoRows) {
		return nil, fmt.Errorf("failed to query viewer metrics from ClickHouse: %w", err)
	}

	// Derive egress and viewer metrics from tenant_viewer_daily, grouped by cluster_id.
	// Each cluster that served viewers gets its own UsageSummary.
	type clusterViewerMetrics struct {
		EgressGB      float64
		ViewerHours   float64
		UniqueViewers int
	}
	clusterMetrics := map[string]*clusterViewerMetrics{}
	var totalEgressGB, totalViewerHours float64
	var totalUniqueViewers int

	viewerMetrics, err := bs.queryTenantViewerMetrics(ctx, tenantID, startTime, endTime)
	if err != nil && !errors.Is(err, database.ErrNoRows) {
		return nil, fmt.Errorf("failed to query egress/viewer metrics from ClickHouse: %w", err)
	}
	for _, metric := range viewerMetrics {
		m := clusterViewerMetrics{EgressGB: metric.EgressGB, ViewerHours: metric.ViewerHours, UniqueViewers: metric.UniqueViewers}
		cid := attributedViewerClusterID(metric.ClusterID, metric.OriginClusterID, primaryClusterID)
		if existing, ok := clusterMetrics[cid]; ok {
			existing.EgressGB += m.EgressGB
			existing.ViewerHours += m.ViewerHours
			existing.UniqueViewers += m.UniqueViewers
		} else {
			clusterMetrics[cid] = &m
		}
		totalEgressGB += m.EgressGB
		totalViewerHours += m.ViewerHours
		totalUniqueViewers += m.UniqueViewers
	}

	// Derive peak bandwidth from client_qoe_5m (avg_bw_out is in bytes/sec)
	var peakBandwidth float64
	err = bs.clickhouse.QueryRowContext(ctx, `
		SELECT COALESCE(max(avg_bw_out) / (1024*1024), 0) as peak_bandwidth_mbps
		FROM periscope.client_qoe_5m
		WHERE tenant_id = ?
		AND timestamp_5m BETWEEN ? AND ?
	`, tenantID, startTime, endTime).Scan(&peakBandwidth)
	if err != nil && !errors.Is(err, database.ErrNoRows) {
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
	if err != nil && !errors.Is(err, database.ErrNoRows) {
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
	if err != nil && !errors.Is(err, database.ErrNoRows) {
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
	if err != nil && !errors.Is(err, database.ErrNoRows) {
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
	if err != nil && !errors.Is(err, database.ErrNoRows) {
		bs.logger.WithError(err).Warn("Failed to query API usage aggregates, defaulting to 0")
	} else if err == nil {
		defer func() { _ = apiRows.Close() }()
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
		totalEgressGB != 0 ||
		totalViewerHours != 0 ||
		avgStorageGB != 0 ||
		peakBandwidth != 0 ||
		totalStreams != 0 ||
		maxViewers != 0 ||
		totalUniqueViewers != 0 ||
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

	if !hasUsage {
		bs.logger.WithField("tenant_id", tenantID).Info("No usage data for tenant in period, skipping")
		return nil, nil
	}

	// Ensure the primary cluster exists in the map (for non-cluster-scoped metrics)
	if _, ok := clusterMetrics[primaryClusterID]; !ok {
		clusterMetrics[primaryClusterID] = &clusterViewerMetrics{}
	}

	period := fmt.Sprintf("%s/%s", startTime.Format(time.RFC3339), endTime.Format(time.RFC3339))
	now := time.Now()
	var summaries []*models.UsageSummary

	for cid, vm := range clusterMetrics {
		summary := &models.UsageSummary{
			TenantID:     tenantID,
			ClusterID:    cid,
			Period:       period,
			EgressGB:     sanitizeFloat(vm.EgressGB),
			ViewerHours:  sanitizeFloat(vm.ViewerHours),
			TotalViewers: vm.UniqueViewers,
			Timestamp:    now,
		}

		// Non-cluster-scoped metrics are attributed to the primary cluster
		if cid == primaryClusterID {
			summary.StreamHours = sanitizeFloat(streamHours)
			summary.PeakBandwidthMbps = sanitizeFloat(peakBandwidth)
			summary.AverageStorageGB = sanitizeFloat(avgStorageGB)
			summary.LivepeerH264Seconds = sanitizeFloat(livepeerH264Seconds)
			summary.LivepeerVP9Seconds = sanitizeFloat(livepeerVP9Seconds)
			summary.LivepeerAV1Seconds = sanitizeFloat(livepeerAV1Seconds)
			summary.LivepeerHEVCSeconds = sanitizeFloat(livepeerHEVCSeconds)
			summary.NativeAvH264Seconds = sanitizeFloat(nativeAvH264Seconds)
			summary.NativeAvVP9Seconds = sanitizeFloat(nativeAvVP9Seconds)
			summary.NativeAvAV1Seconds = sanitizeFloat(nativeAvAV1Seconds)
			summary.NativeAvHEVCSeconds = sanitizeFloat(nativeAvHEVCSeconds)
			summary.NativeAvAACSeconds = sanitizeFloat(nativeAvAACSeconds)
			summary.NativeAvOpusSeconds = sanitizeFloat(nativeAvOpusSeconds)
			summary.TotalStreams = totalStreams
			summary.MaxViewers = maxViewers
			summary.UniqueUsers = uniqueUsers
			summary.APIRequests = sanitizeFloat(apiRequests)
			summary.APIErrors = sanitizeFloat(apiErrors)
			summary.APIDurationMs = sanitizeFloat(apiDurationMs)
			summary.APIComplexity = sanitizeFloat(apiComplexity)
			summary.APIBreakdown = apiBreakdown
		}

		summaries = append(summaries, summary)
	}

	bs.logger.WithFields(logging.Fields{
		"tenant_id":       tenantID,
		"cluster_count":   len(summaries),
		"stream_hours":    streamHours,
		"total_egress_gb": totalEgressGB,
		"viewer_hours":    totalViewerHours,
		"total_streams":   totalStreams,
	}).Info("Generated usage summaries for tenant")

	return summaries, nil
}

func attributedViewerClusterID(clusterID, originClusterID, primaryClusterID string) string {
	if strings.TrimSpace(clusterID) != "" {
		return clusterID
	}
	if strings.TrimSpace(originClusterID) != "" {
		return originClusterID
	}
	return primaryClusterID
}

type tenantViewerMetricRow struct {
	ClusterID       string
	OriginClusterID string
	EgressGB        float64
	ViewerHours     float64
	UniqueViewers   int
}

func (bs *BillingSummarizer) queryTenantViewerMetrics(ctx context.Context, tenantID string, startTime, endTime time.Time) ([]tenantViewerMetricRow, error) {
	queries := []struct {
		name  string
		query string
		scan  func(*sql.Rows) (tenantViewerMetricRow, error)
	}{
		{
			name: "cluster_and_origin",
			query: `
				SELECT
					cluster_id,
					origin_cluster_id,
					COALESCE(sum(egress_gb), 0) as egress_gb,
					COALESCE(sum(viewer_hours), 0) as viewer_hours,
					COALESCE(sum(unique_viewers), 0) as unique_viewers
				FROM periscope.tenant_viewer_daily
				WHERE tenant_id = ?
				AND day BETWEEN toDate(?) AND toDate(?)
				GROUP BY cluster_id, origin_cluster_id
			`,
			scan: func(rows *sql.Rows) (tenantViewerMetricRow, error) {
				var row tenantViewerMetricRow
				err := rows.Scan(&row.ClusterID, &row.OriginClusterID, &row.EgressGB, &row.ViewerHours, &row.UniqueViewers)
				return row, err
			},
		},
		{
			name: "cluster_only",
			query: `
				SELECT
					cluster_id,
					COALESCE(sum(egress_gb), 0) as egress_gb,
					COALESCE(sum(viewer_hours), 0) as viewer_hours,
					COALESCE(sum(unique_viewers), 0) as unique_viewers
				FROM periscope.tenant_viewer_daily
				WHERE tenant_id = ?
				AND day BETWEEN toDate(?) AND toDate(?)
				GROUP BY cluster_id
			`,
			scan: func(rows *sql.Rows) (tenantViewerMetricRow, error) {
				var row tenantViewerMetricRow
				err := rows.Scan(&row.ClusterID, &row.EgressGB, &row.ViewerHours, &row.UniqueViewers)
				return row, err
			},
		},
		{
			name: "tenant_only",
			query: `
				SELECT
					COALESCE(sum(egress_gb), 0) as egress_gb,
					COALESCE(sum(viewer_hours), 0) as viewer_hours,
					COALESCE(sum(unique_viewers), 0) as unique_viewers
				FROM periscope.tenant_viewer_daily
				WHERE tenant_id = ?
				AND day BETWEEN toDate(?) AND toDate(?)
			`,
			scan: func(rows *sql.Rows) (tenantViewerMetricRow, error) {
				var row tenantViewerMetricRow
				err := rows.Scan(&row.EgressGB, &row.ViewerHours, &row.UniqueViewers)
				return row, err
			},
		},
	}

	for i, q := range queries {
		rows, err := bs.clickhouse.QueryContext(ctx, q.query, tenantID, startTime, endTime)
		if err != nil {
			if i < len(queries)-1 && isMissingColumnCompatibilityError(err) {
				bs.logger.WithError(err).WithField("query_variant", q.name).Warn("tenant_viewer_daily schema not yet upgraded on this node, retrying with compatibility query")
				continue
			}
			return nil, err
		}
		defer rows.Close()

		var out []tenantViewerMetricRow
		for rows.Next() {
			row, scanErr := q.scan(rows)
			if scanErr != nil {
				continue
			}
			out = append(out, row)
		}
		return out, nil
	}

	return nil, nil
}

func isMissingColumnCompatibilityError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "unknown expression") ||
		strings.Contains(msg, "unknown identifier") ||
		strings.Contains(msg, "missing columns") ||
		strings.Contains(msg, "there is no column")
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
		return "", fmt.Errorf("quartermaster returned error: %s", tenantResp.GetError())
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

	if errors.Is(err, sql.ErrNoRows) {
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
	summaries, err := bs.generateTenantUsageSummary(tenantID, lastProcessed, targetEnd)
	if err != nil {
		return fmt.Errorf("failed to generate summary: %w", err)
	}

	if len(summaries) > 0 {
		// Send to Purser
		var flat []models.UsageSummary
		for _, s := range summaries {
			flat = append(flat, *s)
		}
		if sendErr := bs.sendUsageToPurser(flat); sendErr != nil {
			return fmt.Errorf("failed to send usage to Purser: %w", sendErr)
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
