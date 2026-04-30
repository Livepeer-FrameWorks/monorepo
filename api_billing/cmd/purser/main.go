package main

import (
	"context"
	"database/sql"
	"fmt"
	"net"
	"os"
	"strconv"
	"time"

	pursergrpc "frameworks/api_billing/internal/grpc"
	"frameworks/api_billing/internal/handlers"
	"frameworks/api_billing/internal/mollie"
	"frameworks/api_billing/internal/stripe"
	commodoreclnt "frameworks/pkg/clients/commodore"
	decklogclient "frameworks/pkg/clients/decklog"
	periscopeclient "frameworks/pkg/clients/periscope"
	qmclient "frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/config"
	"frameworks/pkg/database"
	"frameworks/pkg/logging"
	"frameworks/pkg/monitoring"
	pb "frameworks/pkg/proto"
	"frameworks/pkg/qmbootstrap"
	"frameworks/pkg/server"
	"frameworks/pkg/version"

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

	// Create custom billing metrics for HTTP handlers
	handlerMetrics := &handlers.PurserMetrics{
		BillingCalculations:      metricsCollector.NewCounter("billing_calculations_total", "Billing calculations performed", []string{"tenant_id", "status"}),
		UsageRecords:             metricsCollector.NewCounter("usage_records_processed_total", "Usage records processed", []string{"usage_type"}),
		InvoiceOperations:        metricsCollector.NewCounter("invoice_operations_total", "Invoice operations", []string{"operation", "status"}),
		WebhookSignatureFailures: metricsCollector.NewCounter("webhook_signature_failures_total", "Webhook signature validation failures", []string{"provider"}),
	}

	// Create database metrics
	handlerMetrics.DBQueries, handlerMetrics.DBDuration, handlerMetrics.DBConnections = metricsCollector.CreateDatabaseMetrics()

	// Create gRPC server metrics
	serverMetrics := &pursergrpc.ServerMetrics{
		BillingOperations:      metricsCollector.NewCounter("grpc_billing_operations_total", "gRPC billing operations", []string{"operation", "status"}),
		UsageOperations:        metricsCollector.NewCounter("grpc_usage_operations_total", "gRPC usage operations", []string{"operation", "status"}),
		SubscriptionOperations: metricsCollector.NewCounter("grpc_subscription_operations_total", "gRPC subscription operations", []string{"operation", "status"}),
		InvoiceOperations:      metricsCollector.NewCounter("grpc_invoice_operations_total", "gRPC invoice operations", []string{"operation", "status"}),
		GRPCRequests:           metricsCollector.NewCounter("grpc_requests_total", "Total gRPC requests", []string{"method", "status"}),
		GRPCDuration:           metricsCollector.NewHistogram("grpc_request_duration_seconds", "gRPC request duration", []string{"method"}, nil),
	}

	// Create Quartermaster gRPC client for tenant lookups (used by webhooks)
	qmGRPCClient, err := qmclient.NewGRPCClient(qmclient.GRPCConfig{
		GRPCAddr:      quartermasterGRPCAddr,
		Timeout:       10 * time.Second,
		Logger:        logger,
		ServiceToken:  serviceToken,
		AllowInsecure: config.GetEnvBool("GRPC_ALLOW_INSECURE", false),
		CACertFile:    config.GetEnv("GRPC_TLS_CA_PATH", ""),
		ServerName:    config.GetEnv("GRPC_TLS_SERVER_NAME", ""),
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
		ServerName:    config.GetEnv("GRPC_TLS_SERVER_NAME", ""),
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
		ServerName:    config.GetEnv("GRPC_TLS_SERVER_NAME", ""),
		Timeout:       5 * time.Second,
		Source:        "purser",
		ServiceToken:  serviceToken,
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
		ServerName:    config.GetEnv("GRPC_TLS_SERVER_NAME", ""),
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

	// Initialize handlers
	handlers.Init(db, logger, handlerMetrics, qmGRPCClient, mollieClient, decklogClient)

	// Initialize and start JobManager for background billing tasks
	jobManager := handlers.NewJobManager(db, logger, commodoreClient, decklogClient, periscopeClient)
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
			ServerName:    config.GetEnv("GRPC_TLS_SERVER_NAME", ""),
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
		req := &pb.BootstrapServiceRequest{
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
	rows, err := db.QueryContext(ctx, `
		SELECT id, tier_name, display_name, description, base_price::text, currency,
		       stripe_product_id, stripe_price_id_monthly
		FROM purser.billing_tiers
		WHERE base_price > 0 AND is_active = true
	`)
	if err != nil {
		return fmt.Errorf("query billing tiers: %w", err)
	}
	defer rows.Close()

	type tier struct {
		id                 string
		tierName           string
		displayName        string
		description        string
		basePrice          decimal.Decimal
		currency           string
		stripeProductID    sql.NullString
		stripePriceMonthly sql.NullString
	}

	var tiers []tier
	for rows.Next() {
		var t tier
		var basePriceText string
		if err := rows.Scan(&t.id, &t.tierName, &t.displayName, &t.description,
			&basePriceText, &t.currency, &t.stripeProductID, &t.stripePriceMonthly); err != nil {
			return fmt.Errorf("scan tier: %w", err)
		}
		t.basePrice, err = decimal.NewFromString(basePriceText)
		if err != nil {
			return fmt.Errorf("parse tier %s base_price %q: %w", t.tierName, basePriceText, err)
		}
		tiers = append(tiers, t)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate tiers: %w", err)
	}

	var changed int
	for _, t := range tiers {
		productID, priceID, err := stripeClient.SyncTier(ctx, t.tierName, t.displayName, t.description, t.basePrice, t.currency)
		if err != nil {
			logger.WithError(err).WithField("tier", t.tierName).Error("Failed to sync tier with Stripe")
			continue
		}

		productSame := t.stripeProductID.Valid && t.stripeProductID.String == productID
		priceSame := t.stripePriceMonthly.Valid && t.stripePriceMonthly.String == priceID
		if productSame && priceSame {
			continue
		}

		oldPriceID := ""
		if t.stripePriceMonthly.Valid && t.stripePriceMonthly.String != priceID {
			oldPriceID = t.stripePriceMonthly.String
		}

		if _, err := db.ExecContext(ctx, `
			UPDATE purser.billing_tiers
			SET stripe_product_id = $1, stripe_price_id_monthly = $2, updated_at = NOW()
			WHERE id = $3
		`, productID, priceID, t.id); err != nil {
			logger.WithError(err).WithField("tier", t.tierName).Error("Failed to update tier Stripe IDs")
			continue
		}

		if oldPriceID != "" {
			if err := stripeClient.DeactivatePrice(ctx, oldPriceID); err != nil {
				logger.WithError(err).WithFields(map[string]any{
					"tier":         t.tierName,
					"old_price_id": oldPriceID,
				}).Warn("Failed to deactivate old Stripe price; reconcile manually if it remains active")
			}
		}

		logger.WithFields(map[string]any{
			"tier":         t.tierName,
			"product_id":   productID,
			"price_id":     priceID,
			"old_price_id": oldPriceID,
			"base_price":   t.basePrice,
			"currency":     t.currency,
		}).Info("Reconciled billing tier with Stripe")
		changed++
	}

	if changed > 0 {
		logger.WithField("count", changed).Info("Stripe tier sync complete")
	}
	return nil
}
