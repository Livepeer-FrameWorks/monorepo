package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"

	"frameworks/api_sidecar/internal/handlers"
	"frameworks/pkg/logging"
	"frameworks/pkg/middleware"
	"frameworks/pkg/monitoring"
	"frameworks/pkg/version"
)

// notifyFoghornShutdown sends a final health update to Foghorn before shutdown
func notifyFoghornShutdown() error {
	foghornURL := os.Getenv("FOGHORN_URL")
	if foghornURL == "" {
		foghornURL = "http://foghorn:18008"
	}

	nodeID := os.Getenv("NODE_NAME")
	if nodeID == "" {
		nodeID = "unknown-node"
	}

	update := map[string]interface{}{
		"node_id":   nodeID,
		"type":      "node_shutdown",
		"timestamp": time.Now().Unix(),
		"reason":    "graceful_shutdown",
		"details": map[string]interface{}{
			"initiated_at": time.Now().UTC(),
			"source":       "helmsman",
		},
	}

	jsonData, err := json.Marshal(update)
	if err != nil {
		return fmt.Errorf("failed to marshal shutdown notification: %w", err)
	}

	// Use short timeout since we're shutting down
	client := &http.Client{Timeout: 2 * time.Second}
	req, err := http.NewRequest("POST", foghornURL+"/node/shutdown", bytes.NewBuffer(jsonData))
	if err != nil {
		return fmt.Errorf("failed to create shutdown request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to send shutdown notification: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("foghorn returned error status: %d", resp.StatusCode)
	}

	return nil
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

	// Create metrics for proxy operations
	proxyRequests, proxyOperations, proxyDuration := metricsCollector.CreateBusinessMetrics()

	// TODO: Wire these metrics into handlers
	_ = proxyRequests
	_ = proxyOperations
	_ = proxyDuration

	// Initialize handlers with logger (no database needed - Helmsman is stateless)
	handlers.Init(logger)

	// Initialize Prometheus monitoring
	handlers.InitPrometheusMonitor(logger)

	// Add the local MistServer node to monitoring with default location
	if mistServerURL == "" {
		mistServerURL = "http://mistserver:4242"
	}

	// Add node with default location data for development
	handlers.AddPrometheusNodeDirect("local-mistserver", mistServerURL)

	logger.WithField("mistserver_url", mistServerURL).Info("Added MistServer node for monitoring")

	// Setup Gin router
	r := middleware.SetupGinRouter(logger)

	// Add shared middleware
	middleware.SetupCommonMiddleware(r, logger)

	// Add monitoring middleware
	r.Use(metricsCollector.MetricsMiddleware())

	// API routes - for external API calls and monitoring
	api := r.Group("/api")
	{
		api.GET("/prometheus/nodes", handlers.GetPrometheusNodes)
		api.POST("/prometheus/nodes", handlers.AddPrometheusNode)
		api.DELETE("/prometheus/nodes/:node_id", handlers.RemovePrometheusNode)
	}

	// Webhook routes - MistServer triggers and webhooks
	webhooks := r.Group("/webhooks")
	{
		// MistServer Triggers (for stream routing and validation)
		webhooks.POST("/mist/push_rewrite", handlers.HandlePushRewrite)
		webhooks.POST("/mist/default_stream", handlers.HandleDefaultStream)

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

	// Health check endpoint with proper checks
	r.GET("/health", healthChecker.Handler())

	// Metrics endpoint for Prometheus
	r.GET("/metrics", metricsCollector.Handler())

	// Graceful shutdown handling
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-quit
		logger.WithField("signal", sig.String()).Info("Shutdown signal received")

		// Shutdown Decklog client first
		handlers.ShutdownDecklogClient()

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

	// Start server
	port := os.Getenv("PORT")
	if port == "" {
		port = "18007"
	}

	logger.WithFields(logging.Fields{
		"port":         port,
		"api_base_url": os.Getenv("COMMODORE_URL"),
	}).Info("Starting Helmsman (stateless webhook proxy)")

	if err := r.Run(":" + port); err != nil {
		logger.WithError(err).Fatal("Failed to start server")
	}
}
