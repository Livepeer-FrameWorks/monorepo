package main

import (
	"frameworks/api_balancing/internal/balancer"
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

	// Create load balancer metrics
	balancerOperations, balancerDecisions, balancerLatency := metricsCollector.CreateBusinessMetrics()
	dbQueries, dbDuration, dbConnections := metricsCollector.CreateDatabaseMetrics()

	// TODO: Wire these metrics into handlers
	_ = balancerOperations
	_ = balancerDecisions
	_ = balancerLatency
	_ = dbQueries
	_ = dbDuration
	_ = dbConnections

	// Initialize handlers
	handlers.Init(db, logger, lb)

	// Setup router with unified monitoring
	router := server.SetupServiceRouter(logger, "foghorn", healthChecker, metricsCollector)

	// Node update endpoint for Helmsman
	router.POST("/node/update", handlers.HandleNodeUpdate)

	// Stream health update endpoint for Helmsman
	router.POST("/stream/health", handlers.HandleStreamHealth)

	// Node shutdown notification endpoint for Helmsman
	router.POST("/node/shutdown", handlers.HandleNodeShutdown)

	// MistServer Compatibility - This is the ONLY API we need
	// All requests go through the compatibility handler
	router.NoRoute(handlers.MistServerCompatibilityHandler)

	// Start server with graceful shutdown
	serverConfig := server.DefaultConfig("foghorn", "18008")
	if err := server.Start(serverConfig, router, logger); err != nil {
		logger.WithError(err).Fatal("Server startup failed")
	}
}
