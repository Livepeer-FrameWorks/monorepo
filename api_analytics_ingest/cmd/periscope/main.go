package main

import (
	"context"
	sqldriver "database/sql/driver"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"frameworks/api_analytics_ingest/internal/appconfig"
	"frameworks/api_analytics_ingest/internal/handlers"
	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/monitoring"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/qmbootstrap"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/server"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/topology"
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

	configOptions := config.Options{Service: "periscope-ingest", Logger: logger}
	cfg, err := config.Load[appconfig.PeriscopeIngest](configOptions)
	if err != nil {
		logger.WithError(err).Fatal("Invalid configuration")
	}
	cfg.ApplyLogLevel(logger)

	dbConfig := database.DefaultConfig()
	dbConfig.ServiceName = "periscope-ingest"
	dbConfig.URL = cfg.DatabaseURL
	postgres := database.MustConnect(dbConfig, logger)
	defer func() { _ = postgres.Close() }()

	// Connect to ClickHouse
	chConfig := database.DefaultClickHouseConfig()
	chConfig.ServiceName = "periscope-ingest"
	chConfig.Addr = cfg.Addr
	chConfig.Database = cfg.Database
	chConfig.Username = cfg.User
	chConfig.Password = cfg.Password
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
		LedgerLeader:            metricsCollector.NewGauge("ledger_rebuild_leader", "Result of this Periscope ingest replica's latest ledger-rebuilder lease election", []string{"ledger"}),
		LedgerCursorLag:         metricsCollector.NewGauge("ledger_rebuild_cursor_lag_seconds", "Wall-clock age of each ledger rebuild cursor", []string{"ledger"}),
		DomainEvents:            metricsCollector.NewCounter("domain_events_total", "domain.events records by type and outcome (processed, unknown_type, invalid, error)", []string{"event_type", "status"}),
	}

	// Create Kafka metrics
	metrics.KafkaMessages, metrics.KafkaDuration, metrics.KafkaLag = metricsCollector.CreateKafkaMetrics()

	// Initialize handlers
	analyticsHandler := handlers.NewAnalyticsHandler(clickhouse, logger, metrics)
	eventHandler := kafka.NewAnalyticsEventHandler(analyticsHandler.HandleAnalyticsEvent, logger)

	// We'll add health checks after we have the consumer client

	// Setup Kafka consumer
	analyticsTopic := cfg.AnalyticsTopic
	serviceEventsTopic := cfg.ServiceEventsTopic
	dlqTopic := cfg.DLQTopic

	consumer, err := kafka.NewConsumer(cfg.KafkaBrokers, cfg.KafkaGroupID, cfg.KafkaClusterID, cfg.KafkaClientID, logger,
		kafka.WithLagTracker(kafka.LagTrackerConfig{Gauge: metrics.KafkaLag}),
	)
	if err != nil {
		logger.WithError(err).Fatal("Failed to create Kafka consumer")
	}

	var dlqProducer *kafka.KafkaProducer
	dlqProducer, err = kafka.NewKafkaProducer(cfg.KafkaBrokers, dlqTopic, cfg.KafkaClusterID, logger)
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

				headers := dlqHeaders(consumerName, msg)
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
	// Decklog republishes the original MistTrigger envelope for final and
	// accounting triggers to the raw journal topic; HandleRawMistTriggerMessage
	// logs and drops poison protobuf payloads itself. domain.events has one
	// canonical name and no setting.
	subscriptions := ingestSubscriptions(
		ingestTopics{
			analytics:     analyticsTopic,
			serviceEvents: serviceEventsTopic,
			domainEvents:  topology.TopicDomainEvents,
			rawTriggers:   cfg.RawTriggersTopic,
		},
		ingestHandlers{
			analytics:     eventHandler.HandleMessage,
			serviceEvents: serviceHandler,
			domainEvents:  analyticsHandler.HandleDomainEventMessage,
			rawTriggers:   analyticsHandler.HandleRawMistTriggerMessage,
		},
		wrapWithDLQ,
		wrapRetryOnly,
	)
	// MIRROR_REGION_PREFIXES lists the MirrorMaker2 source aliases whose
	// prefixed topic copies land in this aggregator Kafka cluster.
	subscribedTopics := registerIngestSubscriptions(consumer, subscriptions, cfg.MirrorRegionPrefixes)
	logger.WithField("topics", subscribedTopics).Info("Subscribed to Kafka topics")

	// Now add health checks with all dependencies
	healthChecker.AddCheck("clickhouse", monitoring.ClickHouseNativeHealthCheck(clickhouse))
	healthChecker.AddCheck("postgres", monitoring.DatabaseHealthCheck(postgres))
	healthChecker.AddCheck("kafka", monitoring.KafkaConsumerHealthCheck(consumer.GetClient()))
	if dlqProducer != nil {
		healthChecker.AddCheck("kafka_dlq_producer", monitoring.KafkaProducerHealthCheck(dlqProducer.GetClient()))
	}
	healthChecker.AddCheck("config", monitoring.ConfigurationHealthCheck(map[string]string{
		"DATABASE_URL":               cfg.DatabaseURL,
		"CLICKHOUSE_ADDR":            strings.Join(cfg.Addr, ","),
		"KAFKA_BROKERS":              strings.Join(cfg.KafkaBrokers, ","),
		"KAFKA_GROUP_ID":             cfg.KafkaGroupID,
		"SERVICE_EVENTS_KAFKA_TOPIC": serviceEventsTopic,
		"DECKLOG_DLQ_KAFKA_TOPIC":    dlqTopic,
	}))

	// Every consumed event is written to ClickHouse, so an instance that
	// cannot reach it cannot make progress.
	readiness := monitoring.NewReadinessChecker("periscope-ingest", version.Version)
	readiness.AddCheck("clickhouse", monitoring.ClickHouseNativeHealthCheck(clickhouse))

	// runCtx ends the process; consumerCtx stops consumption and the ledger
	// rebuilders. They are separate so a signal drains the HTTP listener
	// before the consumer stops, and a consumer failure ends the process.
	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	consumerCtx, consumerCancel := context.WithCancel(context.Background())
	defer consumerCancel()

	// Start consuming
	consumerDone := make(chan struct{})
	var consumerErr error
	var consumerFailed atomic.Bool
	go func() {
		consumerErr = consumer.Start(consumerCtx)
		if consumerErr != nil && !errors.Is(consumerErr, context.Canceled) {
			consumerFailed.Store(true)
		}
		close(consumerDone)
		if consumerFailed.Load() {
			runCancel()
		}
	}()

	// Start the canonical 5-min ledger rebuilders. Each runs on its own
	// goroutine at LedgerRebuildInterval and projects the trailing window
	// from its source table into the append-only ledger. See
	// docs/architecture/meter-contracts.md.
	ledgerScheduler := handlers.NewLedgerScheduler(analyticsHandler, handlers.NewPostgresLedgerLease(postgres))
	ledgerScheduler.Start(consumerCtx)
	logger.Info("Started 5-minute ledger rebuilders")

	// Health, readiness, and metrics are served over HTTP; events arrive from Kafka.
	router := server.NewServiceRouter(server.RouterSpec{
		Service:            "periscope-ingest",
		Logger:             logger,
		Health:             healthChecker,
		Ready:              readiness,
		Metrics:            metricsCollector,
		Runtime:            cfg.HTTPRuntime,
		DebugToken:         cfg.ServiceToken,
		DebugConfig:        func() any { return cfg },
		DebugConfigOptions: configOptions,
	})

	logger.Info("Periscope-Ingest started - consuming analytics events from Kafka")

	// Best-effort service registration in Quartermaster (using gRPC)
	go registerWithQuartermaster(runCtx, cfg, logger)

	// Consumers stop after the HTTP listener has drained: cancel consumption,
	// close the consumer client, wait briefly for Start to return, then close
	// the DLQ producer. ClickHouse and PostgreSQL close last, when main returns.
	shutdownConsumers := func(context.Context) {
		consumerCancel()
		closeWithTimeout(logger, "Kafka consumer", 10*time.Second, consumer.Close)
		select {
		case <-consumerDone:
			if consumerErr != nil && !errors.Is(consumerErr, context.Canceled) && !consumerFailed.Load() {
				logger.WithError(consumerErr).Error("Kafka consumer error")
			}
		case <-time.After(2 * time.Second):
			logger.Warn("Kafka consumer did not report shutdown before timeout")
		}
		if dlqProducer != nil {
			closeWithTimeout(logger, "DLQ Kafka producer", 10*time.Second, dlqProducer.Close)
		}
	}

	server.RegisterEnvFileReload("periscope-ingest", logger)
	runErr := server.Run(runCtx, server.RunSpec{
		Service:    "periscope-ingest",
		Logger:     logger,
		Ready:      readiness,
		HTTP:       []server.HTTPListener{{Name: "http", Port: cfg.Port, Handler: router}},
		OnShutdown: []func(context.Context){shutdownConsumers},
	})
	if consumerFailed.Load() {
		<-consumerDone
		logger.WithError(consumerErr).Fatal("Kafka consumer exited")
	}
	if runErr != nil {
		logger.WithError(runErr).Fatal("Server exited with error")
	}

	logger.Info("Periscope-Ingest stopped")
}

// registerWithQuartermaster registers the HTTP port with the /health endpoint
// from servicedefs.
func registerWithQuartermaster(ctx context.Context, cfg *appconfig.PeriscopeIngest, logger logging.Logger) {
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
		ServiceType:   "periscope-ingest",
		Port:          cfg.Port,
		AdvertiseHost: cfg.AdvertiseHost,
		ClusterID:     cfg.ClusterID,
		NodeID:        cfg.NodeID,
	})
	if err != nil {
		logger.WithError(err).Warn("Quartermaster bootstrap skipped")
		return
	}
	if _, err := qmbootstrap.BootstrapServiceWithRetry(ctx, qc, req, logger, qmbootstrap.DefaultRetryConfig("periscope-ingest")); err != nil {
		logger.WithError(err).Warn("Quartermaster bootstrap (periscope-ingest) failed")
	} else {
		logger.Info("Quartermaster bootstrap (periscope-ingest) ok")
	}
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
