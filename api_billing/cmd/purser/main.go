package main

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	"frameworks/api_billing/internal/database/purserdb"
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
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/monitoring"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/qmbootstrap"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/server"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/version"

	"github.com/shopspring/decimal"
)

func main() {
	if version.HandleCLI() {
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

	dbURL := config.RequireEnv("DATABASE_URL")
	jwtSecret := config.RequireEnv("JWT_SECRET")
	serviceToken := config.RequireEnv("SERVICE_TOKEN")
	quartermasterGRPCAddr := config.GetEnv("QUARTERMASTER_GRPC_ADDR", "quartermaster:19002")
	commodoreGRPCAddr := config.GetEnv("COMMODORE_GRPC_ADDR", "commodore:19001")
	periscopeGRPCAddr := config.GetEnv("PERISCOPE_GRPC_ADDR", "periscope-query:19004")

	// Payment provider credentials (optional - service works without them)
	stripeSecretKey := config.GetEnv("STRIPE_SECRET_KEY", "")
	stripeWebhookSecret := config.GetEnv("STRIPE_WEBHOOK_SECRET", "")
	mollieAPIKey := config.GetEnv("MOLLIE_API_KEY", "")

	// Connect to database
	dbConfig := database.DefaultConfig()
	dbConfig.URL = dbURL
	db := database.MustConnect(dbConfig, logger)
	defer func() { _ = db.Close() }()

	// Setup monitoring
	healthChecker := monitoring.NewHealthChecker("purser", version.Version)
	metricsCollector := monitoring.NewMetricsCollector("purser", version.Version, version.GitCommit)

	// Add health checks
	healthChecker.AddCheck("database", monitoring.DatabaseHealthCheck(db))
	healthChecker.AddCheck("config", monitoring.ConfigurationHealthCheck(map[string]string{
		"DATABASE_URL": dbURL,
		"JWT_SECRET":   jwtSecret,
	}))

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
		CryptoScannerErrors:       metricsCollector.NewCounter("crypto_scanner_errors_total", "Crypto scanner errors", []string{"network"}),
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
		GRPCRequests: metricsCollector.NewCounter("grpc_requests_total", "Total gRPC requests", []string{"method", "status"}),
		GRPCDuration: metricsCollector.NewHistogram("grpc_request_duration_seconds", "gRPC request duration", []string{"method"}, nil),
	}

	// Create Quartermaster gRPC client for tenant lookups (used by webhooks)
	qmGRPCClient, err := qmclient.NewGRPCClient(qmclient.GRPCConfig{
		GRPCAddr:           quartermasterGRPCAddr,
		Timeout:            10 * time.Second,
		Logger:             logger,
		ServiceToken:       serviceToken,
		PreferServiceToken: true,
		AllowInsecure:      config.GetEnvBool("GRPC_ALLOW_INSECURE", false),
		CACertFile:         config.GetEnv("GRPC_TLS_CA_PATH", ""),
		ServerName:         config.GetServiceGRPCTLSServerName("quartermaster"),
	})
	if err != nil {
		logger.WithError(err).Fatal("Failed to create Quartermaster gRPC client")
	}
	defer func() { _ = qmGRPCClient.Close() }()

	// Create Commodore gRPC client for stream termination on suspension
	commodoreClient, err := commodoreclnt.NewGRPCClient(commodoreclnt.GRPCConfig{
		GRPCAddr:      commodoreGRPCAddr,
		Timeout:       30 * time.Second,
		Logger:        logger,
		ServiceToken:  serviceToken,
		AllowInsecure: config.GetEnvBool("GRPC_ALLOW_INSECURE", false),
		CACertFile:    config.GetEnv("GRPC_TLS_CA_PATH", ""),
		ServerName:    config.GetServiceGRPCTLSServerName("commodore"),
	})
	if err != nil {
		logger.WithError(err).Fatal("Failed to create Commodore gRPC client")
	}
	defer func() { _ = commodoreClient.Close() }()

	// Create Decklog gRPC client for service events
	decklogGRPCAddr := config.GetEnv("DECKLOG_GRPC_ADDR", "decklog:18006")
	decklogClient, err := decklogclient.NewBatchedClient(decklogclient.BatchedClientConfig{
		Target:        decklogGRPCAddr,
		AllowInsecure: config.GetEnvBool("GRPC_ALLOW_INSECURE", false),
		CACertFile:    config.GetEnv("GRPC_TLS_CA_PATH", ""),
		ServerName:    config.GetServiceGRPCTLSServerName("decklog"),
		Timeout:       5 * time.Second,
		Source:        "purser",
		ServiceToken:  serviceToken,
		ClusterID:     config.GetEnv("CLUSTER_ID", ""),
		SourceRegion:  config.GetEnv("REGION", ""),
	}, logger)
	if err != nil {
		logger.WithError(err).Warn("Failed to create Decklog gRPC client - service events will be disabled")
		decklogClient = nil
	} else {
		defer func() { _ = decklogClient.Close() }()
		logger.WithField("addr", decklogGRPCAddr).Info("Connected to Decklog gRPC")
	}

	// Create Periscope gRPC client for invoice enrichment (accurate unique counts, geo breakdown)
	periscopeClient, err := periscopeclient.NewGRPCClient(periscopeclient.GRPCConfig{
		GRPCAddr:      periscopeGRPCAddr,
		Timeout:       30 * time.Second,
		Logger:        logger,
		ServiceToken:  serviceToken,
		AllowInsecure: config.GetEnvBool("GRPC_ALLOW_INSECURE", false),
		CACertFile:    config.GetEnv("GRPC_TLS_CA_PATH", ""),
		ServerName:    config.GetServiceGRPCTLSServerName("periscope"),
	})
	if err != nil {
		logger.WithError(err).Warn("Failed to create Periscope gRPC client - invoice enrichment will be disabled")
		periscopeClient = nil
	} else {
		defer func() { _ = periscopeClient.Close() }()
		logger.WithField("addr", periscopeGRPCAddr).Info("Connected to Periscope gRPC")
	}

	// Create Stripe client (optional - service works without it)
	var stripeClient *stripe.Client
	if stripeSecretKey != "" {
		stripeClient = stripe.NewClient(stripe.Config{
			SecretKey:     stripeSecretKey,
			WebhookSecret: stripeWebhookSecret,
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
	if mollieAPIKey != "" {
		var err error
		mollieClient, err = mollie.NewClient(mollie.Config{
			APIKey: mollieAPIKey,
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
	tierReconciler := tieraccess.NewReconciler(db, qmGRPCClient, logger)

	// Initialize and start JobManager for background billing tasks
	jobManager := handlers.NewJobManager(db, logger, commodoreClient, decklogClient, periscopeClient, tierReconciler, billingSvc)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	jobManager.Start(ctx)
	defer jobManager.Stop()

	logger.Info("JobManager started - background billing jobs active")

	// Start Livepeer deposit monitor (optional - requires ARBITRUM_RPC_ENDPOINT)
	if config.GetEnvBool("LIVEPEER_DEPOSIT_MONITOR_ENABLED", false) {
		depositMonitor := handlers.NewLivepeerDepositMonitor(logger, qmGRPCClient)
		go depositMonitor.Start(ctx)
		defer depositMonitor.Stop()
		logger.Info("Livepeer deposit monitor started")
	}

	// Expose health and metrics over HTTP; billing APIs are served over gRPC.
	router := server.SetupServiceRouter(logger, "purser", healthChecker, metricsCollector)

	// Start gRPC server in a goroutine
	grpcPort := config.GetEnv("GRPC_PORT", "19003")
	go func() {
		grpcAddr := fmt.Sprintf(":%s", grpcPort)
		lis, err := net.Listen("tcp", grpcAddr)
		if err != nil {
			logger.WithError(err).Fatal("Failed to listen on gRPC port")
		}

		grpcServer := pursergrpc.NewGRPCServer(pursergrpc.GRPCServerConfig{
			DB:                  db,
			Logger:              logger,
			ServiceToken:        serviceToken,
			JWTSecret:           []byte(jwtSecret),
			Metrics:             serverMetrics,
			StripeClient:        stripeClient,
			MollieClient:        mollieClient,
			QuartermasterClient: qmGRPCClient,
			CommodoreClient:     commodoreClient,
			DecklogClient:       decklogClient,
			Billing:             billingSvc,
			CertFile:            config.GetEnv("GRPC_TLS_CERT_PATH", ""),
			KeyFile:             config.GetEnv("GRPC_TLS_KEY_PATH", ""),
			AllowInsecure:       config.GetEnvBool("GRPC_ALLOW_INSECURE", false),
		})
		logger.WithField("addr", grpcAddr).Info("Starting gRPC server")

		if err := grpcServer.Serve(lis); err != nil {
			logger.WithError(err).Fatal("gRPC server failed")
		}
	}()

	// Start HTTP server with graceful shutdown
	serverConfig := server.DefaultConfig("purser", "18003")

	// Best-effort service registration in Quartermaster (using gRPC)
	// Must be launched BEFORE server.Start() which blocks
	go func() {
		qc, err := qmclient.NewGRPCClient(qmclient.GRPCConfig{
			GRPCAddr:      quartermasterGRPCAddr,
			Timeout:       10 * time.Second,
			Logger:        logger,
			ServiceToken:  serviceToken,
			AllowInsecure: config.GetEnvBool("GRPC_ALLOW_INSECURE", false),
			CACertFile:    config.GetEnv("GRPC_TLS_CA_PATH", ""),
			ServerName:    config.GetServiceGRPCTLSServerName("quartermaster"),
		})
		if err != nil {
			logger.WithError(err).Warn("Failed to create Quartermaster gRPC client")
			return
		}
		defer func() { _ = qc.Close() }()
		healthEndpoint := "/health"
		httpPort, err := strconv.Atoi(serverConfig.Port)
		if err != nil || httpPort <= 0 || httpPort > 65535 {
			logger.WithError(err).WithField("port", serverConfig.Port).Warn("Quartermaster bootstrap skipped: invalid port")
			return
		}
		advertiseHost := config.GetEnv("PURSER_HOST", "purser")
		clusterID := config.GetEnv("CLUSTER_ID", "")
		req := &quartermasterpb.BootstrapServiceRequest{
			Type:           "purser",
			Version:        version.Version,
			Protocol:       "http",
			HealthEndpoint: &healthEndpoint,
			Port:           int32(httpPort),
			AdvertiseHost:  &advertiseHost,
			ClusterId: func() *string {
				if clusterID != "" {
					return &clusterID
				}
				return nil
			}(),
		}
		if nodeID := config.GetEnv("NODE_ID", ""); nodeID != "" {
			req.NodeId = &nodeID
		}
		if _, err := qmbootstrap.BootstrapServiceWithRetry(context.Background(), qc, req, logger, qmbootstrap.DefaultRetryConfig("purser")); err != nil {
			logger.WithError(err).Warn("Quartermaster bootstrap (purser) failed")
		} else {
			logger.Info("Quartermaster bootstrap (purser) ok")
		}
	}()

	server.RegisterEnvFileReload("purser", logger)
	if err := server.Start(serverConfig, router, logger); err != nil {
		logger.WithError(err).Fatal("Server startup failed")
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
