package handlers

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/shopspring/decimal"
	"github.com/sirupsen/logrus"

	"frameworks/api_billing/internal/appconfig"
	billingpkg "frameworks/api_billing/internal/billing"
	"frameworks/api_billing/internal/database/purserdb"
	"frameworks/api_billing/internal/fx"
	billingmollie "frameworks/api_billing/internal/mollie"
	"frameworks/api_billing/internal/pricing"
	"frameworks/api_billing/internal/rating"
	billingstripe "frameworks/api_billing/internal/stripe"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/billing"
	decklog "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/decklog"
	periscope "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/periscope"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/geoip"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/models"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	foghorncontrolpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn_control"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/restream"
)

type canonicalUsageDelta struct {
	clusterID    string
	usageType    string
	unit         string
	dimensions   models.JSONB
	usageValue   float64
	usageDetails models.JSONB
}

const maxPersistedUsageQuantity = 100_000_000_000_000

var meteringSourceIdentityPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

func normalizedUsageDimensions(dimensions models.JSONB) models.JSONB {
	if dimensions == nil {
		return models.JSONB{}
	}
	return dimensions
}

func truncateRunes(value string, maximum int) string {
	runes := []rune(value)
	if len(runes) <= maximum {
		return value
	}
	return string(runes[:maximum])
}

func (jm *JobManager) quarantineUsageReport(ctx context.Context, msg kafka.Message, summary *models.UsageSummary, reason string) error {
	reportID, sourceID, tenantID := "", "", any(nil)
	if summary != nil {
		reportID = truncateRunes(summary.ReportID, 64)
		sourceID = truncateRunes(summary.SourceID, 128)
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
		ReportID: reportID, SourceID: sourceID, TenantID: tenant, RejectedReason: truncateRunes(reason, 100),
		SourceTopic: msg.Topic, SourcePartition: sql.NullInt32{Int32: msg.Partition, Valid: true},
		SourceOffset: sql.NullInt64{Int64: msg.Offset, Valid: true}, RawPayload: raw,
	})
}

func validateUsageSummaryEnvelope(summary models.UsageSummary) error {
	if len(summary.ReportID) != 64 {
		return errors.New("invalid_report_id")
	}
	if _, err := hex.DecodeString(summary.ReportID); err != nil {
		return errors.New("invalid_report_id")
	}
	if len(summary.SourceID) == 0 || len(summary.SourceID) > 128 || !meteringSourceIdentityPattern.MatchString(summary.SourceID) {
		return errors.New("invalid_source_id")
	}
	if summary.SourceRegion != "" && (len(summary.SourceRegion) > 64 || !meteringSourceIdentityPattern.MatchString(summary.SourceRegion)) {
		return errors.New("invalid_source_region")
	}
	if summary.ReportKind != "finalized" && summary.ReportKind != "reservation" && summary.ReportKind != "window_complete" {
		return errors.New("invalid_report_kind")
	}
	if summary.Sequence == 0 {
		return errors.New("missing_sequence")
	}
	if summary.Sequence > math.MaxInt64 {
		return errors.New("invalid_sequence")
	}
	if _, err := uuid.Parse(summary.TenantID); err != nil {
		return errors.New("invalid_tenant_id")
	}
	if len(summary.ClusterID) == 0 || len(summary.ClusterID) > 100 {
		return errors.New("invalid_cluster_id")
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
	if summary.ReportKind == "window_complete" && strings.TrimSpace(summary.SourceRegion) == "" {
		return errors.New("window_complete_missing_source_region")
	}
	return nil
}

func (jm *JobManager) validateUsageSummaryMeters(ctx context.Context, summary models.UsageSummary) error {
	type meterDefinition struct {
		unit              string
		allowedDimensions map[string]struct{}
	}
	definitions := make(map[string]meterDefinition)
	validateContract := func(meter, unit string, dimensions models.JSONB, allowEmptyUnit bool) error {
		if !rating.ValidMeter(rating.Meter(meter)) {
			return fmt.Errorf("invalid_meter:%s", meter)
		}
		definition, ok := definitions[meter]
		if !ok {
			row, err := purserdb.New(jm.db).GetActiveMeterDefinition(ctx, meter)
			if err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return fmt.Errorf("unknown_meter:%s", meter)
				}
				return fmt.Errorf("load_meter_definition:%w", err)
			}
			definition = meterDefinition{unit: row.Unit, allowedDimensions: make(map[string]struct{}, len(row.AllowedDimensions))}
			for _, key := range row.AllowedDimensions {
				definition.allowedDimensions[key] = struct{}{}
			}
			definitions[meter] = definition
		}
		if (!allowEmptyUnit || unit != "") && unit != definition.unit {
			return fmt.Errorf("unit_mismatch:%s", meter)
		}
		for key, value := range dimensions {
			if _, ok := definition.allowedDimensions[key]; !ok {
				return fmt.Errorf("dimension_not_allowed:%s:%s", meter, key)
			}
			stringValue, ok := value.(string)
			if !ok {
				return fmt.Errorf("dimension_not_string:%s:%s", meter, key)
			}
			switch key {
			case "delivery_kind":
				if stringValue != "playback" && stringValue != "restream" {
					return fmt.Errorf("dimension_value_not_allowed:%s:%s", meter, key)
				}
			case "platform":
				normalized, supported := restream.NormalizePlatform(stringValue)
				if !supported || normalized != stringValue {
					return fmt.Errorf("dimension_value_not_allowed:%s:%s", meter, key)
				}
			}
		}
		return nil
	}
	validQuantity := func(quantity float64, allowNegative bool) bool {
		return !math.IsNaN(quantity) && !math.IsInf(quantity, 0) && math.Abs(quantity) < maxPersistedUsageQuantity && (allowNegative || quantity >= 0)
	}
	dimensionKey := func(dimensions models.JSONB) (string, error) {
		encoded, err := json.Marshal(normalizedUsageDimensions(dimensions))
		if err != nil {
			return "", err
		}
		return usageDimensionKey(encoded), nil
	}

	canonicalKeys := make(map[string]struct{}, len(summary.Meters))
	for _, meter := range summary.Meters {
		if !validQuantity(meter.Quantity, false) {
			return fmt.Errorf("invalid_quantity:%s", meter.Meter)
		}
		if err := validateContract(meter.Meter, meter.Unit, meter.Dimensions, false); err != nil {
			return err
		}
		key, err := dimensionKey(meter.Dimensions)
		if err != nil {
			return fmt.Errorf("invalid_dimensions:%s", meter.Meter)
		}
		key = meter.Meter + ":" + key
		if _, duplicate := canonicalKeys[key]; duplicate {
			return fmt.Errorf("duplicate_meter:%s", meter.Meter)
		}
		canonicalKeys[key] = struct{}{}
	}
	providerKeys := make(map[string]struct{}, len(summary.ProviderUsage))
	for _, provider := range summary.ProviderUsage {
		if len(provider.ProviderTenantID) > 100 || len(provider.ProviderClusterID) > 100 {
			return errors.New("invalid_provider_identity")
		}
		if !validQuantity(provider.Meter.Quantity, false) {
			return fmt.Errorf("invalid_provider_quantity:%s", provider.Meter.Meter)
		}
		if err := validateContract(provider.Meter.Meter, provider.Meter.Unit, provider.Meter.Dimensions, false); err != nil {
			return err
		}
		key, err := dimensionKey(provider.Meter.Dimensions)
		if err != nil {
			return fmt.Errorf("invalid_provider_dimensions:%s", provider.Meter.Meter)
		}
		key = provider.ProviderTenantID + ":" + provider.ProviderClusterID + ":" + provider.Meter.Meter + ":" + key
		if _, duplicate := providerKeys[key]; duplicate {
			return fmt.Errorf("duplicate_provider_meter:%s", provider.Meter.Meter)
		}
		providerKeys[key] = struct{}{}
	}
	adjustmentKeys := make(map[string]struct{}, len(summary.UsageAdjustments))
	for _, adjustment := range summary.UsageAdjustments {
		if len(adjustment.SourceSystem) == 0 || len(adjustment.SourceSystem) > 64 || len(adjustment.SourceID) == 0 || len(adjustment.SourceID) > 255 {
			return errors.New("invalid_adjustment_source")
		}
		if len(adjustment.ClusterID) > 100 || len(adjustment.Reason) > 255 {
			return errors.New("invalid_adjustment_metadata")
		}
		if !validQuantity(adjustment.DeltaValue, true) {
			return fmt.Errorf("invalid_adjustment_quantity:%s", adjustment.UsageType)
		}
		if rejection := validateCanonicalUsageWindow(adjustment.PeriodStart.UTC(), adjustment.PeriodEnd.UTC(), "minute_5", "delta"); rejection != "" {
			return fmt.Errorf("invalid_adjustment_period:%s", rejection)
		}
		if err := validateContract(adjustment.UsageType, adjustment.Unit, adjustment.Dimensions, true); err != nil {
			return err
		}
		key := adjustment.SourceSystem + ":" + adjustment.SourceID
		if _, duplicate := adjustmentKeys[key]; duplicate {
			return errors.New("duplicate_adjustment")
		}
		adjustmentKeys[key] = struct{}{}
	}
	return nil
}

// mollieAnchoredPeriod is the billing period that ends on Mollie's next
// payment date. A period that started later, at a switch from prepaid that
// closed the prepaid phase, keeps that start: the phase before it is on its
// prepaid statement.
func mollieAnchoredPeriod(mollieNext time.Time, billingPeriodStart sql.NullTime) (time.Time, time.Time) {
	periodEnd := time.Date(mollieNext.Year(), mollieNext.Month(), mollieNext.Day(), 0, 0, 0, 0, time.UTC)
	periodStart := periodEnd.AddDate(0, -1, 0)
	if billingPeriodStart.Valid && billingPeriodStart.Time.After(periodStart) && billingPeriodStart.Time.Before(periodEnd) {
		periodStart = billingPeriodStart.Time
	}
	return periodStart, periodEnd
}

// nextBillingPeriodEnd is the end of the period that follows a period ending
// at periodEnd. splitStart is where that period began before switches
// between prepaid and postpaid split it (splitPeriodStart), so the next
// period is as long as the whole period and not as its last part. A period of
// whole calendar months is followed by as many calendar months; any other
// period, such as a 30-day one or a fragment a split left without its closing
// documents, by one calendar month.
func nextBillingPeriodEnd(splitStart, periodEnd time.Time) time.Time {
	start, end := splitStart.UTC(), periodEnd.UTC()
	for months := 1; months <= 12; months++ {
		if start.AddDate(0, months, 0).Equal(end) {
			return end.AddDate(0, months, 0)
		}
	}
	return end.AddDate(0, 1, 0)
}

func loadSubscriptionPeriod(ctx context.Context, db *sql.DB, tenantID string, now time.Time) (time.Time, time.Time, error) {
	period, err := purserdb.New(db).GetActiveSubscriptionPeriod(ctx, tenantID)
	if err == nil && period.MollieNextPaymentDate.Valid {
		periodStart, periodEnd := mollieAnchoredPeriod(period.MollieNextPaymentDate.Time, period.BillingPeriodStart)
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
	fxSyncer          *fx.Syncer
}

// TierReconciler is the subset of tieraccess.Reconciler used by the downgrade
// applier and the deployment-tier sweep. Defined as an interface so
// JobManager tests can stub it without pulling in the Quartermaster client.
type TierReconciler interface {
	Reconcile(ctx context.Context, tenantID string, tierLevel int32, tierName string) ([]string, string, error)
	RevokeDNSEntitlements(ctx context.Context, tenantID string) error
	SweepDeploymentTiers(ctx context.Context) (int, error)
}

// NewJobManager creates a new job manager
func NewJobManager(database *sql.DB, log logging.Logger, commodoreClient CommodoreClient, decklogSvc *decklog.BatchedClient, periscopeSvc *periscope.GRPCClient, tierReconciler TierReconciler, billing *Service, geoipReader *geoip.Reader) *JobManager {
	// Initialize Kafka consumer
	rt := appconfig.Runtime()
	brokers := rt.KafkaBrokers
	clusterID := rt.KafkaClusterID
	clientID := rt.KafkaClientID
	groupID := rt.KafkaGroupID
	billingTopic := rt.BillingKafkaTopic
	kLogger := logrus.New() // Adapt logger

	// Consumer group for billing reports
	// Note: We reuse KAFKA_BROKERS but use a unique group ID to avoid collision with analytics consumers
	consumer, err := kafka.NewConsumer(brokers, groupID, clusterID, clientID, kLogger)
	if err != nil {
		log.WithError(err).Error("Failed to create Kafka consumer for billing")
		// Don't fatal here, allow API to start without consumer if needed
	}

	includeTestnets := rt.X402IncludeTestnets
	emailSvc := NewEmailService(log)
	x402Submitter := NewX402Handler(database, log, NewHDWallet(database, log), NewRPCClient(), commodoreClient, geoipReader)
	var purserMetrics *PurserMetrics
	if billing != nil {
		purserMetrics = billing.metrics
	}
	var fxAgeGauge *prometheus.GaugeVec
	if purserMetrics != nil {
		fxAgeGauge = purserMetrics.FXRateReferenceAge
	}

	jm := &JobManager{
		db:                database,
		logger:            log,
		emailService:      emailSvc,
		cryptoMonitor:     NewCryptoMonitorWithMetrics(database, log, decklogSvc, purserMetrics, geoipReader),
		gasWalletMonitor:  NewGasWalletMonitor(log),
		x402Reconciler:    NewX402Reconciler(database, log, includeTestnets, x402Submitter),
		kafkaConsumer:     consumer,
		stopCh:            make(chan struct{}),
		billingTopic:      billingTopic,
		commodoreClient:   commodoreClient,
		periscopeClient:   periscopeSvc,
		thresholdEnforcer: NewThresholdEnforcer(database, log, commodoreClient, emailSvc, billing, tierReconciler),
		tierReconciler:    tierReconciler,
		billing:           billing,
		fxSyncer:          fx.NewSyncer(database, log, fx.HTTPFetcher(&http.Client{}), fxAgeGauge),
	}
	if billing != nil {
		wireAdvanceBilling(billing, jm)
	}
	return jm
}

// wireAdvanceBilling gives the webhook service the job manager's advance
// billing steps, which activation paths run after a tenant changes tier.
func wireAdvanceBilling(billing *Service, jm *JobManager) {
	billing.chargeAdvanceBaseFee = jm.ChargeAdvanceBaseFee
	billing.closeAdvanceBilledPeriod = jm.CloseAdvanceBilledPeriod
}

// Start begins all background jobs
func (jm *JobManager) Start(ctx context.Context) {
	jm.logger.Info("Starting billing job manager")

	// Start usage report consumer
	if jm.kafkaConsumer != nil {
		jm.kafkaConsumer.AddHandler(jm.billingTopic, jm.handleUsageReport)
		go func() {
			if err := jm.kafkaConsumer.Start(ctx); err != nil && ctx.Err() == nil {
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

	// Fill card settlements that were not settled when the payment succeeded.
	go jm.runProviderSettlementRetry(ctx)

	// Start deployment-tier sweep (Purser is the authority for
	// quartermaster.tenants.deployment_tier).
	go jm.runDeploymentTierSweep(ctx)

	// Keep ECB reference rates current for presentment-currency conversions.
	if jm.fxSyncer != nil {
		go jm.fxSyncer.Start(ctx, jm.stopCh)
	}
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
	summary, ingestSource, err := decodeUsageSummary(msg.Value)
	if err != nil {
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
	if envelopeErr := validateUsageSummaryEnvelope(summary); envelopeErr != nil {
		if quarantineErr := jm.quarantineUsageReport(ctx, msg, &summary, envelopeErr.Error()); quarantineErr != nil {
			return fmt.Errorf("quarantine invalid usage report: %w", quarantineErr)
		}
		return nil
	}
	if meterErr := jm.validateUsageSummaryMeters(ctx, summary); meterErr != nil {
		if quarantineErr := jm.quarantineUsageReport(ctx, msg, &summary, meterErr.Error()); quarantineErr != nil {
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

	// The report is processed under the billing model of the phase its
	// records belong to, read while they were written.
	acceptedUsage, billingModel, err := jm.receiveUsageSummary(ctx, summary, ingestSource)
	if err != nil {
		jm.logger.WithError(err).WithFields(logging.Fields{
			"tenant_id": summary.TenantID,
			"report_id": summary.ReportID,
		}).Error("Failed to process usage summary from Kafka")
		return err
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
	periodStart, periodEnd, _, err := parseUsageSummaryPeriod(summary)
	if err != nil {
		return err
	}
	return database.WithRetryablePostgresTx(ctx, jm.db, nil, func(tx *sql.Tx) error {
		queries := purserdb.New(tx)
		persistedRegion, err := queries.UpsertMeteringSource(ctx, purserdb.UpsertMeteringSourceParams{
			SourceID: summary.SourceID, Region: summary.SourceRegion, ActiveFrom: periodStart,
		})
		if err != nil {
			return fmt.Errorf("register metering source: %w", err)
		}
		if strings.TrimSpace(persistedRegion) != strings.TrimSpace(summary.SourceRegion) {
			return fmt.Errorf("metering source %q is registered in region %q, report says %q", summary.SourceID, persistedRegion, summary.SourceRegion)
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
		return nil
	})
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
		currency = billing.LedgerCurrency
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
	phase, err := prepaidUsagePhase(ctx, jm.db, summary.TenantID, billingPeriodStart, billingPeriodEnd)
	if err != nil {
		return err
	}
	perCluster, err := collectPhaseDimensionedUsage(ctx, purserdb.New(jm.db), summary.TenantID, phase)
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
	waiveUsage := tier.WaivesUsage(appconfig.Runtime().WaiveUsageCharges)
	finalizedAmount, err := ratePrepaidQuantities(currency, rules, finalized, billingPeriodStart, billingPeriodEnd, waiveUsage)
	if err != nil {
		return fmt.Errorf("rate finalized usage before reservation: %w", err)
	}
	combined := append(append([]rating.DimensionedQuantity{}, finalized...), active...)
	combinedAmount, err := ratePrepaidQuantities(currency, rules, combined, billingPeriodStart, billingPeriodEnd, waiveUsage)
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
	_, usagePeriodEnd, _, err := parseUsageSummaryPeriod(summary)
	if err != nil {
		return err
	}
	return database.WithRetryablePostgresTx(ctx, jm.db, nil, func(tx *sql.Tx) error {
		queries := purserdb.New(tx)
		if err := queries.InsertUsageReportReceipt(ctx, purserdb.InsertUsageReportReceiptParams{
			ReportID: summary.ReportID, ReportKind: "reservation", SourceID: summary.SourceID,
			SourceRegion: summary.SourceRegion, Sequence: int64(summary.Sequence), TenantID: summary.TenantID,
			ClusterID: summary.ClusterID, PeriodStart: usagePeriodStart, PeriodEnd: usagePeriodEnd, Complete: summary.Complete,
		}); err != nil {
			return fmt.Errorf("record reservation report: %w", err)
		}
		if err := queries.UpsertUsageReservation(ctx, purserdb.UpsertUsageReservationParams{
			TenantID: summary.TenantID, SourceID: summary.SourceID, ClusterID: summary.ClusterID,
			Sequence: int64(summary.Sequence), ReportID: summary.ReportID,
			PeriodStart: usagePeriodStart, PeriodEnd: usagePeriodEnd, Meters: metersJSON,
			ReservedAmountMicro: reservedMicro, Currency: currency,
		}); err != nil {
			return fmt.Errorf("upsert usage reservation: %w", err)
		}
		return nil
	})
}

func ratePrepaidQuantities(currency string, rules []rating.Rule, quantities []rating.DimensionedQuantity, periodStart, periodEnd time.Time, waiveUsage bool) (decimal.Decimal, error) {
	usage := make(map[rating.Meter]decimal.Decimal)
	for _, quantity := range quantities {
		usage[quantity.Meter] = usage[quantity.Meter].Add(quantity.Quantity)
	}
	result, err := rating.Rate(rating.Input{
		Currency: currency, BasePrice: decimal.Zero, Rules: rules,
		Usage: usage, Quantities: quantities,
		PeriodStart: periodStart, PeriodEnd: periodEnd,
		WaiveUsageCharges: waiveUsage,
	})
	if err != nil {
		return decimal.Zero, err
	}
	return result.UsageAmount, nil
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

type prepaidUsageSettlementResult struct {
	previousBalanceCents   int64
	newBalanceCents        int64
	applied                bool
	marginalMicro          int64
	desiredCumulativeMicro int64
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
		currency = billing.LedgerCurrency
	}
	if currency != billing.LedgerCurrency {
		return fmt.Errorf("prepaid balance currency %s cannot settle usage priced in %s", billing.LedgerCurrency, currency)
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
	referenceID := usageSummaryReferenceID(summary)
	periodLabel := summary.PeriodStart.UTC().Format(time.RFC3339) + "/" + summary.PeriodEnd.UTC().Format(time.RFC3339)
	result := prepaidUsageSettlementResult{}
	settledByPeer, leftPrepaid := false, false
	var billingPeriodStart time.Time
	err = database.WithRetryablePostgresTx(ctx, jm.db, nil, func(tx *sql.Tx) error {
		// WithRetryablePostgresTx can invoke this closure again after either a
		// body or commit failure. Never carry an aborted attempt's outcome into
		// the successful attempt's post-commit threshold handling.
		result = prepaidUsageSettlementResult{}
		settledByPeer, leftPrepaid = false, false
		queries := purserdb.New(tx)
		// The subscription row lock comes first, as in the switches between
		// prepaid and postpaid: a settlement never runs against a period a
		// switch is closing. A tenant that left prepaid since the report was
		// received has its prepaid phase closed by a statement, which settled
		// the phase's usage.
		subscription, lockErr := queries.LockSubscriptionForUsage(ctx, summary.TenantID)
		if errors.Is(lockErr, sql.ErrNoRows) {
			leftPrepaid = true
			return nil
		}
		if lockErr != nil {
			return fmt.Errorf("lock subscription: %w", lockErr)
		}
		if subscription.BillingModel != "prepaid" {
			leftPrepaid = true
			return nil
		}
		var billingPeriodEnd time.Time
		billingPeriodStart, billingPeriodEnd = prepaidPhasePeriod(subscription.BillingPeriodStart, subscription.BillingPeriodEnd, periodStart)
		if insertErr := queries.EnsurePrepaidBalance(ctx, purserdb.EnsurePrepaidBalanceParams{
			TenantID: summary.TenantID, Currency: currency,
		}); insertErr != nil {
			return fmt.Errorf("ensure prepaid balance: %w", insertErr)
		}
		lockedBalance, lockErr := queries.LockPrepaidBalance(ctx, purserdb.LockPrepaidBalanceParams{
			TenantID: summary.TenantID, Currency: currency,
		})
		if lockErr != nil {
			return fmt.Errorf("lock prepaid balance: %w", lockErr)
		}

		alreadySettled, existsErr := queries.PrepaidUsageSettlementExists(ctx, summary.ReportID)
		if existsErr != nil {
			return fmt.Errorf("recheck prepaid usage settlement: %w", existsErr)
		}
		if alreadySettled {
			settledByPeer = true
			return nil
		}

		phase, phaseErr := prepaidUsagePhase(ctx, tx, summary.TenantID, billingPeriodStart, billingPeriodEnd)
		if phaseErr != nil {
			return phaseErr
		}
		perCluster, collectErr := collectPhaseDimensionedUsage(ctx, queries, summary.TenantID, phase)
		if collectErr != nil {
			return fmt.Errorf("collect cumulative prepaid usage: %w", collectErr)
		}
		desiredAmount, rateErr := jm.rateCumulativePrepaidUsage(ctx, summary.TenantID, billingPeriodStart, billingPeriodEnd, tier, perCluster)
		if rateErr != nil {
			return rateErr
		}
		result.desiredCumulativeMicro = desiredAmount.Mul(decimal.NewFromInt(1_000_000)).Round(0).IntPart()

		previouslySettledMicro, sumErr := queries.SumPrepaidUsageSettlements(ctx, purserdb.SumPrepaidUsageSettlementsParams{
			TenantID: summary.TenantID, BillingPeriodStart: billingPeriodStart, BillingPeriodEnd: billingPeriodEnd,
		})
		if sumErr != nil {
			return fmt.Errorf("sum prior prepaid usage settlements: %w", sumErr)
		}
		result.marginalMicro = result.desiredCumulativeMicro - previouslySettledMicro
		result.previousBalanceCents = lockedBalance.BalanceCents
		result.newBalanceCents = lockedBalance.BalanceCents
		if result.marginalMicro != 0 {
			var applyErr error
			result.previousBalanceCents, result.newBalanceCents, result.applied, applyErr = applyPrepaidBalanceForUsageMicroLocked(
				ctx, queries, summary.TenantID, result.marginalMicro, "Usage: "+periodLabel, referenceID,
				lockedBalance.BalanceCents, lockedBalance.BalanceRemainderMicro,
			)
			if applyErr != nil {
				return fmt.Errorf("apply prepaid usage amount: %w", applyErr)
			}
			if !result.applied {
				return fmt.Errorf("usage balance transaction %s exists without settlement %s", referenceID, summary.ReportID)
			}
		}

		rows, insertErr := queries.InsertPrepaidUsageSettlement(ctx, purserdb.InsertPrepaidUsageSettlementParams{
			ReportID: summary.ReportID, TenantID: summary.TenantID,
			BillingPeriodStart: billingPeriodStart, BillingPeriodEnd: billingPeriodEnd,
			AmountMicro: result.marginalMicro, CumulativeAmountMicro: result.desiredCumulativeMicro,
			Currency: currency,
		})
		if insertErr != nil {
			return fmt.Errorf("record prepaid usage settlement: %w", insertErr)
		}
		if rows != 1 {
			return fmt.Errorf("prepaid usage settlement %s was not inserted", summary.ReportID)
		}
		return nil
	})
	if err != nil {
		return err
	}
	if settledByPeer {
		return nil
	}
	if leftPrepaid {
		jm.logger.WithFields(logging.Fields{
			"tenant_id": summary.TenantID,
			"report_id": summary.ReportID,
		}).Info("Tenant left prepaid before its usage report was settled; the prepaid phase's statement settled it")
		return nil
	}

	jm.logger.WithFields(logging.Fields{
		"tenant_id":               summary.TenantID,
		"period":                  periodLabel,
		"billing_period_start":    billingPeriodStart.Format(time.RFC3339),
		"marginal_micro":          result.marginalMicro,
		"cumulative_amount_micro": result.desiredCumulativeMicro,
	}).Info("Settled cumulative prepaid usage")

	if result.applied && jm.thresholdEnforcer != nil {
		if err := jm.thresholdEnforcer.EnforcePrepaidThresholds(ctx, summary.TenantID, result.previousBalanceCents, result.newBalanceCents); err != nil {
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
		if currency != billing.LedgerCurrency {
			return decimal.Zero, fmt.Errorf("prepaid usage on cluster %s prices in %s but prepaid balance currency is %s", clusterID, currency, billing.LedgerCurrency)
		}
		amount, err := ratePrepaidQuantities(currency, rules, quantities, periodStart, periodEnd, tier.WaivesUsage(appconfig.Runtime().WaiveUsageCharges))
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
// Used by invoice finalization so the credit deduction commits or rolls back
// together with the invoice header and line items.
func (jm *JobManager) deductPrepaidBalanceForCreditTx(ctx context.Context, tx *sql.Tx, tenantID string, requestCents int64, description string, referenceID *string) (newBalance, appliedCents int64, isDuplicate bool, err error) {
	return deductPrepaidBalanceForCreditTx(ctx, tx, tenantID, requestCents, description, referenceID)
}

func deductPrepaidBalanceForCreditTx(ctx context.Context, tx *sql.Tx, tenantID string, requestCents int64, description string, referenceID *string) (newBalance, appliedCents int64, isDuplicate bool, err error) {
	currency := billing.LedgerCurrency
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

func invoiceCreditReturnedDescription(periodStart time.Time) string {
	return fmt.Sprintf("Invoice credit returned: %s", periodStart.Format("2006-01"))
}

// invoiceCreditKey names the ledger rows that hold one billing document's
// invoice credit: SumAppliedInvoiceCredit sums the rows with its two
// descriptions, and reference keeps their idempotency references apart from
// other keys'.
type invoiceCreditKey struct {
	applied, returned, reference string
}

// monthInvoiceCreditKey is the key of the invoice credit held by the first
// billing document starting in a month: the month the period starts in.
func monthInvoiceCreditKey(periodStart time.Time) invoiceCreditKey {
	return invoiceCreditKey{
		applied:   invoiceCreditDescription(periodStart),
		returned:  invoiceCreditReturnedDescription(periodStart),
		reference: periodStart.Format("2006-01-02"),
	}
}

// startInvoiceCreditKey is the key of the invoice credit held by a later
// billing document of a month: its exact period start.
func startInvoiceCreditKey(periodStart time.Time) invoiceCreditKey {
	start := periodStart.UTC().Format(time.RFC3339Nano)
	return invoiceCreditKey{
		applied:   "Invoice credit: " + start,
		returned:  "Invoice credit returned: " + start,
		reference: start,
	}
}

// documentInvoiceCreditKey is the key of the invoice credit the tenant's
// billing document starting at periodStart holds. Each document has its own:
// the first document starting in a month holds it under the month's key,
// which every document had while a month started one period at most, and a
// later one of the same month, after a period closed early or a switch
// between prepaid and postpaid, under its exact start. One document's
// reconciliation therefore never takes or returns another's credit.
func documentInvoiceCreditKey(ctx context.Context, queries *purserdb.Queries, tenantID string, periodStart time.Time) (invoiceCreditKey, error) {
	monthStart := time.Date(periodStart.Year(), periodStart.Month(), 1, 0, 0, 0, 0, periodStart.Location())
	earlier, err := queries.UsageDocumentStartsEarlierInMonth(ctx, purserdb.UsageDocumentStartsEarlierInMonthParams{
		TenantID: tenantID, MonthStart: monthStart, PeriodStart: periodStart,
	})
	if err != nil {
		return invoiceCreditKey{}, fmt.Errorf("look up earlier documents of the month: %w", err)
	}
	if earlier {
		return startInvoiceCreditKey(periodStart), nil
	}
	return monthInvoiceCreditKey(periodStart), nil
}

// invoiceCreditReferenceID names one movement of a document's invoice
// credit. entries is the number of ledger rows the key already has, so a
// debit that repeats an earlier amount after credit was returned gets its own
// reference instead of colliding with the first one.
func invoiceCreditReferenceID(tenantID string, key invoiceCreditKey, entries, heldCents, deltaCents int64) string {
	raw := fmt.Sprintf(
		"invoice_credit:%s:%s:%d:%d:%d",
		tenantID,
		key.reference,
		entries,
		heldCents,
		deltaCents,
	)
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(raw)).String()
}

// invoiceCreditChange is how one reconciliation moved the prepaid credit a
// billing period's invoice holds.
type invoiceCreditChange struct {
	// PreviousCents is the credit the period held before the reconciliation.
	PreviousCents int64
	// AppliedCents is the credit the period holds after it; the invoice
	// records exactly this amount as prepaid_credit_applied.
	AppliedCents int64
	// BalanceCents is the prepaid balance after the movement.
	BalanceCents int64
}

func (c invoiceCreditChange) moved() bool { return c.PreviousCents != c.AppliedCents }

// reconcileInvoicePrepaidCreditTx moves prepaid balance so the tenant's
// billing document starting at periodStart holds exactly grossCents of
// credit, bounded by the credit it already holds plus the positive prepaid
// balance. Only the missing amount is debited; credit above the target
// returns to the balance with its own ledger row. A finalized invoice
// targets its gross amount; drafts, held invoices, statements and switches
// target zero, so credit is taken only when an invoice is finalized. The
// credit is held under the document's own key (documentInvoiceCreditKey).
//
// The prepaid balance row lock serializes every movement of a tenant's
// invoice credit, so the held amount read under it is exact.
func reconcileInvoicePrepaidCreditTx(ctx context.Context, tx *sql.Tx, tenantID string, periodStart time.Time, grossCents int64) (invoiceCreditChange, error) {
	queries := purserdb.New(tx)
	balance, err := queries.LockPrepaidBalanceCents(ctx, purserdb.LockPrepaidBalanceCentsParams{
		TenantID: tenantID,
		Currency: billing.LedgerCurrency,
	})
	if errors.Is(err, sql.ErrNoRows) {
		// Invoice credit only ever comes out of a balance row.
		return invoiceCreditChange{}, nil
	}
	if err != nil {
		return invoiceCreditChange{}, fmt.Errorf("lock prepaid balance: %w", err)
	}
	key, err := documentInvoiceCreditKey(ctx, queries, tenantID, periodStart)
	if err != nil {
		return invoiceCreditChange{}, err
	}
	held, err := queries.SumAppliedInvoiceCredit(ctx, purserdb.SumAppliedInvoiceCreditParams{
		TenantID:            tenantID,
		AppliedDescription:  key.applied,
		ReturnedDescription: key.returned,
	})
	if err != nil {
		return invoiceCreditChange{}, fmt.Errorf("lookup applied invoice credit: %w", err)
	}

	change := invoiceCreditChange{PreviousCents: held.AppliedCents, AppliedCents: held.AppliedCents, BalanceCents: balance}
	target := max(grossCents, 0)
	target = min(target, held.AppliedCents+max(balance, 0))

	switch {
	case target > held.AppliedCents:
		requestCents := target - held.AppliedCents
		referenceID := invoiceCreditReferenceID(tenantID, key, held.Entries, held.AppliedCents, requestCents)
		newBalance, deltaApplied, _, deductErr := deductPrepaidBalanceForCreditTx(ctx, tx, tenantID, requestCents, key.applied, &referenceID)
		if deductErr != nil {
			return invoiceCreditChange{}, fmt.Errorf("deduct invoice credit delta: %w", deductErr)
		}
		change.AppliedCents += deltaApplied
		change.BalanceCents = newBalance
	case target < held.AppliedCents:
		returnCents := held.AppliedCents - target
		referenceID := invoiceCreditReferenceID(tenantID, key, held.Entries, held.AppliedCents, -returnCents)
		newBalance := balance + returnCents
		if err = queries.InsertInvoiceCreditBalanceTransaction(ctx, purserdb.InsertInvoiceCreditBalanceTransactionParams{
			TenantID:          tenantID,
			AmountCents:       returnCents,
			BalanceAfterCents: newBalance,
			Description:       sql.NullString{String: key.returned, Valid: true},
			ReferenceID:       sql.NullString{String: referenceID, Valid: true},
			ReferenceType:     sql.NullString{String: "invoice_credit", Valid: true},
		}); err != nil {
			return invoiceCreditChange{}, fmt.Errorf("record returned invoice credit: %w", err)
		}
		if err = queries.UpdatePrepaidBalance(ctx, purserdb.UpdatePrepaidBalanceParams{
			BalanceCents: newBalance,
			TenantID:     tenantID,
			Currency:     billing.LedgerCurrency,
		}); err != nil {
			return invoiceCreditChange{}, fmt.Errorf("return invoice credit to prepaid balance: %w", err)
		}
		change.AppliedCents = target
		change.BalanceCents = newBalance
	}
	return change, nil
}

// logInvoiceCreditChange records a committed movement of invoice credit.
func logInvoiceCreditChange(logger logging.Logger, tenantID, invoiceID string, periodStart time.Time, change invoiceCreditChange) {
	if !change.moved() {
		return
	}
	message := "Applied prepaid balance as invoice credit"
	if change.AppliedCents < change.PreviousCents {
		message = "Returned invoice credit to prepaid balance"
	}
	logger.WithFields(logging.Fields{
		"tenant_id":             tenantID,
		"invoice_id":            invoiceID,
		"billing_period":        periodStart.Format("2006-01"),
		"previous_credit_cents": change.PreviousCents,
		"credit_cents":          change.AppliedCents,
		"delta_cents":           change.AppliedCents - change.PreviousCents,
		"balance_cents":         change.BalanceCents,
	}).Info(message)
}

// InvoiceCreditReturn is an open invoice whose prepaid credit went back to
// the prepaid balance.
type InvoiceCreditReturn struct {
	InvoiceID     string
	PeriodStart   time.Time
	ReturnedCents int64
	BalanceCents  int64
}

// ReturnOpenInvoicePrepaidCreditTx returns the prepaid credit held by every
// open (draft or manual_review) invoice of the tenant to the prepaid balance
// and clears it from those invoices. A tenant that moves back to prepaid pays
// its usage from the balance, so an open postpaid invoice must not keep part
// of that balance in reserve.
func ReturnOpenInvoicePrepaidCreditTx(ctx context.Context, tx *sql.Tx, tenantID string) ([]InvoiceCreditReturn, error) {
	queries := purserdb.New(tx)
	// Balance first, then invoices: the draft writer takes the same order.
	if _, err := queries.LockPrepaidBalanceCents(ctx, purserdb.LockPrepaidBalanceCentsParams{
		TenantID: tenantID,
		Currency: billing.LedgerCurrency,
	}); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("lock prepaid balance: %w", err)
	}
	invoices, err := queries.ListOpenInvoicesHoldingPrepaidCredit(ctx, tenantID)
	if err != nil {
		return nil, fmt.Errorf("list open invoices: %w", err)
	}
	var returned []InvoiceCreditReturn
	for _, invoice := range invoices {
		change, reconcileErr := reconcileInvoicePrepaidCreditTx(ctx, tx, tenantID, invoice.PeriodStart.Time, 0)
		if reconcileErr != nil {
			return nil, fmt.Errorf("return credit of invoice %s: %w", invoice.ID, reconcileErr)
		}
		if err = queries.ClearOpenInvoicePrepaidCredit(ctx, purserdb.ClearOpenInvoicePrepaidCreditParams{
			InvoiceID: invoice.ID,
			TenantID:  tenantID,
		}); err != nil {
			return nil, fmt.Errorf("clear credit of invoice %s: %w", invoice.ID, err)
		}
		if change.moved() {
			returned = append(returned, InvoiceCreditReturn{
				InvoiceID:     invoice.ID,
				PeriodStart:   invoice.PeriodStart.Time,
				ReturnedCents: change.PreviousCents - change.AppliedCents,
				BalanceCents:  change.BalanceCents,
			})
		}
	}
	return returned, nil
}

// microPerCent converts micro currency units (10^-6) to cents, so there are
// 10^4 micro units per cent. Sub-cent residuals accumulate here so a stream of
// per-event deductions under €0.01 each eventually crosses a whole-cent
// boundary instead of being truncated to zero.
const microPerCent = int64(10_000)

// deductPrepaidBalanceForUsageMicro applies a signed prepaid usage amount in
// micro currency units (10^-6 of a currency unit). Positive amounts debit balance;
// negative correction amounts credit balance. The fractional residual is
// carried in prepaid_balances.balance_remainder_micro across events so
// sub-cent usage and credits do not structurally leak revenue. Returns
// previous and new balances in cents (the residual is private to the prepaid
// balance row).
//
// Idempotency is keyed on (tenant_id, reference_type='usage_summary', reference_id);
// duplicate calls return applied=false.
func (jm *JobManager) deductPrepaidBalanceForUsageMicro(ctx context.Context, tenantID string, amountMicro int64, description string, referenceID uuid.UUID) (int64, int64, bool, error) {
	currency := billing.LedgerCurrency

	var previousBalance, newBalance int64
	var applied bool
	err := database.WithRetryablePostgresTx(ctx, jm.db, nil, func(tx *sql.Tx) error {
		queries := purserdb.New(tx)

		if insertErr := queries.EnsurePrepaidBalance(ctx, purserdb.EnsurePrepaidBalanceParams{
			TenantID: tenantID,
			Currency: currency,
		}); insertErr != nil {
			return insertErr
		}

		lockedBalance, scanErr := queries.LockPrepaidBalance(ctx, purserdb.LockPrepaidBalanceParams{
			TenantID: tenantID,
			Currency: currency,
		})
		if scanErr != nil {
			return scanErr
		}
		var applyErr error
		previousBalance, newBalance, applied, applyErr = applyPrepaidBalanceForUsageMicroLocked(
			ctx, queries, tenantID, amountMicro, description, referenceID,
			lockedBalance.BalanceCents, lockedBalance.BalanceRemainderMicro,
		)
		return applyErr
	})
	if err != nil {
		return 0, 0, false, err
	}
	return previousBalance, newBalance, applied, nil
}

func applyPrepaidBalanceForUsageMicroLocked(
	ctx context.Context,
	queries *purserdb.Queries,
	tenantID string,
	amountMicro int64,
	description string,
	referenceID uuid.UUID,
	currentBalance, currentRemainder int64,
) (int64, int64, bool, error) {
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
		return currentBalance, currentBalance, false, nil
	}

	if updErr := queries.UpdatePrepaidBalanceWithRemainder(ctx, purserdb.UpdatePrepaidBalanceWithRemainderParams{
		BalanceCents:          newBalance,
		BalanceRemainderMicro: newRemainder,
		TenantID:              tenantID,
		Currency:              billing.LedgerCurrency,
	}); updErr != nil {
		return 0, 0, false, updErr
	}

	return currentBalance, newBalance, true, nil
}

// deductPrepaidBalanceForUsage deducts prepaid usage once per usage summary reference.
func (jm *JobManager) deductPrepaidBalanceForUsage(ctx context.Context, tenantID string, amountCents int64, description string, referenceID uuid.UUID) (int64, int64, bool, error) {
	var currentBalance, newBalance int64
	var applied bool
	currency := billing.LedgerCurrency

	err := database.WithRetryablePostgresTx(ctx, jm.db, nil, func(tx *sql.Tx) error {
		queries := purserdb.New(tx)

		err := queries.EnsurePrepaidBalance(ctx, purserdb.EnsurePrepaidBalanceParams{
			TenantID: tenantID,
			Currency: currency,
		})
		if err != nil {
			return err
		}

		currentBalance, err = queries.LockPrepaidBalanceCents(ctx, purserdb.LockPrepaidBalanceCentsParams{
			TenantID: tenantID,
			Currency: currency,
		})
		if err != nil {
			return err
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
			return err
		}
		if rowsAffected == 0 {
			newBalance, applied = currentBalance, false
			return nil
		}

		err = queries.UpdatePrepaidBalance(ctx, purserdb.UpdatePrepaidBalanceParams{
			BalanceCents: newBalance,
			TenantID:     tenantID,
			Currency:     currency,
		})
		if err != nil {
			return err
		}
		applied = true
		return nil
	})
	if err != nil {
		return 0, 0, false, err
	}

	return currentBalance, newBalance, applied, nil
}

// getPrepaidBalance supports the retained idempotent invoice-credit recovery
// path, which is not invoked by the normal dimensioned usage flow.
//
//nolint:unused
func (jm *JobManager) getPrepaidBalance(ctx context.Context, tenantID string) (int64, error) {
	currency := billing.LedgerCurrency
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
	if jm.tierReconciler != nil {
		if err := jm.tierReconciler.RevokeDNSEntitlements(ctx, tenantID); err != nil {
			return fmt.Errorf("revoke DNS entitlements after suspension: %w", err)
		}
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
	defer jm.chargeDueAdvanceBaseFees(ctx, now)

	// Identify tenants due for billing. Pricing rules / entitlements are loaded
	// per-tenant via LoadEffectiveTier so this query stays narrow.
	dueSubscriptions, err := purserdb.New(jm.db).ListSubscriptionsDueForInvoice(ctx, now)

	if err != nil {
		jm.logger.WithFields(logging.Fields{
			"error": err,
		}).Error("Failed to fetch tenant subscriptions for invoice generation")
		return
	}
	invoicesGenerated := jm.finalizeSubscriptionPeriods(ctx, dueSubscriptions, now, time.Time{})
	jm.logger.WithFields(logging.Fields{
		"invoices_generated": invoicesGenerated,
	}).Info("Monthly invoice generation completed")
}

// finalizeSubscriptionPeriods rates and finalizes each subscription's usage
// invoice for its period and returns how many it finalized. With a zero
// closeAt the period is the subscription's current one, finalized once it has
// ended, after which the subscription moves to its next period, a scheduled
// downgrade applies, and the next advance base fee is charged. A non-zero
// closeAt closes the current period early at that time: the invoice covers
// [period start, closeAt) and the caller starts the next period.
func (jm *JobManager) finalizeSubscriptionPeriods(ctx context.Context, dueSubscriptions []purserdb.ListSubscriptionsDueForInvoiceRow, now, closeAt time.Time) int {
	closingEarly := !closeAt.IsZero()
	var invoicesGenerated int
	for _, subscription := range dueSubscriptions {
		tenantID := subscription.TenantID
		billingPeriodStart := subscription.BillingPeriodStart
		billingPeriodEnd := subscription.BillingPeriodEnd
		mollieNextPaymentDate := subscription.MollieNextPaymentDate

		tier, tierErr := billingpkg.LoadEffectiveTier(ctx, jm.db, tenantID)
		if tierErr != nil {
			jm.logger.WithError(tierErr).WithField("tenant_id", tenantID).Error("Failed to load effective tier for invoice")
			continue
		}
		meteringEnabled := tier.MeteringEnabled

		var periodStart, periodEnd time.Time
		if closingEarly {
			if !billingPeriodStart.Valid || !closeAt.After(billingPeriodStart.Time) {
				jm.logger.WithField("tenant_id", tenantID).Error("Cannot close a billing period that has no start before the close time")
				continue
			}
			periodStart = billingPeriodStart.Time
			periodEnd = closeAt
		} else if mollieNextPaymentDate.Valid {
			periodStart, periodEnd = mollieAnchoredPeriod(mollieNextPaymentDate.Time, billingPeriodStart)
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
		if meteringCompletenessRequired(meteringEnabled, tier.UsageWaived) {
			if completenessErr := jm.assertMeteringComplete(ctx, tenantID, periodStart, periodEnd); completenessErr != nil {
				jm.logger.WithError(completenessErr).WithField("tenant_id", tenantID).Error("Metering incomplete; invoice finalization blocked")
				continue
			}
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

		// The prepaid balance already paid a prepaid tenant's usage as it was
		// reported, so its period closes with a statement, not an invoice.
		if subscription.BillingModel == "prepaid" && !closingEarly {
			if jm.finalizePrepaidStatementPeriod(ctx, subscription, periodStart, periodEnd, draftInvoiceID) {
				invoicesGenerated++
			}
			continue
		}

		// A period that follows phases switches closed also rates the usage of
		// those phases that reached Purser after it began.
		phaseStart, phaseErr := splitPeriodStart(ctx, jm.db, tenantID, periodStart)
		if phaseErr != nil {
			jm.logger.WithError(phaseErr).WithField("tenant_id", tenantID).Error("Failed to look up the phases before the period; skipping invoice for this period")
			continue
		}
		phase := usagePhase{start: periodStart, end: periodEnd, chainFrom: phaseStart}
		if closingEarly && billingPeriodEnd.Valid && billingPeriodEnd.Time.After(periodEnd) {
			// A period closed early is a phase of the whole period.
			phase.periodEnd = billingPeriodEnd.Time
		}
		run, rateErr := jm.ratePostpaidInvoice(ctx, subscription, tier, phase, draftInvoiceID, false)
		if rateErr != nil {
			jm.logger.WithError(rateErr).WithField("tenant_id", tenantID).Error("Failed to rate invoice; skipping invoice for this period")
			continue
		}

		nextPeriodStart := periodEnd
		nextPeriodEnd := nextBillingPeriodEnd(phaseStart, periodEnd)
		nextBillingDate := nextPeriodEnd

		// Store invoice header + rated line items atomically. If line-item
		// persistence fails, the whole invoice rolls back so totals never live
		// without their line-item audit trail. The subscription period advances
		// in the same transaction so a finalized invoice cannot leave the
		// subscription pointing at the already-billed period.
		var result postpaidInvoiceResult
		err = withTx(ctx, jm.db, func(tx *sql.Tx) error {
			// The subscription row lock comes before the balance's, in the
			// order usage receipt, settlement and switches take them.
			if txErr := purserdb.New(tx).LockSubscriptionForPeriodClose(ctx, tenantID); txErr != nil {
				return fmt.Errorf("lock subscription: %w", txErr)
			}
			var txErr error
			result, txErr = writePostpaidInvoiceTx(ctx, tx, run)
			if txErr != nil {
				return txErr
			}
			// manual_review: do not advance the subscription period.
			// Resolution flow is ops fixes pricing → re-finalize → side
			// effects fire once on the corrected total. A period closed
			// early is replaced by the caller.
			if result.status == "manual_review" || closingEarly {
				return nil
			}
			rowsAffected, txErr := purserdb.New(tx).AdvanceSubscriptionBillingPeriod(ctx, purserdb.AdvanceSubscriptionBillingPeriodParams{
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
				"amount":    result.total.Round(2).String(),
			}).Error("Failed to create invoice")
			continue
		}
		invoicesGenerated++
		jm.afterPostpaidInvoiceCommit(ctx, run, result, "Generated monthly invoice")

		// Apply any scheduled tier downgrade now that the period's invoice has
		// committed in a non-held state. Three-step ordering favors the user
		// on partial failure: flip tier first, reconcile cluster access second,
		// clear pending_* last. Pending stays set on any error so the next
		// cron tick retries.
		if result.status != "manual_review" && !closingEarly {
			jm.applyPendingDowngrade(ctx, tenantID)
			// The next period's base fee, at the tier that period runs on, is
			// charged in advance when Purser rather than a provider
			// subscription collects it.
			if baseErr := jm.chargeAdvanceBaseFee(ctx, tenantID, now); baseErr != nil {
				jm.logger.WithError(baseErr).WithField("tenant_id", tenantID).Warn("Failed to charge advance base fee; retrying next run")
			}
		}
	}
	return invoicesGenerated
}

func finalizedInvoiceStatus(total decimal.Decimal) string {
	if total.IsZero() {
		return "paid"
	}
	return "pending"
}

// meteringCompletenessRequired reports whether an invoice waits for complete
// metering: waived usage rates at zero, so missing reports change nothing.
func meteringCompletenessRequired(meteringEnabled, tenantUsageWaived bool) bool {
	return meteringEnabled && !tenantUsageWaived && !appconfig.Runtime().WaiveUsageCharges
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

	paymentFX, quoteErr := InvoicePaymentFX(ctx, jm.db, tenantID, invoiceID, currency, amountCents)
	if quoteErr != nil {
		return fmt.Errorf("record Stripe charge in EUR: %w", quoteErr)
	}
	existingPayment, insertErr := queries.UpsertPendingProviderBillingPayment(ctx, purserdb.UpsertPendingProviderBillingPaymentParams{
		PaymentID:           paymentID,
		InvoiceID:           invoiceID,
		Amount:              amountStr,
		Currency:            strings.ToUpper(currency),
		TxID:                sql.NullString{String: intentPlaceholder, Valid: true},
		OriginalAmountCents: paymentFX.OriginalMinor,
		EurAmountCents:      paymentFX.EURMinor,
		FxUnitsPerEur:       paymentFX.UnitsText(),
		FxSource:            paymentFX.Source,
		FxReferenceDate:     paymentFX.ReferenceDate,
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
		actionURL := appconfig.Runtime().WebAppURL
		if actionURL != "" {
			actionURL = strings.TrimRight(actionURL, "/") + "/account/billing?invoice=" + url.QueryEscape(invoiceID)
		}
		jm.logger.WithFields(logging.Fields{
			"tenant_id":         tenantID,
			"invoice_id":        invoiceID,
			"payment_intent_id": result.PaymentIntentID,
			"action_url":        actionURL,
		}).Warn("Stripe off-session overage requires customer authentication (SCA); directing customer to on-session invoice payment")
		go jm.billing.sendTenantActionRequiredEmail(tenantID, invoiceID, paymentID, float64(amountCents)/100, currency, actionURL)
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

	paymentFX, quoteErr := InvoicePaymentFX(ctx, jm.db, tenantID, invoiceID, currency, amountCents)
	if quoteErr != nil {
		return fmt.Errorf("record Mollie charge in EUR: %w", quoteErr)
	}
	existingPayment, insertErr := queries.UpsertPendingProviderBillingPayment(ctx, purserdb.UpsertPendingProviderBillingPaymentParams{
		PaymentID:           paymentID,
		InvoiceID:           invoiceID,
		Amount:              amountStr,
		Currency:            strings.ToUpper(currency),
		TxID:                sql.NullString{String: intentID, Valid: true},
		OriginalAmountCents: paymentFX.OriginalMinor,
		EurAmountCents:      paymentFX.EURMinor,
		FxUnitsPerEur:       paymentFX.UnitsText(),
		FxSource:            paymentFX.Source,
		FxReferenceDate:     paymentFX.ReferenceDate,
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
	if base := appconfig.Runtime().GatewayPublicBaseURL(); base != "" {
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

// processUsageSummary stores a usage summary's records, provider usage and
// corrections (receiveUsageSummary) and returns the accepted usage.
func (jm *JobManager) processUsageSummary(ctx context.Context, summary models.UsageSummary, source string) ([]canonicalUsageDelta, error) {
	accepted, _, err := jm.receiveUsageSummary(ctx, summary, source)
	return accepted, err
}

// receiveUsageSummary stores a usage summary in one transaction that holds
// the tenant's subscription row FOR SHARE (LockSubscriptionForUsage) and
// returns the accepted usage with the billing model to process it under. A
// switch between prepaid and postpaid holds that row FOR UPDATE, so the
// report's records commit before the switch counts the usage of the phase it
// closes, or after the switch committed and dated no earlier than it: every
// record belongs to exactly one phase, and the model returned is that
// phase's. A tenant without a subscription is postpaid.
func (jm *JobManager) receiveUsageSummary(ctx context.Context, summary models.UsageSummary, source string) ([]canonicalUsageDelta, string, error) {
	var accepted []canonicalUsageDelta
	var billingModel string
	err := database.WithRetryablePostgresTx(ctx, jm.db, nil, func(tx *sql.Tx) error {
		var txErr error
		accepted, billingModel, txErr = jm.receiveUsageSummaryTx(ctx, tx, summary, source)
		return txErr
	})
	if err != nil {
		return nil, "", err
	}
	return accepted, billingModel, nil
}

// receiveUsageSummaryTx is receiveUsageSummary inside the caller's
// transaction; the subscription row stays locked until it ends.
func (jm *JobManager) receiveUsageSummaryTx(ctx context.Context, tx *sql.Tx, summary models.UsageSummary, source string) ([]canonicalUsageDelta, string, error) {
	billingModel := "postpaid"
	queries := purserdb.New(tx)
	subscription, err := queries.LockSubscriptionForUsage(ctx, summary.TenantID)
	switch {
	case err == nil:
		billingModel = subscription.BillingModel
	case errors.Is(err, sql.ErrNoRows):
	default:
		return nil, "", fmt.Errorf("lock subscription for usage: %w", err)
	}
	accepted, err := jm.persistUsageSummary(ctx, queries, summary, source)
	if err != nil {
		return nil, "", err
	}
	return accepted, billingModel, nil
}

// persistUsageSummary writes a usage summary's records, provider usage and
// corrections with queries. Records failing validation go to quarantine
// outside the transaction, so a failed quarantine write does not abort it.
func (jm *JobManager) persistUsageSummary(ctx context.Context, queries *purserdb.Queries, summary models.UsageSummary, source string) ([]canonicalUsageDelta, error) {
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
		dimensionKey := usageDimensionKey(dimensionJSON)
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
		if source == legacyKafkaSource {
			adopted, legacyErr := queries.AdoptLegacyUsageRecord(ctx, purserdb.AdoptLegacyUsageRecordParams{
				Unit: meter.Unit, ReportID: summary.ReportID, UsageDetails: json.RawMessage(usageDetailsJSON),
				TenantID: summary.TenantID, ClusterID: summary.ClusterID, UsageType: usageType,
				PeriodStart: sql.NullTime{Time: periodStart, Valid: true},
				PeriodEnd:   sql.NullTime{Time: periodEnd, Valid: true},
			})
			if legacyErr != nil {
				return nil, fmt.Errorf("adopt migrated legacy usage %s: %w", usageType, legacyErr)
			}
			if adopted > 0 {
				acceptedUsage = append(acceptedUsage, canonicalUsageDelta{
					clusterID: summary.ClusterID, usageType: usageType, unit: meter.Unit,
					dimensions: dimensions, usageValue: usageValue, usageDetails: usageDetails,
				})
				continue
			}
		}

		err = queries.UpsertCanonicalUsageRecord(ctx, purserdb.UpsertCanonicalUsageRecordParams{
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

	if persistErr := persistProviderUsage(ctx, queries, summary, periodStart, periodEnd, granularity, source); persistErr != nil {
		return nil, persistErr
	}
	acceptedAdjustments, err := persistUsageAdjustments(ctx, queries, summary, source)
	if err != nil {
		return nil, err
	}
	acceptedUsage = append(acceptedUsage, acceptedAdjustments...)

	return acceptedUsage, nil
}

func usageDimensionKey(dimensionJSON []byte) string {
	dimensionHash := sha256.Sum256(dimensionJSON)
	return fmt.Sprintf("%x", dimensionHash[:])
}

func persistProviderUsage(ctx context.Context, queries *purserdb.Queries, summary models.UsageSummary, periodStart, periodEnd time.Time, granularity, source string) error {
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
		if source == legacyKafkaSource {
			err = queries.UpsertLegacyProviderUsageRecord(ctx, purserdb.UpsertLegacyProviderUsageRecordParams{
				UsageTenantID: summary.TenantID, WorkClusterID: summary.ClusterID,
				ProviderTenantID: rec.ProviderTenantID, ProviderClusterID: rec.ProviderClusterID,
				UsageType: rec.Meter.Meter, Unit: rec.Meter.Unit, UsageValue: rec.Meter.Quantity,
				Dimensions: json.RawMessage(dimensionJSON), ReportID: summary.ReportID,
				PeriodStart: periodStart, PeriodEnd: periodEnd, Source: source,
				UsageDetails: json.RawMessage(detailsJSON),
			})
			if err != nil {
				return fmt.Errorf("upsert legacy provider usage %s/%s: %w", rec.ProviderTenantID, rec.Meter.Meter, err)
			}
			continue
		}
		err = queries.UpsertProviderUsageRecord(ctx, purserdb.UpsertProviderUsageRecordParams{
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

func persistUsageAdjustments(ctx context.Context, queries *purserdb.Queries, summary models.UsageSummary, source string) ([]canonicalUsageDelta, error) {
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
			unit, unitErr := queries.GetMeterUnitForAdjustment(ctx, adj.UsageType)
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
		err = queries.UpsertUsageAdjustment(ctx, purserdb.UpsertUsageAdjustmentParams{
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
	// credit on partial usage and ack the Kafka message as processed. Usage
	// the prepaid balance paid is not rated again.
	phaseStart, err := splitPeriodStart(ctx, jm.db, tenantID, periodStart)
	if err != nil {
		return err
	}
	phase := usagePhase{start: periodStart, end: periodEnd, chainFrom: phaseStart}
	perClusterUsage, perClusterDimensioned, err := collectPhaseUsage(ctx, jm.db, tenantID, phase)
	if err != nil {
		return fmt.Errorf("collect invoice usage: %w", err)
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
	baseFeeInvoiced, baseFeeErr := purserdb.New(jm.db).BaseFeeInvoiceExistsForPeriod(ctx, purserdb.BaseFeeInvoiceExistsForPeriodParams{
		TenantID: tenantID, PeriodStart: periodStart,
	})
	if baseFeeErr != nil {
		return fmt.Errorf("check advance base-fee invoice for draft: %w", baseFeeErr)
	}
	baseProviderManaged := providerIDs.StripeSubscriptionID.Valid || providerIDs.MollieSubscriptionID.Valid || baseFeeInvoiced

	// Rate the period via the engine; one source of truth for invoice math.
	// Money stays as decimal.Decimal end-to-end and binds to NUMERIC columns
	// as decimal strings; float64 never touches the cents.
	ratingResult, err := jm.rateInvoiceForTenant(ctx, tenantID, periodStart, periodEnd, tier, true, baseProviderManaged, perClusterUsage, perClusterDimensioned)
	if err != nil {
		return fmt.Errorf("rate usage: %w", err)
	}
	applyBaseFeeShare(ratingResult, phase)
	baseDec := ratingResult.BaseAmount
	meteredDec := ratingResult.UsageAmount
	unwaivedMeteredDec := ratingResult.GrossUsageAmount
	grossDec := ratingResult.TotalAmount

	// manual_review: an unconfigured cluster pricing means we cannot finalize
	// the credit. Hold the entire draft — it holds no prepaid credit and the
	// period does not advance. Operator fixes pricing then re-runs.
	if len(ratingResult.ManualReviewReasons) > 0 {
		jm.logger.WithFields(logging.Fields{
			"tenant_id": tenantID,
			"reasons":   strings.Join(ratingResult.ManualReviewReasons, "; "),
		}).Warn("Invoice draft routed to manual_review; deduction halted")
		// Persist a manual_review header so ops can see and act on it. Credit
		// an earlier draft took returns to the balance; lines are written for
		// visibility.
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

	// The draft holds no prepaid credit: the balance stays on the balance
	// while the period runs, and the invoice takes the credit it uses when it
	// is finalized (writePostpaidInvoiceTx). Credit the draft's key still
	// holds returns to the balance in the transaction that writes the draft,
	// so the draft states the gross amount.
	dueDate := periodEnd.AddDate(0, 0, 14)
	var invoiceID string
	var creditChange invoiceCreditChange
	err = withTx(ctx, jm.db, func(tx *sql.Tx) error {
		queries := purserdb.New(tx)
		var txErr error
		creditChange, txErr = reconcileInvoicePrepaidCreditTx(ctx, tx, tenantID, periodStart, 0)
		if txErr != nil {
			return txErr
		}

		// Pass decimals as strings into Postgres NUMERIC columns so no float64
		// rounding can sneak in at the SQL boundary. PG parses '1.99'::numeric
		// exactly; '1.9900000000000002'::float8 ≠ 1.99.
		totalAmt := grossDec.Round(2).String()
		baseAmt := baseDec.Round(2).String()
		meteredAmt := meteredDec.Round(2).String()
		grossMeteredAmt := unwaivedMeteredDec.Round(2).String()
		creditAmt := decimal.Zero.String()

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
	logInvoiceCreditChange(jm.logger, tenantID, invoiceID, periodStart, creditChange)
	jm.logger.WithFields(logging.Fields{
		"tenant_id":      tenantID,
		"invoice_id":     invoiceID,
		"billing_period": periodStart.Format("2006-01"),
		"gross_amount":   grossDec.String(),
	}).Debug("Updated invoice draft")

	return nil
}
