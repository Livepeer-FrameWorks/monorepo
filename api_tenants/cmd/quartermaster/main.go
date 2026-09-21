package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"time"

	"frameworks/api_tenants/internal/appconfig"
	"frameworks/api_tenants/internal/database/quartermasterdb"
	quartermastermigrations "frameworks/api_tenants/internal/datamigrations"
	qmgrpc "frameworks/api_tenants/internal/grpc"
	"frameworks/api_tenants/internal/handlers"
	commodoreclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/commodore"
	decklogclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/decklog"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/navigator"
	purserclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/purser"
	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/datamigrate"
	dns "github.com/Livepeer-FrameWorks/monorepo/pkg/dns"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	eventoutbox "github.com/Livepeer-FrameWorks/monorepo/pkg/events/outbox"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/geoip"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/monitoring"
	dnspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/dns"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/qmbootstrap"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/server"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/version"
	"github.com/gin-gonic/gin"
	"google.golang.org/grpc"
)

// servedClustersForInstanceName resolves the active service_cluster_assignments
// clusters for a service instance (by instance_id), so the health-poll wake can
// refresh the pooled record of every media cluster it serves. Best-effort (reconcile
// is the backstop), but query/scan/iteration failures are logged at Warn so a
// recurring resolution failure is visible — matching the server-side servedClusters
// helper, not silently dropped.
func servedClustersForInstanceName(ctx context.Context, db database.PostgresConn, log logging.Logger, instanceName, serviceType string) []string {
	rows, err := quartermasterdb.New(db).ListServedClustersForInstance(ctx, quartermasterdb.ListServedClustersForInstanceParams{
		InstanceID: instanceName, ServiceType: serviceType,
	})
	if err != nil {
		log.WithError(err).Warn("Failed to resolve served clusters for health-poll DNS wake; reconcile loop will converge")
		return nil
	}
	out := make([]string, 0, len(rows))
	for _, c := range rows {
		if strings.TrimSpace(c) != "" {
			out = append(out, c)
		}
	}
	return out
}

func quartermasterPurserClientConfig(cfg *appconfig.Quartermaster, logger logging.Logger) purserclient.GRPCConfig {
	return purserclient.GRPCConfig{
		GRPCAddr:           cfg.PurserGRPCAddr,
		Timeout:            5 * time.Second,
		Logger:             logger,
		ServiceToken:       cfg.ServiceToken,
		PreferServiceToken: true,
		AllowInsecure:      cfg.AllowInsecure,
		CACertFile:         cfg.CAPath,
		ServerName:         cfg.PurserGRPCTLSServerName,
	}
}

func main() {
	if version.HandleCLI() {
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "data-migrations" {
		logger := logging.NewLoggerWithService("quartermaster")
		config.LoadEnv(logger)
		quartermastermigrations.Register()
		pg, err := config.Load[config.Postgres](config.Options{Service: "quartermaster", Logger: logger})
		if err != nil {
			logger.WithError(err).Fatal("Invalid data migration configuration")
		}
		dbConfig := database.DefaultConfig()
		dbConfig.ServiceName = "quartermaster"
		dbConfig.URL = pg.DatabaseURL
		err = datamigrate.HandleArgv(context.Background(), func() (*sql.DB, error) {
			return database.Connect(dbConfig, logger)
		}, os.Stdout, os.Args[1:])
		if err != nil && !errors.Is(err, datamigrate.ErrNotDataMigrationsCommand) {
			logger.WithError(err).Fatal("Data migration command failed")
		}
		return
	}

	// Bootstrap subcommand dispatcher. The Ansible go_service role invokes the
	// binary with no args to start the gRPC+HTTP server; "bootstrap" is the
	// only subcommand and is invoked explicitly by the bootstrap role.
	if len(os.Args) > 1 && os.Args[1] == "bootstrap" {
		os.Exit(runBootstrapCommand(os.Args[2:]))
	}

	// Setup logger
	logger := logging.NewLoggerWithService("quartermaster")

	// Load environment variables
	config.LoadEnv(logger)

	logger.Info("Starting Quartermaster (Tenant Management API)")

	configOptions := config.Options{Service: "quartermaster", Logger: logger}
	cfg, err := config.Load[appconfig.Quartermaster](configOptions)
	if err != nil {
		logger.WithError(err).Fatal("Invalid configuration")
	}
	cfg.ApplyLogLevel(logger)
	metadataPolicy, err := middleware.ParseMetadataPolicy(cfg.MetadataPolicy)
	if err != nil {
		logger.WithError(err).Fatal("Invalid configuration")
	}
	geoipReader, err := geoip.Open(cfg.MMDBPath)
	if err != nil {
		logger.WithError(err).Warn("GeoIP unavailable; continuing without geolocation")
	}
	if geoipReader != nil {
		logger.WithField("provider", geoipReader.GetProvider()).Info("GeoIP reader loaded")
	}
	liveConfig := config.NewLive(cfg, configOptions)
	consentReviewPrivateKey, err := consentReviewSigningConfig(cfg.ConsentReviewKeyID, cfg.ConsentReviewPrivateKeyPEMB64)
	if err != nil {
		logger.WithError(err).Fatal("Invalid capacity consent review configuration")
	}

	// Connect to database
	dbConfig := database.DefaultConfig()
	dbConfig.ServiceName = "quartermaster"
	dbConfig.URL = cfg.DatabaseURL
	db := database.MustConnect(dbConfig, logger)
	defer func() { _ = db.Close() }()

	// Setup monitoring
	healthChecker := monitoring.NewHealthChecker("quartermaster", version.Version)
	metricsCollector := monitoring.NewMetricsCollector("quartermaster", version.Version, version.GitCommit)

	// Add health checks
	healthChecker.AddCheck("database", monitoring.DatabaseHealthCheck(db))

	readiness := monitoring.NewReadinessChecker("quartermaster", version.Version)
	readiness.AddCheck("database", monitoring.DatabaseHealthCheck(db))

	// Per-method counts + duration are captured by GRPCMetricsInterceptor
	// on the GRPCRequests / GRPCDuration vectors; separate tenant_/cluster_/
	// node_/service_operations counters would only rename the same axis.
	serverMetrics := &qmgrpc.ServerMetrics{
		GRPCRequests:                  metricsCollector.NewCounter("grpc_requests_total", "Total gRPC requests", []string{"method", "status"}),
		GRPCDuration:                  metricsCollector.NewHistogram("grpc_request_duration_seconds", "gRPC request duration", []string{"method"}, nil),
		SyncMeshPhaseDuration:         metricsCollector.NewHistogram("sync_mesh_phase_duration_seconds", "SyncMesh phase duration", []string{"phase"}, nil),
		NodeIdentityRejections:        metricsCollector.NewCounter("node_identity_rejections_total", "Node fingerprint identity proof rejections", []string{"reason"}),
		BillingEntitlementStale:       metricsCollector.NewCounter("billing_entitlement_stale_total", "Stale Purser billing entitlement observations rejected", nil),
		DNSBackstopRepairs:            metricsCollector.NewCounter("dns_backstop_repairs_total", "DNS desired/applied drift repairs enqueued", []string{"resource", "action"}),
		NavigatorOutboxFailures:       metricsCollector.NewCounter("navigator_outbox_failures_total", "Navigator outbox delivery failures", []string{"resource"}),
		NavigatorOutboxPending:        metricsCollector.NewGauge("navigator_outbox_pending", "Incomplete Navigator outbox rows", []string{"resource"}),
		ControlCellReassignments:      metricsCollector.NewGauge("control_cell_reassignments", "Tenant-private clusters by control-cell reassignment state", []string{"state"}),
		MediaAuthorityRefreshPending:  metricsCollector.NewGauge("media_authority_refresh_pending", "Pending media-authority refresh obligations", nil),
		MediaAuthorityRefreshOldest:   metricsCollector.NewGauge("media_authority_refresh_oldest_pending_seconds", "Age of the oldest pending media-authority refresh obligation", nil),
		MediaAuthorityRefreshFailures: metricsCollector.NewCounter("media_authority_refresh_failures_total", "Media-authority refresh failures", []string{"stage"}),
	}
	for _, stage := range []string{"commodore_delivery", "complete", "superseded_release", "completion_fence_miss", "observe"} {
		serverMetrics.MediaAuthorityRefreshFailures.WithLabelValues(stage).Add(0)
	}

	// Initialize Navigator client
	var navigatorClient *navigator.Client

	if cfg.NavigatorGRPCAddr != "" {
		navigatorClient, err = navigator.NewClient(navigator.Config{
			Addr:          cfg.NavigatorGRPCAddr,
			Timeout:       5 * time.Second,
			Logger:        logger,
			ServiceToken:  cfg.ServiceToken,
			AllowInsecure: cfg.AllowInsecure,
			CACertFile:    cfg.CAPath,
			ServerName:    cfg.NavigatorGRPCTLSServerName,
		})
		if err != nil {
			logger.WithError(err).Error("Failed to create Navigator client - DNS features will be disabled")
		} else {
			defer func() { _ = navigatorClient.Close() }() // Ensure the client connection is closed
		}
	} else {
		logger.Info("NAVIGATOR_GRPC_ADDR not set - DNS features will be disabled")
	}

	// Create Decklog gRPC client for service events
	decklogClient, err := decklogclient.NewBatchedClient(decklogclient.BatchedClientConfig{
		Target:        cfg.DecklogGRPCAddr,
		AllowInsecure: cfg.AllowInsecure,
		CACertFile:    cfg.CAPath,
		ServerName:    cfg.DecklogGRPCTLSServerName,
		Timeout:       5 * time.Second,
		Source:        "quartermaster",
		ServiceToken:  cfg.ServiceToken,
		ClusterID:     cfg.ClusterID,
		SourceRegion:  cfg.Region,
	}, logger)
	if err != nil {
		logger.WithError(err).Warn("Failed to create Decklog gRPC client - service events will be disabled")
		decklogClient = nil
	} else {
		defer func() { _ = decklogClient.Close() }()
		logger.WithField("addr", cfg.DecklogGRPCAddr).Info("Connected to Decklog gRPC")
	}

	eventTokenHasher, err := events.NewTokenHasher(cfg.UsageHashSecret)
	if err != nil {
		logger.WithError(err).Fatal("USAGE_HASH_SECRET is required for domain event actor attribution")
	}

	// Create Purser gRPC client for billing status lookups (cross-service via gRPC, not DB)
	var purserClient *purserclient.GRPCClient
	purserClient, err = purserclient.NewGRPCClient(quartermasterPurserClientConfig(cfg, logger))
	if err != nil {
		logger.WithError(err).Warn("Failed to create Purser gRPC client - billing status lookups will use defaults")
		purserClient = nil
	} else {
		defer func() { _ = purserClient.Close() }()
		logger.WithField("addr", cfg.PurserGRPCAddr).Info("Connected to Purser gRPC")
	}

	var commodoreClient *commodoreclient.GRPCClient
	commodoreClient, err = commodoreclient.NewGRPCClient(commodoreclient.GRPCConfig{
		GRPCAddr:      cfg.CommodoreGRPCAddr,
		Timeout:       10 * time.Second,
		Logger:        logger,
		ServiceToken:  cfg.ServiceToken,
		AllowInsecure: cfg.AllowInsecure,
		CACertFile:    cfg.CAPath,
		ServerName:    cfg.CommodoreGRPCTLSServerName,
	})
	if err != nil {
		logger.WithError(err).Warn("Failed to create Commodore gRPC client - media authority refresh delivery will be disabled")
		commodoreClient = nil
	} else {
		defer func() { _ = commodoreClient.Close() }()
		logger.WithField("addr", cfg.CommodoreGRPCAddr).Info("Connected to Commodore gRPC")
	}

	// Initialize handlers used by the health poller.
	handlers.Init(db, logger)

	// Expose health, readiness, and metrics over HTTP; tenant and cluster APIs are served over gRPC.
	router := server.NewServiceRouter(server.RouterSpec{
		Service:            "quartermaster",
		Logger:             logger,
		Health:             healthChecker,
		Ready:              readiness,
		Metrics:            metricsCollector,
		Runtime:            cfg.HTTPRuntime,
		DebugToken:         cfg.ServiceToken,
		DebugConfig:        func() any { return liveConfig.Get() },
		DebugConfigOptions: configOptions,
	})
	router.GET("/internal/ingress-sites", func(c *gin.Context) {
		if !server.RequirePrivateClient(c) {
			return
		}
		authz := strings.TrimSpace(c.GetHeader("Authorization"))
		if authz != "Bearer "+cfg.ServiceToken {
			c.JSON(http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}

		nodeID := strings.TrimSpace(c.Query("node_id"))
		if nodeID == "" {
			c.JSON(http.StatusBadRequest, map[string]string{"error": "node_id is required"})
			return
		}

		rows, err := quartermasterdb.New(db).ListIngressSitesForNode(c.Request.Context(), nodeID)
		if err != nil {
			c.JSON(http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		type ingressSite struct {
			SiteID      string                 `json:"site_id"`
			ClusterID   string                 `json:"cluster_id"`
			NodeID      string                 `json:"node_id"`
			Domains     []string               `json:"domains"`
			TLSBundleID string                 `json:"tls_bundle_id"`
			Kind        string                 `json:"kind"`
			Upstream    string                 `json:"upstream"`
			Metadata    map[string]interface{} `json:"metadata"`
		}

		var sites []ingressSite
		for _, row := range rows {
			var site ingressSite
			site.SiteID, site.ClusterID, site.NodeID = row.SiteID, row.ClusterID, row.NodeID
			site.TLSBundleID, site.Kind, site.Upstream = row.TlsBundleID, row.Kind, row.Upstream
			if unmarshalErr := json.Unmarshal(row.Domains, &site.Domains); unmarshalErr != nil {
				site.Domains = nil
			}
			if unmarshalErr := json.Unmarshal(row.Metadata, &site.Metadata); unmarshalErr != nil {
				site.Metadata = nil
			}
			sites = append(sites, site)
		}

		c.JSON(http.StatusOK, map[string]interface{}{"sites": sites})
	})

	// Wake Navigator the moment a pool-assigned/physical instance (livepeer-gateway,
	// foghorn, chandler, vmauth/telemetry) crosses its health state, so its DNS refreshes immediately
	// rather than waiting for the reconcile interval. Pool DNS is keyed by the SERVED
	// media cluster (service_cluster_assignments), so resolve those and fire one
	// SyncDNS per served cluster; with none (e.g. an unassigned gateway) fall back to
	// a physical-only refresh. Registered before StartHealthPoller so the poll
	// goroutines see it without a data race.
	if navigatorClient != nil {
		handlers.SetPoolDNSWake(func(instanceID, serviceType string) {
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				defer cancel()
				clusters := servedClustersForInstanceName(ctx, db, logger, instanceID, serviceType)
				if len(clusters) == 0 {
					if !dns.IsPhysicalEndpointServiceType(serviceType) {
						return // non-physical pool with no served clusters: nothing to wake
					}
					clusters = []string{""} // physical-only refresh
				}
				// Resolve by INSTANCE type (svc.type) above; wake by the DNS-facing name
				// (vmauth -> telemetry, others identity).
				wakeType := dns.PoolDNSWakeServiceType(serviceType)
				for _, c := range clusters {
					cid := c
					log := logger.WithField("service_type", wakeType).WithField("cluster_id", c).WithField("instance_id", instanceID)
					resp, err := navigatorClient.SyncDNS(ctx, &dnspb.SyncDNSRequest{ServiceType: wakeType, ClusterId: &cid})
					if err != nil {
						log.WithError(err).Warn("Navigator pool DNS wake failed; reconcile loop will converge")
						continue
					}
					// Navigator folds physical-sync failures into Success=false/Errors,
					// so the gRPC error alone is not enough — surface a reported failure.
					if !resp.GetSuccess() {
						log.WithField("errors", resp.GetErrors()).Warn("Navigator pool DNS wake reported failure; reconcile loop will converge")
					}
				}
			}()
		})
	}

	// Start health poller before serving
	handlers.StartHealthPoller(handlers.HealthPollerConfig{
		PollIntervalSeconds: cfg.HealthPollIntervalSeconds,
		TimeoutMS:           cfg.HealthTimeoutMS,
		MaxConcurrency:      cfg.HealthMaxConcurrency,
		BatchSize:           cfg.HealthBatchSize,
		MinAgeSeconds:       cfg.HealthMinAge(),
		GRPCWatch:           cfg.HealthGRPCWatch,
		WatchRefreshSeconds: cfg.HealthWatchRefreshSeconds,
		WatchBackoffSeconds: cfg.HealthWatchBackoffSeconds,
		WatchDialTimeoutMS:  cfg.HealthWatchDialTimeoutMS,
		WatchMaxConcurrency: cfg.HealthWatchMaxConcurrency,
		TLS: func(serviceID string) handlers.HealthWatchTLS {
			current := liveConfig.Get()
			return handlers.HealthWatchTLS{
				CAPath:        current.CAPath,
				ServerName:    current.HealthWatchTLSServerName(serviceID),
				AllowInsecure: current.AllowInsecure,
			}
		},
	})

	// NewGRPCServer waits for the gRPC TLS files, so the server builds in the
	// background while HTTP health already serves.
	peerWatchCtx, stopPeerWatches := context.WithCancel(context.Background())
	defer stopPeerWatches()
	buildGRPCServer := func(ctx context.Context) (*grpc.Server, error) {
		return qmgrpc.NewGRPCServer(ctx, qmgrpc.GRPCServerConfig{
			PeerWatchContext:            peerWatchCtx,
			ConsentReviewKeyID:          cfg.ConsentReviewKeyID,
			ConsentReviewPrivateKey:     consentReviewPrivateKey,
			DB:                          db,
			Logger:                      logger,
			ServiceToken:                cfg.ServiceToken,
			JWTSecret:                   []byte(cfg.JWTSecret),
			MetadataPolicy:              metadataPolicy,
			NavigatorClient:             navigatorClient,
			DecklogClient:               decklogClient,
			PurserClient:                purserClient,
			MediaAuthorityRefreshClient: commodoreClient,
			GeoIPReader:                 geoipReader,
			Metrics:                     serverMetrics,
			CertFile:                    cfg.CertPath,
			KeyFile:                     cfg.KeyPath,
			AllowInsecure:               cfg.AllowInsecure,
			AdvertiseGRPCAddr:           cfg.QuartermasterGRPCAddr,
			PlatformRootDomain:          cfg.PlatformRootDomain,
			// Same var Navigator reads, so the public_instance_host freshness gate and
			// Navigator's physical-DNS publish freshness can't drift.
			PhysicalEndpointStaleSeconds:       cfg.PhysicalEndpointStaleSeconds,
			ClusterAccessMaterializationSecret: cfg.ClusterAccessMaterializationSecret,
			EventTokenHasher:                   eventTokenHasher,
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The domain event relay drains quartermaster.domain_event_outbox to
	// Decklog on every replica; leases and SKIP LOCKED keep replicas from
	// delivering the same row concurrently. It stops after the listeners, so a
	// row committed by the last in-flight RPC is still picked up.
	var onShutdown []func(context.Context)
	if decklogClient != nil {
		relay, relayErr := eventoutbox.NewRelay(db, "quartermaster", decklogClient, logger)
		if relayErr != nil {
			logger.WithError(relayErr).Fatal("Failed to create the domain event relay")
		}
		relayCtx, stopRelay := context.WithCancel(context.Background())
		relayDone := make(chan struct{})
		go func() {
			defer close(relayDone)
			relay.Run(relayCtx)
		}()
		onShutdown = append(onShutdown, func(shutdownCtx context.Context) {
			stopRelay()
			select {
			case <-relayDone:
			case <-shutdownCtx.Done():
			}
		})
	} else {
		logger.Warn("Domain event relay disabled: no Decklog client")
	}

	// Best-effort self-registration in Quartermaster (idempotent, using gRPC)
	go func() {
		qc, qcErr := qmclient.NewGRPCClient(qmclient.GRPCConfig{
			GRPCAddr:      cfg.QuartermasterGRPCAddr,
			Timeout:       10 * time.Second,
			Logger:        logger,
			ServiceToken:  cfg.ServiceToken,
			AllowInsecure: cfg.AllowInsecure,
			CACertFile:    cfg.CAPath,
			ServerName:    cfg.QuartermasterGRPCTLSServerName,
		})
		if qcErr != nil {
			logger.WithError(qcErr).Warn("Failed to create Quartermaster gRPC client for self-registration")
			return
		}
		defer func() { _ = qc.Close() }()
		req, reqErr := qmbootstrap.NewServiceRequest(qmbootstrap.ServiceRegistration{
			ServiceType:   "quartermaster",
			Port:          cfg.HTTPListen.Port,
			AdvertiseHost: cfg.AdvertiseHost,
			ClusterID:     cfg.ClusterID,
			NodeID:        cfg.NodeID,
		})
		if reqErr != nil {
			logger.WithError(reqErr).Warn("Quartermaster bootstrap skipped")
			return
		}
		if _, bootstrapErr := qmbootstrap.BootstrapServiceWithRetry(ctx, qc, req, logger, qmbootstrap.DefaultRetryConfig("quartermaster")); bootstrapErr != nil {
			logger.WithError(bootstrapErr).Warn("Quartermaster bootstrap (quartermaster) failed")
		} else {
			logger.Info("Quartermaster bootstrap (quartermaster) ok")
		}
	}()

	server.RegisterEnvFileReload("quartermaster", logger)
	if runErr := server.Run(ctx, server.RunSpec{
		Service: "quartermaster",
		Logger:  logger,
		Ready:   readiness,
		HTTP: []server.HTTPListener{{
			Name:        "http",
			Port:        cfg.HTTPListen.Port,
			Handler:     router,
			TLSCertFile: cfg.HTTPTLSCertFile,
			TLSKeyFile:  cfg.HTTPTLSKeyFile,
		}},
		GRPC:       []server.GRPCListener{{Name: "grpc", Port: cfg.GRPCListen.Port, Build: buildGRPCServer}},
		OnReload:   []server.ReloadCallback{liveConfig.Reload},
		OnDrain:    []func(context.Context){func(context.Context) { stopPeerWatches() }},
		OnShutdown: onShutdown,
	}); runErr != nil {
		logger.WithError(runErr).Fatal("Server exited with error")
	}
}
