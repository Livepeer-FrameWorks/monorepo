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
	topicsEnv := config.GetEnv("ANALYTICS_KAFKA_TOPIC", "analytics_events")
	topics := strings.Split(topicsEnv, ",")
	serviceToken := config.RequireEnv("SERVICE_TOKEN")
	quartermasterGRPCAddr := config.GetEnv("QUARTERMASTER_GRPC_ADDR", "quartermaster:19002")

	consumer, err := kafka.NewConsumer(brokers, groupID, clusterID, clientID, logger)
	if err != nil {
		logger.WithError(err).Fatal("Failed to initialize Kafka consumer")
	}
	defer consumer.Close()

	// Register Kafka message handler that routes to gRPC hub
	msgHandler := func(ctx context.Context, msg kafka.Message) error {
		var event kafka.AnalyticsEvent
		if err := json.Unmarshal(msg.Value, &event); err != nil {
			logger.WithError(err).Error("Failed to unmarshal analytics event")
			return nil
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

	for _, topic := range topics {
		consumer.AddHandler(topic, msgHandler)
	}

	// Add health checks
	healthChecker.AddCheck("kafka", monitoring.KafkaConsumerHealthCheck(consumer.GetClient()))
	healthChecker.AddCheck("config", monitoring.ConfigurationHealthCheck(map[string]string{
		"KAFKA_BROKERS": strings.Join(brokers, ","),
		"KAFKA_TOPICS":  topicsEnv,
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
			Logger:       logger,
			SkipMethods: []string{
				"/grpc.health.v1.Health/Check",
				"/grpc.health.v1.Health/Watch",
			},
		})

		grpcSrv := grpc.NewServer(grpc.UnaryInterceptor(authInterceptor))
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
		advertiseHost := config.GetEnv("SIGNALMAN_HOST", "signalman")
		if _, err := qc.BootstrapService(ctx, &pb.BootstrapServiceRequest{
			Type:          "signalman",
			Version:       version.Version,
			Protocol:      "grpc",
			Port:          int32(grpcPortInt),
			AdvertiseHost: &advertiseHost,
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
	case "stream_lifecycle_update", "stream_track_list", "stream_buffer", "stream_end", "stream_source", "play_rewrite":
		return pb.Channel_CHANNEL_STREAMS
	case "node_lifecycle_update", "load_balancing":
		return pb.Channel_CHANNEL_SYSTEM
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
	case "push_rewrite":
		return pb.EventType_EVENT_TYPE_PUSH_REWRITE
	case "push_out_start":
		return pb.EventType_EVENT_TYPE_PUSH_OUT_START
	case "push_end":
		return pb.EventType_EVENT_TYPE_PUSH_END
	case "stream_bandwidth":
		return pb.EventType_EVENT_TYPE_STREAM_BANDWIDTH
	case "recording_complete":
		return pb.EventType_EVENT_TYPE_RECORDING_COMPLETE
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
	case *pb.MistTrigger_StreamBandwidth:
		eventData.Payload = &pb.EventData_StreamBandwidth{StreamBandwidth: p.StreamBandwidth}
	case *pb.MistTrigger_RecordingComplete:
		eventData.Payload = &pb.EventData_Recording{Recording: p.RecordingComplete}
	}

	return eventData
}
