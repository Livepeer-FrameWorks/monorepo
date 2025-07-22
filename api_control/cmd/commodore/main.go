package main

import (
	"frameworks/api_control/internal/handlers"
	"frameworks/pkg/auth"
	"frameworks/pkg/config"
	"frameworks/pkg/database"
	"frameworks/pkg/logging"
	"frameworks/pkg/middleware"
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

	// Create Gin engine
	app := middleware.NewEngine()
	middleware.SetupCommonMiddleware(app, logger)

	// Health check endpoint
	app.GET("/health", func(c middleware.Context) {
		c.JSON(200, middleware.H{
			"status":  "ok",
			"service": "commodore",
			"version": config.GetEnv("VERSION", "1.0.0"),
		})
	})

	// Basic metrics endpoint for Prometheus
	app.GET("/metrics", func(c middleware.Context) {
		c.Header("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		c.String(200, "# HELP commodore_up Service availability\n# TYPE commodore_up gauge\ncommodore_up 1\n")
	})

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

			// Stream validation (used by MistServer triggers)
			protected.GET("/validate-stream-key/:stream_key", handlers.ValidateStreamKey)

			// Clipping
			protected.POST("/clips", handlers.CreateClip)

			// API tokens
			protected.POST("/developer/tokens", handlers.CreateAPIToken)
			protected.GET("/developer/tokens", handlers.GetAPITokens)
			protected.DELETE("/developer/tokens/:id", handlers.RevokeAPIToken)
		}

		// Webhook endpoints for external services (Helmsman, etc.)
		webhooks := api.Group("")
		webhooks.Use(middleware.ServiceAuthMiddleware(config.GetEnv("SERVICE_TOKEN", "")))
		{
			webhooks.POST("/stream-start", handlers.HandleStreamStart)
			webhooks.POST("/stream-status", handlers.HandleStreamStatus)
			webhooks.POST("/recording-status", handlers.HandleRecordingStatus)
			webhooks.POST("/push-status", handlers.HandlePushStatus)
			webhooks.GET("/resolve-playback-id/:playback_id", handlers.ResolvePlaybackID)
			webhooks.GET("/resolve-internal-name/:internal_name", handlers.ResolveInternalName)
		}

		// Stream node discovery (cluster-aware)
		api.GET("/stream-node/:stream_key", handlers.GetStreamNode)

		// Admin routes
		admin := api.Group("/admin")
		admin.Use(auth.JWTAuthMiddleware([]byte(config.GetEnv("JWT_SECRET", ""))))
		{
			admin.GET("/users", handlers.GetUsers)
			admin.GET("/streams", handlers.GetAllStreams)
			admin.POST("/streams/:id/terminate", handlers.TerminateStream)
		}
	}

	// Start server
	port := config.GetEnv("PORT", "18001")
	logger.WithFields(logging.Fields{
		"port": port,
	}).Info("Starting server")

	if err := app.Run(":" + port); err != nil {
		logger.WithError(err).Fatal("Server failed")
	}
}
