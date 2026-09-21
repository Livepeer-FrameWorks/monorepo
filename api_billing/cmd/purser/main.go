package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"os"
	"time"

	"frameworks/api_billing/internal/appconfig"
	"frameworks/api_billing/internal/billingevents"
	"frameworks/api_billing/internal/database/purserdb"
	pursermigrations "frameworks/api_billing/internal/datamigrations"
	"frameworks/api_billing/internal/fx"
	pursergrpc "frameworks/api_billing/internal/grpc"
	"frameworks/api_billing/internal/handlers"
	"frameworks/api_billing/internal/mollie"
	"frameworks/api_billing/internal/stripe"
	"frameworks/api_billing/internal/tieraccess"
	commodoreclnt "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/commodore"
	decklogclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/decklog"
	periscopeclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/periscope"
	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/datamigrate"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	eventoutbox "github.com/Livepeer-FrameWorks/monorepo/pkg/events/outbox"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/geoip"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/monitoring"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/qmbootstrap"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/server"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/version"

	"github.com/shopspring/decimal"
	"google.golang.org/grpc"
)

func main() {
	if version.HandleCLI() {
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "data-migrations" {
		logger := logging.NewLoggerWithService("purser")
		config.LoadEnv(logger)
		migrationCfg, err := config.Load[appconfig.PurserDataMigrations](config.Options{Service: "purser", Logger: logger})
		if err != nil {
			logger.WithError(err).Fatal("Invalid data migration configuration")
		}
		pursermigrations.Register(pursermigrations.Settings{Fetch: fx.HTTPFetcher(&http.Client{})})
		dbConfig := database.DefaultConfig()
		dbConfig.ServiceName = "purser"
		dbConfig.URL = migrationCfg.DatabaseURL
		err = datamigrate.HandleArgv(context.Background(), func() (*sql.DB, error) {
			return database.Connect(dbConfig, logger)
		}, os.Stdout, os.Args[1:])
		if err != nil && !errors.Is(err, datamigrate.ErrNotDataMigrationsCommand) {
			logger.WithError(err).Fatal("Data migration command failed")
		}
		return
	}

	// `purser bootstrap …` runs reconcilers against the rendered desired-state
	// file and exits — it does NOT start the gRPC server. No-arg invocation
	// (the systemd / go_service Ansible role contract) still falls through to
	// the serve flow below unchanged.
	if len(os.Args) > 1 && os.Args[1] == "bootstrap" {
		os.Exit(runBootstrapCommand(os.Args[2:]))
	}

	// Setup logger
	logger := logging.NewLoggerWithService("purser")

	// Load environment variables
	config.LoadEnv(logger)

	logger.Info("Starting Purser (Billing API)")

	configOptions := config.Options{Service: "purser", Logger: logger}
	cfg, err := config.Load[appconfig.Purser](configOptions)
	if err != nil {
		logger.WithError(err).Fatal("Invalid configuration")
	}
	cfg.ApplyLogLevel(logger)
	metadataPolicy, err := middleware.ParseMetadataPolicy(cfg.MetadataPolicy)
	if err != nil {
		logger.WithError(err).Fatal("Invalid configuration")
	}
	geoipReader, err := geoip.Open(cfg.MMDBPath)
	if err != nil {
		logger.WithError(err).Warn("GeoIP unavailable; continuing without geolocation")
	}
	if geoipReader != nil {
		logger.WithField("provider", geoipReader.GetProvider()).Info("GeoIP reader loaded")
	}
	// Internal packages read PurserRuntime through appconfig.Runtime. It is
	// installed before any of them is constructed, and the reload callback
	// passed to server.Run re-decodes it after a SIGHUP env-file reload.
	runtimeConfig := config.NewLive(&cfg.PurserRuntime, configOptions)
	appconfig.InstallRuntime(runtimeConfig)

	// Connect to database
	dbConfig := database.DefaultConfig()
	dbConfig.ServiceName = "purser"
	dbConfig.URL = cfg.DatabaseURL
	db := database.MustConnect(dbConfig, logger)
	defer func() { _ = db.Close() }()

	// Setup monitoring
	healthChecker := monitoring.NewHealthChecker("purser", version.Version)
	metricsCollector := monitoring.NewMetricsCollector("purser", version.Version, version.GitCommit)

	// Add health checks
	healthChecker.AddCheck("database", monitoring.DatabaseHealthCheck(db))

	readiness := monitoring.NewReadinessChecker("purser", version.Version)
	readiness.AddCheck("database", monitoring.DatabaseHealthCheck(db))

	// Create custom billing metrics for HTTP handlers. invoice_operations_total
	// was declared but has no single lifecycle owner event in the code; the
	// invoice UPSERT branches on a status column internally rather than going
	// through distinct create/finalize/void/reissue entry points, so adding
	// it would only ever fire with one synthetic label combination. Drop.
	handlerMetrics := &handlers.PurserMetrics{
		BillingCalculations:       metricsCollector.NewCounter("billing_calculations_total", "Billing calculations performed", []string{"tenant_id", "status"}),
		UsageRecords:              metricsCollector.NewCounter("usage_records_processed_total", "Usage records processed", []string{"usage_type"}),
		UsageQuarantine:           metricsCollector.NewCounter("usage_records_quarantine_total", "Usage records rejected and routed to purser.usage_records_quarantine", []string{"usage_type", "reason"}),
		WebhookSignatureFailures:  metricsCollector.NewCounter("webhook_signature_failures_total", "Webhook signature validation failures", []string{"provider"}),
		CryptoScannerBlocks:       metricsCollector.NewGauge("crypto_scanner_block", "Observed crypto scanner block by head kind", []string{"network", "head"}),
		CryptoScannerErrors:       metricsCollector.NewCounter("crypto_scanner_errors_total", "Crypto scanner errors by scan stage and cause", []string{"network", "stage", "reason"}),
		CryptoDepositReorgs:       metricsCollector.NewCounter("crypto_deposit_reorg_reversals_total", "Allocated crypto deposit reversals after canonicality failure", []string{"network", "purpose"}),
		CryptoUnsweptBaseUnits:    metricsCollector.NewGauge("crypto_unswept_base_units", "Confirmed custody amount not yet assigned to a confirmed sweep", []string{"network", "asset"}),
		CryptoOldestUnswept:       metricsCollector.NewGauge("crypto_oldest_unswept_age_seconds", "Age of the oldest confirmed unswept custody source", []string{"network", "asset"}),
		CryptoFailedSweepItems:    metricsCollector.NewGauge("crypto_failed_sweep_items", "Sweep items currently failed", []string{"network", "asset"}),
		CryptoRelayerBalanceETH:   metricsCollector.NewGauge("crypto_sweep_relayer_balance_eth", "Dedicated USDC sweep relayer native gas balance", []string{"network"}),
		X402QuoteConversion:       metricsCollector.NewGauge("x402_quote_conversion_ratio", "Confirmed x402 quotes divided by created quotes in the last 24 hours", []string{"network"}),
		X402SettlementLatency:     metricsCollector.NewGauge("x402_settlement_latency_p95_seconds", "P95 confirmed x402 settlement latency over the last 24 hours", []string{"network"}),
		CryptoPendingDeposits:     metricsCollector.NewGauge("crypto_deposit_events", "Crypto deposit events by reconciliation state", []string{"network", "status"}),
		CryptoAccountingAnomalies: metricsCollector.NewGauge("crypto_accounting_anomalies", "Open crypto accounting anomalies", []string{"kind"}),
		CryptoAnomalyOldest:       metricsCollector.NewGauge("crypto_accounting_anomaly_oldest_age_seconds", "Age of the oldest open crypto accounting anomaly", []string{"kind"}),
		CryptoInvoiceReview:       metricsCollector.NewGauge("crypto_invoice_review_items", "Crypto invoice deposit events requiring operator review", []string{"network"}),
		CryptoLedgerReversals:     metricsCollector.NewGauge("crypto_ledger_reversals_total", "Durable crypto-related ledger reversals", []string{"reference_type"}),
		FXRateReferenceAge:        metricsCollector.NewGauge("fx_rate_reference_age_seconds", "Seconds since the start of the newest stored ECB reference date", []string{"currency"}),
	}

	// Register DB connection-pool stats (open/in-use/idle gauges +
	// wait_count/wait_duration counters) sourced from db.Stats() at
	// scrape time.
	metricsCollector.RegisterDBStats(db)

	// Per-method counts + duration are captured by GRPCMetricsInterceptor on
	// the GRPCRequests / GRPCDuration vectors; separate per-domain counters
	// (billing/usage/subscription/invoice) where the operation label maps 1:1
	// to a gRPC method would only rename the same axis.
	serverMetrics := &pursergrpc.ServerMetrics{
		GRPCRequests:                     metricsCollector.NewCounter("grpc_requests_total", "Total gRPC requests", []string{"method", "status"}),
		GRPCDuration:                     metricsCollector.NewHistogram("grpc_request_duration_seconds", "gRPC request duration", []string{"method"}, nil),
		TierAccessReconciliationFailures: metricsCollector.NewCounter("tier_access_reconciliation_failures_total", "Tier access and DNS entitlement reconciliation failures", []string{"operation"}),
		MediaAuthorityRefreshFailures:    metricsCollector.NewCounter("media_authority_refresh_failures_total", "Media-authority refresh failures", []string{"stage"}),
		MediaAuthorityRefreshCompletions: metricsCollector.NewCounter("media_authority_refresh_completions_total", "Media-authority refresh completion outcomes", []string{"outcome"}),
		MediaAuthorityRefreshPending:     metricsCollector.NewGauge("media_authority_refresh_pending", "Pending media-authority refresh obligations", nil),
		MediaAuthorityRefreshOldest:      metricsCollector.NewGauge("media_authority_refresh_oldest_pending_seconds", "Age of the oldest pending media-authority refresh obligation", nil),
		MediaAuthorityRefreshWorkerReady: metricsCollector.NewGauge("media_authority_refresh_worker_ready", "Whether the media-authority refresh worker has its required dependencies", nil),
	}

	// Create Quartermaster gRPC client for tenant lookups (used by webhooks)
	qmGRPCClient, err := qmclient.NewGRPCClient(qmclient.GRPCConfig{
		GRPCAddr:           cfg.QuartermasterGRPCAddr,
		Timeout:            10 * time.Second,
		Logger:             logger,
		ServiceToken:       cfg.ServiceToken,
		PreferServiceToken: true,
		AllowInsecure:      cfg.AllowInsecure,
		CACertFile:         cfg.CAPath,
		ServerName:         cfg.QuartermasterGRPCTLSServerName,

		ClusterAccessMaterializationSecret: cfg.ClusterAccessMaterializationSecret,
	})
	if err != nil {
		logger.WithError(err).Fatal("Failed to create Quartermaster gRPC client")
	}
	defer func() { _ = qmGRPCClient.Close() }()

	// Create Commodore gRPC client for stream termination on suspension
	commodoreClient, err := commodoreclnt.NewGRPCClient(commodoreclnt.GRPCConfig{
		GRPCAddr:      cfg.CommodoreGRPCAddr,
		Timeout:       30 * time.Second,
		Logger:        logger,
		ServiceToken:  cfg.ServiceToken,
		AllowInsecure: cfg.AllowInsecure,
		CACertFile:    cfg.CAPath,
		ServerName:    cfg.CommodoreGRPCTLSServerName,
	})
	if err != nil {
		logger.WithError(err).Fatal("Failed to create Commodore gRPC client")
	}
	defer func() { _ = commodoreClient.Close() }()

	// Create Decklog gRPC client for service events
	decklogClient, err := decklogclient.NewBatchedClient(decklogclient.BatchedClientConfig{
		Target:        cfg.DecklogGRPCAddr,
		AllowInsecure: cfg.AllowInsecure,
		CACertFile:    cfg.CAPath,
		ServerName:    cfg.DecklogGRPCTLSServerName,
		Timeout:       5 * time.Second,
		Source:        "purser",
		ServiceToken:  cfg.ServiceToken,
		ClusterID:     cfg.ClusterID,
		SourceRegion:  cfg.Region,
	}, logger)
	if err != nil {
		logger.WithError(err).Warn("Failed to create Decklog gRPC client - service events will be disabled")
		decklogClient = nil
	} else {
		defer func() { _ = decklogClient.Close() }()
		logger.WithField("addr", cfg.DecklogGRPCAddr).Info("Connected to Decklog gRPC")
	}
	tokenHasher, err := events.NewTokenHasher(cfg.UsageHashSecret)
	if err != nil {
		logger.WithError(err).Fatal("USAGE_HASH_SECRET is required for domain event actor attribution")
	}

	// Create Periscope gRPC client for invoice enrichment (accurate unique counts, geo breakdown)
	periscopeClient, err := periscopeclient.NewGRPCClient(periscopeclient.GRPCConfig{
		GRPCAddr:      cfg.PeriscopeGRPCAddr,
		Timeout:       30 * time.Second,
		Logger:        logger,
		ServiceToken:  cfg.ServiceToken,
		AllowInsecure: cfg.AllowInsecure,
		CACertFile:    cfg.CAPath,
		ServerName:    cfg.PeriscopeGRPCTLSServerName,
	})
	if err != nil {
		logger.WithError(err).Warn("Failed to create Periscope gRPC client - invoice enrichment will be disabled")
		periscopeClient = nil
	} else {
		defer func() { _ = periscopeClient.Close() }()
		logger.WithField("addr", cfg.PeriscopeGRPCAddr).Info("Connected to Periscope gRPC")
	}

	// Create Stripe client (optional - service works without it)
	var stripeClient *stripe.Client
	if cfg.StripeSecretKey != "" {
		stripeClient = stripe.NewClient(stripe.Config{
			SecretKey:     cfg.StripeSecretKey,
			WebhookSecret: cfg.StripeWebhookSecret,
			Logger:        logger,
		})
		logger.Info("Stripe client initialized")

		if err := syncBillingTiersWithStripe(context.Background(), db, stripeClient, logger); err != nil {
			logger.WithError(err).Warn("Stripe tier sync failed - checkout will be unavailable until tiers are configured")
		}
	} else {
		logger.Warn("STRIPE_SECRET_KEY not set - Stripe functionality disabled")
	}

	// Create Mollie client (optional - service works without it)
	var mollieClient *mollie.Client
	if cfg.MollieAPIKey != "" {
		var err error
		mollieClient, err = mollie.NewClient(mollie.Config{
			APIKey: cfg.MollieAPIKey,
			Logger: logger,
		})
		if err != nil {
			logger.WithError(err).Warn("Failed to create Mollie client - Mollie functionality disabled")
		} else {
			logger.Info("Mollie client initialized")
		}
	} else {
		logger.Warn("MOLLIE_API_KEY not set - Mollie functionality disabled")
	}

	// Initialize the webhook/checkout handler service (replaces the prior
	// package-global handlers.Init).
	billingSvc := handlers.NewService(db, logger, handlerMetrics, qmGRPCClient, mollieClient, stripeClient, decklogClient)

	// Shared tier reconciler — used by PurserServer.ChangeBillingTier and
	// by JobManager's downgrade applier so both apply the same grant/suspend
	// logic against tenant_cluster_access.
	tierReconciler := tieraccess.NewReconciler(db, qmGRPCClient, logger, serverMetrics.TierAccessReconciliationFailures)
	billingSvc.SetTenantEntitlementConverger(func(ctx context.Context, tenantID string) error {
		_, _, err := tierReconciler.ReconcileCanonical(ctx, tenantID)
		return err
	})

	// Initialize and start JobManager for background billing tasks
	jobManager := handlers.NewJobManager(db, logger, commodoreClient, decklogClient, periscopeClient, tierReconciler, billingSvc, geoipReader)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	jobManager.Start(ctx)
	defer jobManager.Stop()

	logger.Info("JobManager started - background billing jobs active")

	// Start Livepeer deposit monitor (optional - requires ARBITRUM_RPC_ENDPOINT)
	if cfg.LivepeerDepositMonitorEnabled {
		depositMonitor, err := handlers.NewLivepeerDepositMonitor(logger, db, qmGRPCClient, cfg.ClusterID)
		if err != nil {
			logger.WithError(err).Fatal("Invalid Livepeer deposit monitor configuration")
		}
		go depositMonitor.Start(ctx)
		defer depositMonitor.Stop()
		logger.Info("Livepeer deposit monitor started")
	}

	// Expose health, readiness, and metrics over HTTP; billing APIs are served over gRPC.
	router := server.NewServiceRouter(server.RouterSpec{
		Service:    "purser",
		Logger:     logger,
		Health:     healthChecker,
		Ready:      readiness,
		Metrics:    metricsCollector,
		Runtime:    cfg.HTTPRuntime,
		DebugToken: cfg.ServiceToken,
		DebugConfig: func() any {
			return config.Overlay{Base: cfg, Overrides: []any{runtimeConfig.Get()}}
		},
		DebugConfigOptions: configOptions,
	})

	grpcServerConfig := pursergrpc.GRPCServerConfig{
		DB:                  db,
		Logger:              logger,
		ServiceToken:        cfg.ServiceToken,
		JWTSecret:           []byte(cfg.JWTSecret),
		MetadataPolicy:      metadataPolicy,
		GeoIPReader:         geoipReader,
		Metrics:             serverMetrics,
		StripeClient:        stripeClient,
		MollieClient:        mollieClient,
		QuartermasterClient: qmGRPCClient,
		CommodoreClient:     commodoreClient,
		DecklogClient:       decklogClient,
		Billing:             billingSvc,
		TokenHasher:         tokenHasher,
		CertFile:            cfg.CertPath,
		KeyFile:             cfg.KeyPath,
		AllowInsecure:       cfg.AllowInsecure,
	}
	// NewGRPCServer waits up to two minutes for gRPC TLS files. Building it in
	// the background keeps HTTP /health serving during that wait; readiness
	// reports grpc_grpc unhealthy until the gRPC server serves.
	buildGRPCServer := func(ctx context.Context) (*grpc.Server, error) {
		return pursergrpc.NewGRPCServer(ctx, grpcServerConfig)
	}

	// Best-effort service registration in Quartermaster over a dedicated
	// client that does not prefer the service token.
	go func() {
		qc, qcErr := qmclient.NewGRPCClient(qmclient.GRPCConfig{
			GRPCAddr:      cfg.QuartermasterGRPCAddr,
			Timeout:       10 * time.Second,
			Logger:        logger,
			ServiceToken:  cfg.ServiceToken,
			AllowInsecure: cfg.AllowInsecure,
			CACertFile:    cfg.CAPath,
			ServerName:    cfg.QuartermasterGRPCTLSServerName,
		})
		if qcErr != nil {
			logger.WithError(qcErr).Warn("Failed to create Quartermaster gRPC client")
			return
		}
		defer func() { _ = qc.Close() }()
		req, reqErr := qmbootstrap.NewServiceRequest(qmbootstrap.ServiceRegistration{
			ServiceType:   "purser",
			Port:          cfg.HTTPListen.Port,
			AdvertiseHost: cfg.AdvertiseHost,
			ClusterID:     cfg.ClusterID,
			NodeID:        cfg.NodeID,
		})
		if reqErr != nil {
			logger.WithError(reqErr).Warn("Quartermaster bootstrap skipped")
			return
		}
		if _, bootstrapErr := qmbootstrap.BootstrapServiceWithRetry(ctx, qc, req, logger, qmbootstrap.DefaultRetryConfig("purser")); bootstrapErr != nil {
			logger.WithError(bootstrapErr).Warn("Quartermaster bootstrap (purser) failed")
		} else {
			logger.Info("Quartermaster bootstrap (purser) ok")
		}
	}()

	// The domain event relay publishes committed purser.domain_event_outbox
	// rows to Decklog. It stops after the listeners, so events committed by the
	// last in-flight requests stay in the outbox for the next process.
	var onShutdown []func(context.Context)
	if decklogClient != nil {
		relay, relayErr := eventoutbox.NewRelay(db, billingevents.Schema, decklogClient, logger)
		if relayErr != nil {
			logger.WithError(relayErr).Fatal("Failed to create the domain event relay")
		}
		relayCtx, stopRelay := context.WithCancel(context.Background())
		relayDone := make(chan struct{})
		go func() {
			defer close(relayDone)
			relay.Run(relayCtx)
		}()
		onShutdown = append(onShutdown, func(shutdownCtx context.Context) {
			stopRelay()
			select {
			case <-relayDone:
			case <-shutdownCtx.Done():
			}
		})
	} else {
		logger.Warn("Domain event relay disabled: no Decklog client; domain events stay in the outbox")
	}

	server.RegisterEnvFileReload("purser", logger)
	if runErr := server.Run(ctx, server.RunSpec{
		Service:    "purser",
		Logger:     logger,
		Ready:      readiness,
		HTTP:       []server.HTTPListener{{Name: "http", Port: cfg.HTTPListen.Port, Handler: router}},
		GRPC:       []server.GRPCListener{{Name: "grpc", Port: cfg.GRPCListen.Port, Build: buildGRPCServer}},
		OnReload:   []server.ReloadCallback{runtimeConfig.Reload},
		OnShutdown: onShutdown,
	}); runErr != nil {
		logger.WithError(runErr).Fatal("Server exited with error")
	}
}

// syncBillingTiersWithStripe reconciles each paid billing tier's Stripe product
// and monthly price against the catalog's base_price. Runs on every startup.
//
// When a tier's base_price changes in the catalog, Stripe prices are immutable,
// so SyncTier creates a new price; this function then deactivates the previous
// one. Existing subscriptions on the old price keep billing at the old rate
// until they are explicitly migrated.
func syncBillingTiersWithStripe(ctx context.Context, db *sql.DB, stripeClient *stripe.Client, logger logging.Logger) error {
	queries := purserdb.New(db)
	tiers, err := queries.ListActivePaidBillingTiersForStripe(ctx)
	if err != nil {
		return fmt.Errorf("query billing tiers: %w", err)
	}

	var changed int
	for _, t := range tiers {
		basePrice, parseErr := decimal.NewFromString(t.BasePrice)
		if parseErr != nil {
			return fmt.Errorf("parse tier %s base_price %q: %w", t.TierName, t.BasePrice, parseErr)
		}
		productID, priceID, err := stripeClient.SyncTier(ctx, t.TierName, t.DisplayName, t.Description, basePrice, t.Currency)
		if err != nil {
			logger.WithError(err).WithField("tier", t.TierName).Error("Failed to sync tier with Stripe")
			continue
		}

		productSame := t.StripeProductID.Valid && t.StripeProductID.String == productID
		priceSame := t.StripePriceIDMonthly.Valid && t.StripePriceIDMonthly.String == priceID
		if productSame && priceSame {
			continue
		}

		oldPriceID := ""
		if t.StripePriceIDMonthly.Valid && t.StripePriceIDMonthly.String != priceID {
			oldPriceID = t.StripePriceIDMonthly.String
		}

		rows, updateErr := queries.UpdateBillingTierStripeIDs(ctx, purserdb.UpdateBillingTierStripeIDsParams{
			StripeProductID:      sql.NullString{String: productID, Valid: true},
			StripePriceIDMonthly: sql.NullString{String: priceID, Valid: true},
			ID:                   t.ID,
		})
		if updateErr != nil || rows != 1 {
			logger.WithError(updateErr).WithFields(map[string]any{"tier": t.TierName, "rows_affected": rows}).Error("Failed to update tier Stripe IDs")
			continue
		}

		if oldPriceID != "" {
			if err := stripeClient.DeactivatePrice(ctx, oldPriceID); err != nil {
				logger.WithError(err).WithFields(map[string]any{
					"tier":         t.TierName,
					"old_price_id": oldPriceID,
				}).Warn("Failed to deactivate old Stripe price; reconcile manually if it remains active")
			}
		}

		logger.WithFields(map[string]any{
			"tier":         t.TierName,
			"product_id":   productID,
			"price_id":     priceID,
			"old_price_id": oldPriceID,
			"base_price":   basePrice,
			"currency":     t.Currency,
		}).Info("Reconciled billing tier with Stripe")
		changed++
	}

	if changed > 0 {
		logger.WithField("count", changed).Info("Stripe tier sync complete")
	}
	return nil
}
