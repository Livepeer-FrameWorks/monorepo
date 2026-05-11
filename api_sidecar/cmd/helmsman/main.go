package main

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	sidecarconfig "frameworks/api_sidecar/internal/config"
	"frameworks/api_sidecar/internal/control"
	"frameworks/api_sidecar/internal/handlers"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/monitoring"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/server"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/version"
)

// notifyFoghornShutdown sends a final health update to Foghorn before shutdown using shared client
func notifyFoghornShutdown() error {
	nodeID := control.GetCurrentNodeID()
	if nodeID == "" {
		nodeID = os.Getenv("NODE_ID")
		if nodeID == "" {
			nodeID = "unknown-node"
		}
	}

	trigger := &pb.MistTrigger{
		TriggerType: "NODE_LIFECYCLE_UPDATE",
		NodeId:      nodeID,
		Timestamp:   time.Now().Unix(),
		Blocking:    false,
		RequestId:   "",
		TriggerPayload: &pb.MistTrigger_NodeLifecycleUpdate{
			NodeLifecycleUpdate: &pb.NodeLifecycleUpdate{
				NodeId:    nodeID,
				IsHealthy: false,
				EventType: "node_shutdown",
				Timestamp: time.Now().Unix(),
			},
		},
	}

	_, err := control.SendMistTrigger(trigger, logging.NewLoggerWithService("helmsman-shutdown"))
	return err
}

func main() {
	if version.HandleCLI() {
		return
	}

	// Setup structured logger
	logger := logging.NewLoggerWithService("helmsman")

	// Load environment variables
	config.LoadEnv(logger)

	// Load configuration
	cfg := sidecarconfig.LoadHelmsmanConfig()

	logger.Info("Starting FrameWorks Helmsman (Edge Sidecar)")

	// Setup monitoring
	healthChecker := monitoring.NewHealthChecker("helmsman", version.Version)
	metricsCollector := monitoring.NewMetricsCollector("helmsman", version.Version, version.GitCommit)

	// Add health checks for external dependencies
	// Note: Helmsman only talks to MistServer (local) and Foghorn (gRPC stream)
	healthChecker.AddCheck("mistserver", monitoring.HTTPServiceHealthCheck("MistServer", cfg.MistServerURL+"/api"))

	healthChecker.AddCheck("config", monitoring.ConfigurationHealthCheck(map[string]string{
		"NODE_ID": cfg.NodeID,
	}))

	// Create infrastructure sidecar metrics using handlers.HandlerMetrics directly
	handlerMetrics := &handlers.HandlerMetrics{
		NodeOperations:             metricsCollector.NewCounter("node_operations_total", "Node management operations", []string{"operation", "status"}),
		InfrastructureEvents:       metricsCollector.NewCounter("infrastructure_events_total", "Infrastructure events", []string{"event_type"}),
		NodeHealthChecks:           metricsCollector.NewCounter("node_health_checks_total", "Node health check results", []string{"status"}),
		ResourceAllocationDuration: metricsCollector.NewHistogram("resource_allocation_duration_seconds", "Resource allocation timing", []string{"operation"}, nil),
		MistWebhookRequests:        metricsCollector.NewCounter("mist_webhook_requests_total", "MistServer webhook requests received/processed", []string{"trigger_type", "status"}),
	}
	handlers.Init(logger, handlerMetrics, cfg.NodeID)

	// Initialize Prometheus monitoring
	handlers.InitPrometheusMonitor(logger)

	// Initialize storage management
	if cfg.StorageLocalPath != "" {
		// Initialize cleanup monitor for storage management
		handlers.InitCleanupMonitor(logger, cfg.StorageLocalPath)

		// Initialize dual-storage manager (presigned URL mode - S3 creds held by Foghorn)
		thresholds := handlers.StorageThresholds{
			FreezeThreshold: cfg.FreezeThreshold,
			TargetThreshold: cfg.TargetAfterFreeze,
		}
		if err := handlers.InitStorageManager(logger, cfg.StorageLocalPath, cfg.NodeID, thresholds); err != nil {
			logger.WithError(err).Error("Failed to initialize storage manager")
		}
	}

	var restoreMu sync.Mutex
	control.SetOnControlConnected(func() {
		if cfg.StorageLocalPath == "" {
			return
		}
		restoreMu.Lock()
		defer restoreMu.Unlock()
		idx := control.LocalSegmentIndexInstance(logger)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		if err := idx.RestoreFromDisk(ctx, cfg.StorageLocalPath); err != nil {
			logger.WithError(err).Warn("Local segment index restore-from-disk failed")
		}
		// Bring the segment ledger and on-disk inventory into agreement:
		// detect missing-pre-upload files (-> lost_local), heal reappeared
		// lost_local segments with matching PDT timing, rebuild ledger rows
		// for files-on-disk-no-row, and tombstone manifest-known-but-missing
		// segments. Disk-driven; never fabricates timing.
		if err := control.ReconcileDVRDirectoriesAtStartup(ctx, cfg.StorageLocalPath, logger); err != nil {
			logger.WithError(err).Warn("DVR startup reconciliation failed")
		}
	})

	// Start control client to Foghorn
	control.Start(logger, cfg)

	// Add the local MistServer node to monitoring (use configured node ID)
	// MistServerURL is internal (for API calls), EdgePublicURL is client-facing (for Foghorn BaseUrl)
	handlers.AddPrometheusNodeDirect(cfg.NodeID, cfg.MistServerURL, cfg.EdgePublicURL)

	logger.WithFields(logging.Fields{
		"mistserver_url":  cfg.MistServerURL,
		"edge_public_url": cfg.EdgePublicURL,
	}).Info("Added MistServer node for monitoring")

	// Setup router with unified monitoring
	r := server.SetupServiceRouter(logger, "helmsman", healthChecker, metricsCollector)

	// API routes (root level - nginx adds /api/sidecar/ prefix)
	{
		r.GET("/prometheus/nodes", handlers.GetPrometheusNodes)
		r.POST("/prometheus/nodes", handlers.AddPrometheusNode)
		r.DELETE("/prometheus/nodes/:node_id", handlers.RemovePrometheusNode)

		// Node management (local agent/CLI)
		r.GET("/node/mode", handlers.HandleGetNodeMode)
		r.POST("/node/mode", handlers.HandleSetNodeMode)
	}

	// Edge API — read-only endpoints for tray app / CLI, authenticated via Foghorn
	edge := r.Group("/api/edge", handlers.EdgeAPIAuthMiddleware())
	{
		edge.GET("/status", handlers.HandleEdgeStatus)
		edge.GET("/health", handlers.HandleEdgeHealth)
		edge.GET("/streams", handlers.HandleEdgeStreams)
		edge.GET("/streams/:stream_name", handlers.HandleEdgeStreamDetail)
		edge.GET("/clients", handlers.HandleEdgeClients)
		edge.GET("/metrics", handlers.HandleEdgeMetrics)
	}

	// Webhook routes - MistServer triggers and webhooks
	webhooks := r.Group("/webhooks")
	{
		// MistServer Triggers (for stream routing and validation)
		webhooks.POST("/mist/push_rewrite", handlers.HandlePushRewrite)
		webhooks.POST("/mist/play_rewrite", handlers.HandlePlayRewrite)
		webhooks.POST("/mist/stream_source", handlers.HandleStreamSource)
		webhooks.POST("/mist/stream_process", handlers.HandleStreamProcess)

		// MistServer Webhooks (for event forwarding)
		webhooks.POST("/mist/push_end", handlers.HandlePushEnd)
		webhooks.POST("/mist/push_out_start", handlers.HandlePushOutStart)
		webhooks.POST("/mist/recording_end", handlers.HandleRecordingEnd)
		webhooks.POST("/mist/stream_buffer", handlers.HandleStreamBuffer)
		webhooks.POST("/mist/stream_end", handlers.HandleStreamEnd)
		webhooks.POST("/mist/user_new", handlers.HandleUserNew)
		webhooks.POST("/mist/user_end", handlers.HandleUserEnd)
		webhooks.POST("/mist/live_track_list", handlers.HandleLiveTrackList)
		webhooks.POST("/mist/recording_segment", handlers.HandleRecordingSegment)

		// Processing billing triggers (for tracking transcoding usage)
		webhooks.POST("/mist/livepeer_segment_complete", handlers.HandleLivepeerSegmentComplete)
		webhooks.POST("/mist/process_av_segment_complete", handlers.HandleProcessAVSegmentComplete)

		// Thumbnail triggers
		webhooks.POST("/mist/thumbnail_updated", handlers.HandleThumbnailUpdated)

		// Process exit trigger (from MistServer PROCESS_EXIT)
		webhooks.POST("/mist/process_exit", handlers.HandleProcessExit)
	}

	// Graceful shutdown handling
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-quit
		logger.WithField("signal", sig.String()).Info("Shutdown signal received")

		// Stop storage manager
		handlers.StopStorageManager()

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
