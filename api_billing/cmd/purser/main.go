package main

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"time"

	pursergrpc "frameworks/api_billing/internal/grpc"
	"frameworks/api_billing/internal/handlers"
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
		BillingCalculations: metricsCollector.NewCounter("billing_calculations_total", "Billing calculations performed", []string{"tenant_id", "status"}),
		UsageRecords:        metricsCollector.NewCounter("usage_records_processed_total", "Usage records processed", []string{"usage_type"}),
		InvoiceOperations:   metricsCollector.NewCounter("invoice_operations_total", "Invoice operations", []string{"operation", "status"}),
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

	// Initialize handlers
	handlers.Init(db, logger, handlerMetrics, qmGRPCClient)

	// Initialize and start JobManager for background billing tasks
	jobManager := handlers.NewJobManager(db, logger)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	jobManager.Start(ctx)
	defer jobManager.Stop()

	logger.Info("JobManager started - background billing jobs active")

	// Setup router with unified monitoring (health/metrics only)
	// NOTE: All billing/usage API routes removed - now handled via gRPC only.
	// Gateway -> Purser gRPC for billing, tiers, invoices, payments, usage queries.
	// Usage ingestion is via Kafka (Periscope -> billing.usage_reports -> JobManager)
	router := server.SetupServiceRouter(logger, "purser", healthChecker, metricsCollector)

	// Webhook endpoints for external payment providers (no auth - signature validation in handler)
	// These MUST remain as HTTP endpoints since Stripe/Mollie send webhooks to URLs.
	router.POST("/webhooks/mollie", handlers.HandleMollieWebhook)
	router.POST("/webhooks/stripe", handlers.HandleStripeWebhook)

	// Start gRPC server in a goroutine
	grpcPort := config.GetEnv("GRPC_PORT", "19003")
	go func() {
		grpcAddr := fmt.Sprintf(":%s", grpcPort)
		lis, err := net.Listen("tcp", grpcAddr)
		if err != nil {
			logger.WithError(err).Fatal("Failed to listen on gRPC port")
		}

		grpcServer := pursergrpc.NewGRPCServer(pursergrpc.GRPCServerConfig{
			DB:           db,
			Logger:       logger,
			ServiceToken: serviceToken,
			JWTSecret:    []byte(jwtSecret),
			Metrics:      serverMetrics,
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
			ClusterId:      func() *string { if clusterID != "" { return &clusterID }; return nil }(),
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
