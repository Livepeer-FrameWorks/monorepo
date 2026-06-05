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
	"github.com/shopspring/decimal"
	"github.com/sirupsen/logrus"

	billingpkg "frameworks/api_billing/internal/billing"
	billingmollie "frameworks/api_billing/internal/mollie"
	"frameworks/api_billing/internal/operator"
	"frameworks/api_billing/internal/pricing"
	"frameworks/api_billing/internal/rating"
	billingstripe "frameworks/api_billing/internal/stripe"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	decklog "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/decklog"
	periscope "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/periscope"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/models"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
)

type canonicalUsageDelta struct {
	clusterID    string
	usageType    string
	usageValue   float64
	usageDetails models.JSONB
}

func loadSubscriptionPeriod(ctx context.Context, db *sql.DB, tenantID string, now time.Time) (time.Time, time.Time, error) {
	var start, end, mollieNext sql.NullTime
	err := db.QueryRowContext(ctx, `
		SELECT billing_period_start, billing_period_end, mollie_next_payment_date
		FROM purser.tenant_subscriptions
		WHERE tenant_id = $1 AND status = 'active'
		ORDER BY created_at DESC
		LIMIT 1
	`, tenantID).Scan(&start, &end, &mollieNext)
	if err == nil && mollieNext.Valid {
		periodEnd := time.Date(mollieNext.Time.Year(), mollieNext.Time.Month(), mollieNext.Time.Day(), 0, 0, 0, 0, time.UTC)
		periodStart := periodEnd.AddDate(0, -1, 0)
		return periodStart, periodEnd, nil
	}
	if err == nil && start.Valid && end.Valid && end.Time.After(start.Time) {
		return start.Time, end.Time, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, time.Time{}, fmt.Errorf("load subscription period: %w", err)
	}

	periodStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	periodEnd := periodStart.AddDate(0, 1, 0)
	return periodStart, periodEnd, nil
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
	tierReconciler    TierReconciler
}

// TierReconciler is the subset of tieraccess.Reconciler used by the downgrade
// applier. Defined as an interface so JobManager tests can stub it without
// pulling in the Quartermaster client.
type TierReconciler interface {
	Reconcile(ctx context.Context, tenantID string, tierLevel int32) ([]string, string, error)
}

// NewJobManager creates a new job manager
func NewJobManager(database *sql.DB, log logging.Logger, commodoreClient CommodoreClient, decklogSvc *decklog.BatchedClient, periscopeSvc *periscope.GRPCClient, tierReconciler TierReconciler) *JobManager {
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

	includeTestnets := config.X402IncludeTestnetsEnabled()
	emailSvc := NewEmailService(log)
	x402Submitter := NewX402Handler(database, log, NewHDWallet(database, log), NewRPCClient(), commodoreClient)

	return &JobManager{
		db:                database,
		logger:            log,
		emailService:      emailSvc,
		cryptoMonitor:     NewCryptoMonitor(database, log, decklogSvc),
		gasWalletMonitor:  NewGasWalletMonitor(log),
		x402Reconciler:    NewX402Reconciler(database, log, includeTestnets, x402Submitter.submitTransferWithAuthorization),
		kafkaConsumer:     consumer,
		stopCh:            make(chan struct{}),
		billingTopic:      billingTopic,
		commodoreClient:   commodoreClient,
		periscopeClient:   periscopeSvc,
		thresholdEnforcer: NewThresholdEnforcer(database, log, commodoreClient, emailSvc),
		tierReconciler:    tierReconciler,
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

	// Start wallet cleanup job
	go jm.runWalletCleanup(ctx)

	// Start Stripe meter event flusher.
	go jm.runStripeMeterFlusher(ctx)

	// Start Mollie observation drain backstop.
	go jm.runMollieObservationDrain(ctx)
}

// runStripeMeterFlusher periodically pushes outbox rows to Stripe.
// Cadence is 5 minutes; identifier-based idempotency on the Stripe side
// means a missed tick or duplicate delivery is collapsed within 24 h.
func (jm *JobManager) runStripeMeterFlusher(ctx context.Context) {
	flusher := billingstripe.NewMeterFlusher(jm.db)
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-jm.stopCh:
			return
		case <-ticker.C:
			sent, deferred, err := flusher.Flush(ctx)
			if err != nil {
				jm.logger.WithError(err).Error("Stripe meter flusher: read failure")
				continue
			}
			if sent > 0 || deferred > 0 {
				jm.logger.WithFields(logging.Fields{
					"sent":     sent,
					"deferred": deferred,
				}).Info("Stripe meter flusher tick")
			}
		}
	}
}

// runMollieObservationDrain periodically attaches out-of-order Mollie
// subscription payment observations to invoices that finalized after the
// webhook arrived. The invoice finalization path runs the same drain
// immediately; this loop covers crashes between invoice commit and drain.
func (jm *JobManager) runMollieObservationDrain(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-jm.stopCh:
			return
		case <-ticker.C:
			if err := jm.drainMollieObservationsBackstop(ctx); err != nil {
				jm.logger.WithError(err).Warn("Mollie observation drain backstop failed")
			}
		}
	}
}

func (jm *JobManager) drainMollieObservationsBackstop(ctx context.Context) error {
	rows, err := jm.db.QueryContext(ctx, `
		SELECT DISTINCT bi.id
		FROM purser.mollie_payment_observations mpo
		JOIN purser.billing_invoices bi ON bi.tenant_id = mpo.tenant_id
		WHERE mpo.resolved_at IS NULL
		  AND mpo.mollie_subscription_id IS NOT NULL
		  AND bi.status IN ('pending', 'overdue')
		  AND COALESCE(mpo.paid_at, mpo.created_at) >= bi.period_start
		  AND COALESCE(mpo.paid_at, mpo.created_at) <= bi.period_end
		ORDER BY bi.id
		LIMIT 100
	`)
	if err != nil {
		return fmt.Errorf("list invoices for Mollie observation drain: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var invoiceID string
		if err := rows.Scan(&invoiceID); err != nil {
			return fmt.Errorf("scan Mollie observation invoice: %w", err)
		}
		if err := drainMolliePaymentObservationsForInvoice(ctx, invoiceID); err != nil {
			jm.logger.WithError(err).WithField("invoice_id", invoiceID).Warn("Failed to drain Mollie observations for invoice")
		}
	}
	return rows.Err()
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

	acceptedUsage, err := jm.processUsageSummary(ctx, summary, "kafka")
	if err != nil {
		jm.logger.WithError(err).WithFields(logging.Fields{
			"tenant_id": summary.TenantID,
			"period":    summary.Period,
		}).Error("Failed to process usage summary from Kafka")
		return err
	}

	// Check billing model to determine processing path
	billingModel, err := jm.getTenantBillingModel(ctx, summary.TenantID)
	if err != nil {
		jm.logger.WithError(err).WithField("tenant_id", summary.TenantID).Error("Failed to get billing model")
		return fmt.Errorf("billing model lookup failed: %w", err)
	}

	if billingModel == "prepaid" {
		// Prepaid: deduct usage cost from balance. Surface the error so Kafka
		// retries the message; silently swallowing means the balance never
		// got charged for usage that was already recorded.
		if err := jm.processPrepaidUsage(ctx, summary, acceptedUsage); err != nil {
			jm.logger.WithError(err).WithField("tenant_id", summary.TenantID).Error("Failed to process prepaid usage")
			return fmt.Errorf("prepaid deduction failed: %w", err)
		}
	} else {
		// Postpaid: update invoice draft. Same retry contract: propagate.
		if err := jm.updateInvoiceDraft(ctx, summary.TenantID); err != nil {
			jm.logger.WithError(err).WithField("tenant_id", summary.TenantID).Error("Failed to update invoice draft")
			return fmt.Errorf("invoice draft update failed: %w", err)
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
func (jm *JobManager) getTenantBillingModel(ctx context.Context, tenantID string) (string, error) {
	var billingModel string
	err := jm.db.QueryRowContext(ctx, `
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

// buildUsageDataFromSummary maps a UsageSummary onto the canonical
// usage_types written to purser.usage_records.
//
// Priceable meters: delivered_minutes, ingress_gb, egress_gb,
// storage_gb_seconds_hot/cold, and media_seconds. Operational gauges use
// the same canonical 5-minute delta envelope so public usage APIs never
// mix noncanonical or malformed rows with billable data.
//
// Per-codec processing breakdown does NOT get its own usage_type — it
// travels in usage_details.codec_seconds and is consumed by the
// codec_multiplier rating model. See docs/architecture/meter-contracts.md.
func buildUsageDataFromSummary(summary models.UsageSummary) map[string]float64 {
	data := map[string]float64{
		"delivered_minutes":       summary.ViewerHours * 60,
		"ingress_gb":              summary.IngressGB,
		"egress_gb":               summary.EgressGB,
		"storage_gb_seconds_hot":  summary.StorageGBSecondsHot,
		"storage_gb_seconds_cold": summary.StorageGBSecondsCold,
		"media_seconds":           totalCodecSeconds(summary),
		// Operational/display metrics.
		"stream_runtime_seconds": summary.StreamHours * 3600,
		"peak_bandwidth_mbps":    summary.PeakBandwidthMbps,
		"max_viewers":            float64(summary.MaxViewers),
		"total_streams":          float64(summary.TotalStreams),
		"total_viewers":          float64(summary.TotalViewers),
		"unique_users":           float64(summary.UniqueUsers),
		"api_requests":           summary.APIRequests,
		"api_errors":             summary.APIErrors,
		"api_duration_ms":        summary.APIDurationMs,
		"api_complexity":         summary.APIComplexity,
	}
	for meter, value := range summary.Meters {
		if _, exists := data[meter]; exists && data[meter] != 0 {
			continue
		}
		data[meter] = value
	}
	return data
}

// totalCodecSeconds returns total processed input seconds across all
// codecs and processing types. The per-codec/per-process breakdown is
// preserved in usage_details and consumed by ModelCodecMultiplier.
func totalCodecSeconds(s models.UsageSummary) float64 {
	if len(s.ProcessingSeconds) > 0 {
		total := 0.0
		useJointOnly := hasJointProcessingKeys(s.ProcessingSeconds)
		for key, seconds := range s.ProcessingSeconds {
			if useJointOnly && !isJointProcessingKey(key) {
				continue
			}
			total += seconds
		}
		return total
	}
	return s.LivepeerH264Seconds + s.LivepeerVP9Seconds + s.LivepeerAV1Seconds + s.LivepeerHEVCSeconds +
		s.NativeAvH264Seconds + s.NativeAvVP9Seconds + s.NativeAvAV1Seconds + s.NativeAvHEVCSeconds +
		s.NativeAvAACSeconds + s.NativeAvOpusSeconds
}

func processingDetailsFromSummary(summary models.UsageSummary) (map[string]float64, map[string]float64) {
	processCodec := map[string]float64{}
	if len(summary.ProcessingSeconds) > 0 {
		useJointOnly := hasJointProcessingKeys(summary.ProcessingSeconds)
		for key, seconds := range summary.ProcessingSeconds {
			if useJointOnly && !isJointProcessingKey(key) {
				continue
			}
			if seconds > 0 {
				processCodec[key] += seconds
			}
		}
	} else {
		addProcessCodecSeconds(processCodec, "Livepeer", "h264", summary.LivepeerH264Seconds)
		addProcessCodecSeconds(processCodec, "Livepeer", "hevc", summary.LivepeerHEVCSeconds)
		addProcessCodecSeconds(processCodec, "Livepeer", "vp9", summary.LivepeerVP9Seconds)
		addProcessCodecSeconds(processCodec, "Livepeer", "av1", summary.LivepeerAV1Seconds)
		addProcessCodecSeconds(processCodec, "AV", "h264", summary.NativeAvH264Seconds)
		addProcessCodecSeconds(processCodec, "AV", "hevc", summary.NativeAvHEVCSeconds)
		addProcessCodecSeconds(processCodec, "AV", "vp9", summary.NativeAvVP9Seconds)
		addProcessCodecSeconds(processCodec, "AV", "av1", summary.NativeAvAV1Seconds)
		addProcessCodecSeconds(processCodec, "AV", "aac", summary.NativeAvAACSeconds)
		addProcessCodecSeconds(processCodec, "AV", "opus", summary.NativeAvOpusSeconds)
	}

	codecSeconds := map[string]float64{}
	for key, seconds := range processCodec {
		parts := strings.SplitN(key, ":", 2)
		if len(parts) == 2 {
			codecSeconds[parts[1]] += seconds
		}
		codecSeconds[key] += seconds
	}
	return codecSeconds, processCodec
}

func hasJointProcessingKeys(values map[string]float64) bool {
	for key := range values {
		if isJointProcessingKey(key) {
			return true
		}
	}
	return false
}

func isJointProcessingKey(key string) bool {
	_, _, ok := strings.Cut(key, ":")
	return ok
}

func addProcessCodecSeconds(out map[string]float64, processType, codec string, seconds float64) {
	if seconds <= 0 {
		return
	}
	out[processType+":"+codec] += seconds
}

func usageSummaryReferenceID(summary models.UsageSummary) uuid.UUID {
	clusterID := summary.ClusterID
	if clusterID == "" {
		clusterID = "unknown"
	}
	raw := fmt.Sprintf("%s:%s:%s", summary.TenantID, clusterID, summary.Period)
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(raw))
}

// processPrepaidUsage calculates usage cost and deducts from prepaid balance.
// Uses rating.UsageAmount only, never TotalAmount, so per-event deductions
// don't re-charge the monthly base subscription fee.
func (jm *JobManager) processPrepaidUsage(ctx context.Context, summary models.UsageSummary, acceptedUsage []canonicalUsageDelta) error {
	periodStart, _, _, err := parseUsageSummaryPeriod(summary)
	if err != nil {
		return err
	}

	tier, err := billingpkg.LoadEffectiveTier(ctx, jm.db, summary.TenantID)
	if errors.Is(err, sql.ErrNoRows) {
		jm.logger.WithField("tenant_id", summary.TenantID).Debug("No active subscription for prepaid usage")
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to get effective tier: %w", err)
	}
	if !tier.MeteringEnabled {
		return nil
	}

	// Resolve cluster pricing for the summary's cluster. The Kafka usage
	// summary already carries summary.ClusterID; the resolver picks the
	// effective rules per cluster pricing model. Empty cluster_id usage falls
	// through to tier rules.
	rules := tier.Rules
	currency := tier.Currency
	if currency == "" {
		currency = billing.DefaultCurrency()
	}
	if currency != billing.DefaultCurrency() {
		return fmt.Errorf("prepaid balance currency %s cannot settle usage priced in %s", billing.DefaultCurrency(), currency)
	}
	if summary.ClusterID != "" {
		resolver := jm.pricingResolver()
		if resolver != nil {
			resolved, resolveErr := pricing.ResolveClusterPricing(ctx, pricing.ResolveInputs{
				DB: jm.db, QM: resolver,
				ConsumingTenantID: summary.TenantID,
				ClusterID:         summary.ClusterID,
				AsOf:              periodStart,
				TierRules:         tier.Rules,
				TierCurrency:      tier.Currency,
			})
			switch {
			case errors.Is(resolveErr, pricing.ErrCustomPricingMissingForCluster):
				if metrics != nil {
					metrics.BillingCalculations.WithLabelValues("prepaid", "custom_pricing_missing").Inc()
				}
				return fmt.Errorf("prepaid usage on cluster %s has unconfigured custom pricing", summary.ClusterID)
			case errors.Is(resolveErr, pricing.ErrAmbiguousClusterOwnership):
				if metrics != nil {
					metrics.BillingCalculations.WithLabelValues("prepaid", "ambiguous_cluster_ownership").Inc()
				}
				return fmt.Errorf("prepaid usage on cluster %s has ambiguous ownership", summary.ClusterID)
			case resolveErr != nil:
				return fmt.Errorf("resolve cluster pricing for %s: %w", summary.ClusterID, resolveErr)
			}
			if resolved.Currency != "" && resolved.Currency != currency {
				return fmt.Errorf("prepaid usage on cluster %s prices in %s but prepaid balance currency is %s", summary.ClusterID, resolved.Currency, currency)
			}
			rules = resolved.MeteredRules
		}
	}

	in := buildRatingInputFromCanonicalUsage(acceptedUsage, currency, rules)
	res, err := rating.Rate(in)
	if err != nil {
		return fmt.Errorf("rate usage: %w", err)
	}
	if res.UsageAmount.IsZero() {
		return nil
	}

	microPerUnit := decimal.NewFromInt(1_000_000)
	desiredMicro := res.UsageAmount.Mul(microPerUnit).Round(0).IntPart()
	if desiredMicro == 0 {
		return nil
	}

	referenceID := usageSummaryReferenceID(summary)
	previousBalance, newBalanceCents, applied, err := jm.deductPrepaidBalanceForUsageMicro(ctx, summary.TenantID, desiredMicro, fmt.Sprintf("Usage: %s", summary.Period), referenceID)
	if err != nil {
		return fmt.Errorf("failed to apply prepaid usage amount: %w", err)
	}
	if !applied {
		jm.logger.WithFields(logging.Fields{
			"tenant_id":  summary.TenantID,
			"period":     summary.Period,
			"summary_id": referenceID.String(),
		}).Info("Skipped prepaid usage deduction for duplicate usage summary")
		return nil
	}

	appliedCents := previousBalance - newBalanceCents
	jm.logger.WithFields(logging.Fields{
		"tenant_id":         summary.TenantID,
		"period":            summary.Period,
		"requested_micro":   desiredMicro,
		"applied_cents":     appliedCents,
		"new_balance_cents": newBalanceCents,
	}).Info("Applied prepaid usage amount")

	if jm.thresholdEnforcer != nil {
		if err := jm.thresholdEnforcer.EnforcePrepaidThresholds(ctx, summary.TenantID, previousBalance, newBalanceCents); err != nil {
			jm.logger.WithError(err).WithField("tenant_id", summary.TenantID).Warn("Failed to enforce prepaid thresholds")
		}
	}

	return nil
}

// deductPrepaidBalanceForCreditTx deducts up to requestCents from the prepaid
// balance inside an existing transaction. The actual deducted amount is
// returned as appliedCents and is bounded by the row-locked balance; the
// caller's requestCents is a ceiling, not a guarantee.
//
// Race-safety: the (tenant_id, reference_type, reference_id) UNIQUE index on
// purser.balance_transactions is the idempotency gate. The ledger row is
// inserted FIRST, then the balance is mutated. A racing duplicate hits the
// unique violation before any balance update happens, so we never
// double-debit even when concurrent transactions probe the ledger before
// either commits.
//
// Used by invoice draft/finalization so the credit deduction commits or rolls
// back together with the invoice header and line items.
func (jm *JobManager) deductPrepaidBalanceForCreditTx(ctx context.Context, tx *sql.Tx, tenantID string, requestCents int64, description string, referenceID *string) (newBalance, appliedCents int64, isDuplicate bool, err error) {
	currency := billing.DefaultCurrency()
	referenceType := "invoice_credit"

	if _, insertErr := tx.ExecContext(ctx, `
		INSERT INTO purser.prepaid_balances (tenant_id, balance_cents, currency)
		VALUES ($1, 0, $2)
		ON CONFLICT (tenant_id, currency) DO NOTHING
	`, tenantID, currency); insertErr != nil {
		return 0, 0, false, insertErr
	}

	var currentBalance int64
	if scanErr := tx.QueryRowContext(ctx, `
		SELECT balance_cents FROM purser.prepaid_balances
		WHERE tenant_id = $1 AND currency = $2 FOR UPDATE
	`, tenantID, currency).Scan(&currentBalance); scanErr != nil {
		return 0, 0, false, scanErr
	}

	// Cap against the LOCKED balance. requestCents is a ceiling.
	applied := requestCents
	if applied > currentBalance {
		applied = currentBalance
	}
	if applied <= 0 {
		return currentBalance, 0, false, nil
	}

	// Insert the ledger row FIRST. This is the idempotency gate: a racing
	// duplicate (same reference_id) hits 23505 here before any balance
	// mutation, so the caller's tx rolls back the no-op. Existing duplicates
	// are detected via the same path; convert 23505 into is_duplicate=true
	// without touching the balance, and look up the prior amount to surface
	// to the caller.
	if _, txErr := tx.ExecContext(ctx, `
		INSERT INTO purser.balance_transactions (
			tenant_id, amount_cents, balance_after_cents,
			transaction_type, description, reference_id, reference_type, created_at
		) VALUES ($1, $2, $3, 'credit', $4, $5, $6, NOW())
	`, tenantID, -applied, currentBalance-applied, description, referenceID, referenceType); txErr != nil {
		var pqErr *pq.Error
		if errors.As(txErr, &pqErr) && pqErr.Code == "23505" {
			// Duplicate ledger row exists. Read its amount so the caller can
			// preserve prepaid_credit_applied. Balance is untouched.
			var historicAmount int64
			if probeErr := tx.QueryRowContext(ctx, `
				SELECT amount_cents FROM purser.balance_transactions
				WHERE tenant_id = $1 AND reference_type = $2 AND reference_id = $3
				ORDER BY created_at DESC LIMIT 1
			`, tenantID, referenceType, *referenceID).Scan(&historicAmount); probeErr != nil {
				return 0, 0, false, probeErr
			}
			return currentBalance, -historicAmount, true, nil
		}
		return 0, 0, false, txErr
	}

	newBalance = currentBalance - applied
	if _, updErr := tx.ExecContext(ctx, `
		UPDATE purser.prepaid_balances SET balance_cents = $1, updated_at = NOW()
		WHERE tenant_id = $2 AND currency = $3
	`, newBalance, tenantID, currency); updErr != nil {
		return 0, 0, false, updErr
	}
	return newBalance, applied, false, nil
}

func invoiceCreditDescription(periodStart time.Time) string {
	return fmt.Sprintf("Invoice credit: %s", periodStart.Format("2006-01"))
}

func invoiceCreditReferenceID(tenantID string, periodStart time.Time, alreadyAppliedCents, requestCents int64) string {
	raw := fmt.Sprintf(
		"invoice_credit:%s:%s:%d:%d",
		tenantID,
		periodStart.Format("2006-01-02"),
		alreadyAppliedCents,
		requestCents,
	)
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(raw)).String()
}

func (jm *JobManager) appliedInvoiceCreditCentsTx(ctx context.Context, tx *sql.Tx, tenantID string, periodStart time.Time) (int64, error) {
	var applied int64
	err := tx.QueryRowContext(ctx, `
		SELECT COALESCE(SUM(-amount_cents), 0)
		FROM purser.balance_transactions
		WHERE tenant_id = $1
		  AND reference_type = 'invoice_credit'
		  AND description = $2
		  AND amount_cents < 0
	`, tenantID, invoiceCreditDescription(periodStart)).Scan(&applied)
	if err != nil {
		return 0, err
	}
	return applied, nil
}

// applyInvoicePrepaidCreditTx brings invoice credit for a tenant/period up to
// grossCents, bounded by the locked prepaid balance. It is delta-based: if an
// early draft used EUR 2 of credit and later usage grows the invoice to EUR 150,
// the next draft/finalization attempts to apply only the missing EUR 148.
func (jm *JobManager) applyInvoicePrepaidCreditTx(ctx context.Context, tx *sql.Tx, tenantID string, periodStart time.Time, grossCents int64) (int64, error) {
	if grossCents <= 0 {
		return 0, nil
	}

	applied, err := jm.appliedInvoiceCreditCentsTx(ctx, tx, tenantID, periodStart)
	if err != nil {
		return 0, fmt.Errorf("lookup applied invoice credit: %w", err)
	}
	if applied >= grossCents {
		return applied, nil
	}

	requestCents := grossCents - applied
	referenceID := invoiceCreditReferenceID(tenantID, periodStart, applied, requestCents)
	_, deltaApplied, _, err := jm.deductPrepaidBalanceForCreditTx(ctx, tx, tenantID, requestCents, invoiceCreditDescription(periodStart), &referenceID)
	if err != nil {
		return 0, fmt.Errorf("deduct invoice credit delta: %w", err)
	}
	return applied + deltaApplied, nil
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
			balance, err := jm.getPrepaidBalance(ctx, tenantID)
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
				balance, balanceErr := jm.getPrepaidBalance(ctx, tenantID)
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

// microPerCent is the residual unit: 10^-8 of a currency unit, i.e. 10^4
// micro-cents per cent. Sub-cent residuals accumulate here so a stream of
// per-event deductions under €0.01 each eventually crosses a whole-cent
// boundary instead of being truncated to zero.
const microPerCent = int64(10_000)

// deductPrepaidBalanceForUsageMicro applies a signed prepaid usage amount in
// micro-cents (10^-8 of a currency unit). Positive amounts debit balance;
// negative correction amounts credit balance. The fractional residual is
// carried in prepaid_balances.balance_remainder_micro across events so
// sub-cent usage and credits do not structurally leak revenue. Returns
// previous and new balances in cents (the residual is private to the prepaid
// balance row).
//
// Idempotency is keyed on (tenant_id, reference_type='usage_summary', reference_id);
// duplicate calls return applied=false.
func (jm *JobManager) deductPrepaidBalanceForUsageMicro(ctx context.Context, tenantID string, amountMicro int64, description string, referenceID uuid.UUID) (int64, int64, bool, error) {
	currency := billing.DefaultCurrency()

	tx, err := jm.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, false, err
	}
	defer tx.Rollback() //nolint:errcheck // rollback is best-effort

	if _, insertErr := tx.ExecContext(ctx, `
		INSERT INTO purser.prepaid_balances (tenant_id, balance_cents, currency)
		VALUES ($1, 0, $2)
		ON CONFLICT (tenant_id, currency) DO NOTHING
	`, tenantID, currency); insertErr != nil {
		return 0, 0, false, insertErr
	}

	var currentBalance, currentRemainder int64
	if scanErr := tx.QueryRowContext(ctx, `
		SELECT balance_cents, balance_remainder_micro
		FROM purser.prepaid_balances
		WHERE tenant_id = $1 AND currency = $2
		FOR UPDATE
	`, tenantID, currency).Scan(&currentBalance, &currentRemainder); scanErr != nil {
		return 0, 0, false, scanErr
	}

	// Accumulate the residual; commit whole cents, carry the rest. Go integer
	// division truncates toward zero, so normalize negative residuals to keep
	// balance_remainder_micro in [0, microPerCent).
	totalMicro := currentRemainder + amountMicro
	appliedCents := totalMicro / microPerCent
	newRemainder := totalMicro % microPerCent
	if newRemainder < 0 {
		appliedCents--
		newRemainder += microPerCent
	}
	newBalance := currentBalance - appliedCents

	result, err := tx.ExecContext(ctx, `
		INSERT INTO purser.balance_transactions (
			tenant_id, amount_cents, balance_after_cents,
			transaction_type, description, reference_id, reference_type, created_at
		) VALUES ($1, $2, $3, 'usage', $4, $5, 'usage_summary', NOW())
		ON CONFLICT (tenant_id, reference_type, reference_id)
			WHERE reference_type = 'usage_summary'
		DO NOTHING
	`, tenantID, -appliedCents, newBalance, description, referenceID)
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

	if _, updErr := tx.ExecContext(ctx, `
		UPDATE purser.prepaid_balances
		SET balance_cents = $1, balance_remainder_micro = $2, updated_at = NOW()
		WHERE tenant_id = $3 AND currency = $4
	`, newBalance, newRemainder, tenantID, currency); updErr != nil {
		return 0, 0, false, updErr
	}

	if commitErr := tx.Commit(); commitErr != nil {
		return 0, 0, false, commitErr
	}
	return currentBalance, newBalance, true, nil
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
func (jm *JobManager) getPrepaidBalance(ctx context.Context, tenantID string) (int64, error) {
	var balanceCents int64
	currency := billing.DefaultCurrency()
	err := jm.db.QueryRowContext(ctx, `
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
	result, err := jm.db.ExecContext(ctx, `
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
			jm.generateMonthlyInvoices(ctx)
		}
	}
}

// generateMonthlyInvoices generates invoices for tenants due for billing
func (jm *JobManager) generateMonthlyInvoices(ctx context.Context) {
	jm.logger.Info("Running monthly invoice generation")

	now := time.Now()
	defer jm.applyDuePendingDowngrades(ctx, now)

	// Identify tenants due for billing. Pricing rules / entitlements are loaded
	// per-tenant via LoadEffectiveTier so this query stays narrow.
	rows, err := jm.db.QueryContext(ctx, `
		SELECT ts.tenant_id, ts.billing_email, ts.tier_id, ts.status,
		       ts.billing_period_start, ts.billing_period_end, ts.mollie_next_payment_date,
		       ts.stripe_subscription_id, ts.mollie_subscription_id,
		       bt.tier_name, bt.display_name, bt.billing_period
		FROM purser.tenant_subscriptions ts
		JOIN purser.billing_tiers bt ON ts.tier_id = bt.id
		WHERE ts.status = 'active'
		  AND bt.is_active = true
		  AND (
			  (ts.mollie_next_payment_date IS NOT NULL AND ts.mollie_next_payment_date <= $1::date)
			  OR
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
		var tenantID, tierID, subscriptionStatus string
		var billingEmail sql.NullString
		var tierName, displayName, billingPeriod string
		var billingPeriodStart, billingPeriodEnd, mollieNextPaymentDate sql.NullTime
		var stripeSubID, mollieSubID sql.NullString

		err = rows.Scan(&tenantID, &billingEmail, &tierID, &subscriptionStatus,
			&billingPeriodStart, &billingPeriodEnd, &mollieNextPaymentDate,
			&stripeSubID, &mollieSubID,
			&tierName, &displayName, &billingPeriod)
		if err != nil {
			jm.logger.WithFields(logging.Fields{
				"error": err,
			}).Error("Error scanning tenant subscription data")
			continue
		}

		tier, tierErr := billingpkg.LoadEffectiveTier(ctx, jm.db, tenantID)
		if tierErr != nil {
			jm.logger.WithError(tierErr).WithField("tenant_id", tenantID).Error("Failed to load effective tier for invoice")
			continue
		}
		basePrice, _ := tier.BasePrice.Float64()
		currency := tier.Currency
		meteringEnabled := tier.MeteringEnabled

		var periodStart, periodEnd time.Time
		if mollieNextPaymentDate.Valid {
			periodEnd = time.Date(mollieNextPaymentDate.Time.Year(), mollieNextPaymentDate.Time.Month(), mollieNextPaymentDate.Time.Day(), 0, 0, 0, 0, time.UTC)
			periodStart = periodEnd.AddDate(0, -1, 0)
		} else if billingPeriodStart.Valid && billingPeriodEnd.Valid && billingPeriodEnd.Time.After(billingPeriodStart.Time) {
			periodStart = billingPeriodStart.Time
			periodEnd = billingPeriodEnd.Time
		} else {
			periodStart = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location()).AddDate(0, -1, 0)
			periodEnd = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
		}

		if periodEnd.After(now) {
			continue // Billing period not closed yet
		}

		// Check if a terminally-finalized invoice already exists for the
		// previous month. manual_review is NOT terminal — it's a hold that
		// must be re-runnable once ops fixes the underlying cluster
		// pricing, so we treat it like draft for finalization purposes.
		var existingCount int
		err = jm.db.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM purser.billing_invoices
			WHERE tenant_id = $1
			  AND period_start = $2
			  AND status NOT IN ('draft', 'manual_review')
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

		// Check for an existing draft (or held manual_review) invoice for
		// the previous month. Finalization applies any missing prepaid credit
		// in the same transaction as the invoice header write, so base-only
		// invoices and drafts that grew after first credit both consume balance.
		var draftInvoiceID string
		switch err := jm.db.QueryRowContext(ctx, `
			SELECT id
			FROM purser.billing_invoices
			WHERE tenant_id = $1
			  AND period_start = $2
			  AND status IN ('draft', 'manual_review')
			LIMIT 1
		`, tenantID, periodStart).Scan(&draftInvoiceID); {
		case err == nil, errors.Is(err, sql.ErrNoRows):
			// nil err means draft found; ErrNoRows means no draft, leave zero values.
		default:
			jm.logger.WithError(err).WithField("tenant_id", tenantID).Error("Failed to look up existing draft invoice; skipping invoice for this period")
			continue
		}

		// Aggregate canonical usage metrics for the billing period. SUM handles
		// flow/delta meters; MAX handles peak gauges; unique counts are skipped
		// here and come from Periscope enrichment because scalar windows cannot
		// be summed into unique users.
		// Fetch usage partitioned by cluster_id. A scan/query failure must
		// abort this tenant's invoice: rating against an empty/partial usage
		// map underbills.
		perClusterUsage, usageErr := jm.collectInvoiceUsage(ctx, tenantID, periodStart, periodEnd)
		if usageErr != nil {
			jm.logger.WithError(usageErr).WithField("tenant_id", tenantID).Error("Failed to collect usage; skipping invoice for this period")
			continue
		}
		perClusterCodecBreakdowns, codecErr := jm.collectInvoiceCodecBreakdowns(ctx, tenantID, periodStart, periodEnd)
		if codecErr != nil {
			jm.logger.WithError(codecErr).WithField("tenant_id", tenantID).Error("Failed to collect codec breakdown; skipping invoice for this period")
			continue
		}
		usageData := flattenUsageAcrossClusters(perClusterUsage)

		baseProviderManaged := stripeSubID.Valid || mollieSubID.Valid
		ratingResult, ratingErr := jm.rateInvoiceForTenant(ctx, tenantID, periodStart, periodEnd, tier, true, baseProviderManaged, perClusterUsage, perClusterCodecBreakdowns)
		if ratingErr != nil {
			jm.logger.WithError(ratingErr).WithField("tenant_id", tenantID).Error("Failed to rate usage for invoice")
			continue
		}
		// Money stays in decimal.Decimal until the SQL boundary; NUMERIC
		// columns bind cleanly via $N::numeric. No float64 touches the cents.
		baseDec := ratingResult.BaseAmount
		meteredDec := ratingResult.UsageAmount
		grossDec := ratingResult.TotalAmount
		creditDec := decimal.Zero
		totalDec := grossDec

		// Generate invoice
		invoiceID := uuid.New().String()
		dueDate := periodEnd.AddDate(0, 0, 14) // 14 days to pay

		// Determine invoice status. manual_review takes precedence: when any
		// cluster's pricing failed to resolve we hold the entire invoice so
		// no payment captures, Stripe meter pushes, ledger writes, or
		// subscription period advances happen until ops resolves and
		// re-finalizes. Lines persist for ops visibility.
		status := "pending"
		switch {
		case len(ratingResult.ManualReviewReasons) > 0:
			status = "manual_review"
			jm.logger.WithFields(logging.Fields{
				"tenant_id": tenantID,
				"reasons":   strings.Join(ratingResult.ManualReviewReasons, "; "),
			}).Warn("Invoice routed to manual_review; finalization halted")
		case totalDec.IsZero():
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

		periodDuration := periodEnd.Sub(periodStart)
		if periodDuration <= 0 {
			periodDuration = 30 * 24 * time.Hour
		}
		nextPeriodStart := periodEnd
		nextPeriodEnd := periodEnd.Add(periodDuration)
		nextBillingDate := nextPeriodEnd

		// Store invoice header + rated line items atomically. If line-item
		// persistence fails, the whole invoice rolls back so totals never live
		// without their line-item audit trail. The subscription period advances
		// in the same transaction so a finalized invoice cannot leave the
		// subscription pointing at the already-billed period.
		err = withTx(ctx, jm.db, func(tx *sql.Tx) error {
			var txErr error
			if len(ratingResult.ManualReviewReasons) == 0 {
				grossCents := grossDec.Mul(decimal.NewFromInt(100)).Round(0).IntPart()
				var appliedCreditCents int64
				appliedCreditCents, txErr = jm.applyInvoicePrepaidCreditTx(ctx, tx, tenantID, periodStart, grossCents)
				if txErr != nil {
					return txErr
				}
				creditDec = decimal.NewFromInt(appliedCreditCents).Div(decimal.NewFromInt(100))
				totalDec = grossDec.Sub(creditDec)
				if totalDec.IsNegative() {
					totalDec = decimal.Zero
				}
				if totalDec.IsZero() {
					status = "paid"
				}
			}

			// Bind decimals as strings into NUMERIC columns so no float64 rounding
			// can sneak in at the SQL boundary.
			totalAmt := totalDec.Round(2).String()
			baseAmt := baseDec.Round(2).String()
			meteredAmt := meteredDec.Round(2).String()
			creditAmt := creditDec.Round(2).String()

			if draftInvoiceID != "" {
				txErr = tx.QueryRowContext(ctx, `
						UPDATE purser.billing_invoices
						SET amount = $1::numeric, base_amount = $2::numeric, metered_amount = $3::numeric,
						    prepaid_credit_applied = $4::numeric, currency = $5,
						    status = $6, due_date = $7, usage_details = $8,
						    period_start = $9, period_end = $10, updated_at = NOW()
						WHERE id = $11 AND tenant_id = $12 AND status IN ('draft', 'manual_review')
						RETURNING id
					`, totalAmt, baseAmt, meteredAmt, creditAmt, currency, status, dueDate, usageJSON, periodStart, periodEnd, draftInvoiceID, tenantID).Scan(&invoiceID)
				if txErr != nil {
					return fmt.Errorf("update invoice: %w", txErr)
				}
			} else {
				txErr = tx.QueryRowContext(ctx, `
					INSERT INTO purser.billing_invoices (
						id, tenant_id, amount, currency, status, due_date,
						base_amount, metered_amount, prepaid_credit_applied,
					usage_details, period_start, period_end,
					created_at, updated_at
					) VALUES (
						$1, $2, $3::numeric, $4, $5, $6,
						$7::numeric, $8::numeric, $9::numeric,
						$10, $11, $12, NOW(), NOW()
					)
					ON CONFLICT (tenant_id, period_start) WHERE period_start IS NOT NULL
					DO UPDATE SET
						amount = EXCLUDED.amount,
						currency = EXCLUDED.currency,
						status = EXCLUDED.status,
						due_date = EXCLUDED.due_date,
						base_amount = EXCLUDED.base_amount,
						metered_amount = EXCLUDED.metered_amount,
						prepaid_credit_applied = EXCLUDED.prepaid_credit_applied,
						usage_details = EXCLUDED.usage_details,
						period_end = EXCLUDED.period_end,
						updated_at = NOW()
					WHERE purser.billing_invoices.status IN ('draft', 'manual_review')
					RETURNING id
					`, invoiceID, tenantID, totalAmt, currency, status, dueDate, baseAmt, meteredAmt, creditAmt, usageJSON, periodStart, periodEnd).Scan(&invoiceID)
				if txErr != nil {
					return fmt.Errorf("upsert invoice: %w", txErr)
				}
			}
			txErr = persistInvoiceLineItems(ctx, tx, invoiceID, tenantID, ratingResult)
			if txErr != nil {
				return txErr
			}
			if status != "manual_review" {
				_, txErr = tx.ExecContext(ctx, `
					UPDATE purser.usage_adjustments
					SET applied_invoice_id = $1,
					    updated_at = NOW()
					WHERE tenant_id = $2
					  AND period_start < $4
					  AND period_end > $3
					  AND status = 'applied'
					  AND value_kind = 'correction_delta'
					  AND applied_invoice_id IS NULL
				`, invoiceID, tenantID, periodStart, periodEnd)
				if txErr != nil {
					return fmt.Errorf("mark usage adjustments applied to invoice: %w", txErr)
				}
			}
			// Operator credit ledger: write accrual rows for marketplace
			// lines in the same tx as the invoice finalization. The
			// helper skips manual_review invoices internally.
			txErr = operator.ComputeAndPersistCredits(ctx, tx, invoiceID, status)
			if txErr != nil {
				return fmt.Errorf("persist operator credits: %w", txErr)
			}
			// Enqueue Stripe meter events in the outbox. The async
			// flusher (separate worker) reads pending rows and pushes
			// to Stripe; rollback discards the row.
			txErr = billingstripe.EnqueueMeterEvents(ctx, tx, invoiceID, tenantID, status)
			if txErr != nil {
				return fmt.Errorf("enqueue stripe meter events: %w", txErr)
			}
			// manual_review: do not advance the subscription period.
			// Resolution flow is ops fixes pricing → re-finalize → side
			// effects fire once on the corrected total.
			if status == "manual_review" {
				return nil
			}
			result, txErr := tx.ExecContext(ctx, `
					UPDATE purser.tenant_subscriptions
					SET next_billing_date = $1,
					    billing_period_start = $2,
					    billing_period_end = $3,
					    mollie_next_payment_date = CASE
					        WHEN mollie_next_payment_date IS NOT NULL THEN $3::date
					        ELSE mollie_next_payment_date
					    END,
					    updated_at = NOW()
					WHERE tenant_id = $4
				`, nextBillingDate, nextPeriodStart, nextPeriodEnd, tenantID)
			if txErr != nil {
				return fmt.Errorf("advance subscription period: %w", txErr)
			}
			if rows, rowsErr := result.RowsAffected(); rowsErr != nil {
				return fmt.Errorf("advance subscription period rows: %w", rowsErr)
			} else if rows == 0 {
				return fmt.Errorf("advance subscription period: no subscription row for tenant %s", tenantID)
			}
			return nil
		})
		if err != nil {
			jm.logger.WithFields(logging.Fields{
				"error":     err,
				"tenant_id": tenantID,
				"amount":    totalDec.Round(2).String(),
			}).Error("Failed to create invoice")
			continue
		}

		invoicesGenerated++
		totalAmt := totalDec.Round(2).String()
		baseAmt := baseDec.Round(2).String()
		meteredAmt := meteredDec.Round(2).String()
		jm.logger.WithFields(logging.Fields{
			"invoice_id":       invoiceID,
			"tenant_id":        tenantID,
			"tier_name":        tierName,
			"base_amount":      baseAmt,
			"metered_amount":   meteredAmt,
			"total_amount":     totalAmt,
			"currency":         currency,
			"due_date":         dueDate,
			"metering_enabled": meteringEnabled,
		}).Info("Generated monthly invoice")

		// Drain any out-of-order Mollie subscription payment webhooks that
		// landed before the local invoice for this period existed. The
		// webhook handler parked them in mollie_payment_observations; now
		// that the invoice is finalized, attach them and settle through
		// the partial-payment-aware path.
		if status == "pending" {
			if drainErr := drainMolliePaymentObservationsForInvoice(ctx, invoiceID); drainErr != nil {
				jm.logger.WithError(drainErr).WithFields(logging.Fields{
					"tenant_id":  tenantID,
					"invoice_id": invoiceID,
				}).Warn("Failed to drain Mollie payment observations")
			}
		}

		// Overage collection. Provider subscriptions auto-charge the base;
		// Purser collects the remaining invoice amount after prepaid credit.
		// Branch by the tenant's stored provider id so each side only sees the
		// tenants it can charge — the helper itself is a no-op if the tenant's
		// not on that provider. Webhook reconciliation routes through the
		// shared partial-payment-aware settlement path regardless of provider.
		providerChargeDec := totalDec
		if meteredDec.LessThan(providerChargeDec) {
			providerChargeDec = meteredDec
		}
		if status == "pending" && providerChargeDec.GreaterThan(decimal.Zero) {
			if chargeErr := jm.chargeMollieOverage(ctx, tenantID, invoiceID, providerChargeDec, currency); chargeErr != nil {
				jm.logger.WithError(chargeErr).WithFields(logging.Fields{
					"tenant_id":  tenantID,
					"invoice_id": invoiceID,
				}).Warn("Failed to trigger Mollie overage charge")
			}
			if chargeErr := jm.chargeStripeOverage(ctx, tenantID, invoiceID, providerChargeDec, currency); chargeErr != nil {
				jm.logger.WithError(chargeErr).WithFields(logging.Fields{
					"tenant_id":  tenantID,
					"invoice_id": invoiceID,
				}).Warn("Failed to trigger Stripe off-session overage charge")
			}
		}

		// Send invoice created email notification. Read line items from
		// the canonical surface (purser.invoice_line_items); usage_details
		// is raw/debug JSON only, never rendered to customers.
		if billingEmail.Valid && billingEmail.String != "" {
			emailTotal, _ := totalDec.Round(2).Float64()
			emailLines, emailErr := jm.loadEmailLineItems(ctx, invoiceID, tenantID)
			if emailErr != nil {
				jm.logger.WithError(emailErr).WithField("invoice_id", invoiceID).Warn("Failed to load invoice line items for email; sending without breakdown")
			}
			err = jm.emailService.SendInvoiceCreatedEmail(billingEmail.String, "", invoiceID, emailTotal, currency, dueDate, emailLines)
			if err != nil {
				jm.logger.WithError(err).WithFields(logging.Fields{
					"billing_email": billingEmail.String,
					"invoice_id":    invoiceID,
				}).Error("Failed to send invoice created email")
			}
		}

		// Apply any scheduled tier downgrade now that the period's invoice has
		// committed in a non-held state. Three-step ordering favors the user
		// on partial failure: flip tier first, reconcile cluster access second,
		// clear pending_* last. Pending stays set on any error so the next
		// cron tick retries.
		if status != "manual_review" {
			jm.applyPendingDowngrade(ctx, tenantID)
		}
	}
	if err := rows.Err(); err != nil {
		jm.logger.WithError(err).Error("Invoice subscription cursor failed")
	}

	jm.logger.WithFields(logging.Fields{
		"invoices_generated": invoicesGenerated,
	}).Info("Monthly invoice generation completed")
}

func (jm *JobManager) applyDuePendingDowngrades(ctx context.Context, now time.Time) {
	rows, err := jm.db.QueryContext(ctx, `
		SELECT ts.tenant_id
		FROM purser.tenant_subscriptions ts
		WHERE ts.status = 'active'
		  AND ts.pending_tier_id IS NOT NULL
		  AND ts.pending_effective_at <= $1
		  AND EXISTS (
		      SELECT 1
		      FROM purser.billing_invoices bi
		      WHERE bi.tenant_id = ts.tenant_id
		        AND bi.period_end = ts.pending_effective_at
		        AND bi.status NOT IN ('draft', 'manual_review')
		  )
		ORDER BY ts.pending_effective_at ASC, ts.tenant_id ASC
	`, now)
	if err != nil {
		jm.logger.WithError(err).Warn("scan due pending tier downgrades")
		return
	}
	defer rows.Close()

	for rows.Next() {
		var tenantID string
		if err := rows.Scan(&tenantID); err != nil {
			jm.logger.WithError(err).Warn("scan pending downgrade tenant")
			continue
		}
		jm.applyPendingDowngrade(ctx, tenantID)
	}
	if err := rows.Err(); err != nil {
		jm.logger.WithError(err).Warn("iterate due pending tier downgrades")
	}
}

// isMollieMandateRevokedError returns true when the Mollie API error
// indicates the mandate is invalid/revoked rather than a transient
// failure. The Mollie API surfaces these via 422 with the message
// "The mandate is invalid", "Mandate is revoked", or a 410 Gone on the
// mandate id. We pattern-match on the error string because the SDK
// returns the raw text from Mollie.
func isMollieMandateRevokedError(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "mandate") && (strings.Contains(msg, "invalid") || strings.Contains(msg, "revoked") || strings.Contains(msg, "gone"))
}

// mollieFailureCode extracts a short failure code from a Mollie SDK error.
// Mollie does not expose a typed error code through the v4 SDK, so we
// surface the leading clause of the message as a stable code for ops.
func mollieFailureCode(err error) string {
	if err == nil {
		return ""
	}
	msg := err.Error()
	if i := strings.IndexAny(msg, ":,;"); i > 0 && i < 64 {
		return strings.TrimSpace(msg[:i])
	}
	if len(msg) > 64 {
		return msg[:64]
	}
	return msg
}

const maxProviderPaymentAttempts = 3

// chargeStripeOverage collects the metered overage portion of an invoice
// from a Stripe-backed tenant by creating an off-session PaymentIntent
// against the customer's saved card. The Stripe subscription auto-collects
// the recurring base on its own invoice; Purser owns the overage invoice
// and the off-session collection of it. Each call records a
// billing_payment_attempts row with a deterministic Stripe idempotency
// key (invoice_id + attempt_number) so a half-failed attempt cannot
// double-charge. SCA-required outcomes are persisted as a customer-action
// state on payment_provider_intents rather than being treated as a
// failure — the customer must reauthorize before retry.
func (jm *JobManager) chargeStripeOverage(ctx context.Context, tenantID, invoiceID string, overageAmount decimal.Decimal, currency string) error {
	rounded, amountStr, amountCents, amountErr := overageAmountParts(overageAmount, currency)
	if amountErr != nil {
		return amountErr
	}
	if !rounded.GreaterThan(decimal.Zero) {
		return nil
	}

	var stripeCustomerID sql.NullString
	var stripeSubscriptionID sql.NullString
	err := jm.db.QueryRowContext(ctx, `
		SELECT stripe_customer_id, stripe_subscription_id
		FROM purser.tenant_subscriptions
		WHERE tenant_id = $1
		  AND status = 'active'
		  AND stripe_subscription_id IS NOT NULL
	`, tenantID).Scan(&stripeCustomerID, &stripeSubscriptionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("lookup stripe customer/subscription: %w", err)
	}
	if !stripeCustomerID.Valid || stripeCustomerID.String == "" {
		return nil
	}
	if stripeClient == nil {
		return fmt.Errorf("stripe client not configured for active Stripe subscription")
	}

	if amountCents <= 0 {
		return nil
	}

	attemptNumber, err := jm.nextProviderPaymentAttempt(ctx, "stripe", invoiceID)
	if err != nil {
		return err
	}
	if attemptNumber == 0 {
		return nil
	}
	intentKey := fmt.Sprintf("stripe-overage:%s:%d", invoiceID, attemptNumber)
	paymentID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(intentKey)).String()
	intentPlaceholder := "stripe-overage-intent:" + paymentID

	var existingTxID, existingStatus string
	if insertErr := jm.db.QueryRowContext(ctx, `
		INSERT INTO purser.billing_payments (id, invoice_id, method, amount, currency, tx_id, status, created_at, updated_at)
		VALUES ($1, $2, 'card', $3::numeric, $4, $5, 'pending', NOW(), NOW())
		ON CONFLICT (id) DO UPDATE SET updated_at = purser.billing_payments.updated_at
		RETURNING COALESCE(tx_id, ''), status
	`, paymentID, invoiceID, amountStr, currency, intentPlaceholder).Scan(&existingTxID, &existingStatus); insertErr != nil {
		return fmt.Errorf("insert pending billing_payment: %w", insertErr)
	}
	if existingTxID != "" && existingTxID != intentPlaceholder {
		jm.logger.WithFields(logging.Fields{
			"tenant_id":  tenantID,
			"invoice_id": invoiceID,
			"payment_id": paymentID,
			"tx_id":      existingTxID,
			"status":     existingStatus,
		}).Debug("Stripe overage payment already has provider id; skipping duplicate collection")
		return nil
	}

	// Payment-provider intent before the external call so a crash mid-API
	// leaves a trace operators can reconcile against the orphan Stripe
	// PaymentIntent if one was created.
	var providerIntentID string
	if intentErr := jm.db.QueryRowContext(ctx, `
		INSERT INTO purser.payment_provider_intents (
			tenant_id, provider, purpose, local_reference_type, local_reference_id,
			provider_customer_id, status, currency, amount_cents, idempotency_key
		) VALUES ($1, 'stripe', 'stripe_overage_charge', 'invoice', $2::uuid,
		          $3, 'pending', $4, $5, $6)
		ON CONFLICT (provider, idempotency_key) DO UPDATE SET
			attempt_count = purser.payment_provider_intents.attempt_count + 1,
			updated_at = NOW()
		RETURNING id
	`, tenantID, invoiceID, stripeCustomerID.String, currency, amountCents, intentKey).Scan(&providerIntentID); intentErr != nil {
		return fmt.Errorf("insert payment_provider_intents: %w", intentErr)
	}
	// Tie the billing_payments row to the canonical intent.
	if _, linkErr := jm.db.ExecContext(ctx, `
		UPDATE purser.billing_payments SET intent_id = $1, updated_at = NOW() WHERE id = $2
	`, providerIntentID, paymentID); linkErr != nil {
		jm.logger.WithError(linkErr).WithField("payment_id", paymentID).Warn("link billing_payment to intent")
	}

	// Per-attempt row keyed on the Stripe-side idempotency key so retries
	// collapse to one row at the provider too.
	if _, attemptErr := jm.db.ExecContext(ctx, `
		INSERT INTO purser.billing_payment_attempts (
			payment_id, intent_id, attempt_number, idempotency_key, provider, status
		) VALUES ($1, $2, $3, $4, 'stripe', 'pending')
		ON CONFLICT (payment_id, attempt_number) DO NOTHING
	`, paymentID, providerIntentID, attemptNumber, intentKey); attemptErr != nil {
		return fmt.Errorf("insert billing_payment_attempt: %w", attemptErr)
	}
	if _, attemptErr := jm.db.ExecContext(ctx, `
		UPDATE purser.billing_payment_attempts
		SET status = 'pending', next_retry_at = NULL, updated_at = NOW()
		WHERE payment_id = $1 AND attempt_number = $2 AND status = 'provider_call_failed'
	`, paymentID, attemptNumber); attemptErr != nil {
		return fmt.Errorf("prepare billing_payment_attempt retry: %w", attemptErr)
	}

	result, chargeErr := stripeClient.ChargeOffSession(ctx, billingstripe.OffSessionChargeParams{
		CustomerID:       stripeCustomerID.String,
		TenantID:         tenantID,
		InvoiceID:        invoiceID,
		BillingPaymentID: paymentID,
		AmountCents:      amountCents,
		Currency:         currency,
		IdempotencyKey:   intentKey,
		Description:      fmt.Sprintf("Usage overage for invoice %s", invoiceID),
	})
	if chargeErr != nil {
		terminal := attemptNumber >= maxProviderPaymentAttempts
		intentStatus := "provider_call_failed"
		var nextRetry any = time.Now().Add(1 * time.Hour)
		if terminal {
			intentStatus = "terminal_failed"
			nextRetry = nil
		}
		if _, updateErr := jm.db.ExecContext(ctx, `
			UPDATE purser.payment_provider_intents
			SET status = $1, last_error = $2, updated_at = NOW()
			WHERE id = $3
		`, intentStatus, chargeErr.Error(), providerIntentID); updateErr != nil {
			jm.logger.WithError(updateErr).WithField("intent_id", providerIntentID).Warn("mark Stripe overage intent provider_call_failed")
		}
		if _, attemptUpdateErr := jm.db.ExecContext(ctx, `
			UPDATE purser.billing_payment_attempts
			SET status = 'provider_call_failed',
			    failure_code = 'provider_call_error',
			    failure_message = $1,
			    next_retry_at = $2,
			    updated_at = NOW()
			WHERE payment_id = $3 AND attempt_number = $4
		`, chargeErr.Error(), nextRetry, paymentID, attemptNumber); attemptUpdateErr != nil {
			jm.logger.WithError(attemptUpdateErr).WithField("payment_id", paymentID).Warn("mark Stripe overage attempt provider_call_failed")
		}
		if terminal {
			if _, markErr := jm.db.ExecContext(ctx, `
				UPDATE purser.billing_payments
				SET status = 'failed', updated_at = NOW()
				WHERE id = $1 AND status = 'pending'
			`, paymentID); markErr != nil {
				jm.logger.WithError(markErr).WithField("payment_id", paymentID).Warn("mark Stripe overage payment terminal failed")
			}
		}
		jm.logger.WithError(chargeErr).WithFields(logging.Fields{
			"tenant_id":  tenantID,
			"invoice_id": invoiceID,
			"payment_id": paymentID,
		}).Warn("Stripe off-session charge raised SDK error; retry scheduled")
		return chargeErr
	}

	// Persist the provider PaymentIntent id (when known) so webhooks
	// land on the right local payment.
	if result.PaymentIntentID != "" {
		if _, updateErr := jm.db.ExecContext(ctx, `
			UPDATE purser.billing_payments
			SET tx_id = $1, updated_at = NOW()
			WHERE id = $2 AND status = 'pending'
		`, result.PaymentIntentID, paymentID); updateErr != nil {
			return fmt.Errorf("attach Stripe payment_intent id: %w", updateErr)
		}
		if _, intentUpdateErr := jm.db.ExecContext(ctx, `
			UPDATE purser.payment_provider_intents
			SET provider_payment_id = $1, updated_at = NOW()
			WHERE id = $2
		`, result.PaymentIntentID, providerIntentID); intentUpdateErr != nil {
			jm.logger.WithError(intentUpdateErr).WithField("intent_id", providerIntentID).Warn("link provider_payment_id on intent")
		}
		if _, attemptUpdateErr := jm.db.ExecContext(ctx, `
			UPDATE purser.billing_payment_attempts
			SET provider_payment_id = $1, updated_at = NOW()
			WHERE payment_id = $2 AND attempt_number = $3
		`, result.PaymentIntentID, paymentID, attemptNumber); attemptUpdateErr != nil {
			jm.logger.WithError(attemptUpdateErr).WithField("payment_id", paymentID).Warn("link provider_payment_id on attempt")
		}
	}

	switch {
	case result.SCARequired:
		// SCA required: customer must reauthorize. Park the intent in
		// sca_required; the attempt row mirrors that state so the retry
		// job does not re-fire automatically.
		if _, updateErr := jm.db.ExecContext(ctx, `
			UPDATE purser.payment_provider_intents
			SET status = 'sca_required', last_error = $1, updated_at = NOW()
			WHERE id = $2
		`, result.FailureMessage, providerIntentID); updateErr != nil {
			jm.logger.WithError(updateErr).WithField("intent_id", providerIntentID).Warn("mark intent sca_required")
		}
		if _, attemptUpdateErr := jm.db.ExecContext(ctx, `
			UPDATE purser.billing_payment_attempts
			SET status = 'sca_required', failure_code = $1, failure_message = $2, updated_at = NOW()
			WHERE payment_id = $3 AND attempt_number = $4
		`, result.FailureCode, result.FailureMessage, paymentID, attemptNumber); attemptUpdateErr != nil {
			jm.logger.WithError(attemptUpdateErr).WithField("payment_id", paymentID).Warn("mark attempt sca_required")
		}
		// Off-session SCA cannot be completed off-session, and the parked
		// PaymentIntent is not resumable by a payment-method change. The real
		// resume path is on-session: the overage invoice stays pending/overdue
		// and the customer pays it in the billing UI, where hosted Checkout
		// performs the authentication. Direct them there; dunning reminders also
		// cover the invoice if they do not act.
		actionURL := strings.TrimSpace(config.GetEnv("WEBAPP_PUBLIC_URL", ""))
		if actionURL != "" {
			actionURL = strings.TrimRight(actionURL, "/") + "/account/billing"
		}
		jm.logger.WithFields(logging.Fields{
			"tenant_id":         tenantID,
			"invoice_id":        invoiceID,
			"payment_intent_id": result.PaymentIntentID,
			"action_url":        actionURL,
		}).Warn("Stripe off-session overage requires customer authentication (SCA); directing customer to on-session invoice payment")
		go sendTenantActionRequiredEmail(tenantID, invoiceID, float64(amountCents)/100, currency, actionURL)
		return nil

	case result.Status == "failed":
		// Hard failure (card_declined, expired_card, etc.) requires a new
		// customer action or operator decision rather than blind retry.
		if _, updateErr := jm.db.ExecContext(ctx, `
			UPDATE purser.payment_provider_intents
			SET status = 'terminal_failed', last_error = $1, updated_at = NOW()
			WHERE id = $2
		`, result.FailureCode+": "+result.FailureMessage, providerIntentID); updateErr != nil {
			jm.logger.WithError(updateErr).WithField("intent_id", providerIntentID).Warn("mark intent terminal_failed")
		}
		if _, attemptUpdateErr := jm.db.ExecContext(ctx, `
			UPDATE purser.billing_payment_attempts
			SET status = 'failed', failure_code = $1, failure_message = $2, updated_at = NOW()
			WHERE payment_id = $3 AND attempt_number = $4
		`, result.FailureCode, result.FailureMessage, paymentID, attemptNumber); attemptUpdateErr != nil {
			jm.logger.WithError(attemptUpdateErr).WithField("payment_id", paymentID).Warn("mark attempt failed")
		}
		if _, markErr := jm.db.ExecContext(ctx, `
			UPDATE purser.billing_payments
			SET status = 'failed', updated_at = NOW()
			WHERE id = $1 AND status = 'pending'
		`, paymentID); markErr != nil {
			jm.logger.WithError(markErr).WithField("payment_id", paymentID).Warn("mark stripe overage payment failed")
		}
		return fmt.Errorf("stripe off-session overage failed: %s: %s", result.FailureCode, result.FailureMessage)

	case result.Status == string(stripeStatusSucceeded):
		// Sync success: the webhook will still fire and route through
		// updateInvoicePaymentStatus to flip the invoice paid (and
		// account for partial payments). We do not mark confirmed here
		// — the webhook owns that transition under the partial-payment-
		// aware settlement.
		if _, updateErr := jm.db.ExecContext(ctx, `
			UPDATE purser.payment_provider_intents
			SET status = 'provider_open', updated_at = NOW()
			WHERE id = $1
		`, providerIntentID); updateErr != nil {
			jm.logger.WithError(updateErr).WithField("intent_id", providerIntentID).Warn("mark intent provider_open after success")
		}
		jm.logger.WithFields(logging.Fields{
			"tenant_id":         tenantID,
			"invoice_id":        invoiceID,
			"payment_intent_id": result.PaymentIntentID,
		}).Info("Stripe off-session overage charge captured")
		return nil

	default:
		// requires_action without SCA, processing, etc. Leave attempt
		// pending; webhook drives the next state transition.
		if _, updateErr := jm.db.ExecContext(ctx, `
			UPDATE purser.payment_provider_intents
			SET status = 'provider_open', updated_at = NOW()
			WHERE id = $1
		`, providerIntentID); updateErr != nil {
			jm.logger.WithError(updateErr).WithField("intent_id", providerIntentID).Warn("mark intent provider_open")
		}
		return nil
	}
}

// stripeStatusSucceeded matches the Stripe API's "succeeded" enum value
// without taking a runtime dep on stripe-go's PaymentIntentStatus type at
// this call site. Kept as a string constant so callers can compare result
// strings directly.
const stripeStatusSucceeded = "succeeded"

// stripeOverageMinorUnitExponent mirrors currencyMinorUnitExponent in
// webhooks.go for the overage path. We keep them separate to avoid a
// cross-file dep at the call site; both functions agree on the same
// per-currency exponents that Stripe and Mollie use.
func stripeOverageMinorUnitExponent(currency string) int {
	switch strings.ToUpper(currency) {
	case "JPY", "ISK", "KRW", "VND", "CLP", "PYG", "RWF", "UGX", "XAF", "XOF":
		return 0
	case "BHD", "KWD", "OMR", "JOD", "TND":
		return 3
	default:
		return 2
	}
}

func overageAmountParts(amount decimal.Decimal, currency string) (decimal.Decimal, string, int64, error) {
	exponent := stripeOverageMinorUnitExponent(currency)
	if exponent > 2 {
		return decimal.Zero, "", 0, fmt.Errorf("currency %s has %d minor units, but Purser invoice/payment amount columns currently support at most 2", strings.ToUpper(currency), exponent)
	}
	rounded := amount.Round(int32(exponent))
	amountCents := rounded.Shift(int32(exponent)).IntPart()
	return rounded, rounded.StringFixed(int32(exponent)), amountCents, nil
}

func (jm *JobManager) nextProviderPaymentAttempt(ctx context.Context, provider, invoiceID string) (int, error) {
	var attemptNumber int
	var status string
	err := jm.db.QueryRowContext(ctx, `
		SELECT bpa.attempt_number, bpa.status
		FROM purser.billing_payment_attempts bpa
		JOIN purser.billing_payments bp ON bp.id = bpa.payment_id
		WHERE bpa.provider = $1
		  AND bp.invoice_id = $2
		ORDER BY bpa.attempt_number DESC
		LIMIT 1
	`, provider, invoiceID).Scan(&attemptNumber, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return 1, nil
	}
	if err != nil {
		return 0, fmt.Errorf("lookup latest %s payment attempt: %w", provider, err)
	}
	if status != "provider_call_failed" {
		return 0, nil
	}
	if attemptNumber >= maxProviderPaymentAttempts {
		return 0, nil
	}
	return attemptNumber + 1, nil
}

// chargeMollieOverage triggers an on-demand recurring-sequence charge against
// the tenant's stored Mollie mandate for the metered (overage) portion of an
// invoice. The Mollie subscription auto-collects the base; only the overage
// needs explicit collection. A pending billing_payments row is inserted up
// front so updateInvoicePaymentStatus can flip it confirmed when the webhook
// arrives. Each provider call is recorded as a billing_payment_attempts row
// keyed by a deterministic idempotency_key so retries do not double-charge,
// and the mandate is rechecked just before the API call so a revoked mandate
// is flagged terminal rather than failing in a loop.
func (jm *JobManager) chargeMollieOverage(ctx context.Context, tenantID, invoiceID string, overageAmount decimal.Decimal, currency string) error {
	rounded, amountStr, amountCents, amountErr := overageAmountParts(overageAmount, currency)
	if amountErr != nil {
		return amountErr
	}
	if !rounded.GreaterThan(decimal.Zero) {
		return nil
	}

	var mollieCustomerID string
	var mandateID sql.NullString
	var mandateStatus sql.NullString
	err := jm.db.QueryRowContext(ctx, `
		SELECT mc.mollie_customer_id,
			(SELECT mm.mollie_mandate_id
			 FROM purser.mollie_mandates mm
			 WHERE mm.tenant_id = $1 AND mm.status = 'valid'
			 ORDER BY mm.created_at DESC
			 LIMIT 1),
			(SELECT mm.status
			 FROM purser.mollie_mandates mm
			 WHERE mm.tenant_id = $1
			 ORDER BY mm.created_at DESC
			 LIMIT 1)
		FROM purser.mollie_customers mc
		JOIN purser.tenant_subscriptions ts ON ts.tenant_id = mc.tenant_id
		WHERE mc.tenant_id = $1
		  AND ts.status = 'active'
		  AND ts.mollie_subscription_id IS NOT NULL
	`, tenantID).Scan(&mollieCustomerID, &mandateID, &mandateStatus)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("lookup mollie customer/mandate: %w", err)
	}
	if !mandateID.Valid || mandateID.String == "" {
		// Mandate exists in some non-valid state; do not retry blindly.
		if mandateStatus.Valid && mandateStatus.String != "" && mandateStatus.String != "valid" {
			jm.logger.WithFields(logging.Fields{
				"tenant_id":      tenantID,
				"invoice_id":     invoiceID,
				"mandate_status": mandateStatus.String,
			}).Warn("Skipping Mollie overage: mandate not valid")
		}
		return nil
	}
	if mollieClient == nil {
		return fmt.Errorf("mollie client not configured for active Mollie subscription")
	}

	attemptNumber, err := jm.nextProviderPaymentAttempt(ctx, "mollie", invoiceID)
	if err != nil {
		return err
	}
	if attemptNumber == 0 {
		return nil
	}
	idemKey := fmt.Sprintf("mollie-overage:%s:%d", invoiceID, attemptNumber)
	paymentID := uuid.NewSHA1(uuid.NameSpaceOID, []byte(idemKey)).String()
	intentID := "mollie-overage-intent:" + paymentID

	var existingTxID, existingStatus string
	if insertErr := jm.db.QueryRowContext(ctx, `
		INSERT INTO purser.billing_payments (id, invoice_id, method, amount, currency, tx_id, status, created_at, updated_at)
		VALUES ($1, $2, 'card', $3::numeric, $4, $5, 'pending', NOW(), NOW())
		ON CONFLICT (id) DO UPDATE SET updated_at = purser.billing_payments.updated_at
		RETURNING COALESCE(tx_id, ''), status
	`, paymentID, invoiceID, amountStr, currency, intentID).Scan(&existingTxID, &existingStatus); insertErr != nil {
		return fmt.Errorf("insert pending billing_payment: %w", insertErr)
	}
	if existingTxID != "" && existingTxID != intentID {
		jm.logger.WithFields(logging.Fields{
			"tenant_id":  tenantID,
			"invoice_id": invoiceID,
			"payment_id": paymentID,
			"tx_id":      existingTxID,
			"status":     existingStatus,
		}).Debug("Mollie overage payment already has provider id; skipping duplicate collection")
		return nil
	}

	var providerIntentID string
	if intentErr := jm.db.QueryRowContext(ctx, `
		INSERT INTO purser.payment_provider_intents (
			tenant_id, provider, purpose, local_reference_type, local_reference_id,
			provider_customer_id, status, currency, amount_cents, idempotency_key
		) VALUES ($1, 'mollie', 'mollie_overage_charge', 'invoice', $2::uuid,
		          $3, 'pending', $4, $5, $6)
		ON CONFLICT (provider, idempotency_key) DO UPDATE SET
			attempt_count = purser.payment_provider_intents.attempt_count + 1,
			updated_at = NOW()
		RETURNING id
	`, tenantID, invoiceID, mollieCustomerID, currency, amountCents, idemKey).Scan(&providerIntentID); intentErr != nil {
		return fmt.Errorf("insert Mollie payment_provider_intents: %w", intentErr)
	}
	if _, linkErr := jm.db.ExecContext(ctx, `
		UPDATE purser.billing_payments SET intent_id = $1, updated_at = NOW() WHERE id = $2
	`, providerIntentID, paymentID); linkErr != nil {
		jm.logger.WithError(linkErr).WithField("payment_id", paymentID).Warn("link Mollie billing_payment to intent")
	}

	// Per-attempt audit row. The unique constraint on
	// (provider, idempotency_key) collapses retries to the same logical
	// charge attempt; status advances on provider response.
	if _, attemptErr := jm.db.ExecContext(ctx, `
		INSERT INTO purser.billing_payment_attempts (
			payment_id, intent_id, attempt_number, idempotency_key, provider, status
		) VALUES ($1, $2, $3, $4, 'mollie', 'pending')
		ON CONFLICT (payment_id, attempt_number) DO NOTHING
	`, paymentID, providerIntentID, attemptNumber, idemKey); attemptErr != nil {
		return fmt.Errorf("insert billing_payment_attempt: %w", attemptErr)
	}
	if _, attemptErr := jm.db.ExecContext(ctx, `
		UPDATE purser.billing_payment_attempts
		SET status = 'pending', next_retry_at = NULL, updated_at = NOW()
		WHERE payment_id = $1 AND attempt_number = $2 AND status = 'provider_call_failed'
	`, paymentID, attemptNumber); attemptErr != nil {
		return fmt.Errorf("prepare billing_payment_attempt retry: %w", attemptErr)
	}

	webhookURL := ""
	if base := config.GetGatewayPublicURL(); base != "" {
		webhookURL = base + "/webhooks/billing/mollie"
	}

	payment, err := mollieClient.ChargeOnMandate(ctx, billingmollie.OnDemandChargeParams{
		CustomerID:     mollieCustomerID,
		MandateID:      mandateID.String,
		TenantID:       tenantID,
		InvoiceID:      invoiceID,
		PaymentID:      paymentID,
		Amount:         billingmollie.Amount(amountStr, currency),
		Description:    fmt.Sprintf("Usage overage for invoice %s", invoiceID),
		WebhookURL:     webhookURL,
		IdempotencyKey: idemKey,
	})
	if err != nil {
		mandateRevoked := isMollieMandateRevokedError(err)
		attemptStatus := "provider_call_failed"
		var nextRetry any = time.Now().Add(1 * time.Hour)
		if mandateRevoked {
			attemptStatus = "expired"
			nextRetry = nil
		} else if attemptNumber >= maxProviderPaymentAttempts {
			nextRetry = nil
		}
		if _, attemptErr := jm.db.ExecContext(ctx, `
			UPDATE purser.billing_payment_attempts
			SET status = $1,
			    failure_code = $2,
			    failure_message = $3,
			    next_retry_at = $4,
			    updated_at = NOW()
				WHERE payment_id = $5 AND attempt_number = $6
		`, attemptStatus, mollieFailureCode(err), err.Error(), nextRetry, paymentID, attemptNumber); attemptErr != nil {
			jm.logger.WithError(attemptErr).WithField("payment_id", paymentID).Warn("update billing_payment_attempt on failure")
		}
		if mandateRevoked || attemptNumber >= maxProviderPaymentAttempts {
			if _, markErr := jm.db.ExecContext(ctx, `
				UPDATE purser.billing_payments
				SET status = 'failed', updated_at = NOW()
				WHERE id = $1 AND status = 'pending'
			`, paymentID); markErr != nil {
				jm.logger.WithError(markErr).WithField("payment_id", paymentID).Warn("mark Mollie overage payment failed")
			}
		}
		if mandateRevoked {
			// Mark all valid mandates for this tenant as revoked so the
			// next pass picks up the customer-action gate.
			if _, mandateErr := jm.db.ExecContext(ctx, `
				UPDATE purser.mollie_mandates
				SET status = 'revoked', updated_at = NOW()
				WHERE tenant_id = $1 AND status = 'valid'
			`, tenantID); mandateErr != nil {
				jm.logger.WithError(mandateErr).WithField("tenant_id", tenantID).Warn("mark mollie mandate revoked")
			}
		}
		intentStatus := "provider_call_failed"
		if mandateRevoked || attemptNumber >= maxProviderPaymentAttempts {
			intentStatus = "terminal_failed"
		}
		if _, intentErr := jm.db.ExecContext(ctx, `
			UPDATE purser.payment_provider_intents
			SET status = $1, last_error = $2, updated_at = NOW()
			WHERE id = $3
		`, intentStatus, err.Error(), providerIntentID); intentErr != nil {
			jm.logger.WithError(intentErr).WithField("intent_id", providerIntentID).Warn("mark Mollie overage intent failed")
		}
		return fmt.Errorf("mollie on-demand charge: %w", err)
	}

	if _, updateErr := jm.db.ExecContext(ctx, `
		UPDATE purser.billing_payments
		SET tx_id = $1, updated_at = NOW()
		WHERE id = $2 AND status = 'pending'
	`, payment.ID, paymentID); updateErr != nil {
		return fmt.Errorf("attach Mollie payment id: %w", updateErr)
	}
	if _, intentUpdateErr := jm.db.ExecContext(ctx, `
		UPDATE purser.payment_provider_intents
		SET provider_payment_id = $1, status = 'provider_open', updated_at = NOW()
		WHERE id = $2
	`, payment.ID, providerIntentID); intentUpdateErr != nil {
		jm.logger.WithError(intentUpdateErr).WithField("intent_id", providerIntentID).Warn("link Mollie provider payment id on intent")
	}
	if _, attemptUpdateErr := jm.db.ExecContext(ctx, `
		UPDATE purser.billing_payment_attempts
		SET provider_payment_id = $1, updated_at = NOW()
		WHERE payment_id = $2 AND attempt_number = $3
	`, payment.ID, paymentID, attemptNumber); attemptUpdateErr != nil {
		jm.logger.WithError(attemptUpdateErr).WithField("payment_id", paymentID).Warn("link Mollie provider payment id on attempt")
	}

	jm.logger.WithFields(logging.Fields{
		"tenant_id":  tenantID,
		"invoice_id": invoiceID,
		"amount":     amountStr,
		"payment_id": payment.ID,
	}).Info("Triggered Mollie on-demand overage charge")

	return nil
}

// applyPendingDowngrade flips a tenant's tier_id to its staged pending_tier_id,
// reconciles cluster access, and clears the pending columns. Called after the
// period's invoice has committed and is not held. Idempotent — safe to re-run
// on every cron tick.
//
// Ordering favors the user on partial failure: tier flip first (so we never
// bill at the old paid rate after charging downstream consequences), then
// reconcile + cache invalidation, then clear the pending marker. If reconcile
// fails after the tier flips, the tenant temporarily has extra cluster access
// while already on the cheaper tier — preferable to losing paid entitlements.
func (jm *JobManager) applyPendingDowngrade(ctx context.Context, tenantID string) {
	var (
		pendingTierID    sql.NullString
		pendingDue       sql.NullTime
		currentTierID    string
		pendingTierLevel sql.NullInt32
	)
	err := jm.db.QueryRowContext(ctx, `
		SELECT ts.tier_id,
		       ts.pending_tier_id,
		       ts.pending_effective_at,
		       bt.tier_level
		FROM purser.tenant_subscriptions ts
		LEFT JOIN purser.billing_tiers bt ON bt.id = ts.pending_tier_id
		WHERE ts.tenant_id = $1
	`, tenantID).Scan(&currentTierID, &pendingTierID, &pendingDue, &pendingTierLevel)
	if errors.Is(err, sql.ErrNoRows) {
		return
	}
	if err != nil {
		jm.logger.WithError(err).WithField("tenant_id", tenantID).Warn("load pending tier for downgrade applier")
		return
	}
	if !pendingTierID.Valid || pendingTierID.String == "" {
		return
	}
	if !pendingDue.Valid || pendingDue.Time.After(time.Now()) {
		return
	}
	if !pendingTierLevel.Valid {
		jm.logger.WithFields(logging.Fields{
			"tenant_id":       tenantID,
			"pending_tier_id": pendingTierID.String,
		}).Warn("pending tier id references missing billing_tiers row")
		return
	}
	if jm.tierReconciler == nil {
		jm.logger.WithField("tenant_id", tenantID).Warn("downgrade applier has no tier reconciler configured")
		return
	}

	stagedTarget := pendingTierID.String
	targetLevel := pendingTierLevel.Int32

	// Step 1: flip tier_id in its own short transaction, but keep pending_*
	// set as the "reconcile-not-yet-applied" marker. Conditional on the
	// staged target so a racing ChangeBillingTier that re-pointed the
	// pending is not clobbered.
	result, err := jm.db.ExecContext(ctx, `
		UPDATE purser.tenant_subscriptions
		SET tier_id = $1,
		    updated_at = NOW()
		WHERE tenant_id = $2 AND pending_tier_id = $1
	`, stagedTarget, tenantID)
	if err != nil {
		jm.logger.WithError(err).WithField("tenant_id", tenantID).Warn("flip tier_id for pending downgrade")
		return
	}
	rows, rowsErr := result.RowsAffected()
	if rowsErr != nil {
		jm.logger.WithError(rowsErr).WithField("tenant_id", tenantID).Warn("rows affected for pending downgrade flip")
		return
	}
	if rows == 0 {
		// Race: pending_tier_id changed since we read it. Next tick handles
		// the new state.
		return
	}

	// Step 2: reconcile cluster access + invalidate Commodore cache. Idempotent.
	if _, _, err := jm.tierReconciler.Reconcile(ctx, tenantID, targetLevel); err != nil {
		jm.logger.WithError(err).WithField("tenant_id", tenantID).Warn("reconcile cluster access for pending downgrade; will retry next tick")
		return
	}
	if jm.commodoreClient != nil {
		invalidateCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if _, invErr := jm.commodoreClient.InvalidateTenantCache(invalidateCtx, tenantID, "tier_changed"); invErr != nil {
			jm.logger.WithError(invErr).WithField("tenant_id", tenantID).Warn("invalidate tenant cache after pending downgrade; will retry next tick")
			cancel()
			return
		}
		cancel()
	}

	// Step 3: clear the pending marker. Conditional on the tier already
	// matching the staged target so a concurrent re-stage is not erased.
	if _, err := jm.db.ExecContext(ctx, `
		UPDATE purser.tenant_subscriptions
		SET pending_tier_id = NULL,
		    pending_effective_at = NULL,
		    pending_reason = NULL,
		    updated_at = NOW()
		WHERE tenant_id = $1 AND tier_id = $2 AND pending_tier_id = $2
	`, tenantID, stagedTarget); err != nil {
		jm.logger.WithError(err).WithField("tenant_id", tenantID).Warn("clear pending downgrade marker; will retry next tick")
		return
	}

	jm.logger.WithFields(logging.Fields{
		"tenant_id":  tenantID,
		"from_tier":  currentTierID,
		"to_tier":    stagedTarget,
		"tier_level": targetLevel,
	}).Info("Pending tier downgrade applied")
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
			jm.retryFailedPayments(ctx)
			jm.retryProviderPaymentAttempts(ctx)
			jm.sendPaymentReminders(ctx)
		}
	}
}

// retryFailedPayments retries payments that failed due to temporary issues
func (jm *JobManager) retryFailedPayments(ctx context.Context) {
	// Mark failed traditional payments for retry (crypto payments don't need retry)
	_, err := jm.db.ExecContext(ctx, `
		UPDATE purser.billing_payments bp
		SET status = 'pending', updated_at = NOW()
		WHERE bp.status = 'failed'
		  AND bp.method = 'card'
		  AND bp.created_at > NOW() - INTERVAL '24 hours'
		  AND bp.updated_at < NOW() - INTERVAL '1 hour'
		  AND EXISTS (
			SELECT 1
			FROM purser.billing_invoices bi
			WHERE bi.id = bp.invoice_id
			  AND bi.status IN ('pending', 'overdue')
		  )
	`)

	if err != nil {
		jm.logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to retry payments")
	} else {
		jm.logger.Info("Marked eligible failed payments for retry")
	}
}

func (jm *JobManager) retryProviderPaymentAttempts(ctx context.Context) {
	rows, err := jm.db.QueryContext(ctx, `
		SELECT bpa.provider, bi.tenant_id::text, bp.invoice_id::text, bp.amount::text, bp.currency
		FROM purser.billing_payment_attempts bpa
		JOIN purser.billing_payments bp ON bp.id = bpa.payment_id
		JOIN purser.billing_invoices bi ON bi.id = bp.invoice_id
		WHERE bpa.status = 'provider_call_failed'
		  AND bpa.next_retry_at IS NOT NULL
		  AND bpa.next_retry_at <= NOW()
		  AND bpa.attempt_number < $1
		  AND bi.status IN ('pending', 'overdue')
		  AND NOT EXISTS (
		      SELECT 1
		      FROM purser.billing_payment_attempts newer
		      JOIN purser.billing_payments newer_bp ON newer_bp.id = newer.payment_id
		      WHERE newer.provider = bpa.provider
		        AND newer_bp.invoice_id = bp.invoice_id
		        AND newer.attempt_number > bpa.attempt_number
		  )
		ORDER BY bpa.next_retry_at ASC
		LIMIT 50
	`, maxProviderPaymentAttempts)
	if err != nil {
		jm.logger.WithError(err).Error("Failed to fetch provider payment attempts for retry")
		return
	}
	defer func() { _ = rows.Close() }()

	for rows.Next() {
		var provider, tenantID, invoiceID, amountText, currency string
		if err := rows.Scan(&provider, &tenantID, &invoiceID, &amountText, &currency); err != nil {
			jm.logger.WithError(err).Warn("Failed to scan provider payment attempt")
			continue
		}
		amount, parseErr := decimal.NewFromString(amountText)
		if parseErr != nil {
			jm.logger.WithError(parseErr).WithField("invoice_id", invoiceID).Warn("Failed to parse provider retry amount")
			continue
		}
		var retryErr error
		switch provider {
		case "stripe":
			retryErr = jm.chargeStripeOverage(ctx, tenantID, invoiceID, amount, currency)
		case "mollie":
			retryErr = jm.chargeMollieOverage(ctx, tenantID, invoiceID, amount, currency)
		default:
			jm.logger.WithField("provider", provider).Warn("Unknown provider payment attempt provider")
			continue
		}
		if retryErr != nil {
			jm.logger.WithError(retryErr).WithFields(logging.Fields{
				"provider":   provider,
				"tenant_id":  tenantID,
				"invoice_id": invoiceID,
			}).Warn("Provider payment attempt retry failed")
		}
	}
	if err := rows.Err(); err != nil {
		jm.logger.WithError(err).Error("Provider payment attempt retry rows failed")
	}
}

// sendPaymentReminders sends reminders for overdue invoices
func (jm *JobManager) sendPaymentReminders(ctx context.Context) {
	// Get overdue invoices with tenant subscription information
	rows, err := jm.db.QueryContext(ctx, `
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
			_, execErr := jm.db.ExecContext(ctx, `
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
			jm.cleanupExpiredWallets(ctx)
		}
	}
}

// cleanupExpiredWallets marks expired crypto wallets as inactive
func (jm *JobManager) cleanupExpiredWallets(ctx context.Context) {
	result, err := jm.db.ExecContext(ctx, `
		UPDATE purser.crypto_wallets
		SET status = 'expired', updated_at = NOW()
		WHERE status IN ('pending', 'confirming')
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
// Periscope produces tenant usage summaries to Kafka; Purser persists them
// and rates them through the billing engine.
// ============================================================================

func parseUsageSummaryPeriod(summary models.UsageSummary) (time.Time, time.Time, string, error) {
	parts := strings.Split(summary.Period, "/")
	if len(parts) != 2 {
		return time.Time{}, time.Time{}, "", fmt.Errorf("invalid usage period %q", summary.Period)
	}
	periodStart, err := time.Parse(time.RFC3339, parts[0])
	if err != nil {
		return time.Time{}, time.Time{}, "", fmt.Errorf("parse usage period start %q: %w", parts[0], err)
	}
	periodEnd, err := time.Parse(time.RFC3339, parts[1])
	if err != nil {
		return time.Time{}, time.Time{}, "", fmt.Errorf("parse usage period end %q: %w", parts[1], err)
	}
	if !periodEnd.After(periodStart) {
		return time.Time{}, time.Time{}, "", fmt.Errorf("non-positive usage period %q", summary.Period)
	}

	granularity := "minute_5"
	duration := periodEnd.Sub(periodStart)
	switch {
	case duration >= 28*24*time.Hour:
		granularity = "monthly"
	case duration >= 24*time.Hour:
		granularity = "daily"
	case duration >= time.Hour:
		granularity = "hourly"
	}
	return periodStart, periodEnd, granularity, nil
}

// processUsageSummary processes a single usage summary and stores it in the usage records table
func (jm *JobManager) processUsageSummary(ctx context.Context, summary models.UsageSummary, source string) ([]canonicalUsageDelta, error) {
	periodStart, periodEnd, granularity, err := parseUsageSummaryPeriod(summary)
	if err != nil {
		return nil, err
	}

	// Use shared helper for usage data extraction
	usageTypes := buildUsageDataFromSummary(summary)

	codecSeconds, processCodecSeconds := processingDetailsFromSummary(summary)
	processSeconds := map[string]float64{}
	for key, seconds := range processCodecSeconds {
		processType, _, ok := strings.Cut(key, ":")
		if ok {
			processSeconds[processType] += seconds
		}
	}
	usageDetails := models.JSONB{
		"max_viewers":           summary.MaxViewers,
		"total_viewers":         summary.TotalViewers,
		"total_streams":         summary.TotalStreams,
		"unique_users":          summary.UniqueUsers,
		"source":                source,
		"codec_seconds":         codecSeconds,
		"process_seconds":       processSeconds,
		"process_codec_seconds": processCodecSeconds,
		"api_requests":          summary.APIRequests,
		"api_errors":            summary.APIErrors,
		"api_duration_ms":       summary.APIDurationMs,
		"api_complexity":        summary.APIComplexity,
		"api_breakdown":         summary.APIBreakdown,
	}
	acceptedUsage := []canonicalUsageDelta{}

	// Upsert each usage type
	for usageType, usageValue := range usageTypes {
		if usageValue <= 0 {
			continue
		}

		// Per-record validation: rated meters must come in as 5-minute
		// delta rows on aligned period boundaries. Mismatches go to
		// purser.usage_records_quarantine with the rejection reason so
		// operators can inspect; the bad row is NOT written to
		// usage_records and therefore never billed. See
		// docs/architecture/meter-contracts.md.
		valueKind := "delta"
		rejection := validateUsageRecord(usageType, usageValue, periodStart, periodEnd, granularity, valueKind)
		if rejection == "" && summary.ClusterID == "" {
			rejection = "missing_cluster_id"
		}
		if rejection != "" {
			if _, qErr := jm.db.ExecContext(ctx, `
				INSERT INTO purser.usage_records_quarantine
					(tenant_id, cluster_id, usage_type, usage_value, usage_details,
					 period_start, period_end, granularity, value_kind,
					 rejected_reason, source, raw_payload)
				VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
			`, summary.TenantID, summary.ClusterID, usageType, usageValue, usageDetails,
				periodStart, periodEnd, granularity, valueKind, rejection, source, usageDetails); qErr != nil {
				jm.logger.WithError(qErr).WithFields(logging.Fields{
					"tenant_id":  summary.TenantID,
					"usage_type": usageType,
				}).Warn("Failed to write usage_records_quarantine row")
			}
			if metrics != nil && metrics.UsageQuarantine != nil {
				metrics.UsageQuarantine.WithLabelValues(usageType, rejection).Inc()
			}
			continue
		}

		_, err := jm.db.ExecContext(ctx, `
			INSERT INTO purser.usage_records (tenant_id, cluster_id, usage_type, usage_value, usage_details, period_start, period_end, granularity, value_kind, created_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, NOW())
			ON CONFLICT (tenant_id, cluster_id, usage_type, period_start, period_end) DO UPDATE SET
				usage_value = EXCLUDED.usage_value,
				usage_details = EXCLUDED.usage_details,
				granularity = EXCLUDED.granularity,
				value_kind = EXCLUDED.value_kind,
				updated_at = NOW()
		`, summary.TenantID, summary.ClusterID, usageType, usageValue, usageDetails, periodStart, periodEnd, granularity, valueKind)

		if err != nil {
			return nil, fmt.Errorf("failed to upsert %s: %w", usageType, err)
		}
		if metrics != nil && metrics.UsageRecords != nil {
			metrics.UsageRecords.WithLabelValues(usageType).Inc()
		}
		acceptedUsage = append(acceptedUsage, canonicalUsageDelta{
			clusterID:    summary.ClusterID,
			usageType:    usageType,
			usageValue:   usageValue,
			usageDetails: usageDetails,
		})
	}

	if persistErr := jm.persistStorageProviderUsage(ctx, summary, periodStart, periodEnd, granularity, source); persistErr != nil {
		return nil, persistErr
	}
	acceptedAdjustments, err := jm.persistUsageAdjustments(ctx, summary, source)
	if err != nil {
		return nil, err
	}
	acceptedUsage = append(acceptedUsage, acceptedAdjustments...)

	return acceptedUsage, nil
}

func (jm *JobManager) persistStorageProviderUsage(ctx context.Context, summary models.UsageSummary, periodStart, periodEnd time.Time, granularity, source string) error {
	if rejection := validateCanonicalUsageWindow(periodStart, periodEnd, granularity, "delta"); rejection != "" {
		return fmt.Errorf("reject storage provider usage for non-canonical period: %s", rejection)
	}
	for _, rec := range summary.StorageProviderUsage {
		if rec.GBSeconds <= 0 {
			continue
		}
		if rec.CustomerClusterID == "" {
			rec.CustomerClusterID = summary.ClusterID
		}
		if rec.CustomerClusterID == "" {
			return fmt.Errorf("storage provider usage missing customer_cluster_id")
		}
		if rec.UsageType == "" {
			switch rec.StorageScope {
			case "cold":
				rec.UsageType = string(rating.MeterStorageGBSecondsCld)
			default:
				rec.UsageType = string(rating.MeterStorageGBSecondsHot)
			}
		}
		if !rating.ValidMeter(rating.Meter(rec.UsageType)) {
			return fmt.Errorf("invalid storage provider usage_type %q", rec.UsageType)
		}
		details := models.JSONB{
			"source":                      source,
			"customer_cluster_id":         rec.CustomerClusterID,
			"storage_provider_tenant_id":  rec.StorageProviderTenantID,
			"storage_provider_cluster_id": rec.StorageProviderClusterID,
			"storage_backend":             rec.StorageBackend,
			"storage_scope":               rec.StorageScope,
		}
		_, err := jm.db.ExecContext(ctx, `
			INSERT INTO purser.storage_provider_usage_records (
				usage_tenant_id, customer_cluster_id,
				storage_provider_tenant_id, storage_provider_cluster_id,
				storage_backend, storage_scope, usage_type, gb_seconds,
				period_start, period_end, granularity, value_kind, source, usage_details
			) VALUES (
				$1, $2, $3, $4, $5, $6, $7, $8,
				$9, $10, 'minute_5', 'delta', $11, $12
			)
			ON CONFLICT (
				usage_tenant_id, customer_cluster_id,
				storage_provider_tenant_id, storage_provider_cluster_id,
				storage_backend, storage_scope, usage_type,
				period_start, period_end
			) DO UPDATE SET
				gb_seconds = EXCLUDED.gb_seconds,
				granularity = EXCLUDED.granularity,
				value_kind = EXCLUDED.value_kind,
				source = EXCLUDED.source,
				usage_details = EXCLUDED.usage_details,
				updated_at = NOW()
		`, summary.TenantID, rec.CustomerClusterID,
			rec.StorageProviderTenantID, rec.StorageProviderClusterID,
			rec.StorageBackend, rec.StorageScope, rec.UsageType, rec.GBSeconds,
			periodStart, periodEnd, source, details)
		if err != nil {
			return fmt.Errorf("upsert storage provider usage %s/%s: %w", rec.StorageProviderTenantID, rec.UsageType, err)
		}
	}
	return nil
}

func (jm *JobManager) persistUsageAdjustments(ctx context.Context, summary models.UsageSummary, source string) ([]canonicalUsageDelta, error) {
	accepted := []canonicalUsageDelta{}
	for _, adj := range summary.UsageAdjustments {
		if adj.DeltaValue == 0 {
			continue
		}
		if !rating.ValidMeter(rating.Meter(adj.UsageType)) {
			return nil, fmt.Errorf("invalid usage adjustment usage_type %q", adj.UsageType)
		}
		if adj.SourceSystem == "" || adj.SourceID == "" {
			return nil, fmt.Errorf("usage adjustment missing source identity for %s", adj.UsageType)
		}
		if adj.ClusterID == "" {
			adj.ClusterID = summary.ClusterID
		}
		if adj.ClusterID == "" {
			return nil, fmt.Errorf("usage adjustment %s missing cluster_id", adj.SourceID)
		}
		if adj.PeriodStart.IsZero() || adj.PeriodEnd.IsZero() || !adj.PeriodEnd.After(adj.PeriodStart) {
			return nil, fmt.Errorf("usage adjustment %s has invalid period", adj.SourceID)
		}
		if adj.Details == nil {
			adj.Details = models.JSONB{}
		}
		adj.Details["source"] = source
		_, err := jm.db.ExecContext(ctx, `
			INSERT INTO purser.usage_adjustments (
				tenant_id, cluster_id, usage_type, delta_value,
				period_start, period_end, value_kind, status,
				source_system, source_id, reason, details
			) VALUES (
				$1, $2, $3, $4,
				$5, $6, 'correction_delta', 'applied',
				$7, $8, $9, $10
			)
			ON CONFLICT (source_system, source_id) DO UPDATE SET
				tenant_id = EXCLUDED.tenant_id,
				cluster_id = EXCLUDED.cluster_id,
				usage_type = EXCLUDED.usage_type,
				delta_value = EXCLUDED.delta_value,
				period_start = EXCLUDED.period_start,
				period_end = EXCLUDED.period_end,
				status = EXCLUDED.status,
				reason = EXCLUDED.reason,
				details = EXCLUDED.details,
				updated_at = NOW()
		`, summary.TenantID, adj.ClusterID, adj.UsageType, adj.DeltaValue,
			adj.PeriodStart, adj.PeriodEnd, adj.SourceSystem, adj.SourceID, adj.Reason, adj.Details)
		if err != nil {
			return nil, fmt.Errorf("upsert usage adjustment %s: %w", adj.SourceID, err)
		}
		accepted = append(accepted, canonicalUsageDelta{
			clusterID:    adj.ClusterID,
			usageType:    adj.UsageType,
			usageValue:   adj.DeltaValue,
			usageDetails: adj.Details,
		})
	}
	return accepted, nil
}

// validateUsageRecord checks per-meter constraints. Returns "" on
// success or a rejection_reason string on failure.
func validateUsageRecord(usageType string, usageValue float64, periodStart, periodEnd time.Time, granularity, valueKind string) string {
	if usageValue < 0 {
		return "negative_value"
	}
	if !rating.ValidMeter(rating.Meter(usageType)) {
		return "invalid_meter"
	}
	if rejection := validateCanonicalUsageWindow(periodStart, periodEnd, granularity, valueKind); rejection != "" {
		return rejection
	}
	return ""
}

func validateCanonicalUsageWindow(periodStart, periodEnd time.Time, granularity, valueKind string) string {
	if periodEnd.IsZero() || periodStart.IsZero() {
		return "missing_period"
	}
	if !periodEnd.After(periodStart) {
		return "non_positive_period"
	}
	if valueKind != "delta" {
		return "value_kind_mismatch"
	}
	if granularity != "minute_5" {
		return "granularity_unsupported"
	}
	if periodEnd.Sub(periodStart) != 5*time.Minute {
		return "period_duration_mismatch"
	}
	// 5-min boundary alignment check.
	const fiveMin = 5 * 60
	if periodStart.Unix()%fiveMin != 0 || periodEnd.Unix()%fiveMin != 0 {
		return "period_misaligned"
	}
	return ""
}

// updateInvoiceDraft creates or updates an invoice draft for the tenant based on usage
func (jm *JobManager) updateInvoiceDraft(ctx context.Context, tenantID string) error {
	tier, err := billingpkg.LoadEffectiveTier(ctx, jm.db, tenantID)
	if errors.Is(err, sql.ErrNoRows) {
		jm.logger.WithField("tenant_id", tenantID).Info("No active subscription, skipping invoice draft")
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to load effective tier: %w", err)
	}
	tierID := tier.TierID
	tierName := tier.TierName
	displayName := tier.TierName
	basePrice, _ := tier.BasePrice.Float64()
	currency := tier.Currency
	meteringEnabled := tier.MeteringEnabled

	// Get current billing period
	now := time.Now()
	periodStart, periodEnd, periodErr := loadSubscriptionPeriod(ctx, jm.db, tenantID, now)
	if periodErr != nil {
		return periodErr
	}

	// manual_review is a hold, not a terminal state — let the draft refresh
	// re-rate it once ops fixes the cluster pricing. Only truly finalized
	// invoices block draft updates.
	var finalizedCount int
	if countErr := jm.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM purser.billing_invoices
		WHERE tenant_id = $1
		  AND period_start = $2
		  AND status NOT IN ('draft', 'manual_review')
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

	// Aggregate usage via the shared fail-closed helper; query/scan/iteration
	// errors abort the draft update so we never apply the wrong prepaid
	// credit on partial usage and ack the Kafka message as processed.
	perClusterUsage, err := jm.collectInvoiceUsage(ctx, tenantID, periodStart, periodEnd)
	if err != nil {
		return fmt.Errorf("collect invoice usage: %w", err)
	}
	perClusterCodecBreakdowns, err := jm.collectInvoiceCodecBreakdowns(ctx, tenantID, periodStart, periodEnd)
	if err != nil {
		return fmt.Errorf("collect invoice codec breakdown: %w", err)
	}
	usageTotals := flattenUsageAcrossClusters(perClusterUsage)

	// Provider-managed base detection: external recurring subscription owns
	// the base fee. The draft mirrors that by emitting a $0 informational
	// included_subscription base line instead of duplicating the tier's base
	// price. A query failure aborts the draft so we never emit a wrong base
	// silently — the next Kafka redelivery retries.
	var stripeSubID, mollieSubID sql.NullString
	if scanErr := jm.db.QueryRowContext(ctx, `
		SELECT stripe_subscription_id, mollie_subscription_id
		FROM purser.tenant_subscriptions
		WHERE tenant_id = $1
	`, tenantID).Scan(&stripeSubID, &mollieSubID); scanErr != nil && !errors.Is(scanErr, sql.ErrNoRows) {
		return fmt.Errorf("read provider sub ids for draft: %w", scanErr)
	}
	baseProviderManaged := stripeSubID.Valid || mollieSubID.Valid

	// Rate the period via the engine; one source of truth for invoice math.
	// Money stays as decimal.Decimal end-to-end and binds to NUMERIC columns
	// as decimal strings; float64 never touches the cents.
	ratingResult, err := jm.rateInvoiceForTenant(ctx, tenantID, periodStart, periodEnd, tier, true, baseProviderManaged, perClusterUsage, perClusterCodecBreakdowns)
	if err != nil {
		return fmt.Errorf("rate usage: %w", err)
	}
	baseDec := ratingResult.BaseAmount
	meteredDec := ratingResult.UsageAmount
	grossDec := ratingResult.TotalAmount

	// manual_review: an unconfigured cluster pricing means we cannot finalize
	// the credit. Hold the entire draft — no prepaid deduction, no draft
	// invoice write, no period advance. Operator fixes pricing then re-runs.
	if len(ratingResult.ManualReviewReasons) > 0 {
		jm.logger.WithFields(logging.Fields{
			"tenant_id": tenantID,
			"reasons":   strings.Join(ratingResult.ManualReviewReasons, "; "),
		}).Warn("Invoice draft routed to manual_review; deduction halted")
		// Persist a manual_review header so ops can see and act on it. No
		// credit is deducted; lines are written for visibility.
		return jm.persistManualReviewDraft(ctx, tenantID, periodStart, periodEnd, currency, ratingResult)
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
	for k, v := range usageTotals {
		usageDetails[k] = v
	}

	usageJSON, err := json.Marshal(usageDetails)
	if err != nil {
		jm.logger.WithError(err).WithField("tenant_id", tenantID).Warn("Failed to marshal usage details for invoice draft")
		usageJSON = []byte("{}")
	}

	// Apply prepaid credit, write invoice header + line items in one
	// transaction so the credit and the invoice always commit together. If any
	// step fails, the credit is not deducted from the prepaid balance.
	//
	// Credit is delta-based for the period. Reruns preserve prior credit, while
	// larger drafts apply only the missing amount up to the current balance.
	dueDate := periodEnd.AddDate(0, 0, 14)
	var invoiceID string
	var prepaidCreditDec decimal.Decimal
	var netDec decimal.Decimal
	hundred := decimal.NewFromInt(100)
	err = withTx(ctx, jm.db, func(tx *sql.Tx) error {
		grossCents := grossDec.Mul(hundred).Round(0).IntPart()
		appliedCreditCents, txErr := jm.applyInvoicePrepaidCreditTx(ctx, tx, tenantID, periodStart, grossCents)
		if txErr != nil {
			return txErr
		}
		prepaidCreditDec = decimal.NewFromInt(appliedCreditCents).Div(hundred)
		totalDec := grossDec.Sub(prepaidCreditDec)
		if totalDec.IsNegative() {
			totalDec = decimal.Zero
		}
		netDec = totalDec

		// Pass decimals as strings into Postgres NUMERIC columns so no float64
		// rounding can sneak in at the SQL boundary. PG parses '1.99'::numeric
		// exactly; '1.9900000000000002'::float8 ≠ 1.99.
		totalAmt := totalDec.Round(2).String()
		baseAmt := baseDec.Round(2).String()
		meteredAmt := meteredDec.Round(2).String()
		creditAmt := prepaidCreditDec.Round(2).String()

		txErr = tx.QueryRowContext(ctx, `
				INSERT INTO purser.billing_invoices (
					id, tenant_id, amount, currency, status, due_date,
					base_amount, metered_amount, prepaid_credit_applied, usage_details,
					period_start, period_end,
					created_at, updated_at
				) VALUES (
					gen_random_uuid(), $1, $2::numeric, $3, 'draft', $4,
					$5::numeric, $6::numeric, $7::numeric, $8, $9, $10, NOW(), NOW()
				)
				ON CONFLICT (tenant_id, period_start) WHERE period_start IS NOT NULL
				DO UPDATE SET
					amount = EXCLUDED.amount,
					currency = EXCLUDED.currency,
					status = 'draft',
					due_date = EXCLUDED.due_date,
					base_amount = EXCLUDED.base_amount,
					metered_amount = EXCLUDED.metered_amount,
					prepaid_credit_applied = EXCLUDED.prepaid_credit_applied,
					usage_details = EXCLUDED.usage_details,
					period_end = EXCLUDED.period_end,
					updated_at = NOW()
				WHERE purser.billing_invoices.status IN ('draft', 'manual_review')
				RETURNING id
			`, tenantID, totalAmt, currency, dueDate, baseAmt, meteredAmt, creditAmt, usageJSON, periodStart, periodEnd).Scan(&invoiceID)
		if txErr != nil {
			return fmt.Errorf("upsert invoice draft: %w", txErr)
		}
		if txErr = persistInvoiceLineItems(ctx, tx, invoiceID, tenantID, ratingResult); txErr != nil {
			return txErr
		}
		_, txErr = tx.ExecContext(ctx, `
				UPDATE purser.tenant_subscriptions
				SET billing_period_start = COALESCE(billing_period_start, $2),
				    billing_period_end = COALESCE(billing_period_end, $3),
				    next_billing_date = COALESCE(next_billing_date, $3),
				    updated_at = CASE
				        WHEN billing_period_start IS NULL OR billing_period_end IS NULL OR next_billing_date IS NULL
				        THEN NOW()
				        ELSE updated_at
				    END
				WHERE tenant_id = $1
				  AND status = 'active'
			`, tenantID, periodStart, periodEnd)
		if txErr != nil {
			return fmt.Errorf("backfill subscription period from draft: %w", txErr)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("invoice draft transaction: %w", err)
	}
	_ = invoiceID
	jm.logger.WithFields(logging.Fields{
		"tenant_id":              tenantID,
		"billing_period":         periodStart.Format("2006-01"),
		"gross_amount":           grossDec.String(),
		"prepaid_credit_applied": prepaidCreditDec.String(),
		"net_amount":             netDec.String(),
	}).Info("Updated invoice draft")

	return nil
}
