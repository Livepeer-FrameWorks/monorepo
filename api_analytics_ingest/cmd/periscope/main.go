package main

import (
	"context"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"frameworks/api_analytics_ingest/internal/handlers"
	qmclient "frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/config"
	"frameworks/pkg/database"
	"frameworks/pkg/kafka"
	"frameworks/pkg/logging"
	"frameworks/pkg/monitoring"
	pb "frameworks/pkg/proto"
	"frameworks/pkg/server"
	"frameworks/pkg/version"
)

func main() {
	// Setup logger
	logger := logging.NewLoggerWithService("periscope-ingest")

	// Load environment variables
	config.LoadEnv(logger)

	logger.Info("Starting Periscope-Ingest (Analytics Event Processing)")

	clickhouseHost := config.RequireEnv("CLICKHOUSE_HOST")
	clickhouseDB := config.RequireEnv("CLICKHOUSE_DB")
	clickhouseUser := config.RequireEnv("CLICKHOUSE_USER")
	clickhousePassword := config.RequireEnv("CLICKHOUSE_PASSWORD")
	brokersEnv := config.RequireEnv("KAFKA_BROKERS")
	clusterID := config.RequireEnv("KAFKA_CLUSTER_ID")
	serviceToken := config.RequireEnv("SERVICE_TOKEN")
	quartermasterGRPCAddr := config.GetEnv("QUARTERMASTER_GRPC_ADDR", "quartermaster:19002")

	// Connect to ClickHouse
	chConfig := database.DefaultClickHouseConfig()
	chConfig.Addr = []string{clickhouseHost}
	chConfig.Database = clickhouseDB
	chConfig.Username = clickhouseUser
	chConfig.Password = clickhousePassword
	clickhouse := database.MustConnectClickHouseNative(chConfig, logger)
	defer clickhouse.Close()

	// Setup monitoring
	healthChecker := monitoring.NewHealthChecker("periscope-ingest", version.Version)
	metricsCollector := monitoring.NewMetricsCollector("periscope-ingest", version.Version, version.GitCommit)

	// Create custom analytics ingestion metrics
	metrics := &handlers.PeriscopeMetrics{
		AnalyticsEvents:         metricsCollector.NewCounter("analytics_events_total", "Analytics events processed", []string{"event_type", "status"}),
		BatchProcessingDuration: metricsCollector.NewHistogram("batch_processing_duration_seconds", "Batch processing time", []string{"source"}, nil),
		ClickHouseInserts:       metricsCollector.NewCounter("clickhouse_inserts_total", "ClickHouse inserts", []string{"table", "status"}),
	}

	// Create Kafka metrics
	metrics.KafkaMessages, metrics.KafkaDuration, metrics.KafkaLag = metricsCollector.CreateKafkaMetrics()

	// Initialize handlers
	analyticsHandler := handlers.NewAnalyticsHandler(clickhouse, logger, metrics)
	eventHandler := kafka.NewAnalyticsEventHandler(analyticsHandler.HandleAnalyticsEvent, logger)

	// We'll add health checks after we have the consumer client

	// Setup Kafka consumer
	brokers := strings.Split(brokersEnv, ",")
	groupID := config.GetEnv("KAFKA_GROUP_ID", "periscope-ingest")
	clientID := config.GetEnv("KAFKA_CLIENT_ID", "periscope-ingest")
	topics := strings.Split(config.GetEnv("ANALYTICS_KAFKA_TOPIC", "analytics_events"), ",")

	consumer, err := kafka.NewConsumer(brokers, groupID, clusterID, clientID, logger)
	if err != nil {
		logger.WithError(err).Fatal("Failed to create Kafka consumer")
	}

	// Subscribe to topics with the handler
	for _, topic := range topics {
		consumer.AddHandler(topic, eventHandler.HandleMessage)
	}

	// Now add health checks with all dependencies
	healthChecker.AddCheck("clickhouse", monitoring.ClickHouseNativeHealthCheck(clickhouse))
	healthChecker.AddCheck("kafka", monitoring.KafkaConsumerHealthCheck(consumer.GetClient()))
	healthChecker.AddCheck("config", monitoring.ConfigurationHealthCheck(map[string]string{
		"CLICKHOUSE_HOST": clickhouseHost,
		"KAFKA_BROKERS":   brokersEnv,
		"KAFKA_GROUP_ID":  groupID,
	}))

	// Start consuming
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		if err := consumer.Start(ctx); err != nil {
			logger.WithError(err).Error("Kafka consumer error")
		}
	}()

	// Optional health check server
	if config.GetEnvBool("ENABLE_HEALTH_ENDPOINT", true) {
		go startHealthServer(healthChecker, metricsCollector, logger)
	}

	logger.Info("Periscope-Ingest started - consuming analytics events from Kafka")

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
		defer qc.Close()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		healthEndpoint := "/health"
		advertiseHost := config.GetEnv("PERISCOPE_INGEST_HOST", "periscope-ingest")
		if _, err := qc.BootstrapService(ctx, &pb.BootstrapServiceRequest{
			Type:           "periscope_ingest",
			Version:        version.Version,
			Protocol:       "http",
			HealthEndpoint: &healthEndpoint,
			Port:           18005,
			AdvertiseHost:  &advertiseHost,
		}); err != nil {
			logger.WithError(err).Warn("Quartermaster bootstrap (periscope_ingest) failed")
		} else {
			logger.Info("Quartermaster bootstrap (periscope_ingest) ok")
		}
	}()

	// Wait for interrupt
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	<-sigChan
	logger.Info("Shutting down Periscope-Ingest...")

	// Cleanup
	cancel()
	if consumer != nil {
		consumer.Close()
	}

	logger.Info("Periscope-Ingest stopped")
}

func startHealthServer(healthChecker *monitoring.HealthChecker, metricsCollector *monitoring.MetricsCollector, logger logging.Logger) {
	router := server.SetupServiceRouter(logger, "periscope-ingest", healthChecker, metricsCollector)

	serverConfig := server.DefaultConfig("periscope-ingest", "18005")
	if err := server.Start(serverConfig, router, logger); err != nil {
		logger.WithError(err).Error("Health server error")
	}
}
