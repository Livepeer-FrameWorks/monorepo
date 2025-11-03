package main

import (
	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/handlers"
	"frameworks/api_balancing/internal/state"
	"frameworks/pkg/cache"
	"frameworks/pkg/config"
	"frameworks/pkg/database"
	"frameworks/pkg/logging"
	"frameworks/pkg/monitoring"
	"frameworks/pkg/server"
	"frameworks/pkg/version"
	"time"
)

func main() {
	// Initialize logger
	logger := logging.NewLoggerWithService("foghorn")

	// Load environment variables
	config.LoadEnv(logger)

	logger.WithField("service", "foghorn").Info("Starting Foghorn Load Balancer")

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

	// Create custom load balancing metrics
	metrics := &handlers.FoghornMetrics{
		RoutingDecisions:        metricsCollector.NewCounter("routing_decisions_total", "Routing decisions made", []string{"algorithm", "selected_node"}),
		NodeSelectionDuration:   metricsCollector.NewHistogram("node_selection_duration_seconds", "Node selection latency", []string{}, nil),
		LoadDistribution:        metricsCollector.NewGauge("load_distribution_ratio", "Load distribution ratio", []string{"node_id"}),
		HealthScoreCalculations: metricsCollector.NewCounter("health_score_calculations_total", "Health score calculations", []string{}),
	}

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

	// Initialize handlers
	handlers.Init(db, logger, lb, metrics)

	// Start Helmsman control gRPC server
	control.Init(logger)

	// Configure unified state policies and rehydrate from DB (nodes, DVR, clips)
	state.DefaultManager().ConfigurePolicies(state.PoliciesConfig{
		WritePolicies: map[state.EntityType]state.WritePolicy{
			state.EntityClip: {Enabled: true, Mode: state.WriteThrough},
			state.EntityDVR:  {Enabled: true, Mode: state.WriteThrough},
		},
		SyncPolicies: map[state.EntityType]state.SyncPolicy{
			state.EntityClip: {BootRehydrate: true, ReconcileInterval: 180 * time.Second},
			state.EntityDVR:  {BootRehydrate: true, ReconcileInterval: 180 * time.Second},
		},
		ClipRepo: control.NewClipRepository(),
		DVRRepo:  control.NewDVRRepository(),
		NodeRepo: control.NewNodeRepository(),
	})
	controlAddr := config.RequireEnv("FOGHORN_CONTROL_BIND_ADDR")
	if _, err := control.StartGRPCServer(controlAddr, logger); err != nil {
		logger.WithError(err).Fatal("Failed to start control gRPC server")
	}

	// Setup router with unified monitoring
	router := server.SetupServiceRouter(logger, "foghorn", healthChecker, metricsCollector)

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
	router.POST("/viewer/resolve-endpoint", handlers.HandleResolveViewerEndpoint)
	// Stream meta endpoint
	router.POST("/viewer/stream-meta", handlers.HandleStreamMeta)

	// Root page debug interface (takes precedence over MistServer compatibility)
	router.GET("/dashboard", handlers.HandleRootPage)

	// MistServer Compatibility - all requests including capability filtering via query params
	router.NoRoute(handlers.MistServerCompatibilityHandler)

	// Start server with graceful shutdown
	serverConfig := server.DefaultConfig("foghorn", "18008")
	if err := server.Start(serverConfig, router, logger); err != nil {
		logger.WithError(err).Fatal("Server startup failed")
	}
}
