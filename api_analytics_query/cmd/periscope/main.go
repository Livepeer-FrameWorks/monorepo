package main

import (
	"frameworks/api_analytics_query/internal/handlers"
	"frameworks/api_analytics_query/internal/scheduler"
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

	// Setup router with unified monitoring
	router := server.SetupServiceRouter(logger, "periscope-query", healthChecker, metricsCollector)

	// API routes (root level - nginx adds /api/analytics/ prefix)
	{
		// All routes require authentication
		protected := router.Group("")
		protected.Use(auth.JWTAuthMiddleware([]byte(config.GetEnv("JWT_SECRET", ""))))
		{
			// Stream analytics endpoints
			streams := protected.Group("/analytics/streams")
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
			protected.GET("/analytics/viewer-metrics", handlers.GetViewerMetrics)
			protected.GET("/analytics/connection-events", handlers.GetConnectionEvents)
			protected.GET("/analytics/node-metrics", handlers.GetNodeMetrics)
			protected.GET("/analytics/routing-events", handlers.GetRoutingEvents)
			protected.GET("/analytics/stream-health", handlers.GetStreamHealthMetrics)

			// Aggregated analytics endpoints
			protected.GET("/analytics/viewer-metrics/5m", handlers.GetViewerMetrics5m)
			protected.GET("/analytics/node-metrics/1h", handlers.GetNodeMetrics1h)

			// Platform analytics endpoints
			platform := protected.Group("/analytics/platform")
			{
				platform.GET("/overview", handlers.GetPlatformOverview)
				platform.GET("/metrics", handlers.GetPlatformMetrics)
				platform.GET("/events", handlers.GetPlatformEvents)
			}

			// Realtime analytics endpoints
			realtime := protected.Group("/analytics/realtime")
			{
				realtime.GET("/streams", handlers.GetRealtimeStreams)
				realtime.GET("/viewers", handlers.GetRealtimeViewers)
				realtime.GET("/events", handlers.GetRealtimeEvents)
			}

			// Usage endpoints
			usage := protected.Group("/usage")
			{
				usage.GET("/summary", handlers.GetUsageSummary)
				usage.POST("/trigger-hourly", handlers.TriggerHourlySummary)
				usage.POST("/trigger-daily", handlers.TriggerDailySummary)
			}
		}
	}

	// Start server with graceful shutdown
	serverConfig := server.DefaultConfig("periscope-query", "18004")
	if err := server.Start(serverConfig, router, logger); err != nil {
		logger.WithError(err).Fatal("Server startup failed")
	}
}
