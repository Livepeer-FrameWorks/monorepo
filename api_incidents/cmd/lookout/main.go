package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"frameworks/api_incidents/internal/appconfig"
	"frameworks/api_incidents/internal/grpcserver"
	"frameworks/api_incidents/internal/httpapi"
	"frameworks/api_incidents/internal/incidents"
	"frameworks/api_incidents/internal/notify"
	"frameworks/api_incidents/internal/ownership"

	decklogclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/decklog"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/email"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/grpcutil"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/monitoring"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/outbox"
	lookoutpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/lookout"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/qmbootstrap"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/server"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/topology"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/version"

	"github.com/prometheus/client_golang/prometheus"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
)

// Delivery outbox tuning. The lease covers a full batch of slow webhook or
// SMTP sends so a live worker keeps its claims until settlement.
var deliveryOutboxConfig = outbox.Config{
	BaseBackoff:        5 * time.Second,
	MaxBackoff:         10 * time.Minute,
	BatchSize:          10,
	PollPeriod:         5 * time.Second,
	Lease:              5 * time.Minute,
	SettleTimeout:      30 * time.Second,
	AlertAfterAttempts: 12,
}

func main() {
	if version.HandleCLI() {
		return
	}
	logger := logging.NewLoggerWithService("lookout")
	config.LoadEnv(logger)
	logger.Info("Starting Lookout (incidents)")

	configOptions := config.Options{Service: "lookout", Logger: logger}
	cfg, err := config.Load[appconfig.Lookout](configOptions)
	if err != nil {
		logger.WithError(err).Fatal("Invalid configuration")
	}
	cfg.ApplyLogLevel(logger)
	// The Alertmanager token and the notification settings are read through
	// liveConfig on every use, so they follow a SIGHUP env-file reload.
	liveConfig := config.NewLive(cfg, configOptions)
	settings := notify.SettingsSource(func() notify.Settings { return notifySettings(liveConfig.Get()) })

	dbConfig := database.DefaultConfig()
	dbConfig.ServiceName = "lookout"
	dbConfig.URL = cfg.DatabaseURL
	db := database.MustConnect(dbConfig, logger)
	defer func() { _ = db.Close() }()

	healthChecker := monitoring.NewHealthChecker("lookout", version.Version)
	metricsCollector := monitoring.NewMetricsCollector("lookout", version.Version, version.GitCommit)
	grpcRequests := metricsCollector.NewCounter("grpc_requests_total", "Total gRPC requests", []string{"method", "status"})
	grpcDuration := metricsCollector.NewHistogram("grpc_request_duration_seconds", "gRPC request duration", []string{"method"}, nil)
	domainMetrics := incidents.NewMetrics(metricsCollector)
	healthChecker.AddCheck("database", monitoring.DatabaseHealthCheck(db))

	// Incidents, the delivery outboxes, and cluster ownership all live in
	// Postgres, so readiness tracks the database.
	readiness := monitoring.NewReadinessChecker("lookout", version.Version)
	readiness.AddCheck("database", monitoring.DatabaseHealthCheck(db))

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

	decklogClient, err := decklogclient.NewBatchedClient(decklogclient.BatchedClientConfig{
		Target:        cfg.DecklogGRPCAddr,
		AllowInsecure: cfg.AllowInsecure,
		CACertFile:    cfg.CAPath,
		ServerName:    cfg.DecklogGRPCTLSServerName,
		Timeout:       5 * time.Second,
		Source:        "lookout",
		ServiceToken:  cfg.ServiceToken,
		ClusterID:     cfg.ClusterID,
		SourceRegion:  cfg.Region,
	}, logger)
	if err != nil {
		logger.WithError(err).Fatal("Failed to create Decklog client")
	}
	defer func() { _ = decklogClient.Close() }()

	producer, err := kafka.NewKafkaProducer(cfg.KafkaBrokers, topology.TopicLookoutIncidents, cfg.KafkaClusterID, logger)
	if err != nil {
		logger.WithError(err).Fatal("Failed to create Kafka producer")
	}
	defer func() { _ = producer.Close() }()

	router := notify.Router{Settings: settings}
	incidentService := &incidents.Service{
		DB: db,
		Owners: &incidents.QuartermasterOwners{
			Clusters: qmClient,
			Logger:   logger,
			Metrics:  domainMetrics,
		},
		Router:   router,
		Realtime: decklogClient,
		Logger:   logger,
		Metrics:  domainMetrics,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	webhookDispatcher := &notify.Dispatcher{
		Channels: router,
		Settings: settings,
		HTTP:     &http.Client{Timeout: 10 * time.Second},
		Mailer:   notify.SMTPMailer(settings),
		Producer: producer,
		Topic:    topology.TopicLookoutIncidents,
		Logger:   logger,
		Metrics:  domainMetrics,
	}
	deliveryWorker := &outbox.Worker[notify.Delivery]{
		Config:     deliveryOutboxConfig,
		Store:      &notify.Store{DB: db, Channels: router, Metrics: domainMetrics, Logger: logger},
		Dispatcher: webhookDispatcher,
		Logger:     logger,
		AlertLabel: "lookout delivery outbox",
	}
	activityStore := &notify.ActivityStore{DB: db, Metrics: domainMetrics, Logger: logger}
	activityWorker := &outbox.Worker[notify.ActivityDelivery]{
		Config:     deliveryOutboxConfig,
		Store:      activityStore,
		Dispatcher: &notify.ActivityDispatcher{Webhook: webhookDispatcher},
		Logger:     logger,
		AlertLabel: "lookout operator activity outbox",
	}
	go deliveryWorker.Run(ctx)
	go activityWorker.Run(ctx)
	go (&notify.Retention{DB: db, Metrics: domainMetrics, Logger: logger}).Run(ctx)

	ownershipConsumer, err := kafka.NewConsumer(cfg.KafkaBrokers, ownership.ConsumerGroup, cfg.KafkaClusterID, "lookout", logger,
		kafka.WithLagTracker(kafka.LagTrackerConfig{
			Gauge: metricsCollector.NewGauge("kafka_consumer_lag", "Kafka consumer lag", []string{"topic", "partition"}),
		}),
	)
	if err != nil {
		logger.WithError(err).Fatal("Failed to create Kafka consumer")
	}
	defer func() { _ = ownershipConsumer.Close() }()
	ownershipHandler := &ownership.Consumer{Reconciler: incidentService, Logger: logger, Metrics: domainMetrics}
	activityHandler := &notify.ActivityConsumer{Sink: activityStore, Channels: router, Logger: logger, Metrics: domainMetrics}
	// The consumer reads the aggregator's local service_events topic. Central
	// control-plane and marketing producers publish there, so mirrored
	// regional copies are not read.
	ownershipConsumer.AddHandler(topology.TopicServiceEvents, func(ctx context.Context, msg kafka.Message) error {
		if err := activityHandler.HandleUntilEnqueued(ctx, msg); err != nil {
			return err
		}
		return ownershipHandler.HandleUntilApplied(ctx, msg)
	})
	healthChecker.AddCheck("kafka", monitoring.KafkaConsumerHealthCheck(ownershipConsumer.GetClient()))
	go func() {
		// Start returns when group membership is lost; restarting the process
		// rejoins the group from committed offsets.
		if startErr := ownershipConsumer.Start(ctx); startErr != nil && ctx.Err() == nil {
			logger.WithError(startErr).Fatal("Cluster ownership consumer exited")
		}
	}()
	go ownershipHandler.ReconcileAtStartup(ctx)

	go registerWithQuartermaster(cfg, qmClient, logger)

	app := server.NewServiceRouter(server.RouterSpec{
		Service:            "lookout",
		Logger:             logger,
		Health:             healthChecker,
		Ready:              readiness,
		Metrics:            metricsCollector,
		Runtime:            cfg.HTTPRuntime,
		DebugToken:         cfg.ServiceToken,
		DebugConfig:        func() any { return liveConfig.Get() },
		DebugConfigOptions: configOptions,
	})
	alertmanagerToken := func() string { return liveConfig.Get().AlertmanagerToken }
	app.POST("/v1/alertmanager", httpapi.AlertmanagerHandler(incidentService, alertmanagerToken, logger, domainMetrics))

	incidentServer := &grpcserver.Server{Incidents: incidentService, Logger: logger}
	server.RegisterEnvFileReload("lookout", logger)
	if runErr := server.Run(ctx, server.RunSpec{
		Service: "lookout",
		Logger:  logger,
		Ready:   readiness,
		HTTP:    []server.HTTPListener{{Name: "http", Port: cfg.HTTPListenPort(), Handler: app}},
		// The gRPC server is built after the port is bound, so HTTP health and
		// the Alertmanager webhook serve while the TLS files are still being
		// synced.
		GRPC: []server.GRPCListener{{Name: "grpc", Port: cfg.GRPCPort, Build: func(buildCtx context.Context) (*grpc.Server, error) {
			return newGRPCServer(buildCtx, cfg, logger, incidentServer, grpcRequests, grpcDuration)
		}}},
		// Only settings read through liveConfig follow a reload; everything
		// else keeps its startup value.
		OnReload: []server.ReloadCallback{liveConfig.Reload},
	}); runErr != nil {
		logger.WithError(runErr).Fatal("Server exited with error")
	}
}

// notifySettings maps the configuration snapshot to the notification settings
// of one routing decision or delivery.
func notifySettings(cfg *appconfig.Lookout) notify.Settings {
	return notify.Settings{
		EmailRecipients:   cfg.NotifyEmailTo,
		SlackWebhookURL:   cfg.SlackWebhookURL,
		DiscordWebhookURL: cfg.DiscordWebhookURL,
		WebappURL:         strings.TrimRight(cfg.WebAppURL, "/"),
		SMTP: email.Config{
			Host:          cfg.SMTPHost,
			Port:          cfg.SMTPPort,
			User:          cfg.SMTPUser,
			Password:      cfg.SMTPPassword,
			From:          cfg.FromEmail,
			FromName:      cfg.FromName,
			AllowInsecure: cfg.SMTPAllowInsecure,
		},
		Branding: cfg.EmailBranding,
	}
}

// newGRPCServer builds the Lookout gRPC server. It waits up to two minutes
// for the TLS files, and stops waiting when ctx ends.
func newGRPCServer(ctx context.Context, cfg *appconfig.Lookout, logger logging.Logger, srv *grpcserver.Server, requests *prometheus.CounterVec, duration *prometheus.HistogramVec) (*grpc.Server, error) {
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
		ServiceOnlyMethods: []string{lookoutpb.LookoutService_AttachInvestigation_FullMethodName},
	})

	tlsCfg := grpcutil.ServerTLSConfig{
		CertFile:      cfg.CertPath,
		KeyFile:       cfg.KeyPath,
		AllowInsecure: cfg.AllowInsecure,
	}
	waitCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := grpcutil.WaitForServerTLSFiles(waitCtx, tlsCfg, logger); err != nil {
		return nil, fmt.Errorf("wait for Lookout gRPC TLS files: %w", err)
	}
	tlsOpt, err := grpcutil.ServerTLS(tlsCfg, logger)
	if err != nil {
		return nil, fmt.Errorf("configure Lookout gRPC TLS: %w", err)
	}

	serverOpts := []grpc.ServerOption{
		// The metrics interceptor sits outermost so authentication rejections
		// are counted in lookout_grpc_requests_total.
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
	lookoutpb.RegisterLookoutServiceServer(grpcServer, srv)
	hs := health.NewServer()
	hs.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
	hs.SetServingStatus(lookoutpb.LookoutService_ServiceDesc.ServiceName, grpc_health_v1.HealthCheckResponse_SERVING)
	server.RegisterHealthServer(grpcServer, hs)
	reflection.Register(grpcServer)
	return grpcServer, nil
}

// registerWithQuartermaster registers the gRPC port without a health
// endpoint.
func registerWithQuartermaster(cfg *appconfig.Lookout, qmClient qmbootstrap.BootstrapClient, logger logging.Logger) {
	req, err := qmbootstrap.NewServiceRequest(qmbootstrap.ServiceRegistration{
		ServiceType:        "lookout",
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
	if _, err := qmbootstrap.BootstrapServiceWithRetry(context.Background(), qmClient, req, logger, qmbootstrap.DefaultRetryConfig("lookout")); err != nil {
		logger.WithError(err).Warn("Quartermaster bootstrap (lookout) failed")
		return
	}
	logger.Info("Quartermaster bootstrap (lookout) ok")
}
