package main

import (
	"context"
	sqldriver "database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"frameworks/api_analytics_ingest/internal/handlers"
	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/monitoring"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/qmbootstrap"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/server"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/version"
)

func main() {
	if version.HandleCLI() {
		return
	}

	// Setup logger
	logger := logging.NewLoggerWithService("periscope-ingest")

	// Load environment variables
	config.LoadEnv(logger)

	logger.Info("Starting Periscope-Ingest (Analytics Event Processing)")

	clickhouseAddr := config.RequireEnv("CLICKHOUSE_ADDR")
	clickhouseDB := config.RequireEnv("CLICKHOUSE_DB")
	clickhouseUser := config.RequireEnv("CLICKHOUSE_USER")
	clickhousePassword := config.RequireEnv("CLICKHOUSE_PASSWORD")
	brokersEnv := config.RequireEnv("KAFKA_BROKERS")
	clusterID := config.RequireEnv("KAFKA_CLUSTER_ID")
	serviceToken := config.RequireEnv("SERVICE_TOKEN")
	quartermasterGRPCAddr := config.GetEnv("QUARTERMASTER_GRPC_ADDR", "quartermaster:19002")

	// Connect to ClickHouse
	chConfig := database.DefaultClickHouseConfig()
	chConfig.Addr = strings.Split(clickhouseAddr, ",")
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
		ProjectionDivergences:   metricsCollector.NewCounter("projection_divergence_total", "Projection rows whose rated field value diverged from a prior projection beyond per-meter epsilon", []string{"table", "meter", "field"}),
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

	consumer, err := kafka.NewConsumer(brokers, groupID, clusterID, clientID, logger,
		kafka.WithLagTracker(kafka.LagTrackerConfig{Gauge: metrics.KafkaLag}),
	)
	if err != nil {
		logger.WithError(err).Fatal("Failed to create Kafka consumer")
	}

	var dlqProducer *kafka.KafkaProducer
	dlqProducer, err = kafka.NewKafkaProducer(brokers, dlqTopic, clusterID, logger)
	if err != nil {
		logger.WithError(err).Warn("Failed to create DLQ Kafka producer (DLQ disabled)")
		dlqProducer = nil
	}

	wrapConsumerHandler := func(consumerName string, handler func(context.Context, kafka.Message) error, useDLQ bool) func(context.Context, kafka.Message) error {
		return func(ctx context.Context, msg kafka.Message) error {
			start := time.Now()
			err := handler(ctx, msg)
			for attempt := 1; err != nil && isRetryableConsumerError(err); attempt++ {
				delay := retryableConsumerBackoff(attempt)
				logger.WithError(err).WithFields(logging.Fields{
					"topic":       msg.Topic,
					"partition":   msg.Partition,
					"offset":      msg.Offset,
					"attempt":     attempt,
					"retry_in":    delay.String(),
					"consumer":    consumerName,
					"dlq_enabled": useDLQ,
				}).Warn("Handler dependency failed; retrying message")

				timer := time.NewTimer(delay)
				select {
				case <-ctx.Done():
					if !timer.Stop() {
						select {
						case <-timer.C:
						default:
						}
					}
				case <-timer.C:
				}
				if ctx.Err() != nil {
					err = ctx.Err()
					break
				}

				err = handler(ctx, msg)
			}
			if metrics.KafkaDuration != nil {
				metrics.KafkaDuration.WithLabelValues("consume").Observe(time.Since(start).Seconds())
			}
			if metrics.KafkaMessages != nil {
				status := "ok"
				if err != nil {
					status = "error"
				}
				metrics.KafkaMessages.WithLabelValues(msg.Topic, "consume", status).Inc()
			}
			if err != nil {
				if isRetryableConsumerError(err) {
					logger.WithError(err).WithFields(logging.Fields{
						"topic":     msg.Topic,
						"partition": msg.Partition,
						"offset":    msg.Offset,
					}).Warn("Handler dependency failed; leaving message uncommitted for retry")
					return err
				}
				if !useDLQ {
					return err
				}
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
	wrapWithDLQ := func(consumerName string, handler func(context.Context, kafka.Message) error) func(context.Context, kafka.Message) error {
		return wrapConsumerHandler(consumerName, handler, true)
	}
	wrapRetryOnly := func(consumerName string, handler func(context.Context, kafka.Message) error) func(context.Context, kafka.Message) error {
		return wrapConsumerHandler(consumerName, handler, false)
	}

	// Subscribe to local regional topics. Cross-region mirrored topics
	// from MIRROR_REGION_PREFIXES are wired separately below.
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
			// Envelope headers — propagate to the event when the producer
			// didn't stamp the body. Decklog backfills source on emit so
			// the headers normally agree; honor either source.
			if k == "source_region" && event.SourceRegion == "" {
				event.SourceRegion = v
			}
			if k == "source_cluster_id" && event.SourceClusterID == "" {
				event.SourceClusterID = v
			}
			if k == "stream_origin_region" && event.StreamOriginRegion == "" {
				event.StreamOriginRegion = v
			}
			if k == "stream_origin_cluster_id" && event.StreamOriginClusterID == "" {
				event.StreamOriginClusterID = v
			}
		}
		return analyticsHandler.HandleServiceEvent(event)
	}
	consumer.AddHandler(serviceEventsTopic, wrapWithDLQ("periscope-ingest-service", serviceHandler))

	// Raw MistTrigger audit/replay topic. Decklog republishes the original
	// MistTrigger envelope for the seven final/accounting trigger types so
	// Periscope can populate raw_mist_triggers for incident recovery and
	// reparse. Configurable via RAW_MIST_TRIGGERS_KAFKA_TOPIC; set to "-"
	// to disable consumption on this instance.
	rawTriggersTopic := strings.TrimSpace(config.GetEnv("RAW_MIST_TRIGGERS_KAFKA_TOPIC", "analytics.raw_mist_triggers"))
	if rawTriggersTopic != "" && rawTriggersTopic != "-" {
		// Raw final-trigger projection is billing-critical. Let Kafka
		// retry transient ClickHouse failures instead of committing the
		// offset via DLQ; poison protobuf payloads are swallowed inside
		// HandleRawMistTriggerMessage after logging.
		consumer.AddHandler(rawTriggersTopic, wrapRetryOnly("periscope-ingest-raw-triggers", analyticsHandler.HandleRawMistTriggerMessage))
	}

	// Mirrored-topic subscriptions. MIRROR_REGION_PREFIXES is a comma-
	// separated list of region IDs (e.g. "us-east,ap-tokyo"); each adds
	// {region}.analytics_events and {region}.service_events to the
	// subscription set. Empty = consume only local-cluster topics.
	mirrorPrefixes := strings.TrimSpace(config.GetEnv("MIRROR_REGION_PREFIXES", ""))
	if mirrorPrefixes != "" {
		for _, prefix := range strings.Split(mirrorPrefixes, ",") {
			prefix = strings.TrimSpace(prefix)
			if prefix == "" {
				continue
			}
			mirroredAnalytics := prefix + "." + analyticsTopic
			mirroredServiceEvents := prefix + "." + serviceEventsTopic
			consumer.AddHandler(mirroredAnalytics, wrapWithDLQ("periscope-ingest-analytics-mirror:"+prefix, eventHandler.HandleMessage))
			consumer.AddHandler(mirroredServiceEvents, wrapWithDLQ("periscope-ingest-service-mirror:"+prefix, serviceHandler))
			logger.WithFields(logging.Fields{
				"region_prefix":   prefix,
				"analytics_topic": mirroredAnalytics,
				"service_events":  mirroredServiceEvents,
			}).Info("Subscribed to MirrorMaker2 mirrored topics")
		}
	}

	// Now add health checks with all dependencies
	healthChecker.AddCheck("clickhouse", monitoring.ClickHouseNativeHealthCheck(clickhouse))
	healthChecker.AddCheck("kafka", monitoring.KafkaConsumerHealthCheck(consumer.GetClient()))
	if dlqProducer != nil {
		healthChecker.AddCheck("kafka_dlq_producer", monitoring.KafkaProducerHealthCheck(dlqProducer.GetClient()))
	}
	healthChecker.AddCheck("config", monitoring.ConfigurationHealthCheck(map[string]string{
		"CLICKHOUSE_ADDR":            clickhouseAddr,
		"KAFKA_BROKERS":              brokersEnv,
		"KAFKA_GROUP_ID":             groupID,
		"SERVICE_EVENTS_KAFKA_TOPIC": serviceEventsTopic,
		"DECKLOG_DLQ_KAFKA_TOPIC":    dlqTopic,
	}))

	// Start consuming
	ctx, cancel := context.WithCancel(context.Background())
	consumerDone := make(chan error, 1)
	go func() {
		consumerDone <- consumer.Start(ctx)
	}()

	// Start the canonical 5-min ledger rebuilders. Each runs on its own
	// goroutine at LedgerRebuildInterval and projects the trailing window
	// from its source table into the append-only ledger. See
	// docs/architecture/meter-contracts.md.
	ledgerScheduler := handlers.NewLedgerScheduler(analyticsHandler)
	ledgerScheduler.Start(ctx)
	logger.Info("Started 5-minute ledger rebuilders")

	// Optional health check server
	if config.GetEnvBool("ENABLE_HEALTH_ENDPOINT", true) {
		go startHealthServer(healthChecker, metricsCollector, logger)
	}

	logger.Info("Periscope-Ingest started - consuming analytics events from Kafka")

	// Best-effort service registration in Quartermaster (using gRPC)
	go func() {
		qc, err := qmclient.NewGRPCClient(qmclient.GRPCConfig{
			GRPCAddr:      quartermasterGRPCAddr,
			Timeout:       10 * time.Second,
			Logger:        logger,
			ServiceToken:  serviceToken,
			AllowInsecure: config.GetEnvBool("GRPC_ALLOW_INSECURE", false),
			CACertFile:    config.GetEnv("GRPC_TLS_CA_PATH", ""),
			ServerName:    config.GetServiceGRPCTLSServerName("quartermaster"),
		})
		if err != nil {
			logger.WithError(err).Warn("Failed to create Quartermaster gRPC client")
			return
		}
		defer func() { _ = qc.Close() }()
		healthEndpoint := "/health"
		advertiseHost := config.GetEnv("PERISCOPE_INGEST_HOST", "periscope-ingest")
		clusterID := config.GetEnv("CLUSTER_ID", "")
		req := &quartermasterpb.BootstrapServiceRequest{
			Type:           "periscope-ingest",
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
		if _, err := qmbootstrap.BootstrapServiceWithRetry(context.Background(), qc, req, logger, qmbootstrap.DefaultRetryConfig("periscope-ingest")); err != nil {
			logger.WithError(err).Warn("Quartermaster bootstrap (periscope-ingest) failed")
		} else {
			logger.Info("Quartermaster bootstrap (periscope-ingest) ok")
		}
	}()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	select {
	case <-sigChan:
	case err := <-consumerDone:
		if err != nil && !errors.Is(err, context.Canceled) {
			logger.WithError(err).Fatal("Kafka consumer exited")
		}
	}
	logger.Info("Shutting down Periscope-Ingest...")

	// Cleanup
	cancel()
	if consumer != nil {
		closeWithTimeout(logger, "Kafka consumer", 10*time.Second, consumer.Close)
	}
	select {
	case err := <-consumerDone:
		if err != nil && !errors.Is(err, context.Canceled) {
			logger.WithError(err).Error("Kafka consumer error")
		}
	case <-time.After(2 * time.Second):
		logger.Warn("Kafka consumer did not report shutdown before timeout")
	}
	if dlqProducer != nil {
		closeWithTimeout(logger, "DLQ Kafka producer", 10*time.Second, dlqProducer.Close)
	}

	logger.Info("Periscope-Ingest stopped")
}

func isRetryableConsumerError(err error) bool {
	if err == nil {
		return false
	}

	if errors.Is(err, context.Canceled) ||
		errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, sqldriver.ErrBadConn) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNREFUSED) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, syscall.ECONNABORTED) ||
		errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ETIMEDOUT) {
		return true
	}

	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}

	msg := strings.ToLower(err.Error())
	for _, fragment := range []string{
		"connection refused",
		"connection reset by peer",
		"driver: bad connection",
		"broken pipe",
		"i/o timeout",
		"no such host",
		"unexpected eof",
		"use of closed network connection",
	} {
		if strings.Contains(msg, fragment) {
			return true
		}
	}

	return false
}

func retryableConsumerBackoff(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}

	delay := 500 * time.Millisecond
	for i := 1; i < attempt; i++ {
		delay *= 2
		if delay >= 30*time.Second {
			return 30 * time.Second
		}
	}
	return delay
}

func closeWithTimeout(logger logging.Logger, name string, timeout time.Duration, closeFn func() error) {
	done := make(chan error, 1)
	go func() {
		done <- closeFn()
	}()

	select {
	case err := <-done:
		if err != nil {
			logger.WithError(err).WithField("component", name).Warn("Failed to close component")
		}
	case <-time.After(timeout):
		logger.WithField("component", name).Warn("Timed out closing component")
	}
}

func startHealthServer(healthChecker *monitoring.HealthChecker, metricsCollector *monitoring.MetricsCollector, logger logging.Logger) {
	router := server.SetupServiceRouter(logger, "periscope-ingest", healthChecker, metricsCollector)

	serverConfig := server.DefaultConfig("periscope-ingest", "18005")
	server.RegisterEnvFileReload("periscope-ingest", logger)
	if err := server.Start(serverConfig, router, logger); err != nil {
		logger.WithError(err).Error("Health server error")
	}
}
