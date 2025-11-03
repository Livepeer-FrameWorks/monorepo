package main

import (
	"context"
	"frameworks/api_analytics_query/internal/handlers"
	"frameworks/api_analytics_query/internal/metrics"
	"frameworks/api_analytics_query/internal/scheduler"
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
	logger := logging.NewLoggerWithService("periscope-query")

	// Load environment variables
	config.LoadEnv(logger)

	logger.Info("Starting Periscope-Query (Analytics Query API)")

	dbURL := config.RequireEnv("DATABASE_URL")
	clickhouseHost := config.RequireEnv("CLICKHOUSE_HOST")
	clickhouseDB := config.RequireEnv("CLICKHOUSE_DB")
	clickhouseUser := config.RequireEnv("CLICKHOUSE_USER")
	clickhousePassword := config.RequireEnv("CLICKHOUSE_PASSWORD")
	jwtSecret := config.RequireEnv("JWT_SECRET")
	serviceToken := config.RequireEnv("SERVICE_TOKEN")
	quartermasterURL := config.RequireEnv("QUARTERMASTER_URL")

	// Database configuration
	dbConfig := database.DefaultConfig()
	dbConfig.URL = dbURL
	yugaDB := database.MustConnect(dbConfig, logger)
	defer yugaDB.Close()

	// Connect to ClickHouse
	chConfig := database.DefaultClickHouseConfig()
	chConfig.Addr = []string{clickhouseHost}
	chConfig.Database = clickhouseDB
	chConfig.Username = clickhouseUser
	chConfig.Password = clickhousePassword
	clickhouse := database.MustConnectClickHouse(chConfig, logger)
	defer clickhouse.Close()

	// Setup monitoring
	healthChecker := monitoring.NewHealthChecker("periscope-query", version.Version)
	metricsCollector := monitoring.NewMetricsCollector("periscope-query", version.Version, version.GitCommit)

	// Add health checks
	healthChecker.AddCheck("postgres", monitoring.DatabaseHealthCheck(yugaDB))
	healthChecker.AddCheck("clickhouse", monitoring.DatabaseHealthCheck(clickhouse))
	healthChecker.AddCheck("config", monitoring.ConfigurationHealthCheck(map[string]string{
		"DATABASE_URL":    dbURL,
		"CLICKHOUSE_HOST": clickhouseHost,
		"CLICKHOUSE_DB":   clickhouseDB,
		"JWT_SECRET":      jwtSecret,
	}))

	// Create custom analytics query metrics
	serviceMetrics := &metrics.Metrics{
		AnalyticsQueries:  metricsCollector.NewCounter("analytics_queries_total", "Analytics queries executed", []string{"query_type", "status"}),
		QueryDuration:     metricsCollector.NewHistogram("analytics_query_duration_seconds", "Analytics query duration", []string{"query_type"}, nil),
		ClickHouseQueries: metricsCollector.NewCounter("clickhouse_queries_total", "ClickHouse queries executed", []string{"table", "status"}),
		PostgresQueries:   metricsCollector.NewCounter("postgres_queries_total", "PostgreSQL queries executed", []string{"table", "status"}),
	}

	// Create database metrics
	serviceMetrics.PostgresQueries, serviceMetrics.DBDuration, serviceMetrics.DBConnections = metricsCollector.CreateDatabaseMetrics()

	// Initialize handlers with unified metrics
	handlers.Init(yugaDB, clickhouse, logger, serviceMetrics)

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
		protected.Use(auth.JWTAuthMiddleware([]byte(jwtSecret)))
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
			protected.GET("/analytics/clip-events", handlers.GetClipEvents)
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

	// Best-effort service registration in Quartermaster
	go func() {
		qc := qmclient.NewClient(qmclient.Config{BaseURL: quartermasterURL, ServiceToken: serviceToken, Logger: logger})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := qc.BootstrapService(ctx, &qmapi.BootstrapServiceRequest{Type: "periscope_query", Version: version.Version, Protocol: "http", HealthEndpoint: func() *string { s := "/health"; return &s }(), Port: 18004}); err != nil {
			logger.WithError(err).Warn("Quartermaster bootstrap (periscope_query) failed")
		} else {
			logger.Info("Quartermaster bootstrap (periscope_query) ok")
		}
	}()
}
