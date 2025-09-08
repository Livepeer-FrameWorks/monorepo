package main

import (
	"context"
	"frameworks/api_tenants/internal/handlers"
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

	// Create custom tenant management metrics
	metrics := &handlers.QuartermasterMetrics{
		TenantOperations:    metricsCollector.NewCounter("tenant_operations_total", "Tenant CRUD operations", []string{"operation", "status"}),
		ClusterOperations:   metricsCollector.NewCounter("cluster_operations_total", "Cluster management operations", []string{"operation", "status"}),
		TenantResourceUsage: metricsCollector.NewGauge("tenant_resource_usage", "Tenant resource usage", []string{"tenant_id", "resource_type"}),
	}

	// Create database metrics
	metrics.DBQueries, metrics.DBDuration, metrics.DBConnections = metricsCollector.CreateDatabaseMetrics()

	// Initialize handlers
	handlers.Init(db, logger, metrics)

	// Setup router with unified monitoring
	router := server.SetupServiceRouter(logger, "quartermaster", healthChecker, metricsCollector)

	// API routes (root level - nginx adds /api/tenants/ prefix)
	{
		// Public routes
		router.POST("/tenants/validate", handlers.ValidateTenant)

		// Protected routes (accept JWT user tokens and service token fallback)
		protected := router.Group("")
		protected.Use(auth.JWTAuthMiddleware([]byte(config.GetEnv("JWT_SECRET", "default-secret-key-change-in-production"))))
		{
			// Tenant management
			protected.POST("/tenants", handlers.CreateTenant)
			protected.GET("/tenants/:id", handlers.GetTenant)
			protected.PUT("/tenants/:id", handlers.UpdateTenant)
			protected.DELETE("/tenants/:id", handlers.DeleteTenant)

			// Cluster management
			protected.GET("/clusters", handlers.GetClusters)
			protected.POST("/clusters", handlers.CreateCluster)
			protected.GET("/clusters/:id", handlers.GetCluster)
			protected.PUT("/clusters/:id", handlers.UpdateCluster)

			// Service management
			protected.GET("/services", handlers.GetServices)
			protected.GET("/services/:id", handlers.GetService)
			protected.GET("/clusters/:id/services", handlers.GetClusterServices)
			protected.PUT("/clusters/:id/services/:service_id", handlers.UpdateClusterServiceState)
			protected.GET("/service-instances", handlers.GetServiceInstances)

			// Discovery & Bootstrap
			protected.GET("/service-discovery", handlers.ServiceDiscovery)
			protected.GET("/services/health", handlers.GetServicesHealth)
			protected.GET("/services/:id/health", handlers.GetServiceHealth)
			protected.GET("/clusters/access", handlers.GetClustersAccess)
			protected.GET("/clusters/available", handlers.GetClustersAvailable)
			protected.POST("/bootstrap/edge-node", handlers.BootstrapEdgeNode)
			protected.POST("/bootstrap/service", handlers.BootstrapService)

			// Bootstrap token management (provider/admin only)
			protected.POST("/admin/bootstrap-tokens", handlers.CreateBootstrapToken)
			protected.GET("/admin/bootstrap-tokens", handlers.ListBootstrapTokens)
			protected.DELETE("/admin/bootstrap-tokens/:id", handlers.RevokeBootstrapToken)

			// Node management
			protected.GET("/nodes", handlers.GetNodes)
			protected.GET("/nodes/:id", handlers.GetNode)
			protected.POST("/nodes/resolve-fingerprint", handlers.ResolveNodeFingerprint)
			protected.POST("/nodes", handlers.CreateNode)
			protected.PUT("/nodes/:id/health", handlers.UpdateNodeHealth)
			protected.GET("/nodes/:id/owner", handlers.GetNodeOwner)
		}
	}

	// Start health poller before serving
	handlers.StartHealthPoller()

	// Start server with graceful shutdown
	serverConfig := server.DefaultConfig("quartermaster", "18002")
	if err := server.Start(serverConfig, router, logger); err != nil {
		logger.WithError(err).Fatal("Server startup failed")
	}

	// Best-effort self-registration in Quartermaster (idempotent)
	go func() {
		qc := qmclient.NewClient(qmclient.Config{BaseURL: config.GetEnv("QUARTERMASTER_URL", "http://localhost:18002"), ServiceToken: config.GetEnv("SERVICE_TOKEN", ""), Logger: logger})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, _ = qc.BootstrapService(ctx, &qmapi.BootstrapServiceRequest{Type: "quartermaster", Version: version.Version, Protocol: "http", HealthEndpoint: func() *string { s := "/health"; return &s }(), Port: 18002})
	}()
}
