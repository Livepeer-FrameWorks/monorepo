package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"frameworks/api_realtime/internal/appconfig"
	signalmangrpc "frameworks/api_realtime/internal/grpc"
	"frameworks/api_realtime/internal/metrics"
	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/grpcutil"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/monitoring"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	signalmanpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/signalman"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/qmbootstrap"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/server"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/serviceevents"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/topology"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/version"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
	"google.golang.org/protobuf/encoding/protojson"
)

func main() {
	if version.HandleCLI() {
		return
	}

	// Setup logger
	logger := logging.NewLoggerWithService("signalman")

	// Load environment variables
	config.LoadEnv(logger)

	logger.Info("Starting Signalman (Real-time Event Hub)")

	configOptions := config.Options{Service: "signalman", Logger: logger}
	cfg, err := config.Load[appconfig.Signalman](configOptions)
	if err != nil {
		logger.WithError(err).Fatal("Invalid configuration")
	}
	cfg.ApplyLogLevel(logger)

	// Setup monitoring
	healthChecker := monitoring.NewHealthChecker("signalman", version.Version)
	metricsCollector := monitoring.NewMetricsCollector("signalman", version.Version, version.GitCommit)

	// Create custom metrics
	serviceMetrics := &metrics.Metrics{
		HubConnections:     metricsCollector.NewGauge("grpc_hub_connections_active", "Active gRPC hub connections", []string{"channel"}),
		HubMessages:        metricsCollector.NewCounter("grpc_hub_messages_total", "gRPC hub messages", []string{"channel", "direction"}),
		EventsPublished:    metricsCollector.NewCounter("realtime_events_published_total", "Real-time events published", []string{"event_type", "channel"}),
		MessageDeliveryLag: metricsCollector.NewHistogram("message_delivery_lag_seconds", "Message delivery latency", []string{"channel", "type"}, nil),
	}

	// Create Kafka metrics
	serviceMetrics.KafkaMessages, serviceMetrics.KafkaDuration, serviceMetrics.KafkaLag = metricsCollector.CreateKafkaMetrics()
	serviceMetrics.KafkaDuplicateEvents = metricsCollector.NewCounter("kafka_duplicate_events_total", "Kafka events dropped because their event ID was already broadcast", []string{"topic"})
	serviceMetrics.DomainEventsDropped = metricsCollector.NewCounter("domain_events_dropped_total", "domain.events records withheld from tenant subscribers", []string{"reason"})

	// Initialize gRPC server
	signalmanServer := signalmangrpc.NewSignalmanServer(logger, serviceMetrics)
	grpcHub := signalmanServer.GetHub()
	if cfg.MaxConnectionsPerTenant > 0 {
		grpcHub.SetMaxConnectionsPerTenant(cfg.MaxConnectionsPerTenant)
	}

	// Setup Kafka consumer. Signalman runs N replicas per region with one
	// consumer group per replica (set by the provisioner as
	// KAFKA_GROUP_ID=signalman-{host}) so every replica receives every event
	// for broadcast fanout. A fresh group has no committed offsets, so reset
	// must be `latest` to avoid replaying retained history to live clients.
	analyticsTopic := cfg.AnalyticsTopic
	serviceEventsTopic := cfg.ServiceEventsTopic
	dlqTopic := cfg.DLQTopic

	consumerOpts := []kafka.ConsumerOption{
		kafka.WithLagTracker(kafka.LagTrackerConfig{Gauge: serviceMetrics.KafkaLag}),
	}
	if cfg.ResetOffsetLatest() {
		consumerOpts = append(consumerOpts, kafka.WithResetOffsetLatest())
	}

	consumer, err := kafka.NewConsumer(cfg.KafkaBrokers, cfg.KafkaGroupID, cfg.KafkaClusterID, cfg.KafkaClientID, logger, consumerOpts...)
	if err != nil {
		logger.WithError(err).Fatal("Failed to initialize Kafka consumer")
	}

	var dlqProducer *kafka.KafkaProducer
	dlqProducer, err = kafka.NewKafkaProducer(cfg.KafkaBrokers, dlqTopic, cfg.KafkaClusterID, logger)
	if err != nil {
		logger.WithError(err).Warn("Failed to create DLQ Kafka producer (DLQ disabled)")
		dlqProducer = nil
	}

	wrapWithDLQ := func(consumerName string, handler func(context.Context, kafka.Message) error) func(context.Context, kafka.Message) error {
		return func(ctx context.Context, msg kafka.Message) error {
			start := time.Now()
			err := handler(ctx, msg)
			if serviceMetrics.KafkaDuration != nil {
				serviceMetrics.KafkaDuration.WithLabelValues("consume").Observe(time.Since(start).Seconds())
			}
			if serviceMetrics.KafkaMessages != nil {
				status := "ok"
				if err != nil {
					status = "error"
				}
				serviceMetrics.KafkaMessages.WithLabelValues(msg.Topic, "consume", status).Inc()
			}
			if err != nil {
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
				} else if eventType, ok := msg.Headers[events.HeaderType]; ok {
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

	// Local topics and MirrorMaker2 copies from other regions can deliver the
	// same event more than once; broadcast each event ID once per window.
	recentEvents := newEventIDWindow(signalmanEventDedupCapacity, signalmanEventDedupTTL)

	// Register Kafka message handler that routes analytics events to gRPC hub
	analyticsHandler := func(ctx context.Context, msg kafka.Message) error {
		var event kafka.AnalyticsEvent
		if err := json.Unmarshal(msg.Value, &event); err != nil {
			logger.WithError(err).Error("Failed to unmarshal analytics event")
			return fmt.Errorf("unmarshal analytics event: %w", err)
		}
		// Map headers
		for k, v := range msg.Headers {
			if k == "source" && event.Source == "" {
				event.Source = v
			}
			if k == "tenant_id" && event.TenantID == "" {
				event.TenantID = v
			}
			if k == "event_id" && event.EventID == "" {
				event.EventID = v
			}
		}
		if recentEvents.seen(event.EventID) {
			serviceMetrics.RecordDuplicateEvent(msg.Topic)
			return nil
		}

		if event.EventType == "client_lifecycle_batch" {
			if event.TenantID == "" {
				logger.WithFields(logging.Fields{
					"event_type": event.EventType,
					"channel":    signalmanpb.Channel_CHANNEL_ANALYTICS,
				}).Warn("Dropping event without tenant_id for non-system channel")
				return nil
			}
			for _, data := range clientLifecycleBatchToProtoData(event.Data, logger) {
				grpcHub.BroadcastToTenant(event.TenantID, signalmanpb.EventType_EVENT_TYPE_CLIENT_LIFECYCLE_UPDATE, signalmanpb.Channel_CHANNEL_ANALYTICS, data)
			}
			return nil
		}

		channel := mapEventTypeToChannel(event.EventType)
		eventType := mapEventTypeToProto(event.EventType)
		routeEvent(grpcHub, event.EventType, event.TenantID, channel, eventType, eventToProtoData(event.Data, logger), logger)
		return nil
	}

	// Register Kafka message handler for service events
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
			if k == "event_id" && event.EventID == "" {
				event.EventID = v
			}
		}
		if recentEvents.seen(event.EventID) {
			serviceMetrics.RecordDuplicateEvent(msg.Topic)
			return nil
		}

		// Service-plane events that should never hit real-time channels.
		if event.EventType == "api_request_batch" {
			return nil
		}

		channel := mapEventTypeToChannel(event.EventType)
		eventType := mapEventTypeToProto(event.EventType)
		if eventType == signalmanpb.EventType_EVENT_TYPE_UNSPECIFIED {
			return nil
		}

		data := serviceEventToProtoData(event, logger)
		if data == nil {
			return fmt.Errorf("service event payload missing required data")
		}

		routeEvent(grpcHub, event.EventType, event.TenantID, channel, eventType, data, logger)
		return nil
	}

	consumer.AddHandler(analyticsTopic, wrapWithDLQ("signalman-analytics", analyticsHandler))
	consumer.AddHandler(serviceEventsTopic, wrapWithDLQ("signalman-service", serviceHandler))

	// MIRROR_REGION_PREFIXES lists the MirrorMaker2 source aliases whose realtime
	// topic copies land in this region's Kafka cluster, so subscribers attached
	// here also receive events produced in other regions.
	for _, prefix := range cfg.MirrorRegionPrefixes {
		consumer.AddHandler(topology.MirroredTopicName(prefix, analyticsTopic), wrapWithDLQ("signalman-analytics-mirror:"+prefix, analyticsHandler))
		consumer.AddHandler(topology.MirroredTopicName(prefix, serviceEventsTopic), wrapWithDLQ("signalman-service-mirror:"+prefix, serviceHandler))
	}

	// Public domain events go to their tenant's CHANNEL_EVENTS subscribers.
	domainHandler := newDomainEventHandler(grpcHub, newEventIDWindow(signalmanEventDedupCapacity, signalmanEventDedupTTL), serviceMetrics, logger)
	registerDomainEventSubscriptions(consumer, cfg.MirrorRegionPrefixes, domainHandler, func(name string, handler kafka.Handler) kafka.Handler {
		return wrapWithDLQ(name, handler)
	})

	// Add health checks
	healthChecker.AddCheck("kafka", monitoring.KafkaConsumerHealthCheck(consumer.GetClient()))
	if dlqProducer != nil {
		healthChecker.AddCheck("kafka_dlq_producer", monitoring.KafkaProducerHealthCheck(dlqProducer.GetClient()))
	}
	healthChecker.AddCheck("config", monitoring.ConfigurationHealthCheck(map[string]string{
		"KAFKA_BROKERS":           strings.Join(cfg.KafkaBrokers, ","),
		"KAFKA_TOPICS":            strings.Join([]string{analyticsTopic, serviceEventsTopic, topology.TopicDomainEvents}, ","),
		"DECKLOG_DLQ_KAFKA_TOPIC": dlqTopic,
	}))

	// Readiness carries no dependency checks: a replica without Kafka still
	// accepts Subscribe streams. It reports draining during shutdown.
	readiness := monitoring.NewReadinessChecker("signalman", version.Version)

	// HTTP serves health, readiness, and metrics only; realtime traffic is the
	// gRPC Subscribe stream.
	router := server.NewServiceRouter(server.RouterSpec{
		Service:            "signalman",
		Logger:             logger,
		Health:             healthChecker,
		Ready:              readiness,
		Metrics:            metricsCollector,
		Runtime:            cfg.HTTPRuntime,
		DebugToken:         cfg.ServiceToken,
		DebugConfig:        func() any { return cfg },
		DebugConfigOptions: configOptions,
	})

	// runCtx ends the process; consumerCtx stops the Kafka consumer. A
	// consumer failure cancels runCtx so the listeners drain before exit.
	runCtx, runCancel := context.WithCancel(context.Background())
	defer runCancel()
	consumerCtx, consumerCancel := context.WithCancel(context.Background())
	defer consumerCancel()

	consumerFailure := make(chan error, 1)
	go func() {
		if err := consumer.Start(consumerCtx); err != nil && !errors.Is(err, context.Canceled) {
			consumerFailure <- err
			runCancel()
		}
	}()

	// Best-effort service registration in Quartermaster (using gRPC)
	go registerWithQuartermaster(runCtx, cfg, logger)

	server.RegisterEnvFileReload("signalman", logger)
	runErr := server.Run(runCtx, server.RunSpec{
		Service: "signalman",
		Logger:  logger,
		Ready:   readiness,
		HTTP:    []server.HTTPListener{{Name: "http", Port: cfg.HTTPListen.Port, Handler: router}},
		// The gRPC server is built after the port is bound, so HTTP health
		// serves while the TLS files are still being synced.
		GRPC: []server.GRPCListener{{Name: "grpc", Port: cfg.GRPCListen.Port, Build: func(ctx context.Context) (*grpc.Server, error) {
			return newGRPCServer(ctx, cfg, logger, signalmanServer)
		}}},
		OnDrain: []func(context.Context){closeStreamsHook(signalmanServer)},
		OnShutdown: []func(context.Context){func(context.Context) {
			consumerCancel()
			if dlqProducer != nil {
				_ = dlqProducer.Close()
			}
			_ = consumer.Close()
		}},
	})
	select {
	case consumerErr := <-consumerFailure:
		logger.WithError(consumerErr).Fatal("Kafka consumer exited")
	default:
	}
	if runErr != nil {
		logger.WithError(runErr).Fatal("Server exited with error")
	}
}

// streamCloser ends every open Subscribe stream.
type streamCloser interface {
	Shutdown()
}

// closeStreamsHook returns the drain hook that ends every Subscribe stream
// once the instance starts draining. A Subscribe stream stays open until the
// client leaves, so without it the gRPC graceful stop would wait out the whole
// shutdown timeout; clients instead receive Unavailable and reconnect to
// another replica.
func closeStreamsHook(streams streamCloser) func(context.Context) {
	return func(context.Context) {
		streams.Shutdown()
	}
}

// newGRPCServer builds the Signalman gRPC server. It waits up to two minutes
// for the TLS files, and stops waiting when ctx ends.
func newGRPCServer(ctx context.Context, cfg *appconfig.Signalman, logger logging.Logger, signalmanServer *signalmangrpc.SignalmanServer) (*grpc.Server, error) {
	metadataPolicy, policyErr := middleware.ParseMetadataPolicy(cfg.MetadataPolicy)
	if policyErr != nil {
		return nil, policyErr
	}
	authConfig := middleware.GRPCAuthConfig{
		ServiceToken:   cfg.ServiceToken,
		JWTSecret:      []byte(cfg.JWTSecret),
		MetadataPolicy: metadataPolicy,
		Logger:         logger,
		SkipMethods: []string{
			"/grpc.health.v1.Health/Check",
			"/grpc.health.v1.Health/Watch",
		},
	}

	serverOpts := []grpc.ServerOption{
		grpc.ChainUnaryInterceptor(
			grpcutil.SanitizeUnaryServerInterceptor(),
			middleware.GRPCAuthInterceptor(authConfig),
		),
		grpc.ChainStreamInterceptor(middleware.GRPCStreamAuthInterceptor(authConfig)),
	}
	tlsCfg := grpcutil.ServerTLSConfig{
		CertFile:      cfg.CertPath,
		KeyFile:       cfg.KeyPath,
		AllowInsecure: cfg.AllowInsecure,
	}
	waitCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := grpcutil.WaitForServerTLSFiles(waitCtx, tlsCfg, logger); err != nil {
		return nil, fmt.Errorf("wait for Signalman gRPC TLS files: %w", err)
	}
	tlsOpt, err := grpcutil.ServerTLS(tlsCfg, logger)
	if err != nil {
		return nil, fmt.Errorf("configure Signalman gRPC TLS: %w", err)
	}
	if tlsOpt != nil {
		serverOpts = append(serverOpts, tlsOpt)
	}
	grpcSrv := grpc.NewServer(serverOpts...)
	signalmanpb.RegisterSignalmanServiceServer(grpcSrv, signalmanServer)

	// gRPC health service so Quartermaster's gRPC probe passes
	hs := health.NewServer()
	hs.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
	hs.SetServingStatus(signalmanpb.SignalmanService_ServiceDesc.ServiceName, grpc_health_v1.HealthCheckResponse_SERVING)
	server.RegisterHealthServer(grpcSrv, hs)
	reflection.Register(grpcSrv)
	return grpcSrv, nil
}

// registerWithQuartermaster registers the gRPC port with protocol grpc and no
// health endpoint.
func registerWithQuartermaster(ctx context.Context, cfg *appconfig.Signalman, logger logging.Logger) {
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
		ServiceType:        "signalman",
		Protocol:           "grpc",
		Port:               cfg.GRPCListen.Port,
		AdvertiseHost:      cfg.AdvertiseHost,
		ClusterID:          cfg.ClusterID,
		NodeID:             cfg.NodeID,
		OmitHealthEndpoint: true,
	})
	if err != nil {
		logger.WithError(err).Warn("Quartermaster bootstrap skipped")
		return
	}
	if _, err := qmbootstrap.BootstrapServiceWithRetry(ctx, qc, req, logger, qmbootstrap.DefaultRetryConfig("signalman")); err != nil {
		logger.WithError(err).Warn("Quartermaster bootstrap (signalman) failed")
	} else {
		logger.Info("Quartermaster bootstrap (signalman) ok")
	}
}

// eventHub is the part of the gRPC hub that delivers routed events.
type eventHub interface {
	BroadcastToTenant(tenantID string, eventType signalmanpb.EventType, channel signalmanpb.Channel, data *signalmanpb.EventData)
	BroadcastInfrastructure(eventType signalmanpb.EventType, data *signalmanpb.EventData)
	BroadcastPlatform(eventType signalmanpb.EventType, data *signalmanpb.EventData)
}

// routeEvent delivers one consumed event to its audience. Platform channel
// events go to the operator audience whether or not they name a tenant; a
// tenantless event is otherwise delivered only on the system channel, and
// dropped on every other channel. A tenantless incident change on the system
// channel is dropped too: a system broadcast reaches every tenant subscriber.
func routeEvent(hub eventHub, rawType, tenantID string, channel signalmanpb.Channel, eventType signalmanpb.EventType, data *signalmanpb.EventData, logger logging.Logger) {
	switch {
	case channel == signalmanpb.Channel_CHANNEL_PLATFORM:
		hub.BroadcastPlatform(eventType, data)
	case tenantID != "":
		hub.BroadcastToTenant(tenantID, eventType, channel, data)
	case channel == signalmanpb.Channel_CHANNEL_SYSTEM && eventType != signalmanpb.EventType_EVENT_TYPE_INCIDENT_UPDATED:
		hub.BroadcastInfrastructure(eventType, data)
	default:
		logger.WithFields(logging.Fields{
			"event_type": rawType,
			"channel":    channel,
		}).Warn("Dropping event without tenant_id that has no tenantless audience")
	}
}

// mapEventTypeToChannel maps Kafka event types to gRPC channels
func mapEventTypeToChannel(eventType string) signalmanpb.Channel {
	switch eventType {
	case serviceevents.PlatformIncidentUpdated:
		return signalmanpb.Channel_CHANNEL_PLATFORM
	case "stream_lifecycle_update", "stream_track_list", "stream_buffer", "stream_end", "stream_source", "play_rewrite", "push_rewrite",
		"stream_created", "stream_updated", "stream_deleted":
		return signalmanpb.Channel_CHANNEL_STREAMS
	case "node_lifecycle_update", "load_balancing", "incident_updated":
		return signalmanpb.Channel_CHANNEL_SYSTEM
	case "storage_lifecycle", "storage_snapshot", "process_billing":
		return signalmanpb.Channel_CHANNEL_ANALYTICS
	case "message_lifecycle", "message_received", "message_updated", "conversation_created", "conversation_updated":
		return signalmanpb.Channel_CHANNEL_MESSAGING
	case "skipper_investigation":
		return signalmanpb.Channel_CHANNEL_AI
	default:
		return signalmanpb.Channel_CHANNEL_ANALYTICS
	}
}

// mapEventTypeToProto maps Kafka event type strings to proto EventType
func mapEventTypeToProto(eventType string) signalmanpb.EventType {
	switch eventType {
	// Stream events
	case "stream_lifecycle_update":
		return signalmanpb.EventType_EVENT_TYPE_STREAM_LIFECYCLE_UPDATE
	case "stream_created", "stream_updated", "stream_deleted":
		return signalmanpb.EventType_EVENT_TYPE_STREAM_LIFECYCLE_UPDATE
	case "stream_track_list":
		return signalmanpb.EventType_EVENT_TYPE_STREAM_TRACK_LIST
	case "stream_buffer":
		return signalmanpb.EventType_EVENT_TYPE_STREAM_BUFFER
	case "stream_end":
		return signalmanpb.EventType_EVENT_TYPE_STREAM_END
	case "stream_source":
		return signalmanpb.EventType_EVENT_TYPE_STREAM_SOURCE
	case "play_rewrite":
		return signalmanpb.EventType_EVENT_TYPE_PLAY_REWRITE
	// System events
	case "node_lifecycle_update":
		return signalmanpb.EventType_EVENT_TYPE_NODE_LIFECYCLE_UPDATE
	case "load_balancing":
		return signalmanpb.EventType_EVENT_TYPE_LOAD_BALANCING
	case "incident_updated", serviceevents.PlatformIncidentUpdated:
		return signalmanpb.EventType_EVENT_TYPE_INCIDENT_UPDATED
	// Analytics events
	case "viewer_connect":
		return signalmanpb.EventType_EVENT_TYPE_VIEWER_CONNECT
	case "viewer_disconnect":
		return signalmanpb.EventType_EVENT_TYPE_VIEWER_DISCONNECT
	case "client_lifecycle_update", "client_lifecycle_batch":
		return signalmanpb.EventType_EVENT_TYPE_CLIENT_LIFECYCLE_UPDATE
	case "clip_lifecycle":
		return signalmanpb.EventType_EVENT_TYPE_CLIP_LIFECYCLE
	case "dvr_lifecycle":
		return signalmanpb.EventType_EVENT_TYPE_DVR_LIFECYCLE
	case "vod_lifecycle":
		return signalmanpb.EventType_EVENT_TYPE_VOD_LIFECYCLE
	case "push_rewrite":
		return signalmanpb.EventType_EVENT_TYPE_PUSH_REWRITE
	case "push_out_start":
		return signalmanpb.EventType_EVENT_TYPE_PUSH_OUT_START
	case "push_end":
		return signalmanpb.EventType_EVENT_TYPE_PUSH_END
	case "recording_complete":
		return signalmanpb.EventType_EVENT_TYPE_RECORDING_COMPLETE
	case "storage_lifecycle":
		return signalmanpb.EventType_EVENT_TYPE_STORAGE_LIFECYCLE
	case "process_billing":
		return signalmanpb.EventType_EVENT_TYPE_PROCESS_BILLING
	case "storage_snapshot":
		return signalmanpb.EventType_EVENT_TYPE_STORAGE_SNAPSHOT
	// Messaging events
	case "message_lifecycle", "message_received", "message_updated", "conversation_created", "conversation_updated":
		return signalmanpb.EventType_EVENT_TYPE_MESSAGE_LIFECYCLE
	// AI events
	case "skipper_investigation":
		return signalmanpb.EventType_EVENT_TYPE_SKIPPER_INVESTIGATION
	default:
		return signalmanpb.EventType_EVENT_TYPE_UNSPECIFIED
	}
}

// eventToProtoData converts Kafka event data to proto EventData
// Data comes as a MistTrigger envelope from Kafka
func eventToProtoData(data map[string]interface{}, logger logging.Logger) *signalmanpb.EventData {
	mt, ok := mistTriggerFromEventData(data, logger)
	if !ok {
		return &signalmanpb.EventData{}
	}
	eventData := mistEnvelopeToProtoData(mt)

	// Extract typed payload from MistTrigger oneof
	switch p := mt.GetTriggerPayload().(type) {
	case *ipcpb.MistTrigger_ClientLifecycleUpdate:
		eventData.Payload = &signalmanpb.EventData_ClientLifecycle{ClientLifecycle: p.ClientLifecycleUpdate}
	case *ipcpb.MistTrigger_NodeLifecycleUpdate:
		eventData.Payload = &signalmanpb.EventData_NodeLifecycle{NodeLifecycle: p.NodeLifecycleUpdate}
	case *ipcpb.MistTrigger_TrackList:
		eventData.Payload = &signalmanpb.EventData_TrackList{TrackList: p.TrackList}
	case *ipcpb.MistTrigger_ClipLifecycleData:
		eventData.Payload = &signalmanpb.EventData_ClipLifecycle{ClipLifecycle: p.ClipLifecycleData}
	case *ipcpb.MistTrigger_DvrLifecycleData:
		eventData.Payload = &signalmanpb.EventData_DvrLifecycle{DvrLifecycle: p.DvrLifecycleData}
	case *ipcpb.MistTrigger_VodLifecycleData:
		eventData.Payload = &signalmanpb.EventData_VodLifecycle{VodLifecycle: p.VodLifecycleData}
	case *ipcpb.MistTrigger_LoadBalancingData:
		eventData.Payload = &signalmanpb.EventData_LoadBalancing{LoadBalancing: p.LoadBalancingData}
	case *ipcpb.MistTrigger_PushRewrite:
		eventData.Payload = &signalmanpb.EventData_PushRewrite{PushRewrite: p.PushRewrite}
	case *ipcpb.MistTrigger_PushOutStart:
		eventData.Payload = &signalmanpb.EventData_PushOutStart{PushOutStart: p.PushOutStart}
	case *ipcpb.MistTrigger_PushEnd:
		eventData.Payload = &signalmanpb.EventData_PushEnd{PushEnd: p.PushEnd}
	case *ipcpb.MistTrigger_ViewerConnect:
		eventData.Payload = &signalmanpb.EventData_ViewerConnect{ViewerConnect: p.ViewerConnect}
	case *ipcpb.MistTrigger_ViewerDisconnect:
		eventData.Payload = &signalmanpb.EventData_ViewerDisconnect{ViewerDisconnect: p.ViewerDisconnect}
	case *ipcpb.MistTrigger_StreamEnd:
		eventData.Payload = &signalmanpb.EventData_StreamEnd{StreamEnd: p.StreamEnd}
	case *ipcpb.MistTrigger_RecordingComplete:
		eventData.Payload = &signalmanpb.EventData_Recording{Recording: p.RecordingComplete}
	case *ipcpb.MistTrigger_StreamLifecycleUpdate:
		eventData.Payload = &signalmanpb.EventData_StreamLifecycle{StreamLifecycle: p.StreamLifecycleUpdate}
	case *ipcpb.MistTrigger_StreamBuffer:
		eventData.Payload = &signalmanpb.EventData_StreamBuffer{StreamBuffer: p.StreamBuffer}
	case *ipcpb.MistTrigger_StorageLifecycleData:
		eventData.Payload = &signalmanpb.EventData_StorageLifecycle{StorageLifecycle: p.StorageLifecycleData}
	case *ipcpb.MistTrigger_ProcessBilling:
		eventData.Payload = &signalmanpb.EventData_ProcessBilling{ProcessBilling: p.ProcessBilling}
	case *ipcpb.MistTrigger_PlayRewrite:
		eventData.Payload = &signalmanpb.EventData_PlayRewrite{PlayRewrite: p.PlayRewrite}
	case *ipcpb.MistTrigger_StreamSource:
		eventData.Payload = &signalmanpb.EventData_StreamSource{StreamSource: p.StreamSource}
	case *ipcpb.MistTrigger_StorageSnapshot:
		eventData.Payload = &signalmanpb.EventData_StorageSnapshot{StorageSnapshot: p.StorageSnapshot}
	case *ipcpb.MistTrigger_MessageLifecycleData:
		eventData.Payload = &signalmanpb.EventData_MessageLifecycle{MessageLifecycle: p.MessageLifecycleData}
	}

	return eventData
}

func mistEnvelopeToProtoData(mt *ipcpb.MistTrigger) *signalmanpb.EventData {
	eventData := &signalmanpb.EventData{}
	if mt == nil {
		return eventData
	}
	eventData.SourceRegion = mt.GetSourceRegion()
	eventData.SourceClusterId = mt.GetClusterId()
	eventData.StreamOriginRegion = mt.GetStreamOriginRegion()
	eventData.StreamOriginClusterId = mt.GetOriginClusterId()
	eventData.SchemaVersion = mt.GetSchemaVersion()
	eventData.ControlCellId = mt.GetControlCellId()

	return eventData
}

func clientLifecycleBatchToProtoData(data map[string]interface{}, logger logging.Logger) []*signalmanpb.EventData {
	mt, ok := mistTriggerFromEventData(data, logger)
	if !ok {
		return nil
	}
	batch := mt.GetClientLifecycleBatch()
	if batch == nil {
		return nil
	}
	out := make([]*signalmanpb.EventData, 0, len(batch.GetSamples()))
	for _, sample := range batch.GetSamples() {
		if sample == nil {
			continue
		}
		eventData := mistEnvelopeToProtoData(mt)
		eventData.Payload = &signalmanpb.EventData_ClientLifecycle{ClientLifecycle: sample}
		out = append(out, eventData)
	}
	return out
}

func mistTriggerFromEventData(data map[string]interface{}, logger logging.Logger) (*ipcpb.MistTrigger, bool) {
	if data == nil {
		return nil, false
	}

	b, err := json.Marshal(data)
	if err != nil {
		logger.WithError(err).Debug("Failed to marshal event data")
		return nil, false
	}

	var mt ipcpb.MistTrigger
	if err := protojson.Unmarshal(b, &mt); err != nil {
		logger.WithError(err).Debug("Failed to unmarshal MistTrigger from event data")
		return nil, false
	}

	return &mt, true
}

func serviceEventToProtoData(event kafka.ServiceEvent, logger logging.Logger) *signalmanpb.EventData {
	eventData := &signalmanpb.EventData{
		SourceRegion:          event.SourceRegion,
		SourceClusterId:       event.SourceClusterID,
		StreamOriginRegion:    event.StreamOriginRegion,
		StreamOriginClusterId: event.StreamOriginClusterID,
		SchemaVersion:         event.SchemaVersion,
	}
	switch event.EventType {
	case "stream_created", "stream_updated", "stream_deleted":
		streamID := getString(event.Data, "stream_id")
		if streamID == "" {
			return nil
		}
		eventData.Payload = &signalmanpb.EventData_StreamChange{StreamChange: &ipcpb.StreamChangeEvent{
			StreamId: streamID, ChangedFields: getStringSlice(event.Data, "changed_fields"),
		}}
		return eventData
	case "message_received", "message_updated", "conversation_created", "conversation_updated":
		ml := &ipcpb.MessageLifecycleData{}
		switch event.EventType {
		case "message_received":
			ml.EventType = ipcpb.MessageLifecycleData_EVENT_TYPE_MESSAGE_CREATED
		case "message_updated":
			ml.EventType = ipcpb.MessageLifecycleData_EVENT_TYPE_MESSAGE_UPDATED
		case "conversation_created":
			ml.EventType = ipcpb.MessageLifecycleData_EVENT_TYPE_CONVERSATION_CREATED
		case "conversation_updated":
			ml.EventType = ipcpb.MessageLifecycleData_EVENT_TYPE_CONVERSATION_UPDATED
		}

		if event.TenantID != "" {
			tenantID := event.TenantID
			ml.TenantId = &tenantID
		}
		if event.UserID != "" {
			userID := event.UserID
			ml.UserId = &userID
		}

		if convID := getString(event.Data, "conversation_id"); convID != "" {
			ml.ConversationId = convID
		} else {
			return nil
		}

		if msgID := getString(event.Data, "message_id"); msgID != "" {
			ml.MessageId = &msgID
		}

		if sender := getString(event.Data, "sender"); sender != "" {
			ml.Sender = &sender
		}
		if status := getString(event.Data, "status"); status != "" {
			ml.Status = &status
		}

		ts := event.Timestamp
		if unix, ok := getInt64(event.Data, "timestamp"); ok {
			ts = time.Unix(unix, 0)
		}
		ml.Timestamp = ts.Unix()

		eventData.Payload = &signalmanpb.EventData_MessageLifecycle{MessageLifecycle: ml}
		return eventData
	case "skipper_investigation":
		return eventData
	case "incident_updated", serviceevents.PlatformIncidentUpdated:
		incidentID := getString(event.Data, "incident_id")
		if incidentID == "" {
			return nil
		}
		incident := &ipcpb.IncidentEvent{
			IncidentId: incidentID,
			TenantId:   getString(event.Data, "tenant_id"),
			ClusterId:  getString(event.Data, "cluster_id"),
			Status:     getString(event.Data, "status"),
			Severity:   getString(event.Data, "severity"),
			Title:      getString(event.Data, "title"),
			Change:     getString(event.Data, "change"),
		}
		if updatedAt, ok := getInt64(event.Data, "updated_at_ms"); ok {
			incident.UpdatedAtMs = updatedAt
		}
		eventData.Payload = &signalmanpb.EventData_IncidentUpdated{IncidentUpdated: incident}
		return eventData
	default:
		return nil
	}
}

func getString(data map[string]interface{}, key string) string {
	if data == nil {
		return ""
	}
	if value, ok := data[key]; ok {
		switch v := value.(type) {
		case string:
			return v
		case fmt.Stringer:
			return v.String()
		}
	}
	return ""
}

func getStringSlice(data map[string]interface{}, key string) []string {
	if data == nil {
		return nil
	}
	value, ok := data[key]
	if !ok {
		return nil
	}
	switch items := value.(type) {
	case []string:
		return append([]string(nil), items...)
	case []interface{}:
		out := make([]string, 0, len(items))
		for _, item := range items {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func getInt64(data map[string]interface{}, key string) (int64, bool) {
	if data == nil {
		return 0, false
	}
	value, ok := data[key]
	if !ok {
		return 0, false
	}
	switch v := value.(type) {
	case int64:
		return v, true
	case int32:
		return int64(v), true
	case int:
		return int64(v), true
	case float64:
		return int64(v), true
	case float32:
		return int64(v), true
	case json.Number:
		n, err := v.Int64()
		if err != nil {
			return 0, false
		}
		return n, true
	case string:
		// protojson renders int64 fields as JSON strings.
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			return 0, false
		}
		return n, true
	default:
		return 0, false
	}
}
