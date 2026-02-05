package main

import (
	"context"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"frameworks/api_firehose/internal/grpc"
	qmclient "frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/config"
	"frameworks/pkg/kafka"
	"frameworks/pkg/logging"
	"frameworks/pkg/monitoring"
	pb "frameworks/pkg/proto"
	"frameworks/pkg/server"
	"frameworks/pkg/version"
)

func main() {
	// Setup logger
	logger := logging.NewLoggerWithService("decklog")

	// Load environment variables
	config.LoadEnv(logger)

	logger.Info("Starting Decklog (Firehose API)")

	// Setup monitoring
	healthChecker := monitoring.NewHealthChecker("decklog", version.Version)
	metricsCollector := monitoring.NewMetricsCollector("decklog", version.Version, version.GitCommit)

	// Setup Kafka producer
	brokers := strings.Split(config.RequireEnv("KAFKA_BROKERS"), ",")
	clusterID := config.RequireEnv("KAFKA_CLUSTER_ID")
	serviceToken := config.RequireEnv("SERVICE_TOKEN")
	quartermasterGRPCAddr := config.GetEnv("QUARTERMASTER_GRPC_ADDR", "quartermaster:19002")
	analyticsTopic := config.GetEnv("ANALYTICS_KAFKA_TOPIC", "analytics_events")
	serviceEventsTopic := config.GetEnv("SERVICE_EVENTS_KAFKA_TOPIC", "service_events")

	producer, err := kafka.NewKafkaProducer(brokers, analyticsTopic, clusterID, logger)
	if err != nil {
		logger.WithError(err).Fatal("Failed to create Kafka producer")
	}
	defer func() {
		if err := producer.Close(); err != nil {
			logger.WithError(err).Error("Failed to close Kafka producer")
		}
	}()

	// Add health checks
	healthChecker.AddCheck("kafka_producer", monitoring.KafkaProducerHealthCheck(producer.GetClient()))
	healthChecker.AddCheck("config", monitoring.ConfigurationHealthCheck(map[string]string{
		"KAFKA_BROKERS":    strings.Join(brokers, ","),
		"KAFKA_CLUSTER_ID": clusterID,
	}))

	// Create custom event streaming metrics
	metrics := &grpc.DecklogMetrics{
		EventsIngested:     metricsCollector.NewCounter("events_ingested_total", "Total events ingested", []string{"event_type", "status"}),
		ProcessingDuration: metricsCollector.NewHistogram("event_processing_duration_seconds", "Event processing duration", []string{"event_type"}, nil),
		GRPCRequests:       metricsCollector.NewCounter("grpc_requests_total", "gRPC requests", []string{"method", "status"}),
	}

	// Create Kafka metrics
	metrics.KafkaMessages, metrics.KafkaDuration, metrics.KafkaLag = metricsCollector.CreateKafkaMetrics()

	// Get TLS configuration
	certFile := config.GetEnv("DECKLOG_TLS_CERT_FILE", "/etc/letsencrypt/live/decklog/fullchain.pem")
	keyFile := config.GetEnv("DECKLOG_TLS_KEY_FILE", "/etc/letsencrypt/live/decklog/privkey.pem")
	allowInsecure := config.GetEnvBool("DECKLOG_ALLOW_INSECURE", false)

	// Create gRPC server
	grpcServer, err := grpc.NewGRPCServer(grpc.GRPCServerConfig{
		Producer:           producer,
		Logger:             logger,
		Metrics:            metrics,
		CertFile:           certFile,
		KeyFile:            keyFile,
		AllowInsecure:      allowInsecure,
		ServiceToken:       serviceToken,
		ServiceEventsTopic: serviceEventsTopic,
	})
	if err != nil {
		logger.WithError(err).Fatal("Failed to create gRPC server")
	}

	// gRPC listener
	port := config.GetEnv("PORT", "18006")
	lis, err := net.Listen("tcp", ":"+port)
	if err != nil {
		logger.WithError(err).Fatal("Failed to listen")
	}

	// Setup router with unified monitoring
	router := server.SetupServiceRouter(logger, "decklog", healthChecker, metricsCollector)

	metricsPort := config.GetEnv("DECKLOG_METRICS_PORT", "18026")
	httpSrv := &http.Server{Addr: ":" + metricsPort, Handler: router}

	logger.WithFields(logging.Fields{"grpc_port": port, "http_port": metricsPort}).Info("Starting Decklog servers")

	// Best-effort service registration in Quartermaster (using gRPC)
	go func() {
		qc, err := qmclient.NewGRPCClient(qmclient.GRPCConfig{
			GRPCAddr:     quartermasterGRPCAddr,
			Timeout:      10 * time.Second,
			Logger:       logger,
			ServiceToken: serviceToken,
		})
		if err != nil {
			logger.WithError(err).Warn("Failed to create Quartermaster gRPC client")
			return
		}
		defer func() { _ = qc.Close() }()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		pi, _ := strconv.Atoi(port)
		advertiseHost := config.GetEnv("DECKLOG_HOST", "decklog")
		healthEndpoint := "/health"
		clusterID := config.GetEnv("CLUSTER_ID", "")
		if _, err := qc.BootstrapService(ctx, &pb.BootstrapServiceRequest{
			Type:           "decklog",
			Version:        version.Version,
			Protocol:       "grpc",
			Port:           int32(pi),
			AdvertiseHost:  &advertiseHost,
			HealthEndpoint: &healthEndpoint,
			ClusterId: func() *string {
				if clusterID != "" {
					return &clusterID
				}
				return nil
			}(),
		}); err != nil {
			logger.WithError(err).Warn("Quartermaster bootstrap (decklog) failed")
		} else {
			logger.Info("Quartermaster bootstrap (decklog) ok")
		}
	}()

	// Handle graceful shutdown
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		logger.Info("Shutting down gRPC and HTTP listeners...")
		grpcServer.GracefulStop()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutdownCtx)
		_ = lis.Close()
	}()

	// Start servers
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.WithError(err).Error("HTTP server error")
		}
	}()

	if err := grpcServer.Serve(lis); err != nil {
		logger.WithError(err).Fatal("gRPC server error")
	}
}
