package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	signalmangrpc "frameworks/api_realtime/internal/grpc"
	"frameworks/api_realtime/internal/metrics"
	qmclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/config"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/grpcutil"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/monitoring"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/qmbootstrap"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/server"
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

	// Initialize gRPC server
	signalmanServer := signalmangrpc.NewSignalmanServer(logger, serviceMetrics)
	grpcHub := signalmanServer.GetHub()
	maxConnectionsPerTenant := config.GetEnvInt("SIGNALMAN_MAX_CONNECTIONS_PER_TENANT", 0)
	if maxConnectionsPerTenant > 0 {
		grpcHub.SetMaxConnectionsPerTenant(maxConnectionsPerTenant)
	}

	// Setup Kafka consumer. Signalman runs N replicas per region with one
	// consumer group per replica (set by the provisioner as
	// KAFKA_GROUP_ID=signalman-{host}) so every replica receives every event
	// for broadcast fanout. A fresh group has no committed offsets, so reset
	// must be `latest` to avoid replaying retained history to live clients.
	brokers := strings.Split(config.RequireEnv("KAFKA_BROKERS"), ",")
	groupID := config.GetEnv("KAFKA_GROUP_ID", "signalman-group")
	clusterID := config.RequireEnv("KAFKA_CLUSTER_ID")
	clientID := config.GetEnv("KAFKA_CLIENT_ID", "signalman")
	analyticsTopic := config.GetEnv("ANALYTICS_KAFKA_TOPIC", "analytics_events")
	serviceEventsTopic := config.GetEnv("SERVICE_EVENTS_KAFKA_TOPIC", "service_events")
	dlqTopic := config.GetEnv("DECKLOG_DLQ_KAFKA_TOPIC", "decklog_events_dlq")
	serviceToken := config.RequireEnv("SERVICE_TOKEN")
	jwtSecret := []byte(config.RequireEnv("JWT_SECRET"))
	quartermasterGRPCAddr := config.GetEnv("QUARTERMASTER_GRPC_ADDR", "quartermaster:19002")

	consumerOpts := []kafka.ConsumerOption{
		kafka.WithLagTracker(kafka.LagTrackerConfig{Gauge: serviceMetrics.KafkaLag}),
	}
	if strings.EqualFold(config.GetEnv("KAFKA_CONSUME_RESET_OFFSET", "latest"), "latest") {
		consumerOpts = append(consumerOpts, kafka.WithResetOffsetLatest())
	}

	consumer, err := kafka.NewConsumer(brokers, groupID, clusterID, clientID, logger, consumerOpts...)
	if err != nil {
		logger.WithError(err).Fatal("Failed to initialize Kafka consumer")
	}
	defer consumer.Close()

	var dlqProducer *kafka.KafkaProducer
	dlqProducer, err = kafka.NewKafkaProducer(brokers, dlqTopic, clusterID, logger)
	if err != nil {
		logger.WithError(err).Warn("Failed to create DLQ Kafka producer (DLQ disabled)")
		dlqProducer = nil
	} else {
		defer dlqProducer.Close()
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
		}

		if event.EventType == "client_lifecycle_batch" {
			if event.TenantID == "" {
				logger.WithFields(logging.Fields{
					"event_type": event.EventType,
					"channel":    pb.Channel_CHANNEL_ANALYTICS,
				}).Warn("Dropping event without tenant_id for non-system channel")
				return nil
			}
			for _, data := range clientLifecycleBatchToProtoData(event.Data, logger) {
				grpcHub.BroadcastToTenant(event.TenantID, pb.EventType_EVENT_TYPE_CLIENT_LIFECYCLE_UPDATE, pb.Channel_CHANNEL_ANALYTICS, data)
			}
			return nil
		}

		// Route event to gRPC hub
		channel := mapEventTypeToChannel(event.EventType)
		eventType := mapEventTypeToProto(event.EventType)

		if channel == pb.Channel_CHANNEL_SYSTEM {
			if event.TenantID != "" {
				grpcHub.BroadcastToTenant(event.TenantID, eventType, channel, eventToProtoData(event.Data, logger))
			} else {
				grpcHub.BroadcastInfrastructure(eventType, eventToProtoData(event.Data, logger))
			}
		} else if event.TenantID != "" {
			grpcHub.BroadcastToTenant(event.TenantID, eventType, channel, eventToProtoData(event.Data, logger))
		} else {
			logger.WithFields(logging.Fields{
				"event_type": event.EventType,
				"channel":    channel,
			}).Warn("Dropping event without tenant_id for non-system channel")
		}

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
		}

		// Service-plane events that should never hit real-time channels.
		if event.EventType == "api_request_batch" {
			return nil
		}

		channel := mapEventTypeToChannel(event.EventType)
		eventType := mapEventTypeToProto(event.EventType)
		if eventType == pb.EventType_EVENT_TYPE_UNSPECIFIED {
			return nil
		}

		data := serviceEventToProtoData(event, logger)
		if data == nil {
			return fmt.Errorf("service event payload missing required data")
		}

		if channel == pb.Channel_CHANNEL_SYSTEM {
			if event.TenantID != "" {
				grpcHub.BroadcastToTenant(event.TenantID, eventType, channel, data)
			} else {
				grpcHub.BroadcastInfrastructure(eventType, data)
			}
		} else if event.TenantID != "" {
			grpcHub.BroadcastToTenant(event.TenantID, eventType, channel, data)
		} else {
			logger.WithFields(logging.Fields{
				"event_type": event.EventType,
				"channel":    channel,
			}).Warn("Dropping service event without tenant_id for non-system channel")
		}

		return nil
	}

	consumer.AddHandler(analyticsTopic, wrapWithDLQ("signalman-analytics", analyticsHandler))
	consumer.AddHandler(serviceEventsTopic, wrapWithDLQ("signalman-service", serviceHandler))

	// Mirrored topics from MM2 carry the source-region prefix (default MM2
	// behaviour: source-cluster alias added as topic-prefix). Subscribe so a
	// stream-origin Signalman in another region can still deliver events to
	// viewers attached to this regional Signalman.
	if mirrorPrefixes := strings.TrimSpace(config.GetEnv("MIRROR_REGION_PREFIXES", "")); mirrorPrefixes != "" {
		for prefix := range strings.SplitSeq(mirrorPrefixes, ",") {
			prefix = strings.TrimSpace(prefix)
			if prefix == "" {
				continue
			}
			consumer.AddHandler(prefix+"."+analyticsTopic, wrapWithDLQ("signalman-analytics-mirror", analyticsHandler))
			consumer.AddHandler(prefix+"."+serviceEventsTopic, wrapWithDLQ("signalman-service-mirror", serviceHandler))
		}
	}

	// Add health checks
	healthChecker.AddCheck("kafka", monitoring.KafkaConsumerHealthCheck(consumer.GetClient()))
	if dlqProducer != nil {
		healthChecker.AddCheck("kafka_dlq_producer", monitoring.KafkaProducerHealthCheck(dlqProducer.GetClient()))
	}
	healthChecker.AddCheck("config", monitoring.ConfigurationHealthCheck(map[string]string{
		"KAFKA_BROKERS":           strings.Join(brokers, ","),
		"KAFKA_TOPICS":            strings.Join([]string{analyticsTopic, serviceEventsTopic}, ","),
		"DECKLOG_DLQ_KAFKA_TOPIC": dlqTopic,
	}))

	// Start Kafka consumer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		if err := consumer.Start(ctx); err != nil {
			logger.WithError(err).Error("Kafka consumer error")
		}
	}()

	// Start gRPC server in a goroutine
	grpcPort := config.GetEnv("GRPC_PORT", "19005")
	go func() {
		grpcAddr := fmt.Sprintf(":%s", grpcPort)
		lis, err := net.Listen("tcp", grpcAddr)
		if err != nil {
			logger.WithError(err).Fatal("Failed to listen on gRPC port")
		}

		// Auth interceptor for service-to-service calls
		authInterceptor := middleware.GRPCAuthInterceptor(middleware.GRPCAuthConfig{
			ServiceToken: serviceToken,
			JWTSecret:    jwtSecret,
			Logger:       logger,
			SkipMethods: []string{
				"/grpc.health.v1.Health/Check",
				"/grpc.health.v1.Health/Watch",
			},
		})

		streamAuthInterceptor := middleware.GRPCStreamAuthInterceptor(middleware.GRPCAuthConfig{
			ServiceToken: serviceToken,
			JWTSecret:    jwtSecret,
			Logger:       logger,
			SkipMethods: []string{
				"/grpc.health.v1.Health/Check",
				"/grpc.health.v1.Health/Watch",
			},
		})

		serverOpts := []grpc.ServerOption{
			grpc.ChainUnaryInterceptor(
				grpcutil.SanitizeUnaryServerInterceptor(),
				authInterceptor,
			),
			grpc.ChainStreamInterceptor(streamAuthInterceptor),
		}
		tlsCfg := grpcutil.ServerTLSConfig{
			CertFile:      config.GetEnv("GRPC_TLS_CERT_PATH", ""),
			KeyFile:       config.GetEnv("GRPC_TLS_KEY_PATH", ""),
			AllowInsecure: config.GetEnvBool("GRPC_ALLOW_INSECURE", false),
		}
		waitCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		if waitErr := grpcutil.WaitForServerTLSFiles(waitCtx, tlsCfg, logger); waitErr != nil {
			logger.WithError(waitErr).Fatal("Timed out waiting for Signalman gRPC TLS files")
		}
		tlsOpt, err := grpcutil.ServerTLS(tlsCfg, logger)
		if err != nil {
			logger.WithError(err).Fatal("Failed to configure Signalman gRPC TLS")
		}
		if tlsOpt != nil {
			serverOpts = append(serverOpts, tlsOpt)
		}
		grpcSrv := grpc.NewServer(serverOpts...)
		pb.RegisterSignalmanServiceServer(grpcSrv, signalmanServer)

		// gRPC health service so Quartermaster's gRPC probe passes
		hs := health.NewServer()
		hs.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
		hs.SetServingStatus(pb.SignalmanService_ServiceDesc.ServiceName, grpc_health_v1.HealthCheckResponse_SERVING)
		grpc_health_v1.RegisterHealthServer(grpcSrv, hs)
		reflection.Register(grpcSrv)

		logger.WithField("addr", grpcAddr).Info("Starting gRPC server")
		if err := grpcSrv.Serve(lis); err != nil {
			logger.WithError(err).Fatal("gRPC server failed")
		}
	}()

	// Setup HTTP router for health/metrics only
	router := server.SetupServiceRouter(logger, "signalman", healthChecker, metricsCollector)

	// Start HTTP server with graceful shutdown
	serverConfig := server.DefaultConfig("signalman", "18009")

	// Best-effort service registration in Quartermaster (using gRPC)
	// Must be launched BEFORE server.Start() which blocks
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
		defer qc.Close()
		grpcPortInt, _ := strconv.Atoi(grpcPort)
		if grpcPortInt <= 0 || grpcPortInt > 65535 {
			logger.Warn("Quartermaster bootstrap skipped: invalid port")
			return
		}
		advertiseHost := config.GetEnv("SIGNALMAN_HOST", "signalman")
		clusterID := config.GetEnv("CLUSTER_ID", "")
		req := &pb.BootstrapServiceRequest{
			Type:          "signalman",
			Version:       version.Version,
			Protocol:      "grpc",
			Port:          int32(grpcPortInt),
			AdvertiseHost: &advertiseHost,
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
		if _, err := qmbootstrap.BootstrapServiceWithRetry(context.Background(), qc, req, logger, qmbootstrap.DefaultRetryConfig("signalman")); err != nil {
			logger.WithError(err).Warn("Quartermaster bootstrap (signalman) failed")
		} else {
			logger.Info("Quartermaster bootstrap (signalman) ok")
		}
	}()

	server.RegisterEnvFileReload("signalman", logger)
	if err := server.Start(serverConfig, router, logger); err != nil {
		logger.WithError(err).Fatal("HTTP server startup failed")
	}
}

// mapEventTypeToChannel maps Kafka event types to gRPC channels
func mapEventTypeToChannel(eventType string) pb.Channel {
	switch eventType {
	case "stream_lifecycle_update", "stream_track_list", "stream_buffer", "stream_end", "stream_source", "play_rewrite", "push_rewrite":
		return pb.Channel_CHANNEL_STREAMS
	case "node_lifecycle_update", "load_balancing":
		return pb.Channel_CHANNEL_SYSTEM
	case "storage_lifecycle", "storage_snapshot", "process_billing":
		return pb.Channel_CHANNEL_ANALYTICS
	case "message_lifecycle", "message_received", "message_updated", "conversation_created", "conversation_updated":
		return pb.Channel_CHANNEL_MESSAGING
	case "skipper_investigation":
		return pb.Channel_CHANNEL_AI
	default:
		return pb.Channel_CHANNEL_ANALYTICS
	}
}

// mapEventTypeToProto maps Kafka event type strings to proto EventType
func mapEventTypeToProto(eventType string) pb.EventType {
	switch eventType {
	// Stream events
	case "stream_lifecycle_update":
		return pb.EventType_EVENT_TYPE_STREAM_LIFECYCLE_UPDATE
	case "stream_track_list":
		return pb.EventType_EVENT_TYPE_STREAM_TRACK_LIST
	case "stream_buffer":
		return pb.EventType_EVENT_TYPE_STREAM_BUFFER
	case "stream_end":
		return pb.EventType_EVENT_TYPE_STREAM_END
	case "stream_source":
		return pb.EventType_EVENT_TYPE_STREAM_SOURCE
	case "play_rewrite":
		return pb.EventType_EVENT_TYPE_PLAY_REWRITE
	// System events
	case "node_lifecycle_update":
		return pb.EventType_EVENT_TYPE_NODE_LIFECYCLE_UPDATE
	case "load_balancing":
		return pb.EventType_EVENT_TYPE_LOAD_BALANCING
	// Analytics events
	case "viewer_connect":
		return pb.EventType_EVENT_TYPE_VIEWER_CONNECT
	case "viewer_disconnect":
		return pb.EventType_EVENT_TYPE_VIEWER_DISCONNECT
	case "client_lifecycle_update", "client_lifecycle_batch":
		return pb.EventType_EVENT_TYPE_CLIENT_LIFECYCLE_UPDATE
	case "clip_lifecycle":
		return pb.EventType_EVENT_TYPE_CLIP_LIFECYCLE
	case "dvr_lifecycle":
		return pb.EventType_EVENT_TYPE_DVR_LIFECYCLE
	case "vod_lifecycle":
		return pb.EventType_EVENT_TYPE_VOD_LIFECYCLE
	case "push_rewrite":
		return pb.EventType_EVENT_TYPE_PUSH_REWRITE
	case "push_out_start":
		return pb.EventType_EVENT_TYPE_PUSH_OUT_START
	case "push_end":
		return pb.EventType_EVENT_TYPE_PUSH_END
	case "recording_complete":
		return pb.EventType_EVENT_TYPE_RECORDING_COMPLETE
	case "storage_lifecycle":
		return pb.EventType_EVENT_TYPE_STORAGE_LIFECYCLE
	case "process_billing":
		return pb.EventType_EVENT_TYPE_PROCESS_BILLING
	case "storage_snapshot":
		return pb.EventType_EVENT_TYPE_STORAGE_SNAPSHOT
	// Messaging events
	case "message_lifecycle", "message_received", "message_updated", "conversation_created", "conversation_updated":
		return pb.EventType_EVENT_TYPE_MESSAGE_LIFECYCLE
	// AI events
	case "skipper_investigation":
		return pb.EventType_EVENT_TYPE_SKIPPER_INVESTIGATION
	default:
		return pb.EventType_EVENT_TYPE_UNSPECIFIED
	}
}

// eventToProtoData converts Kafka event data to proto EventData
// Data comes as a MistTrigger envelope from Kafka
func eventToProtoData(data map[string]interface{}, logger logging.Logger) *pb.EventData {
	eventData := &pb.EventData{}
	mt, ok := mistTriggerFromEventData(data, logger)
	if !ok {
		return eventData
	}

	// Extract typed payload from MistTrigger oneof
	switch p := mt.GetTriggerPayload().(type) {
	case *pb.MistTrigger_ClientLifecycleUpdate:
		eventData.Payload = &pb.EventData_ClientLifecycle{ClientLifecycle: p.ClientLifecycleUpdate}
	case *pb.MistTrigger_NodeLifecycleUpdate:
		eventData.Payload = &pb.EventData_NodeLifecycle{NodeLifecycle: p.NodeLifecycleUpdate}
	case *pb.MistTrigger_TrackList:
		eventData.Payload = &pb.EventData_TrackList{TrackList: p.TrackList}
	case *pb.MistTrigger_ClipLifecycleData:
		eventData.Payload = &pb.EventData_ClipLifecycle{ClipLifecycle: p.ClipLifecycleData}
	case *pb.MistTrigger_DvrLifecycleData:
		eventData.Payload = &pb.EventData_DvrLifecycle{DvrLifecycle: p.DvrLifecycleData}
	case *pb.MistTrigger_VodLifecycleData:
		eventData.Payload = &pb.EventData_VodLifecycle{VodLifecycle: p.VodLifecycleData}
	case *pb.MistTrigger_LoadBalancingData:
		eventData.Payload = &pb.EventData_LoadBalancing{LoadBalancing: p.LoadBalancingData}
	case *pb.MistTrigger_PushRewrite:
		eventData.Payload = &pb.EventData_PushRewrite{PushRewrite: p.PushRewrite}
	case *pb.MistTrigger_PushOutStart:
		eventData.Payload = &pb.EventData_PushOutStart{PushOutStart: p.PushOutStart}
	case *pb.MistTrigger_PushEnd:
		eventData.Payload = &pb.EventData_PushEnd{PushEnd: p.PushEnd}
	case *pb.MistTrigger_ViewerConnect:
		eventData.Payload = &pb.EventData_ViewerConnect{ViewerConnect: p.ViewerConnect}
	case *pb.MistTrigger_ViewerDisconnect:
		eventData.Payload = &pb.EventData_ViewerDisconnect{ViewerDisconnect: p.ViewerDisconnect}
	case *pb.MistTrigger_StreamEnd:
		eventData.Payload = &pb.EventData_StreamEnd{StreamEnd: p.StreamEnd}
	case *pb.MistTrigger_RecordingComplete:
		eventData.Payload = &pb.EventData_Recording{Recording: p.RecordingComplete}
	case *pb.MistTrigger_StreamLifecycleUpdate:
		eventData.Payload = &pb.EventData_StreamLifecycle{StreamLifecycle: p.StreamLifecycleUpdate}
	case *pb.MistTrigger_StreamBuffer:
		eventData.Payload = &pb.EventData_StreamBuffer{StreamBuffer: p.StreamBuffer}
	case *pb.MistTrigger_StorageLifecycleData:
		eventData.Payload = &pb.EventData_StorageLifecycle{StorageLifecycle: p.StorageLifecycleData}
	case *pb.MistTrigger_ProcessBilling:
		eventData.Payload = &pb.EventData_ProcessBilling{ProcessBilling: p.ProcessBilling}
	case *pb.MistTrigger_PlayRewrite:
		eventData.Payload = &pb.EventData_PlayRewrite{PlayRewrite: p.PlayRewrite}
	case *pb.MistTrigger_StreamSource:
		eventData.Payload = &pb.EventData_StreamSource{StreamSource: p.StreamSource}
	case *pb.MistTrigger_StorageSnapshot:
		eventData.Payload = &pb.EventData_StorageSnapshot{StorageSnapshot: p.StorageSnapshot}
	case *pb.MistTrigger_MessageLifecycleData:
		eventData.Payload = &pb.EventData_MessageLifecycle{MessageLifecycle: p.MessageLifecycleData}
	}

	return eventData
}

func clientLifecycleBatchToProtoData(data map[string]interface{}, logger logging.Logger) []*pb.EventData {
	mt, ok := mistTriggerFromEventData(data, logger)
	if !ok {
		return nil
	}
	batch := mt.GetClientLifecycleBatch()
	if batch == nil {
		return nil
	}
	out := make([]*pb.EventData, 0, len(batch.GetSamples()))
	for _, sample := range batch.GetSamples() {
		if sample == nil {
			continue
		}
		out = append(out, &pb.EventData{
			Payload: &pb.EventData_ClientLifecycle{ClientLifecycle: sample},
		})
	}
	return out
}

func mistTriggerFromEventData(data map[string]interface{}, logger logging.Logger) (*pb.MistTrigger, bool) {
	if data == nil {
		return nil, false
	}

	b, err := json.Marshal(data)
	if err != nil {
		logger.WithError(err).Debug("Failed to marshal event data")
		return nil, false
	}

	var mt pb.MistTrigger
	if err := protojson.Unmarshal(b, &mt); err != nil {
		logger.WithError(err).Debug("Failed to unmarshal MistTrigger from event data")
		return nil, false
	}

	return &mt, true
}

func serviceEventToProtoData(event kafka.ServiceEvent, logger logging.Logger) *pb.EventData {
	switch event.EventType {
	case "message_received", "message_updated", "conversation_created", "conversation_updated":
		ml := &pb.MessageLifecycleData{}
		switch event.EventType {
		case "message_received":
			ml.EventType = pb.MessageLifecycleData_EVENT_TYPE_MESSAGE_CREATED
		case "message_updated":
			ml.EventType = pb.MessageLifecycleData_EVENT_TYPE_MESSAGE_UPDATED
		case "conversation_created":
			ml.EventType = pb.MessageLifecycleData_EVENT_TYPE_CONVERSATION_CREATED
		case "conversation_updated":
			ml.EventType = pb.MessageLifecycleData_EVENT_TYPE_CONVERSATION_UPDATED
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

		return &pb.EventData{Payload: &pb.EventData_MessageLifecycle{MessageLifecycle: ml}}
	case "skipper_investigation":
		return &pb.EventData{}
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
	default:
		return 0, false
	}
}
