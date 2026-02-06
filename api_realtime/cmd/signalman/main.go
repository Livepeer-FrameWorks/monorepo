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
	qmclient "frameworks/pkg/clients/quartermaster"
	"frameworks/pkg/config"
	"frameworks/pkg/grpcutil"
	"frameworks/pkg/kafka"
	"frameworks/pkg/logging"
	"frameworks/pkg/middleware"
	"frameworks/pkg/monitoring"
	pb "frameworks/pkg/proto"
	"frameworks/pkg/server"
	"frameworks/pkg/version"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/protobuf/encoding/protojson"
)

func main() {
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

	// Setup Kafka consumer
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

	consumer, err := kafka.NewConsumer(brokers, groupID, clusterID, clientID, logger)
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

		grpcSrv := grpc.NewServer(
			grpc.ChainUnaryInterceptor(
				grpcutil.SanitizeUnaryServerInterceptor(),
				authInterceptor,
			),
			grpc.ChainStreamInterceptor(streamAuthInterceptor),
		)
		pb.RegisterSignalmanServiceServer(grpcSrv, signalmanServer)

		// gRPC health service so Quartermaster's gRPC probe passes
		hs := health.NewServer()
		hs.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
		hs.SetServingStatus(pb.SignalmanService_ServiceDesc.ServiceName, grpc_health_v1.HealthCheckResponse_SERVING)
		grpc_health_v1.RegisterHealthServer(grpcSrv, hs)

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
		grpcPortInt, _ := strconv.Atoi(grpcPort)
		if grpcPortInt <= 0 || grpcPortInt > 65535 {
			logger.Warn("Quartermaster bootstrap skipped: invalid port")
			return
		}
		advertiseHost := config.GetEnv("SIGNALMAN_HOST", "signalman")
		clusterID := config.GetEnv("CLUSTER_ID", "")
		if _, err := qc.BootstrapService(ctx, &pb.BootstrapServiceRequest{
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
		}); err != nil {
			logger.WithError(err).Warn("Quartermaster bootstrap (signalman) failed")
		} else {
			logger.Info("Quartermaster bootstrap (signalman) ok")
		}
	}()

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
	case "client_lifecycle_update":
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
	if data == nil {
		return eventData
	}

	// Marshal to JSON then unmarshal to MistTrigger protobuf
	b, err := json.Marshal(data)
	if err != nil {
		return eventData
	}

	var mt pb.MistTrigger
	if err := protojson.Unmarshal(b, &mt); err != nil {
		logger.WithError(err).Debug("Failed to unmarshal MistTrigger from event data")
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
