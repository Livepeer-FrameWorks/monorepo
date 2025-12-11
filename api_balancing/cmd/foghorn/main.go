package main

import (
	"frameworks/api_balancing/internal/balancer"
	"frameworks/api_balancing/internal/control"
	foghorngrpc "frameworks/api_balancing/internal/grpc"
	"frameworks/api_balancing/internal/handlers"
	"frameworks/api_balancing/internal/state"
	"frameworks/api_balancing/internal/triggers"
	"frameworks/pkg/cache"
	"frameworks/pkg/clients/commodore"
	"frameworks/pkg/clients/decklog"
	qmclient "frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/config"
	"frameworks/pkg/database"
	"frameworks/pkg/geoip"
	"frameworks/pkg/logging"
	"frameworks/pkg/monitoring"
	"frameworks/pkg/server"
	"frameworks/pkg/version"
	"strconv"
	"strings"
	"time"
)

func main() {
	// Initialize logger
	logger := logging.NewLoggerWithService("foghorn")

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

	// Create custom load balancing metrics
	metrics := &handlers.FoghornMetrics{
		RoutingDecisions:      metricsCollector.NewCounter("routing_decisions_total", "Routing decisions made", []string{"algorithm", "selected_node"}),
		NodeSelectionDuration: metricsCollector.NewHistogram("node_selection_duration_seconds", "Node selection latency", []string{}, nil),
		LoadDistribution:      metricsCollector.NewGauge("load_distribution_ratio", "Load distribution ratio", []string{"node_id"}),
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

	// --- Initialize Clients (Lifted from Handlers) ---

	// Decklog
	decklogURL := config.GetEnv("DECKLOG_URL", "decklog:18006")
	address := strings.TrimPrefix(decklogURL, "http://")
	address = strings.TrimPrefix(address, "https://")
	allowInsecure := config.GetEnv("DECKLOG_USE_TLS", "false") != "true"
	decklogConfig := decklog.BatchedClientConfig{
		Target:        address,
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
		logger.Debug("GeoIP disabled (no GEOIP_MMDB_PATH or failed to load)")
	}

	// Initialize handlers with injected clients
	handlers.Init(db, logger, lb, metrics, decklogClient, commodoreClient, qmClient, geoipReader, geoipCache)

	// Initialize trigger processor (Lifted from Handlers)
	triggerProcessor := triggers.NewProcessor(logger, commodoreClient, decklogClient, lb, geoipReader)
	if geoipReader != nil && geoipCache != nil {
		triggerProcessor.SetGeoIPCache(geoipCache)
	}
	logger.Info("Initialized trigger processor with Commodore and Decklog clients")

	// Start Helmsman control gRPC server with injected dependencies
	control.Init(logger, commodoreClient, triggerProcessor)

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

	// Create Foghorn control plane gRPC server (for Commodore: clips, DVR, viewer resolution)
	foghornServer := foghorngrpc.NewFoghornGRPCServer(db, logger, lb, geoipReader, decklogClient)

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

	// Setup router with unified monitoring (health/metrics only - all API routes now gRPC)
	router := server.SetupServiceRouter(logger, "foghorn", healthChecker, metricsCollector)

	// Nodes overview for debugging (kept as HTTP for quick inspection)
	router.GET("/nodes/overview", handlers.HandleNodesOverview)

	// Root page debug interface
	router.GET("/dashboard", handlers.HandleRootPage)

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
