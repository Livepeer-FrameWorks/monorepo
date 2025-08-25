package main

import (
	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/handlers"
	"frameworks/pkg/config"
	"frameworks/pkg/database"
	"frameworks/pkg/logging"
	"frameworks/pkg/monitoring"
	"frameworks/pkg/server"
	"frameworks/pkg/version"
)

func main() {
	// Initialize logger
	logger := logging.NewLoggerWithService("foghorn")

	// Load environment variables
	config.LoadEnv(logger)

	logger.WithField("service", "foghorn").Info("Starting Foghorn Load Balancer")

	// Connect to database
	dbConfig := database.DefaultConfig()
	dbConfig.URL = config.GetEnv("DATABASE_URL", "")
	db := database.MustConnect(dbConfig, logger)
	defer db.Close()

	// Create load balancer instance
	lb := balancer.NewLoadBalancer(db, logger)

	// Set weights from environment variables
	cpu := uint64(config.GetEnvInt("CPU_WEIGHT", 500))
	ram := uint64(config.GetEnvInt("RAM_WEIGHT", 500))
	bw := uint64(config.GetEnvInt("BANDWIDTH_WEIGHT", 1000))
	geo := uint64(config.GetEnvInt("GEO_WEIGHT", 1000))
	bonus := uint64(config.GetEnvInt("STREAM_BONUS", 50))

	if cpu > 0 && ram > 0 && bw > 0 && geo > 0 && bonus > 0 {
		lb.SetWeights(cpu, ram, bw, geo, bonus)
	}

	// Setup monitoring
	healthChecker := monitoring.NewHealthChecker("foghorn", version.Version)
	metricsCollector := monitoring.NewMetricsCollector("foghorn", version.Version, version.GitCommit)

	// Add health checks
	healthChecker.AddCheck("database", monitoring.DatabaseHealthCheck(db))
	healthChecker.AddCheck("config", monitoring.ConfigurationHealthCheck(map[string]string{
		"DATABASE_URL": config.GetEnv("DATABASE_URL", ""),
	}))

	// Create custom load balancing metrics
	metrics := &handlers.FoghornMetrics{
		RoutingDecisions:        metricsCollector.NewCounter("routing_decisions_total", "Routing decisions made", []string{"algorithm", "selected_node"}),
		NodeSelectionDuration:   metricsCollector.NewHistogram("node_selection_duration_seconds", "Node selection latency", []string{}, nil),
		LoadDistribution:        metricsCollector.NewGauge("load_distribution_ratio", "Load distribution ratio", []string{"node_id"}),
		HealthScoreCalculations: metricsCollector.NewCounter("health_score_calculations_total", "Health score calculations", []string{}),
	}

	// Create database metrics
	metrics.DBQueries, metrics.DBDuration, metrics.DBConnections = metricsCollector.CreateDatabaseMetrics()

	// Initialize handlers
	handlers.Init(db, logger, lb, metrics)

	// Start Helmsman control gRPC server
	control.Init(logger)
	controlAddr := config.GetEnv("FOGHORN_CONTROL_ADDR", ":18019")
	if _, err := control.StartGRPCServer(controlAddr, logger); err != nil {
		logger.WithError(err).Fatal("Failed to start control gRPC server")
	}

	// Setup router with unified monitoring
	router := server.SetupServiceRouter(logger, "foghorn", healthChecker, metricsCollector)

	// Node update endpoint for Helmsman
	router.POST("/node/update", handlers.HandleNodeUpdate)

	// Stream health update endpoint for Helmsman
	router.POST("/stream/health", handlers.HandleStreamHealth)

	// Node shutdown notification endpoint for Helmsman
	router.POST("/node/shutdown", handlers.HandleNodeShutdown)

	// Clip orchestration endpoints
	router.POST("/clips/create", handlers.HandleCreateClip)
	router.GET("/clips", handlers.HandleGetClips)
	router.GET("/clips/:clip_hash", handlers.HandleGetClip)
	router.GET("/clips/:clip_hash/node", handlers.HandleGetClipNode)
	router.DELETE("/clips/:clip_hash", handlers.HandleDeleteClip)
	router.GET("/clips/resolve/:clip_hash", handlers.HandleResolveClip)

	// Nodes overview for capabilities/limits/artifacts
	router.GET("/nodes/overview", handlers.HandleNodesOverview)

	// Viewer endpoint resolution
	router.POST("/viewer/resolve", handlers.HandleResolveViewerEndpoint)

	// MistServer Compatibility - all requests including capability filtering via query params
	router.NoRoute(handlers.MistServerCompatibilityHandler)

	// Start server with graceful shutdown
	serverConfig := server.DefaultConfig("foghorn", "18008")
	if err := server.Start(serverConfig, router, logger); err != nil {
		logger.WithError(err).Fatal("Server startup failed")
	}
}
