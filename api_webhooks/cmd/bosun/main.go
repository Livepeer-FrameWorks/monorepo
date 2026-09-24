package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"frameworks/api_webhooks/internal/appconfig"
	"frameworks/api_webhooks/internal/consumer"
	"frameworks/api_webhooks/internal/delivery"
	"frameworks/api_webhooks/internal/grpcserver"
	"frameworks/api_webhooks/internal/ledger"
	"frameworks/api_webhooks/internal/maintenance"
	"frameworks/api_webhooks/internal/metrics"
	"frameworks/api_webhooks/internal/notify"

	decklogclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/decklog"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/purser"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/email"
	eventoutbox "github.com/Livepeer-FrameWorks/monorepo/pkg/events/outbox"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/grpcutil"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/monitoring"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/outbox"
	bosunpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/bosun"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/qmbootstrap"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/restream"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/server"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/version"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
)

// notificationOutboxConfig paces the endpoint-disabled emails. The lease
// covers one Purser lookup and one SMTP send.
var notificationOutboxConfig = outbox.Config{
	BaseBackoff:        30 * time.Second,
	MaxBackoff:         time.Hour,
	BatchSize:          10,
	PollPeriod:         15 * time.Second,
	Lease:              2 * time.Minute,
	SettleTimeout:      30 * time.Second,
	AlertAfterAttempts: 12,
}

func main() {
	if version.HandleCLI() {
		return
	}
	logger := logging.NewLoggerWithService("bosun")
	config.LoadEnv(logger)
	logger.Info("Starting Bosun (webhooks)")

	configOptions := config.Options{Service: "bosun", Logger: logger}
	cfg, err := config.Load[appconfig.Bosun](configOptions)
	if err != nil {
		logger.WithError(err).Fatal("Invalid configuration")
	}
	cfg.ApplyLogLevel(logger)
	keyring, err := cfg.FieldKeyring()
	if err != nil {
		logger.WithError(err).Fatal("Invalid BOSUN_FIELD_ENCRYPTION_KEY settings")
	}

	dbConfig := database.DefaultConfig()
	dbConfig.ServiceName = "bosun"
	dbConfig.URL = cfg.DatabaseURL
	db := database.MustConnect(dbConfig, logger)
	defer func() { _ = db.Close() }()

	healthChecker := monitoring.NewHealthChecker("bosun", version.Version)
	metricsCollector := metrics.NewCollector(version.Version, version.GitCommit)
	domainMetrics := metrics.New(metricsCollector)
	healthChecker.AddCheck("database", monitoring.DatabaseHealthCheck(db))

	// Endpoints, the ledger, and every worker live in Postgres, so readiness
	// tracks the database.
	readiness := monitoring.NewReadinessChecker("bosun", version.Version)
	readiness.AddCheck("database", monitoring.DatabaseHealthCheck(db))

	store := &ledger.Store{DB: db, Cipher: keyring}

	qmClient, err := quartermaster.NewGRPCClient(quartermaster.GRPCConfig{
		GRPCAddr:      cfg.QuartermasterGRPCAddr,
		Timeout:       10 * time.Second,
		Logger:        logger,
		ServiceToken:  cfg.ServiceToken,
		AllowInsecure: cfg.AllowInsecure,
		CACertFile:    cfg.CAPath,
		ServerName:    cfg.QuartermasterGRPCTLSServerName,
	})
	if err != nil {
		logger.WithError(err).Fatal("Failed to create Quartermaster gRPC client")
	}
	defer func() { _ = qmClient.Close() }()

	purserClient, err := purser.NewGRPCClient(purser.GRPCConfig{
		GRPCAddr:           cfg.PurserGRPCAddr,
		Timeout:            10 * time.Second,
		Logger:             logger,
		ServiceToken:       cfg.ServiceToken,
		PreferServiceToken: true,
		AllowInsecure:      cfg.AllowInsecure,
		CACertFile:         cfg.CAPath,
		ServerName:         cfg.PurserGRPCTLSServerName,
	})
	if err != nil {
		logger.WithError(err).Fatal("Failed to create Purser gRPC client")
	}
	defer func() { _ = purserClient.Close() }()

	// Decklog receives the audit events Bosun commits to its domain event
	// outbox.
	decklogClient, err := decklogclient.NewBatchedClient(decklogclient.BatchedClientConfig{
		Target:        cfg.DecklogGRPCAddr,
		AllowInsecure: cfg.AllowInsecure,
		CACertFile:    cfg.CAPath,
		ServerName:    cfg.DecklogGRPCTLSServerName,
		Timeout:       5 * time.Second,
		Source:        "bosun",
		ServiceToken:  cfg.ServiceToken,
		ClusterID:     cfg.ClusterID,
		SourceRegion:  cfg.Region,
	}, logger)
	if err != nil {
		logger.WithError(err).Fatal("Failed to create Decklog client")
	}
	defer func() { _ = decklogClient.Close() }()
	relay, err := eventoutbox.NewRelay(db, ledger.DomainEventSchema, decklogClient, logger)
	if err != nil {
		logger.WithError(err).Fatal("Failed to create the domain event relay")
	}

	dlqProducer, err := kafka.NewKafkaProducer(cfg.KafkaBrokers, cfg.DLQTopic, cfg.KafkaClusterID, logger)
	if err != nil {
		logger.WithError(err).Fatal("Failed to create the dead-letter Kafka producer")
	}
	defer func() { _ = dlqProducer.Close() }()

	eventConsumer, err := kafka.NewConsumer(cfg.KafkaBrokers, consumer.GroupID, cfg.KafkaClusterID, "bosun", logger,
		kafka.WithLagTracker(kafka.LagTrackerConfig{
			Gauge: domainMetrics.KafkaLag,
		}),
	)
	if err != nil {
		logger.WithError(err).Fatal("Failed to create Kafka consumer")
	}
	defer func() { _ = eventConsumer.Close() }()
	eventHandler := &consumer.Handler{Recorder: store, Metrics: domainMetrics, Logger: logger}
	topics := consumer.Register(eventConsumer, cfg.MirrorRegionPrefixes, eventHandler.Handle, consumer.DeadLetter(dlqProducer, cfg.DLQTopic, logger))
	logger.WithField("topics", topics).Info("Consuming public domain events")
	healthChecker.AddCheck("kafka", monitoring.KafkaConsumerHealthCheck(eventConsumer.GetClient()))

	// Webhook destinations get the shared destination policy: the URL, its DNS
	// answer at validation, and the address of every connection must be public,
	// unless the operator lets an isolated cluster deliver to private addresses.
	destinationPolicy := restream.WebhookDestinationPolicy(cfg.AllowPrivateDestinations)
	if destinationPolicy.AllowPrivate {
		logger.Warn("BOSUN_ALLOW_PRIVATE_DESTINATIONS is set: webhook endpoints may target private addresses and plain http")
	}
	httpClient := delivery.NewHTTPClient(delivery.ClientOptions{Policy: destinationPolicy})
	worker := &delivery.Worker{
		Store:   store,
		Sender:  &delivery.Sender{HTTP: httpClient},
		Metrics: domainMetrics,
		Logger:  logger,
	}

	var mailer notify.Mailer
	if cfg.SMTPHost != "" {
		mailer = email.NewSender(email.Config{
			Host:          cfg.SMTPHost,
			Port:          cfg.SMTPPort,
			User:          cfg.SMTPUser,
			Password:      cfg.SMTPPassword,
			From:          cfg.FromEmail,
			FromName:      cfg.FromName,
			AllowInsecure: cfg.SMTPAllowInsecure,
		})
	}
	notificationWorker := &outbox.Worker[ledger.Notification]{
		Config: notificationOutboxConfig,
		Store:  &ledger.NotificationStore{Store: store},
		Dispatcher: &notify.Dispatcher{
			Contacts: purserClient,
			Mailer:   mailer,
			Branding: cfg.EmailBranding,
			Logger:   logger,
		},
		Logger:     logger,
		AlertLabel: "bosun endpoint notification outbox",
	}

	// Background work stops when shutdown starts draining: the consumer stops
	// polling, the worker finishes the sends in flight, and the relay stops
	// last so audit events committed during shutdown stay in the outbox.
	workCtx, stopWork := context.WithCancel(context.Background())
	defer stopWork()
	var background sync.WaitGroup
	background.Go(func() {
		// Start returns when group membership is lost; restarting the process
		// rejoins the group from committed offsets.
		if startErr := eventConsumer.Start(workCtx); startErr != nil && workCtx.Err() == nil {
			logger.WithError(startErr).Fatal("Domain event consumer exited")
		}
	})
	background.Go(func() { worker.Run(workCtx) })
	background.Go(func() { notificationWorker.Run(workCtx) })
	background.Go(func() { (&maintenance.Runner{Store: store, Metrics: domainMetrics, Logger: logger}).Run(workCtx) })
	relayCtx, stopRelay := context.WithCancel(context.Background())
	defer stopRelay()
	relayDone := make(chan struct{})
	go func() {
		defer close(relayDone)
		relay.Run(relayCtx)
	}()

	go registerWithQuartermaster(cfg, qmClient, logger)

	app := server.NewServiceRouter(server.RouterSpec{
		Service:            "bosun",
		Logger:             logger,
		Health:             healthChecker,
		Ready:              readiness,
		Metrics:            metricsCollector,
		Runtime:            cfg.HTTPRuntime,
		DebugToken:         cfg.ServiceToken,
		DebugConfig:        func() any { return cfg },
		DebugConfigOptions: configOptions,
	})

	api := &grpcserver.Server{
		Store:   store,
		Tester:  worker,
		Policy:  destinationPolicy,
		Metrics: domainMetrics,
		Logger:  logger,
	}
	server.RegisterEnvFileReload("bosun", logger)
	if runErr := server.Run(context.Background(), server.RunSpec{
		Service: "bosun",
		Logger:  logger,
		Ready:   readiness,
		HTTP:    []server.HTTPListener{{Name: "http", Port: cfg.HTTPListenPort(), Handler: app}},
		// The gRPC server is built after the port is bound, so HTTP health
		// serves while the TLS files are still being synced.
		GRPC: []server.GRPCListener{{Name: "grpc", Port: cfg.GRPCPort, Build: func(buildCtx context.Context) (*grpc.Server, error) {
			return newGRPCServer(buildCtx, cfg, logger, api, domainMetrics.GRPCRequests, domainMetrics.GRPCDuration)
		}}},
		OnDrain: []func(context.Context){func(ctx context.Context) {
			stopWork()
			done := make(chan struct{})
			go func() { background.Wait(); close(done) }()
			select {
			case <-done:
			case <-ctx.Done():
				logger.Warn("Background webhook work did not stop within the shutdown timeout")
			}
		}},
		OnShutdown: []func(context.Context){func(ctx context.Context) {
			stopRelay()
			select {
			case <-relayDone:
			case <-ctx.Done():
			}
		}},
	}); runErr != nil {
		logger.WithError(runErr).Fatal("Server exited with error")
	}
}

// newGRPCServer builds the Bosun gRPC server. It waits up to two minutes for
// the TLS files, and stops waiting when ctx ends.
func newGRPCServer(ctx context.Context, cfg *appconfig.Bosun, logger logging.Logger, srv bosunpb.BosunServiceServer, requests *prometheus.CounterVec, duration *prometheus.HistogramVec) (*grpc.Server, error) {
	metadataPolicy, policyErr := middleware.ParseMetadataPolicy(cfg.MetadataPolicy)
	if policyErr != nil {
		return nil, policyErr
	}
	authInterceptor := middleware.GRPCAuthInterceptor(middleware.GRPCAuthConfig{
		ServiceToken:   cfg.ServiceToken,
		JWTSecret:      []byte(cfg.JWTSecret),
		MetadataPolicy: metadataPolicy,
		Logger:         logger,
		SkipMethods: []string{
			"/grpc.health.v1.Health/Check",
			"/grpc.health.v1.Health/Watch",
		},
	})
	tlsCfg := grpcutil.ServerTLSConfig{
		CertFile:      cfg.CertPath,
		KeyFile:       cfg.KeyPath,
		AllowInsecure: cfg.AllowInsecure,
	}
	waitCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := grpcutil.WaitForServerTLSFiles(waitCtx, tlsCfg, logger); err != nil {
		return nil, fmt.Errorf("wait for Bosun gRPC TLS files: %w", err)
	}
	tlsOpt, err := grpcutil.ServerTLS(tlsCfg, logger)
	if err != nil {
		return nil, fmt.Errorf("configure Bosun gRPC TLS: %w", err)
	}
	serverOpts := []grpc.ServerOption{
		// The metrics interceptor sits outermost so authentication rejections
		// are counted in bosun_grpc_requests_total.
		grpc.ChainUnaryInterceptor(
			middleware.GRPCMetricsInterceptor(requests, duration),
			grpcutil.SanitizeUnaryServerInterceptor(),
			authInterceptor,
		),
	}
	if tlsOpt != nil {
		serverOpts = append(serverOpts, tlsOpt)
	}
	grpcServer := grpc.NewServer(serverOpts...)
	bosunpb.RegisterBosunServiceServer(grpcServer, srv)
	hs := health.NewServer()
	hs.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
	hs.SetServingStatus(bosunpb.BosunService_ServiceDesc.ServiceName, grpc_health_v1.HealthCheckResponse_SERVING)
	server.RegisterHealthServer(grpcServer, hs)
	reflection.Register(grpcServer)
	return grpcServer, nil
}

// registerWithQuartermaster registers the gRPC port without a health
// endpoint.
func registerWithQuartermaster(cfg *appconfig.Bosun, qmClient qmbootstrap.BootstrapClient, logger logging.Logger) {
	req, err := qmbootstrap.NewServiceRequest(qmbootstrap.ServiceRegistration{
		ServiceType:        "bosun",
		Protocol:           "grpc",
		Port:               cfg.GRPCPort,
		AdvertiseHost:      cfg.AdvertiseHost,
		ClusterID:          cfg.ClusterID,
		NodeID:             cfg.NodeID,
		OmitHealthEndpoint: true,
	})
	if err != nil {
		logger.WithError(err).Warn("Quartermaster bootstrap skipped")
		return
	}
	if _, err := qmbootstrap.BootstrapServiceWithRetry(context.Background(), qmClient, req, logger, qmbootstrap.DefaultRetryConfig("bosun")); err != nil {
		logger.WithError(err).Warn("Quartermaster bootstrap (bosun) failed")
		return
	}
	logger.Info("Quartermaster bootstrap (bosun) ok")
}
