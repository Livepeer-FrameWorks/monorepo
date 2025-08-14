package main

import (
	"context"

	"frameworks/api_billing/internal/handlers"
	"frameworks/pkg/auth"
	"frameworks/pkg/config"
	"frameworks/pkg/database"
	"frameworks/pkg/logging"
	"frameworks/pkg/monitoring"
	"frameworks/pkg/server"
	"frameworks/pkg/version"
)

func main() {
	// Setup logger
	logger := logging.NewLoggerWithService("purser")

	// Load environment variables
	config.LoadEnv(logger)

	logger.Info("Starting Purser (Billing API)")

	// Connect to database
	dbConfig := database.DefaultConfig()
	dbConfig.URL = config.GetEnv("DATABASE_URL", "")
	db := database.MustConnect(dbConfig, logger)
	defer db.Close()

	// Initialize handlers
	handlers.Init(db, logger)

	// Setup monitoring
	healthChecker := monitoring.NewHealthChecker("purser", version.Version)
	metricsCollector := monitoring.NewMetricsCollector("purser", version.Version, version.GitCommit)

	// Add health checks
	healthChecker.AddCheck("database", monitoring.DatabaseHealthCheck(db))
	healthChecker.AddCheck("config", monitoring.ConfigurationHealthCheck(map[string]string{
		"DATABASE_URL": config.GetEnv("DATABASE_URL", ""),
		"JWT_SECRET":   config.GetEnv("JWT_SECRET", ""),
	}))

	// Create business metrics for billing operations
	activeItems, operations, operationDuration := metricsCollector.CreateBusinessMetrics()
	dbQueries, dbDuration, dbConnections := metricsCollector.CreateDatabaseMetrics()

	// TODO: Wire these metrics into handlers
	_ = activeItems
	_ = operations
	_ = operationDuration
	_ = dbQueries
	_ = dbDuration
	_ = dbConnections

	// Initialize and start JobManager for background billing tasks
	jobManager := handlers.NewJobManager(db, logger)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	jobManager.Start(ctx)
	defer jobManager.Stop()

	logger.Info("JobManager started - background billing jobs active")

	// Setup router with unified monitoring
	router := server.SetupServiceRouter(logger, "purser", healthChecker, metricsCollector)

	// API routes
	api := router.Group("/api/v1")
	{
		// Authentication required endpoints
		protected := api.Group("")
		protected.Use(auth.JWTAuthMiddleware([]byte(config.GetEnv("JWT_SECRET", ""))))
		{
			// Billing endpoints
			protected.GET("/billing/plans", handlers.GetPlans)
			protected.GET("/billing/invoices", handlers.GetInvoices)
			protected.POST("/billing/pay", handlers.CreatePayment)
			protected.GET("/billing/status", handlers.GetBillingStatus)
		}

		// Webhook endpoints (no auth required)
		api.POST("/webhooks/mollie", handlers.HandleMollieWebhook)
		api.POST("/webhooks/stripe", handlers.HandleStripeWebhook)
	}

	// Usage ingestion endpoints (service-to-service)
	serviceAPI := router.Group("/api/v1")
	serviceAPI.Use(auth.ServiceAuthMiddleware(config.GetEnv("SERVICE_TOKEN", "")))
	{
		serviceAPI.POST("/usage/ingest", handlers.IngestUsageData)
		serviceAPI.GET("/usage/ledger/:tenant_id", handlers.GetUsageRecords) // Legacy endpoint name
		serviceAPI.GET("/usage/records", handlers.GetUsageRecords)           // New endpoint
	}

	// Start server with graceful shutdown
	serverConfig := server.DefaultConfig("purser", "18003")
	if err := server.Start(serverConfig, router, logger); err != nil {
		logger.WithError(err).Fatal("Server startup failed")
	}
}
