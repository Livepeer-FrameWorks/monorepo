package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"frameworks/api_sidecar/internal/appconfig"
	sidecarconfig "frameworks/api_sidecar/internal/config"
	"frameworks/api_sidecar/internal/control"
	"frameworks/api_sidecar/internal/edgeseed"
	"frameworks/api_sidecar/internal/handlers"
	"frameworks/api_sidecar/internal/leases"
	"frameworks/api_sidecar/internal/relay"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/monitoring"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/restream"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/server"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/version"
	"github.com/gin-gonic/gin"
)

// shutdownDrainTimeout bounds how long in-flight requests may finish after
// SIGINT or SIGTERM once the restart is announced. Foghorn holds the health
// of an announced restart for 5 to 30 seconds (20 by default), and that window
// has to cover the announcement, the exit, and the supervisor restart.
const shutdownDrainTimeout = 3 * time.Second

func main() {
	if len(os.Args) > 1 && os.Args[1] == "seed-edge" {
		if err := edgeseed.Run(); err != nil {
			fmt.Fprintln(os.Stderr, "seed-edge:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "scrub-edge-credentials" {
		if err := scrubEdgeCredentials(); err != nil {
			fmt.Fprintln(os.Stderr, "scrub-edge-credentials:", err)
			os.Exit(1)
		}
		return
	}
	if version.HandleCLI() {
		return
	}

	// Setup structured logger
	logger := logging.NewLoggerWithService("helmsman")

	// Load environment variables
	config.LoadEnv(logger)

	configOptions := config.Options{Service: appconfig.ServiceID, Logger: logger}
	appCfg, err := config.Load[appconfig.Helmsman](configOptions)
	if err != nil {
		logger.WithError(err).Fatal("Invalid configuration")
	}
	appCfg.ApplyLogLevel(logger)
	liveConfig := config.NewLive(appCfg, configOptions)
	appconfig.Install(liveConfig)
	cfg := sidecarconfig.NewHelmsmanConfig(appCfg)

	go control.ObserveCredentialCleanup(context.Background(), cfg.StateDir, cfg.EnrollmentTokenFile, cfg.RuntimeEnvFile)
	if _, err := restream.DestinationPolicyFromValues(appCfg.RestreamAllowPrivateDestinations, appCfg.RestreamAllowedPrivateCIDRs, appCfg.RestreamDeniedCIDRs); err != nil {
		logger.WithError(err).Fatal("invalid restream destination policy")
	}

	logger.Info("Starting FrameWorks Helmsman (Edge Sidecar)")

	// Setup monitoring
	healthChecker := monitoring.NewHealthChecker("helmsman", version.Version)
	metricsCollector := monitoring.NewMetricsCollector("helmsman", version.Version, version.GitCommit)

	// Health and readiness run the same node-local checks: Helmsman only
	// needs the local MistServer to serve, and a Foghorn outage must not take
	// the node out of rotation. Readiness additionally reports draining once
	// shutdown starts.
	mistServerCheck := monitoring.HTTPServiceHealthCheck("MistServer", cfg.MistServerURL+"/api")
	configCheck := monitoring.ConfigurationHealthCheck(map[string]string{
		"NODE_ID": cfg.NodeID,
	})
	healthChecker.AddCheck("mistserver", mistServerCheck)
	healthChecker.AddCheck("config", configCheck)
	readiness := monitoring.NewReadinessChecker("helmsman", version.Version)
	readiness.AddCheck("mistserver", mistServerCheck)
	readiness.AddCheck("config", configCheck)

	// Create infrastructure sidecar metrics using handlers.HandlerMetrics directly
	handlerMetrics := &handlers.HandlerMetrics{
		NodeOperations:             metricsCollector.NewCounter("node_operations_total", "Node management operations", []string{"operation", "status"}),
		InfrastructureEvents:       metricsCollector.NewCounter("infrastructure_events_total", "Infrastructure events", []string{"event_type"}),
		NodeHealthChecks:           metricsCollector.NewCounter("node_health_checks_total", "Node health check results", []string{"status"}),
		ResourceAllocationDuration: metricsCollector.NewHistogram("resource_allocation_duration_seconds", "Resource allocation timing", []string{"operation"}, nil),
		MistWebhookRequests:        metricsCollector.NewCounter("mist_webhook_requests_total", "MistServer webhook requests received/processed", []string{"trigger_type", "status"}),
	}
	handlers.Init(logger, handlerMetrics, cfg.NodeID)

	// Initialize storage management
	if cfg.StorageLocalPath != "" {
		// Initialize cleanup monitor for storage management
		handlers.InitCleanupMonitor(logger, cfg.StorageLocalPath)

		// Initialize dual-storage manager (presigned URL mode - S3 creds held by Foghorn)
		thresholds := handlers.StorageThresholds{
			FreezeThreshold: cfg.FreezeThreshold,
			TargetThreshold: cfg.TargetAfterFreeze,
			CapacityBytes:   cfg.StorageCapacityBytes,
		}
		if err := handlers.InitStorageManager(logger, cfg.StorageLocalPath, cfg.NodeID, thresholds); err != nil {
			logger.WithError(err).Error("Failed to initialize storage manager")
		}

		// Initialize Mist-session disk leases. Must run AFTER InitStorageManager
		// so the storage path is known; spawns goroutines for chapter registry
		// rehydration and deferred-delete drain. Boot pause keeps destructive
		// cleanup disabled until rehydration + first successful Mist
		// reconciliation completes.
		handlers.InitLeases(logger, cfg.StorageLocalPath)

		// Rebuild the local DVR job map from local disk + local Mist without
		// waiting for Foghorn. Ledger reconciliation remains connection-bound
		// below, but media-process recovery is a cell-local startup invariant.
		go func() {
			for {
				if err := control.RecoverActiveDVRJobsFromMist(cfg.StorageLocalPath, logger); err == nil {
					return
				} else {
					logger.WithError(err).Warn("Active DVR recovery from local Mist failed; retrying")
				}
				time.Sleep(5 * time.Second)
			}
		}()
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

	// Helmsman has no service token, so neither router serves the /debug
	// surface.
	r := server.NewServiceRouter(server.RouterSpec{
		Service: "helmsman",
		Logger:  logger,
		Health:  healthChecker,
		Ready:   readiness,
		Metrics: metricsCollector,
		Runtime: appCfg.HTTPRuntime,
	})
	managementRouter := server.NewServiceRouter(server.RouterSpec{
		Service: "helmsman-management",
		Logger:  logger,
		Health:  healthChecker,
		Ready:   readiness,
		Metrics: metricsCollector,
		Runtime: appCfg.HTTPRuntime,
	})

	// Operator and diagnostic routes stay on the loopback-only management
	// listener. The public/Mist listener contains only data-plane callbacks and
	// authenticated edge reads.
	{
		managementRouter.GET("/prometheus/nodes", handlers.GetPrometheusNodes)
		managementRouter.POST("/prometheus/nodes", handlers.AddPrometheusNode)
		managementRouter.DELETE("/prometheus/nodes/:node_id", handlers.RemovePrometheusNode)

		// Durable trigger WAL inspection + replay. Operators hit these
		// during incident response to see what's awaiting Foghorn's ack
		// and to kick the forwarder without waiting for the periodic
		// tick. See docs/architecture/trigger-durability.md.
		managementRouter.GET("/triggers/wal", handlers.HandleTriggerWALStatus)
		managementRouter.POST("/triggers/wal/replay", handlers.HandleTriggerWALReplay)
	}
	managementRouter.GET("/node/mode", handlers.HandleGetNodeMode)
	managementRouter.POST("/node/mode", handlers.HandleSetNodeMode)

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

	registerMistAdminRoutes(r, cfg.MistServerURL, logger)

	// Read-through artifact relay: Mist sees stable seekable HTTP sources
	// for vod/clip/dvr-chapter playback and safe-wrapper processing input.
	// Behind these URLs the relay materializes bytes from local disk
	// (warm), from S3 into disk (cold + healthy), or from S3 straight to
	// socket (cold + pressured). The route group lives on the existing
	// internal-only Helmsman listener; no per-route auth.
	var relayServer *relay.Server
	if sm := handlers.GetStorageManager(); sm != nil {
		relayServer = relay.New(relay.Options{
			BasePath:         cfg.StorageLocalPath,
			Admitter:         sm,
			Resolver:         relay.NewControlResolver(),
			Freeze:           handlers.NewRelayFreezeHandoff(),
			Heat:             leases.GlobalHeat(),
			Logger:           logger,
			NodeID:           cfg.NodeID,
			RelayTrustedCIDR: cfg.RelayTrustedCIDR,
		})
		relayServer.MountRoutes(r)
	} else {
		logger.Warn("Storage manager not initialized; relay /internal/artifact/* routes skipped")
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
		webhooks.POST("/mist/push_input_close", handlers.HandlePushInputClose)
		webhooks.POST("/mist/push_out_start", handlers.HandlePushOutStart)
		webhooks.POST("/mist/recording_end", handlers.HandleRecordingEnd)
		webhooks.POST("/mist/stream_buffer", handlers.HandleStreamBuffer)
		webhooks.POST("/mist/stream_end", handlers.HandleStreamEnd)
		webhooks.POST("/mist/user_new", handlers.HandleUserNew)
		webhooks.POST("/mist/conn_play", handlers.HandleConnPlay)
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
		webhooks.POST("/mist/process_replace", handlers.HandleProcessReplace)
	}

	// server.Run calls this only when SIGINT or SIGTERM stopped it, so the
	// announcement covers every planned exit and never a listener failure.
	signaled := false
	signalStopHook := func(_ context.Context, sig os.Signal) {
		signaled = true
		logger.WithField("signal", sig.String()).Info("Shutdown signal received")

		// Announce the planned exit before the listeners drain, so Foghorn
		// holds DNS health for a bounded reconnect window (the supervisor
		// restarts us in seconds; the data plane keeps serving meanwhile). A
		// crash skips this, so unannounced disconnects still go unhealthy
		// immediately.
		if err := control.AnnounceRestart(logging.NewLoggerWithService("helmsman-shutdown")); err != nil {
			logger.WithError(err).Error("Failed to announce restart to Foghorn")
		} else {
			logger.Info("Announced restart to Foghorn")
		}

		// Brief pause to allow final messages to be sent
		time.Sleep(500 * time.Millisecond)
	}
	shutdownHook := func(context.Context) {
		// Relay has no background fills to stop — cold fetches live
		// inside HTTP request handlers and cancel through the request
		// context when the HTTP server stops accepting.
		handlers.StopStorageManager()
		handlers.StopCleanupMonitor()
	}

	server.RegisterEnvFileReload("helmsman", logger)
	runErr := server.Run(context.Background(), server.RunSpec{
		Service: "helmsman",
		Logger:  logger,
		Ready:   readiness,
		HTTP: []server.HTTPListener{
			{Name: "helmsman", BindAddr: listenHost(appCfg.PublicBindAddr), Port: appCfg.Port, Handler: r},
			{Name: "helmsman-management", BindAddr: listenHost(appCfg.ManagementBindAddr), Port: appCfg.ManagementPort, Handler: managementRouter},
		},
		OnReload:        []server.ReloadCallback{liveConfig.Reload},
		OnSignalStop:    []func(context.Context, os.Signal){signalStopHook},
		OnShutdown:      []func(context.Context){shutdownHook},
		ShutdownTimeout: shutdownDrainTimeout,
	})
	if runErr != nil {
		if !signaled {
			logger.WithError(runErr).Fatal("Server exited with error")
		}
		logger.WithError(runErr).Warn("Listeners did not drain before shutdown")
	}

	logger.WithFields(logging.Fields{
		"reason":    "graceful_shutdown",
		"service":   "helmsman",
		"timestamp": time.Now().Format(time.RFC3339),
	}).Info("Helmsman stopped")
}

// scrubEdgeCredentials runs the scrub-edge-credentials subcommand.
func scrubEdgeCredentials() error {
	scrub, err := config.Load[appconfig.HelmsmanScrubEdgeCredentials](config.Options{Service: appconfig.ServiceID})
	if err != nil {
		return err
	}
	return control.RunCredentialCleanupWorker(context.Background(), scrub.StateDir, scrub.NodeID, scrub.EnrollmentTokenFile, scrub.RuntimeEnvFile)
}

// listenHost strips IPv6 brackets from a configured bind address, because
// server.Run joins the host and port itself.
func listenHost(bindAddr string) string {
	return strings.Trim(strings.TrimSpace(bindAddr), "[]")
}

func registerMistAdminRoutes(r *gin.Engine, mistServerURL string, logger logging.Logger) {
	mistAdminProxy := handlers.MistAdminProxy(mistServerURL, logger)
	requireMistAdmin := handlers.RequireMistAdmin(logger)
	mistAdminSession := handlers.MistAdminSessionHandler(logger)

	r.POST("/_mist-session", mistAdminSession)
	r.Any("/_mist", requireMistAdmin, mistAdminProxy)
	r.Any("/_mist/*proxy", requireMistAdmin, mistAdminProxy)
}
