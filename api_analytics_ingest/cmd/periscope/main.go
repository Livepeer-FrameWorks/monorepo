package main

import (
	"context"
	"encoding/json"
	"fmt"
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
	"frameworks/pkg/qmbootstrap"
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
	defer func() { _ = clickhouse.Close() }()

	// Setup monitoring
	healthChecker := monitoring.NewHealthChecker("periscope-ingest", version.Version)
	metricsCollector := monitoring.NewMetricsCollector("periscope-ingest", version.Version, version.GitCommit)

	// Create custom analytics ingestion metrics
	metrics := &handlers.PeriscopeMetrics{
		AnalyticsEvents:         metricsCollector.NewCounter("analytics_events_total", "Analytics events processed", []string{"event_type", "status"}),
		BatchProcessingDuration: metricsCollector.NewHistogram("batch_processing_duration_seconds", "Batch processing time", []string{"source"}, nil),
		ClickHouseInserts:       metricsCollector.NewCounter("clickhouse_inserts_total", "ClickHouse inserts", []string{"table", "status"}),
		DuplicateEvents:         metricsCollector.NewCounter("duplicate_events_total", "Duplicate analytics events skipped", []string{"event_type"}),
		DLQMessages:             metricsCollector.NewCounter("dlq_messages_total", "Messages sent to the DLQ", []string{"topic", "error_type"}),
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
	analyticsTopic := config.GetEnv("ANALYTICS_KAFKA_TOPIC", "analytics_events")
	serviceEventsTopic := config.GetEnv("SERVICE_EVENTS_KAFKA_TOPIC", "service_events")
	dlqTopic := config.GetEnv("DECKLOG_DLQ_KAFKA_TOPIC", "decklog_events_dlq")

	consumer, err := kafka.NewConsumer(brokers, groupID, clusterID, clientID, logger)
	if err != nil {
		logger.WithError(err).Fatal("Failed to create Kafka consumer")
	}

	var dlqProducer *kafka.KafkaProducer
	dlqProducer, err = kafka.NewKafkaProducer(brokers, dlqTopic, clusterID, logger)
	if err != nil {
		logger.WithError(err).Warn("Failed to create DLQ Kafka producer (DLQ disabled)")
		dlqProducer = nil
	} else {
		defer func() { _ = dlqProducer.Close() }()
	}

	wrapWithDLQ := func(consumerName string, handler func(context.Context, kafka.Message) error) func(context.Context, kafka.Message) error {
		return func(ctx context.Context, msg kafka.Message) error {
			if err := handler(ctx, msg); err != nil {
				if dlqProducer == nil {
					return err
				}

				payload, encodeErr := kafka.EncodeDLQMessage(msg, err, consumerName)
				if encodeErr != nil {
					logger.WithError(encodeErr).WithFields(logging.Fields{
						"topic":     msg.Topic,
						"partition": msg.Partition,
						"offset":    msg.Offset,
					}).Error("Failed to encode DLQ payload")
					return encodeErr
				}

				key := msg.Key
				if len(key) == 0 {
					key = []byte(fmt.Sprintf("%s:%d:%d", msg.Topic, msg.Partition, msg.Offset))
				}

				headers := map[string]string{
					"source":         consumerName,
					"original_topic": msg.Topic,
				}
				if tenantID, ok := msg.Headers["tenant_id"]; ok {
					headers["tenant_id"] = tenantID
				}
				if eventType, ok := msg.Headers["event_type"]; ok {
					headers["event_type"] = eventType
				}

				if produceErr := dlqProducer.ProduceMessage(dlqTopic, key, payload, headers); produceErr != nil {
					logger.WithError(produceErr).WithFields(logging.Fields{
						"topic":     msg.Topic,
						"partition": msg.Partition,
						"offset":    msg.Offset,
					}).Error("Failed to publish message to DLQ")
					return produceErr
				}

				if metrics.DLQMessages != nil {
					metrics.DLQMessages.WithLabelValues(msg.Topic, fmt.Sprintf("%T", err)).Inc()
				}

				logger.WithError(err).WithFields(logging.Fields{
					"topic":     msg.Topic,
					"partition": msg.Partition,
					"offset":    msg.Offset,
					"dlq_topic": dlqTopic,
				}).Warn("Message sent to DLQ after handler error")

				return nil
			}

			return nil
		}
	}

	// Subscribe to topics with the handlers
	consumer.AddHandler(analyticsTopic, wrapWithDLQ("periscope-ingest-analytics", eventHandler.HandleMessage))

	serviceHandler := func(ctx context.Context, msg kafka.Message) error {
		var event kafka.ServiceEvent
		if err := json.Unmarshal(msg.Value, &event); err != nil {
			logger.WithError(err).Error("Failed to unmarshal service event")
			return fmt.Errorf("unmarshal service event: %w", err)
		}
		for k, v := range msg.Headers {
			if k == "source" && event.Source == "" {
				event.Source = v
			}
			if k == "tenant_id" && event.TenantID == "" {
				event.TenantID = v
			}
			if k == "event_type" && event.EventType == "" {
				event.EventType = v
			}
		}
		return analyticsHandler.HandleServiceEvent(event)
	}
	consumer.AddHandler(serviceEventsTopic, wrapWithDLQ("periscope-ingest-service", serviceHandler))

	// Now add health checks with all dependencies
	healthChecker.AddCheck("clickhouse", monitoring.ClickHouseNativeHealthCheck(clickhouse))
	healthChecker.AddCheck("kafka", monitoring.KafkaConsumerHealthCheck(consumer.GetClient()))
	if dlqProducer != nil {
		healthChecker.AddCheck("kafka_dlq_producer", monitoring.KafkaProducerHealthCheck(dlqProducer.GetClient()))
	}
	healthChecker.AddCheck("config", monitoring.ConfigurationHealthCheck(map[string]string{
		"CLICKHOUSE_HOST":            clickhouseHost,
		"KAFKA_BROKERS":              brokersEnv,
		"KAFKA_GROUP_ID":             groupID,
		"SERVICE_EVENTS_KAFKA_TOPIC": serviceEventsTopic,
		"DECKLOG_DLQ_KAFKA_TOPIC":    dlqTopic,
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
		defer func() { _ = qc.Close() }()
		healthEndpoint := "/health"
		advertiseHost := config.GetEnv("PERISCOPE_INGEST_HOST", "periscope-ingest")
		clusterID := config.GetEnv("CLUSTER_ID", "")
		req := &pb.BootstrapServiceRequest{
			Type:           "periscope_ingest",
			Version:        version.Version,
			Protocol:       "http",
			HealthEndpoint: &healthEndpoint,
			Port:           18005,
			AdvertiseHost:  &advertiseHost,
			ClusterId: func() *string {
				if clusterID != "" {
					return &clusterID
				}
				return nil
			}(),
		}
		if nodeID := config.GetEnv("NODE_ID", ""); nodeID != "" {
			req.NodeId = &nodeID
		}
		if _, err := qmbootstrap.BootstrapServiceWithRetry(context.Background(), qc, req, logger, qmbootstrap.DefaultRetryConfig("periscope_ingest")); err != nil {
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
		if err := consumer.Close(); err != nil {
			logger.WithError(err).Warn("Failed to close Kafka consumer")
		}
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
