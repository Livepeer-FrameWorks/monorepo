package main

import (
	"context"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"frameworks/api_firehose/internal/grpc"
	qmapi "frameworks/pkg/api/quartermaster"
	qmclient "frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/config"
	"frameworks/pkg/kafka"
	"frameworks/pkg/logging"
	"frameworks/pkg/monitoring"
	"frameworks/pkg/server"
	"frameworks/pkg/version"
	"strconv"
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
	brokers := strings.Split(config.GetEnv("KAFKA_BROKERS", "localhost:9092"), ",")
	clusterID := config.GetEnv("KAFKA_CLUSTER_ID", "frameworks")

	producer, err := kafka.NewKafkaProducer(brokers, clusterID, logger)
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
		"KAFKA_BROKERS":    config.GetEnv("KAFKA_BROKERS", ""),
		"KAFKA_CLUSTER_ID": config.GetEnv("KAFKA_CLUSTER_ID", ""),
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
	certFile := config.GetEnv("TLS_CERT_FILE", "/etc/letsencrypt/live/decklog/fullchain.pem")
	keyFile := config.GetEnv("TLS_KEY_FILE", "/etc/letsencrypt/live/decklog/privkey.pem")
	allowInsecure := config.GetEnvBool("ALLOW_INSECURE", false)

	// Create gRPC server
	grpcServer, err := grpc.NewGRPCServer(producer, logger, metrics, certFile, keyFile, allowInsecure)
	if err != nil {
		logger.WithError(err).Fatal("Failed to create gRPC server")
	}

	// gRPC listener
	port := config.GetEnv("GRPC_PORT", "18006")
	lis, err := net.Listen("tcp", ":"+port)
	if err != nil {
		logger.WithError(err).Fatal("Failed to listen")
	}

	// Setup router with unified monitoring
	router := server.SetupServiceRouter(logger, "decklog", healthChecker, metricsCollector)

	metricsPort := config.GetEnv("METRICS_PORT", "18026")
	httpSrv := &http.Server{Addr: ":" + metricsPort, Handler: router}

	logger.WithFields(logging.Fields{"grpc_port": port, "http_port": metricsPort}).Info("Starting Decklog servers")

	// Best-effort service registration in Quartermaster
	go func() {
		qc := qmclient.NewClient(qmclient.Config{BaseURL: config.GetEnv("QUARTERMASTER_URL", "http://localhost:18002"), ServiceToken: config.GetEnv("SERVICE_TOKEN", ""), Logger: logger})
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		pi, _ := strconv.Atoi(port)
		if _, err := qc.BootstrapService(ctx, &qmapi.BootstrapServiceRequest{Type: "decklog", Version: version.Version, Protocol: "grpc", Port: pi}); err != nil {
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
