package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"frameworks/api_billing/internal/handlers"
	"frameworks/pkg/auth"
	"frameworks/pkg/config"
	"frameworks/pkg/database"
	"frameworks/pkg/logging"
	"frameworks/pkg/middleware"
	"frameworks/pkg/monitoring"
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

	// Setup Gin router
	if config.GetEnv("GIN_MODE", "") == "release" {
		middleware.SetGinMode("release")
	}

	router := middleware.NewEngine()
	router.Use(middleware.RequestIDMiddleware())
	router.Use(middleware.LoggingMiddleware(logger))
	router.Use(middleware.RecoveryMiddleware(logger))
	router.Use(middleware.CORSMiddleware())

	// Add monitoring middleware
	router.Use(metricsCollector.MetricsMiddleware())

	// Health check endpoint with proper checks
	router.GET("/health", healthChecker.Handler())

	// Metrics endpoint for Prometheus
	router.GET("/metrics", metricsCollector.Handler())

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
	serviceAPI.Use(middleware.ServiceAuthMiddleware(config.GetEnv("SERVICE_TOKEN", "")))
	{
		serviceAPI.POST("/usage/ingest", handlers.IngestUsageData)
		serviceAPI.GET("/usage/ledger/:tenant_id", handlers.GetUsageRecords) // Legacy endpoint name
		serviceAPI.GET("/usage/records", handlers.GetUsageRecords)           // New endpoint
	}

	// Start server
	port := config.GetEnv("PORT", "18003")
	srv := &http.Server{
		Addr:         ":" + port,
		Handler:      router,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// Start server in a goroutine
	go func() {
		logger.WithFields(logging.Fields{
			"port":    port,
			"service": "purser",
		}).Info("Starting HTTP server")

		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.WithError(err).Fatal("Failed to start server")
		}
	}()

	// Wait for interrupt
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("Shutting down Purser...")

	// Graceful shutdown
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		logger.WithError(err).Fatal("Server forced to shutdown")
	}

	logger.Info("Purser stopped")
}
