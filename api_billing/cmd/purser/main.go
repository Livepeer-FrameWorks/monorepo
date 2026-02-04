package main

import (
	"context"
	"fmt"
	"net"
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
	"frameworks/pkg/server"
	"frameworks/pkg/version"
)

func main() {
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
	periscopeGRPCAddr := config.GetEnv("PERISCOPE_GRPC_ADDR", "periscope:19004")

	// Payment provider credentials (optional - service works without them)
	stripeSecretKey := config.GetEnv("STRIPE_SECRET_KEY", "")
	stripeWebhookSecret := config.GetEnv("STRIPE_WEBHOOK_SECRET", "")
	mollieAPIKey := config.GetEnv("MOLLIE_API_KEY", "")
	mollieWebhookSecret := config.GetEnv("MOLLIE_WEBHOOK_SECRET", "")

	// Connect to database
	dbConfig := database.DefaultConfig()
	dbConfig.URL = dbURL
	db := database.MustConnect(dbConfig, logger)
	defer db.Close()

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
		GRPCAddr:     quartermasterGRPCAddr,
		Timeout:      10 * time.Second,
		Logger:       logger,
		ServiceToken: serviceToken,
	})
	if err != nil {
		logger.WithError(err).Fatal("Failed to create Quartermaster gRPC client")
	}
	defer qmGRPCClient.Close()

	// Create Commodore gRPC client for stream termination on suspension
	commodoreClient, err := commodoreclnt.NewGRPCClient(commodoreclnt.GRPCConfig{
		GRPCAddr:     commodoreGRPCAddr,
		Timeout:      30 * time.Second,
		Logger:       logger,
		ServiceToken: serviceToken,
	})
	if err != nil {
		logger.WithError(err).Fatal("Failed to create Commodore gRPC client")
	}
	defer commodoreClient.Close()

	// Create Decklog gRPC client for service events
	decklogGRPCAddr := config.GetEnv("DECKLOG_GRPC_ADDR", "decklog:18006")
	decklogClient, err := decklogclient.NewBatchedClient(decklogclient.BatchedClientConfig{
		Target:        decklogGRPCAddr,
		AllowInsecure: config.GetEnvBool("DECKLOG_ALLOW_INSECURE", true),
		Timeout:       5 * time.Second,
		Source:        "purser",
		ServiceToken:  serviceToken,
	}, logger)
	if err != nil {
		logger.WithError(err).Warn("Failed to create Decklog gRPC client - service events will be disabled")
		decklogClient = nil
	} else {
		defer decklogClient.Close()
		logger.WithField("addr", decklogGRPCAddr).Info("Connected to Decklog gRPC")
	}

	// Create Periscope gRPC client for invoice enrichment (accurate unique counts, geo breakdown)
	periscopeClient, err := periscopeclient.NewGRPCClient(periscopeclient.GRPCConfig{
		GRPCAddr:     periscopeGRPCAddr,
		Timeout:      30 * time.Second,
		Logger:       logger,
		ServiceToken: serviceToken,
	})
	if err != nil {
		logger.WithError(err).Warn("Failed to create Periscope gRPC client - invoice enrichment will be disabled")
		periscopeClient = nil
	} else {
		defer periscopeClient.Close()
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
	} else {
		logger.Warn("STRIPE_SECRET_KEY not set - Stripe functionality disabled")
	}

	// Create Mollie client (optional - service works without it)
	var mollieClient *mollie.Client
	if mollieAPIKey != "" {
		var err error
		mollieClient, err = mollie.NewClient(mollie.Config{
			APIKey:        mollieAPIKey,
			WebhookSecret: mollieWebhookSecret,
			Logger:        logger,
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

	// Setup router with unified monitoring (health/metrics only)
	// NOTE: All billing/usage API routes removed - now handled via gRPC only.
	// Gateway -> Purser gRPC for billing, tiers, invoices, payments, usage queries.
	// Usage ingestion is via Kafka (Periscope -> billing.usage_reports -> JobManager)
	// Webhooks are now routed through Gateway -> gRPC -> ProcessWebhook (keeps Purser internal)
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
			GRPCAddr:     quartermasterGRPCAddr,
			Timeout:      10 * time.Second,
			Logger:       logger,
			ServiceToken: serviceToken,
		})
		if err != nil {
			logger.WithError(err).Warn("Failed to create Quartermaster gRPC client")
			return
		}
		defer qc.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		healthEndpoint := "/health"
		httpPort, _ := strconv.Atoi(serverConfig.Port)
		advertiseHost := config.GetEnv("PURSER_HOST", "purser")
		clusterID := config.GetEnv("CLUSTER_ID", "")
		if _, err := qc.BootstrapService(ctx, &pb.BootstrapServiceRequest{
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
		}); err != nil {
			logger.WithError(err).Warn("Quartermaster bootstrap (purser) failed")
		} else {
			logger.Info("Quartermaster bootstrap (purser) ok")
		}
	}()

	if err := server.Start(serverConfig, router, logger); err != nil {
		logger.WithError(err).Fatal("Server startup failed")
	}
}
