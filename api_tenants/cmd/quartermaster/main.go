package main

import (
	"frameworks/api_tenants/internal/handlers"
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
	logger := logging.NewLoggerWithService("quartermaster")

	// Load environment variables
	config.LoadEnv(logger)

	logger.Info("Starting Quartermaster (Tenant Management API)")

	// Connect to database
	dbConfig := database.DefaultConfig()
	dbConfig.URL = config.GetEnv("DATABASE_URL", "")
	db := database.MustConnect(dbConfig, logger)
	defer db.Close()

	// Setup monitoring
	healthChecker := monitoring.NewHealthChecker("quartermaster", version.Version)
	metricsCollector := monitoring.NewMetricsCollector("quartermaster", version.Version, version.GitCommit)

	// Add health checks
	healthChecker.AddCheck("database", monitoring.DatabaseHealthCheck(db))
	healthChecker.AddCheck("config", monitoring.ConfigurationHealthCheck(map[string]string{
		"DATABASE_URL":  config.GetEnv("DATABASE_URL", ""),
		"SERVICE_TOKEN": config.GetEnv("SERVICE_TOKEN", ""),
	}))

	// Create tenant management metrics
	tenantOperations, tenantRequests, tenantLatency := metricsCollector.CreateBusinessMetrics()
	dbQueries, dbDuration, dbConnections := metricsCollector.CreateDatabaseMetrics()

	// TODO: Wire these metrics into handlers
	_ = tenantOperations
	_ = tenantRequests
	_ = tenantLatency
	_ = dbQueries
	_ = dbDuration
	_ = dbConnections

	// Initialize handlers
	handlers.Init(db, logger)

	// Setup router with unified monitoring
	router := server.SetupServiceRouter(logger, "quartermaster", healthChecker, metricsCollector)

	// Public routes
	public := router.Group("/api")
	{
		public.POST("/tenants/validate", handlers.ValidateTenant)
	}

	// Protected routes (require service token authentication)
	protected := router.Group("/api")
	protected.Use(auth.ServiceAuthMiddleware(config.GetEnv("SERVICE_TOKEN", "default-service-token")))
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
