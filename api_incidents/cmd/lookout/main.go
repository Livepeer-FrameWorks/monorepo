package main

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"time"

	lookoutconfig "frameworks/api_incidents/internal/config"
	"frameworks/api_incidents/internal/grpcserver"
	"frameworks/api_incidents/internal/httpapi"
	"frameworks/api_incidents/internal/incidents"
	"frameworks/api_incidents/internal/notify"
	"frameworks/api_incidents/internal/ownership"

	decklogclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/decklog"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	pkgconfig "github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/grpcutil"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/monitoring"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/outbox"
	lookoutpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/lookout"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/qmbootstrap"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/server"
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
	pkgconfig.LoadEnv(logger)
	logger.Info("Starting Lookout (incidents)")

	cfg, err := lookoutconfig.Load()
	if err != nil {
		logger.WithError(err).Fatal("Invalid Lookout configuration")
	}

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

	qmClient, err := quartermaster.NewGRPCClient(quartermaster.GRPCConfig{
		GRPCAddr:      cfg.QuartermasterGRPCAddr,
		Timeout:       10 * time.Second,
		Logger:        logger,
		ServiceToken:  cfg.ServiceToken,
		AllowInsecure: cfg.GRPCAllowInsecure,
		CACertFile:    cfg.GRPCTLSCAPath,
		ServerName:    pkgconfig.GetServiceGRPCTLSServerName("quartermaster"),
	})
	if err != nil {
		logger.WithError(err).Fatal("Failed to create Quartermaster gRPC client")
	}
	defer func() { _ = qmClient.Close() }()

	decklogClient, err := decklogclient.NewBatchedClient(decklogclient.BatchedClientConfig{
		Target:        cfg.DecklogGRPCAddr,
		AllowInsecure: cfg.GRPCAllowInsecure,
		CACertFile:    cfg.GRPCTLSCAPath,
		ServerName:    pkgconfig.GetServiceGRPCTLSServerName("decklog"),
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

	producer, err := kafka.NewKafkaProducer(cfg.KafkaBrokers, cfg.IncidentsTopic, cfg.KafkaClusterID, logger)
	if err != nil {
		logger.WithError(err).Fatal("Failed to create Kafka producer")
	}
	defer func() { _ = producer.Close() }()

	router := notify.Router{}
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
	deliveryWorker := &outbox.Worker[notify.Delivery]{
		Config: deliveryOutboxConfig,
		Store:  &notify.Store{DB: db, Channels: router, Metrics: domainMetrics, Logger: logger},
		Dispatcher: &notify.Dispatcher{
			Channels: router,
			HTTP:     &http.Client{Timeout: 10 * time.Second},
			Mailer:   notify.SMTPMailer,
			Producer: producer,
			Topic:    cfg.IncidentsTopic,
			Logger:   logger,
			Metrics:  domainMetrics,
		},
		Logger:     logger,
		AlertLabel: "lookout delivery outbox",
	}
	go deliveryWorker.Run(ctx)
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
	ownershipConsumer.AddHandler(cfg.ServiceEventsTopic, ownershipHandler.HandleUntilApplied)
	healthChecker.AddCheck("kafka", monitoring.KafkaConsumerHealthCheck(ownershipConsumer.GetClient()))
	go func() {
		// Start returns when group membership is lost; restarting the process
		// rejoins the group from committed offsets.
		if startErr := ownershipConsumer.Start(ctx); startErr != nil && ctx.Err() == nil {
			logger.WithError(startErr).Fatal("Cluster ownership consumer exited")
		}
	}()
	go ownershipHandler.ReconcileAtStartup(ctx)

	go serveGRPC(cfg, logger, &grpcserver.Server{Incidents: incidentService, Logger: logger}, grpcRequests, grpcDuration)

	go registerWithQuartermaster(cfg, qmClient, logger)

	serverConfig := server.DefaultConfig("lookout", cfg.HTTPPort)
	app := server.SetupServiceRouter(logger, "lookout", healthChecker, metricsCollector)
	app.POST("/v1/alertmanager", httpapi.AlertmanagerHandler(incidentService, lookoutconfig.AlertmanagerToken, logger, domainMetrics))

	server.RegisterEnvFileReload("lookout", logger)
	if err := server.Start(serverConfig, app, logger); err != nil {
		logger.WithError(err).Fatal("Lookout HTTP server failed")
	}
}

func serveGRPC(cfg lookoutconfig.Config, logger logging.Logger, srv *grpcserver.Server, requests *prometheus.CounterVec, duration *prometheus.HistogramVec) {
	lis, err := net.Listen("tcp", ":"+cfg.GRPCPort)
	if err != nil {
		logger.WithError(err).Fatal("Failed to listen for gRPC")
	}
	authInterceptor := middleware.GRPCAuthInterceptor(middleware.GRPCAuthConfig{
		ServiceToken: cfg.ServiceToken,
		JWTSecret:    []byte(cfg.JWTSecret),
		Logger:       logger,
		SkipMethods: []string{
			"/grpc.health.v1.Health/Check",
			"/grpc.health.v1.Health/Watch",
		},
		ServiceOnlyMethods: []string{lookoutpb.LookoutService_AttachInvestigation_FullMethodName},
	})

	tlsCfg := grpcutil.ServerTLSConfig{
		CertFile:      cfg.GRPCTLSCertPath,
		KeyFile:       cfg.GRPCTLSKeyPath,
		AllowInsecure: cfg.GRPCAllowInsecure,
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if waitErr := grpcutil.WaitForServerTLSFiles(waitCtx, tlsCfg, logger); waitErr != nil {
		logger.WithError(waitErr).Fatal("Timed out waiting for Lookout gRPC TLS files")
	}
	tlsOpt, err := grpcutil.ServerTLS(tlsCfg, logger)
	if err != nil {
		logger.WithError(err).Fatal("Failed to configure Lookout gRPC TLS")
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
	grpc_health_v1.RegisterHealthServer(grpcServer, hs)
	reflection.Register(grpcServer)

	logger.WithField("port", cfg.GRPCPort).Info("Lookout gRPC server starting")
	if err := grpcServer.Serve(lis); err != nil {
		logger.WithError(err).Fatal("Lookout gRPC server failed")
	}
}

func registerWithQuartermaster(cfg lookoutconfig.Config, qmClient *quartermaster.GRPCClient, logger logging.Logger) {
	port, err := strconv.Atoi(cfg.GRPCPort)
	if err != nil || port <= 0 || port > 65535 {
		logger.Warn("Quartermaster bootstrap skipped: invalid gRPC port")
		return
	}
	advertiseHost := cfg.AdvertiseHost
	req := &quartermasterpb.BootstrapServiceRequest{
		Type:          "lookout",
		Version:       version.Version,
		Protocol:      "grpc",
		Port:          int32(port),
		AdvertiseHost: &advertiseHost,
	}
	if cfg.ClusterID != "" {
		clusterID := cfg.ClusterID
		req.ClusterId = &clusterID
	}
	if cfg.NodeID != "" {
		nodeID := cfg.NodeID
		req.NodeId = &nodeID
	}
	if _, err := qmbootstrap.BootstrapServiceWithRetry(context.Background(), qmClient, req, logger, qmbootstrap.DefaultRetryConfig("lookout")); err != nil {
		logger.WithError(err).Warn("Quartermaster bootstrap (lookout) failed")
		return
	}
	logger.Info("Quartermaster bootstrap (lookout) ok")
}
