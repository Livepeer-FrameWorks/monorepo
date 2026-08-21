package handlers

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/sirupsen/logrus"

	billingpkg "frameworks/api_billing/internal/billing"
	"frameworks/api_billing/internal/database/purserdb"
	billingmollie "frameworks/api_billing/internal/mollie"
	"frameworks/api_billing/internal/operator"
	"frameworks/api_billing/internal/pricing"
	"frameworks/api_billing/internal/rating"
	billingstripe "frameworks/api_billing/internal/stripe"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	decklog "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/decklog"
	periscope "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/periscope"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/models"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	foghorncontrolpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
)

type canonicalUsageDelta struct {
	clusterID    string
	usageType    string
	unit         string
	dimensions   models.JSONB
	usageValue   float64
	usageDetails models.JSONB
}

func normalizedUsageDimensions(dimensions models.JSONB) models.JSONB {
	if dimensions == nil {
		return models.JSONB{}
	}
	return dimensions
}

func (jm *JobManager) quarantineUsageReport(ctx context.Context, msg kafka.Message, summary *models.UsageSummary, reason string) error {
	reportID, sourceID, tenantID := "", "", any(nil)
	if summary != nil {
		reportID = summary.ReportID
		sourceID = summary.SourceID
		if parsed, err := uuid.Parse(summary.TenantID); err == nil {
			tenantID = parsed
		}
	}
	rawPayload := models.JSONB{"base64": base64.StdEncoding.EncodeToString(msg.Value)}
	if json.Valid(msg.Value) {
		var decoded any
		if err := json.Unmarshal(msg.Value, &decoded); err == nil {
			rawPayload = models.JSONB{"payload": decoded}
		}
	}
	raw, err := json.Marshal(rawPayload)
	if err != nil {
		return err
	}
	tenant := sql.NullString{}
	if id, ok := tenantID.(uuid.UUID); ok {
		tenant = sql.NullString{String: id.String(), Valid: true}
	}
	return purserdb.New(jm.db).InsertUsageReportQuarantine(ctx, purserdb.InsertUsageReportQuarantineParams{
		ReportID: reportID, SourceID: sourceID, TenantID: tenant, RejectedReason: reason,
		SourceTopic: msg.Topic, SourcePartition: sql.NullInt32{Int32: msg.Partition, Valid: true},
		SourceOffset: sql.NullInt64{Int64: msg.Offset, Valid: true}, RawPayload: raw,
	})
}

func validateUsageSummaryEnvelope(summary models.UsageSummary) error {
	if len(summary.ReportID) != 64 {
		return errors.New("invalid_report_id")
	}
	if summary.SourceID == "" {
		return errors.New("missing_source_id")
	}
	if summary.ReportKind != "finalized" && summary.ReportKind != "reservation" && summary.ReportKind != "window_complete" {
		return errors.New("invalid_report_kind")
	}
	if summary.Sequence == 0 {
		return errors.New("missing_sequence")
	}
	if _, err := uuid.Parse(summary.TenantID); err != nil {
		return errors.New("invalid_tenant_id")
	}
	if summary.ClusterID == "" {
		return errors.New("missing_cluster_id")
	}
	if _, _, _, err := parseUsageSummaryPeriod(summary); err != nil {
		return errors.New("invalid_period")
	}
	if (summary.ReportKind == "finalized" || summary.ReportKind == "window_complete") && !summary.Complete {
		return errors.New("incomplete_finalized_report")
	}
	if summary.ReportKind == "window_complete" && (len(summary.Meters) != 0 || len(summary.ProviderUsage) != 0 || len(summary.UsageAdjustments) != 0) {
		return errors.New("window_complete_contains_usage")
	}
	return nil
}

func (jm *JobManager) validateUsageSummaryMeters(ctx context.Context, summary models.UsageSummary) error {
	for _, meter := range summary.Meters {
		if !rating.ValidMeter(rating.Meter(meter.Meter)) {
			return fmt.Errorf("invalid_meter:%s", meter.Meter)
		}
		if math.IsNaN(meter.Quantity) || math.IsInf(meter.Quantity, 0) || meter.Quantity < 0 {
			return fmt.Errorf("invalid_quantity:%s", meter.Meter)
		}
		definition, err := purserdb.New(jm.db).GetActiveMeterDefinition(ctx, meter.Meter)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("unknown_meter:%s", meter.Meter)
			}
			return fmt.Errorf("load_meter_definition:%w", err)
		}
		if meter.Unit != definition.Unit {
			return fmt.Errorf("unit_mismatch:%s", meter.Meter)
		}
		allowedSet := make(map[string]struct{}, len(definition.AllowedDimensions))
		for _, key := range definition.AllowedDimensions {
			allowedSet[key] = struct{}{}
		}
		for key, value := range meter.Dimensions {
			if _, ok := allowedSet[key]; !ok {
				return fmt.Errorf("dimension_not_allowed:%s:%s", meter.Meter, key)
			}
			if _, ok := value.(string); !ok {
				return fmt.Errorf("dimension_not_string:%s:%s", meter.Meter, key)
			}
		}
	}
	return nil
}

func loadSubscriptionPeriod(ctx context.Context, db *sql.DB, tenantID string, now time.Time) (time.Time, time.Time, error) {
	period, err := purserdb.New(db).GetActiveSubscriptionPeriod(ctx, tenantID)
	if err == nil && period.MollieNextPaymentDate.Valid {
		mollieNext := period.MollieNextPaymentDate.Time
		periodEnd := time.Date(mollieNext.Year(), mollieNext.Month(), mollieNext.Day(), 0, 0, 0, 0, time.UTC)
		periodStart := periodEnd.AddDate(0, -1, 0)
		return periodStart, periodEnd, nil
	}
	if err == nil && period.BillingPeriodStart.Valid && period.BillingPeriodEnd.Valid && period.BillingPeriodEnd.Time.After(period.BillingPeriodStart.Time) {
		return period.BillingPeriodStart.Time, period.BillingPeriodEnd.Time, nil
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
	TerminateTenantStreams(ctx context.Context, tenantID, reason string) (*foghorncontrolpb.TerminateTenantStreamsResponse, error)
	InvalidateTenantCache(ctx context.Context, tenantID, reason string) (*foghorncontrolpb.InvalidateTenantCacheResponse, error)
	GetTenantUserCount(ctx context.Context, tenantID string) (*commodorepb.GetTenantUserCountResponse, error)
	GetTenantPrimaryUser(ctx context.Context, tenantID string) (*commodorepb.GetTenantPrimaryUserResponse, error)
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
	billing           *Service
}

// TierReconciler is the subset of tieraccess.Reconciler used by the downgrade
// applier and the deployment-tier sweep. Defined as an interface so
// JobManager tests can stub it without pulling in the Quartermaster client.
type TierReconciler interface {
	Reconcile(ctx context.Context, tenantID string, tierLevel int32, tierName string) ([]string, string, error)
	SweepDeploymentTiers(ctx context.Context) (int, error)
}

// NewJobManager creates a new job manager
func NewJobManager(database *sql.DB, log logging.Logger, commodoreClient CommodoreClient, decklogSvc *decklog.BatchedClient, periscopeSvc *periscope.GRPCClient, tierReconciler TierReconciler, billing *Service) *JobManager {
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
	var purserMetrics *PurserMetrics
	if billing != nil {
		purserMetrics = billing.metrics
	}

	return &JobManager{
		db:                database,
		logger:            log,
		emailService:      emailSvc,
		cryptoMonitor:     NewCryptoMonitorWithMetrics(database, log, decklogSvc, purserMetrics),
		gasWalletMonitor:  NewGasWalletMonitor(log),
		x402Reconciler:    NewX402Reconciler(database, log, includeTestnets, x402Submitter),
		kafkaConsumer:     consumer,
		stopCh:            make(chan struct{}),
		billingTopic:      billingTopic,
		commodoreClient:   commodoreClient,
		periscopeClient:   periscopeSvc,
		thresholdEnforcer: NewThresholdEnforcer(database, log, commodoreClient, emailSvc, billing),
		tierReconciler:    tierReconciler,
		billing:           billing,
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

	// Deliver customer invoice notifications from the durable finalization outbox.
	go jm.runInvoiceEmailOutbox(ctx)

	// Reconcile verified provider callbacks after their durable ingress write.
	go jm.runProviderWebhookInbox(ctx)

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

	// Start deployment-tier sweep (Purser is the authority for
	// quartermaster.tenants.deployment_tier).
	go jm.runDeploymentTierSweep(ctx)
}

// runDeploymentTierSweep converges quartermaster.tenants.deployment_tier with
// each tenant's effective billing tier. One early run shortly after startup
// (so a deploy converges stale stamps without waiting an hour), then hourly.
// The in-band stamp in tieraccess.Reconciler is the primary path; this loop
// absorbs crashes between a tier flip and its stamp, QM outages, and rows
// that predate Purser owning the column.
func (jm *JobManager) runDeploymentTierSweep(ctx context.Context) {
	if jm.tierReconciler == nil {
		jm.logger.Info("deployment-tier sweep disabled: no tier reconciler configured")
		return
	}
	startupDelay := time.NewTimer(1 * time.Minute)
	defer startupDelay.Stop()
	select {
	case <-ctx.Done():
		return
	case <-jm.stopCh:
		return
	case <-startupDelay.C:
	}
	jm.deploymentTierSweepTick(ctx)

	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-jm.stopCh:
			return
		case <-ticker.C:
			jm.deploymentTierSweepTick(ctx)
		}
	}
}

func (jm *JobManager) deploymentTierSweepTick(ctx context.Context) {
	repaired, err := jm.tierReconciler.SweepDeploymentTiers(ctx)
	if err != nil {
		jm.logger.WithError(err).Warn("deployment-tier sweep failed")
		return
	}
	if repaired > 0 {
		jm.logger.WithField("repaired", repaired).Info("deployment-tier sweep stamped tenants")
	}
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
	invoiceIDs, err := purserdb.New(jm.db).ListMollieObservationDrainInvoiceIDs(ctx)
	if err != nil {
		return fmt.Errorf("list invoices for Mollie observation drain: %w", err)
	}
	for _, invoiceID := range invoiceIDs {
		if err := jm.billing.drainMolliePaymentObservationsForInvoice(ctx, invoiceID); err != nil {
			jm.logger.WithError(err).WithField("invoice_id", invoiceID).Warn("Failed to drain Mollie observations for invoice")
		}
	}
	return nil
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
		}).Error("Failed to unmarshal usage summary from Kafka")
		if quarantineErr := jm.quarantineUsageReport(ctx, msg, nil, "invalid_json"); quarantineErr != nil {
			return fmt.Errorf("quarantine malformed usage report: %w", quarantineErr)
		}
		return nil
	}
	if err := validateUsageSummaryEnvelope(summary); err != nil {
		if quarantineErr := jm.quarantineUsageReport(ctx, msg, &summary, err.Error()); quarantineErr != nil {
			return fmt.Errorf("quarantine invalid usage report: %w", quarantineErr)
		}
		return nil
	}
	if err := jm.validateUsageSummaryMeters(ctx, summary); err != nil {
		if quarantineErr := jm.quarantineUsageReport(ctx, msg, &summary, err.Error()); quarantineErr != nil {
			return fmt.Errorf("quarantine invalid usage meters: %w", quarantineErr)
		}
		return nil
	}
	alreadyProcessed, err := purserdb.New(jm.db).UsageReportExists(ctx, summary.ReportID)
	if err != nil {
		return fmt.Errorf("check usage report receipt: %w", err)
	}
	if alreadyProcessed {
		return nil
	}

	if summary.ReportKind == "window_complete" {
		return jm.processWindowCompletion(ctx, summary)
	}
	if summary.ReportKind == "reservation" {
		return jm.processUsageReservation(ctx, summary)
	}

	acceptedUsage, err := jm.processUsageSummary(ctx, summary, "kafka")
	if err != nil {
		jm.logger.WithError(err).WithFields(logging.Fields{
			"tenant_id": summary.TenantID,
			"report_id": summary.ReportID,
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
		if prepaidErr := jm.processPrepaidUsage(ctx, summary, acceptedUsage); prepaidErr != nil {
			jm.logger.WithError(prepaidErr).WithField("tenant_id", summary.TenantID).Error("Failed to process prepaid usage")
			return fmt.Errorf("prepaid deduction failed: %w", prepaidErr)
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
		"report_id":     summary.ReportID,
		"billing_model": billingModel,
	}).Debug("Processed usage summary from Kafka")

	periodStart, periodEnd, _, err := parseUsageSummaryPeriod(summary)
	if err != nil {
		return err
	}
	err = purserdb.New(jm.db).InsertUsageReportReceipt(ctx, purserdb.InsertUsageReportReceiptParams{
		ReportID: summary.ReportID, ReportKind: summary.ReportKind, SourceID: summary.SourceID,
		SourceRegion: summary.SourceRegion, Sequence: int64(summary.Sequence), TenantID: summary.TenantID,
		ClusterID: summary.ClusterID, PeriodStart: periodStart, PeriodEnd: periodEnd, Complete: summary.Complete,
	})
	if err != nil {
		return fmt.Errorf("record usage report receipt: %w", err)
	}
	return nil
}

func (jm *JobManager) processWindowCompletion(ctx context.Context, summary models.UsageSummary) error {
	tx, err := jm.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin window completion: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck
	periodStart, periodEnd, _, err := parseUsageSummaryPeriod(summary)
	if err != nil {
		return err
	}
	queries := purserdb.New(tx)
	if err := queries.UpsertMeteringSource(ctx, purserdb.UpsertMeteringSourceParams{
		SourceID: summary.SourceID, Region: summary.SourceRegion, ActiveFrom: periodStart,
	}); err != nil {
		return fmt.Errorf("register metering source: %w", err)
	}
	if err := queries.InsertUsageReportReceipt(ctx, purserdb.InsertUsageReportReceiptParams{
		ReportID: summary.ReportID, ReportKind: summary.ReportKind, SourceID: summary.SourceID,
		SourceRegion: summary.SourceRegion, Sequence: int64(summary.Sequence), TenantID: summary.TenantID,
		ClusterID: summary.ClusterID, PeriodStart: periodStart, PeriodEnd: periodEnd, Complete: true,
	}); err != nil {
		return fmt.Errorf("record window completion receipt: %w", err)
	}
	if err := queries.UpsertCompletedMeteringWindow(ctx, purserdb.UpsertCompletedMeteringWindowParams{
		SourceID: summary.SourceID, PeriodStart: periodStart, PeriodEnd: periodEnd,
	}); err != nil {
		return fmt.Errorf("record metering window: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit window completion: %w", err)
	}
	return nil
}

func (jm *JobManager) processUsageReservation(ctx context.Context, summary models.UsageSummary) error {
	usagePeriodStart, _, _, err := parseUsageSummaryPeriod(summary)
	if err != nil {
		return err
	}
	tier, err := billingpkg.LoadEffectiveTier(ctx, jm.db, summary.TenantID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load reservation pricing: %w", err)
	}
	currency := tier.Currency
	if currency == "" {
		currency = billing.DefaultCurrency()
	}
	rules := tier.Rules
	if summary.ClusterID != "" && jm.pricingResolver() != nil {
		resolved, resolveErr := pricing.ResolveClusterPricing(ctx, pricing.ResolveInputs{
			DB: jm.db, QM: jm.pricingResolver(), ConsumingTenantID: summary.TenantID,
			ClusterID: summary.ClusterID, AsOf: summary.PeriodStart,
			TierRules: tier.Rules, TierCurrency: tier.Currency,
		})
		if resolveErr != nil {
			return fmt.Errorf("resolve reservation pricing: %w", resolveErr)
		}
		rules = resolved.MeteredRules
		if resolved.Currency != "" {
			currency = resolved.Currency
		}
	}
	billingPeriodStart, billingPeriodEnd, err := jm.prepaidBillingPeriod(ctx, summary.TenantID, usagePeriodStart)
	if err != nil {
		return err
	}
	perCluster, err := jm.collectInvoiceDimensionedUsage(ctx, summary.TenantID, billingPeriodStart, billingPeriodEnd)
	if err != nil {
		return fmt.Errorf("collect finalized usage for reservation: %w", err)
	}
	active := make([]rating.DimensionedQuantity, 0, len(summary.Meters))
	for _, meter := range summary.Meters {
		dimensions := make(map[string]string, len(meter.Dimensions))
		for key, raw := range meter.Dimensions {
			if value, ok := raw.(string); ok {
				dimensions[key] = value
			}
		}
		active = append(active, rating.DimensionedQuantity{
			Meter: rating.Meter(meter.Meter), Unit: meter.Unit,
			Dimensions: dimensions, Quantity: decimal.NewFromFloat(meter.Quantity),
		})
	}
	finalized := perCluster[summary.ClusterID]
	finalizedAmount, err := ratePrepaidQuantities(currency, rules, finalized, billingPeriodStart, billingPeriodEnd)
	if err != nil {
		return fmt.Errorf("rate finalized usage before reservation: %w", err)
	}
	combined := append(append([]rating.DimensionedQuantity{}, finalized...), active...)
	combinedAmount, err := ratePrepaidQuantities(currency, rules, combined, billingPeriodStart, billingPeriodEnd)
	if err != nil {
		return fmt.Errorf("rate usage reservation: %w", err)
	}
	reservationAmount := combinedAmount.Sub(finalizedAmount)
	if reservationAmount.IsNegative() {
		reservationAmount = decimal.Zero
	}
	reservedMicro := reservationAmount.Mul(decimal.NewFromInt(1_000_000)).Round(0).IntPart()
	metersJSON, err := json.Marshal(summary.Meters)
	if err != nil {
		return fmt.Errorf("marshal reservation meters: %w", err)
	}
	tx, err := jm.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin reservation transaction: %w", err)
	}
	defer func() {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			jm.logger.WithError(rollbackErr).Warn("Failed to roll back usage reservation transaction")
		}
	}()
	_, usagePeriodEnd, _, err := parseUsageSummaryPeriod(summary)
	if err != nil {
		return err
	}
	queries := purserdb.New(tx)
	if err = queries.InsertUsageReportReceipt(ctx, purserdb.InsertUsageReportReceiptParams{
		ReportID: summary.ReportID, ReportKind: "reservation", SourceID: summary.SourceID,
		SourceRegion: summary.SourceRegion, Sequence: int64(summary.Sequence), TenantID: summary.TenantID,
		ClusterID: summary.ClusterID, PeriodStart: usagePeriodStart, PeriodEnd: usagePeriodEnd, Complete: summary.Complete,
	}); err != nil {
		return fmt.Errorf("record reservation report: %w", err)
	}
	if err = queries.UpsertUsageReservation(ctx, purserdb.UpsertUsageReservationParams{
		TenantID: summary.TenantID, SourceID: summary.SourceID, ClusterID: summary.ClusterID,
		Sequence: int64(summary.Sequence), ReportID: summary.ReportID,
		PeriodStart: usagePeriodStart, PeriodEnd: usagePeriodEnd, Meters: metersJSON,
		ReservedAmountMicro: reservedMicro, Currency: currency,
	}); err != nil {
		return fmt.Errorf("upsert usage reservation: %w", err)
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit usage reservation: %w", err)
	}
	return nil
}

func ratePrepaidQuantities(currency string, rules []rating.Rule, quantities []rating.DimensionedQuantity, periodStart, periodEnd time.Time) (decimal.Decimal, error) {
	usage := make(map[rating.Meter]decimal.Decimal)
	for _, quantity := range quantities {
		usage[quantity.Meter] = usage[quantity.Meter].Add(quantity.Quantity)
	}
	result, err := rating.Rate(rating.Input{
		Currency: currency, BasePrice: decimal.Zero, Rules: rules,
		Usage: usage, Quantities: quantities,
		PeriodStart: periodStart, PeriodEnd: periodEnd,
		WaiveUsageCharges: config.WaiveUsageChargesEnabled(),
	})
	if err != nil {
		return decimal.Zero, err
	}
	return result.UsageAmount, nil
}

// getTenantBillingModel returns the billing model for a tenant (prepaid or postpaid)
func (jm *JobManager) getTenantBillingModel(ctx context.Context, tenantID string) (string, error) {
	billingModel, err := purserdb.New(jm.db).GetActiveTenantBillingModelForJobs(ctx, tenantID)
	if errors.Is(err, sql.ErrNoRows) {
		return "postpaid", nil // Default for tenants without subscription
	}
	return billingModel, err
}

// buildUsageDataFromSummary is used by aggregate rating paths that do not need
// per-dimension rows. Financial persistence iterates summary.Meters directly.
func buildUsageDataFromSummary(summary models.UsageSummary) map[string]float64 {
	data := make(map[string]float64, len(summary.Meters))
	for _, meter := range summary.Meters {
		data[meter.Meter] += meter.Quantity
	}
	return data
}

func usageSummaryReferenceID(summary models.UsageSummary) uuid.UUID {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(summary.ReportID))
}

// processPrepaidUsage rates the tenant's cumulative billing-period usage and
// deducts only the marginal change from prepaid balance. Applying included
// allowances to each five-minute report would renew the allowance every five
// minutes and structurally undercharge. Base subscription fees are never part
// of this path.
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

	currency := tier.Currency
	if currency == "" {
		currency = billing.DefaultCurrency()
	}
	if currency != billing.DefaultCurrency() {
		return fmt.Errorf("prepaid balance currency %s cannot settle usage priced in %s", billing.DefaultCurrency(), currency)
	}
	if len(acceptedUsage) == 0 {
		return nil
	}
	alreadySettled, queryErr := purserdb.New(jm.db).PrepaidUsageSettlementExists(ctx, summary.ReportID)
	if queryErr != nil {
		return fmt.Errorf("check prepaid usage settlement: %w", queryErr)
	}
	if alreadySettled {
		return nil
	}
	billingPeriodStart, billingPeriodEnd, err := jm.prepaidBillingPeriod(ctx, summary.TenantID, periodStart)
	if err != nil {
		return err
	}
	perCluster, err := jm.collectInvoiceDimensionedUsage(ctx, summary.TenantID, billingPeriodStart, billingPeriodEnd)
	if err != nil {
		return fmt.Errorf("collect cumulative prepaid usage: %w", err)
	}
	desiredAmount, err := jm.rateCumulativePrepaidUsage(ctx, summary.TenantID, billingPeriodStart, billingPeriodEnd, tier, perCluster)
	if err != nil {
		return err
	}

	microPerUnit := decimal.NewFromInt(1_000_000)
	desiredCumulativeMicro := desiredAmount.Mul(microPerUnit).Round(0).IntPart()
	previouslySettledMicro, queryErr := purserdb.New(jm.db).SumPrepaidUsageSettlements(ctx, purserdb.SumPrepaidUsageSettlementsParams{
		TenantID:           summary.TenantID,
		BillingPeriodStart: billingPeriodStart,
		BillingPeriodEnd:   billingPeriodEnd,
	})
	if queryErr != nil {
		return fmt.Errorf("sum prior prepaid usage settlements: %w", queryErr)
	}
	marginalMicro := desiredCumulativeMicro - previouslySettledMicro

	referenceID := usageSummaryReferenceID(summary)
	periodLabel := summary.PeriodStart.UTC().Format(time.RFC3339) + "/" + summary.PeriodEnd.UTC().Format(time.RFC3339)
	previousBalance, newBalanceCents, applied := int64(0), int64(0), false
	if marginalMicro != 0 {
		previousBalance, newBalanceCents, applied, err = jm.deductPrepaidBalanceForUsageMicro(ctx, summary.TenantID, marginalMicro, "Usage: "+periodLabel, referenceID)
		if err != nil {
			return fmt.Errorf("failed to apply prepaid usage amount: %w", err)
		}
	}
	if err := purserdb.New(jm.db).InsertPrepaidUsageSettlement(ctx, purserdb.InsertPrepaidUsageSettlementParams{
		ReportID:              summary.ReportID,
		TenantID:              summary.TenantID,
		BillingPeriodStart:    billingPeriodStart,
		BillingPeriodEnd:      billingPeriodEnd,
		AmountMicro:           marginalMicro,
		CumulativeAmountMicro: desiredCumulativeMicro,
		Currency:              currency,
	}); err != nil {
		return fmt.Errorf("record prepaid usage settlement: %w", err)
	}

	jm.logger.WithFields(logging.Fields{
		"tenant_id":               summary.TenantID,
		"period":                  periodLabel,
		"marginal_micro":          marginalMicro,
		"cumulative_amount_micro": desiredCumulativeMicro,
	}).Info("Settled cumulative prepaid usage")

	if applied && jm.thresholdEnforcer != nil {
		if err := jm.thresholdEnforcer.EnforcePrepaidThresholds(ctx, summary.TenantID, previousBalance, newBalanceCents); err != nil {
			jm.logger.WithError(err).WithField("tenant_id", summary.TenantID).Warn("Failed to enforce prepaid thresholds")
		}
	}

	return nil
}

func (jm *JobManager) prepaidBillingPeriod(ctx context.Context, tenantID string, at time.Time) (time.Time, time.Time, error) {
	period, err := purserdb.New(jm.db).GetActiveSubscriptionPeriod(ctx, tenantID)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("load prepaid billing period: %w", err)
	}
	if period.BillingPeriodStart.Valid && period.BillingPeriodEnd.Valid && period.BillingPeriodEnd.Time.After(period.BillingPeriodStart.Time) {
		return period.BillingPeriodStart.Time.UTC(), period.BillingPeriodEnd.Time.UTC(), nil
	}
	fallbackStart := time.Date(at.Year(), at.Month(), 1, 0, 0, 0, 0, time.UTC)
	return fallbackStart, fallbackStart.AddDate(0, 1, 0), nil
}

func (jm *JobManager) rateCumulativePrepaidUsage(
	ctx context.Context,
	tenantID string,
	periodStart, periodEnd time.Time,
	tier *billingpkg.EffectiveTier,
	perCluster map[string][]rating.DimensionedQuantity,
) (decimal.Decimal, error) {
	total := decimal.Zero
	for clusterID, quantities := range perCluster {
		rules := tier.Rules
		currency := tier.Currency
		if clusterID != "" {
			resolver := jm.pricingResolver()
			if resolver == nil {
				return decimal.Zero, fmt.Errorf("prepaid usage on cluster %s requires pricing resolver", clusterID)
			}
			resolved, err := pricing.ResolveClusterPricing(ctx, pricing.ResolveInputs{
				DB: jm.db, QM: resolver, ConsumingTenantID: tenantID,
				ClusterID: clusterID, AsOf: periodStart,
				TierRules: tier.Rules, TierCurrency: tier.Currency,
			})
			if err != nil {
				return decimal.Zero, fmt.Errorf("resolve prepaid pricing for cluster %s: %w", clusterID, err)
			}
			rules = resolved.MeteredRules
			if resolved.Currency != "" {
				currency = resolved.Currency
			}
		}
		if currency != billing.DefaultCurrency() {
			return decimal.Zero, fmt.Errorf("prepaid usage on cluster %s prices in %s but prepaid balance currency is %s", clusterID, currency, billing.DefaultCurrency())
		}
		amount, err := ratePrepaidQuantities(currency, rules, quantities, periodStart, periodEnd)
		if err != nil {
			return decimal.Zero, fmt.Errorf("rate cumulative prepaid usage for cluster %s: %w", clusterID, err)
		}
		total = total.Add(amount)
	}
	return total, nil
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
	queries := purserdb.New(tx)

	if insertErr := queries.EnsurePrepaidBalance(ctx, purserdb.EnsurePrepaidBalanceParams{
		TenantID: tenantID,
		Currency: currency,
	}); insertErr != nil {
		return 0, 0, false, insertErr
	}

	currentBalance, scanErr := queries.LockPrepaidBalanceCents(ctx, purserdb.LockPrepaidBalanceCentsParams{
		TenantID: tenantID,
		Currency: currency,
	})
	if scanErr != nil {
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
	referenceIDParam := sql.NullString{}
	if referenceID != nil {
		referenceIDParam = sql.NullString{String: *referenceID, Valid: true}
	}
	if txErr := queries.InsertInvoiceCreditBalanceTransaction(ctx, purserdb.InsertInvoiceCreditBalanceTransactionParams{
		TenantID:          tenantID,
		AmountCents:       -applied,
		BalanceAfterCents: currentBalance - applied,
		Description:       sql.NullString{String: description, Valid: true},
		ReferenceID:       referenceIDParam,
		ReferenceType:     sql.NullString{String: referenceType, Valid: true},
	}); txErr != nil {
		if database.SQLState(txErr) == "23505" {
			// Duplicate ledger row exists. Read its amount so the caller can
			// preserve prepaid_credit_applied. Balance is untouched.
			if referenceID == nil {
				return 0, 0, false, txErr
			}
			historicAmount, probeErr := queries.GetBalanceTransactionAmountByReference(ctx, purserdb.GetBalanceTransactionAmountByReferenceParams{
				TenantID:      tenantID,
				ReferenceType: sql.NullString{String: referenceType, Valid: true},
				ReferenceID:   *referenceID,
			})
			if probeErr != nil {
				return 0, 0, false, probeErr
			}
			return currentBalance, -historicAmount, true, nil
		}
		return 0, 0, false, txErr
	}

	newBalance = currentBalance - applied
	if updErr := queries.UpdatePrepaidBalance(ctx, purserdb.UpdatePrepaidBalanceParams{
		BalanceCents: newBalance,
		TenantID:     tenantID,
		Currency:     currency,
	}); updErr != nil {
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
	return purserdb.New(tx).SumAppliedInvoiceCredit(ctx, purserdb.SumAppliedInvoiceCreditParams{
		TenantID:    tenantID,
		Description: sql.NullString{String: invoiceCreditDescription(periodStart), Valid: true},
	})
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
	queries := purserdb.New(tx)

	if insertErr := queries.EnsurePrepaidBalance(ctx, purserdb.EnsurePrepaidBalanceParams{
		TenantID: tenantID,
		Currency: currency,
	}); insertErr != nil {
		return 0, 0, false, insertErr
	}

	lockedBalance, scanErr := queries.LockPrepaidBalance(ctx, purserdb.LockPrepaidBalanceParams{
		TenantID: tenantID,
		Currency: currency,
	})
	if scanErr != nil {
		return 0, 0, false, scanErr
	}
	currentBalance := lockedBalance.BalanceCents
	currentRemainder := lockedBalance.BalanceRemainderMicro

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

	rowsAffected, err := queries.InsertUsageBalanceTransaction(ctx, purserdb.InsertUsageBalanceTransactionParams{
		TenantID:          tenantID,
		AmountCents:       -appliedCents,
		BalanceAfterCents: newBalance,
		Description:       sql.NullString{String: description, Valid: true},
		ReferenceID:       referenceID.String(),
	})
	if err != nil {
		return 0, 0, false, err
	}
	if rowsAffected == 0 {
		if commitErr := tx.Commit(); commitErr != nil {
			return 0, 0, false, commitErr
		}
		return currentBalance, currentBalance, false, nil
	}

	if updErr := queries.UpdatePrepaidBalanceWithRemainder(ctx, purserdb.UpdatePrepaidBalanceWithRemainderParams{
		BalanceCents:          newBalance,
		BalanceRemainderMicro: newRemainder,
		TenantID:              tenantID,
		Currency:              currency,
	}); updErr != nil {
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
	queries := purserdb.New(tx)

	err = queries.EnsurePrepaidBalance(ctx, purserdb.EnsurePrepaidBalanceParams{
		TenantID: tenantID,
		Currency: currency,
	})
	if err != nil {
		return 0, 0, false, err
	}

	currentBalance, err := queries.LockPrepaidBalanceCents(ctx, purserdb.LockPrepaidBalanceCentsParams{
		TenantID: tenantID,
		Currency: currency,
	})
	if err != nil {
		return 0, 0, false, err
	}

	newBalance = currentBalance - amountCents
	rowsAffected, err := queries.InsertUsageBalanceTransaction(ctx, purserdb.InsertUsageBalanceTransactionParams{
		TenantID:          tenantID,
		AmountCents:       -amountCents,
		BalanceAfterCents: newBalance,
		Description:       sql.NullString{String: description, Valid: true},
		ReferenceID:       referenceID.String(),
	})
	if err != nil {
		return 0, 0, false, err
	}
	if rowsAffected == 0 {
		if commitErr := tx.Commit(); commitErr != nil {
			return 0, 0, false, commitErr
		}
		return currentBalance, currentBalance, false, nil
	}

	err = queries.UpdatePrepaidBalance(ctx, purserdb.UpdatePrepaidBalanceParams{
		BalanceCents: newBalance,
		TenantID:     tenantID,
		Currency:     currency,
	})
	if err != nil {
		return 0, 0, false, err
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, false, err
	}

	return currentBalance, newBalance, true, nil
}

// getPrepaidBalance supports the retained idempotent invoice-credit recovery
// path, which is not invoked by the normal dimensioned usage flow.
//
//nolint:unused
func (jm *JobManager) getPrepaidBalance(ctx context.Context, tenantID string) (int64, error) {
	currency := billing.DefaultCurrency()
	balanceCents, err := purserdb.New(jm.db).GetPrepaidBalanceForJobs(ctx, purserdb.GetPrepaidBalanceForJobsParams{
		TenantID: tenantID,
		Currency: currency,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return balanceCents, nil
}

func (jm *JobManager) getBalanceTransactionByReference(ctx context.Context, tenantID, referenceType, referenceID string) (int64, bool, error) {
	amountCents, err := purserdb.New(jm.db).GetBalanceTransactionAmountByReference(ctx, purserdb.GetBalanceTransactionAmountByReferenceParams{
		TenantID:      tenantID,
		ReferenceType: sql.NullString{String: referenceType, Valid: true},
		ReferenceID:   referenceID,
	})
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
	rowsAffected, err := purserdb.New(jm.db).SuspendActiveTenantSubscription(ctx, tenantID)
	if err != nil {
		return err
	}

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
	jm.logger.Info("Starting invoice generation job")
	// The writer is idempotent by tenant + billing period, so reconcile once at
	// startup. This prevents restarts from postponing a due invoice forever.
	jm.generateMonthlyInvoices(ctx)
	timer := time.NewTimer(time.Until(nextUTCStart(0)))
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-jm.stopCh:
			return
		case <-timer.C:
			jm.generateMonthlyInvoices(ctx)
			timer.Reset(time.Until(nextUTCStart(0)))
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
	dueSubscriptions, err := purserdb.New(jm.db).ListSubscriptionsDueForInvoice(ctx, now)

	if err != nil {
		jm.logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to fetch tenant subscriptions for invoice generation")
		return
	}
	var invoicesGenerated int
	for _, subscription := range dueSubscriptions {
		tenantID := subscription.TenantID
		tierID := subscription.TierID.String()
		billingEmail := subscription.BillingEmail
		tierName := subscription.TierName
		displayName := subscription.DisplayName
		billingPeriodStart := subscription.BillingPeriodStart
		billingPeriodEnd := subscription.BillingPeriodEnd
		mollieNextPaymentDate := subscription.MollieNextPaymentDate
		stripeSubID := subscription.StripeSubscriptionID
		mollieSubID := subscription.MollieSubscriptionID
		paymentMethod := subscription.PaymentMethod

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
		if completenessErr := jm.assertMeteringComplete(ctx, tenantID, periodStart, periodEnd); completenessErr != nil {
			jm.logger.WithError(completenessErr).WithField("tenant_id", tenantID).Error("Metering incomplete; invoice finalization blocked")
			continue
		}

		// Check if a terminally-finalized invoice already exists for the
		// previous month. manual_review is NOT terminal — it's a hold that
		// must be re-runnable once ops fixes the underlying cluster
		// pricing, so we treat it like draft for finalization purposes.
		existingCount, err := purserdb.New(jm.db).CountFinalizedInvoicesForPeriod(ctx, purserdb.CountFinalizedInvoicesForPeriodParams{
			TenantID:    tenantID,
			PeriodStart: sql.NullTime{Time: periodStart, Valid: true},
		})
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
		draftInvoiceID, draftErr := purserdb.New(jm.db).GetDraftInvoiceIDForPeriod(ctx, purserdb.GetDraftInvoiceIDForPeriodParams{
			TenantID:    tenantID,
			PeriodStart: sql.NullTime{Time: periodStart, Valid: true},
		})
		switch {
		case draftErr == nil, errors.Is(draftErr, sql.ErrNoRows):
			// nil err means draft found; ErrNoRows means no draft, leave zero values.
		default:
			jm.logger.WithError(draftErr).WithField("tenant_id", tenantID).Error("Failed to look up existing draft invoice; skipping invoice for this period")
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
		perClusterDimensioned, dimensionErr := jm.collectInvoiceDimensionedUsage(ctx, tenantID, periodStart, periodEnd)
		if dimensionErr != nil {
			jm.logger.WithError(dimensionErr).WithField("tenant_id", tenantID).Error("Failed to collect dimensioned usage; skipping invoice for this period")
			continue
		}
		usageData := flattenUsageAcrossClusters(perClusterUsage)

		baseProviderManaged := stripeSubID.Valid || mollieSubID.Valid
		collectionProvider, providerErr := resolveInvoiceCollectionProvider(paymentMethod.String, stripeSubID.Valid, mollieSubID.Valid)
		if providerErr != nil {
			jm.logger.WithError(providerErr).WithField("tenant_id", tenantID).Error("Invoice finalization blocked by ambiguous collection provider")
			continue
		}
		ratingResult, ratingErr := jm.rateInvoiceForTenant(ctx, tenantID, periodStart, periodEnd, tier, true, baseProviderManaged, perClusterUsage, perClusterDimensioned)
		if ratingErr != nil {
			jm.logger.WithError(ratingErr).WithField("tenant_id", tenantID).Error("Failed to rate usage for invoice")
			continue
		}
		// Money stays in decimal.Decimal until the SQL boundary; NUMERIC
		// columns bind cleanly via $N::numeric. No float64 touches the cents.
		baseDec := ratingResult.BaseAmount
		meteredDec := ratingResult.UsageAmount
		// unwaivedMeteredDec is the would-have-cost usage total (display only;
		// equals meteredDec when usage is not waived). It must NEVER feed prepaid
		// credit, provider charges, the meter outbox, or operator credits.
		unwaivedMeteredDec := ratingResult.GrossUsageAmount
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
		if len(ratingResult.ManualReviewReasons) > 0 {
			status = "manual_review"
			jm.logger.WithFields(logging.Fields{
				"tenant_id": tenantID,
				"reasons":   strings.Join(ratingResult.ManualReviewReasons, "; "),
			}).Warn("Invoice routed to manual_review; finalization halted")
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
		var collectionDecision *invoiceCollectionDecision
		err = withTx(ctx, jm.db, func(tx *sql.Tx) error {
			queries := purserdb.New(tx)
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
				totalDec = totalDec.Round(2)
				if collectionProvider != "" {
					decision, decisionErr := applyInvoiceCollectionMinimumTx(
						ctx, tx, tenantID, collectionProvider, currency,
						totalDec.Mul(decimal.NewFromInt(100)).IntPart(),
					)
					if decisionErr != nil {
						return fmt.Errorf("apply invoice collection minimum: %w", decisionErr)
					}
					collectionDecision = &decision
					usageDetails["collection"] = map[string]interface{}{
						"provider":              decision.Provider,
						"minimum_cents":         decision.MinimumCents,
						"opening_balance_cents": decision.OpeningBalanceCents,
						"current_charge_cents":  decision.CurrentChargeCents,
						"collected_cents":       decision.CollectedCents,
						"closing_balance_cents": decision.ClosingBalanceCents,
						"outcome":               decision.Outcome,
					}
					usageJSON, txErr = json.Marshal(usageDetails)
					if txErr != nil {
						return fmt.Errorf("marshal invoice collection details: %w", txErr)
					}
					totalDec = decimal.NewFromInt(decision.CollectedCents).Div(decimal.NewFromInt(100))
				}
				status = finalizedInvoiceStatus(totalDec)
			} else {
				totalDec = totalDec.Round(2)
			}

			// Bind decimals as strings into NUMERIC columns so no float64 rounding
			// can sneak in at the SQL boundary.
			totalAmt := totalDec.Round(2).String()
			baseAmt := baseDec.Round(2).String()
			meteredAmt := meteredDec.Round(2).String()
			grossMeteredAmt := unwaivedMeteredDec.Round(2).String()
			creditAmt := creditDec.Round(2).String()

			if draftInvoiceID != "" {
				invoiceID, txErr = queries.UpdateDraftInvoice(ctx, purserdb.UpdateDraftInvoiceParams{
					Amount:               totalAmt,
					BaseAmount:           baseAmt,
					MeteredAmount:        meteredAmt,
					PrepaidCreditApplied: creditAmt,
					Currency:             currency,
					Status:               status,
					DueDate:              dueDate,
					UsageDetails:         json.RawMessage(usageJSON),
					PeriodStart:          sql.NullTime{Time: periodStart, Valid: true},
					PeriodEnd:            sql.NullTime{Time: periodEnd, Valid: true},
					GrossMeteredAmount:   grossMeteredAmt,
					InvoiceID:            draftInvoiceID,
					TenantID:             tenantID,
				})
				if txErr != nil {
					return fmt.Errorf("update invoice: %w", txErr)
				}
			} else {
				invoiceID, txErr = queries.UpsertInvoiceForPeriod(ctx, purserdb.UpsertInvoiceForPeriodParams{
					InvoiceID:            invoiceID,
					TenantID:             tenantID,
					Amount:               totalAmt,
					Currency:             currency,
					Status:               status,
					DueDate:              dueDate,
					BaseAmount:           baseAmt,
					MeteredAmount:        meteredAmt,
					PrepaidCreditApplied: creditAmt,
					UsageDetails:         json.RawMessage(usageJSON),
					PeriodStart:          sql.NullTime{Time: periodStart, Valid: true},
					PeriodEnd:            sql.NullTime{Time: periodEnd, Valid: true},
					GrossMeteredAmount:   grossMeteredAmt,
				})
				if txErr != nil {
					return fmt.Errorf("upsert invoice: %w", txErr)
				}
			}
			txErr = persistInvoiceLineItems(ctx, tx, invoiceID, tenantID, ratingResult)
			if txErr != nil {
				return txErr
			}
			if collectionDecision != nil {
				txErr = persistInvoiceCollectionDecisionTx(ctx, tx, invoiceID, tenantID, *collectionDecision)
				if txErr != nil {
					return txErr
				}
			}
			if status != "manual_review" {
				txErr = queries.MarkUsageAdjustmentsAppliedToInvoice(ctx, purserdb.MarkUsageAdjustmentsAppliedToInvoiceParams{
					InvoiceID:   invoiceID,
					TenantID:    tenantID,
					PeriodEnd:   periodEnd,
					PeriodStart: periodStart,
				})
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
			txErr = enqueueInvoiceEmailTx(ctx, tx, invoiceID, tenantID, billingEmail.String, status)
			if txErr != nil {
				return fmt.Errorf("enqueue invoice email: %w", txErr)
			}
			// manual_review: do not advance the subscription period.
			// Resolution flow is ops fixes pricing → re-finalize → side
			// effects fire once on the corrected total.
			if status == "manual_review" {
				return nil
			}
			rowsAffected, txErr := queries.AdvanceSubscriptionBillingPeriod(ctx, purserdb.AdvanceSubscriptionBillingPeriodParams{
				NextBillingDate:    sql.NullTime{Time: nextBillingDate, Valid: true},
				BillingPeriodStart: sql.NullTime{Time: nextPeriodStart, Valid: true},
				BillingPeriodEnd:   sql.NullTime{Time: nextPeriodEnd, Valid: true},
				TenantID:           tenantID,
			})
			if txErr != nil {
				return fmt.Errorf("advance subscription period: %w", txErr)
			}
			if rowsAffected == 0 {
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
			if drainErr := jm.billing.drainMolliePaymentObservationsForInvoice(ctx, invoiceID); drainErr != nil {
				jm.logger.WithError(drainErr).WithFields(logging.Fields{
					"tenant_id":  tenantID,
					"invoice_id": invoiceID,
				}).Warn("Failed to drain Mollie payment observations")
			}
		}

		// Overage collection. Provider subscriptions auto-charge the base;
		// Purser collects the remaining invoice amount after prepaid credit.
		// Route through exactly one selected provider. Stale IDs from a prior
		// provider switch cannot cause a second charge. Webhook reconciliation
		// uses the shared partial-payment-aware settlement path regardless of
		// provider.
		providerChargeDec := totalDec
		if status == "pending" && providerChargeDec.GreaterThan(decimal.Zero) {
			switch collectionProvider {
			case "mollie":
				if chargeErr := jm.chargeMollieOverage(ctx, tenantID, invoiceID, providerChargeDec, currency); chargeErr != nil {
					jm.logger.WithError(chargeErr).WithFields(logging.Fields{
						"tenant_id":  tenantID,
						"invoice_id": invoiceID,
					}).Warn("Failed to trigger Mollie overage charge")
				}
			case "stripe":
				if chargeErr := jm.chargeStripeOverage(ctx, tenantID, invoiceID, providerChargeDec, currency); chargeErr != nil {
					jm.logger.WithError(chargeErr).WithFields(logging.Fields{
						"tenant_id":  tenantID,
						"invoice_id": invoiceID,
					}).Warn("Failed to trigger Stripe off-session overage charge")
				}
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
	jm.logger.WithFields(logging.Fields{
		"invoices_generated": invoicesGenerated,
	}).Info("Monthly invoice generation completed")
}

func finalizedInvoiceStatus(total decimal.Decimal) string {
	if total.IsZero() {
		return "paid"
	}
	return "pending"
}

func (jm *JobManager) assertMeteringComplete(ctx context.Context, tenantID string, periodStart, periodEnd time.Time) error {
	queries := purserdb.New(jm.db)
	activeSources, err := queries.CountActiveMeteringSources(ctx, purserdb.CountActiveMeteringSourcesParams{
		PeriodStart: periodStart,
		PeriodEnd:   periodEnd,
	})
	if err != nil {
		return fmt.Errorf("count active metering sources: %w", err)
	}
	if activeSources == 0 {
		return errors.New("no active metering sources registered")
	}
	missingWindows, err := queries.CountMissingMeteringWindows(ctx, purserdb.CountMissingMeteringWindowsParams{
		PeriodStart: periodStart,
		PeriodEnd:   periodEnd,
	})
	if err != nil {
		return fmt.Errorf("check metering windows: %w", err)
	}
	if missingWindows > 0 {
		return fmt.Errorf("%d required metering windows are missing", missingWindows)
	}
	openAnomalies, err := queries.CountOpenMeteringAnomalies(ctx, purserdb.CountOpenMeteringAnomaliesParams{
		TenantID:  tenantID,
		PeriodEnd: periodEnd,
	})
	if err != nil {
		return fmt.Errorf("check metering anomalies: %w", err)
	}
	if openAnomalies > 0 {
		return fmt.Errorf("%d unresolved metering anomalies", openAnomalies)
	}
	return nil
}

func (jm *JobManager) applyDuePendingDowngrades(ctx context.Context, now time.Time) {
	tenantIDs, err := purserdb.New(jm.db).ListDuePendingDowngradeTenantIDs(ctx, sql.NullTime{Time: now, Valid: true})
	if err != nil {
		jm.logger.WithError(err).Warn("scan due pending tier downgrades")
		return
	}
	for _, tenantID := range tenantIDs {
		jm.applyPendingDowngrade(ctx, tenantID)
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
// key. Transport-level retries reuse that same key, so an ambiguous API
// response cannot create a second external charge. SCA-required outcomes are persisted as a customer-action
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
	queries := purserdb.New(jm.db)

	stripeDetails, err := queries.GetActiveStripeCollectionDetails(ctx, tenantID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("lookup stripe customer/subscription: %w", err)
	}
	if !stripeDetails.StripeCustomerID.Valid || stripeDetails.StripeCustomerID.String == "" {
		return nil
	}
	if jm.billing.stripeClient == nil {
		return fmt.Errorf("stripe client not configured for active Stripe subscription")
	}
	paymentMethodID, err := jm.billing.stripeClient.ResolveDefaultPaymentMethod(ctx, stripeDetails.StripeCustomerID.String, stripeDetails.StripeSubscriptionID.String)
	if err != nil {
		return fmt.Errorf("resolve Stripe payment method: %w", err)
	}
	if paymentMethodID == "" {
		jm.logger.WithFields(logging.Fields{
			"tenant_id":  tenantID,
			"invoice_id": invoiceID,
		}).Warn("Skipping automatic Stripe overage collection because no default payment method is configured")
		return nil
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

	existingPayment, insertErr := queries.UpsertPendingProviderBillingPayment(ctx, purserdb.UpsertPendingProviderBillingPaymentParams{
		PaymentID: paymentID,
		InvoiceID: invoiceID,
		Amount:    amountStr,
		Currency:  currency,
		TxID:      sql.NullString{String: intentPlaceholder, Valid: true},
	})
	if insertErr != nil {
		return fmt.Errorf("insert pending billing_payment: %w", insertErr)
	}
	if existingPayment.TxID != "" && existingPayment.TxID != intentPlaceholder {
		jm.logger.WithFields(logging.Fields{
			"tenant_id":  tenantID,
			"invoice_id": invoiceID,
			"payment_id": paymentID,
			"tx_id":      existingPayment.TxID,
			"status":     existingPayment.Status,
		}).Debug("Stripe overage payment already has provider id; skipping duplicate collection")
		return nil
	}

	// Payment-provider intent before the external call so a crash mid-API
	// leaves a trace operators can reconcile against the orphan Stripe
	// PaymentIntent if one was created.
	providerIntent, intentErr := queries.UpsertProviderPaymentIntent(ctx, purserdb.UpsertProviderPaymentIntentParams{
		TenantID:           tenantID,
		Provider:           "stripe",
		Purpose:            "stripe_overage_charge",
		InvoiceID:          invoiceID,
		ProviderCustomerID: sql.NullString{String: stripeDetails.StripeCustomerID.String, Valid: true},
		Currency:           currency,
		AmountCents:        amountCents,
		IdempotencyKey:     intentKey,
	})
	if intentErr != nil {
		return fmt.Errorf("insert payment_provider_intents: %w", intentErr)
	}
	providerIntentID := providerIntent.ID
	providerCallCount := int(providerIntent.AttemptCount)
	// Tie the billing_payments row to the canonical intent.
	if linkErr := queries.LinkBillingPaymentIntent(ctx, purserdb.LinkBillingPaymentIntentParams{
		IntentID:  providerIntentID,
		PaymentID: paymentID,
	}); linkErr != nil {
		jm.logger.WithError(linkErr).WithField("payment_id", paymentID).Warn("link billing_payment to intent")
	}

	// Per-attempt row keyed on the Stripe-side idempotency key so retries
	// collapse to one row at the provider too.
	if attemptErr := queries.InsertProviderBillingPaymentAttempt(ctx, purserdb.InsertProviderBillingPaymentAttemptParams{
		PaymentID:      paymentID,
		IntentID:       providerIntentID,
		AttemptNumber:  int32(attemptNumber),
		IdempotencyKey: intentKey,
		Provider:       "stripe",
	}); attemptErr != nil {
		return fmt.Errorf("insert billing_payment_attempt: %w", attemptErr)
	}
	if attemptErr := queries.PrepareProviderBillingPaymentAttemptRetry(ctx, purserdb.PrepareProviderBillingPaymentAttemptRetryParams{
		PaymentID:     paymentID,
		AttemptNumber: int32(attemptNumber),
	}); attemptErr != nil {
		return fmt.Errorf("prepare billing_payment_attempt retry: %w", attemptErr)
	}

	result, chargeErr := jm.billing.stripeClient.ChargeOffSession(ctx, billingstripe.OffSessionChargeParams{
		CustomerID:       stripeDetails.StripeCustomerID.String,
		PaymentMethodID:  paymentMethodID,
		TenantID:         tenantID,
		InvoiceID:        invoiceID,
		BillingPaymentID: paymentID,
		AmountCents:      amountCents,
		Currency:         currency,
		IdempotencyKey:   intentKey,
		Description:      fmt.Sprintf("Usage overage for invoice %s", invoiceID),
	})
	if chargeErr != nil {
		terminal := providerCallCount >= maxProviderPaymentAttempts
		intentStatus := "provider_call_failed"
		nextRetry := sql.NullTime{Time: time.Now().Add(1 * time.Hour), Valid: true}
		if terminal {
			intentStatus = "terminal_failed"
			nextRetry = sql.NullTime{}
		}
		if updateErr := queries.SetProviderPaymentIntentFailure(ctx, purserdb.SetProviderPaymentIntentFailureParams{
			Status:    intentStatus,
			LastError: sql.NullString{String: chargeErr.Error(), Valid: true},
			IntentID:  providerIntentID,
		}); updateErr != nil {
			jm.logger.WithError(updateErr).WithField("intent_id", providerIntentID).Warn("mark Stripe overage intent provider_call_failed")
		}
		if attemptUpdateErr := queries.SetProviderBillingPaymentAttemptFailure(ctx, purserdb.SetProviderBillingPaymentAttemptFailureParams{
			Status:         "provider_call_failed",
			FailureCode:    sql.NullString{String: "provider_call_error", Valid: true},
			FailureMessage: sql.NullString{String: chargeErr.Error(), Valid: true},
			NextRetryAt:    nextRetry,
			PaymentID:      paymentID,
			AttemptNumber:  int32(attemptNumber),
		}); attemptUpdateErr != nil {
			jm.logger.WithError(attemptUpdateErr).WithField("payment_id", paymentID).Warn("mark Stripe overage attempt provider_call_failed")
		}
		if terminal {
			if markErr := queries.MarkPendingBillingPaymentFailed(ctx, paymentID); markErr != nil {
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
		if updateErr := queries.AttachProviderPaymentIDToBillingPayment(ctx, purserdb.AttachProviderPaymentIDToBillingPaymentParams{
			ProviderPaymentID: sql.NullString{String: result.PaymentIntentID, Valid: true},
			PaymentID:         paymentID,
		}); updateErr != nil {
			return fmt.Errorf("attach Stripe payment_intent id: %w", updateErr)
		}
		if intentUpdateErr := queries.AttachProviderPaymentIDToIntent(ctx, purserdb.AttachProviderPaymentIDToIntentParams{
			ProviderPaymentID: sql.NullString{String: result.PaymentIntentID, Valid: true},
			IntentID:          providerIntentID,
		}); intentUpdateErr != nil {
			jm.logger.WithError(intentUpdateErr).WithField("intent_id", providerIntentID).Warn("link provider_payment_id on intent")
		}
		if attemptUpdateErr := queries.AttachProviderPaymentIDToAttempt(ctx, purserdb.AttachProviderPaymentIDToAttemptParams{
			ProviderPaymentID: sql.NullString{String: result.PaymentIntentID, Valid: true},
			PaymentID:         paymentID,
			AttemptNumber:     int32(attemptNumber),
		}); attemptUpdateErr != nil {
			jm.logger.WithError(attemptUpdateErr).WithField("payment_id", paymentID).Warn("link provider_payment_id on attempt")
		}
	}

	switch {
	case result.SCARequired:
		// SCA required: customer must reauthorize. Park the intent in
		// sca_required; the attempt row mirrors that state so the retry
		// job does not re-fire automatically.
		if updateErr := queries.SetProviderPaymentIntentFailure(ctx, purserdb.SetProviderPaymentIntentFailureParams{
			Status:    "sca_required",
			LastError: sql.NullString{String: result.FailureMessage, Valid: true},
			IntentID:  providerIntentID,
		}); updateErr != nil {
			jm.logger.WithError(updateErr).WithField("intent_id", providerIntentID).Warn("mark intent sca_required")
		}
		if attemptUpdateErr := queries.SetProviderBillingPaymentAttemptFailure(ctx, purserdb.SetProviderBillingPaymentAttemptFailureParams{
			Status:         "sca_required",
			FailureCode:    sql.NullString{String: result.FailureCode, Valid: true},
			FailureMessage: sql.NullString{String: result.FailureMessage, Valid: true},
			PaymentID:      paymentID,
			AttemptNumber:  int32(attemptNumber),
		}); attemptUpdateErr != nil {
			jm.logger.WithError(attemptUpdateErr).WithField("payment_id", paymentID).Warn("mark attempt sca_required")
		}
		if markErr := queries.MarkPendingBillingPaymentFailed(ctx, paymentID); markErr != nil {
			jm.logger.WithError(markErr).WithField("payment_id", paymentID).Warn("release Stripe overage reservation requiring SCA")
		}
		// Off-session SCA cannot be completed off-session, and the parked
		// PaymentIntent is not resumable by a payment-method change. The real
		// resume path is on-session: the overage invoice stays pending/overdue
		// and the customer pays it in the billing UI, where hosted Checkout
		// performs the authentication. Direct them there; dunning reminders also
		// cover the invoice if they do not act.
		actionURL := strings.TrimSpace(config.GetEnv("WEBAPP_PUBLIC_URL", ""))
		if actionURL != "" {
			actionURL = strings.TrimRight(actionURL, "/") + "/account/billing?invoice=" + url.QueryEscape(invoiceID)
		}
		jm.logger.WithFields(logging.Fields{
			"tenant_id":         tenantID,
			"invoice_id":        invoiceID,
			"payment_intent_id": result.PaymentIntentID,
			"action_url":        actionURL,
		}).Warn("Stripe off-session overage requires customer authentication (SCA); directing customer to on-session invoice payment")
		go jm.billing.sendTenantActionRequiredEmail(tenantID, invoiceID, float64(amountCents)/100, currency, actionURL)
		return nil

	case result.Status == "failed":
		// Hard failure (card_declined, expired_card, etc.) requires a new
		// customer action or operator decision rather than blind retry.
		if updateErr := queries.SetProviderPaymentIntentFailure(ctx, purserdb.SetProviderPaymentIntentFailureParams{
			Status:    "terminal_failed",
			LastError: sql.NullString{String: result.FailureCode + ": " + result.FailureMessage, Valid: true},
			IntentID:  providerIntentID,
		}); updateErr != nil {
			jm.logger.WithError(updateErr).WithField("intent_id", providerIntentID).Warn("mark intent terminal_failed")
		}
		if attemptUpdateErr := queries.SetProviderBillingPaymentAttemptFailure(ctx, purserdb.SetProviderBillingPaymentAttemptFailureParams{
			Status:         "failed",
			FailureCode:    sql.NullString{String: result.FailureCode, Valid: true},
			FailureMessage: sql.NullString{String: result.FailureMessage, Valid: true},
			PaymentID:      paymentID,
			AttemptNumber:  int32(attemptNumber),
		}); attemptUpdateErr != nil {
			jm.logger.WithError(attemptUpdateErr).WithField("payment_id", paymentID).Warn("mark attempt failed")
		}
		if markErr := queries.MarkPendingBillingPaymentFailed(ctx, paymentID); markErr != nil {
			jm.logger.WithError(markErr).WithField("payment_id", paymentID).Warn("mark stripe overage payment failed")
		}
		return fmt.Errorf("stripe off-session overage failed: %s: %s", result.FailureCode, result.FailureMessage)

	case result.Status == string(stripeStatusSucceeded):
		// Sync success: the webhook will still fire and route through
		// updateInvoicePaymentStatus to flip the invoice paid (and
		// account for partial payments). We do not mark confirmed here
		// — the webhook owns that transition under the partial-payment-
		// aware settlement.
		if updateErr := queries.SetProviderPaymentIntentStatus(ctx, purserdb.SetProviderPaymentIntentStatusParams{
			Status:   "provider_open",
			IntentID: providerIntentID,
		}); updateErr != nil {
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
		if updateErr := queries.SetProviderPaymentIntentStatus(ctx, purserdb.SetProviderPaymentIntentStatusParams{
			Status:   "provider_open",
			IntentID: providerIntentID,
		}); updateErr != nil {
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
	latest, err := purserdb.New(jm.db).GetLatestProviderPaymentAttempt(ctx, purserdb.GetLatestProviderPaymentAttemptParams{
		Provider:  provider,
		InvoiceID: invoiceID,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return 1, nil
	}
	if err != nil {
		return 0, fmt.Errorf("lookup latest %s payment attempt: %w", provider, err)
	}
	if latest.Status != "provider_call_failed" {
		return 0, nil
	}
	// A provider_call_failed result is ambiguous: the provider may have
	// accepted the request even though the response never reached us. Reuse
	// the same logical attempt and idempotency key. payment_provider_intents
	// tracks the bounded number of actual API calls separately.
	return int(latest.AttemptNumber), nil
}

// chargeMollieOverage triggers an on-demand recurring-sequence charge against
// the tenant's stored Mollie mandate for the metered (overage) portion of an
// invoice. The Mollie subscription auto-collects the base; only the overage
// needs explicit collection. A pending billing_payments row is inserted up
// front so updateInvoicePaymentStatus can flip it confirmed when the webhook
// arrives. Each provider call is recorded as a billing_payment_attempts row
// keyed by a deterministic idempotency_key which is reused by transport
// retries so an ambiguous response cannot double-charge,
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
	queries := purserdb.New(jm.db)

	mollieDetails, err := queries.GetActiveMollieCollectionDetails(ctx, tenantID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("lookup mollie customer/mandate: %w", err)
	}
	if mollieDetails.MollieMandateID == "" {
		// Mandate exists in some non-valid state; do not retry blindly.
		if mollieDetails.MandateStatus != "" && mollieDetails.MandateStatus != "valid" {
			jm.logger.WithFields(logging.Fields{
				"tenant_id":      tenantID,
				"invoice_id":     invoiceID,
				"mandate_status": mollieDetails.MandateStatus,
			}).Warn("Skipping Mollie overage: mandate not valid")
		}
		return nil
	}
	if jm.billing.mollieClient == nil {
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

	existingPayment, insertErr := queries.UpsertPendingProviderBillingPayment(ctx, purserdb.UpsertPendingProviderBillingPaymentParams{
		PaymentID: paymentID,
		InvoiceID: invoiceID,
		Amount:    amountStr,
		Currency:  currency,
		TxID:      sql.NullString{String: intentID, Valid: true},
	})
	if insertErr != nil {
		return fmt.Errorf("insert pending billing_payment: %w", insertErr)
	}
	if existingPayment.TxID != "" && existingPayment.TxID != intentID {
		jm.logger.WithFields(logging.Fields{
			"tenant_id":  tenantID,
			"invoice_id": invoiceID,
			"payment_id": paymentID,
			"tx_id":      existingPayment.TxID,
			"status":     existingPayment.Status,
		}).Debug("Mollie overage payment already has provider id; skipping duplicate collection")
		return nil
	}

	providerIntent, intentErr := queries.UpsertProviderPaymentIntent(ctx, purserdb.UpsertProviderPaymentIntentParams{
		TenantID:           tenantID,
		Provider:           "mollie",
		Purpose:            "mollie_overage_charge",
		InvoiceID:          invoiceID,
		ProviderCustomerID: sql.NullString{String: mollieDetails.MollieCustomerID, Valid: true},
		Currency:           currency,
		AmountCents:        amountCents,
		IdempotencyKey:     idemKey,
	})
	if intentErr != nil {
		return fmt.Errorf("insert Mollie payment_provider_intents: %w", intentErr)
	}
	providerIntentID := providerIntent.ID
	providerCallCount := int(providerIntent.AttemptCount)
	if linkErr := queries.LinkBillingPaymentIntent(ctx, purserdb.LinkBillingPaymentIntentParams{
		IntentID:  providerIntentID,
		PaymentID: paymentID,
	}); linkErr != nil {
		jm.logger.WithError(linkErr).WithField("payment_id", paymentID).Warn("link Mollie billing_payment to intent")
	}

	// Per-attempt audit row. The unique constraint on
	// (provider, idempotency_key) collapses retries to the same logical
	// charge attempt; status advances on provider response.
	if attemptErr := queries.InsertProviderBillingPaymentAttempt(ctx, purserdb.InsertProviderBillingPaymentAttemptParams{
		PaymentID:      paymentID,
		IntentID:       providerIntentID,
		AttemptNumber:  int32(attemptNumber),
		IdempotencyKey: idemKey,
		Provider:       "mollie",
	}); attemptErr != nil {
		return fmt.Errorf("insert billing_payment_attempt: %w", attemptErr)
	}
	if attemptErr := queries.PrepareProviderBillingPaymentAttemptRetry(ctx, purserdb.PrepareProviderBillingPaymentAttemptRetryParams{
		PaymentID:     paymentID,
		AttemptNumber: int32(attemptNumber),
	}); attemptErr != nil {
		return fmt.Errorf("prepare billing_payment_attempt retry: %w", attemptErr)
	}

	webhookURL := ""
	if base := config.GetGatewayPublicURL(); base != "" {
		webhookURL = base + "/webhooks/billing/mollie"
	}

	payment, err := jm.billing.mollieClient.ChargeOnMandate(ctx, billingmollie.OnDemandChargeParams{
		CustomerID:     mollieDetails.MollieCustomerID,
		MandateID:      mollieDetails.MollieMandateID,
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
		nextRetry := sql.NullTime{Time: time.Now().Add(1 * time.Hour), Valid: true}
		if mandateRevoked {
			attemptStatus = "expired"
			nextRetry = sql.NullTime{}
		} else if providerCallCount >= maxProviderPaymentAttempts {
			nextRetry = sql.NullTime{}
		}
		if attemptErr := queries.SetProviderBillingPaymentAttemptFailure(ctx, purserdb.SetProviderBillingPaymentAttemptFailureParams{
			Status:         attemptStatus,
			FailureCode:    sql.NullString{String: mollieFailureCode(err), Valid: true},
			FailureMessage: sql.NullString{String: err.Error(), Valid: true},
			NextRetryAt:    nextRetry,
			PaymentID:      paymentID,
			AttemptNumber:  int32(attemptNumber),
		}); attemptErr != nil {
			jm.logger.WithError(attemptErr).WithField("payment_id", paymentID).Warn("update billing_payment_attempt on failure")
		}
		if mandateRevoked || providerCallCount >= maxProviderPaymentAttempts {
			if markErr := queries.MarkPendingBillingPaymentFailed(ctx, paymentID); markErr != nil {
				jm.logger.WithError(markErr).WithField("payment_id", paymentID).Warn("mark Mollie overage payment failed")
			}
		}
		if mandateRevoked {
			// Mark all valid mandates for this tenant as revoked so the
			// next pass picks up the customer-action gate.
			if mandateErr := queries.RevokeValidMollieMandates(ctx, tenantID); mandateErr != nil {
				jm.logger.WithError(mandateErr).WithField("tenant_id", tenantID).Warn("mark mollie mandate revoked")
			}
		}
		intentStatus := "provider_call_failed"
		if mandateRevoked || providerCallCount >= maxProviderPaymentAttempts {
			intentStatus = "terminal_failed"
		}
		if intentErr := queries.SetProviderPaymentIntentFailure(ctx, purserdb.SetProviderPaymentIntentFailureParams{
			Status:    intentStatus,
			LastError: sql.NullString{String: err.Error(), Valid: true},
			IntentID:  providerIntentID,
		}); intentErr != nil {
			jm.logger.WithError(intentErr).WithField("intent_id", providerIntentID).Warn("mark Mollie overage intent failed")
		}
		return fmt.Errorf("mollie on-demand charge: %w", err)
	}

	if updateErr := queries.AttachProviderPaymentIDToBillingPayment(ctx, purserdb.AttachProviderPaymentIDToBillingPaymentParams{
		ProviderPaymentID: sql.NullString{String: payment.ID, Valid: true},
		PaymentID:         paymentID,
	}); updateErr != nil {
		return fmt.Errorf("attach Mollie payment id: %w", updateErr)
	}
	if intentUpdateErr := queries.AttachOpenProviderPaymentIDToIntent(ctx, purserdb.AttachOpenProviderPaymentIDToIntentParams{
		ProviderPaymentID: sql.NullString{String: payment.ID, Valid: true},
		IntentID:          providerIntentID,
	}); intentUpdateErr != nil {
		jm.logger.WithError(intentUpdateErr).WithField("intent_id", providerIntentID).Warn("link Mollie provider payment id on intent")
	}
	if attemptUpdateErr := queries.AttachProviderPaymentIDToAttempt(ctx, purserdb.AttachProviderPaymentIDToAttemptParams{
		ProviderPaymentID: sql.NullString{String: payment.ID, Valid: true},
		PaymentID:         paymentID,
		AttemptNumber:     int32(attemptNumber),
	}); attemptUpdateErr != nil {
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
	queries := purserdb.New(jm.db)
	pending, err := queries.GetPendingDowngrade(ctx, tenantID)
	if errors.Is(err, sql.ErrNoRows) {
		return
	}
	if err != nil {
		jm.logger.WithError(err).WithField("tenant_id", tenantID).Warn("load pending tier for downgrade applier")
		return
	}
	if pending.PendingTierID == "" {
		return
	}
	if !pending.PendingEffectiveAt.Valid || pending.PendingEffectiveAt.Time.After(time.Now()) {
		return
	}
	if !pending.TierLevel.Valid {
		jm.logger.WithFields(logging.Fields{
			"tenant_id":       tenantID,
			"pending_tier_id": pending.PendingTierID,
		}).Warn("pending tier id references missing billing_tiers row")
		return
	}
	if jm.tierReconciler == nil {
		jm.logger.WithField("tenant_id", tenantID).Warn("downgrade applier has no tier reconciler configured")
		return
	}

	stagedTarget := pending.PendingTierID
	targetLevel := pending.TierLevel.Int32
	targetName := pending.TierName.String

	// Step 1: flip tier_id in its own short transaction, but keep pending_*
	// set as the "reconcile-not-yet-applied" marker. Conditional on the
	// staged target so a racing ChangeBillingTier that re-pointed the
	// pending is not clobbered.
	rows, err := queries.ApplyPendingDowngradeTier(ctx, purserdb.ApplyPendingDowngradeTierParams{
		PendingTierID: stagedTarget,
		TenantID:      tenantID,
	})
	if err != nil {
		jm.logger.WithError(err).WithField("tenant_id", tenantID).Warn("flip tier_id for pending downgrade")
		return
	}
	if rows == 0 {
		// Race: pending_tier_id changed since we read it. Next tick handles
		// the new state.
		return
	}

	// Step 2: reconcile cluster access + invalidate Commodore cache. Idempotent.
	if _, _, err := jm.tierReconciler.Reconcile(ctx, tenantID, targetLevel, targetName); err != nil {
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
	if err := queries.ClearAppliedPendingDowngrade(ctx, purserdb.ClearAppliedPendingDowngradeParams{
		TenantID: tenantID,
		TierID:   stagedTarget,
	}); err != nil {
		jm.logger.WithError(err).WithField("tenant_id", tenantID).Warn("clear pending downgrade marker; will retry next tick")
		return
	}

	jm.logger.WithFields(logging.Fields{
		"tenant_id":  tenantID,
		"from_tier":  pending.TierID,
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
	retryTicker := time.NewTicker(time.Hour)
	reminderTimer := time.NewTimer(time.Until(nextUTCStart(9)))
	defer retryTicker.Stop()
	defer reminderTimer.Stop()

	jm.logger.Info("Starting payment retry job")
	// Reconcile due provider calls at startup. The provider idempotency key is
	// stable across retries, so this is safe after a crash or restart.
	jm.retryProviderPaymentAttempts(ctx)
	jm.sendPaymentReminders(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-jm.stopCh:
			return
		case <-retryTicker.C:
			jm.retryProviderPaymentAttempts(ctx)
		case <-reminderTimer.C:
			jm.sendPaymentReminders(ctx)
			reminderTimer.Reset(time.Until(nextUTCStart(9)))
		}
	}
}

func (jm *JobManager) retryProviderPaymentAttempts(ctx context.Context) {
	attempts, err := purserdb.New(jm.db).ListProviderPaymentAttemptsForRetry(ctx, maxProviderPaymentAttempts)
	if err != nil {
		jm.logger.WithError(err).Error("Failed to fetch provider payment attempts for retry")
		return
	}
	for _, attempt := range attempts {
		amount, parseErr := decimal.NewFromString(attempt.Amount)
		if parseErr != nil {
			jm.logger.WithError(parseErr).WithField("invoice_id", attempt.InvoiceID).Warn("Failed to parse provider retry amount")
			continue
		}
		var retryErr error
		switch attempt.Provider {
		case "stripe":
			retryErr = jm.chargeStripeOverage(ctx, attempt.TenantID, attempt.InvoiceID, amount, attempt.Currency)
		case "mollie":
			retryErr = jm.chargeMollieOverage(ctx, attempt.TenantID, attempt.InvoiceID, amount, attempt.Currency)
		default:
			jm.logger.WithField("provider", attempt.Provider).Warn("Unknown provider payment attempt provider")
			continue
		}
		if retryErr != nil {
			jm.logger.WithError(retryErr).WithFields(logging.Fields{
				"provider":   attempt.Provider,
				"tenant_id":  attempt.TenantID,
				"invoice_id": attempt.InvoiceID,
			}).Warn("Provider payment attempt retry failed")
		}
	}
}

// sendPaymentReminders marks invoices overdue and stages one durable reminder
// at 1, 7, 14, and 30 days past due. The outbox worker owns SMTP retries.
func (jm *JobManager) sendPaymentReminders(ctx context.Context) {
	queries := purserdb.New(jm.db)
	if err := queries.MarkPendingInvoicesOverdue(ctx); err != nil {
		jm.logger.WithError(err).Error("Failed to mark invoices overdue")
		return
	}

	count, err := queries.StageOverdueInvoiceReminders(ctx)
	if err != nil {
		jm.logger.WithError(err).Error("Failed to stage overdue invoice reminders")
		return
	}
	if count > 0 {
		jm.logger.WithField("reminder_count", count).Info("Staged overdue invoice reminders")
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
	rowsAffected, err := purserdb.New(jm.db).ExpireStaleCryptoWallets(ctx)

	if err != nil {
		jm.logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to cleanup expired wallets")
		return
	}

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
	periodStart := summary.PeriodStart.UTC()
	periodEnd := summary.PeriodEnd.UTC()
	if periodStart.IsZero() || periodEnd.IsZero() {
		return time.Time{}, time.Time{}, "", errors.New("usage report period is required")
	}
	if !periodEnd.After(periodStart) {
		return time.Time{}, time.Time{}, "", errors.New("usage report period must be positive")
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

	acceptedUsage := []canonicalUsageDelta{}

	for _, meter := range summary.Meters {
		usageType := meter.Meter
		usageValue := meter.Quantity
		if usageValue <= 0 {
			continue
		}
		dimensions := normalizedUsageDimensions(meter.Dimensions)
		dimensionJSON, marshalErr := json.Marshal(dimensions)
		if marshalErr != nil {
			return nil, fmt.Errorf("marshal dimensions for %s: %w", usageType, marshalErr)
		}
		dimensionHash := sha256.Sum256(dimensionJSON)
		dimensionKey := fmt.Sprintf("%x", dimensionHash[:])
		usageDetails := models.JSONB{
			"source":        source,
			"source_id":     summary.SourceID,
			"report_id":     summary.ReportID,
			"unit":          meter.Unit,
			"dimensions":    dimensions,
			"source_region": summary.SourceRegion,
		}
		usageDetailsJSON, marshalErr := json.Marshal(usageDetails)
		if marshalErr != nil {
			return nil, fmt.Errorf("marshal usage details for %s: %w", usageType, marshalErr)
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
			if qErr := purserdb.New(jm.db).InsertUsageRecordQuarantine(ctx, purserdb.InsertUsageRecordQuarantineParams{
				TenantID:       summary.TenantID,
				ClusterID:      summary.ClusterID,
				UsageType:      usageType,
				UsageValue:     usageValue,
				UsageDetails:   json.RawMessage(usageDetailsJSON),
				PeriodStart:    sql.NullTime{Time: periodStart, Valid: true},
				PeriodEnd:      sql.NullTime{Time: periodEnd, Valid: true},
				Granularity:    granularity,
				ValueKind:      sql.NullString{String: valueKind, Valid: true},
				RejectedReason: rejection,
				Source:         source,
				RawPayload:     json.RawMessage(usageDetailsJSON),
			}); qErr != nil {
				jm.logger.WithError(qErr).WithFields(logging.Fields{
					"tenant_id":  summary.TenantID,
					"usage_type": usageType,
				}).Warn("Failed to write usage_records_quarantine row")
			}
			if jm.billing.metrics != nil && jm.billing.metrics.UsageQuarantine != nil {
				jm.billing.metrics.UsageQuarantine.WithLabelValues(usageType, rejection).Inc()
			}
			continue
		}

		err = purserdb.New(jm.db).UpsertCanonicalUsageRecord(ctx, purserdb.UpsertCanonicalUsageRecordParams{
			TenantID:     summary.TenantID,
			ClusterID:    summary.ClusterID,
			UsageType:    usageType,
			Unit:         meter.Unit,
			Dimensions:   json.RawMessage(dimensionJSON),
			DimensionKey: dimensionKey,
			SourceID:     summary.SourceID,
			ReportID:     summary.ReportID,
			UsageValue:   usageValue,
			UsageDetails: json.RawMessage(usageDetailsJSON),
			PeriodStart:  sql.NullTime{Time: periodStart, Valid: true},
			PeriodEnd:    sql.NullTime{Time: periodEnd, Valid: true},
			Granularity:  granularity,
			ValueKind:    valueKind,
		})

		if err != nil {
			return nil, fmt.Errorf("failed to upsert %s: %w", usageType, err)
		}
		if jm.billing.metrics != nil && jm.billing.metrics.UsageRecords != nil {
			jm.billing.metrics.UsageRecords.WithLabelValues(usageType).Inc()
		}
		acceptedUsage = append(acceptedUsage, canonicalUsageDelta{
			clusterID:    summary.ClusterID,
			usageType:    usageType,
			unit:         meter.Unit,
			dimensions:   dimensions,
			usageValue:   usageValue,
			usageDetails: usageDetails,
		})
	}

	if persistErr := jm.persistProviderUsage(ctx, summary, periodStart, periodEnd, granularity, source); persistErr != nil {
		return nil, persistErr
	}
	acceptedAdjustments, err := jm.persistUsageAdjustments(ctx, summary, source)
	if err != nil {
		return nil, err
	}
	acceptedUsage = append(acceptedUsage, acceptedAdjustments...)

	return acceptedUsage, nil
}

func (jm *JobManager) persistProviderUsage(ctx context.Context, summary models.UsageSummary, periodStart, periodEnd time.Time, granularity, source string) error {
	if rejection := validateCanonicalUsageWindow(periodStart, periodEnd, granularity, "delta"); rejection != "" {
		return fmt.Errorf("reject storage provider usage for non-canonical period: %s", rejection)
	}
	for _, rec := range summary.ProviderUsage {
		if rec.Meter.Quantity <= 0 {
			continue
		}
		if !rating.ValidMeter(rating.Meter(rec.Meter.Meter)) {
			return fmt.Errorf("invalid provider usage_type %q", rec.Meter.Meter)
		}
		dimensions := normalizedUsageDimensions(rec.Meter.Dimensions)
		dimensionJSON, err := json.Marshal(dimensions)
		if err != nil {
			return fmt.Errorf("marshal provider dimensions: %w", err)
		}
		dimensionHash := sha256.Sum256(dimensionJSON)
		dimensionKey := fmt.Sprintf("%x", dimensionHash[:])
		details := models.JSONB{
			"source":              source,
			"source_id":           summary.SourceID,
			"report_id":           summary.ReportID,
			"provider_tenant_id":  rec.ProviderTenantID,
			"provider_cluster_id": rec.ProviderClusterID,
			"dimensions":          dimensions,
		}
		detailsJSON, marshalErr := json.Marshal(details)
		if marshalErr != nil {
			return fmt.Errorf("marshal provider usage details: %w", marshalErr)
		}
		err = purserdb.New(jm.db).UpsertProviderUsageRecord(ctx, purserdb.UpsertProviderUsageRecordParams{
			UsageTenantID:     summary.TenantID,
			WorkClusterID:     summary.ClusterID,
			ProviderTenantID:  rec.ProviderTenantID,
			ProviderClusterID: rec.ProviderClusterID,
			UsageType:         rec.Meter.Meter,
			Unit:              rec.Meter.Unit,
			UsageValue:        rec.Meter.Quantity,
			Dimensions:        json.RawMessage(dimensionJSON),
			DimensionKey:      dimensionKey,
			SourceID:          summary.SourceID,
			ReportID:          summary.ReportID,
			PeriodStart:       periodStart,
			PeriodEnd:         periodEnd,
			Source:            source,
			UsageDetails:      json.RawMessage(detailsJSON),
		})
		if err != nil {
			return fmt.Errorf("upsert provider usage %s/%s: %w", rec.ProviderTenantID, rec.Meter.Meter, err)
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
		if adj.Unit == "" {
			unit, unitErr := purserdb.New(jm.db).GetMeterUnitForAdjustment(ctx, adj.UsageType)
			if unitErr != nil {
				return nil, fmt.Errorf("resolve adjustment unit for %s: %w", adj.UsageType, unitErr)
			}
			adj.Unit = unit
		}
		adj.Dimensions = normalizedUsageDimensions(adj.Dimensions)
		dimensionJSON, err := json.Marshal(adj.Dimensions)
		if err != nil {
			return nil, fmt.Errorf("marshal adjustment dimensions: %w", err)
		}
		dimensionHash := sha256.Sum256(dimensionJSON)
		dimensionKey := fmt.Sprintf("%x", dimensionHash[:])
		adj.Details["source"] = source
		detailsJSON, marshalErr := json.Marshal(adj.Details)
		if marshalErr != nil {
			return nil, fmt.Errorf("marshal adjustment details: %w", marshalErr)
		}
		err = purserdb.New(jm.db).UpsertUsageAdjustment(ctx, purserdb.UpsertUsageAdjustmentParams{
			TenantID:     summary.TenantID,
			ClusterID:    adj.ClusterID,
			UsageType:    adj.UsageType,
			Unit:         adj.Unit,
			Dimensions:   json.RawMessage(dimensionJSON),
			DimensionKey: dimensionKey,
			DeltaValue:   adj.DeltaValue,
			PeriodStart:  adj.PeriodStart,
			PeriodEnd:    adj.PeriodEnd,
			SourceSystem: adj.SourceSystem,
			SourceID:     adj.SourceID,
			Reason:       sql.NullString{String: adj.Reason, Valid: adj.Reason != ""},
			Details:      json.RawMessage(detailsJSON),
		})
		if err != nil {
			return nil, fmt.Errorf("upsert usage adjustment %s: %w", adj.SourceID, err)
		}
		accepted = append(accepted, canonicalUsageDelta{
			clusterID:    adj.ClusterID,
			usageType:    adj.UsageType,
			unit:         adj.Unit,
			dimensions:   adj.Dimensions,
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
	finalizedCount, countErr := purserdb.New(jm.db).CountFinalizedInvoicesForPeriod(ctx, purserdb.CountFinalizedInvoicesForPeriodParams{
		TenantID:    tenantID,
		PeriodStart: sql.NullTime{Time: periodStart, Valid: true},
	})
	if countErr != nil {
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
	perClusterDimensioned, err := jm.collectInvoiceDimensionedUsage(ctx, tenantID, periodStart, periodEnd)
	if err != nil {
		return fmt.Errorf("collect dimensioned invoice usage: %w", err)
	}
	usageTotals := flattenUsageAcrossClusters(perClusterUsage)

	// Provider-managed base detection: external recurring subscription owns
	// the base fee. The draft mirrors that by emitting a $0 informational
	// included_subscription base line instead of duplicating the tier's base
	// price. A query failure aborts the draft so we never emit a wrong base
	// silently — the next Kafka redelivery retries.
	providerIDs, scanErr := purserdb.New(jm.db).GetSubscriptionProviderIDs(ctx, tenantID)
	if scanErr != nil && !errors.Is(scanErr, sql.ErrNoRows) {
		return fmt.Errorf("read provider sub ids for draft: %w", scanErr)
	}
	baseProviderManaged := providerIDs.StripeSubscriptionID.Valid || providerIDs.MollieSubscriptionID.Valid

	// Rate the period via the engine; one source of truth for invoice math.
	// Money stays as decimal.Decimal end-to-end and binds to NUMERIC columns
	// as decimal strings; float64 never touches the cents.
	ratingResult, err := jm.rateInvoiceForTenant(ctx, tenantID, periodStart, periodEnd, tier, true, baseProviderManaged, perClusterUsage, perClusterDimensioned)
	if err != nil {
		return fmt.Errorf("rate usage: %w", err)
	}
	baseDec := ratingResult.BaseAmount
	meteredDec := ratingResult.UsageAmount
	unwaivedMeteredDec := ratingResult.GrossUsageAmount
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
		queries := purserdb.New(tx)
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
		grossMeteredAmt := unwaivedMeteredDec.Round(2).String()
		creditAmt := prepaidCreditDec.Round(2).String()

		invoiceID, txErr = queries.UpsertInvoiceDraft(ctx, purserdb.UpsertInvoiceDraftParams{
			TenantID:             tenantID,
			Amount:               totalAmt,
			Currency:             currency,
			DueDate:              dueDate,
			BaseAmount:           baseAmt,
			MeteredAmount:        meteredAmt,
			PrepaidCreditApplied: creditAmt,
			UsageDetails:         json.RawMessage(usageJSON),
			PeriodStart:          sql.NullTime{Time: periodStart, Valid: true},
			PeriodEnd:            sql.NullTime{Time: periodEnd, Valid: true},
			GrossMeteredAmount:   grossMeteredAmt,
		})
		if txErr != nil {
			return fmt.Errorf("upsert invoice draft: %w", txErr)
		}
		if txErr = persistInvoiceLineItems(ctx, tx, invoiceID, tenantID, ratingResult); txErr != nil {
			return txErr
		}
		txErr = queries.BackfillSubscriptionPeriodFromDraft(ctx, purserdb.BackfillSubscriptionPeriodFromDraftParams{
			PeriodStart: sql.NullTime{Time: periodStart, Valid: true},
			PeriodEnd:   sql.NullTime{Time: periodEnd, Valid: true},
			TenantID:    tenantID,
		})
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
	}).Debug("Updated invoice draft")

	return nil
}
