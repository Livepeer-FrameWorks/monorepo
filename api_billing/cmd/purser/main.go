package main

import (
	"context"

	"frameworks/api_billing/internal/handlers"
	qmapi "frameworks/pkg/api/quartermaster"
	"frameworks/pkg/auth"
	qmclient "frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/config"
	"frameworks/pkg/database"
	"frameworks/pkg/logging"
	"frameworks/pkg/monitoring"
	"frameworks/pkg/server"
	"frameworks/pkg/version"
	"time"
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
	quartermasterURL := config.RequireEnv("QUARTERMASTER_URL")

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

	// Create custom billing metrics
	metrics := &handlers.PurserMetrics{
		BillingCalculations: metricsCollector.NewCounter("billing_calculations_total", "Billing calculations performed", []string{"tenant_id", "status"}),
		UsageRecords:        metricsCollector.NewCounter("usage_records_processed_total", "Usage records processed", []string{"usage_type"}),
		InvoiceOperations:   metricsCollector.NewCounter("invoice_operations_total", "Invoice operations", []string{"operation", "status"}),
	}

	// Create database metrics
	metrics.DBQueries, metrics.DBDuration, metrics.DBConnections = metricsCollector.CreateDatabaseMetrics()

	// Initialize handlers
	handlers.Init(db, logger, metrics)

	// Initialize and start JobManager for background billing tasks
	jobManager := handlers.NewJobManager(db, logger)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	jobManager.Start(ctx)
	defer jobManager.Stop()

	logger.Info("JobManager started - background billing jobs active")

	// Setup router with unified monitoring
	router := server.SetupServiceRouter(logger, "purser", healthChecker, metricsCollector)

	// API routes (root level - nginx adds /api/billing/ prefix)
	{
		// Authentication required endpoints
		protected := router.Group("")
		protected.Use(auth.JWTAuthMiddleware([]byte(jwtSecret)))
		{
			// Billing endpoints
			protected.GET("/billing/plans", handlers.GetPlans)
			protected.GET("/billing/invoices", handlers.GetInvoices)
			protected.POST("/billing/pay", handlers.CreatePayment)
			protected.GET("/billing/status", handlers.GetBillingStatus)
		}

		// Webhook endpoints (no auth required)
		router.POST("/webhooks/mollie", handlers.HandleMollieWebhook)
		router.POST("/webhooks/stripe", handlers.HandleStripeWebhook)

		// Usage ingestion endpoints (service-to-service)
		serviceAPI := router.Group("")
		serviceAPI.Use(auth.ServiceAuthMiddleware(serviceToken))
		{
			serviceAPI.POST("/usage/ingest", handlers.IngestUsageData)
			serviceAPI.GET("/usage/ledger/:tenant_id", handlers.GetUsageRecords) // Legacy endpoint name
			serviceAPI.GET("/usage/records", handlers.GetUsageRecords)           // New endpoint
		}
	}

	// Start server with graceful shutdown
	serverConfig := server.DefaultConfig("purser", "18003")
	if err := server.Start(serverConfig, router, logger); err != nil {
		logger.WithError(err).Fatal("Server startup failed")
	}

	// Best-effort service registration in Quartermaster
	go func() {
		qc := qmclient.NewClient(qmclient.Config{BaseURL: quartermasterURL, ServiceToken: serviceToken, Logger: logger})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := qc.BootstrapService(ctx, &qmapi.BootstrapServiceRequest{Type: "purser", Version: version.Version, Protocol: "http", HealthEndpoint: func() *string { s := "/health"; return &s }(), Port: 18003}); err != nil {
			logger.WithError(err).Warn("Quartermaster bootstrap (purser) failed")
		} else {
			logger.Info("Quartermaster bootstrap (purser) ok")
		}
	}()
}
