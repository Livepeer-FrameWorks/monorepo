package main

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"errors"
	"os"
	"time"

	"frameworks/api_control/internal/appconfig"
	"frameworks/api_control/internal/clusterurls"
	commodoremigrations "frameworks/api_control/internal/datamigrations"
	commodoregrpc "frameworks/api_control/internal/grpc"
	"frameworks/api_control/internal/placementpolicy"
	decklogclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/decklog"
	foghornclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/foghorn"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/listmonk"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/navigator"
	purserclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/purser"
	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	fieldcrypt "github.com/Livepeer-FrameWorks/monorepo/pkg/crypto"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/datamigrate"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	eventoutbox "github.com/Livepeer-FrameWorks/monorepo/pkg/events/outbox"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/monitoring"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/qmbootstrap"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/restream"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/server"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/version"
	"google.golang.org/grpc"
)

func main() {
	if version.HandleCLI() {
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "data-migrations" {
		logger := logging.NewLoggerWithService("commodore")
		config.LoadEnv(logger)
		migrationCfg, err := config.Load[appconfig.CommodoreDataMigrations](config.Options{Service: "commodore", Logger: logger})
		if err != nil {
			logger.WithError(err).Fatal("Invalid data migration configuration")
		}
		commodoremigrations.Register(commodoremigrations.FieldEncryptionSettings{
			ActiveKeyID:            migrationCfg.FieldEncryptionKeyID,
			ActiveKey:              migrationCfg.FieldEncryptionKey,
			PreviousKeys:           migrationCfg.FieldEncryptionPreviousKeys,
			LegacySecrets:          migrationCfg.FieldEncryptionLegacySecrets,
			JWTSecret:              migrationCfg.JWTSecret,
			AllowQuarantine:        migrationCfg.FieldEncryptionAllowQuarantine,
			RequeueQuarantine:      migrationCfg.FieldEncryptionRequeueQuarantine,
			AckUnverifiedLegacyKey: migrationCfg.FieldEncryptionAckUnverifiedLegacyKey,
		})
		placementpolicy.RegisterPullSourcePinsToStreamRules()
		dbConfig := database.DefaultConfig()
		dbConfig.ServiceName = "commodore"
		dbConfig.URL = migrationCfg.DatabaseURL
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
	logger := logging.NewLoggerWithService("commodore")

	// Load environment variables
	config.LoadEnv(logger)

	logger.Info("Starting Commodore (Control API)")

	configOptions := config.Options{Service: "commodore", Logger: logger}
	cfg, err := config.Load[appconfig.Commodore](configOptions)
	if err != nil {
		logger.WithError(err).Fatal("Invalid configuration")
	}
	cfg.ApplyLogLevel(logger)
	metadataPolicy, err := middleware.ParseMetadataPolicy(cfg.MetadataPolicy)
	if err != nil {
		logger.WithError(err).Fatal("Invalid configuration")
	}
	liveConfig := config.NewLive(cfg, configOptions)

	fieldEncryptionPrevious, err := fieldcrypt.ParseFieldKeySet(cfg.FieldEncryptionPreviousKeys)
	if err != nil {
		logger.WithError(err).Fatal("Invalid FIELD_ENCRYPTION_PREVIOUS_KEYS")
	}
	fieldEncryptionLegacy, err := fieldcrypt.ParseLegacyFieldSecrets(cfg.FieldEncryptionLegacySecrets)
	if err != nil {
		logger.WithError(err).Fatal("Invalid FIELD_ENCRYPTION_LEGACY_SECRETS")
	}
	destinationPolicy, err := restream.DestinationPolicyFromValues(cfg.RestreamAllowPrivateDestinations, cfg.RestreamAllowedPrivateCIDRs, cfg.RestreamDeniedCIDRs)
	if err != nil {
		logger.WithError(err).Fatal("Invalid restream destination policy")
	}
	// Config validation already enforced that the signer key ID and private
	// key are set together, and that both are set outside development.
	var mediaAuthorityPrivateKey ed25519.PrivateKey
	if cfg.MediaAuthoritySigningPrivateKeyPEMB64 != "" {
		mediaAuthorityPrivateKey, err = sharedauthority.ParseSigningPrivateKey(cfg.MediaAuthoritySigningPrivateKeyPEMB64)
		if err != nil {
			logger.WithError(err).Fatal("Invalid media authority signing private key")
		}
		logger.WithField("signer_key_id", cfg.MediaAuthoritySigningKeyID).Info("Signed media authority compiler enabled")
	} else {
		logger.Warn("Media authority signing key is not configured in development; authority compilation is disabled")
	}
	var mediaAuthoritySealRecipients sharedauthority.SealRecipientSet
	if cfg.MediaAuthoritySealRecipients != "" {
		mediaAuthoritySealRecipients, err = sharedauthority.ParseSealRecipients(cfg.MediaAuthoritySealRecipients)
		if err != nil {
			logger.WithError(err).Fatal("Invalid media authority seal recipient set")
		}
		logger.WithField("recipient_cells", len(mediaAuthoritySealRecipients)).Info("Media authority sealed-secret delivery enabled")
	}

	// Connect to database
	dbConfig := database.DefaultConfig()
	dbConfig.ServiceName = "commodore"
	dbConfig.URL = cfg.DatabaseURL
	db := database.MustConnect(dbConfig, logger)
	defer func() { _ = db.Close() }()

	// Setup monitoring
	healthChecker := monitoring.NewHealthChecker("commodore", version.Version)
	metricsCollector := monitoring.NewMetricsCollector("commodore", version.Version, version.GitCommit)

	// Add health checks
	healthChecker.AddCheck("database", monitoring.DatabaseHealthCheck(db))

	readiness := monitoring.NewReadinessChecker("commodore", version.Version)
	readiness.AddCheck("database", monitoring.DatabaseHealthCheck(db))

	// Per-method counters live on commodore_grpc_requests_total{method,status}
	// produced by middleware.GRPCMetricsInterceptor (wired into the gRPC
	// server below); separate auth_/stream_operations counters would just
	// rename the same axis.
	serverMetrics := &commodoregrpc.ServerMetrics{
		GRPCRequests: metricsCollector.NewCounter("grpc_requests_total", "Total gRPC requests", []string{"method", "status"}),
		GRPCDuration: metricsCollector.NewHistogram("grpc_request_duration_seconds", "gRPC request duration", []string{"method"}, nil),
		MediaAuthorityDeliveryAttempts: metricsCollector.NewCounter(
			"media_authority_delivery_attempts_total",
			"Signed media-authority delivery attempts by result",
			[]string{"authority_kind", "result"},
		),
		MediaAuthorityPending: metricsCollector.NewGauge(
			"media_authority_pending_deliveries",
			"Current authority deliveries not yet acknowledged",
			[]string{"authority_kind"},
		),
		MediaAuthorityMaxVersionLag: metricsCollector.NewGauge(
			"media_authority_max_version_lag",
			"Largest current-version minus acknowledged-version lag across target cells",
			[]string{"authority_kind"},
		),
		MediaAuthorityOldestPendingSeconds: metricsCollector.NewGauge(
			"media_authority_oldest_pending_seconds",
			"Age in seconds of the oldest unacknowledged current authority delivery",
			[]string{"authority_kind"},
		),
		MediaAuthorityRejectedDeliveries: metricsCollector.NewGauge(
			"media_authority_rejected_deliveries",
			"Current authority deliveries a target cell refused on a precondition",
			[]string{"authority_kind"},
		),
		MediaAuthorityRefreshPending: metricsCollector.NewGauge(
			"media_authority_refresh_pending",
			"Refresh obligations that are due and not yet settled",
			[]string{"lane"},
		),
		MediaAuthorityRefreshOldestPendingSeconds: metricsCollector.NewGauge(
			"media_authority_refresh_oldest_pending_seconds",
			"Seconds the oldest due refresh obligation has been waiting",
			[]string{"lane"},
		),
		MediaAuthorityRefreshParked: metricsCollector.NewGauge(
			"media_authority_refresh_parked",
			"Refresh targets that cannot compile until their source state changes",
			[]string{"target_kind"},
		),
		MediaAuthorityExpiredWarm: metricsCollector.NewGauge(
			"media_authority_expired_warm",
			"Authorities still being renewed whose current version has run out",
			[]string{"authority_kind"},
		),
		MediaAuthorityRevocationCheckFailures: metricsCollector.NewCounter(
			"media_authority_revocation_check_failures_total",
			"Compile-failure access checks that could not complete",
			[]string{"stage"},
		),
		MediaAuthorityObservationTimestamp: metricsCollector.NewGauge(
			"media_authority_observation_timestamp_seconds",
			"Unix timestamp of the last complete authority obligation observation",
			[]string{},
		),
		MediaAuthorityRefreshSettlements: metricsCollector.NewCounter(
			"media_authority_refresh_settlements_total",
			"Claimed refresh obligations by how they settled",
			[]string{"lane", "outcome"},
		),
		MediaAuthorityVersionsPublished: metricsCollector.NewCounter(
			"media_authority_versions_published_total",
			"Published authority versions by the reason a new version was needed",
			[]string{"authority_kind", "cause"},
		),
		MediaAuthorityEarlyRenewals: metricsCollector.NewCounter(
			"media_authority_early_renewals_total",
			"Renewals published while the replaced version had used less than a quarter of its validity",
			[]string{"authority_kind"},
		),
		FieldDecryptFailures: metricsCollector.NewCounter(
			"field_decrypt_failures_total",
			"Application-field decryption failures by bounded purpose and ciphertext format",
			[]string{"purpose", "format"},
		),
	}
	for _, lane := range []string{"event", "bulk", "object_deadline", "tenant_deadline"} {
		for _, outcome := range []string{"completed", "noop", "superseded", "transient", "parked", "dormant"} {
			serverMetrics.MediaAuthorityRefreshSettlements.WithLabelValues(lane, outcome).Add(0)
		}
	}
	for _, kind := range []string{"tenant", "media_object"} {
		for _, cause := range []string{"content", "targets", "validity", "renewal"} {
			serverMetrics.MediaAuthorityVersionsPublished.WithLabelValues(kind, cause).Add(0)
		}
		serverMetrics.MediaAuthorityEarlyRenewals.WithLabelValues(kind).Add(0)
	}
	for _, stage := range []string{"previous_read", "previous_decode", "playback_source_read", "placement_read", "deny_publish"} {
		serverMetrics.MediaAuthorityRevocationCheckFailures.WithLabelValues(stage).Add(0)
	}

	foghornPool := foghornclient.NewPool(foghornclient.PoolConfig{
		ServiceToken:  cfg.ServiceToken,
		Timeout:       30 * time.Second,
		Logger:        logger,
		MaxIdleTime:   10 * time.Minute,
		CACertFile:    cfg.CAPath,
		ServerName:    cfg.FoghornGRPCTLSServerName,
		AllowInsecure: cfg.AllowInsecure,
	})
	defer foghornPool.Close()

	// Create Quartermaster gRPC client for tenant creation during registration
	quartermasterGRPCClient, err := qmclient.NewGRPCClient(qmclient.GRPCConfig{
		GRPCAddr:           cfg.QuartermasterGRPCAddr,
		Timeout:            30 * time.Second,
		Logger:             logger,
		ServiceToken:       cfg.ServiceToken,
		PreferServiceToken: true,
		AllowInsecure:      cfg.AllowInsecure,
		CACertFile:         cfg.CAPath,
		ServerName:         cfg.QuartermasterGRPCTLSServerName,
	})
	if err != nil {
		logger.WithError(err).Warn("Failed to create Quartermaster gRPC client - tenant creation will use fallback")
		quartermasterGRPCClient = nil
	} else {
		defer func() { _ = quartermasterGRPCClient.Close() }()
		logger.WithField("addr", cfg.QuartermasterGRPCAddr).Info("Connected to Quartermaster gRPC")
	}

	// Optional Navigator gRPC client. Used for GetTenantAliasStatus when
	// populating CreateStreamResponse.tenant_*_domain fields. Without
	// Navigator, those fields stay unset and clients fall back to
	// global / cluster-concrete URLs.
	var navigatorGRPCClient *navigator.Client
	if cfg.NavigatorGRPCAddr != "" {
		navigatorGRPCClient, err = navigator.NewClient(navigator.Config{
			Addr:          cfg.NavigatorGRPCAddr,
			Timeout:       5 * time.Second,
			Logger:        logger,
			ServiceToken:  cfg.ServiceToken,
			AllowInsecure: cfg.AllowInsecure,
			CACertFile:    cfg.CAPath,
			ServerName:    cfg.NavigatorGRPCTLSServerName,
		})
		if err != nil {
			logger.WithError(err).Warn("Failed to create Navigator gRPC client - tenant alias status lookups disabled")
			navigatorGRPCClient = nil
		} else {
			defer func() { _ = navigatorGRPCClient.Close() }()
			logger.WithField("addr", cfg.NavigatorGRPCAddr).Info("Connected to Navigator gRPC")
		}
	}

	// Create Purser gRPC client for user limit checking during registration
	purserGRPCClient, err := purserclient.NewGRPCClient(purserclient.GRPCConfig{
		GRPCAddr:           cfg.PurserGRPCAddr,
		Timeout:            30 * time.Second,
		Logger:             logger,
		ServiceToken:       cfg.ServiceToken,
		PreferServiceToken: true,
		AllowInsecure:      cfg.AllowInsecure,
		CACertFile:         cfg.CAPath,
		ServerName:         cfg.PurserGRPCTLSServerName,
	})
	if err != nil {
		logger.WithError(err).Warn("Failed to create Purser gRPC client - user limit checks will be skipped")
		purserGRPCClient = nil
	} else {
		defer func() { _ = purserGRPCClient.Close() }()
		logger.WithField("addr", cfg.PurserGRPCAddr).Info("Connected to Purser gRPC")
	}

	// Create Decklog gRPC client for service events
	decklogClient, err := decklogclient.NewBatchedClient(decklogclient.BatchedClientConfig{
		Target:        cfg.DecklogGRPCAddr,
		AllowInsecure: cfg.AllowInsecure,
		CACertFile:    cfg.CAPath,
		ServerName:    cfg.DecklogGRPCTLSServerName,
		Timeout:       5 * time.Second,
		Source:        "commodore",
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
	tokenHasher, err := events.NewTokenHasher(cfg.UsageHashSecret)
	if err != nil {
		logger.WithError(err).Fatal("USAGE_HASH_SECRET is required for domain event actor attribution")
	}

	// Create Listmonk client for newsletter subscription
	var listmonkClient *listmonk.Client
	if cfg.ListmonkURL != "" {
		if cfg.ListmonkAPIUsername == "" || cfg.ListmonkAPIToken == "" {
			logger.Warn("LISTMONK_URL is set but LISTMONK_API_USERNAME or LISTMONK_API_TOKEN is missing; newsletter integration disabled")
		} else {
			listmonkClient = listmonk.NewClient(cfg.ListmonkURL, cfg.ListmonkAPIUsername, cfg.ListmonkAPIToken)
			logger.WithField("url", cfg.ListmonkURL).Info("Listmonk client configured")
		}
	}

	// Expose health, readiness, and metrics over HTTP; product APIs are served over gRPC.
	app := server.NewServiceRouter(server.RouterSpec{
		Service:            "commodore",
		Logger:             logger,
		Health:             healthChecker,
		Ready:              readiness,
		Metrics:            metricsCollector,
		Runtime:            cfg.HTTPRuntime,
		DebugToken:         cfg.ServiceToken,
		DebugConfig:        func() any { return liveConfig.Get() },
		DebugConfigOptions: configOptions,
	})

	// Cluster routing snapshot: read paths derive Chandler URLs from cluster_id
	// without a per-row network call. Refreshes from Quartermaster every 60s.
	clusterURLsResolver := clusterurls.NewResolver(quartermasterGRPCClient, logger, cfg.ChandlerBaseURL)
	clusterURLsResolver.Start(context.Background(), 60*time.Second)

	// NewGRPCServer waits for the gRPC TLS files, so the server builds in the
	// background while HTTP health already serves.
	buildGRPCServer := func(ctx context.Context) (*grpc.Server, error) {
		return commodoregrpc.NewGRPCServer(ctx, commodoregrpc.CommodoreServerConfig{
			DB:                              db,
			DBMaxIdleConns:                  dbConfig.MaxIdleConns,
			Logger:                          logger,
			FoghornPool:                     foghornPool,
			QuartermasterClient:             quartermasterGRPCClient,
			NavigatorClient:                 navigatorGRPCClient,
			PurserClient:                    purserGRPCClient,
			ListmonkClient:                  listmonkClient,
			DecklogClient:                   decklogClient,
			TokenHasher:                     tokenHasher,
			ClusterURLs:                     clusterURLsResolver,
			DefaultMailingListID:            cfg.DefaultMailingListID,
			Metrics:                         serverMetrics,
			ServiceToken:                    cfg.ServiceToken,
			JWTSecret:                       []byte(cfg.JWTSecret),
			MetadataPolicy:                  metadataPolicy,
			FieldEncryptionKeyID:            cfg.FieldEncryptionKeyID,
			FieldEncryptionKey:              []byte(cfg.FieldEncryptionKey),
			FieldEncryptionPrevious:         fieldEncryptionPrevious,
			FieldEncryptionLegacy:           fieldEncryptionLegacy,
			DestinationPolicy:               destinationPolicy,
			TurnstileSecretKey:              cfg.TurnstileAuthSecretKey,
			TurnstileFailOpen:               cfg.TurnstileFailOpen,
			PasswordResetSecret:             []byte(cfg.PasswordResetSecret),
			SystemTenantID:                  cfg.SystemTenantUUID(),
			MediaAuthoritySigningKeyID:      cfg.MediaAuthoritySigningKeyID,
			MediaAuthoritySigningPrivateKey: mediaAuthorityPrivateKey,
			MediaAuthoritySealRecipients:    mediaAuthoritySealRecipients,
			CertFile:                        cfg.CertPath,
			KeyFile:                         cfg.KeyPath,
			AllowInsecure:                   cfg.AllowInsecure,
			Settings:                        func() commodoregrpc.RuntimeSettings { return runtimeSettings(liveConfig.Get()) },
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The domain event relay publishes committed commodore.domain_event_outbox
	// rows to Decklog. It stops after the listeners, so events committed by the
	// last in-flight requests stay in the outbox for the next process.
	var onShutdown []func(context.Context)
	if decklogClient != nil {
		relay, relayErr := eventoutbox.NewRelay(db, commodoregrpc.DomainEventSchema, decklogClient, logger)
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
		logger.Warn("Domain event relay disabled: no Decklog client; domain events stay in the outbox")
	}

	// Best-effort service registration in Quartermaster (using gRPC client)
	go func() {
		if quartermasterGRPCClient == nil {
			logger.Warn("Quartermaster gRPC client not available, skipping bootstrap")
			return
		}
		req, reqErr := qmbootstrap.NewServiceRequest(qmbootstrap.ServiceRegistration{
			ServiceType:   "commodore",
			Port:          cfg.HTTPListen.Port,
			AdvertiseHost: cfg.AdvertiseHost,
			ClusterID:     cfg.ClusterID,
			NodeID:        cfg.NodeID,
		})
		if reqErr != nil {
			logger.WithError(reqErr).Warn("Quartermaster bootstrap skipped")
			return
		}
		if _, bootstrapErr := qmbootstrap.BootstrapServiceWithRetry(ctx, quartermasterGRPCClient, req, logger, qmbootstrap.DefaultRetryConfig("commodore")); bootstrapErr != nil {
			logger.WithError(bootstrapErr).Warn("Quartermaster bootstrap (commodore) failed")
		} else {
			logger.Info("Quartermaster bootstrap (commodore) ok")
		}
	}()

	server.RegisterEnvFileReload("commodore", logger)
	if runErr := server.Run(ctx, server.RunSpec{
		Service: "commodore",
		Logger:  logger,
		Ready:   readiness,
		HTTP:    []server.HTTPListener{{Name: "http", Port: cfg.HTTPListen.Port, Handler: app}},
		GRPC:    []server.GRPCListener{{Name: "grpc", Port: cfg.GRPCListen.Port, Build: buildGRPCServer}},
		// Only settings read through liveConfig follow a reload; everything
		// copied from cfg above stays at its startup value.
		OnReload:   []server.ReloadCallback{liveConfig.Reload},
		OnShutdown: onShutdown,
	}); runErr != nil {
		logger.WithError(runErr).Fatal("Server exited with error")
	}
}

// runtimeSettings projects the request-time settings from one configuration
// snapshot.
func runtimeSettings(c *appconfig.Commodore) commodoregrpc.RuntimeSettings {
	return commodoregrpc.RuntimeSettings{
		JWTSecret:             []byte(c.JWTSecret),
		Branding:              c.EmailBranding,
		DeviceVerificationURL: c.DeviceVerificationURL,
		PlatformRootDomain:    c.PlatformRootDomain,
		BrandDomain:           c.BrandDomain,
		Development:           c.IsDevelopment(),
		SMTPHost:              c.SMTPHost,
		SMTPPort:              c.SMTPPort,
		SMTPUser:              c.SMTPUser,
		SMTPPassword:          c.SMTPPassword,
		FromEmail:             c.FromEmail,
		FromName:              c.FromName,
		SMTPAllowInsecure:     c.SMTPAllowInsecure,
	}
}
