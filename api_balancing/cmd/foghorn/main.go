package main

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/handlers"
	"frameworks/pkg/config"
	"frameworks/pkg/database"
	"frameworks/pkg/logging"
	"frameworks/pkg/middleware"
	"frameworks/pkg/monitoring"
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

	// Create Gin router
	if config.GetEnv("GIN_MODE", "") == "release" {
		middleware.SetGinMode("release")
	}

	router := middleware.NewEngine()
	middleware.SetupCommonMiddleware(router, logger)

	// Add monitoring middleware
	router.Use(metricsCollector.MetricsMiddleware())

	// Health check endpoint with proper checks
	router.GET("/health", healthChecker.Handler())

	// Metrics endpoint for Prometheus
	router.GET("/metrics", metricsCollector.Handler())

	// Node update endpoint for Helmsman
	router.POST("/node/update", handlers.HandleNodeUpdate)

	// Stream health update endpoint for Helmsman
	router.POST("/stream/health", handlers.HandleStreamHealth)

	// Node shutdown notification endpoint for Helmsman
	router.POST("/node/shutdown", handlers.HandleNodeShutdown)

	// MistServer Compatibility - This is the ONLY API we need
	// All requests go through the compatibility handler
	router.NoRoute(handlers.MistServerCompatibilityHandler)

	// Start HTTP server
	port := config.GetEnv("PORT", "18008")
	server := fmt.Sprintf(":%s", port)

	logger.WithFields(logging.Fields{
		"port":          port,
		"mode":          middleware.GetGinMode(),
		"compatibility": "MistServer API Only",
	}).Info("Foghorn server starting")

	// Graceful shutdown handling
	go func() {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		<-sigChan

		logger.Info("Shutting down Foghorn server...")
		time.Sleep(2 * time.Second)
		os.Exit(0)
	}()

	// Start server
	if err := router.Run(server); err != nil {
		logger.WithError(err).Fatal("Failed to start HTTP server")
	}
}
