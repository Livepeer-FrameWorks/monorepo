package main

import (
	"context"
	"strings"
	"time"

	"frameworks/api_firehose/internal/appconfig"
	"frameworks/api_firehose/internal/grpc"
	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/monitoring"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/qmbootstrap"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/server"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/version"

	googlegrpc "google.golang.org/grpc"
)

func main() {
	if version.HandleCLI() {
		return
	}

	// Setup logger
	logger := logging.NewLoggerWithService("decklog")

	// Load environment variables
	config.LoadEnv(logger)

	logger.Info("Starting Decklog (Firehose API)")

	configOptions := config.Options{Service: "decklog", Logger: logger}
	cfg, err := config.Load[appconfig.Decklog](configOptions)
	if err != nil {
		logger.WithError(err).Fatal("Invalid configuration")
	}
	cfg.ApplyLogLevel(logger)
	metadataPolicy, err := middleware.ParseMetadataPolicy(cfg.MetadataPolicy)
	if err != nil {
		logger.WithError(err).Fatal("Invalid configuration")
	}

	// Setup monitoring
	healthChecker := monitoring.NewHealthChecker("decklog", version.Version)
	metricsCollector := monitoring.NewMetricsCollector("decklog", version.Version, version.GitCommit)

	// Setup Kafka producer
	producer, err := kafka.NewKafkaProducer(cfg.KafkaBrokers, cfg.AnalyticsTopic, cfg.KafkaClusterID, logger)
	if err != nil {
		logger.WithError(err).Fatal("Failed to create Kafka producer")
	}

	// Add health checks
	healthChecker.AddCheck("kafka_producer", monitoring.KafkaProducerHealthCheck(producer.GetClient()))
	healthChecker.AddCheck("config", monitoring.ConfigurationHealthCheck(map[string]string{
		"KAFKA_BROKERS":    strings.Join(cfg.KafkaBrokers, ","),
		"KAFKA_CLUSTER_ID": cfg.KafkaClusterID,
	}))

	// Every ingress RPC produces synchronously to Kafka, so an instance whose
	// producer cannot reach the brokers cannot accept events.
	readiness := monitoring.NewReadinessChecker("decklog", version.Version)
	readiness.AddCheck("kafka_producer", monitoring.KafkaProducerHealthCheck(producer.GetClient()))

	// Create custom event streaming metrics
	metrics := &grpc.DecklogMetrics{
		EventsIngested:     metricsCollector.NewCounter("events_ingested_total", "Total events ingested", []string{"event_type", "status"}),
		ProcessingDuration: metricsCollector.NewHistogram("event_processing_duration_seconds", "Event processing duration", []string{"event_type"}, nil),
		GRPCRequests:       metricsCollector.NewCounter("grpc_requests_total", "gRPC requests", []string{"method", "status"}),
	}

	// Create Kafka producer metrics (no consumer, so no lag gauge).
	metrics.KafkaMessages = metricsCollector.NewCounter("kafka_messages_total", "Total Kafka messages", []string{"topic", "operation", "status"})
	metrics.KafkaDuration = metricsCollector.NewHistogram("kafka_operation_duration_seconds", "Kafka operation duration", []string{"operation"}, nil)

	// gRPC server settings; Run builds the server after the port is bound, so
	// HTTP health serves while the TLS files are still being synced.
	grpcConfig := grpc.GRPCServerConfig{
		Producer:           producer,
		Logger:             logger,
		Metrics:            metrics,
		CertFile:           cfg.CertPath,
		KeyFile:            cfg.KeyPath,
		AllowInsecure:      cfg.AllowInsecure,
		ServiceToken:       cfg.ServiceToken,
		MetadataPolicy:     metadataPolicy,
		ServiceEventsTopic: cfg.ServiceEventsTopic,
		RawTriggersTopic:   cfg.RawTriggersTopic,
		// Envelope identity for this Decklog instance — backfills onto
		// events that arrive without source_region / source_cluster_id
		// already set. Empty when running outside a regional deployment.
		SourceRegion:    cfg.BackfillRegion(),
		SourceClusterID: cfg.ClusterID,
	}

	// Health, readiness, and metrics are served over HTTP on the metrics port;
	// events arrive over gRPC.
	router := server.NewServiceRouter(server.RouterSpec{
		Service:            "decklog",
		Logger:             logger,
		Health:             healthChecker,
		Ready:              readiness,
		Metrics:            metricsCollector,
		Runtime:            cfg.HTTPRuntime,
		DebugToken:         cfg.ServiceToken,
		DebugConfig:        func() any { return cfg },
		DebugConfigOptions: configOptions,
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Best-effort service registration in Quartermaster (using gRPC)
	go registerWithQuartermaster(ctx, cfg, logger)

	server.RegisterEnvFileReload("decklog", logger)
	if runErr := server.Run(ctx, server.RunSpec{
		Service: "decklog",
		Logger:  logger,
		Ready:   readiness,
		HTTP:    []server.HTTPListener{{Name: "metrics", Port: cfg.MetricsPort, Handler: router}},
		GRPC: []server.GRPCListener{{Name: "grpc", Port: cfg.GRPCPort, Build: func(ctx context.Context) (*googlegrpc.Server, error) {
			return grpc.NewGRPCServer(ctx, grpcConfig)
		}}},
		OnShutdown: []func(context.Context){func(context.Context) {
			if closeErr := producer.Close(); closeErr != nil {
				logger.WithError(closeErr).Error("Failed to close Kafka producer")
			}
		}},
	}); runErr != nil {
		logger.WithError(runErr).Fatal("Server exited with error")
	}
}

// registerWithQuartermaster registers the gRPC ingress port with protocol grpc
// and the /health endpoint from servicedefs.
func registerWithQuartermaster(ctx context.Context, cfg *appconfig.Decklog, logger logging.Logger) {
	qc, err := qmclient.NewGRPCClient(qmclient.GRPCConfig{
		GRPCAddr:      cfg.QuartermasterGRPCAddr,
		Timeout:       10 * time.Second,
		Logger:        logger,
		ServiceToken:  cfg.ServiceToken,
		AllowInsecure: cfg.AllowInsecure,
		CACertFile:    cfg.CAPath,
		ServerName:    cfg.QuartermasterGRPCTLSServerName,
	})
	if err != nil {
		logger.WithError(err).Warn("Failed to create Quartermaster gRPC client")
		return
	}
	defer func() { _ = qc.Close() }()
	req, err := qmbootstrap.NewServiceRequest(qmbootstrap.ServiceRegistration{
		ServiceType:   "decklog",
		Protocol:      "grpc",
		Port:          cfg.GRPCPort,
		AdvertiseHost: cfg.AdvertiseHost,
		ClusterID:     cfg.ClusterID,
		NodeID:        cfg.NodeID,
	})
	if err != nil {
		logger.WithError(err).Warn("Quartermaster bootstrap skipped")
		return
	}
	if _, err := qmbootstrap.BootstrapServiceWithRetry(ctx, qc, req, logger, qmbootstrap.DefaultRetryConfig("decklog")); err != nil {
		logger.WithError(err).Warn("Quartermaster bootstrap (decklog) failed")
	} else {
		logger.Info("Quartermaster bootstrap (decklog) ok")
	}
}
