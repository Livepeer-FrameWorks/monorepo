package main

import (
	"frameworks/api_control/internal/handlers"
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
	logger := logging.NewLoggerWithService("commodore")

	// Load environment variables
	config.LoadEnv(logger)

	logger.Info("Starting Commodore (Control API)")

	// Connect to database
	dbConfig := database.DefaultConfig()
	dbConfig.URL = config.GetEnv("DATABASE_URL", "")
	db := database.MustConnect(dbConfig, logger)
	defer db.Close()

	// Initialize router
	router, err := handlers.NewRouter(db, logger)
	if err != nil {
		logger.WithError(err).Fatal("Failed to create router")
	}

	// Initialize handlers
	handlers.Init(db, logger, router)

	// Setup monitoring
	healthChecker := monitoring.NewHealthChecker("commodore", version.Version)
	metricsCollector := monitoring.NewMetricsCollector("commodore", version.Version, version.GitCommit)

	// Add health checks
	healthChecker.AddCheck("database", monitoring.DatabaseHealthCheck(db))
	healthChecker.AddCheck("config", monitoring.ConfigurationHealthCheck(map[string]string{
		"DATABASE_URL": config.GetEnv("DATABASE_URL", ""),
		"JWT_SECRET":   config.GetEnv("JWT_SECRET", ""),
	}))

	// Create business metrics for streams and operations
	activeStreams, operations, operationDuration := metricsCollector.CreateBusinessMetrics()
	dbQueries, dbDuration, dbConnections := metricsCollector.CreateDatabaseMetrics()

	// TODO: Wire these metrics into handlers
	_ = activeStreams
	_ = operations
	_ = operationDuration
	_ = dbQueries
	_ = dbDuration
	_ = dbConnections

	// Setup router with unified monitoring
	app := server.SetupServiceRouter(logger, "commodore", healthChecker, metricsCollector)

	// API routes
	api := app.Group("/api/v1")
	{
		// Public routes
		api.POST("/register", handlers.Register)
		api.POST("/login", handlers.Login)
		api.GET("/verify", handlers.VerifyEmail)

		// Protected routes
		protected := api.Group("")
		protected.Use(auth.JWTAuthMiddleware([]byte(config.GetEnv("JWT_SECRET", ""))))
		{
			// User profile
			protected.GET("/me", handlers.GetMe)

			// Stream management
			protected.GET("/streams", handlers.GetStreams)
			protected.POST("/streams", handlers.CreateStream)
			protected.GET("/streams/:id", handlers.GetStream)
			protected.DELETE("/streams/:id", handlers.DeleteStream)
			protected.GET("/streams/:id/metrics", handlers.GetStreamMetrics)
			protected.GET("/streams/:id/embed", handlers.GetStreamEmbed)
			protected.POST("/streams/:id/refresh-key", handlers.RefreshStreamKey)

			// Clipping
			protected.POST("/clips", handlers.CreateClip)

			// API tokens
			protected.POST("/developer/tokens", handlers.CreateAPIToken)
			protected.GET("/developer/tokens", handlers.GetAPITokens)
			protected.DELETE("/developer/tokens/:id", handlers.RevokeAPIToken)
		}

		// Webhook endpoints for external services (Helmsman, etc.)
		webhooks := api.Group("")
		webhooks.Use(auth.ServiceAuthMiddleware(config.GetEnv("SERVICE_TOKEN", "")))
		{
			webhooks.POST("/stream-start", handlers.HandleStreamStart)
			webhooks.POST("/stream-status", handlers.HandleStreamStatus)
			webhooks.POST("/recording-status", handlers.HandleRecordingStatus)
			webhooks.POST("/push-status", handlers.HandlePushStatus)
			webhooks.GET("/validate-stream-key/:stream_key", handlers.ValidateStreamKey)
			webhooks.GET("/resolve-playback-id/:playback_id", handlers.ResolvePlaybackID)
			webhooks.GET("/resolve-internal-name/:internal_name", handlers.ResolveInternalName)
		}

		// Stream node discovery (cluster-aware)
		api.GET("/stream-node/:stream_key", handlers.GetStreamNode)

		// Developer API routes (using API token authentication)
		devAPI := api.Group("/api/v1/dev")
		devAPI.Use(auth.APIAuthMiddleware(db))
		{
			devAPI.GET("/streams", handlers.GetStreams)
			devAPI.GET("/streams/:id", handlers.GetStream)
			devAPI.GET("/streams/:id/metrics", handlers.GetStreamMetrics)
			devAPI.POST("/clips", handlers.CreateClip)
		}

		// Admin routes
		admin := api.Group("/admin")
		admin.Use(auth.JWTAuthMiddleware([]byte(config.GetEnv("JWT_SECRET", ""))))
		{
			admin.GET("/users", handlers.GetUsers)
			admin.GET("/streams", handlers.GetAllStreams)
			admin.POST("/streams/:id/terminate", handlers.TerminateStream)
		}
	}

	// Start server with graceful shutdown
	serverConfig := server.DefaultConfig("commodore", "18001")
	if err := server.Start(serverConfig, app, logger); err != nil {
		logger.WithError(err).Fatal("Server startup failed")
	}
}
