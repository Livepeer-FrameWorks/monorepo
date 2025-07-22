package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"frameworks/pkg/auth"
	"frameworks/pkg/config"
	"frameworks/pkg/database"
	"frameworks/pkg/logging"
	"frameworks/pkg/middleware"
	"frameworks/purser/internal/handlers"
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

	// Health check endpoint
	router.GET("/health", func(c middleware.Context) {
		c.JSON(200, middleware.H{
			"status":  "healthy",
			"service": "purser",
			"version": config.GetEnv("VERSION", "1.0.0"),
		})
	})

	// Basic metrics endpoint for Prometheus
	router.GET("/metrics", func(c middleware.Context) {
		c.Header("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		c.String(200, "# HELP purser_up Service availability\n# TYPE purser_up gauge\npurser_up 1\n")
	})

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
