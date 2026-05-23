package handlers

import (
	"context"
	"crypto/sha1"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/models"

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
	kLogger := logrus.New()

	kafkaProducer, err := kafka.NewKafkaProducer(brokers, billingTopic, "periscope-query", kLogger)
	if err != nil {
		logger.WithError(err).Fatal("Failed to create Kafka producer for billing")
	}

	quartermasterClient, err := qmclient.NewGRPCClient(qmclient.GRPCConfig{
		GRPCAddr:      quartermasterGRPCAddr,
		ServiceToken:  serviceToken,
		Timeout:       10 * time.Second,
		Logger:        logger,
		AllowInsecure: config.GetEnvBool("GRPC_ALLOW_INSECURE", false),
		CACertFile:    config.GetEnv("GRPC_TLS_CA_PATH", ""),
		ServerName:    config.GetServiceGRPCTLSServerName("quartermaster"),
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

// SummarizeUsageForPeriod aggregates usage data for all tenants for a given time period.
// It emits the same canonical 5-minute rows as the cursor path; callers that need a
// wider range get one validated slice at a time.
func (bs *BillingSummarizer) SummarizeUsageForPeriod(startTime, endTime time.Time) error {
	startTime = startTime.UTC()
	endTime = endTime.UTC()
	const billingCursorAlignment = 5 * time.Minute
	if !endTime.After(startTime) {
		return fmt.Errorf("invalid summary period: end_time must be after start_time")
	}
	if !startTime.Equal(startTime.Truncate(billingCursorAlignment)) || !endTime.Equal(endTime.Truncate(billingCursorAlignment)) {
		return fmt.Errorf("invalid summary period: start_time and end_time must be aligned to 5-minute boundaries")
	}

	bs.logger.WithFields(logging.Fields{
		"start_time": startTime,
		"end_time":   endTime,
	}).Info("Starting usage summarization for period")

	// Get all active tenants
	tenants, err := bs.getActiveTenants()
	if err != nil {
		return fmt.Errorf("failed to get active tenants: %w", err)
	}

	for sliceStart := startTime; sliceStart.Before(endTime); sliceStart = sliceStart.Add(billingCursorAlignment) {
		sliceEnd := sliceStart.Add(billingCursorAlignment)
		if sliceEnd.After(endTime) {
			sliceEnd = endTime
		}
		var summaries []models.UsageSummary
		var failedTenants []string

		for _, tenantID := range tenants {
			tenantSummaries, summaryErr := bs.generateTenantUsageSummary(tenantID, sliceStart, sliceEnd)
			if summaryErr != nil {
				bs.logger.WithError(summaryErr).WithFields(logging.Fields{
					"tenant_id": tenantID,
					"start":     sliceStart,
					"end":       sliceEnd,
				}).Error("Failed to generate usage summary for tenant")
				failedTenants = append(failedTenants, tenantID)
				continue
			}

			for _, s := range tenantSummaries {
				summaries = append(summaries, *s)
			}
		}
		if len(failedTenants) > 0 {
			return fmt.Errorf("failed to generate usage summaries for %s in %s/%s", strings.Join(failedTenants, ","), sliceStart.Format(time.RFC3339), sliceEnd.Format(time.RFC3339))
		}

		if len(summaries) > 0 {
			if err = bs.sendUsageToPurser(summaries); err != nil {
				return fmt.Errorf("failed to send usage to Purser: %w", err)
			}

			bs.logger.WithFields(logging.Fields{
				"summary_count": len(summaries),
				"start":         sliceStart,
				"end":           sliceEnd,
			}).Info("Successfully sent usage summaries to Purser")
		}
	}

	return nil
}

// getActiveTenants retrieves all active tenant IDs from the canonical
// finalized-fact tables and storage snapshots that the billing path
// reads. Sourcing from these tables (not stream_event_log /
// artifact_events) guarantees that any tenant the rated meters can see
// is also a tenant the cursor walks.
func (bs *BillingSummarizer) getActiveTenants() ([]string, error) {
	rows, err := bs.clickhouse.QueryContext(context.Background(), `
		SELECT DISTINCT tenant_id FROM (
			SELECT tenant_id FROM periscope.viewer_sessions_final
			WHERE projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 7 DAY)

			UNION ALL

			SELECT tenant_id FROM periscope.processing_segments_final
			WHERE projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 7 DAY)

			UNION ALL

			SELECT tenant_id FROM periscope.stream_sessions_final
			WHERE projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 7 DAY)

			UNION ALL

			SELECT tenant_id
			FROM (
				SELECT
					tenant_id, cluster_id, storage_scope,
					storage_provider_tenant_id, storage_provider_cluster_id, storage_backend,
					argMax(total_bytes, tuple(timestamp, ingested_at_ms)) AS total_bytes
				FROM periscope.storage_snapshots
				GROUP BY tenant_id, cluster_id, storage_scope,
				         storage_provider_tenant_id, storage_provider_cluster_id, storage_backend
			)
			WHERE total_bytes > 0

			UNION ALL

			SELECT tenant_id FROM periscope.stream_runtime_5m
			WHERE projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 7 DAY)

			UNION ALL

			SELECT tenant_id FROM periscope.api_usage_5m
			WHERE projection_version_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 7 DAY)

			UNION ALL

			SELECT JSONExtractString(natural_key_json, 'tenant_id') AS tenant_id
			FROM periscope.projection_divergences
			WHERE observed_at_ms >= toUnixTimestamp64Milli(now64(3) - INTERVAL 7 DAY)
		)
		WHERE tenant_id IS NOT NULL
		AND tenant_id != ''
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
			return nil, fmt.Errorf("scan active tenant: %w", err)
		}
		tenants = append(tenants, tenantID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate active tenants: %w", err)
	}

	return tenants, nil
}

// generateTenantUsageSummary creates one usage summary per cluster that has
// canonical usage in the period. Tenant-wide gauges attach to the primary
// cluster; meters with source cluster identity stay cluster-scoped.
func (bs *BillingSummarizer) generateTenantUsageSummary(tenantID string, startTime, endTime time.Time) ([]*models.UsageSummary, error) {
	ctx := context.Background()

	// Get tenant's primary cluster ID from Quartermaster API (not direct DB access!)
	primaryClusterID, err := bs.getTenantPrimaryCluster(tenantID)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve tenant primary cluster: %w", err)
	}

	type clusterStreamRuntimeMetrics struct {
		MaxViewers   int
		TotalStreams int
		StreamHours  float64
	}
	clusterStreamRuntime := map[string]clusterStreamRuntimeMetrics{}
	streamRows, err := bs.clickhouse.QueryContext(ctx, `
		SELECT
			cluster_id,
			COALESCE(toInt32(max(peak_viewers)), 0)             AS max_viewers,
			COALESCE(toInt32(uniqCombined(stream_id)), 0)       AS total_streams,
			COALESCE(sum(active_seconds) / 3600.0, 0)           AS stream_hours
		FROM periscope.stream_runtime_5m_v
		WHERE tenant_id = ?
		  AND window_start >= ?
		  AND window_start <  ?
		GROUP BY cluster_id
	`, tenantID, startTime, endTime)
	if err != nil && !errors.Is(err, database.ErrNoRows) {
		return nil, fmt.Errorf("failed to query stream runtime metrics from ClickHouse: %w", err)
	} else if err == nil {
		defer func() { _ = streamRows.Close() }()
		for streamRows.Next() {
			var cid string
			var m clusterStreamRuntimeMetrics
			if scanErr := streamRows.Scan(&cid, &m.MaxViewers, &m.TotalStreams, &m.StreamHours); scanErr != nil {
				return nil, fmt.Errorf("scan stream runtime row: %w", scanErr)
			}
			if cid == "" {
				return nil, fmt.Errorf("stream runtime row missing cluster_id for tenant %s", tenantID)
			}
			m.StreamHours = sanitizeFloat(m.StreamHours)
			clusterStreamRuntime[cid] = m
		}
		if iterErr := streamRows.Err(); iterErr != nil {
			return nil, fmt.Errorf("iterate stream runtime rows: %w", iterErr)
		}
	}

	// Derive finalized viewer and bandwidth metrics from USER_END projections,
	// grouped by cluster_id.
	// Each cluster that served viewers gets its own UsageSummary.
	type clusterViewerMetrics struct {
		IngressGB     float64
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
		m := clusterViewerMetrics{IngressGB: metric.IngressGB, EgressGB: metric.EgressGB, ViewerHours: metric.ViewerHours, UniqueViewers: metric.UniqueViewers}
		cid := attributedViewerClusterID(metric.ClusterID, metric.OriginClusterID, primaryClusterID)
		if existing, ok := clusterMetrics[cid]; ok {
			existing.IngressGB += m.IngressGB
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
		return nil, fmt.Errorf("failed to query peak bandwidth from ClickHouse: %w", err)
	}

	// Calculate Month-to-Date (MTD) Unique Users for correct MAX aggregation in Billing
	firstOfMonth := time.Date(startTime.Year(), startTime.Month(), 1, 0, 0, 0, 0, startTime.Location())
	var uniqueUsers int
	err = bs.clickhouse.QueryRowContext(ctx, `
		WITH sessions AS (
			SELECT
				tenant_id, node_id, session_id,
				argMax(source_ended_at_ms, projection_version_ms) AS source_ended_at_ms,
				argMax(closed_reason, projection_version_ms) AS closed_reason
			FROM periscope.viewer_sessions_final
			WHERE tenant_id = ?
			  AND projection_version_ms < ?
			GROUP BY tenant_id, node_id, session_id
		)
		SELECT COALESCE(uniqCombined(session_id), 0) as unique_users
		FROM sessions
		WHERE tenant_id = ?
		  AND closed_reason = 'final'
		  AND source_ended_at_ms >= ?
		  AND source_ended_at_ms <  ?
	`, tenantID, endTime.UnixMilli(), tenantID, firstOfMonth.UnixMilli(), endTime.UnixMilli()).Scan(&uniqueUsers)
	if err != nil && !errors.Is(err, database.ErrNoRows) {
		return nil, fmt.Errorf("failed to query finalized unique users from ClickHouse: %w", err)
	}

	clusterStorageProviderUsage, err := bs.queryClusterStorageProviderUsage(ctx, tenantID, startTime, endTime, primaryClusterID)
	if err != nil {
		return nil, fmt.Errorf("failed to query provider storage GiB-seconds from ClickHouse: %w", err)
	}
	clusterStorageGB := storageMetricsFromProviderUsage(clusterStorageProviderUsage)
	usageAdjustments, err := bs.queryUsageAdjustments(ctx, tenantID, startTime, endTime, primaryClusterID)
	if err != nil {
		return nil, fmt.Errorf("failed to query usage adjustments from ClickHouse: %w", err)
	}
	clusterProcessing, err := bs.queryClusterProcessingSeconds(ctx, tenantID, startTime, endTime, primaryClusterID)
	if err != nil {
		return nil, fmt.Errorf("failed to query processing seconds from ClickHouse: %w", err)
	}

	// API usage aggregates from the canonical 5-minute ledger. These are
	// operational rows in Purser, but keeping them on the canonical path
	// avoids hidden rollup dependencies in the billing summarizer.
	var apiRequests, apiErrors, apiDurationMs, apiComplexity float64
	var apiBreakdown []models.APIUsageBreakdown
	breakdownIndex := map[string]int{}
	apiRows, err := bs.clickhouse.QueryContext(ctx, `
		SELECT
			auth_type,
			operation_type,
			operation_name,
			COALESCE(sum(requests), 0)                              AS total_requests,
			COALESCE(sum(errors), 0)                                AS total_errors,
			COALESCE(sum(duration_ms), 0)                           AS total_duration_ms,
			COALESCE(sum(complexity), 0)                            AS total_complexity,
			COALESCE(uniqCombinedMerge(unique_users_state), 0)      AS unique_users,
			COALESCE(uniqCombinedMerge(unique_tokens_state), 0)     AS unique_tokens
		FROM periscope.api_usage_5m_v
		WHERE tenant_id = ?
		  AND window_start >= ?
		  AND window_start <  ?
		GROUP BY auth_type, operation_type, operation_name
	`, tenantID, startTime, endTime)
	if err != nil && !errors.Is(err, database.ErrNoRows) {
		return nil, fmt.Errorf("failed to query API usage aggregates from ClickHouse: %w", err)
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
				return nil, fmt.Errorf("scan API usage row: %w", scanErr)
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
		if iterErr := apiRows.Err(); iterErr != nil {
			return nil, fmt.Errorf("iterate API usage rows: %w", iterErr)
		}
	}

	totalStorageGB := 0.0
	for _, sm := range clusterStorageGB {
		totalStorageGB += sm.GBSecondsHot + sm.GBSecondsCold
	}
	totalProcessingSeconds := 0.0
	for _, proc := range clusterProcessing {
		totalProcessingSeconds += proc.Total()
	}
	totalStreamHours := 0.0
	totalStreamCount := 0
	for _, stream := range clusterStreamRuntime {
		totalStreamHours += stream.StreamHours
		totalStreamCount += stream.TotalStreams
	}
	hasUsage := totalStreamHours != 0 ||
		totalEgressGB != 0 ||
		totalViewerHours != 0 ||
		totalStorageGB != 0 ||
		len(clusterStorageProviderUsage) != 0 ||
		len(usageAdjustments) != 0 ||
		totalProcessingSeconds != 0 ||
		peakBandwidth != 0 ||
		totalUniqueViewers != 0 ||
		uniqueUsers != 0 ||
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

	// Make sure clusters that only had storage or processing (no
	// viewer/egress) still get a UsageSummary so those meters bill
	// against the right cluster's pricing.
	for cid := range clusterStorageGB {
		if _, ok := clusterMetrics[cid]; !ok {
			clusterMetrics[cid] = &clusterViewerMetrics{}
		}
	}
	for cid := range clusterStorageProviderUsage {
		if _, ok := clusterMetrics[cid]; !ok {
			clusterMetrics[cid] = &clusterViewerMetrics{}
		}
	}
	for cid := range usageAdjustments {
		if _, ok := clusterMetrics[cid]; !ok {
			clusterMetrics[cid] = &clusterViewerMetrics{}
		}
	}
	for cid := range clusterProcessing {
		if _, ok := clusterMetrics[cid]; !ok {
			clusterMetrics[cid] = &clusterViewerMetrics{}
		}
	}
	for cid := range clusterStreamRuntime {
		if _, ok := clusterMetrics[cid]; !ok {
			clusterMetrics[cid] = &clusterViewerMetrics{}
		}
	}

	// Period seconds for converting GiB-seconds to average GiB held over the
	// window for display-only summary fields. Purser rates the per-scope
	// GiB-seconds fields below.
	periodSeconds := endTime.Sub(startTime).Seconds()
	if periodSeconds <= 0 {
		periodSeconds = 1
	}
	for cid, vm := range clusterMetrics {
		sm := clusterStorageGB[cid]
		// The per-scope GBSeconds fields are what Purser emits as distinct
		// usage_records (cold=rated, hot=operational). DisplayStorageGB is a
		// display-only summary for consumers that need one average value.
		displayAvgGB := (sm.GBSecondsHot + sm.GBSecondsCold) / periodSeconds

		summary := &models.UsageSummary{
			TenantID:             tenantID,
			ClusterID:            cid,
			Period:               period,
			IngressGB:            sanitizeFloat(vm.IngressGB),
			EgressGB:             sanitizeFloat(vm.EgressGB),
			ViewerHours:          sanitizeFloat(vm.ViewerHours),
			TotalViewers:         vm.UniqueViewers,
			DisplayStorageGB:     displayAvgGB,
			StorageGBSecondsHot:  sm.GBSecondsHot,
			StorageGBSecondsCold: sm.GBSecondsCold,
			StorageProviderUsage: clusterStorageProviderUsage[cid],
			UsageAdjustments:     usageAdjustments[cid],
			Timestamp:            now,
		}

		// Per-cluster processing seconds. Each cluster's transcoding work
		// is attributed to that cluster's pricing model.
		if proc, ok := clusterProcessing[cid]; ok {
			summary.ProcessingSeconds = proc.ProcessCodecSeconds
			summary.LivepeerH264Seconds = sanitizeFloat(proc.LivepeerH264Seconds)
			summary.LivepeerVP9Seconds = sanitizeFloat(proc.LivepeerVP9Seconds)
			summary.LivepeerAV1Seconds = sanitizeFloat(proc.LivepeerAV1Seconds)
			summary.LivepeerHEVCSeconds = sanitizeFloat(proc.LivepeerHEVCSeconds)
			summary.NativeAvH264Seconds = sanitizeFloat(proc.NativeAvH264Seconds)
			summary.NativeAvVP9Seconds = sanitizeFloat(proc.NativeAvVP9Seconds)
			summary.NativeAvAV1Seconds = sanitizeFloat(proc.NativeAvAV1Seconds)
			summary.NativeAvHEVCSeconds = sanitizeFloat(proc.NativeAvHEVCSeconds)
			summary.NativeAvAACSeconds = sanitizeFloat(proc.NativeAvAACSeconds)
			summary.NativeAvOpusSeconds = sanitizeFloat(proc.NativeAvOpusSeconds)
		}

		if stream, ok := clusterStreamRuntime[cid]; ok {
			summary.StreamHours = stream.StreamHours
			summary.TotalStreams = stream.TotalStreams
			summary.MaxViewers = stream.MaxViewers
		}

		// Tenant-level metrics still attach to the primary cluster
		// (peaks, API counters, MTD unique users — these aren't naturally
		// cluster-scoped).
		if cid == primaryClusterID {
			summary.PeakBandwidthMbps = sanitizeFloat(peakBandwidth)
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
		"stream_hours":    totalStreamHours,
		"total_egress_gb": totalEgressGB,
		"viewer_hours":    totalViewerHours,
		"total_streams":   totalStreamCount,
	}).Info("Generated usage summaries for tenant")

	return summaries, nil
}

// clusterProcessingMetrics holds the per-codec second totals for one cluster.
type clusterProcessingMetrics struct {
	LivepeerH264Seconds float64
	LivepeerVP9Seconds  float64
	LivepeerAV1Seconds  float64
	LivepeerHEVCSeconds float64
	NativeAvH264Seconds float64
	NativeAvVP9Seconds  float64
	NativeAvAV1Seconds  float64
	NativeAvHEVCSeconds float64
	NativeAvAACSeconds  float64
	NativeAvOpusSeconds float64
	ProcessCodecSeconds map[string]float64
}

// Total returns the sum across all codecs — useful for a quick has-data check.
func (c clusterProcessingMetrics) Total() float64 {
	if len(c.ProcessCodecSeconds) > 0 {
		total := 0.0
		for _, seconds := range c.ProcessCodecSeconds {
			total += seconds
		}
		return total
	}
	return c.LivepeerH264Seconds + c.LivepeerVP9Seconds + c.LivepeerAV1Seconds + c.LivepeerHEVCSeconds +
		c.NativeAvH264Seconds + c.NativeAvVP9Seconds + c.NativeAvAV1Seconds + c.NativeAvHEVCSeconds +
		c.NativeAvAACSeconds + c.NativeAvOpusSeconds
}

func (c *clusterProcessingMetrics) add(processType, codec string, seconds float64) {
	codec = normalizedProcessingCodec(codec)
	if codec == "" || seconds == 0 {
		return
	}
	if c.ProcessCodecSeconds == nil {
		c.ProcessCodecSeconds = map[string]float64{}
	}
	c.ProcessCodecSeconds[processType+":"+codec] += seconds
	switch processType {
	case "Livepeer":
		switch codec {
		case "h264":
			c.LivepeerH264Seconds += seconds
		case "vp9":
			c.LivepeerVP9Seconds += seconds
		case "av1":
			c.LivepeerAV1Seconds += seconds
		case "hevc":
			c.LivepeerHEVCSeconds += seconds
		}
	case "AV":
		switch codec {
		case "h264":
			c.NativeAvH264Seconds += seconds
		case "vp9":
			c.NativeAvVP9Seconds += seconds
		case "av1":
			c.NativeAvAV1Seconds += seconds
		case "hevc":
			c.NativeAvHEVCSeconds += seconds
		case "aac":
			c.NativeAvAACSeconds += seconds
		case "opus":
			c.NativeAvOpusSeconds += seconds
		}
	}
}

func normalizedProcessingCodec(codec string) string {
	switch strings.ToLower(strings.TrimSpace(codec)) {
	case "h265":
		return "hevc"
	default:
		return strings.ToLower(strings.TrimSpace(codec))
	}
}

// queryClusterProcessingSeconds returns processing-second totals grouped by
// cluster_id for the period. Empty cluster_id is bucketed under the
// tenant's primary cluster.
func (bs *BillingSummarizer) queryClusterProcessingSeconds(ctx context.Context, tenantID string, startTime, endTime time.Time, primaryClusterID string) (map[string]clusterProcessingMetrics, error) {
	out := map[string]clusterProcessingMetrics{}
	// source_event_id is the logical fact identity. process_type, codec,
	// and track are materialized fields that may be corrected by replay;
	// grouping by them here would double-bill a format correction.
	rows, err := bs.clickhouse.QueryContext(ctx, `
		WITH window_candidates AS (
			SELECT
				tenant_id, node_id, stream_id, source_event_id,
				min(projection_version_ms) AS proj_first_in_window,
				argMax(process_type,   projection_version_ms) AS process_type,
				argMax(output_codec,   projection_version_ms) AS output_codec,
				argMax(track_type,     projection_version_ms) AS track_type,
				argMax(cluster_id,     projection_version_ms) AS cluster_id,
				argMax(media_seconds,  projection_version_ms) AS media_seconds
			FROM periscope.processing_segments_final
			WHERE tenant_id = ?
			  AND projection_version_ms >= ?
			  AND projection_version_ms <  ?
			GROUP BY tenant_id, node_id, stream_id, source_event_id
		)
		SELECT
			c.cluster_id AS cluster_id,
			c.process_type AS process_type,
			c.output_codec AS output_codec,
			sum(c.media_seconds) AS media_seconds
		FROM window_candidates c
		LEFT ANTI JOIN (
			SELECT DISTINCT tenant_id, node_id, stream_id, source_event_id
			FROM periscope.processing_segments_final
			WHERE tenant_id = ?
			  AND projection_version_ms < ?
			  AND (tenant_id, node_id, stream_id, source_event_id) IN (
			      SELECT tenant_id, node_id, stream_id, source_event_id FROM window_candidates
			  )
		) prior USING (tenant_id, node_id, stream_id, source_event_id)
		GROUP BY c.cluster_id, c.process_type, c.output_codec
	`, tenantID, startTime.UnixMilli(), endTime.UnixMilli(), tenantID, startTime.UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("processing_segments_final per cluster: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var cid, processType, outputCodec string
		var mediaSeconds float64
		if scanErr := rows.Scan(&cid, &processType, &outputCodec, &mediaSeconds); scanErr != nil {
			return nil, fmt.Errorf("scan processing row: %w", scanErr)
		}
		if cid == "" {
			cid = primaryClusterID
		}
		existing := out[cid]
		existing.add(processType, outputCodec, sanitizeFloat(mediaSeconds))
		out[cid] = existing
	}
	if iterErr := rows.Err(); iterErr != nil {
		return nil, fmt.Errorf("iterate processing rows: %w", iterErr)
	}
	return out, nil
}

// clusterStorageMetrics carries per-scope GiB-seconds for one cluster so
// the billing emitter can write distinct rated lines for cold (S3) and
// operational lines for hot (edge cache). See meter-contracts.md.
type clusterStorageMetrics struct {
	GBSecondsHot  float64
	GBSecondsCold float64
}

func storageUsageType(scope string) string {
	if scope == "cold" {
		return "storage_gb_seconds_cold"
	}
	return "storage_gb_seconds_hot"
}

func (bs *BillingSummarizer) queryClusterStorageProviderUsage(ctx context.Context, tenantID string, startTime, endTime time.Time, primaryClusterID string) (map[string][]models.StorageProviderUsage, error) {
	out := map[string][]models.StorageProviderUsage{}
	periodSeconds := endTime.Sub(startTime).Seconds()
	if periodSeconds <= 0 {
		return out, nil
	}
	rows, err := bs.clickhouse.QueryContext(ctx, `
		WITH points AS (
			SELECT
				tenant_id, cluster_id, storage_scope,
				storage_provider_tenant_id, storage_provider_cluster_id, storage_backend,
				? AS point_ts,
				argMax(total_bytes, tuple(timestamp, ingested_at_ms)) AS total_bytes
			FROM periscope.storage_snapshots
			WHERE tenant_id = ?
			  AND timestamp <= ?
			  AND ingested_at_ms < ?
			GROUP BY tenant_id, cluster_id, storage_scope,
			         storage_provider_tenant_id, storage_provider_cluster_id, storage_backend

			UNION ALL

			SELECT
				tenant_id, cluster_id, storage_scope,
				storage_provider_tenant_id, storage_provider_cluster_id, storage_backend,
				timestamp AS point_ts,
				argMax(total_bytes, ingested_at_ms) AS total_bytes
			FROM periscope.storage_snapshots
			WHERE tenant_id = ?
			  AND timestamp > ?
			  AND timestamp < ?
			  AND ingested_at_ms < ?
			GROUP BY tenant_id, cluster_id, storage_scope,
			         storage_provider_tenant_id, storage_provider_cluster_id, storage_backend,
			         point_ts

			UNION ALL

			SELECT
				tenant_id, cluster_id, storage_scope,
				storage_provider_tenant_id, storage_provider_cluster_id, storage_backend,
				? AS point_ts,
				argMax(total_bytes, tuple(timestamp, ingested_at_ms)) AS total_bytes
			FROM periscope.storage_snapshots
			WHERE tenant_id = ?
			  AND timestamp < ?
			  AND ingested_at_ms < ?
			GROUP BY tenant_id, cluster_id, storage_scope,
			         storage_provider_tenant_id, storage_provider_cluster_id, storage_backend
		),
		segments AS (
			SELECT
				cluster_id,
				storage_provider_tenant_id,
				storage_provider_cluster_id,
				storage_backend,
				storage_scope,
				point_ts,
				total_bytes,
				lead(point_ts, 1, point_ts) OVER (
					PARTITION BY tenant_id, cluster_id, storage_scope,
					             storage_provider_tenant_id, storage_provider_cluster_id, storage_backend
					ORDER BY point_ts
				) AS next_ts
			FROM points
		)
		SELECT
			cluster_id,
			storage_provider_tenant_id,
			storage_provider_cluster_id,
			storage_backend,
			storage_scope,
			sum((toFloat64(total_bytes) / pow(1024, 3)) * dateDiff('second', point_ts, next_ts)) AS gb_seconds
		FROM segments
		WHERE next_ts > point_ts
		  AND total_bytes != 0
		GROUP BY cluster_id, storage_provider_tenant_id, storage_provider_cluster_id,
		         storage_backend, storage_scope
		HAVING gb_seconds != 0
	`, startTime, tenantID, startTime, endTime.UnixMilli(),
		tenantID, startTime, endTime, endTime.UnixMilli(),
		endTime, tenantID, endTime, endTime.UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("storage snapshots provider usage: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var rec models.StorageProviderUsage
		var clusterID string
		if scanErr := rows.Scan(
			&clusterID,
			&rec.StorageProviderTenantID,
			&rec.StorageProviderClusterID,
			&rec.StorageBackend,
			&rec.StorageScope,
			&rec.GBSeconds,
		); scanErr != nil {
			return nil, fmt.Errorf("scan storage provider row: %w", scanErr)
		}
		if clusterID == "" {
			clusterID = primaryClusterID
		}
		rec.CustomerClusterID = clusterID
		rec.UsageType = storageUsageType(rec.StorageScope)
		rec.GBSeconds = sanitizeFloat(rec.GBSeconds)
		out[clusterID] = append(out[clusterID], rec)
	}
	if iterErr := rows.Err(); iterErr != nil {
		return nil, fmt.Errorf("iterate storage provider rows: %w", iterErr)
	}
	return out, nil
}

func storageMetricsFromProviderUsage(providerUsage map[string][]models.StorageProviderUsage) map[string]clusterStorageMetrics {
	out := map[string]clusterStorageMetrics{}
	for clusterID, records := range providerUsage {
		v := out[clusterID]
		for _, rec := range records {
			switch rec.StorageScope {
			case "cold":
				v.GBSecondsCold += sanitizeFloat(rec.GBSeconds)
			default:
				v.GBSecondsHot += sanitizeFloat(rec.GBSeconds)
			}
		}
		out[clusterID] = v
	}
	return out
}

func (bs *BillingSummarizer) queryUsageAdjustments(ctx context.Context, tenantID string, startTime, endTime time.Time, primaryClusterID string) (map[string][]models.UsageAdjustment, error) {
	out := map[string][]models.UsageAdjustment{}
	rows, err := bs.clickhouse.QueryContext(ctx, `
		SELECT observed_at_ms, table_name, meter, field,
		       natural_key_json, prior_value_json, new_value_json, source_event_id
		FROM periscope.projection_divergences
		WHERE observed_at_ms >= ?
		  AND observed_at_ms <  ?
		  AND table_name IN ('storage_gb_seconds_5m', 'viewer_sessions_final', 'stream_sessions_final', 'processing_segments_final')
		  AND JSONExtractString(natural_key_json, 'tenant_id') = ?
	`, startTime.UnixMilli(), endTime.UnixMilli(), tenantID)
	if err != nil {
		return nil, fmt.Errorf("projection_divergences query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var observedAtMS int64
		var tableName, meter, field, naturalKeyJSON, priorValueJSON, newValueJSON, sourceEventID string
		if scanErr := rows.Scan(&observedAtMS, &tableName, &meter, &field, &naturalKeyJSON, &priorValueJSON, &newValueJSON, &sourceEventID); scanErr != nil {
			return nil, fmt.Errorf("scan projection divergence: %w", scanErr)
		}
		alreadyBilled, billableErr := bs.divergenceAlreadyCursored(ctx, tableName, naturalKeyJSON, startTime)
		if billableErr != nil {
			return nil, billableErr
		}
		if !alreadyBilled {
			continue
		}
		adjustments, buildErr := usageAdjustmentsFromProjectionDivergence(
			tableName, meter, field, naturalKeyJSON, priorValueJSON, newValueJSON, sourceEventID, observedAtMS, primaryClusterID, startTime, endTime,
		)
		if buildErr != nil {
			return nil, buildErr
		}
		for _, adjustment := range adjustments {
			if adjustment.DeltaValue == 0 {
				continue
			}
			out[adjustment.ClusterID] = append(out[adjustment.ClusterID], adjustment)
		}
	}
	if iterErr := rows.Err(); iterErr != nil {
		return nil, fmt.Errorf("iterate projection divergences: %w", iterErr)
	}
	return out, nil
}

func (bs *BillingSummarizer) divergenceAlreadyCursored(ctx context.Context, tableName, naturalKeyJSON string, sliceStart time.Time) (bool, error) {
	var naturalKey map[string]any
	if err := json.Unmarshal([]byte(naturalKeyJSON), &naturalKey); err != nil {
		return false, fmt.Errorf("parse projection divergence natural key: %w", err)
	}

	queryFirstProjection := func(query string, args ...any) (bool, error) {
		var firstProjectionMS int64
		if err := bs.clickhouse.QueryRowContext(ctx, query, args...).Scan(&firstProjectionMS); err != nil {
			return false, err
		}
		if firstProjectionMS == 0 {
			return false, fmt.Errorf("projection divergence references %s key with no source projection: %s", tableName, naturalKeyJSON)
		}
		return firstProjectionMS < sliceStart.UnixMilli(), nil
	}

	switch tableName {
	case "viewer_sessions_final":
		return queryFirstProjection(`
			SELECT if(count() = 0, 0, min(projection_version_ms))
			FROM periscope.viewer_sessions_final
			WHERE tenant_id = toUUID(?)
			  AND node_id = ?
			  AND session_id = ?
		`, stringFromJSONMap(naturalKey, "tenant_id"), stringFromJSONMap(naturalKey, "node_id"), stringFromJSONMap(naturalKey, "session_id"))
	case "processing_segments_final":
		return queryFirstProjection(`
			SELECT if(count() = 0, 0, min(projection_version_ms))
			FROM periscope.processing_segments_final
			WHERE tenant_id = toUUID(?)
			  AND node_id = ?
			  AND stream_id = toUUID(?)
			  AND source_event_id = ?
		`, stringFromJSONMap(naturalKey, "tenant_id"), stringFromJSONMap(naturalKey, "node_id"), stringFromJSONMap(naturalKey, "stream_id"), stringFromJSONMap(naturalKey, "source_event_id"))
	case "stream_sessions_final":
		return queryFirstProjection(`
			SELECT if(count() = 0, 0, min(projection_version_ms))
			FROM periscope.stream_sessions_final
			WHERE tenant_id = toUUID(?)
			  AND node_id = ?
			  AND stream_id = toUUID(?)
			  AND source_event_id = ?
		`, stringFromJSONMap(naturalKey, "tenant_id"), stringFromJSONMap(naturalKey, "node_id"), stringFromJSONMap(naturalKey, "stream_id"), stringFromJSONMap(naturalKey, "source_event_id"))
	case "storage_gb_seconds_5m":
		windowStart, err := time.Parse(time.RFC3339, stringFromJSONMap(naturalKey, "window_start"))
		if err != nil {
			return false, fmt.Errorf("parse storage divergence window_start: %w", err)
		}
		if windowStart.Before(sliceStart) {
			return true, nil
		}
		return queryFirstProjection(`
			SELECT if(count() = 0, 0, min(projection_version_ms))
			FROM periscope.storage_gb_seconds_5m
			WHERE tenant_id = toUUID(?)
			  AND cluster_id = ?
			  AND storage_scope = ?
			  AND storage_provider_tenant_id = ?
			  AND storage_provider_cluster_id = ?
			  AND storage_backend = ?
			  AND window_start = parseDateTimeBestEffort(?)
		`,
			stringFromJSONMap(naturalKey, "tenant_id"),
			stringFromJSONMap(naturalKey, "cluster_id"),
			stringFromJSONMap(naturalKey, "storage_scope"),
			stringFromJSONMap(naturalKey, "storage_provider_tenant_id"),
			stringFromJSONMap(naturalKey, "storage_provider_cluster_id"),
			stringFromJSONMap(naturalKey, "storage_backend"),
			stringFromJSONMap(naturalKey, "window_start"),
		)
	default:
		return false, fmt.Errorf("unsupported projection divergence table %q", tableName)
	}
}

type projectionAdjustmentDelta struct {
	usageType         string
	clusterID         string
	deltaValue        float64
	processType       string
	outputCodec       string
	sourcePeriodStart time.Time
	sourcePeriodEnd   time.Time
}

func usageAdjustmentsFromProjectionDivergence(tableName, meter, field, naturalKeyJSON, priorValueJSON, newValueJSON, sourceEventID string, observedAtMS int64, primaryClusterID string, adjustmentPeriodStart, adjustmentPeriodEnd time.Time) ([]models.UsageAdjustment, error) {
	var naturalKey map[string]any
	if err := json.Unmarshal([]byte(naturalKeyJSON), &naturalKey); err != nil {
		return nil, fmt.Errorf("parse projection divergence natural key: %w", err)
	}
	var priorValue any
	if err := json.Unmarshal([]byte(priorValueJSON), &priorValue); err != nil {
		return nil, fmt.Errorf("parse projection divergence prior value: %w", err)
	}
	var newValue any
	if err := json.Unmarshal([]byte(newValueJSON), &newValue); err != nil {
		return nil, fmt.Errorf("parse projection divergence new value: %w", err)
	}

	clusterID := stringFromJSONMap(naturalKey, "cluster_id")
	if clusterID == "" {
		clusterID = primaryClusterID
	}

	deltas, err := adjustmentDeltasFromProjectionDivergence(tableName, field, naturalKey, priorValue, newValue, clusterID)
	if err != nil {
		return nil, err
	}

	out := make([]models.UsageAdjustment, 0, len(deltas))
	for _, delta := range deltas {
		if delta.clusterID == "" {
			delta.clusterID = primaryClusterID
		}
		sourceMaterial := fmt.Sprintf("%s|%s|%s|%s|%s|%f|%s|%s|%s|%s|%s|%s", tableName, meter, field, delta.usageType, delta.clusterID, delta.deltaValue, delta.processType, delta.outputCodec, naturalKeyJSON, priorValueJSON, newValueJSON, sourceEventID)
		sourceHash := sha1.Sum([]byte(sourceMaterial))
		details := models.JSONB{
			"table_name":       tableName,
			"meter":            meter,
			"field":            field,
			"natural_key":      naturalKey,
			"prior_value":      priorValue,
			"new_value":        newValue,
			"observed_at_ms":   observedAtMS,
			"source_event_id":  sourceEventID,
			"correction_scope": "additive_delta",
		}
		if delta.processType != "" {
			details["process_type"] = delta.processType
		}
		if delta.outputCodec != "" {
			details["output_codec"] = delta.outputCodec
		}
		if !delta.sourcePeriodStart.IsZero() && !delta.sourcePeriodEnd.IsZero() {
			details["source_period"] = map[string]string{"start": delta.sourcePeriodStart.Format(time.RFC3339), "end": delta.sourcePeriodEnd.Format(time.RFC3339)}
		}
		out = append(out, models.UsageAdjustment{
			SourceSystem: "periscope.projection_divergences",
			SourceID:     fmt.Sprintf("%x", sourceHash),
			UsageType:    delta.usageType,
			ClusterID:    delta.clusterID,
			DeltaValue:   sanitizeFloat(delta.deltaValue),
			PeriodStart:  adjustmentPeriodStart,
			PeriodEnd:    adjustmentPeriodEnd,
			Reason:       "projection_divergence",
			Details:      details,
		})
	}
	return out, nil
}

func adjustmentDeltasFromProjectionDivergence(tableName, field string, naturalKey map[string]any, priorValue, newValue any, clusterID string) ([]projectionAdjustmentDelta, error) {
	switch tableName {
	case "storage_gb_seconds_5m":
		sourceStart, err := time.Parse(time.RFC3339, stringFromJSONMap(naturalKey, "window_start"))
		if err != nil {
			return nil, fmt.Errorf("parse storage divergence window_start: %w", err)
		}
		scope := stringFromJSONMap(naturalKey, "storage_scope")
		priorMap, priorOK := priorValue.(map[string]any)
		newMap, newOK := newValue.(map[string]any)
		if !priorOK || !newOK {
			return nil, fmt.Errorf("storage divergence values must be JSON objects")
		}
		return []projectionAdjustmentDelta{{
			usageType:         storageUsageType(scope),
			clusterID:         clusterID,
			deltaValue:        floatFromJSONMap(newMap, "gb_seconds") - floatFromJSONMap(priorMap, "gb_seconds"),
			sourcePeriodStart: sourceStart,
			sourcePeriodEnd:   sourceStart.Add(5 * time.Minute),
		}}, nil
	case "viewer_sessions_final":
		switch field {
		case "duration_seconds":
			return []projectionAdjustmentDelta{{usageType: "delivered_minutes", clusterID: clusterID, deltaValue: (floatFromJSONValue(newValue) - floatFromJSONValue(priorValue)) / 60.0}}, nil
		case "uploaded_bytes":
			return []projectionAdjustmentDelta{{usageType: "ingress_gb", clusterID: clusterID, deltaValue: (floatFromJSONValue(newValue) - floatFromJSONValue(priorValue)) / math.Pow(1024, 3)}}, nil
		case "downloaded_bytes":
			return []projectionAdjustmentDelta{{usageType: "egress_gb", clusterID: clusterID, deltaValue: (floatFromJSONValue(newValue) - floatFromJSONValue(priorValue)) / math.Pow(1024, 3)}}, nil
		case "cluster_id":
			priorMap, priorOK := priorValue.(map[string]any)
			newMap, newOK := newValue.(map[string]any)
			if !priorOK || !newOK {
				return nil, fmt.Errorf("viewer cluster divergence values must be JSON objects")
			}
			priorCluster := stringFromJSONMap(priorMap, "cluster_id")
			newCluster := stringFromJSONMap(newMap, "cluster_id")
			return []projectionAdjustmentDelta{
				{usageType: "delivered_minutes", clusterID: priorCluster, deltaValue: -floatFromJSONMap(priorMap, "duration_seconds") / 60.0},
				{usageType: "ingress_gb", clusterID: priorCluster, deltaValue: -floatFromJSONMap(priorMap, "uploaded_bytes") / math.Pow(1024, 3)},
				{usageType: "egress_gb", clusterID: priorCluster, deltaValue: -floatFromJSONMap(priorMap, "downloaded_bytes") / math.Pow(1024, 3)},
				{usageType: "delivered_minutes", clusterID: newCluster, deltaValue: floatFromJSONMap(newMap, "duration_seconds") / 60.0},
				{usageType: "ingress_gb", clusterID: newCluster, deltaValue: floatFromJSONMap(newMap, "uploaded_bytes") / math.Pow(1024, 3)},
				{usageType: "egress_gb", clusterID: newCluster, deltaValue: floatFromJSONMap(newMap, "downloaded_bytes") / math.Pow(1024, 3)},
			}, nil
		default:
			return nil, fmt.Errorf("unsupported viewer divergence field %q", field)
		}
	case "stream_sessions_final":
		switch field {
		case "runtime_seconds":
			return []projectionAdjustmentDelta{{usageType: "stream_runtime_seconds", clusterID: clusterID, deltaValue: floatFromJSONValue(newValue) - floatFromJSONValue(priorValue)}}, nil
		case "cluster_id":
			priorMap, priorOK := priorValue.(map[string]any)
			newMap, newOK := newValue.(map[string]any)
			if !priorOK || !newOK {
				return nil, fmt.Errorf("stream cluster divergence values must be JSON objects")
			}
			priorCluster := stringFromJSONMap(priorMap, "cluster_id")
			newCluster := stringFromJSONMap(newMap, "cluster_id")
			return []projectionAdjustmentDelta{
				{usageType: "stream_runtime_seconds", clusterID: priorCluster, deltaValue: -floatFromJSONMap(priorMap, "runtime_seconds")},
				{usageType: "stream_runtime_seconds", clusterID: newCluster, deltaValue: floatFromJSONMap(newMap, "runtime_seconds")},
			}, nil
		default:
			return nil, fmt.Errorf("unsupported stream divergence field %q", field)
		}
	case "processing_segments_final":
		switch field {
		case "media_seconds":
			return []projectionAdjustmentDelta{{
				usageType:   "media_seconds",
				clusterID:   clusterID,
				deltaValue:  floatFromJSONValue(newValue) - floatFromJSONValue(priorValue),
				processType: stringFromJSONMap(naturalKey, "process_type"),
				outputCodec: stringFromJSONMap(naturalKey, "output_codec"),
			}}, nil
		case "cluster_id":
			priorMap, priorOK := priorValue.(map[string]any)
			newMap, newOK := newValue.(map[string]any)
			if !priorOK || !newOK {
				return nil, fmt.Errorf("processing cluster divergence values must be JSON objects")
			}
			return []projectionAdjustmentDelta{
				{
					usageType:   "media_seconds",
					clusterID:   stringFromJSONMap(priorMap, "cluster_id"),
					deltaValue:  -floatFromJSONMap(priorMap, "media_seconds"),
					processType: stringFromJSONMap(priorMap, "process_type"),
					outputCodec: stringFromJSONMap(priorMap, "output_codec"),
				},
				{
					usageType:   "media_seconds",
					clusterID:   stringFromJSONMap(newMap, "cluster_id"),
					deltaValue:  floatFromJSONMap(newMap, "media_seconds"),
					processType: stringFromJSONMap(newMap, "process_type"),
					outputCodec: stringFromJSONMap(newMap, "output_codec"),
				},
			}, nil
		case "identity":
			priorMap, priorOK := priorValue.(map[string]any)
			newMap, newOK := newValue.(map[string]any)
			if !priorOK || !newOK {
				return nil, fmt.Errorf("processing identity divergence values must be JSON objects")
			}
			return []projectionAdjustmentDelta{
				{
					usageType:   "media_seconds",
					clusterID:   stringFromJSONMap(priorMap, "cluster_id"),
					deltaValue:  -floatFromJSONMap(priorMap, "media_seconds"),
					processType: stringFromJSONMap(priorMap, "process_type"),
					outputCodec: stringFromJSONMap(priorMap, "output_codec"),
				},
				{
					usageType:   "media_seconds",
					clusterID:   stringFromJSONMap(newMap, "cluster_id"),
					deltaValue:  floatFromJSONMap(newMap, "media_seconds"),
					processType: stringFromJSONMap(newMap, "process_type"),
					outputCodec: stringFromJSONMap(newMap, "output_codec"),
				},
			}, nil
		default:
			return nil, fmt.Errorf("unsupported processing divergence field %q", field)
		}
	default:
		return nil, fmt.Errorf("unsupported projection divergence table %q", tableName)
	}
}

func stringFromJSONMap(m map[string]any, key string) string {
	v, _ := m[key].(string)
	return v
}

func floatFromJSONMap(m map[string]any, key string) float64 {
	return floatFromJSONValue(m[key])
}

func floatFromJSONValue(v any) float64 {
	switch v := v.(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case uint32:
		return float64(v)
	case uint64:
		return float64(v)
	case json.Number:
		f, _ := v.Float64()
		return f
	case string:
		f, _ := strconv.ParseFloat(v, 64)
		return f
	default:
		return 0
	}
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
	IngressGB       float64
	EgressGB        float64
	ViewerHours     float64
	UniqueViewers   int
}

func (bs *BillingSummarizer) queryTenantViewerMetrics(ctx context.Context, tenantID string, startTime, endTime time.Time) ([]tenantViewerMetricRow, error) {
	// Walks billable_at_ms over viewer_sessions_final using the two-step
	// CTE + LEFT ANTI JOIN pattern from docs/architecture/meter-contracts.md:
	// each session bills exactly once when its min(projection_version_ms)
	// first lands in the cursor window; later reprojections don't re-bill
	// because the anti-join filters out natural keys with an earlier
	// projection.
	rows, err := bs.clickhouse.QueryContext(ctx, `
		WITH window_candidates AS (
			SELECT
				tenant_id, node_id, session_id,
				min(projection_version_ms) AS proj_first_in_window,
				argMax(cluster_id,       projection_version_ms) AS cluster_id,
				argMax(duration_seconds, projection_version_ms) AS duration_seconds,
				argMax(uploaded_bytes,   projection_version_ms) AS uploaded_bytes,
				argMax(downloaded_bytes, projection_version_ms) AS downloaded_bytes,
				argMax(closed_reason,    projection_version_ms) AS closed_reason
			FROM periscope.viewer_sessions_final
			WHERE tenant_id = ?
			  AND projection_version_ms >= ?
			  AND projection_version_ms <  ?
			GROUP BY tenant_id, node_id, session_id
		)
		SELECT
			c.cluster_id AS cluster_id,
			''           AS origin_cluster_id,
			sum(c.uploaded_bytes) / pow(1024, 3)                      AS ingress_gb,
			sum(c.downloaded_bytes) / pow(1024, 3)                    AS egress_gb,
			sum(c.duration_seconds) / 3600.0                          AS viewer_hours,
			toInt64(uniqCombined(c.session_id))                       AS unique_viewers
		FROM window_candidates c
		LEFT ANTI JOIN (
			SELECT DISTINCT tenant_id, node_id, session_id
			FROM periscope.viewer_sessions_final
			WHERE tenant_id = ?
			  AND projection_version_ms < ?
			  AND (tenant_id, node_id, session_id) IN (
			      SELECT tenant_id, node_id, session_id FROM window_candidates
			  )
		) prior USING (tenant_id, node_id, session_id)
		WHERE c.closed_reason = 'final'
		GROUP BY c.cluster_id
	`, tenantID, startTime.UnixMilli(), endTime.UnixMilli(), tenantID, startTime.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []tenantViewerMetricRow
	for rows.Next() {
		var row tenantViewerMetricRow
		if scanErr := rows.Scan(&row.ClusterID, &row.OriginClusterID, &row.IngressGB, &row.EgressGB, &row.ViewerHours, &row.UniqueViewers); scanErr != nil {
			return nil, fmt.Errorf("scan viewer metric row: %w", scanErr)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate viewer metric rows: %w", err)
	}
	return out, nil
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

	return "", fmt.Errorf("tenant %s has no primary_cluster_id", tenantID)
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

	var failedTenants []string
	for _, tenantID := range tenants {
		if err := bs.processTenantPendingUsage(ctx, tenantID); err != nil {
			bs.logger.WithError(err).WithField("tenant_id", tenantID).Error("Failed to process pending usage for tenant")
			failedTenants = append(failedTenants, tenantID)
		}
	}
	if len(failedTenants) > 0 {
		return fmt.Errorf("failed to process pending usage for tenants: %s", strings.Join(failedTenants, ","))
	}
	return nil
}
func (bs *BillingSummarizer) processTenantPendingUsage(ctx context.Context, tenantID string) error {
	// Get last processed timestamp from cursor
	var lastProcessed time.Time
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		return bs.yugaDB.QueryRowContext(ctx, `
			SELECT last_processed_at FROM periscope.billing_cursors WHERE tenant_id = $1
		`, tenantID).Scan(&lastProcessed)
	})

	if errors.Is(err, sql.ErrNoRows) {
		// Default to 24 hours ago for new tenants/first run
		// This avoids reprocessing history forever if we add a new tenant
		lastProcessed = time.Now().Add(-24 * time.Hour)
		// Insert initial cursor
		err = database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
			_, execErr := bs.yugaDB.ExecContext(ctx, `
				INSERT INTO periscope.billing_cursors (tenant_id, last_processed_at, updated_at)
				VALUES ($1, $2, NOW())
			`, tenantID, lastProcessed)
			return execErr
		})
		if err != nil {
			return fmt.Errorf("failed to initialize cursor: %w", err)
		}
	} else if err != nil {
		return fmt.Errorf("failed to query cursor: %w", err)
	}

	// Canonical billing cursor: 5-minute aligned with 2-minute settlement
	// lag. Anything within `settlementLag` of now is still considered
	// "in-flight" — the canonical-ledger projection may not have settled
	// yet — so we deliberately don't emit it. See
	// docs/architecture/meter-contracts.md.
	const (
		billingCursorAlignment = 5 * time.Minute
		billingSettlementLag   = 2 * time.Minute
	)
	targetEnd := time.Now().Add(-billingSettlementLag).Truncate(billingCursorAlignment)

	// Snap any minute-aligned lastProcessed value up to the next 5-minute
	// boundary so emissions stay aligned with the canonical meter contract.
	if rem := lastProcessed.Sub(lastProcessed.Truncate(billingCursorAlignment)); rem > 0 {
		lastProcessed = lastProcessed.Truncate(billingCursorAlignment).Add(billingCursorAlignment)
	}

	// If no new aligned window to process, skip.
	if !targetEnd.After(lastProcessed) {
		return nil
	}

	// Walk the cursor in exact 5-minute slices. A single call from
	// lastProcessed to targetEnd can span hours/days on first-run, after
	// downtime, or following a manual cursor reset; emitting a single
	// wide-window summary makes Purser stamp it as hourly/daily and
	// quarantine the rated meters. Each slice emits a minute_5 summary
	// and advances the cursor independently so a mid-walk failure stops
	// where it landed instead of replaying the full window.
	sliceStart := lastProcessed
	for sliceStart.Before(targetEnd) {
		sliceEnd := sliceStart.Add(billingCursorAlignment)
		if sliceEnd.After(targetEnd) {
			sliceEnd = targetEnd
		}
		if sendErr := bs.processBillingSlice(ctx, tenantID, sliceStart, sliceEnd); sendErr != nil {
			return sendErr
		}
		sliceStart = sliceEnd
	}

	bs.logger.WithFields(logging.Fields{
		"tenant_id": tenantID,
		"start":     lastProcessed,
		"end":       targetEnd,
	}).Info("Successfully processed pending usage")
	return nil
}

// processBillingSlice generates the usage summary for one 5-minute aligned
// window, ships it to Purser, then advances the per-tenant cursor. Splitting
// this out keeps the slice-walk loop above readable and ensures the cursor
// advances exactly once per slice.
func (bs *BillingSummarizer) processBillingSlice(ctx context.Context, tenantID string, sliceStart, sliceEnd time.Time) error {
	summaries, err := bs.generateTenantUsageSummary(tenantID, sliceStart, sliceEnd)
	if err != nil {
		return fmt.Errorf("failed to generate summary: %w", err)
	}

	if len(summaries) > 0 {
		flat := make([]models.UsageSummary, 0, len(summaries))
		for _, s := range summaries {
			flat = append(flat, *s)
		}
		if sendErr := bs.sendUsageToPurser(flat); sendErr != nil {
			return fmt.Errorf("failed to send usage to Purser: %w", sendErr)
		}
	}

	// Cursor advances after every slice — even empty ones — so a steady
	// stream of zero-usage 5-min windows still moves the cursor forward.
	err = database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		_, execErr := bs.yugaDB.ExecContext(ctx, `
			UPDATE periscope.billing_cursors
			SET last_processed_at = $1, updated_at = NOW()
			WHERE tenant_id = $2
		`, sliceEnd, tenantID)
		return execErr
	})
	if err != nil {
		return fmt.Errorf("failed to update cursor: %w", err)
	}
	return nil
}
