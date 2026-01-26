package main

import (
	"fmt"
	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/control"
	foghorngrpc "frameworks/api_balancing/internal/grpc"
	"frameworks/api_balancing/internal/handlers"
	"frameworks/api_balancing/internal/jobs"
	"frameworks/api_balancing/internal/state"
	"frameworks/api_balancing/internal/storage"
	"frameworks/api_balancing/internal/triggers"
	"frameworks/pkg/cache"
	"frameworks/pkg/clients/commodore"
	"frameworks/pkg/clients/decklog"
	purserclient "frameworks/pkg/clients/purser"
	qmclient "frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/config"
	"frameworks/pkg/database"
	"frameworks/pkg/geoip"
	"frameworks/pkg/logging"
	"frameworks/pkg/monitoring"
	"frameworks/pkg/server"
	"frameworks/pkg/version"
	"strconv"
	"time"
)

func main() {
	// Initialize logger
	logger := logging.NewLoggerWithService("foghorn")
	state.SetLogger(logger)

	// Load environment variables
	config.LoadEnv(logger)

	logger.WithField("service", "foghorn").Info("Starting Foghorn Load Balancer")

	// Service token for service-to-service authentication
	serviceToken := config.RequireEnv("SERVICE_TOKEN")

	// Connect to database
	dbConfig := database.DefaultConfig()
	dbURL := config.RequireEnv("DATABASE_URL")
	dbConfig.URL = dbURL
	db := database.MustConnect(dbConfig, logger)
	defer db.Close()

	// Create load balancer instance
	lb := balancer.NewLoadBalancer(logger)

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
		"DATABASE_URL": dbURL,
	}))
	healthChecker.AddCheck("state_rehydrate", func() monitoring.CheckResult {
		at, errMsg := state.DefaultManager().RehydrateStatus()
		if errMsg != "" {
			return monitoring.CheckResult{
				Status:  "unhealthy",
				Message: fmt.Sprintf("last rehydrate error: %s", errMsg),
				Latency: time.Since(at).String(),
			}
		}
		if at.IsZero() {
			return monitoring.CheckResult{
				Status:  "healthy",
				Message: "rehydrate not yet run",
			}
		}
		return monitoring.CheckResult{
			Status:  "healthy",
			Message: "rehydrate ok",
			Latency: time.Since(at).String(),
		}
	})

	// Create custom load balancing metrics
	metrics := &handlers.FoghornMetrics{
		RoutingDecisions:      metricsCollector.NewCounter("routing_decisions_total", "Routing decisions made", []string{"algorithm", "selected_node"}),
		NodeSelectionDuration: metricsCollector.NewHistogram("node_selection_duration_seconds", "Node selection latency", []string{}, nil),
		LoadDistribution:      metricsCollector.NewGauge("load_distribution_ratio", "Load distribution ratio", []string{"node_id"}),
	}

	// Control-plane (HelmsmanControl) and data-plane (Decklog fan-out) observability
	control.SetMetrics(&control.ControlMetrics{
		MistTriggers: metricsCollector.NewCounter(
			"control_mist_triggers_total",
			"MistTrigger messages received/processed over the HelmsmanControl stream",
			[]string{"trigger_type", "blocking", "status"},
		),
	})

	// Wire state metrics hooks
	stateWrites := metricsCollector.NewCounter("state_writes_total", "State write-through operations", []string{"entity", "op"})
	rehydrateDur := metricsCollector.NewHistogram("state_rehydrate_seconds", "State rehydrate duration", []string{"entity"}, nil)
	state.SetMetricsHooks(
		func(labels map[string]string) { stateWrites.WithLabelValues(labels["entity"], labels["op"]).Inc() },
		func(seconds float64, labels map[string]string) {
			rehydrateDur.WithLabelValues(labels["entity"]).Observe(seconds)
		},
	)

	// Cache metrics and factory
	cacheHits := metricsCollector.NewCounter("cache_hits_total", "Cache hits", []string{"key"})
	cacheMiss := metricsCollector.NewCounter("cache_misses_total", "Cache misses", []string{"key"})
	cacheStale := metricsCollector.NewCounter("cache_stale_total", "Cache stale served", []string{"key"})
	cacheStore := metricsCollector.NewCounter("cache_store_total", "Cache stores", []string{"key", "ok"})
	cacheError := metricsCollector.NewCounter("cache_errors_total", "Cache load errors", []string{"key"})

	newCache := func(ttl, swr, negTTL time.Duration, max int) *cache.Cache {
		return cache.New(cache.Options{TTL: ttl, StaleWhileRevalidate: swr, NegativeTTL: negTTL, MaxEntries: max}, cache.MetricsHooks{
			OnHit:   func(l map[string]string) { cacheHits.WithLabelValues(l["key"]).Inc() },
			OnMiss:  func(l map[string]string) { cacheMiss.WithLabelValues(l["key"]).Inc() },
			OnStale: func(l map[string]string) { cacheStale.WithLabelValues(l["key"]).Inc() },
			OnStore: func(l map[string]string) { cacheStore.WithLabelValues(l["key"], l["ok"]).Inc() },
			OnError: func(l map[string]string) { cacheError.WithLabelValues(l["key"]).Inc() },
		})
	}
	_ = newCache // Placeholder until wired into clients (Commodore/GeoIP)

	// Create database metrics
	metrics.DBQueries, metrics.DBDuration, metrics.DBConnections = metricsCollector.CreateDatabaseMetrics()

	// --- Initialize Clients (Lifted from Handlers) ---

	decklogGRPCAddr := config.GetEnv("DECKLOG_GRPC_ADDR", "decklog:18006")
	allowInsecure := config.GetEnv("DECKLOG_USE_TLS", "false") != "true"
	decklogConfig := decklog.BatchedClientConfig{
		Target:        decklogGRPCAddr,
		AllowInsecure: allowInsecure,
		Timeout:       10 * time.Second,
		Source:        "foghorn",
		ServiceToken:  serviceToken,
	}
	decklogClient, err := decklog.NewBatchedClient(decklogConfig, logger)
	if err != nil {
		logger.WithError(err).Error("Failed to initialize Decklog gRPC client")
	}

	// Quartermaster (gRPC)
	quartermasterGRPCURL := config.GetEnv("QUARTERMASTER_GRPC_ADDR", "localhost:19002")
	qmClient, err := qmclient.NewGRPCClient(qmclient.GRPCConfig{
		GRPCAddr:     quartermasterGRPCURL,
		Timeout:      30 * time.Second,
		Logger:       logger,
		ServiceToken: serviceToken,
	})
	if err != nil {
		logger.WithError(err).Fatal("Failed to create Quartermaster gRPC client")
	}
	defer qmClient.Close()

	// Commodore (gRPC)
	commodoreGRPCURL := config.GetEnv("COMMODORE_GRPC_ADDR", "localhost:19001")

	// Commodore Cache
	ttl := 60 * time.Second
	if v := config.GetEnv("COMMODORE_CACHE_TTL", ""); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			ttl = d
		}
	}
	swr := 30 * time.Second
	if v := config.GetEnv("COMMODORE_CACHE_SWR", ""); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			swr = d
		}
	}
	neg := 10 * time.Second
	if v := config.GetEnv("COMMODORE_CACHE_NEG_TTL", ""); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			neg = d
		}
	}
	maxEntries := 10000
	if v := config.GetEnv("COMMODORE_CACHE_MAX", ""); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			maxEntries = n
		}
	}
	// Use the cache factory from main
	commodoreCache := newCache(ttl, swr, neg, maxEntries)

	commodoreClient, err := commodore.NewGRPCClient(commodore.GRPCConfig{
		GRPCAddr:     commodoreGRPCURL,
		Timeout:      30 * time.Second,
		Logger:       logger,
		Cache:        commodoreCache,
		ServiceToken: serviceToken,
	})
	if err != nil {
		logger.WithError(err).Fatal("Failed to create Commodore gRPC client")
	}
	defer commodoreClient.Close()

	// Purser (gRPC) - x402 settlement + billing checks
	purserGRPCURL := config.GetEnv("PURSER_GRPC_ADDR", "purser:19003")
	purserClient, err := purserclient.NewGRPCClient(purserclient.GRPCConfig{
		GRPCAddr:     purserGRPCURL,
		Timeout:      30 * time.Second,
		Logger:       logger,
		ServiceToken: serviceToken,
	})
	if err != nil {
		logger.WithError(err).Warn("Failed to create Purser gRPC client - x402 payments will be unavailable")
		purserClient = nil
	} else {
		defer purserClient.Close()
	}

	// GeoIP
	var geoipReader *geoip.Reader
	var geoipCache *cache.Cache
	geoipReader = geoip.GetSharedReader()
	if geoipReader != nil {
		gttl := 300 * time.Second
		gswr := 120 * time.Second
		gneg := 60 * time.Second
		gmax := 50000
		if v := config.GetEnv("GEOIP_CACHE_TTL", ""); v != "" {
			if d, err := time.ParseDuration(v); err == nil {
				gttl = d
			}
		}
		if v := config.GetEnv("GEOIP_CACHE_SWR", ""); v != "" {
			if d, err := time.ParseDuration(v); err == nil {
				gswr = d
			}
		}
		if v := config.GetEnv("GEOIP_CACHE_NEG_TTL", ""); v != "" {
			if d, err := time.ParseDuration(v); err == nil {
				gneg = d
			}
		}
		if v := config.GetEnv("GEOIP_CACHE_MAX", ""); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				gmax = n
			}
		}
		geoipCache = newCache(gttl, gswr, gneg, gmax)
		logger.Info("GeoIP reader initialized successfully with cache")
	} else {
		logger.Info("GeoIP disabled (no GEOIP_MMDB_PATH or failed to load)")
	}

	// S3 Cold Storage Client (optional - only if STORAGE_S3_BUCKET is configured)
	// Credentials stay in Foghorn; edge nodes receive presigned URLs only
	//
	// IMPORTANT: We use interface types to avoid Go's typed nil pointer issue.
	// A typed nil (*storage.S3Client)(nil) passed as an interface is NOT nil,
	// but calling methods on it panics. Using interface type ensures true nil.
	// We need separate interface variables because different consumers expect
	// different interface types (foghorngrpc.S3ClientInterface vs jobs.S3Client).
	var s3ForGRPC foghorngrpc.S3ClientInterface
	var s3ForJobs jobs.S3Client
	if s3Bucket := config.GetEnv("STORAGE_S3_BUCKET", ""); s3Bucket != "" {
		s3Config := storage.S3Config{
			Bucket:    s3Bucket,
			Prefix:    config.GetEnv("STORAGE_S3_PREFIX", ""),
			Region:    config.GetEnv("STORAGE_S3_REGION", "us-east-1"),
			Endpoint:  config.GetEnv("STORAGE_S3_ENDPOINT", ""),
			AccessKey: config.GetEnv("STORAGE_S3_ACCESS_KEY", ""),
			SecretKey: config.GetEnv("STORAGE_S3_SECRET_KEY", ""),
		}
		client, err := storage.NewS3Client(s3Config, logger)
		if err != nil {
			logger.WithError(err).Error("Failed to initialize S3 client for cold storage")
		} else {
			// Only assign to interfaces if successfully created (avoids typed nil issue)
			s3ForGRPC = client
			s3ForJobs = client
			control.SetS3Client(client)
			control.SetDB(db)
			logger.WithFields(logging.Fields{
				"bucket": s3Bucket,
				"prefix": s3Config.Prefix,
			}).Info("S3 cold storage enabled")
		}
	} else {
		logger.Info("S3 cold storage disabled (no STORAGE_S3_BUCKET configured)")
	}

	// Livepeer Gateway URL (optional - enables H.264 transcoding via Livepeer network)
	if livepeerGatewayURL := config.GetEnv("LIVEPEER_GATEWAY_URL", ""); livepeerGatewayURL != "" {
		control.SetLivepeerGatewayURL(livepeerGatewayURL)
		logger.WithField("gateway_url", livepeerGatewayURL).Info("Livepeer Gateway enabled for H.264 transcoding")
	} else {
		logger.Info("Livepeer Gateway disabled (no LIVEPEER_GATEWAY_URL configured)")
	}

	// Initialize handlers with injected clients
	handlers.Init(db, logger, lb, metrics, decklogClient, commodoreClient, purserClient, qmClient, geoipReader, geoipCache)

	// Initialize trigger processor (Lifted from Handlers)
	triggerProcessor := triggers.NewProcessor(logger, commodoreClient, decklogClient, lb, geoipReader)
	triggerProcessor.SetMetrics(&triggers.ProcessorMetrics{
		DecklogTriggerSends: metricsCollector.NewCounter(
			"decklog_trigger_sends_total",
			"Attempts and results when forwarding MistTriggers to Decklog",
			[]string{"trigger_type", "status"},
		),
	})
	if geoipReader != nil && geoipCache != nil {
		triggerProcessor.SetGeoIPCache(geoipCache)
	}
	triggerProcessor.SetQuartermasterClient(qmClient)
	logger.Info("Initialized trigger processor with Commodore, Decklog and Quartermaster clients")
	handlers.SetTriggerProcessor(triggerProcessor)

	// Start Helmsman control gRPC server with injected dependencies
	control.Init(logger, commodoreClient, triggerProcessor)

	// Configure unified state policies and rehydrate from DB (nodes, DVR, clips, artifacts)
	state.DefaultManager().ConfigurePolicies(state.PoliciesConfig{
		WritePolicies: map[state.EntityType]state.WritePolicy{
			state.EntityClip: {Enabled: true, Mode: state.WriteThrough},
			state.EntityDVR:  {Enabled: true, Mode: state.WriteThrough},
		},
		SyncPolicies: map[state.EntityType]state.SyncPolicy{
			state.EntityClip: {BootRehydrate: true, ReconcileInterval: 180 * time.Second},
			state.EntityDVR:  {BootRehydrate: true, ReconcileInterval: 180 * time.Second},
		},
		ClipRepo:     control.NewClipRepository(),
		DVRRepo:      control.NewDVRRepository(),
		NodeRepo:     control.NewNodeRepository(),
		ArtifactRepo: control.NewArtifactRepository(),
	})

	// Set artifact repository for control server handlers (dual-storage sync)
	control.SetArtifactRepository(control.NewArtifactRepository())

	// Create Foghorn control plane gRPC server (for Commodore: clips, DVR, viewer resolution, VOD uploads)
	foghornServer := foghorngrpc.NewFoghornGRPCServer(db, logger, lb, geoipReader, decklogClient, s3ForGRPC, purserClient)

	// Wire DVR service to trigger processor for auto-start recordings on stream start
	triggerProcessor.SetDVRService(foghornServer)

	// Wire cache invalidator for instant tenant reactivation (Purser → Commodore → Foghorn)
	foghornServer.SetCacheInvalidator(triggerProcessor)

	// Start unified gRPC server with both Helmsman control and Foghorn control plane services
	controlAddr := config.RequireEnv("FOGHORN_CONTROL_BIND_ADDR")
	if _, err := control.StartGRPCServer(control.GRPCServerConfig{
		Addr:         controlAddr,
		Logger:       logger,
		ServiceToken: serviceToken,
		Registrars:   []control.ServiceRegistrar{foghornServer.RegisterServices},
	}); err != nil {
		logger.WithError(err).Fatal("Failed to start control gRPC server")
	}

	// Start the hourly storage snapshot scheduler
	go startStorageSnapshotScheduler(triggerProcessor, logger)

	// Start retention job (marks expired assets as deleted)
	retentionJob := jobs.NewRetentionJob(jobs.RetentionConfig{
		DB:            db,
		Logger:        logger,
		Interval:      1 * time.Hour,
		RetentionDays: 30, // Default 30 days
		DecklogClient: decklogClient,
	})
	retentionJob.Start()
	defer retentionJob.Stop()

	// Start orphan reconciliation job (retries failed deletions)
	orphanCleanupJob := jobs.NewOrphanCleanupJob(jobs.OrphanCleanupConfig{
		DB:       db,
		Logger:   logger,
		Interval: 5 * time.Minute,
		MaxAge:   30 * time.Minute,
	})
	orphanCleanupJob.Start()
	defer orphanCleanupJob.Stop()

	// Start purge job (hard-deletes old soft-deleted records)
	purgeDeletedJob := jobs.NewPurgeDeletedJob(jobs.PurgeDeletedConfig{
		DB:           db,
		Logger:       logger,
		Interval:     24 * time.Hour,
		RetentionAge: 30 * 24 * time.Hour, // 30 days
		S3Client:     s3ForJobs,
	})
	purgeDeletedJob.Start()
	defer purgeDeletedJob.Stop()

	// Setup router with unified monitoring (health/metrics only - all API routes now gRPC)
	router := server.SetupServiceRouter(logger, "foghorn", healthChecker, metricsCollector)

	// Nodes overview for debugging (kept as HTTP for quick inspection)
	router.GET("/nodes/overview", handlers.HandleNodesOverview)

	// Root page debug interface
	router.GET("/dashboard", handlers.HandleRootPage)
	router.GET("/debug/cache/stream-context", handlers.HandleStreamContextCache)

	// Viewer playback routes - generic player redirects via edge.* domain
	router.GET("/play/*path", handlers.HandleGenericViewerPlayback)
	router.GET("/resolve/*path", handlers.HandleGenericViewerPlayback)

	// MistServer Compatibility - stream key routing for Helmsman/MistServer
	router.NoRoute(handlers.MistServerCompatibilityHandler)

	// Start server with graceful shutdown
	serverConfig := server.DefaultConfig("foghorn", "18008")
	if err := server.Start(serverConfig, router, logger); err != nil {
		logger.WithError(err).Fatal("Server startup failed")
	}
}

func startStorageSnapshotScheduler(p *triggers.Processor, logger logging.Logger) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()

	for range ticker.C {
		if err := p.GenerateAndSendStorageSnapshots(); err != nil {
			logger.WithError(err).Error("Failed to generate and send storage snapshots")
		}
	}
}
