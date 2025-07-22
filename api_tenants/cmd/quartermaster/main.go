package main

import (
	"frameworks/pkg/config"
	"frameworks/pkg/database"
	"frameworks/pkg/logging"
	"frameworks/pkg/middleware"
	"frameworks/pkg/server"
	"frameworks/quartermaster/internal/handlers"
)

func main() {
	// Setup logger
	logger := logging.NewLoggerWithService("quartermaster")

	// Load environment variables
	config.LoadEnv(logger)

	logger.Info("Starting Quartermaster (Tenant Management API)")

	// Connect to database
	dbConfig := database.DefaultConfig()
	dbConfig.URL = config.GetEnv("DATABASE_URL", "")
	db := database.MustConnect(dbConfig, logger)
	defer db.Close()

	// Initialize handlers
	handlers.Init(db, logger)

	// Setup router with common middleware
	router := server.SetupRouterWithService(logger, "quartermaster")

	// Public routes
	public := router.Group("/api")
	{
		public.POST("/tenants/validate", handlers.ValidateTenant)
	}

	// Protected routes (require service token authentication)
	protected := router.Group("/api")
	protected.Use(middleware.ServiceAuthMiddleware(config.GetEnv("SERVICE_TOKEN", "default-service-token")))
	{
		// Tenant management
		protected.POST("/tenants", handlers.CreateTenant)
		protected.GET("/tenants/:id", handlers.GetTenant)
		protected.PUT("/tenants/:id", handlers.UpdateTenant)
		protected.DELETE("/tenants/:id", handlers.DeleteTenant)

		// Cluster management
		protected.GET("/clusters", handlers.GetClusters)
		protected.GET("/clusters/:id", handlers.GetCluster)

		// Service management
		protected.GET("/services", handlers.GetServices)
		protected.GET("/services/:id", handlers.GetService)
		protected.GET("/clusters/:id/services", handlers.GetClusterServices)
		protected.PUT("/clusters/:cluster_id/services/:service_id", handlers.UpdateClusterServiceState)
		protected.GET("/service-instances", handlers.GetServiceInstances)

		// Node management
		protected.GET("/nodes", handlers.GetNodes)
		protected.GET("/nodes/:id", handlers.GetNode)
		protected.POST("/nodes", handlers.CreateNode)
		protected.PUT("/nodes/:id/health", handlers.UpdateNodeHealth)
	}

	// Start server with graceful shutdown
	serverConfig := server.DefaultConfig("quartermaster", "18002")
	if err := server.Start(serverConfig, router, logger); err != nil {
		logger.WithError(err).Fatal("Server startup failed")
	}
}
