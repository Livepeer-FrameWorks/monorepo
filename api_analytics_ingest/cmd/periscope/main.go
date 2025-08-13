package main

import (
	"context"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"frameworks/api_analytics_ingest/internal/handlers"
	"frameworks/pkg/config"
	"frameworks/pkg/database"
	"frameworks/pkg/kafka"
	"frameworks/pkg/logging"
	"frameworks/pkg/middleware"
	"frameworks/pkg/monitoring"
	"frameworks/pkg/version"
)

func main() {
	// Setup logger
	logger := logging.NewLoggerWithService("periscope-ingest")

	// Load environment variables
	config.LoadEnv(logger)

	logger.Info("Starting Periscope-Ingest (Analytics Event Processing)")

	// Database configuration
	dbConfig := database.DefaultConfig()
	dbConfig.URL = config.GetEnv("DATABASE_URL", "postgres://frameworks_user:frameworks_dev@localhost:5432/frameworks?sslmode=disable")
	yugaDB := database.MustConnect(dbConfig, logger)
	defer yugaDB.Close()

	// Connect to ClickHouse
	chConfig := database.DefaultClickHouseConfig()
	chConfig.Addr = []string{config.GetEnv("CLICKHOUSE_HOST", "localhost:9000")}
	chConfig.Database = config.GetEnv("CLICKHOUSE_DB", "frameworks")
	chConfig.Username = config.GetEnv("CLICKHOUSE_USER", "frameworks")
	chConfig.Password = config.GetEnv("CLICKHOUSE_PASSWORD", "frameworks_dev")
	clickhouse := database.MustConnectClickHouseNative(chConfig, logger)
	defer clickhouse.Close()

	// Initialize handlers with both databases
	analyticsHandler := handlers.NewAnalyticsHandler(clickhouse, logger)
	eventHandler := kafka.NewAnalyticsEventHandler(yugaDB, analyticsHandler.HandleAnalyticsEvent, logger)

	// Setup monitoring
	healthChecker := monitoring.NewHealthChecker("periscope-ingest", version.Version)
	metricsCollector := monitoring.NewMetricsCollector("periscope-ingest", version.Version, version.GitCommit)

	// We'll add health checks after we have the consumer client

	// Setup Kafka consumer
	brokers := strings.Split(config.GetEnv("KAFKA_BROKERS", "localhost:9092"), ",")
	groupID := config.GetEnv("KAFKA_GROUP_ID", "periscope-ingest")
	clusterID := config.GetEnv("KAFKA_CLUSTER_ID", "frameworks")
	clientID := config.GetEnv("KAFKA_CLIENT_ID", "periscope-ingest")
	topics := strings.Split(config.GetEnv("KAFKA_TOPICS", "analytics_events"), ",")

	consumer, err := kafka.NewConsumer(brokers, groupID, clusterID, clientID, logger, eventHandler)
	if err != nil {
		logger.WithError(err).Fatal("Failed to create Kafka consumer")
	}

	// Subscribe to topics
	if err := consumer.Subscribe(topics); err != nil {
		logger.WithError(err).Fatal("Failed to subscribe to topics")
	}

	// Now add health checks with all dependencies
	healthChecker.AddCheck("postgres", monitoring.DatabaseHealthCheck(yugaDB))
	healthChecker.AddCheck("clickhouse", monitoring.ClickHouseNativeHealthCheck(clickhouse))
	healthChecker.AddCheck("kafka", monitoring.KafkaConsumerHealthCheck(consumer.GetClient()))
	healthChecker.AddCheck("config", monitoring.ConfigurationHealthCheck(map[string]string{
		"DATABASE_URL":    config.GetEnv("DATABASE_URL", ""),
		"CLICKHOUSE_HOST": config.GetEnv("CLICKHOUSE_HOST", ""),
		"KAFKA_BROKERS":   config.GetEnv("KAFKA_BROKERS", ""),
		"KAFKA_GROUP_ID":  config.GetEnv("KAFKA_GROUP_ID", ""),
	}))

	// Create Kafka and business metrics
	kafkaMessages, kafkaDuration, kafkaLag := metricsCollector.CreateKafkaMetrics()
	businessItems, operations, operationDuration := metricsCollector.CreateBusinessMetrics()

	// TODO: Wire these metrics into handlers
	_ = kafkaMessages
	_ = kafkaDuration
	_ = kafkaLag
	_ = businessItems
	_ = operations
	_ = operationDuration

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
	middleware.SetGinMode("release")
	r := middleware.NewEngine()

	// Add shared middleware
	middleware.SetupCommonMiddleware(r, logger)

	// Add monitoring middleware
	r.Use(metricsCollector.MetricsMiddleware())

	// Health check endpoint with proper checks
	r.GET("/health", healthChecker.Handler())

	// Metrics endpoint for Prometheus
	r.GET("/metrics", metricsCollector.Handler())

	port := config.GetEnv("HEALTH_PORT", "18005")
	logger.Infof("Health endpoint available on port %s", port)

	if err := r.Run(":" + port); err != nil {
		logger.WithError(err).Error("Health server error")
	}
}
