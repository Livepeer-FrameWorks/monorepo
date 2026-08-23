package handlers

import (
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
	"time"

	"frameworks/api_analytics_query/internal/database/meteringdb"
	"frameworks/api_analytics_query/internal/database/periscopequerydb"
	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/models"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/tenants"

	"github.com/sirupsen/logrus"
)

const (
	gibibyte               = 1024 * 1024 * 1024
	billingCursorAlignment = 5 * time.Minute
	billingSettlementLag   = 2 * time.Minute
)

// sanitizeFloat returns 0.0 if f is NaN or Inf, otherwise returns f
func sanitizeFloat(f float64) float64 {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return 0
	}
	return f
}

func appendMeter(existing []models.MeterQuantity, meter, unit string, quantity float64, dimensions models.JSONB) []models.MeterQuantity {
	quantity = sanitizeFloat(quantity)
	if quantity == 0 {
		return existing
	}
	for i := range existing {
		if existing[i].Meter != meter || existing[i].Unit != unit {
			continue
		}
		if reflect.DeepEqual(existing[i].Dimensions, dimensions) {
			existing[i].Quantity += quantity
			return existing
		}
	}
	return append(existing, models.MeterQuantity{
		Meter:      meter,
		Unit:       unit,
		Quantity:   quantity,
		Dimensions: dimensions,
	})
}

// BillingSummarizer handles usage summarization for billing
type BillingSummarizer struct {
	postgresQueries       meteringdb.Querier
	clickhouse            database.ClickHouseConn
	logger                logging.Logger
	usageProducer         usageProducer
	resolvePrimaryCluster func(string) (string, error)
	billingTopic          string
	sourceID              string
	sourceRegion          string
	systemTenantID        string
}

type usageProducer interface {
	ProduceMessage(topic string, key, value []byte, headers map[string]string) error
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
	systemTenantID, err := tenants.RuntimeSystemTenantID()
	if err != nil {
		logger.WithError(err).Fatal("Invalid system tenant identity")
	}

	bs := &BillingSummarizer{
		postgresQueries: meteringdb.New(yugaDB),
		clickhouse:      clickhouse,
		logger:          logger,
		usageProducer:   kafkaProducer,
		billingTopic:    billingTopic,
		sourceID:        config.GetEnv("METERING_SOURCE_ID", "periscope-default"),
		sourceRegion:    config.GetEnv("METERING_SOURCE_REGION", ""),
		systemTenantID:  systemTenantID.String(),
	}
	bs.resolvePrimaryCluster = func(tenantID string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		tenantResp, callErr := quartermasterClient.GetTenant(ctx, tenantID)
		if callErr != nil {
			return "", fmt.Errorf("failed to call Quartermaster: %w", callErr)
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
	return bs
}

func (bs *BillingSummarizer) reportID(tenantID, clusterID string, startTime, endTime time.Time, kind string) string {
	material := strings.Join([]string{
		bs.sourceID,
		tenantID,
		clusterID,
		startTime.UTC().Format(time.RFC3339Nano),
		endTime.UTC().Format(time.RFC3339Nano),
		kind,
	}, "\x00")
	sum := sha256.Sum256([]byte(material))
	return fmt.Sprintf("%x", sum[:])
}

type activeViewerReservation struct {
	TenantID        string
	ClusterID       string
	DeliveredMinute float64
	EgressGB        float64
}

// PublishUsageReservations emits replaceable, absolute holds for open viewer
// sessions. USER_END remains the final rated fact; these values only make
// prepaid admission responsive while a long connection is still in flight.
func (bs *BillingSummarizer) PublishUsageReservations(ctx context.Context) error {
	rows, err := periscopequerydb.ActiveViewerReservations.Query(ctx, bs.clickhouse)
	if err != nil {
		return fmt.Errorf("query active viewer reservations: %w", err)
	}
	defer func() { _ = rows.Close() }()

	current := map[string]activeViewerReservation{}
	for rows.Next() {
		var reservation activeViewerReservation
		if scanErr := rows.Scan(&reservation.TenantID, &reservation.ClusterID, &reservation.DeliveredMinute, &reservation.EgressGB); scanErr != nil {
			return fmt.Errorf("scan active viewer reservation: %w", scanErr)
		}
		if strings.TrimSpace(reservation.ClusterID) == "" {
			return fmt.Errorf("active viewer reservation missing serving cluster for tenant %s", reservation.TenantID)
		}
		reservation.DeliveredMinute = sanitizeFloat(reservation.DeliveredMinute)
		reservation.EgressGB = sanitizeFloat(reservation.EgressGB)
		current[reservation.TenantID+"\x00"+reservation.ClusterID] = reservation
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return fmt.Errorf("iterate active viewer reservations: %w", rowsErr)
	}

	previousKeys, err := bs.postgresQueries.ListReservationKeys(ctx, bs.sourceID)
	if err != nil {
		return fmt.Errorf("query previous reservation keys: %w", err)
	}
	for _, previous := range previousKeys {
		key := previous.TenantID + "\x00" + previous.ClusterID
		if _, ok := current[key]; !ok {
			current[key] = activeViewerReservation{TenantID: previous.TenantID, ClusterID: previous.ClusterID}
		}
	}
	if len(current) == 0 {
		return nil
	}

	periodEnd := time.Now().UTC().Truncate(time.Minute)
	periodStart := periodEnd.Add(-time.Minute)
	sequence := uint64(periodEnd.Unix())
	summaries := make([]models.UsageSummary, 0, len(current))
	for _, reservation := range current {
		summary := models.UsageSummary{
			ReportKind: "reservation", SourceID: bs.sourceID, SourceRegion: bs.sourceRegion,
			Sequence: sequence, TenantID: reservation.TenantID, ClusterID: reservation.ClusterID,
			PeriodStart: periodStart, PeriodEnd: periodEnd, Complete: true,
		}
		summary.ReportID = bs.reportID(summary.TenantID, summary.ClusterID, periodStart, periodEnd, summary.ReportKind)
		summary.Meters = appendMeter(summary.Meters, "delivered_minutes", "minute", reservation.DeliveredMinute, nil)
		summary.Meters = appendMeter(summary.Meters, "egress_gb", "gibibyte", reservation.EgressGB, nil)
		summaries = append(summaries, summary)
	}
	if err := bs.sendUsageToPurser(summaries); err != nil {
		return err
	}
	for _, reservation := range current {
		if reservation.DeliveredMinute == 0 && reservation.EgressGB == 0 {
			if err := bs.postgresQueries.DeleteReservationKey(ctx, meteringdb.DeleteReservationKeyParams{
				SourceID: bs.sourceID, TenantID: reservation.TenantID, ClusterID: reservation.ClusterID,
			}); err != nil {
				return fmt.Errorf("release reservation key: %w", err)
			}
			continue
		}
		if err := bs.postgresQueries.UpsertReservationKey(ctx, meteringdb.UpsertReservationKeyParams{
			SourceID: bs.sourceID, TenantID: reservation.TenantID, ClusterID: reservation.ClusterID, LastSequence: int64(sequence),
		}); err != nil {
			return fmt.Errorf("persist reservation key: %w", err)
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
	rows, err := periscopequerydb.ActiveTenants.Query(context.Background(), bs.clickhouse,
		tenants.ServiceAccountUserID.String(), bs.systemTenantID, tenants.AnonymousTenantID.String())

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

func (bs *BillingSummarizer) getCursorTenants(ctx context.Context) ([]string, error) {
	tenants, err := bs.postgresQueries.ListBillingCursorTenants(ctx, bs.sourceID)
	if err != nil {
		return nil, fmt.Errorf("query billing cursor tenants: %w", err)
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

	clusterStreamRuntime, err := bs.queryClusterStreamRuntime(ctx, tenantID, startTime, endTime)
	if err != nil {
		return nil, fmt.Errorf("failed to query stream runtime metrics from ClickHouse: %w", err)
	}

	tenantMetrics, err := bs.queryTenantViewerMetrics(ctx, tenantID, startTime, endTime)
	if err != nil {
		return nil, fmt.Errorf("failed to query finalized viewer metrics from ClickHouse: %w", err)
	}

	clusterMetrics := map[string]*clusterViewerMetrics{}
	totalEgressGB := 0.0
	totalViewerHours := 0.0
	totalUniqueViewers := 0
	for _, m := range tenantMetrics {
		cid := m.BillableClusterID()
		if cid == "" {
			return nil, fmt.Errorf("viewer metric row missing billable cluster for tenant %s", tenantID)
		}
		cm := clusterViewerMetrics{
			IngressGB:     m.IngressGB,
			EgressGB:      m.EgressGB,
			ViewerHours:   m.ViewerHours,
			UniqueViewers: m.UniqueViewers,
		}
		if existing, ok := clusterMetrics[cid]; ok {
			existing.IngressGB += cm.IngressGB
			existing.EgressGB += cm.EgressGB
			existing.ViewerHours += cm.ViewerHours
			existing.UniqueViewers += cm.UniqueViewers
		} else {
			clusterMetrics[cid] = &cm
		}
		totalEgressGB += cm.EgressGB
		totalViewerHours += cm.ViewerHours
		totalUniqueViewers += cm.UniqueViewers
	}

	// Derive peak bandwidth from client_qoe_5m (avg_bw_out is in bytes/sec)
	var peakBandwidth float64
	err = periscopequerydb.PeakBandwidth.QueryRow(ctx, bs.clickhouse, tenantID, startTime, endTime).Scan(&peakBandwidth)
	if err != nil && !errors.Is(err, database.ErrNoRows) {
		return nil, fmt.Errorf("failed to query peak bandwidth from ClickHouse: %w", err)
	}

	// Calculate Month-to-Date (MTD) Unique Users for correct MAX aggregation in Billing
	firstOfMonth := time.Date(startTime.Year(), startTime.Month(), 1, 0, 0, 0, 0, startTime.Location())
	var uniqueUsers int
	err = periscopequerydb.MonthlyUniqueUsers.QueryRow(ctx, bs.clickhouse,
		tenantID, endTime.UnixMilli(), tenantID, firstOfMonth.UnixMilli(), endTime.UnixMilli()).Scan(&uniqueUsers)
	if err != nil && !errors.Is(err, database.ErrNoRows) {
		return nil, fmt.Errorf("failed to query finalized unique users from ClickHouse: %w", err)
	}

	clusterStorageProviderUsage, err := bs.queryClusterStorageProviderUsage(ctx, tenantID, startTime, endTime)
	if err != nil {
		return nil, fmt.Errorf("failed to query provider storage GiB-seconds from ClickHouse: %w", err)
	}
	clusterStorageGB := storageMetricsFromProviderUsage(clusterStorageProviderUsage)
	usageAdjustments, err := bs.queryUsageAdjustments(ctx, tenantID, startTime, endTime)
	if err != nil {
		return nil, fmt.Errorf("failed to query usage adjustments from ClickHouse: %w", err)
	}
	clusterProcessing, err := bs.queryClusterProcessingSeconds(ctx, tenantID, startTime, endTime)
	if err != nil {
		return nil, fmt.Errorf("failed to query processing seconds from ClickHouse: %w", err)
	}

	// API usage is cursored by ingestion time from the deduplicated source
	// facts. The source-time 5-minute ledger remains a dashboard projection;
	// using it here would lose a durable event delivered after its old window.
	var apiRequests, apiErrors, apiDurationMs, apiComplexity, llmInputTokens, llmOutputTokens float64
	var apiBreakdown []models.APIUsageBreakdown
	apiRows, err := periscopequerydb.APIUsageByDimension.Query(ctx, bs.clickhouse,
		tenantID, startTime.UnixMilli(), endTime.UnixMilli(), tenantID, startTime.UnixMilli())
	if err != nil && !errors.Is(err, database.ErrNoRows) {
		return nil, fmt.Errorf("failed to query API usage aggregates from ClickHouse: %w", err)
	} else if err == nil {
		defer func() { _ = apiRows.Close() }()
		for apiRows.Next() {
			var breakdown models.APIUsageBreakdown
			var uniqueUsers, uniqueTokens float64
			if scanErr := apiRows.Scan(
				&breakdown.AuthType,
				&breakdown.OperationType,
				&breakdown.OperationName,
				&breakdown.Service,
				&breakdown.LLMModel,
				&breakdown.LLMProvider,
				&breakdown.Requests,
				&breakdown.Errors,
				&breakdown.DurationMs,
				&breakdown.Complexity,
				&breakdown.LLMInputTokens,
				&breakdown.LLMOutputTokens,
				&uniqueUsers,
				&uniqueTokens,
			); scanErr != nil {
				return nil, fmt.Errorf("scan API usage row: %w", scanErr)
			}
			breakdown.Requests = sanitizeFloat(breakdown.Requests)
			breakdown.Errors = sanitizeFloat(breakdown.Errors)
			breakdown.DurationMs = sanitizeFloat(breakdown.DurationMs)
			breakdown.Complexity = sanitizeFloat(breakdown.Complexity)
			breakdown.LLMInputTokens = sanitizeFloat(breakdown.LLMInputTokens)
			breakdown.LLMOutputTokens = sanitizeFloat(breakdown.LLMOutputTokens)
			breakdown.UniqueUsers = sanitizeFloat(uniqueUsers)
			breakdown.UniqueTokens = sanitizeFloat(uniqueTokens)
			apiBreakdown = append(apiBreakdown, breakdown)
			apiRequests += breakdown.Requests
			apiErrors += breakdown.Errors
			apiDurationMs += breakdown.DurationMs
			apiComplexity += breakdown.Complexity
			llmInputTokens += breakdown.LLMInputTokens
			llmOutputTokens += breakdown.LLMOutputTokens
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
		apiComplexity != 0 ||
		llmInputTokens != 0 ||
		llmOutputTokens != 0

	if !hasUsage {
		bs.logger.WithField("tenant_id", tenantID).Debug("Emitting complete zero-usage report")
	}

	// Ensure the primary cluster exists in the map (for non-cluster-scoped metrics)
	if _, ok := clusterMetrics[primaryClusterID]; !ok {
		clusterMetrics[primaryClusterID] = &clusterViewerMetrics{}
	}

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

	for cid, vm := range clusterMetrics {
		sm := clusterStorageGB[cid]
		summary := &models.UsageSummary{
			ReportKind:       "finalized",
			SourceID:         bs.sourceID,
			SourceRegion:     bs.sourceRegion,
			Sequence:         uint64(endTime.Unix()),
			TenantID:         tenantID,
			ClusterID:        cid,
			PeriodStart:      startTime.UTC(),
			PeriodEnd:        endTime.UTC(),
			Complete:         true,
			ProviderUsage:    clusterStorageProviderUsage[cid],
			UsageAdjustments: usageAdjustments[cid],
		}
		summary.ReportID = bs.reportID(tenantID, cid, summary.PeriodStart, summary.PeriodEnd, summary.ReportKind)
		summary.Meters = appendMeter(summary.Meters, "ingress_gb", "gibibyte", sanitizeFloat(vm.IngressGB), nil)
		summary.Meters = appendMeter(summary.Meters, "egress_gb", "gibibyte", sanitizeFloat(vm.EgressGB), nil)
		summary.Meters = appendMeter(summary.Meters, "delivered_minutes", "minute", sanitizeFloat(vm.ViewerHours)*60, nil)
		summary.Meters = appendMeter(summary.Meters, "total_viewers", "viewer", float64(vm.UniqueViewers), nil)
		summary.Meters = appendMeter(summary.Meters, "storage_gb_seconds_hot", "gibibyte_second", sm.GBSecondsHot, nil)
		summary.Meters = appendMeter(summary.Meters, "storage_gb_seconds_cold", "gibibyte_second", sm.GBSecondsCold, nil)

		if proc, ok := clusterProcessing[cid]; ok {
			summary.Meters = append(summary.Meters, proc.Quantities()...)
		}

		if stream, ok := clusterStreamRuntime[cid]; ok {
			summary.Meters = appendMeter(summary.Meters, "stream_runtime_seconds", "second", stream.StreamHours*3600, nil)
			summary.Meters = appendMeter(summary.Meters, "total_streams", "stream", float64(stream.TotalStreams), nil)
			summary.Meters = appendMeter(summary.Meters, "max_viewers", "viewer", float64(stream.MaxViewers), nil)
		}

		// Tenant-level metrics still attach to the primary cluster
		// (peaks, API counters, MTD unique users — these aren't naturally
		// cluster-scoped).
		if cid == primaryClusterID {
			summary.Meters = appendMeter(summary.Meters, "peak_bandwidth_mbps", "megabit_per_second", sanitizeFloat(peakBandwidth), nil)
			summary.Meters = appendMeter(summary.Meters, "unique_users", "user", float64(uniqueUsers), nil)
			for _, breakdown := range apiBreakdown {
				dimensions := models.JSONB{
					"auth_type":      breakdown.AuthType,
					"operation_type": breakdown.OperationType,
					"service":        breakdown.Service,
				}
				summary.Meters = appendMeter(summary.Meters, "api_requests", "request", breakdown.Requests, dimensions)
				summary.Meters = appendMeter(summary.Meters, "api_errors", "request", breakdown.Errors, dimensions)
				summary.Meters = appendMeter(summary.Meters, "api_duration_ms", "millisecond", breakdown.DurationMs, dimensions)
				summary.Meters = appendMeter(summary.Meters, "api_complexity", "point", breakdown.Complexity, dimensions)
				llmDimensions := models.JSONB{
					"service":  breakdown.Service,
					"model":    breakdown.LLMModel,
					"provider": breakdown.LLMProvider,
				}
				summary.Meters = appendMeter(summary.Meters, "llm_input_tokens", "token", breakdown.LLMInputTokens, llmDimensions)
				summary.Meters = appendMeter(summary.Meters, "llm_output_tokens", "token", breakdown.LLMOutputTokens, llmDimensions)
				switch breakdown.OperationType {
				case "skipper_search_query":
					summary.Meters = appendMeter(summary.Meters, "search_requests", "request", breakdown.Requests, models.JSONB{
						"service": breakdown.Service, "provider": breakdown.LLMProvider,
					})
				case "skipper_embedding":
					summary.Meters = appendMeter(summary.Meters, "embedding_requests", "request", breakdown.Requests, models.JSONB{
						"service": breakdown.Service, "provider": breakdown.LLMProvider, "model": breakdown.LLMModel,
					})
				}
			}
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
	}).Debug("Generated usage summaries for tenant")

	return summaries, nil
}

type clusterViewerMetrics struct {
	IngressGB     float64
	EgressGB      float64
	ViewerHours   float64
	UniqueViewers int
}

type clusterStreamRuntimeMetrics struct {
	MaxViewers   int
	TotalStreams int
	StreamHours  float64
}

func (bs *BillingSummarizer) queryClusterStreamRuntime(ctx context.Context, tenantID string, startTime, endTime time.Time) (map[string]clusterStreamRuntimeMetrics, error) {
	rows, err := periscopequerydb.ClusterStreamRuntime.Query(ctx, bs.clickhouse,
		tenantID, startTime.UnixMilli(), endTime.UnixMilli(), tenantID, startTime.UnixMilli())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := map[string]clusterStreamRuntimeMetrics{}
	for rows.Next() {
		var cid string
		var m clusterStreamRuntimeMetrics
		if scanErr := rows.Scan(&cid, &m.MaxViewers, &m.TotalStreams, &m.StreamHours); scanErr != nil {
			return nil, fmt.Errorf("scan stream runtime row: %w", scanErr)
		}
		if cid == "" {
			return nil, fmt.Errorf("stream runtime row missing cluster_id for tenant %s", tenantID)
		}
		m.StreamHours = sanitizeFloat(m.StreamHours)
		out[cid] = m
	}
	if iterErr := rows.Err(); iterErr != nil {
		return nil, fmt.Errorf("iterate stream runtime rows: %w", iterErr)
	}
	return out, nil
}

// clusterProcessingMetrics holds product-shaped processing quantities for one
// executing cluster.
type clusterProcessingMetrics struct {
	meters []models.MeterQuantity
}

func (c clusterProcessingMetrics) Total() float64 {
	total := 0.0
	for _, meter := range c.meters {
		if meter.Meter == "transcode_rendition_seconds" {
			total += meter.Quantity
		}
	}
	return total
}

func (c clusterProcessingMetrics) Quantities() []models.MeterQuantity {
	return append([]models.MeterQuantity(nil), c.meters...)
}

func processingBackend(processType string) string {
	switch strings.ToLower(strings.TrimSpace(processType)) {
	case "livepeer":
		return "livepeer_network"
	case "av":
		return "native_av"
	case "ffmpeg":
		return "ffmpeg"
	default:
		return strings.ToLower(strings.TrimSpace(processType))
	}
}

func (c *clusterProcessingMetrics) add(processType, codec, trackType string, seconds float64, renditionCount int, renditionsJSON string) {
	codec = normalizedProcessingCodec(codec)
	backend := processingBackend(processType)
	if codec == "" || backend == "" || seconds <= 0 {
		return
	}
	dimensions := models.JSONB{
		"execution_backend": backend,
		"output_codec":      codec,
	}
	if trackType = strings.ToLower(strings.TrimSpace(trackType)); trackType != "" {
		dimensions["track_type"] = trackType
	}
	c.meters = appendMeter(c.meters, "transcode_input_seconds", "second", seconds, dimensions)

	var renditions []struct {
		Name string `json:"name"`
	}
	if strings.TrimSpace(renditionsJSON) != "" {
		if err := json.Unmarshal([]byte(renditionsJSON), &renditions); err != nil {
			renditions = nil
		}
	}
	if len(renditions) > 0 {
		for _, rendition := range renditions {
			profile := strings.TrimSpace(rendition.Name)
			if profile == "" {
				continue
			}
			profileDimensions := models.JSONB{}
			for key, value := range dimensions {
				profileDimensions[key] = value
			}
			profileDimensions["rendition_profile"] = profile
			c.meters = appendMeter(c.meters, "transcode_rendition_seconds", "second", seconds, profileDimensions)
		}
		return
	}
	if renditionCount <= 0 {
		renditionCount = 1
	}
	c.meters = appendMeter(c.meters, "transcode_rendition_seconds", "second", seconds*float64(renditionCount), dimensions)
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
func (bs *BillingSummarizer) queryClusterProcessingSeconds(ctx context.Context, tenantID string, startTime, endTime time.Time) (map[string]clusterProcessingMetrics, error) {
	out := map[string]clusterProcessingMetrics{}
	// source_event_id is the logical fact identity. process_type, codec,
	// and track are materialized fields that may be corrected by replay;
	// grouping by them here would double-bill a format correction.
	rows, err := periscopequerydb.ClusterProcessingSeconds.Query(ctx, bs.clickhouse,
		tenantID, startTime.UnixMilli(), endTime.UnixMilli(), tenantID, startTime.UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("processing_segments_final per cluster: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var cid, processType, outputCodec, trackType, renditionsJSON string
		var renditionCount int
		var mediaSeconds float64
		if scanErr := rows.Scan(&cid, &processType, &outputCodec, &trackType, &renditionCount, &renditionsJSON, &mediaSeconds); scanErr != nil {
			return nil, fmt.Errorf("scan processing row: %w", scanErr)
		}
		existing := out[cid]
		existing.add(processType, outputCodec, trackType, sanitizeFloat(mediaSeconds), renditionCount, renditionsJSON)
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

func (bs *BillingSummarizer) queryClusterStorageProviderUsage(ctx context.Context, tenantID string, startTime, endTime time.Time) (map[string][]models.ProviderUsage, error) {
	out := map[string][]models.ProviderUsage{}
	rows, err := periscopequerydb.ClusterStorageProviderUsage.Query(ctx, bs.clickhouse,
		tenantID, endTime.UnixMilli(), startTime.UnixMilli(), endTime.UnixMilli())
	if err != nil {
		return nil, fmt.Errorf("storage ledger provider usage: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var rec models.ProviderUsage
		var clusterID, storageBackend, storageScope string
		var quantity float64
		if scanErr := rows.Scan(
			&clusterID,
			&rec.ProviderTenantID,
			&rec.ProviderClusterID,
			&storageBackend,
			&storageScope,
			&quantity,
		); scanErr != nil {
			return nil, fmt.Errorf("scan storage provider row: %w", scanErr)
		}
		rec.Meter = models.MeterQuantity{
			Meter:    storageUsageType(storageScope),
			Unit:     "gibibyte_second",
			Quantity: sanitizeFloat(quantity),
			Dimensions: models.JSONB{
				"storage_backend": storageBackend,
				"storage_scope":   storageScope,
			},
		}
		out[clusterID] = append(out[clusterID], rec)
	}
	if iterErr := rows.Err(); iterErr != nil {
		return nil, fmt.Errorf("iterate storage provider rows: %w", iterErr)
	}
	return out, nil
}

func storageMetricsFromProviderUsage(providerUsage map[string][]models.ProviderUsage) map[string]clusterStorageMetrics {
	out := map[string]clusterStorageMetrics{}
	for clusterID, records := range providerUsage {
		v := out[clusterID]
		for _, rec := range records {
			switch rec.Meter.Meter {
			case "storage_gb_seconds_cold":
				v.GBSecondsCold += sanitizeFloat(rec.Meter.Quantity)
			default:
				v.GBSecondsHot += sanitizeFloat(rec.Meter.Quantity)
			}
		}
		out[clusterID] = v
	}
	return out
}

func (bs *BillingSummarizer) queryUsageAdjustments(ctx context.Context, tenantID string, startTime, endTime time.Time) (map[string][]models.UsageAdjustment, error) {
	out := map[string][]models.UsageAdjustment{}
	rows, err := periscopequerydb.UsageAdjustments.Query(ctx, bs.clickhouse,
		startTime.UnixMilli(), endTime.UnixMilli(), tenantID)
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
			tableName, meter, field, naturalKeyJSON, priorValueJSON, newValueJSON, sourceEventID, observedAtMS, startTime, endTime,
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

	queryFirstProjection := func(query periscopequerydb.Statement, args ...any) (bool, error) {
		var firstProjectionMS int64
		if err := query.QueryRow(ctx, bs.clickhouse, args...).Scan(&firstProjectionMS); err != nil {
			return false, err
		}
		if firstProjectionMS == 0 {
			return false, fmt.Errorf("projection divergence references %s key with no source projection: %s", tableName, naturalKeyJSON)
		}
		return firstProjectionMS < sliceStart.UnixMilli(), nil
	}

	switch tableName {
	case "viewer_sessions_final":
		return queryFirstProjection(periscopequerydb.FirstViewerSessionProjection,
			stringFromJSONMap(naturalKey, "tenant_id"), stringFromJSONMap(naturalKey, "node_id"), stringFromJSONMap(naturalKey, "session_id"))
	case "processing_segments_final":
		return queryFirstProjection(periscopequerydb.FirstProcessingSegmentProjection,
			stringFromJSONMap(naturalKey, "tenant_id"), stringFromJSONMap(naturalKey, "node_id"), stringFromJSONMap(naturalKey, "stream_id"), stringFromJSONMap(naturalKey, "source_event_id"))
	case "stream_sessions_final":
		return queryFirstProjection(periscopequerydb.FirstStreamSessionProjection,
			stringFromJSONMap(naturalKey, "tenant_id"), stringFromJSONMap(naturalKey, "node_id"), stringFromJSONMap(naturalKey, "stream_id"), stringFromJSONMap(naturalKey, "source_event_id"))
	case "storage_gb_seconds_5m":
		return queryFirstProjection(periscopequerydb.FirstStorageProjection,
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

func usageAdjustmentsFromProjectionDivergence(tableName, meter, field, naturalKeyJSON, priorValueJSON, newValueJSON, sourceEventID string, observedAtMS int64, adjustmentPeriodStart, adjustmentPeriodEnd time.Time) ([]models.UsageAdjustment, error) {
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
		return nil, fmt.Errorf("projection divergence %s missing cluster_id", sourceEventID)
	}

	deltas, err := adjustmentDeltasFromProjectionDivergence(tableName, field, naturalKey, priorValue, newValue, clusterID)
	if err != nil {
		return nil, err
	}

	out := make([]models.UsageAdjustment, 0, len(deltas))
	for _, delta := range deltas {
		if delta.clusterID == "" {
			return nil, fmt.Errorf("projection divergence %s produced adjustment without cluster_id", sourceEventID)
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
		priorGBSeconds, err := floatFromJSONMap(priorMap, "gb_seconds")
		if err != nil {
			return nil, err
		}
		newGBSeconds, err := floatFromJSONMap(newMap, "gb_seconds")
		if err != nil {
			return nil, err
		}
		return []projectionAdjustmentDelta{{
			usageType:         storageUsageType(scope),
			clusterID:         clusterID,
			deltaValue:        newGBSeconds - priorGBSeconds,
			sourcePeriodStart: sourceStart,
			sourcePeriodEnd:   sourceStart.Add(5 * time.Minute),
		}}, nil
	case "viewer_sessions_final":
		switch field {
		case "duration_seconds":
			prior, next, err := floatDeltaValues(priorValue, newValue)
			if err != nil {
				return nil, err
			}
			return []projectionAdjustmentDelta{{usageType: "delivered_minutes", clusterID: clusterID, deltaValue: (next - prior) / 60.0}}, nil
		case "uploaded_bytes":
			prior, next, err := floatDeltaValues(priorValue, newValue)
			if err != nil {
				return nil, err
			}
			return []projectionAdjustmentDelta{{usageType: "ingress_gb", clusterID: clusterID, deltaValue: (next - prior) / gibibyte}}, nil
		case "downloaded_bytes":
			prior, next, err := floatDeltaValues(priorValue, newValue)
			if err != nil {
				return nil, err
			}
			return []projectionAdjustmentDelta{{usageType: "egress_gb", clusterID: clusterID, deltaValue: (next - prior) / gibibyte}}, nil
		case "cluster_id":
			priorMap, priorOK := priorValue.(map[string]any)
			newMap, newOK := newValue.(map[string]any)
			if !priorOK || !newOK {
				return nil, fmt.Errorf("viewer cluster divergence values must be JSON objects")
			}
			priorCluster := stringFromJSONMap(priorMap, "cluster_id")
			newCluster := stringFromJSONMap(newMap, "cluster_id")
			priorDuration, err := floatFromJSONMap(priorMap, "duration_seconds")
			if err != nil {
				return nil, err
			}
			priorUploaded, err := floatFromJSONMap(priorMap, "uploaded_bytes")
			if err != nil {
				return nil, err
			}
			priorDownloaded, err := floatFromJSONMap(priorMap, "downloaded_bytes")
			if err != nil {
				return nil, err
			}
			newDuration, err := floatFromJSONMap(newMap, "duration_seconds")
			if err != nil {
				return nil, err
			}
			newUploaded, err := floatFromJSONMap(newMap, "uploaded_bytes")
			if err != nil {
				return nil, err
			}
			newDownloaded, err := floatFromJSONMap(newMap, "downloaded_bytes")
			if err != nil {
				return nil, err
			}
			return []projectionAdjustmentDelta{
				{usageType: "delivered_minutes", clusterID: priorCluster, deltaValue: -priorDuration / 60.0},
				{usageType: "ingress_gb", clusterID: priorCluster, deltaValue: -priorUploaded / gibibyte},
				{usageType: "egress_gb", clusterID: priorCluster, deltaValue: -priorDownloaded / gibibyte},
				{usageType: "delivered_minutes", clusterID: newCluster, deltaValue: newDuration / 60.0},
				{usageType: "ingress_gb", clusterID: newCluster, deltaValue: newUploaded / gibibyte},
				{usageType: "egress_gb", clusterID: newCluster, deltaValue: newDownloaded / gibibyte},
			}, nil
		default:
			return nil, fmt.Errorf("unsupported viewer divergence field %q", field)
		}
	case "stream_sessions_final":
		switch field {
		case "runtime_seconds":
			prior, next, err := floatDeltaValues(priorValue, newValue)
			if err != nil {
				return nil, err
			}
			return []projectionAdjustmentDelta{{usageType: "stream_runtime_seconds", clusterID: clusterID, deltaValue: next - prior}}, nil
		case "cluster_id":
			priorMap, priorOK := priorValue.(map[string]any)
			newMap, newOK := newValue.(map[string]any)
			if !priorOK || !newOK {
				return nil, fmt.Errorf("stream cluster divergence values must be JSON objects")
			}
			priorCluster := stringFromJSONMap(priorMap, "cluster_id")
			newCluster := stringFromJSONMap(newMap, "cluster_id")
			priorRuntime, err := floatFromJSONMap(priorMap, "runtime_seconds")
			if err != nil {
				return nil, err
			}
			newRuntime, err := floatFromJSONMap(newMap, "runtime_seconds")
			if err != nil {
				return nil, err
			}
			return []projectionAdjustmentDelta{
				{usageType: "stream_runtime_seconds", clusterID: priorCluster, deltaValue: -priorRuntime},
				{usageType: "stream_runtime_seconds", clusterID: newCluster, deltaValue: newRuntime},
			}, nil
		default:
			return nil, fmt.Errorf("unsupported stream divergence field %q", field)
		}
	case "processing_segments_final":
		switch field {
		case "media_seconds":
			prior, next, err := floatDeltaValues(priorValue, newValue)
			if err != nil {
				return nil, err
			}
			return []projectionAdjustmentDelta{{
				usageType:   "media_seconds",
				clusterID:   clusterID,
				deltaValue:  next - prior,
				processType: stringFromJSONMap(naturalKey, "process_type"),
				outputCodec: stringFromJSONMap(naturalKey, "output_codec"),
			}}, nil
		case "cluster_id":
			priorMap, priorOK := priorValue.(map[string]any)
			newMap, newOK := newValue.(map[string]any)
			if !priorOK || !newOK {
				return nil, fmt.Errorf("processing cluster divergence values must be JSON objects")
			}
			priorMediaSeconds, err := floatFromJSONMap(priorMap, "media_seconds")
			if err != nil {
				return nil, err
			}
			newMediaSeconds, err := floatFromJSONMap(newMap, "media_seconds")
			if err != nil {
				return nil, err
			}
			return []projectionAdjustmentDelta{
				{
					usageType:   "media_seconds",
					clusterID:   stringFromJSONMap(priorMap, "cluster_id"),
					deltaValue:  -priorMediaSeconds,
					processType: stringFromJSONMap(priorMap, "process_type"),
					outputCodec: stringFromJSONMap(priorMap, "output_codec"),
				},
				{
					usageType:   "media_seconds",
					clusterID:   stringFromJSONMap(newMap, "cluster_id"),
					deltaValue:  newMediaSeconds,
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
			priorMediaSeconds, err := floatFromJSONMap(priorMap, "media_seconds")
			if err != nil {
				return nil, err
			}
			newMediaSeconds, err := floatFromJSONMap(newMap, "media_seconds")
			if err != nil {
				return nil, err
			}
			return []projectionAdjustmentDelta{
				{
					usageType:   "media_seconds",
					clusterID:   stringFromJSONMap(priorMap, "cluster_id"),
					deltaValue:  -priorMediaSeconds,
					processType: stringFromJSONMap(priorMap, "process_type"),
					outputCodec: stringFromJSONMap(priorMap, "output_codec"),
				},
				{
					usageType:   "media_seconds",
					clusterID:   stringFromJSONMap(newMap, "cluster_id"),
					deltaValue:  newMediaSeconds,
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
	v, ok := m[key].(string)
	if !ok {
		return ""
	}
	return v
}

func floatFromJSONMap(m map[string]any, key string) (float64, error) {
	return floatFromJSONValue(m[key])
}

func floatDeltaValues(priorValue, newValue any) (float64, float64, error) {
	prior, err := floatFromJSONValue(priorValue)
	if err != nil {
		return 0, 0, err
	}
	next, err := floatFromJSONValue(newValue)
	if err != nil {
		return 0, 0, err
	}
	return prior, next, nil
}

func floatFromJSONValue(v any) (float64, error) {
	switch v := v.(type) {
	case float64:
		return v, nil
	case int:
		return float64(v), nil
	case int64:
		return float64(v), nil
	case uint32:
		return float64(v), nil
	case uint64:
		return float64(v), nil
	case json.Number:
		f, err := v.Float64()
		if err != nil {
			return 0, fmt.Errorf("parse numeric JSON value %q: %w", v.String(), err)
		}
		return f, nil
	case string:
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return 0, fmt.Errorf("parse numeric string %q: %w", v, err)
		}
		return f, nil
	default:
		return 0, fmt.Errorf("unsupported numeric JSON value %T", v)
	}
}

type tenantViewerMetricRow struct {
	ClusterID       string
	OriginClusterID string
	IngressGB       float64
	EgressGB        float64
	ViewerHours     float64
	UniqueViewers   int
}

func (r tenantViewerMetricRow) BillableClusterID() string {
	return strings.TrimSpace(r.ClusterID)
}

func (bs *BillingSummarizer) queryTenantViewerMetrics(ctx context.Context, tenantID string, startTime, endTime time.Time) ([]tenantViewerMetricRow, error) {
	// Walks billable_at_ms over viewer_sessions_final using the two-step
	// CTE + LEFT ANTI JOIN pattern from docs/architecture/meter-contracts.md:
	// each session bills exactly once when its min(projection_version_ms)
	// first lands in the cursor window; later reprojections don't re-bill
	// because the anti-join filters out natural keys with an earlier
	// projection.
	rows, err := periscopequerydb.TenantViewerMetrics.Query(ctx, bs.clickhouse,
		tenantID, startTime.UnixMilli(), endTime.UnixMilli(), tenantID, startTime.UnixMilli())
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
	if bs.resolvePrimaryCluster == nil {
		return "", fmt.Errorf("primary cluster resolver not initialized")
	}
	return bs.resolvePrimaryCluster(tenantID)
}

// sendUsageToPurser sends usage summaries to the Purser billing service via Kafka
func (bs *BillingSummarizer) sendUsageToPurser(summaries []models.UsageSummary) error {
	if bs.usageProducer == nil {
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

		// Final reports and their source-completion marker share a
		// source/window key. Kafka therefore cannot expose the marker to
		// Purser before every report that it certifies.
		messageKey := bs.usageMessageKey(summary)
		err = bs.usageProducer.ProduceMessage(
			bs.billingTopic,
			[]byte(messageKey),
			payload,
			map[string]string{
				"source":    "periscope-metering",
				"source_id": bs.sourceID,
				"report_id": summary.ReportID,
				"type":      "usage_summary",
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
	}).Debug("Successfully produced usage summaries to Kafka")

	if successCount < len(summaries) {
		return fmt.Errorf("failed to send some summaries")
	}

	return nil
}

func (bs *BillingSummarizer) usageMessageKey(summary models.UsageSummary) string {
	if summary.ReportKind == "finalized" || summary.ReportKind == "window_complete" {
		return bs.sourceID + "\x00" + summary.PeriodEnd.UTC().Format(time.RFC3339Nano)
	}
	return summary.TenantID
}

// ProcessPendingUsage processes all pending usage since the last cursor
func (bs *BillingSummarizer) ProcessPendingUsage(ctx context.Context) error {
	bs.logger.Info("Processing pending usage for all tenants")
	activation, err := bs.ensureSourceActivation(ctx)
	if err != nil {
		return fmt.Errorf("initialize metering source: %w", err)
	}

	// Get all active tenants
	tenants, err := bs.getActiveTenants()
	if err != nil {
		return fmt.Errorf("failed to get active tenants: %w", err)
	}
	cursorTenants, err := bs.getCursorTenants(ctx)
	if err != nil {
		return fmt.Errorf("failed to get billing cursor tenants: %w", err)
	}
	tenantSet := make(map[string]struct{}, len(tenants)+len(cursorTenants))
	mergedTenants := make([]string, 0, len(tenants)+len(cursorTenants))
	for _, tenantID := range append(tenants, cursorTenants...) {
		if _, seen := tenantSet[tenantID]; seen {
			continue
		}
		tenantSet[tenantID] = struct{}{}
		mergedTenants = append(mergedTenants, tenantID)
	}
	tenants = mergedTenants
	targetEnd := time.Now().Add(-billingSettlementLag).Truncate(billingCursorAlignment)

	var failedTenants []string
	for _, tenantID := range tenants {
		if err := bs.processTenantPendingUsage(ctx, tenantID, activation, targetEnd); err != nil {
			bs.logger.WithError(err).WithField("tenant_id", tenantID).Error("Failed to process pending usage for tenant")
			failedTenants = append(failedTenants, tenantID)
		}
	}
	if len(failedTenants) > 0 {
		return fmt.Errorf("failed to process pending usage for tenants: %s", strings.Join(failedTenants, ","))
	}
	return bs.publishWindowCompletions(ctx, activation, targetEnd)
}

func (bs *BillingSummarizer) ensureSourceActivation(ctx context.Context) (time.Time, error) {
	var activatedAt time.Time
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var queryErr error
		activatedAt, queryErr = bs.postgresQueries.EnsureMeteringSource(ctx, meteringdb.EnsureMeteringSourceParams{
			SourceID: bs.sourceID, SourceRegion: bs.sourceRegion, ActivatedAt: time.Now().UTC().Truncate(5 * time.Minute),
		})
		return queryErr
	})
	return activatedAt.UTC(), err
}

func (bs *BillingSummarizer) earliestCanonicalBillingFact(ctx context.Context, tenantID string) (time.Time, bool, error) {
	var firstMS sql.NullInt64
	err := periscopequerydb.EarliestCanonicalBillingFact.QueryRow(ctx, bs.clickhouse,
		tenantID, tenantID, tenantID, tenantID, tenantID).Scan(&firstMS)
	if err != nil {
		return time.Time{}, false, err
	}
	if !firstMS.Valid || firstMS.Int64 <= 0 {
		return time.Time{}, false, nil
	}
	return time.UnixMilli(firstMS.Int64), true, nil
}

func (bs *BillingSummarizer) processTenantPendingUsage(ctx context.Context, tenantID string, sourceActivatedAt, targetEnd time.Time) error {
	// Get last processed timestamp from cursor
	var lastProcessed time.Time
	err := database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
		var queryErr error
		lastProcessed, queryErr = bs.postgresQueries.GetBillingCursor(ctx, meteringdb.GetBillingCursorParams{
			SourceID: bs.sourceID, TenantID: tenantID,
		})
		return queryErr
	})

	if errors.Is(err, sql.ErrNoRows) {
		lastProcessed = sourceActivatedAt.UTC().Truncate(5 * time.Minute)
		if firstFact, ok, firstErr := bs.earliestCanonicalBillingFact(ctx, tenantID); firstErr != nil {
			return fmt.Errorf("find first canonical billing fact: %w", firstErr)
		} else if ok && firstFact.After(lastProcessed) {
			lastProcessed = firstFact.UTC().Truncate(5 * time.Minute)
		}
		// Insert initial cursor
		err = database.RetryPostgres(ctx, database.DefaultRetryAttempts, 25*time.Millisecond, func() error {
			return bs.postgresQueries.InitializeBillingCursor(ctx, meteringdb.InitializeBillingCursorParams{
				SourceID: bs.sourceID, TenantID: tenantID, LastProcessedAt: lastProcessed,
			})
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
	lastProcessed = alignBillingCursorStart(lastProcessed, billingCursorAlignment)

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

func (bs *BillingSummarizer) publishWindowCompletions(ctx context.Context, activation, targetEnd time.Time) error {
	completedThrough, err := bs.postgresQueries.GetMeteringSourceCompletion(ctx, bs.sourceID)
	if err != nil {
		return fmt.Errorf("read source completion cursor: %w", err)
	}
	start := activation.UTC().Truncate(billingCursorAlignment)
	if completedThrough.Valid && completedThrough.Time.After(start) {
		start = completedThrough.Time.UTC().Truncate(billingCursorAlignment)
	}
	if !targetEnd.After(start) {
		return nil
	}
	markers := bs.windowCompletionReports(start, targetEnd)
	if err := bs.sendUsageToPurser(markers); err != nil {
		return fmt.Errorf("publish source window completion: %w", err)
	}
	if err := bs.postgresQueries.UpdateMeteringSourceCompletion(ctx, meteringdb.UpdateMeteringSourceCompletionParams{
		SourceID: bs.sourceID, CompletedThrough: targetEnd,
	}); err != nil {
		return fmt.Errorf("advance source completion cursor: %w", err)
	}
	return nil
}

func (bs *BillingSummarizer) windowCompletionReports(start, targetEnd time.Time) []models.UsageSummary {
	markers := make([]models.UsageSummary, 0, int(targetEnd.Sub(start)/billingCursorAlignment))
	for windowStart := start; windowStart.Before(targetEnd); windowStart = windowStart.Add(billingCursorAlignment) {
		windowEnd := windowStart.Add(billingCursorAlignment)
		marker := models.UsageSummary{
			ReportKind: "window_complete", SourceID: bs.sourceID, SourceRegion: bs.sourceRegion,
			Sequence: uint64(windowEnd.Unix()), TenantID: bs.systemTenantID, ClusterID: "_source",
			PeriodStart: windowStart, PeriodEnd: windowEnd, Complete: true,
		}
		marker.ReportID = bs.reportID(marker.TenantID, marker.ClusterID, windowStart, windowEnd, marker.ReportKind)
		markers = append(markers, marker)
	}
	return markers
}

func alignBillingCursorStart(lastProcessed time.Time, alignment time.Duration) time.Time {
	if alignment <= 0 {
		return lastProcessed
	}
	return lastProcessed.UTC().Truncate(alignment)
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
		return bs.postgresQueries.AdvanceBillingCursor(ctx, meteringdb.AdvanceBillingCursorParams{
			LastProcessedAt: sliceEnd, SourceID: bs.sourceID, TenantID: tenantID,
		})
	})
	if err != nil {
		return fmt.Errorf("failed to update cursor: %w", err)
	}
	return nil
}
