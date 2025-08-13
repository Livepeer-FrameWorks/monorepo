package main

import (
	"context"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"frameworks/api_analytics_query/internal/handlers"
	"frameworks/api_analytics_query/internal/scheduler"
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
	logger := logging.NewLoggerWithService("periscope-query")

	// Load environment variables
	config.LoadEnv(logger)

	logger.Info("Starting Periscope-Query (Analytics Query API)")

	// Database configuration
	dbConfig := database.DefaultConfig()
	dbConfig.URL = config.GetEnv("DATABASE_URL", "postgres://frameworks_user:frameworks_dev@localhost:5432/frameworks?sslmode=disable")
	yugaDB := database.MustConnect(dbConfig, logger)
	defer yugaDB.Close()

	// Connect to ClickHouse
	chConfig := database.DefaultClickHouseConfig()
	chConfig.Addr = []string{config.GetEnv("CLICKHOUSE_HOST", "localhost:9000")}
	chConfig.Database = config.GetEnv("CLICKHOUSE_DB", "frameworks")
	chConfig.Username = config.GetEnv("CLICKHOUSE_USER", "frameworks")
	chConfig.Password = config.GetEnv("CLICKHOUSE_PASSWORD", "frameworks_dev")
	clickhouse := database.MustConnectClickHouse(chConfig, logger)
	defer clickhouse.Close()

	// Initialize handlers with both databases
	handlers.Init(yugaDB, clickhouse, logger)

	// Setup monitoring
	healthChecker := monitoring.NewHealthChecker("periscope-query", version.Version)
	metricsCollector := monitoring.NewMetricsCollector("periscope-query", version.Version, version.GitCommit)

	// Add health checks
	healthChecker.AddCheck("postgres", monitoring.DatabaseHealthCheck(yugaDB))
	healthChecker.AddCheck("clickhouse", monitoring.DatabaseHealthCheck(clickhouse))
	healthChecker.AddCheck("config", monitoring.ConfigurationHealthCheck(map[string]string{
		"DATABASE_URL":    config.GetEnv("DATABASE_URL", ""),
		"CLICKHOUSE_HOST": config.GetEnv("CLICKHOUSE_HOST", ""),
		"CLICKHOUSE_DB":   config.GetEnv("CLICKHOUSE_DB", ""),
		"JWT_SECRET":      config.GetEnv("JWT_SECRET", ""),
	}))

	// Create analytics metrics
	analyticsQueries, analyticsOperations, analyticsLatency := metricsCollector.CreateBusinessMetrics()
	pgQueries, pgDuration, pgConnections := metricsCollector.CreateDatabaseMetrics()

	// TODO: Wire these metrics into handlers
	_ = analyticsQueries
	_ = analyticsOperations
	_ = analyticsLatency
	_ = pgQueries
	_ = pgDuration
	_ = pgConnections

	// Initialize and start scheduler for billing summarization
	taskScheduler := scheduler.NewScheduler(yugaDB, clickhouse, logger)
	taskScheduler.Start()
	defer taskScheduler.Stop()

	// Setup Gin
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

	// API routes with authentication
	v1 := router.Group("/api/v1")
	v1.Use(auth.JWTAuthMiddleware([]byte(config.GetEnv("JWT_SECRET", ""))))
	{
		// Stream analytics endpoints
		streams := v1.Group("/analytics/streams")
		{
			streams.GET("", handlers.GetStreamAnalytics)
			streams.GET("/:internal_name", handlers.GetStreamDetails)
			streams.GET("/:internal_name/events", handlers.GetStreamEvents)
			streams.GET("/:internal_name/viewers", handlers.GetViewerStats)
			streams.GET("/:internal_name/track-list", handlers.GetTrackListEvents)
			streams.GET("/:internal_name/buffer", handlers.GetStreamBufferEvents)
			streams.GET("/:internal_name/end", handlers.GetStreamEndEvents)
		}

		// Time-series analytics endpoints
		v1.GET("/analytics/viewer-metrics", handlers.GetViewerMetrics)
		v1.GET("/analytics/connection-events", handlers.GetConnectionEvents)
		v1.GET("/analytics/node-metrics", handlers.GetNodeMetrics)
		v1.GET("/analytics/routing-events", handlers.GetRoutingEvents)
		v1.GET("/analytics/stream-health", handlers.GetStreamHealthMetrics)

		// Aggregated analytics endpoints
		v1.GET("/analytics/viewer-metrics/5m", handlers.GetViewerMetrics5m)
		v1.GET("/analytics/node-metrics/1h", handlers.GetNodeMetrics1h)

		// Platform analytics endpoints
		platform := v1.Group("/analytics/platform")
		{
			platform.GET("/overview", handlers.GetPlatformOverview)
			platform.GET("/metrics", handlers.GetPlatformMetrics)
			platform.GET("/events", handlers.GetPlatformEvents)
		}

		// Realtime analytics endpoints
		realtime := v1.Group("/analytics/realtime")
		{
			realtime.GET("/streams", handlers.GetRealtimeStreams)
			realtime.GET("/viewers", handlers.GetRealtimeViewers)
			realtime.GET("/events", handlers.GetRealtimeEvents)
		}

		// Usage endpoints
		usage := v1.Group("/usage")
		{
			usage.GET("/summary", handlers.GetUsageSummary)
			usage.POST("/trigger-hourly", handlers.TriggerHourlySummary)
			usage.POST("/trigger-daily", handlers.TriggerDailySummary)
		}
	}

	// Start HTTP server
	port := config.GetEnv("PORT", "18004")
	srv := &http.Server{
		Addr:    ":" + port,
		Handler: router,
	}

	// Graceful shutdown
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.WithError(err).Fatal("Failed to start server")
		}
	}()

	// Wait for interrupt signal to gracefully shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	logger.Info("Shutting down server...")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		logger.WithError(err).Fatal("Server forced to shutdown")
	}

	logger.Info("Server exiting")
}
