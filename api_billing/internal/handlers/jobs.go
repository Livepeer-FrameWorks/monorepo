package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/sirupsen/logrus"

	"frameworks/pkg/billing"
	decklog "frameworks/pkg/clients/decklog"
	periscope "frameworks/pkg/clients/periscope"
	"frameworks/pkg/config"
	"frameworks/pkg/kafka"
	"frameworks/pkg/logging"
	"frameworks/pkg/models"
	pb "frameworks/pkg/proto"
)

// calculateCharges computes base and metered charges based on usage and pricing rules.
func calculateCharges(usageData map[string]float64, basePrice float64, meteringEnabled bool, overageRates models.OverageRates, storageAllocation, bandwidthAllocation models.AllocationDetails, customPricing models.CustomPricing, customAllocations models.AllocationDetails) (baseAmount, meteredAmount float64) {
	// Base price (custom override if provided)
	baseAmount = basePrice
	if customPricing.BasePrice > 0 {
		baseAmount = customPricing.BasePrice
	}

	if !meteringEnabled {
		return baseAmount, 0
	}

	// Effective allocations (custom overrides bandwidth allocation)
	effectiveBandwidthAllocation := bandwidthAllocation
	if customAllocations.Limit != nil {
		effectiveBandwidthAllocation = customAllocations
	}

	// Effective overage rates (custom overrides defaults)
	effectiveOverageRates := overageRates
	if customPricing.OverageRates.Bandwidth.UnitPrice > 0 {
		effectiveOverageRates.Bandwidth = customPricing.OverageRates.Bandwidth
	}
	if customPricing.OverageRates.Storage.UnitPrice > 0 {
		effectiveOverageRates.Storage = customPricing.OverageRates.Storage
	}
	if customPricing.OverageRates.Compute.UnitPrice > 0 {
		effectiveOverageRates.Compute = customPricing.OverageRates.Compute
	}
	if customPricing.OverageRates.Processing.H264RatePerMin > 0 {
		effectiveOverageRates.Processing = customPricing.OverageRates.Processing
	}

	// 1) Bandwidth (delivered minutes)
	viewerMinutes := usageData["viewer_hours"] * 60
	if effectiveBandwidthAllocation.Limit != nil && viewerMinutes > 0 && effectiveOverageRates.Bandwidth.UnitPrice > 0 {
		billable := viewerMinutes - *effectiveBandwidthAllocation.Limit
		if billable > 0 {
			meteredAmount += billable * effectiveOverageRates.Bandwidth.UnitPrice
		}
	}

	// 2) Storage overage
	storageUsage := usageData["average_storage_gb"]
	if storageAllocation.Limit != nil && storageUsage > 0 && effectiveOverageRates.Storage.UnitPrice > 0 {
		billable := storageUsage - *storageAllocation.Limit
		if billable > 0 {
			meteredAmount += billable * effectiveOverageRates.Storage.UnitPrice
		}
	}

	// 3) Compute overage (GPU hours)
	gpuHours := usageData["gpu_hours"]
	if gpuHours > 0 && effectiveOverageRates.Compute.UnitPrice > 0 {
		meteredAmount += gpuHours * effectiveOverageRates.Compute.UnitPrice
	}

	// 4) Processing/transcoding overage (per-codec pricing)
	processingRates := effectiveOverageRates.Processing
	if processingRates.H264RatePerMin > 0 {
		baseRate := processingRates.H264RatePerMin
		calcCodecCost := func(seconds float64, codec string) float64 {
			if seconds <= 0 {
				return 0
			}
			minutes := seconds / 60
			mult := processingRates.GetCodecMultiplier(codec)
			return minutes * baseRate * mult
		}

		meteredAmount += calcCodecCost(usageData["livepeer_h264_seconds"], "h264")
		meteredAmount += calcCodecCost(usageData["livepeer_vp9_seconds"], "vp9")
		meteredAmount += calcCodecCost(usageData["livepeer_av1_seconds"], "av1")
		meteredAmount += calcCodecCost(usageData["livepeer_hevc_seconds"], "hevc")
		meteredAmount += calcCodecCost(usageData["native_av_h264_seconds"], "h264")
		meteredAmount += calcCodecCost(usageData["native_av_vp9_seconds"], "vp9")
		meteredAmount += calcCodecCost(usageData["native_av_av1_seconds"], "av1")
		meteredAmount += calcCodecCost(usageData["native_av_hevc_seconds"], "hevc")
		meteredAmount += calcCodecCost(usageData["native_av_aac_seconds"], "aac")
		meteredAmount += calcCodecCost(usageData["native_av_opus_seconds"], "opus")
	}

	return baseAmount, meteredAmount
}

func loadSubscriptionPeriod(db *sql.DB, tenantID string, now time.Time) (time.Time, time.Time) {
	var start, end sql.NullTime
	err := db.QueryRow(`
		SELECT billing_period_start, billing_period_end
		FROM purser.tenant_subscriptions
		WHERE tenant_id = $1 AND status = 'active'
		ORDER BY created_at DESC
		LIMIT 1
	`, tenantID).Scan(&start, &end)
	if err == nil && start.Valid && end.Valid && end.Time.After(start.Time) {
		return start.Time, end.Time
	}

	periodStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	periodEnd := periodStart.AddDate(0, 1, 0)
	return periodStart, periodEnd
}

// enrichInvoiceFromPeriscope queries Periscope for accurate analytics data at invoice time.
// This provides correct unique counts (via uniqMerge), geographic breakdown, and averages
// that cannot be accurately rolled up through the Kafka pipeline.
func (jm *JobManager) enrichInvoiceFromPeriscope(ctx context.Context, tenantID string, periodStart, periodEnd time.Time) map[string]interface{} {
	if jm.periscopeClient == nil {
		return nil
	}

	timeRange := &periscope.TimeRangeOpts{
		StartTime: periodStart,
		EndTime:   periodEnd,
	}

	enrichment := make(map[string]interface{})

	// 1. Platform overview - unique counts, peaks, averages (pre-aggregated, no pagination)
	overview, err := jm.periscopeClient.GetPlatformOverview(ctx, tenantID, timeRange)
	if err != nil {
		jm.logger.WithError(err).WithField("tenant_id", tenantID).Warn("Failed to get platform overview for invoice enrichment")
	} else if overview != nil {
		enrichment["unique_users"] = overview.UniqueViewers
		enrichment["total_streams"] = overview.TotalStreams
		enrichment["total_viewers"] = overview.TotalViewers
		enrichment["avg_viewers"] = overview.AverageViewers
		enrichment["peak_concurrent_viewers"] = overview.PeakConcurrentViewers
	}

	// 2. Geographic distribution - pre-aggregated (no pagination needed)
	// Returns unique_countries, unique_cities, and top countries by viewer count with percentage
	geo, err := jm.periscopeClient.GetGeographicDistribution(ctx, tenantID, nil, timeRange, 100)
	if err != nil {
		jm.logger.WithError(err).WithField("tenant_id", tenantID).Warn("Failed to get geo data for invoice enrichment")
	} else if geo != nil {
		enrichment["unique_countries"] = geo.UniqueCountries
		enrichment["unique_cities"] = geo.UniqueCities

		// 3. Get hourly geo data for viewer_hours per country
		viewerHoursByCountry := make(map[string]float64)
		geoHourly, err := jm.periscopeClient.GetViewerGeoHourly(ctx, tenantID, nil, timeRange, nil)
		if err != nil {
			jm.logger.WithError(err).WithField("tenant_id", tenantID).Debug("Failed to get geo hourly data for invoice enrichment")
		} else if geoHourly != nil {
			for _, record := range geoHourly.Records {
				viewerHoursByCountry[record.CountryCode] += record.ViewerHours
			}
		}

		// Build geo breakdown with full data: count, percentage, viewer_hours
		if len(geo.TopCountries) > 0 {
			geoBreakdown := make([]models.CountryMetrics, 0, len(geo.TopCountries))
			for _, c := range geo.TopCountries {
				geoBreakdown = append(geoBreakdown, models.CountryMetrics{
					CountryCode: c.CountryCode,
					ViewerCount: int(c.ViewerCount),
					Percentage:  float64(c.Percentage),
					ViewerHours: viewerHoursByCountry[c.CountryCode],
				})
			}
			enrichment["geo_breakdown"] = geoBreakdown
		}
	}

	if len(enrichment) == 0 {
		return nil
	}

	jm.logger.WithFields(logging.Fields{
		"tenant_id":       tenantID,
		"enrichment_keys": len(enrichment),
	}).Debug("Invoice enriched from Periscope")

	return enrichment
}

// CommodoreClient is the interface for Commodore gRPC client used by JobManager and PurserServer
type CommodoreClient interface {
	TerminateTenantStreams(ctx context.Context, tenantID, reason string) (*pb.TerminateTenantStreamsResponse, error)
	InvalidateTenantCache(ctx context.Context, tenantID, reason string) (*pb.InvalidateTenantCacheResponse, error)
	GetTenantUserCount(ctx context.Context, tenantID string) (*pb.GetTenantUserCountResponse, error)
	GetTenantPrimaryUser(ctx context.Context, tenantID string) (*pb.GetTenantPrimaryUserResponse, error)
}

// JobManager handles background billing jobs
type JobManager struct {
	db                *sql.DB
	logger            logging.Logger
	emailService      *EmailService
	cryptoMonitor     *CryptoMonitor
	gasWalletMonitor  *GasWalletMonitor
	x402Reconciler    *X402Reconciler
	kafkaConsumer     *kafka.Consumer
	stopCh            chan struct{}
	billingTopic      string
	commodoreClient   CommodoreClient
	periscopeClient   *periscope.GRPCClient
	thresholdEnforcer *ThresholdEnforcer
}

// NewJobManager creates a new job manager
func NewJobManager(database *sql.DB, log logging.Logger, commodoreClient CommodoreClient, decklogSvc *decklog.BatchedClient, periscopeSvc *periscope.GRPCClient) *JobManager {
	// Initialize Kafka consumer
	brokers := strings.Split(config.GetEnv("KAFKA_BROKERS", "kafka:9092"), ",")
	clusterID := config.GetEnv("KAFKA_CLUSTER_ID", "local")
	clientID := config.GetEnv("KAFKA_CLIENT_ID", "purser")
	groupID := config.GetEnv("KAFKA_GROUP_ID", "purser-ingest")
	billingTopic := config.GetEnv("BILLING_KAFKA_TOPIC", "billing.usage_reports")
	kLogger := logrus.New() // Adapt logger

	// Consumer group for billing reports
	// Note: We reuse KAFKA_BROKERS but use a unique group ID to avoid collision with analytics consumers
	consumer, err := kafka.NewConsumer(brokers, groupID, clusterID, clientID, kLogger)
	if err != nil {
		log.WithError(err).Error("Failed to create Kafka consumer for billing")
		// Don't fatal here, allow API to start without consumer if needed
	}

	includeTestnets := config.GetEnv("X402_INCLUDE_TESTNETS", "false") == "true"
	emailSvc := NewEmailService(log)

	return &JobManager{
		db:                database,
		logger:            log,
		emailService:      emailSvc,
		cryptoMonitor:     NewCryptoMonitor(database, log, decklogSvc),
		gasWalletMonitor:  NewGasWalletMonitor(log),
		x402Reconciler:    NewX402Reconciler(database, log, includeTestnets),
		kafkaConsumer:     consumer,
		stopCh:            make(chan struct{}),
		billingTopic:      billingTopic,
		commodoreClient:   commodoreClient,
		periscopeClient:   periscopeSvc,
		thresholdEnforcer: NewThresholdEnforcer(database, log, commodoreClient, emailSvc),
	}
}

// Start begins all background jobs
func (jm *JobManager) Start(ctx context.Context) {
	jm.logger.Info("Starting billing job manager")

	// Start usage report consumer
	if jm.kafkaConsumer != nil {
		jm.kafkaConsumer.AddHandler(jm.billingTopic, jm.handleUsageReport)
		go func() {
			if err := jm.kafkaConsumer.Start(ctx); err != nil {
				jm.logger.WithError(err).Error("Kafka consumer exited with error")
			}
		}()
	}

	// Start crypto payment monitor
	go jm.cryptoMonitor.Start(ctx)

	// Start gas wallet balance monitor (Prometheus metric: gas_wallet_balance_eth)
	go jm.gasWalletMonitor.Start(ctx)

	// Start x402 settlement reconciler (confirms or fails pending settlements)
	go jm.x402Reconciler.Start(ctx)

	// Start invoice generation job
	go jm.runInvoiceGeneration(ctx)

	// Start payment retry job
	go jm.runPaymentRetry(ctx)

	// NOTE: Crypto sweeps happen OFFLINE with the master seed
	// The server only has xpub - cannot sign transactions

	// Start usage rollup + purge job
	go jm.runUsageRollups(ctx)

	// Start wallet cleanup job
	go jm.runWalletCleanup(ctx)
}

// Stop stops all background jobs
func (jm *JobManager) Stop() {
	jm.logger.Info("Stopping billing job manager")
	jm.cryptoMonitor.Stop()
	jm.gasWalletMonitor.Stop()
	jm.x402Reconciler.Stop()
	if jm.kafkaConsumer != nil {
		if err := jm.kafkaConsumer.Close(); err != nil {
			jm.logger.WithError(err).Warn("Failed to close Kafka consumer")
		}
	}
	close(jm.stopCh)
}

// handleUsageReport consumes billing usage reports from Kafka
func (jm *JobManager) handleUsageReport(ctx context.Context, msg kafka.Message) error {
	var summary models.UsageSummary
	if err := json.Unmarshal(msg.Value, &summary); err != nil {
		jm.logger.WithError(err).WithFields(logging.Fields{
			"topic":     msg.Topic,
			"partition": msg.Partition,
			"offset":    msg.Offset,
		}).Error("Failed to unmarshal usage summary from Kafka (skipping poison message)")
		return nil
	}

	if err := jm.processUsageSummary(summary, "kafka"); err != nil {
		jm.logger.WithError(err).WithFields(logging.Fields{
			"tenant_id": summary.TenantID,
			"period":    summary.Period,
		}).Error("Failed to process usage summary from Kafka")
		return err
	}

	// Check billing model to determine processing path
	billingModel, err := jm.getTenantBillingModel(summary.TenantID)
	if err != nil {
		jm.logger.WithError(err).WithField("tenant_id", summary.TenantID).Warn("Failed to get billing model, defaulting to postpaid")
		billingModel = "postpaid"
	}

	if billingModel == "prepaid" {
		// Prepaid: deduct usage cost from balance
		if err := jm.processPrepaidUsage(ctx, summary); err != nil {
			jm.logger.WithError(err).WithField("tenant_id", summary.TenantID).Warn("Failed to process prepaid usage")
		}
	} else {
		// Postpaid: update invoice draft
		if err := jm.updateInvoiceDraft(ctx, summary.TenantID); err != nil {
			jm.logger.WithError(err).WithField("tenant_id", summary.TenantID).Warn("Failed to update invoice draft")
		}
	}

	jm.logger.WithFields(logging.Fields{
		"tenant_id":     summary.TenantID,
		"period":        summary.Period,
		"billing_model": billingModel,
	}).Info("Processed usage summary from Kafka")

	return nil
}

// getTenantBillingModel returns the billing model for a tenant (prepaid or postpaid)
func (jm *JobManager) getTenantBillingModel(tenantID string) (string, error) {
	var billingModel string
	err := jm.db.QueryRow(`
		SELECT COALESCE(billing_model, 'postpaid')
		FROM purser.tenant_subscriptions
		WHERE tenant_id = $1 AND status = 'active'
		ORDER BY created_at DESC
		LIMIT 1
	`, tenantID).Scan(&billingModel)
	if errors.Is(err, sql.ErrNoRows) {
		return "postpaid", nil // Default for tenants without subscription
	}
	return billingModel, err
}

// buildUsageDataFromSummary extracts usage metrics from a UsageSummary into a map
// suitable for charge calculation. Reused by both prepaid and postpaid flows.
func buildUsageDataFromSummary(summary models.UsageSummary) map[string]float64 {
	return map[string]float64{
		"stream_hours":           summary.StreamHours,
		"viewer_hours":           summary.ViewerHours,
		"egress_gb":              summary.EgressGB,
		"peak_bandwidth_mbps":    summary.PeakBandwidthMbps,
		"average_storage_gb":     summary.AverageStorageGB,
		"max_viewers":            float64(summary.MaxViewers),
		"total_streams":          float64(summary.TotalStreams),
		"total_viewers":          float64(summary.TotalViewers),
		"unique_users":           float64(summary.UniqueUsers),
		"livepeer_h264_seconds":  summary.LivepeerH264Seconds,
		"livepeer_vp9_seconds":   summary.LivepeerVP9Seconds,
		"livepeer_av1_seconds":   summary.LivepeerAV1Seconds,
		"livepeer_hevc_seconds":  summary.LivepeerHEVCSeconds,
		"native_av_h264_seconds": summary.NativeAvH264Seconds,
		"native_av_vp9_seconds":  summary.NativeAvVP9Seconds,
		"native_av_av1_seconds":  summary.NativeAvAV1Seconds,
		"native_av_hevc_seconds": summary.NativeAvHEVCSeconds,
		"native_av_aac_seconds":  summary.NativeAvAACSeconds,
		"native_av_opus_seconds": summary.NativeAvOpusSeconds,
		"api_requests":           summary.APIRequests,
		"api_errors":             summary.APIErrors,
		"api_duration_ms":        summary.APIDurationMs,
		"api_complexity":         summary.APIComplexity,
	}
}

func usageSummaryReferenceID(summary models.UsageSummary) uuid.UUID {
	clusterID := summary.ClusterID
	if clusterID == "" {
		clusterID = "unknown"
	}
	raw := fmt.Sprintf("%s:%s:%s", summary.TenantID, clusterID, summary.Period)
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(raw))
}

// processPrepaidUsage calculates usage cost and deducts from prepaid balance
func (jm *JobManager) processPrepaidUsage(ctx context.Context, summary models.UsageSummary) error {
	// Get subscription tier info for pricing
	var (
		basePrice           float64
		meteringEnabled     bool
		overageRates        models.OverageRates
		storageAllocation   models.AllocationDetails
		bandwidthAllocation models.AllocationDetails
		customPricing       models.CustomPricing
		customAllocations   models.AllocationDetails
	)

	err := jm.db.QueryRow(`
		SELECT bt.base_price, bt.metering_enabled,
		       bt.overage_rates, bt.storage_allocation, bt.bandwidth_allocation,
		       ts.custom_pricing, ts.custom_allocations
		FROM purser.tenant_subscriptions ts
		JOIN purser.billing_tiers bt ON ts.tier_id = bt.id
		WHERE ts.tenant_id = $1 AND ts.status = 'active' AND bt.is_active = true
		ORDER BY ts.created_at DESC
		LIMIT 1
	`, summary.TenantID).Scan(&basePrice, &meteringEnabled,
		&overageRates, &storageAllocation, &bandwidthAllocation,
		&customPricing, &customAllocations)

	if errors.Is(err, sql.ErrNoRows) {
		jm.logger.WithField("tenant_id", summary.TenantID).Debug("No active subscription for prepaid usage")
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to get subscription: %w", err)
	}

	// Use the same usage data extraction as postpaid
	usageData := buildUsageDataFromSummary(summary)

	// Calculate metered usage cost (skip base price - that's monthly, not per-report)
	_, meteredAmount := calculateCharges(usageData, 0, meteringEnabled, overageRates, storageAllocation, bandwidthAllocation, customPricing, customAllocations)

	if meteredAmount <= 0 {
		return nil // No billable usage in this report
	}

	// Convert to cents (meteredAmount is in dollars)
	deductCents := int64(meteredAmount * 100)

	// Deduct from prepaid balance
	referenceID := usageSummaryReferenceID(summary)
	previousBalance, newBalanceCents, applied, err := jm.deductPrepaidBalanceForUsage(ctx, summary.TenantID, deductCents, fmt.Sprintf("Usage: %s", summary.Period), referenceID)
	if err != nil {
		return fmt.Errorf("failed to deduct prepaid balance: %w", err)
	}
	if !applied {
		jm.logger.WithFields(logging.Fields{
			"tenant_id":  summary.TenantID,
			"period":     summary.Period,
			"summary_id": referenceID.String(),
		}).Info("Skipped prepaid usage deduction for duplicate usage summary")
		return nil
	}

	jm.logger.WithFields(logging.Fields{
		"tenant_id":         summary.TenantID,
		"period":            summary.Period,
		"deducted_cents":    deductCents,
		"new_balance_cents": newBalanceCents,
	}).Info("Deducted prepaid usage")

	if jm.thresholdEnforcer != nil {
		if err := jm.thresholdEnforcer.EnforcePrepaidThresholds(ctx, summary.TenantID, previousBalance, newBalanceCents); err != nil {
			jm.logger.WithError(err).WithField("tenant_id", summary.TenantID).Warn("Failed to enforce prepaid thresholds")
		}
	}

	return nil
}

// deductPrepaidBalanceForCredit deducts amount from prepaid balance for non-usage adjustments.
// If referenceID is provided, the deduction is idempotent (duplicate calls are no-ops).
func (jm *JobManager) deductPrepaidBalanceForCredit(ctx context.Context, tenantID string, amountCents int64, description string, referenceID *string) (int64, bool, error) {
	var newBalance int64
	currency := billing.DefaultCurrency()
	referenceType := "invoice_credit"

	if referenceID != nil {
		if _, found, err := jm.getBalanceTransactionByReference(ctx, tenantID, referenceType, *referenceID); err != nil {
			return 0, false, err
		} else if found {
			balance, err := jm.getPrepaidBalance(tenantID)
			if err != nil {
				return 0, false, err
			}
			return balance, true, nil
		}
	}

	tx, err := jm.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, false, err
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort

	_, err = tx.ExecContext(ctx, `
		INSERT INTO purser.prepaid_balances (tenant_id, balance_cents, currency)
		VALUES ($1, 0, $2)
		ON CONFLICT (tenant_id, currency) DO NOTHING
	`, tenantID, currency)
	if err != nil {
		return 0, false, err
	}

	var currentBalance int64
	err = tx.QueryRowContext(ctx, `
		SELECT balance_cents
		FROM purser.prepaid_balances
		WHERE tenant_id = $1 AND currency = $2
		FOR UPDATE
	`, tenantID, currency).Scan(&currentBalance)
	if err != nil {
		return 0, false, err
	}

	newBalance = currentBalance - amountCents

	_, err = tx.ExecContext(ctx, `
		UPDATE purser.prepaid_balances
		SET balance_cents = $1, updated_at = NOW()
		WHERE tenant_id = $2 AND currency = $3
	`, newBalance, tenantID, currency)
	if err != nil {
		return 0, false, err
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO purser.balance_transactions (
			tenant_id, amount_cents, balance_after_cents,
			transaction_type, description, reference_id, reference_type, created_at
		) VALUES ($1, $2, $3, 'credit', $4, $5, $6, NOW())
	`, tenantID, -amountCents, newBalance, description, referenceID, referenceType)
	if err != nil {
		if referenceID != nil {
			var pqErr *pq.Error
			if errors.As(err, &pqErr) && pqErr.Code == "23505" {
				if rollbackErr := tx.Rollback(); rollbackErr != nil {
					jm.logger.WithError(rollbackErr).Warn("Failed to rollback duplicate credit deduction")
				}
				balance, balanceErr := jm.getPrepaidBalance(tenantID)
				if balanceErr != nil {
					return 0, false, balanceErr
				}
				return balance, true, nil
			}
		}
		return 0, false, err
	}

	if err := tx.Commit(); err != nil {
		return 0, false, err
	}

	if jm.thresholdEnforcer != nil {
		if err := jm.thresholdEnforcer.EnforcePrepaidThresholds(ctx, tenantID, currentBalance, newBalance); err != nil {
			jm.logger.WithError(err).WithField("tenant_id", tenantID).Warn("Failed to enforce prepaid thresholds for credit deduction")
		}
	}

	return newBalance, false, nil
}

// deductPrepaidBalanceForUsage deducts prepaid usage once per usage summary reference.
func (jm *JobManager) deductPrepaidBalanceForUsage(ctx context.Context, tenantID string, amountCents int64, description string, referenceID uuid.UUID) (int64, int64, bool, error) {
	var newBalance int64
	currency := billing.DefaultCurrency()

	tx, err := jm.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, false, err
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort

	_, err = tx.ExecContext(ctx, `
		INSERT INTO purser.prepaid_balances (tenant_id, balance_cents, currency)
		VALUES ($1, 0, $2)
		ON CONFLICT (tenant_id, currency) DO NOTHING
	`, tenantID, currency)
	if err != nil {
		return 0, 0, false, err
	}

	var currentBalance int64
	err = tx.QueryRowContext(ctx, `
		SELECT balance_cents
		FROM purser.prepaid_balances
		WHERE tenant_id = $1 AND currency = $2
		FOR UPDATE
	`, tenantID, currency).Scan(&currentBalance)
	if err != nil {
		return 0, 0, false, err
	}

	newBalance = currentBalance - amountCents
	result, err := tx.ExecContext(ctx, `
		INSERT INTO purser.balance_transactions (
			tenant_id, amount_cents, balance_after_cents,
			transaction_type, description, reference_id, reference_type, created_at
		) VALUES ($1, $2, $3, 'usage', $4, $5, 'usage_summary', NOW())
		ON CONFLICT (tenant_id, reference_type, reference_id)
			WHERE reference_type = 'usage_summary'
		DO NOTHING
	`, tenantID, -amountCents, newBalance, description, referenceID)
	if err != nil {
		return 0, 0, false, err
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return 0, 0, false, err
	}
	if rowsAffected == 0 {
		if commitErr := tx.Commit(); commitErr != nil {
			return 0, 0, false, commitErr
		}
		return currentBalance, currentBalance, false, nil
	}

	_, err = tx.ExecContext(ctx, `
		UPDATE purser.prepaid_balances
		SET balance_cents = $1, updated_at = NOW()
		WHERE tenant_id = $2 AND currency = $3
	`, newBalance, tenantID, currency)
	if err != nil {
		return 0, 0, false, err
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, false, err
	}

	return currentBalance, newBalance, true, nil
}

// getPrepaidBalance returns the current prepaid balance in cents for a tenant (0 if none exists)
func (jm *JobManager) getPrepaidBalance(tenantID string) (int64, error) {
	var balanceCents int64
	currency := billing.DefaultCurrency()
	err := jm.db.QueryRow(`
		SELECT balance_cents FROM purser.prepaid_balances
		WHERE tenant_id = $1 AND currency = $2
	`, tenantID, currency).Scan(&balanceCents)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return balanceCents, nil
}

func (jm *JobManager) getBalanceTransactionByReference(ctx context.Context, tenantID, referenceType, referenceID string) (int64, bool, error) {
	var amountCents int64
	err := jm.db.QueryRowContext(ctx, `
		SELECT amount_cents
		FROM purser.balance_transactions
		WHERE tenant_id = $1 AND reference_type = $2 AND reference_id = $3
		ORDER BY created_at DESC
		LIMIT 1
	`, tenantID, referenceType, referenceID).Scan(&amountCents)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	return amountCents, true, nil
}

// suspendTenantForBalance suspends a tenant due to negative prepaid balance
// This function is called when balance drops below -$10 (threshold defined in processPrepaidUsage)
//
//nolint:unused // retained for reference; threshold enforcer handles suspensions now
func (jm *JobManager) suspendTenantForBalance(ctx context.Context, tenantID string, balanceCents int64) error {
	// Update subscription status to 'suspended'
	// This blocks NEW ingests/streams via Foghorn (which checks suspension status)
	result, err := jm.db.Exec(`
		UPDATE purser.tenant_subscriptions
		SET status = 'suspended', updated_at = NOW()
		WHERE tenant_id = $1 AND status = 'active'
	`, tenantID)
	if err != nil {
		return err
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected > 0 {
		jm.logger.WithFields(logging.Fields{
			"tenant_id":     tenantID,
			"balance_cents": balanceCents,
		}).Warn("Suspended tenant due to negative prepaid balance")

		// Terminate all active streams for this tenant via Commodore -> Foghorn -> MistServer
		if jm.commodoreClient != nil {
			terminateCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			resp, err := jm.commodoreClient.TerminateTenantStreams(terminateCtx, tenantID, "insufficient_balance")
			if err != nil {
				jm.logger.WithError(err).WithField("tenant_id", tenantID).Error("Failed to terminate tenant streams on suspension")
			} else {
				jm.logger.WithFields(logging.Fields{
					"tenant_id":           tenantID,
					"streams_terminated":  resp.StreamsTerminated,
					"sessions_terminated": resp.SessionsTerminated,
					"stream_names":        resp.StreamNames,
				}).Info("Terminated tenant streams due to insufficient balance")
			}

			// Invalidate media plane caches so suspension takes effect immediately for new requests
			invalidateCtx, cancel2 := context.WithTimeout(ctx, 10*time.Second)
			defer cancel2()
			invalidateResp, err := jm.commodoreClient.InvalidateTenantCache(invalidateCtx, tenantID, "suspended")
			if err != nil {
				jm.logger.WithError(err).WithField("tenant_id", tenantID).Warn("Failed to invalidate tenant cache on suspension")
			} else {
				jm.logger.WithFields(logging.Fields{
					"tenant_id":           tenantID,
					"entries_invalidated": invalidateResp.EntriesInvalidated,
				}).Info("Invalidated media plane cache after suspension")
			}
		}

	}

	return nil
}

// runInvoiceGeneration generates monthly invoices for active tenants
func (jm *JobManager) runInvoiceGeneration(ctx context.Context) {
	ticker := time.NewTicker(24 * time.Hour) // Run daily
	defer ticker.Stop()

	jm.logger.Info("Starting invoice generation job")

	for {
		select {
		case <-ctx.Done():
			return
		case <-jm.stopCh:
			return
		case <-ticker.C:
			jm.generateMonthlyInvoices()
		}
	}
}

// generateMonthlyInvoices generates invoices for tenants due for billing
func (jm *JobManager) generateMonthlyInvoices() {
	jm.logger.Info("Running monthly invoice generation")

	now := time.Now()

	// Get all active tenant subscriptions with their tiers
	rows, err := jm.db.Query(`
		SELECT ts.tenant_id, ts.billing_email, ts.tier_id, ts.status,
		       ts.billing_period_start, ts.billing_period_end,
		       bt.tier_name, bt.display_name, bt.base_price, bt.currency, bt.billing_period,
		       bt.metering_enabled, bt.overage_rates, bt.storage_allocation, bt.bandwidth_allocation,
		       ts.custom_pricing, ts.custom_features, ts.custom_allocations
		FROM purser.tenant_subscriptions ts
		JOIN purser.billing_tiers bt ON ts.tier_id = bt.id
		WHERE ts.status = 'active' 
		  AND bt.is_active = true
		  AND (
			  (ts.billing_period_end IS NOT NULL AND ts.billing_period_end <= $1)
			  OR (ts.billing_period_end IS NULL AND (ts.next_billing_date IS NULL OR ts.next_billing_date <= $1))
		  )
	`, now)

	if err != nil {
		jm.logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to fetch tenant subscriptions for invoice generation")
		return
	}
	defer func() { _ = rows.Close() }()

	var invoicesGenerated int
	for rows.Next() {
		var tenantID, billingEmail, tierID, subscriptionStatus string
		var tierName, displayName, currency, billingPeriod string
		var basePrice float64
		var meteringEnabled bool
		var overageRates models.OverageRates
		var storageAllocation, bandwidthAllocation models.AllocationDetails
		var customPricing models.CustomPricing
		var customFeatures models.BillingFeatures
		var customAllocations models.AllocationDetails
		var billingPeriodStart, billingPeriodEnd sql.NullTime

		err = rows.Scan(&tenantID, &billingEmail, &tierID, &subscriptionStatus,
			&billingPeriodStart, &billingPeriodEnd,
			&tierName, &displayName, &basePrice, &currency, &billingPeriod,
			&meteringEnabled, &overageRates, &storageAllocation, &bandwidthAllocation,
			&customPricing, &customFeatures, &customAllocations)
		if err != nil {
			jm.logger.WithFields(logging.Fields{
				"error": err,
			}).Error("Error scanning tenant subscription data")
			continue
		}

		var periodStart, periodEnd time.Time
		if billingPeriodStart.Valid && billingPeriodEnd.Valid && billingPeriodEnd.Time.After(billingPeriodStart.Time) {
			periodStart = billingPeriodStart.Time
			periodEnd = billingPeriodEnd.Time
		} else {
			periodStart = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()).AddDate(0, -1, 0)
			periodEnd = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
		}

		if periodEnd.After(now) {
			continue // Billing period not closed yet
		}

		// Check if a finalized invoice already exists for the previous month
		var existingCount int
		err = jm.db.QueryRow(`
			SELECT COUNT(*) FROM purser.billing_invoices
			WHERE tenant_id = $1
			  AND period_start = $2
			  AND status != 'draft'
		`, tenantID, periodStart).Scan(&existingCount)
		if err != nil {
			jm.logger.WithFields(logging.Fields{
				"error":     err,
				"tenant_id": tenantID,
			}).Error("Error checking existing invoices")
			continue
		}
		if existingCount > 0 {
			continue // Invoice already finalized for this period
		}

		// Check for an existing draft invoice for the previous month
		var draftInvoiceID string
		_ = jm.db.QueryRow(`
			SELECT id FROM purser.billing_invoices
			WHERE tenant_id = $1
			  AND period_start = $2
			  AND status = 'draft'
			LIMIT 1
		`, tenantID, periodStart).Scan(&draftInvoiceID)

		// Aggregate rollup-able usage metrics for billing period
		// - SUM: flow metrics (viewer_hours, egress_gb, *_seconds)
		// - AVG: average_storage_gb
		// - MAX: peak_bandwidth_mbps, max_viewers
		// - SKIP: unique counts (from Periscope enrichment only - cannot roll up 5-min windows)
		usageData := map[string]float64{}

		usageRows, err := jm.db.Query(`
			SELECT usage_type,
				CASE
					WHEN usage_type = 'average_storage_gb' THEN AVG(usage_value)
					WHEN usage_type IN ('peak_bandwidth_mbps', 'max_viewers') THEN MAX(usage_value)
					ELSE SUM(usage_value)
				END as aggregated_value
			FROM purser.usage_records
			WHERE tenant_id = $1
			  AND period_start < $3
			  AND period_end > $2
			  AND usage_type NOT IN ('unique_users', 'total_streams', 'total_viewers', 'unique_users_period')
			GROUP BY usage_type
		`, tenantID, periodStart, periodEnd)

		if err != nil {
			jm.logger.WithError(err).WithField("tenant_id", tenantID).Error("Failed to fetch usage data")
		} else {
			defer func() { _ = usageRows.Close() }()
			for usageRows.Next() {
				var usageType string
				var val float64
				if scanErr := usageRows.Scan(&usageType, &val); scanErr == nil {
					usageData[usageType] = val
				}
			}
		}

		baseAmount, meteredAmount := calculateCharges(usageData, basePrice, meteringEnabled, overageRates, storageAllocation, bandwidthAllocation, customPricing, customAllocations)
		totalAmount := baseAmount + meteredAmount

		// Generate invoice
		invoiceID := uuid.New().String()
		dueDate := periodEnd.AddDate(0, 0, 14) // 14 days to pay

		// Determine invoice status
		status := "pending"
		if totalAmount == 0 {
			status = "paid"
		}

		// Build flat usage_details - all metrics at top level for email and API
		usageDetails := map[string]interface{}{
			"period_start": periodStart,
			"period_end":   periodEnd,
			"tier_info": map[string]interface{}{
				"tier_id":          tierID,
				"tier_name":        tierName,
				"display_name":     displayName,
				"base_price":       basePrice,
				"metering_enabled": meteringEnabled,
			},
		}

		// Add rollup-able billing metrics
		for k, v := range usageData {
			usageDetails[k] = v
		}

		// Add accurate unique counts and geo from Periscope (cannot be rolled up from 5-min windows)
		enrichCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		if enrichment := jm.enrichInvoiceFromPeriscope(enrichCtx, tenantID, periodStart, periodEnd); enrichment != nil {
			for k, v := range enrichment {
				usageDetails[k] = v
			}
		}
		cancel()

		// Marshal usage details
		usageJSON, err := json.Marshal(usageDetails)
		if err != nil {
			jm.logger.WithFields(logging.Fields{
				"error":     err,
				"tenant_id": tenantID,
			}).Error("Failed to marshal usage data")
			continue
		}

		// Store or finalize the invoice
		if draftInvoiceID != "" {
			_, err = jm.db.Exec(`
				UPDATE purser.billing_invoices
				SET amount = $1,
					base_amount = $2,
					metered_amount = $3,
					currency = $4,
					status = $5,
					due_date = $6,
					usage_details = $7,
					period_start = $8,
					period_end = $9,
					updated_at = NOW()
				WHERE id = $10
			`, totalAmount, baseAmount, meteredAmount, currency, status, dueDate, usageJSON, periodStart, periodEnd, draftInvoiceID)
			invoiceID = draftInvoiceID
		} else {
			_, err = jm.db.Exec(`
				INSERT INTO purser.billing_invoices (
					id, tenant_id, amount, currency, status, due_date,
					base_amount, metered_amount,
					usage_details, period_start, period_end,
					created_at, updated_at
				) VALUES (
					$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, NOW(), NOW()
				)
			`, invoiceID, tenantID, totalAmount, currency, status, dueDate, baseAmount, meteredAmount, usageJSON, periodStart, periodEnd)
		}

		if err != nil {
			jm.logger.WithFields(logging.Fields{
				"error":     err,
				"tenant_id": tenantID,
				"amount":    totalAmount,
			}).Error("Failed to create invoice")
			continue
		}

		// Update subscription billing period + next billing date
		periodDuration := periodEnd.Sub(periodStart)
		if periodDuration <= 0 {
			periodDuration = 30 * 24 * time.Hour
		}
		nextPeriodStart := periodEnd
		nextPeriodEnd := periodEnd.Add(periodDuration)
		nextBillingDate := nextPeriodEnd
		_, err = jm.db.Exec(`
			UPDATE purser.tenant_subscriptions 
			SET next_billing_date = $1,
			    billing_period_start = $2,
			    billing_period_end = $3,
			    updated_at = NOW()
			WHERE tenant_id = $4
		`, nextBillingDate, nextPeriodStart, nextPeriodEnd, tenantID)

		if err != nil {
			jm.logger.WithError(err).WithField("tenant_id", tenantID).Error("Failed to update next billing date")
		}

		invoicesGenerated++
		jm.logger.WithFields(logging.Fields{
			"invoice_id":       invoiceID,
			"tenant_id":        tenantID,
			"tier_name":        tierName,
			"base_amount":      totalAmount - meteredAmount,
			"metered_amount":   meteredAmount,
			"total_amount":     totalAmount,
			"currency":         currency,
			"due_date":         dueDate,
			"metering_enabled": meteringEnabled,
		}).Info("Generated monthly invoice")

		// Send invoice created email notification
		if billingEmail != "" {
			// usageDetails already has usage_data and enrichment from Periscope
			err = jm.emailService.SendInvoiceCreatedEmail(billingEmail, "", invoiceID, totalAmount, currency, dueDate, usageDetails)
			if err != nil {
				jm.logger.WithError(err).WithFields(logging.Fields{
					"billing_email": billingEmail,
					"invoice_id":    invoiceID,
				}).Error("Failed to send invoice created email")
			}
		}
	}

	jm.logger.WithFields(logging.Fields{
		"invoices_generated": invoicesGenerated,
	}).Info("Monthly invoice generation completed")
}

func (jm *JobManager) runUsageRollups(ctx context.Context) {
	jm.logger.Info("Starting usage rollup job")

	for {
		nextRun := nextUTCStart(1)
		timer := time.NewTimer(time.Until(nextRun))
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-jm.stopCh:
			timer.Stop()
			return
		case <-timer.C:
		}

		if err := jm.rollupAndPurgeUsage(ctx); err != nil {
			jm.logger.WithError(err).Error("Usage rollup job failed")
		}
	}
}

func (jm *JobManager) rollupAndPurgeUsage(ctx context.Context) error {
	now := time.Now()
	hourlyCutoff := now.Add(-7 * 24 * time.Hour)
	dailyCutoff := now.Add(-90 * 24 * time.Hour)

	if err := jm.rollupUsageRecords(ctx, "hourly", "daily", "day", "1 day", hourlyCutoff); err != nil {
		return err
	}
	if err := jm.rollupUsageRecords(ctx, "daily", "monthly", "month", "1 month", dailyCutoff); err != nil {
		return err
	}

	if err := jm.purgeUsageRecords(ctx, "hourly", now.Add(-8*24*time.Hour)); err != nil {
		return err
	}
	if err := jm.purgeUsageRecords(ctx, "daily", now.Add(-91*24*time.Hour)); err != nil {
		return err
	}

	jm.logger.Info("Usage rollup + purge completed")
	return nil
}

func (jm *JobManager) rollupUsageRecords(ctx context.Context, fromGranularity, toGranularity, truncUnit, interval string, cutoff time.Time) error {
	// Defense-in-depth: these values are string-formatted into SQL below.
	// Keep the allowed set explicit so callers can't accidentally widen input surface.
	switch truncUnit {
	case "day", "month":
	default:
		return fmt.Errorf("invalid truncUnit: %s", truncUnit)
	}
	switch interval {
	case "1 day", "1 month":
	default:
		return fmt.Errorf("invalid interval: %s", interval)
	}
	switch toGranularity {
	case "daily", "monthly":
	default:
		return fmt.Errorf("invalid toGranularity: %s", toGranularity)
	}

	maxTypes := "'peak_bandwidth_mbps', 'max_viewers', 'total_streams', 'total_viewers', 'unique_users', 'unique_users_period', 'livepeer_unique_streams', 'native_av_unique_streams'"
	avgTypes := "'average_storage_gb'"
	query := fmt.Sprintf(`
		WITH base AS (
			SELECT tenant_id, cluster_id, usage_type,
			       date_trunc('%s', period_start) AS period_start,
			       date_trunc('%s', period_start) + INTERVAL '%s' AS period_end,
			       usage_value
			FROM purser.usage_records
			WHERE granularity = $1
			  AND period_start < $2
		),
		meta AS (
			SELECT DISTINCT ON (tenant_id, cluster_id, period_start)
			       tenant_id, cluster_id, period_start, usage_details
			FROM (
				SELECT tenant_id, cluster_id,
				       date_trunc('%s', period_start) AS period_start,
				       usage_details, created_at
				FROM purser.usage_records
				WHERE granularity = $1
				  AND period_start < $2
				  AND usage_details IS NOT NULL
				  AND usage_details ? 'geo_breakdown'
			) latest
			ORDER BY tenant_id, cluster_id, period_start, created_at DESC
		),
		aggregated AS (
			SELECT tenant_id, cluster_id, usage_type, period_start, period_end,
			       CASE
			           WHEN usage_type IN (%s) THEN MAX(usage_value)
			           WHEN usage_type IN (%s) THEN AVG(usage_value)
			           ELSE SUM(usage_value)
			       END AS usage_value
			FROM base
			GROUP BY tenant_id, cluster_id, usage_type, period_start, period_end
		)
		INSERT INTO purser.usage_records (
			tenant_id, cluster_id, usage_type, usage_value, usage_details,
			period_start, period_end, granularity, created_at
		)
		SELECT a.tenant_id, a.cluster_id, a.usage_type, a.usage_value,
		       COALESCE(m.usage_details, '{}'::jsonb),
		       a.period_start, a.period_end, '%s', NOW()
		FROM aggregated a
		LEFT JOIN meta m
		  ON m.tenant_id = a.tenant_id
		 AND m.cluster_id = a.cluster_id
		 AND m.period_start = a.period_start
		ON CONFLICT (tenant_id, cluster_id, usage_type, period_start, period_end) DO UPDATE SET
			usage_value = EXCLUDED.usage_value,
			usage_details = EXCLUDED.usage_details,
			granularity = EXCLUDED.granularity,
			updated_at = NOW()
	`, truncUnit, truncUnit, interval, truncUnit, maxTypes, avgTypes, toGranularity)

	_, err := jm.db.ExecContext(ctx, query, fromGranularity, cutoff)
	if err != nil {
		return fmt.Errorf("rollup %s -> %s failed: %w", fromGranularity, toGranularity, err)
	}

	jm.logger.WithFields(logging.Fields{
		"from":   fromGranularity,
		"to":     toGranularity,
		"cutoff": cutoff,
	}).Info("Rolled up usage records")

	return nil
}

func (jm *JobManager) purgeUsageRecords(ctx context.Context, granularity string, cutoff time.Time) error {
	_, err := jm.db.ExecContext(ctx, `
		DELETE FROM purser.usage_records
		WHERE granularity = $1
		  AND period_start < $2
	`, granularity, cutoff)
	if err != nil {
		return fmt.Errorf("purge %s usage records failed: %w", granularity, err)
	}
	return nil
}

func nextUTCStart(hour int) time.Time {
	now := time.Now().UTC()
	next := time.Date(now.Year(), now.Month(), now.Day(), hour, 0, 0, 0, time.UTC)
	if !next.After(now) {
		next = next.Add(24 * time.Hour)
	}
	return next
}

// runPaymentRetry retries failed payments and sends reminders
func (jm *JobManager) runPaymentRetry(ctx context.Context) {
	ticker := time.NewTicker(4 * time.Hour) // Run every 4 hours
	defer ticker.Stop()

	jm.logger.Info("Starting payment retry job")

	for {
		select {
		case <-ctx.Done():
			return
		case <-jm.stopCh:
			return
		case <-ticker.C:
			jm.retryFailedPayments()
			jm.sendPaymentReminders()
		}
	}
}

// retryFailedPayments retries payments that failed due to temporary issues
func (jm *JobManager) retryFailedPayments() {
	// Mark failed traditional payments for retry (crypto payments don't need retry)
	_, err := jm.db.Exec(`
		UPDATE purser.billing_payments
		SET status = 'pending', updated_at = NOW()
		WHERE status = 'failed' 
		  AND method IN ('mollie')
		  AND created_at > NOW() - INTERVAL '24 hours'
		  AND updated_at < NOW() - INTERVAL '1 hour'
	`)

	if err != nil {
		jm.logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to retry payments")
	} else {
		jm.logger.Info("Marked eligible failed payments for retry")
	}
}

// sendPaymentReminders sends reminders for overdue invoices
func (jm *JobManager) sendPaymentReminders() {
	// Get overdue invoices with tenant subscription information
	rows, err := jm.db.Query(`
		SELECT bi.id, bi.tenant_id, bi.amount, bi.currency, bi.due_date,
		       ts.billing_email, bi.status
		FROM purser.billing_invoices bi
		JOIN purser.tenant_subscriptions ts ON bi.tenant_id = ts.tenant_id
		WHERE bi.status IN ('pending', 'overdue')
		  AND bi.due_date < NOW()
		  AND bi.due_date > NOW() - INTERVAL '30 days'
		  AND ts.status = 'active'
	`)

	if err != nil {
		jm.logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to fetch overdue invoices")
		return
	}
	defer func() { _ = rows.Close() }()

	var overdueCount int
	for rows.Next() {
		var invoiceID, tenantID, currency, billingEmail, invoiceStatus string
		var amount float64
		var dueDate time.Time

		err = rows.Scan(&invoiceID, &tenantID, &amount, &currency, &dueDate, &billingEmail, &invoiceStatus)
		if err != nil {
			continue
		}

		overdueCount++
		daysPastDue := int(time.Since(dueDate).Hours() / 24)

		if invoiceStatus == "pending" {
			_, execErr := jm.db.ExecContext(context.Background(), `
					UPDATE purser.billing_invoices
					SET status = 'overdue', updated_at = NOW()
					WHERE id = $1 AND status = 'pending'
				`, invoiceID)
			if execErr != nil {
				jm.logger.WithFields(logging.Fields{
					"error":      execErr,
					"invoice_id": invoiceID,
				}).Warn("Failed to mark invoice overdue")
			}
		}

		jm.logger.WithFields(logging.Fields{
			"invoice_id":    invoiceID,
			"tenant_id":     tenantID,
			"amount":        amount,
			"currency":      currency,
			"days_past_due": daysPastDue,
		}).Warn("Invoice is overdue - reminder needed")

		// Send overdue reminder email
		if billingEmail != "" {
			err = jm.emailService.SendOverdueReminderEmail(billingEmail, "", invoiceID, amount, currency, daysPastDue)
			if err != nil {
				jm.logger.WithError(err).WithFields(logging.Fields{
					"billing_email": billingEmail,
					"invoice_id":    invoiceID,
				}).Error("Failed to send overdue reminder email")
			}
		}
	}

	if overdueCount > 0 {
		jm.logger.WithFields(logging.Fields{
			"overdue_count": overdueCount,
		}).Info("Processed payment reminders")
	}
}

// NOTE: Crypto sweep operations are performed OFFLINE with the master seed.
// The server only stores the xpub (extended public key) for address derivation.
// See docs/operations/sweep-ceremony.md for the sweep process.

// runWalletCleanup cleans up expired crypto wallets
func (jm *JobManager) runWalletCleanup(ctx context.Context) {
	ticker := time.NewTicker(12 * time.Hour) // Run twice daily
	defer ticker.Stop()

	jm.logger.Info("Starting wallet cleanup job")

	for {
		select {
		case <-ctx.Done():
			return
		case <-jm.stopCh:
			return
		case <-ticker.C:
			jm.cleanupExpiredWallets()
		}
	}
}

// cleanupExpiredWallets marks expired crypto wallets as inactive
func (jm *JobManager) cleanupExpiredWallets() {
	result, err := jm.db.Exec(`
		UPDATE purser.crypto_wallets
		SET status = 'expired', updated_at = NOW()
		WHERE status = 'active'
		  AND expires_at < NOW()
	`)

	if err != nil {
		jm.logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to cleanup expired wallets")
		return
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected > 0 {
		jm.logger.WithFields(logging.Fields{
			"expired_wallets": rowsAffected,
		}).Info("Cleaned up expired crypto wallets")
	}
}

// ============================================================================
// USAGE PROCESSING (Kafka ingestion)
// These methods were moved from usage.go when HTTP handlers were deleted.
// Usage ingestion flows: Periscope -> Kafka -> JobManager.handleUsageReport
// ============================================================================

// processUsageSummary processes a single usage summary and stores it in the usage records table
func (jm *JobManager) processUsageSummary(summary models.UsageSummary, source string) error {
	// Parse the period to get the actual start and end time of usage
	// Format is expected to be "start_time_rfc3339/end_time_rfc3339"
	var periodStart, periodEnd time.Time
	parts := strings.Split(summary.Period, "/")
	if len(parts) >= 2 {
		var err error
		periodStart, err = time.Parse(time.RFC3339, parts[0])
		if err != nil {
			jm.logger.WithFields(logging.Fields{
				"tenant_id": summary.TenantID,
				"period":    summary.Period,
				"source":    source,
				"err":       err,
			}).Warn("Failed to parse usage period start")
		}
		periodEnd, err = time.Parse(time.RFC3339, parts[1])
		if err != nil {
			jm.logger.WithFields(logging.Fields{
				"tenant_id": summary.TenantID,
				"period":    summary.Period,
				"source":    source,
				"err":       err,
			}).Warn("Failed to parse usage period end")
		}
	} else if len(parts) >= 1 {
		// Try to parse at least start time
		var err error
		periodStart, err = time.Parse(time.RFC3339, parts[0])
		if err == nil {
			// Default end time to start time if not provided
			periodEnd = periodStart
		} else {
			jm.logger.WithFields(logging.Fields{
				"tenant_id": summary.TenantID,
				"period":    summary.Period,
				"source":    source,
				"err":       err,
			}).Warn("Failed to parse usage period")
		}
	}

	// Fallback if parsing fails
	if periodStart.IsZero() || periodEnd.IsZero() {
		// Use timestamp for period start/end fallback
		periodStart = summary.Timestamp
		periodEnd = summary.Timestamp

		jm.logger.WithFields(logging.Fields{
			"tenant_id": summary.TenantID,
			"period":    summary.Period,
		}).Warn("Failed to parse period for usage window, falling back to timestamp")
	}

	granularity := "hourly"
	if !periodEnd.IsZero() && !periodStart.IsZero() {
		duration := periodEnd.Sub(periodStart)
		if duration >= 28*24*time.Hour {
			granularity = "monthly"
		} else if duration >= 24*time.Hour {
			granularity = "daily"
		}
	}

	// Use shared helper for usage data extraction
	usageTypes := buildUsageDataFromSummary(summary)

	// Build usage details JSONB
	usageDetails := models.JSONB{
		"max_viewers":   summary.MaxViewers,
		"total_viewers": summary.TotalViewers,
		"total_streams": summary.TotalStreams,
		"unique_users":  summary.UniqueUsers,
		"source":        source,
		// Per-codec breakdown: Livepeer
		"livepeer_h264_seconds": summary.LivepeerH264Seconds,
		"livepeer_vp9_seconds":  summary.LivepeerVP9Seconds,
		"livepeer_av1_seconds":  summary.LivepeerAV1Seconds,
		"livepeer_hevc_seconds": summary.LivepeerHEVCSeconds,
		// Per-codec breakdown: Native AV
		"native_av_h264_seconds": summary.NativeAvH264Seconds,
		"native_av_vp9_seconds":  summary.NativeAvVP9Seconds,
		"native_av_av1_seconds":  summary.NativeAvAV1Seconds,
		"native_av_hevc_seconds": summary.NativeAvHEVCSeconds,
		"native_av_aac_seconds":  summary.NativeAvAACSeconds,
		"native_av_opus_seconds": summary.NativeAvOpusSeconds,
		// API usage aggregates
		"api_requests":    summary.APIRequests,
		"api_errors":      summary.APIErrors,
		"api_duration_ms": summary.APIDurationMs,
		"api_complexity":  summary.APIComplexity,
		"api_breakdown":   summary.APIBreakdown,
	}

	// Upsert each usage type
	for usageType, usageValue := range usageTypes {
		if usageValue <= 0 {
			continue
		}

		_, err := jm.db.Exec(`
			INSERT INTO purser.usage_records (tenant_id, cluster_id, usage_type, usage_value, usage_details, period_start, period_end, granularity, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, NOW())
			ON CONFLICT (tenant_id, cluster_id, usage_type, period_start, period_end) DO UPDATE SET
				usage_value = EXCLUDED.usage_value,
				usage_details = EXCLUDED.usage_details,
				granularity = EXCLUDED.granularity,
				updated_at = NOW()
		`, summary.TenantID, summary.ClusterID, usageType, usageValue, usageDetails, periodStart, periodEnd, granularity)

		if err != nil {
			return fmt.Errorf("failed to upsert %s: %w", usageType, err)
		}
	}

	return nil
}

// updateInvoiceDraft creates or updates an invoice draft for the tenant based on usage
func (jm *JobManager) updateInvoiceDraft(ctx context.Context, tenantID string) error {
	var (
		tierID              string
		subscriptionStatus  string
		tierName            string
		displayName         string
		basePrice           float64
		currency            string
		meteringEnabled     bool
		overageRates        models.OverageRates
		storageAllocation   models.AllocationDetails
		bandwidthAllocation models.AllocationDetails
		customPricing       models.CustomPricing
		customAllocations   models.AllocationDetails
	)

	err := jm.db.QueryRow(`
		SELECT ts.tier_id, ts.status, bt.tier_name, bt.display_name, bt.base_price, bt.currency, bt.metering_enabled,
		       bt.overage_rates, bt.storage_allocation, bt.bandwidth_allocation, ts.custom_pricing, ts.custom_allocations
		FROM purser.tenant_subscriptions ts
		JOIN purser.billing_tiers bt ON ts.tier_id = bt.id
		WHERE ts.tenant_id = $1 AND ts.status = 'active' AND bt.is_active = true
	`, tenantID).Scan(&tierID, &subscriptionStatus, &tierName, &displayName, &basePrice, &currency, &meteringEnabled,
		&overageRates, &storageAllocation, &bandwidthAllocation, &customPricing, &customAllocations)

	if errors.Is(err, sql.ErrNoRows) {
		jm.logger.WithField("tenant_id", tenantID).Info("No active subscription, skipping invoice draft")
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to get subscription: %w", err)
	}

	// Get current billing period
	now := time.Now()
	periodStart, periodEnd := loadSubscriptionPeriod(jm.db, tenantID, now)

	var finalizedCount int
	if countErr := jm.db.QueryRow(`
		SELECT COUNT(*) FROM purser.billing_invoices
		WHERE tenant_id = $1
		  AND period_start = $2
		  AND status != 'draft'
	`, tenantID, periodStart).Scan(&finalizedCount); countErr != nil {
		return fmt.Errorf("failed to check finalized invoices: %w", countErr)
	}
	if finalizedCount > 0 {
		jm.logger.WithFields(logging.Fields{
			"tenant_id":      tenantID,
			"billing_period": periodStart.Format("2006-01"),
		}).Info("Finalized invoice exists; skipping draft update")
		return nil
	}

	// Aggregate usage for current billing period
	// Only aggregate rollup-able metrics - uniques come from Periscope enrichment
	rows, err := jm.db.Query(`
		SELECT usage_type,
		       CASE
			       WHEN usage_type = 'average_storage_gb' THEN AVG(usage_value)
			       WHEN usage_type IN ('peak_bandwidth_mbps', 'max_viewers') THEN MAX(usage_value)
			       ELSE SUM(usage_value)
		       END as total
		FROM purser.usage_records
		WHERE tenant_id = $1
		  AND period_start < $3
		  AND period_end > $2
		  AND usage_type NOT IN ('unique_users', 'total_streams', 'total_viewers', 'unique_users_period')
		GROUP BY usage_type
	`, tenantID, periodStart, periodEnd)
	if err != nil {
		return fmt.Errorf("failed to query usage: %w", err)
	}
	defer func() { _ = rows.Close() }()

	usageTotals := make(map[string]float64)

	for rows.Next() {
		var usageType string
		var total float64
		if scanErr := rows.Scan(&usageType, &total); scanErr != nil {
			continue
		}
		usageTotals[usageType] = total
	}

	// Calculate charges (base + metered)
	baseAmount, meteredAmount := calculateCharges(usageTotals, basePrice, meteringEnabled, overageRates, storageAllocation, bandwidthAllocation, customPricing, customAllocations)
	grossAmount := baseAmount + meteredAmount

	// Apply prepaid credit if available (for postpaid users with prepaid balance)
	var prepaidCreditApplied float64
	prepaidBalance, err := jm.getPrepaidBalance(tenantID)
	if err != nil {
		jm.logger.WithError(err).WithField("tenant_id", tenantID).Warn("Failed to get prepaid balance for invoice")
	} else if prepaidBalance > 0 && grossAmount > 0 {
		// Convert gross amount to cents for comparison
		grossAmountCents := int64(grossAmount * 100)
		// Credit to apply is min of balance and gross amount
		creditToApplyCents := prepaidBalance
		if creditToApplyCents > grossAmountCents {
			creditToApplyCents = grossAmountCents
		}
		referenceID := uuid.NewSHA1(
			uuid.NameSpaceOID,
			[]byte(fmt.Sprintf("invoice_credit:%s:%s", tenantID, periodStart.Format("2006-01-02"))),
		).String()

		// Deduct from prepaid balance (idempotent via referenceID)
		var duplicate bool
		_, duplicate, err = jm.deductPrepaidBalanceForCredit(ctx, tenantID, creditToApplyCents, fmt.Sprintf("Invoice credit: %s", periodStart.Format("2006-01")), &referenceID)
		if err != nil {
			jm.logger.WithError(err).WithField("tenant_id", tenantID).Warn("Failed to deduct prepaid balance for invoice credit")
		} else if duplicate {
			amountCents, found, lookupErr := jm.getBalanceTransactionByReference(ctx, tenantID, "invoice_credit", referenceID)
			if lookupErr != nil {
				jm.logger.WithError(lookupErr).WithField("tenant_id", tenantID).Warn("Failed to fetch prior invoice credit transaction")
			} else if found && amountCents < 0 {
				prepaidCreditApplied = float64(-amountCents) / 100.0
			}
		} else {
			prepaidCreditApplied = float64(creditToApplyCents) / 100.0
			jm.logger.WithFields(logging.Fields{
				"tenant_id":              tenantID,
				"prepaid_credit_applied": prepaidCreditApplied,
				"gross_amount":           grossAmount,
			}).Info("Applied prepaid credit to invoice")
		}
	}

	// Net amount is gross minus credit applied
	totalAmount := grossAmount - prepaidCreditApplied

	// Build flat usage_details - all metrics at top level for email and API
	usageDetails := map[string]interface{}{
		"period_start": periodStart,
		"period_end":   periodEnd,
		"tier_info": map[string]interface{}{
			"tier_id":          tierID,
			"tier_name":        tierName,
			"display_name":     displayName,
			"base_price":       basePrice,
			"metering_enabled": meteringEnabled,
		},
	}

	// Add rollup-able billing metrics
	for k, v := range usageTotals {
		usageDetails[k] = v
	}

	usageJSON, err := json.Marshal(usageDetails)
	if err != nil {
		jm.logger.WithError(err).WithField("tenant_id", tenantID).Warn("Failed to marshal usage details for invoice draft")
		usageJSON = []byte("{}")
	}

	// Upsert draft invoice in billing_invoices
	dueDate := periodEnd.AddDate(0, 0, 14)
	result, err := jm.db.Exec(`
		UPDATE purser.billing_invoices
		SET amount = $1,
			base_amount = $2,
			metered_amount = $3,
			prepaid_credit_applied = $4,
			currency = $5,
			status = 'draft',
			due_date = $6,
			usage_details = $7,
			period_start = $8,
			period_end = $9,
			updated_at = NOW()
		WHERE tenant_id = $10
		  AND status = 'draft'
		  AND period_start = $8
	`, totalAmount, baseAmount, meteredAmount, prepaidCreditApplied, currency, dueDate, usageJSON, periodStart, periodEnd, tenantID)

	if err != nil {
		return fmt.Errorf("failed to update invoice draft: %w", err)
	}

	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		_, err = jm.db.Exec(`
			INSERT INTO purser.billing_invoices (
				id, tenant_id, amount, currency, status, due_date,
				base_amount, metered_amount, prepaid_credit_applied, usage_details,
				period_start, period_end,
				created_at, updated_at
			) VALUES (
				gen_random_uuid(), $1, $2, $3, 'draft', $4,
				$5, $6, $7, $8, $9, $10, NOW(), NOW()
			)
		`, tenantID, totalAmount, currency, dueDate, baseAmount, meteredAmount, prepaidCreditApplied, usageJSON, periodStart, periodEnd)
		if err != nil {
			return fmt.Errorf("failed to insert invoice draft: %w", err)
		}
	}

	if err != nil {
		return fmt.Errorf("failed to upsert invoice draft: %w", err)
	}

	jm.logger.WithFields(logging.Fields{
		"tenant_id":              tenantID,
		"billing_period":         periodStart.Format("2006-01"),
		"gross_amount":           grossAmount,
		"prepaid_credit_applied": prepaidCreditApplied,
		"net_amount":             totalAmount,
	}).Info("Updated invoice draft")

	return nil
}
