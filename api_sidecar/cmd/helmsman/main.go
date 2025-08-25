package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"frameworks/api_sidecar/internal/control"
	"frameworks/api_sidecar/internal/handlers"
	fclient "frameworks/pkg/clients/foghorn"
	"frameworks/pkg/logging"
	"frameworks/pkg/monitoring"
	"frameworks/pkg/server"
	"frameworks/pkg/version"
)

// notifyFoghornShutdown sends a final health update to Foghorn before shutdown using shared client
func notifyFoghornShutdown() error {
	foghornURL := os.Getenv("FOGHORN_URL")
	if foghornURL == "" {
		foghornURL = "http://localhost:18008"
	}

	nodeID := os.Getenv("NODE_NAME")
	if nodeID == "" {
		nodeID = "unknown-node"
	}

	// Create shared Foghorn client with short timeout for shutdown
	client := fclient.NewClient(fclient.Config{
		BaseURL: foghornURL,
		Timeout: 2 * time.Second,
		Logger:  logging.NewLoggerWithService("helmsman-shutdown"),
	})

	// Build request with EXACT same payload structure as original
	req := &fclient.NodeShutdownRequest{
		NodeID:    nodeID,
		Type:      "node_shutdown",
		Timestamp: time.Now().Unix(),
		Reason:    "graceful_shutdown",
		Details:   nil, // No FoghornNodeShutdown struct defined, using nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	return client.NotifyShutdown(ctx, req)
}

func main() {
	// Setup structured logger
	logger := logging.NewLoggerWithService("helmsman")

	// Load environment variables from .env file
	if err := godotenv.Load(); err != nil {
		logger.Debug("No .env file found, using environment variables")
	}

	logger.Info("Starting FrameWorks Helmsman (Edge Sidecar)")

	// Setup monitoring
	healthChecker := monitoring.NewHealthChecker("helmsman", version.Version)
	metricsCollector := monitoring.NewMetricsCollector("helmsman", version.Version, version.GitCommit)

	// Add health checks for external dependencies
	commodoreURL := os.Getenv("COMMODORE_URL")
	foghornURL := os.Getenv("FOGHORN_URL")
	mistServerURL := os.Getenv("MISTSERVER_URL")

	if commodoreURL != "" {
		healthChecker.AddCheck("commodore", monitoring.HTTPServiceHealthCheck("Commodore", commodoreURL+"/health"))
	}
	if foghornURL != "" {
		healthChecker.AddCheck("foghorn", monitoring.HTTPServiceHealthCheck("Foghorn", foghornURL+"/health"))
	}
	if mistServerURL != "" {
		healthChecker.AddCheck("mistserver", monitoring.HTTPServiceHealthCheck("MistServer", mistServerURL+"/api"))
	}

	healthChecker.AddCheck("config", monitoring.ConfigurationHealthCheck(map[string]string{
		"NODE_NAME": os.Getenv("NODE_NAME"),
		"PORT":      os.Getenv("PORT"),
	}))

	// Create infrastructure sidecar metrics using handlers.HandlerMetrics directly
	handlerMetrics := &handlers.HandlerMetrics{
		NodeOperations:             metricsCollector.NewCounter("node_operations_total", "Node management operations", []string{"operation", "status"}),
		InfrastructureEvents:       metricsCollector.NewCounter("infrastructure_events_total", "Infrastructure events", []string{"event_type"}),
		NodeHealthChecks:           metricsCollector.NewCounter("node_health_checks_total", "Node health check results", []string{"status"}),
		ResourceAllocationDuration: metricsCollector.NewHistogram("resource_allocation_duration_seconds", "Resource allocation timing", []string{"operation"}, nil),
	}
	handlers.Init(logger, handlerMetrics)

	// Initialize Prometheus monitoring
	handlers.InitPrometheusMonitor(logger)

	// Initialize cleanup monitor for storage management (reuse existing storage path)
	storagePath := os.Getenv("HELMSMAN_STORAGE_LOCAL_PATH")
	if storagePath != "" {
		handlers.InitCleanupMonitor(logger, storagePath)
	}

	// Start control client to Foghorn
	control.Start(logger)

	// Add the local MistServer node to monitoring with default location
	if mistServerURL == "" {
		mistServerURL = "http://localhost:4242"
	}

	// Add node with default location data for development
	handlers.AddPrometheusNodeDirect("local-mistserver", mistServerURL)

	logger.WithField("mistserver_url", mistServerURL).Info("Added MistServer node for monitoring")

	// Setup router with unified monitoring
	r := server.SetupServiceRouter(logger, "helmsman", healthChecker, metricsCollector)

	// API routes (root level - nginx adds /api/sidecar/ prefix)
	{
		r.GET("/prometheus/nodes", handlers.GetPrometheusNodes)
		r.POST("/prometheus/nodes", handlers.AddPrometheusNode)
		r.DELETE("/prometheus/nodes/:node_id", handlers.RemovePrometheusNode)
	}

	// Webhook routes - MistServer triggers and webhooks
	webhooks := r.Group("/webhooks")
	{
		// MistServer Triggers (for stream routing and validation)
		webhooks.POST("/mist/push_rewrite", handlers.HandlePushRewrite)
		webhooks.POST("/mist/default_stream", handlers.HandleDefaultStream)
		webhooks.POST("/mist/stream_source", handlers.HandleStreamSource)

		// MistServer Webhooks (for event forwarding)
		webhooks.POST("/mist/push_end", handlers.HandlePushEnd)
		webhooks.POST("/mist/push_out_start", handlers.HandlePushOutStart)
		webhooks.POST("/mist/recording_end", handlers.HandleRecordingEnd)
		webhooks.POST("/mist/stream_buffer", handlers.HandleStreamBuffer)
		webhooks.POST("/mist/stream_end", handlers.HandleStreamEnd)
		webhooks.POST("/mist/user_new", handlers.HandleUserNew)
		webhooks.POST("/mist/user_end", handlers.HandleUserEnd)
		webhooks.POST("/mist/live_track_list", handlers.HandleLiveTrackList)
		webhooks.POST("/mist/live_bandwidth", handlers.HandleLiveBandwidth)
	}

	// Graceful shutdown handling
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-quit
		logger.WithField("signal", sig.String()).Info("Shutdown signal received")

		// Shutdown Decklog client first
		handlers.ShutdownDecklogClient()

		// Stop cleanup monitor
		handlers.StopCleanupMonitor()

		// Try to notify Foghorn
		if err := notifyFoghornShutdown(); err != nil {
			logger.WithError(err).Error("Failed to notify Foghorn of shutdown")
		} else {
			logger.Info("Successfully notified Foghorn of shutdown")
		}

		// Brief pause to allow final messages to be sent
		time.Sleep(500 * time.Millisecond)

		logger.WithFields(logging.Fields{
			"reason":    "graceful_shutdown",
			"service":   "helmsman",
			"timestamp": time.Now().Format(time.RFC3339),
		}).Info("Shutting down Helmsman gracefully...")

		os.Exit(0)
	}()

	// Start server with graceful shutdown
	serverConfig := server.DefaultConfig("helmsman", "18007")
	if err := server.Start(serverConfig, r, logger); err != nil {
		logger.WithError(err).Fatal("Server startup failed")
	}
}
