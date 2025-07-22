package main

import (
	"context"
	"net/http"
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

	// Start consuming
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		if err := consumer.Start(ctx); err != nil {
			logger.WithError(err).Error("Kafka consumer error")
		}
	}()

	// Optional health check server
	if config.GetEnvBool("ENABLE_HEALTH_ENDPOINT", true) {
		go startHealthServer(yugaDB, clickhouse, logger)
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

func startHealthServer(yugaDB database.PostgresConn, clickhouse database.ClickHouseNativeConn, logger logging.Logger) {
	middleware.SetGinMode("release")
	r := middleware.NewEngine()

	// Add shared middleware
	middleware.SetupCommonMiddleware(r, logger)

	r.GET("/health", func(c middleware.Context) {
		// Check YugaDB
		if err := yugaDB.Ping(); err != nil {
			c.JSON(http.StatusServiceUnavailable, middleware.H{
				"status":  "unhealthy",
				"service": "periscope-ingest",
				"version": config.GetEnv("VERSION", "1.0.0"),
				"mode":    "kafka-consumer",
				"error":   "YugaDB: " + err.Error(),
			})
			return
		}

		// Check ClickHouse
		if err := clickhouse.Ping(context.Background()); err != nil {
			c.JSON(http.StatusServiceUnavailable, middleware.H{
				"status":  "unhealthy",
				"service": "periscope-ingest",
				"version": config.GetEnv("VERSION", "1.0.0"),
				"mode":    "kafka-consumer",
				"error":   "ClickHouse: " + err.Error(),
			})
			return
		}

		c.JSON(http.StatusOK, middleware.H{
			"status":  "healthy",
			"service": "periscope-ingest",
			"version": config.GetEnv("VERSION", "1.0.0"),
			"mode":    "kafka-consumer",
		})
	})

	// Basic metrics endpoint for Prometheus
	r.GET("/metrics", func(c middleware.Context) {
		c.Header("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		c.String(http.StatusOK, "# HELP periscope_ingest_up Service availability\n# TYPE periscope_ingest_up gauge\nperiscope_ingest_up 1\n")
	})

	port := config.GetEnv("HEALTH_PORT", "18005")
	logger.Infof("Health endpoint available on port %s", port)

	if err := r.Run(":" + port); err != nil {
		logger.WithError(err).Error("Health server error")
	}
}
