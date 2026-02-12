package main

import (
	"context"
	"fmt"
	"frameworks/api_balancing/internal/balancer"
	foghornconfig "frameworks/api_balancing/internal/config"
	"frameworks/api_balancing/internal/control"
	"frameworks/api_balancing/internal/federation"
	foghorngrpc "frameworks/api_balancing/internal/grpc"
	"frameworks/api_balancing/internal/handlers"
	"frameworks/api_balancing/internal/jobs"
	"frameworks/api_balancing/internal/state"
	"frameworks/api_balancing/internal/storage"
	"frameworks/api_balancing/internal/triggers"
	"frameworks/pkg/cache"
	"frameworks/pkg/clients/commodore"
	"frameworks/pkg/clients/decklog"
	foghornpool "frameworks/pkg/clients/foghorn"
	navclient "frameworks/pkg/clients/navigator"
	purserclient "frameworks/pkg/clients/purser"
	qmclient "frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/config"
	"frameworks/pkg/database"
	"frameworks/pkg/geoip"
	"frameworks/pkg/logging"
	"frameworks/pkg/monitoring"
	pb "frameworks/pkg/proto"
	pkgredis "frameworks/pkg/redis"
	"frameworks/pkg/server"
	"frameworks/pkg/version"
	"github.com/prometheus/client_golang/prometheus"
	goredis "github.com/redis/go-redis/v9"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

type clientState struct {
	mu               sync.RWMutex
	quartermasterOK  bool
	quartermasterErr error
	commodoreOK      bool
	commodoreErr     error
}

func (cs *clientState) setQuartermaster(ok bool, err error) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.quartermasterOK = ok
	cs.quartermasterErr = err
}

func (cs *clientState) setCommodore(ok bool, err error) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.commodoreOK = ok
	cs.commodoreErr = err
}

func (cs *clientState) quartermasterStatus() (bool, error) {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.quartermasterOK, cs.quartermasterErr
}

func (cs *clientState) commodoreStatus() (bool, error) {
	cs.mu.RLock()
	defer cs.mu.RUnlock()
	return cs.commodoreOK, cs.commodoreErr
}

func main() {
	// Initialize logger
	logger := logging.NewLoggerWithService("foghorn")
	state.SetLogger(logger)

	// Load environment variables
	config.LoadEnv(logger)
	foghornCfg := foghornconfig.Load()
	control.SetLocalClusterID(foghornCfg.ClusterID)

	// Storage base path for defrost operations when node has no StorageLocal.
	// Must match Helmsman's HELMSMAN_STORAGE_LOCAL_PATH for path reconstruction.
	if storageBase := config.GetEnv("FOGHORN_DEFAULT_STORAGE_BASE", ""); storageBase != "" {
		if !filepath.IsAbs(storageBase) {
			logger.WithField("path", storageBase).Fatal("FOGHORN_DEFAULT_STORAGE_BASE must be absolute path")
		}
		control.SetDefaultStorageBase(storageBase)
		logger.WithField("storage_base", storageBase).Info("Using custom default storage base")
	}

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

	instanceID := config.GetEnv("FOGHORN_INSTANCE_ID", "")
	if instanceID == "" {
		instanceID = fmt.Sprintf("foghorn-%d", time.Now().UnixNano())
		if foghornCfg.Redis.Mode != "" || foghornCfg.RedisURL != "" {
			logger.Warn("FOGHORN_INSTANCE_ID not set but Redis is configured — ephemeral ID will not persist across restarts, breaking HA state sync and leader election")
		}
	}

	var redisClient goredis.UniversalClient
	if foghornCfg.Redis.Mode != "" {
		var err error
		redisClient, err = pkgredis.NewUniversalClient(context.Background(), foghornCfg.Redis)
		if err != nil {
			logger.WithError(err).Warn("Failed to initialize redis (universal mode)")
			redisClient = nil
		}
	} else if foghornCfg.RedisURL != "" {
		client, err := pkgredis.NewClientFromURL(context.Background(), foghornCfg.RedisURL)
		if err != nil {
			logger.WithError(err).Warn("Failed to initialize redis state store")
		} else {
			redisClient = client
		}
	}
	var redisStore *state.RedisStateStore
	if redisClient != nil {
		redisStore = state.NewRedisStateStore(redisClient, foghornCfg.ClusterID)
		if err := state.DefaultManager().EnableRedisSync(context.Background(), redisStore, instanceID, logger); err != nil {
			logger.WithError(err).Warn("Failed to enable redis state synchronization")
		}
	}

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
	clients := &clientState{}
	clientStatusGauge := metricsCollector.NewGauge(
		"control_plane_client_status",
		"Control plane client availability (1=ok, 0=unavailable)",
		[]string{"client"},
	)
	clientReconnects := metricsCollector.NewCounter(
		"control_plane_client_reconnect_total",
		"Control plane client reconnect attempts",
		[]string{"client", "status"},
	)

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
	healthChecker.AddCheck("quartermaster", func() monitoring.CheckResult {
		ok, err := clients.quartermasterStatus()
		if !ok {
			msg := "Quartermaster unavailable"
			if err != nil {
				msg = fmt.Sprintf("Quartermaster unavailable: %v", err)
			}
			return monitoring.CheckResult{
				Status:  monitoring.StatusDegraded,
				Message: msg,
			}
		}
		return monitoring.CheckResult{Status: monitoring.StatusHealthy}
	})
	healthChecker.AddCheck("commodore", func() monitoring.CheckResult {
		ok, err := clients.commodoreStatus()
		if !ok {
			msg := "Commodore unavailable"
			if err != nil {
				msg = fmt.Sprintf("Commodore unavailable: %v", err)
			}
			return monitoring.CheckResult{
				Status:  monitoring.StatusDegraded,
				Message: msg,
			}
		}
		return monitoring.CheckResult{Status: monitoring.StatusHealthy}
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
		logger.WithError(err).Error("Failed to create Quartermaster gRPC client - starting in degraded mode")
		clients.setQuartermaster(false, err)
		clientStatusGauge.WithLabelValues("quartermaster").Set(0)
		qmClient = nil
	} else {
		clients.setQuartermaster(true, nil)
		clientStatusGauge.WithLabelValues("quartermaster").Set(1)
	}
	if qmClient != nil {
		defer func() { _ = qmClient.Close() }()
	}

	// Commodore (gRPC)
	commodoreGRPCURL := config.GetEnv("COMMODORE_GRPC_ADDR", "localhost:19001")

	// Commodore Cache
	ttl := 60 * time.Second
	if v := config.GetEnv("COMMODORE_CACHE_TTL", ""); v != "" {
		if d, errParse := time.ParseDuration(v); errParse == nil {
			ttl = d
		}
	}
	swr := 30 * time.Second
	if v := config.GetEnv("COMMODORE_CACHE_SWR", ""); v != "" {
		if d, errParse := time.ParseDuration(v); errParse == nil {
			swr = d
		}
	}
	neg := 10 * time.Second
	if v := config.GetEnv("COMMODORE_CACHE_NEG_TTL", ""); v != "" {
		if d, errParse := time.ParseDuration(v); errParse == nil {
			neg = d
		}
	}
	maxEntries := 10000
	if v := config.GetEnv("COMMODORE_CACHE_MAX", ""); v != "" {
		if n, errParse := strconv.Atoi(v); errParse == nil && n > 0 {
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
		logger.WithError(err).Error("Failed to create Commodore gRPC client - starting in degraded mode")
		clients.setCommodore(false, err)
		clientStatusGauge.WithLabelValues("commodore").Set(0)
		commodoreClient = nil
	} else {
		clients.setCommodore(true, nil)
		clientStatusGauge.WithLabelValues("commodore").Set(1)
	}
	if commodoreClient != nil {
		defer func() { _ = commodoreClient.Close() }()
	}

	// Navigator (gRPC) - wildcard certificate retrieval for edge ConfigSeed
	navigatorAddr := config.GetEnv("NAVIGATOR_GRPC_ADDR", "navigator:19004")
	navigatorClient, err := navclient.NewClient(navclient.Config{
		Addr:         navigatorAddr,
		Timeout:      10 * time.Second,
		Logger:       logger,
		ServiceToken: serviceToken,
	})
	if err != nil {
		logger.WithError(err).Warn("Failed to create Navigator gRPC client - TLS bundles will not be seeded")
		navigatorClient = nil
	} else {
		defer navigatorClient.Close()
		control.SetNavigatorClient(navigatorClient)
	}
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
	var s3ForFederation *storage.S3Client
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
			s3ForFederation = client
			control.SetS3Client(client)
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

	// --- Federation (cross-cluster stream routing) ---
	federationEnabled := config.GetEnv("FEDERATION_ENABLED", "false") == "true"
	var federationServer *federation.FederationServer
	var peerManager *federation.PeerManager
	var remoteEdgeCache *federation.RemoteEdgeCache
	var fedClient *federation.FederationClient
	if federationEnabled && redisClient != nil && qmClient != nil {
		remoteEdgeCache = federation.NewRemoteEdgeCache(redisClient, foghornCfg.ClusterID, logger)

		federationServer = federation.NewFederationServer(federation.FederationServerConfig{
			Logger:    logger,
			LB:        lb,
			ClusterID: foghornCfg.ClusterID,
			Cache:     remoteEdgeCache,
			DB:        db,
			S3Client:  s3ForFederation,
		})

		fedPool := foghornpool.NewPool(foghornpool.PoolConfig{
			ServiceToken: serviceToken,
			Logger:       logger,
		})
		defer fedPool.Close()

		peerManager = federation.NewPeerManager(federation.PeerManagerConfig{
			ClusterID:     foghornCfg.ClusterID,
			InstanceID:    instanceID,
			Pool:          fedPool,
			QM:            qmClient,
			Cache:         remoteEdgeCache,
			Logger:        logger,
			DecklogClient: decklogClient,
			SelfGeoFunc:   handlers.GetSelfGeo,
		})
		defer peerManager.Close()

		fedClient = federation.NewFederationClient(federation.FederationClientConfig{
			Pool:   fedPool,
			Logger: logger,
		})

		logger.WithField("cluster_id", foghornCfg.ClusterID).Info("Federation enabled")
	} else if federationEnabled {
		logger.Warn("Federation enabled but missing prerequisites (redis and/or quartermaster)")
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
	if peerManager != nil {
		triggerProcessor.SetPeerNotifier(peerManager)
	}
	logger.Info("Initialized trigger processor with Commodore, Decklog and Quartermaster clients")
	handlers.SetTriggerProcessor(triggerProcessor)

	if qmClient == nil {
		go reconnectQuartermaster(quartermasterGRPCURL, serviceToken, logger, clients, clientStatusGauge, clientReconnects, triggerProcessor)
	}
	if commodoreClient == nil {
		go reconnectCommodore(commodoreGRPCURL, serviceToken, logger, commodoreCache, clients, clientStatusGauge, clientReconnects, triggerProcessor)
	}

	// Start Helmsman control gRPC server with injected dependencies
	control.Init(logger, commodoreClient, triggerProcessor)
	control.SetGeoIPCache(geoipCache)

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
	foghornServer := foghorngrpc.NewFoghornGRPCServer(db, logger, lb, geoipReader, geoipCache, decklogClient, s3ForGRPC, purserClient)
	control.SetDecklogClient(decklogClient)
	control.SetDVRStopRegistry(foghornServer)

	// Wire DVR service to trigger processor for auto-start recordings on stream start
	triggerProcessor.SetDVRService(foghornServer)

	// Wire cache invalidator for instant tenant reactivation (Purser → Commodore → Foghorn)
	foghornServer.SetCacheInvalidator(triggerProcessor)

	// Wire federation remote edge cache for cross-cluster viewer routing
	if remoteEdgeCache != nil {
		foghornServer.SetRemoteEdgeCache(remoteEdgeCache, foghornCfg.ClusterID)
		handlers.SetRemoteEdgeCache(remoteEdgeCache)
	}
	if fedClient != nil {
		handlers.SetFederationClient(fedClient)
		foghornServer.SetFederationClient(fedClient)
	}
	if peerManager != nil {
		handlers.SetPeerManager(peerManager)
		foghornServer.SetPeerManager(peerManager)
	}
	if federationServer != nil {
		federationServer.SetClipCreator(foghornServer)
		federationServer.SetDVRCreator(foghornServer)
		federationServer.SetArtifactCommandHandler(foghornServer)
	}

	// HA relay: register at QM to discover mesh address, enable cross-instance command forwarding.
	// QM derives the advertise address from the node's mesh identity (wireguard_ip > internal_ip).
	var relayServer *foghorngrpc.RelayServer
	if redisStore != nil && qmClient != nil {
		bsReq := &pb.BootstrapServiceRequest{
			Type:      "foghorn",
			Version:   version.Version,
			Protocol:  "grpc",
			Port:      int32(config.GetEnvInt("FOGHORN_GRPC_PORT", 18019)),
			ClusterId: &foghornCfg.ClusterID,
		}
		if nodeID := config.GetEnv("NODE_ID", ""); nodeID != "" {
			bsReq.NodeId = &nodeID
		}
		if host := config.GetEnv("FOGHORN_HOST", ""); host != "" {
			bsReq.AdvertiseHost = &host
		}

		bsCtx, bsCancel := context.WithTimeout(context.Background(), 10*time.Second)
		bsResp, bsErr := qmClient.BootstrapService(bsCtx, bsReq)
		bsCancel()

		advertiseAddr := ""
		if bsErr != nil {
			logger.WithError(bsErr).Warn("BootstrapService failed — HA relay disabled")
		} else {
			advertiseAddr = bsResp.GetAdvertiseAddr()
		}

		if advertiseAddr != "" {
			relayPool := foghornpool.NewPool(foghornpool.PoolConfig{
				ServiceToken: serviceToken,
				Timeout:      10 * time.Second,
				Logger:       logger,
			})
			defer relayPool.Close()

			control.InitRelay(redisStore, instanceID, advertiseAddr, &relayPoolAdapter{pool: relayPool}, logger)
			relayServer = foghorngrpc.NewRelayServer(logger)

			logger.WithFields(logging.Fields{
				"instance_id":    instanceID,
				"advertise_addr": advertiseAddr,
			}).Info("HA command relay enabled")
		}
	}

	// Start unified gRPC server with both Helmsman control and Foghorn control plane services
	controlAddr := config.RequireEnv("FOGHORN_CONTROL_BIND_ADDR")
	registrars := []control.ServiceRegistrar{foghornServer.RegisterServices}
	if federationServer != nil {
		registrars = append(registrars, federationServer.RegisterServices)
	}
	if relayServer != nil {
		registrars = append(registrars, relayServer.RegisterServices)
	}
	if _, err := control.StartGRPCServer(control.GRPCServerConfig{
		Addr:         controlAddr,
		Logger:       logger,
		ServiceToken: serviceToken,
		Registrars:   registrars,
	}); err != nil {
		logger.WithError(err).Fatal("Failed to start control gRPC server")
	}

	// Start cert refresh loop (re-pushes ConfigSeed when Navigator renews wildcard certs)
	certRefreshCtx, certRefreshCancel := context.WithCancel(context.Background())
	defer certRefreshCancel()
	go control.StartCertRefreshLoop(certRefreshCtx, 1*time.Hour, logger)

	// Bulk-load served cluster assignments from DB and refresh every 5 minutes
	control.LoadServedClusters()
	clusterRefreshCtx, clusterRefreshCancel := context.WithCancel(context.Background())
	defer clusterRefreshCancel()
	go control.StartServedClustersRefresh(clusterRefreshCtx, 5*time.Minute, logger)

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

	// Start stale defrost cleanup job (resets stuck defrosting artifacts)
	staleDefrostJob := jobs.NewStaleDefrostCleanupJob(jobs.StaleDefrostCleanupConfig{
		DB:         db,
		Logger:     logger,
		Interval:   1 * time.Minute,
		StaleAfter: 10 * time.Minute,
	})
	staleDefrostJob.Start()
	defer staleDefrostJob.Stop()

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
	router.PUT("/nodes/:node_id/mode", handlers.HandleSetNodeMaintenanceMode)
	router.GET("/nodes/:node_id/drain-status", handlers.HandleGetNodeDrainStatus)

	// Root page debug interface
	router.GET("/dashboard", handlers.HandleRootPage)
	router.GET("/debug/cache/stream-context", handlers.HandleStreamContextCache)
	router.GET("/debug/served-clusters", handlers.HandleServedClusters)

	// Viewer playback routes - generic player redirects via foghorn.* domain
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

func reconnectQuartermaster(
	grpcAddr string,
	serviceToken string,
	logger logging.Logger,
	clients *clientState,
	statusGauge *prometheus.GaugeVec,
	reconnects *prometheus.CounterVec,
	triggerProcessor *triggers.Processor,
) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		client, err := qmclient.NewGRPCClient(qmclient.GRPCConfig{
			GRPCAddr:     grpcAddr,
			Timeout:      30 * time.Second,
			Logger:       logger,
			ServiceToken: serviceToken,
		})
		if err != nil {
			clients.setQuartermaster(false, err)
			statusGauge.WithLabelValues("quartermaster").Set(0)
			reconnects.WithLabelValues("quartermaster", "failure").Inc()
			logger.WithError(err).Warn("Quartermaster reconnect failed")
			continue
		}
		clients.setQuartermaster(true, nil)
		statusGauge.WithLabelValues("quartermaster").Set(1)
		reconnects.WithLabelValues("quartermaster", "success").Inc()
		handlers.SetQuartermasterClient(client)
		if triggerProcessor != nil {
			triggerProcessor.SetQuartermasterClient(client)
		}
		logger.Info("Quartermaster reconnected")
		return
	}
}

func reconnectCommodore(
	grpcAddr string,
	serviceToken string,
	logger logging.Logger,
	commodoreCache *cache.Cache,
	clients *clientState,
	statusGauge *prometheus.GaugeVec,
	reconnects *prometheus.CounterVec,
	triggerProcessor *triggers.Processor,
) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		client, err := commodore.NewGRPCClient(commodore.GRPCConfig{
			GRPCAddr:     grpcAddr,
			Timeout:      30 * time.Second,
			Logger:       logger,
			Cache:        commodoreCache,
			ServiceToken: serviceToken,
		})
		if err != nil {
			clients.setCommodore(false, err)
			statusGauge.WithLabelValues("commodore").Set(0)
			reconnects.WithLabelValues("commodore", "failure").Inc()
			logger.WithError(err).Warn("Commodore reconnect failed")
			continue
		}
		clients.setCommodore(true, nil)
		statusGauge.WithLabelValues("commodore").Set(1)
		reconnects.WithLabelValues("commodore", "success").Inc()
		handlers.SetCommodoreClient(client)
		control.SetCommodoreClient(client)
		if triggerProcessor != nil {
			triggerProcessor.SetCommodoreClient(client)
		}
		logger.Info("Commodore reconnected")
		return
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

// relayPoolAdapter wraps FoghornPool to satisfy control.CommandRelayPool.
type relayPoolAdapter struct {
	pool *foghornpool.FoghornPool
}

func (a *relayPoolAdapter) GetOrCreate(key, addr string) (control.CommandRelayClient, error) {
	client, err := a.pool.GetOrCreate(key, addr)
	if err != nil {
		return nil, err
	}
	return client, nil
}
