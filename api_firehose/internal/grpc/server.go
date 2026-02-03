package grpc

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"frameworks/pkg/logging"
	"frameworks/pkg/middleware"

	"github.com/prometheus/client_golang/prometheus"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"

	"frameworks/pkg/kafka"
	pb "frameworks/pkg/proto"
	"github.com/google/uuid"
)

// DecklogMetrics holds all Prometheus metrics for Decklog
type DecklogMetrics struct {
	EventsIngested     *prometheus.CounterVec
	ProcessingDuration *prometheus.HistogramVec
	KafkaMessages      *prometheus.CounterVec
	GRPCRequests       *prometheus.CounterVec
	KafkaDuration      *prometheus.HistogramVec
	KafkaLag           *prometheus.GaugeVec
}

type DecklogServer struct {
	pb.UnimplementedDecklogServiceServer
	producer           kafka.ProducerInterface
	logger             logging.Logger
	metrics            *DecklogMetrics
	serviceEventsTopic string
}

func NewDecklogServer(producer kafka.ProducerInterface, logger logging.Logger, metrics *DecklogMetrics, serviceEventsTopic string) *DecklogServer {
	if serviceEventsTopic == "" {
		serviceEventsTopic = "service_events"
	}
	return &DecklogServer{
		producer:           producer,
		logger:             logger,
		metrics:            metrics,
		serviceEventsTopic: serviceEventsTopic,
	}
}

// convertProtobufToKafkaEvent converts any protobuf message to kafka.AnalyticsEvent with transparent JSON serialization
func (s *DecklogServer) convertProtobufToKafkaEvent(msg interface{}, eventType, source, tenantID string) (*kafka.AnalyticsEvent, error) {
	// Tenant ID should be enriched by Foghorn; do not normalize to zero UUID.
	normalized := tenantID
	if normalized == "" || !isValidUUID(normalized) {
		// Reject to avoid polluting Kafka with un-attributed events.
		s.logger.WithFields(logging.Fields{
			"event_type": eventType,
			"tenant_id":  tenantID,
		}).Error("Rejected event: missing or invalid tenant_id")
		if s.metrics != nil && s.metrics.EventsIngested != nil {
			s.metrics.EventsIngested.WithLabelValues(eventType, "tenant_rejected").Inc()
		}
		return nil, fmt.Errorf("tenant_id required for event_type %s", eventType)
	}
	// Serialize the entire protobuf message to JSON transparently
	marshaler := protojson.MarshalOptions{
		UseProtoNames:   true,
		EmitUnpopulated: false,
	}

	jsonBytes, err := marshaler.Marshal(msg.(proto.Message))
	if err != nil {
		return nil, fmt.Errorf("failed to marshal protobuf message: %w", err)
	}

	// Parse JSON into map for Data field - this is the transparent representation
	var dataMap map[string]interface{}
	if err := json.Unmarshal(jsonBytes, &dataMap); err != nil {
		return nil, fmt.Errorf("failed to parse JSON to map: %w", err)
	}

	event := &kafka.AnalyticsEvent{
		EventID:   generateEventID(),
		EventType: eventType,
		Timestamp: time.Now(),
		Source:    source,
		TenantID:  normalized,
		Data:      dataMap, // Transparent protobuf message as JSON
	}

	return event, nil
}

func isValidUUID(s string) bool {
	if s == "" {
		return false
	}
	_, err := uuid.Parse(s)
	return err == nil
}

func generateEventID() string {
	return uuid.New().String()
}

// SendServiceEvent handles service-plane events and publishes to the service_events topic
func (s *DecklogServer) SendServiceEvent(ctx context.Context, event *pb.ServiceEvent) (*emptypb.Empty, error) {
	start := time.Now()

	if s.metrics != nil {
		s.metrics.GRPCRequests.WithLabelValues("SendServiceEvent", "requested").Inc()
	}

	if event == nil {
		return nil, fmt.Errorf("service event cannot be nil")
	}

	eventID := event.GetEventId()
	if eventID == "" {
		eventID = generateEventID()
	}

	eventType := event.GetEventType()
	if eventType == "" {
		eventType = "unknown"
	}

	tenantID := event.GetTenantId()
	if tenantID == "" || !isValidUUID(tenantID) {
		if s.metrics != nil && s.metrics.EventsIngested != nil {
			s.metrics.EventsIngested.WithLabelValues(eventType, "tenant_missing").Inc()
		}
		return nil, fmt.Errorf("tenant_id required for service event type %s", eventType)
	}

	timestamp := time.Now()
	if event.Timestamp != nil {
		timestamp = event.Timestamp.AsTime()
	}

	data, err := serviceEventPayloadToMap(event)
	if err != nil {
		if s.metrics != nil {
			s.metrics.GRPCRequests.WithLabelValues("SendServiceEvent", "conversion_error").Inc()
		}
		return nil, fmt.Errorf("failed to convert service event payload: %w", err)
	}
	if data == nil {
		data = map[string]interface{}{}
	}

	serviceEvent := &kafka.ServiceEvent{
		EventID:      eventID,
		EventType:    eventType,
		Timestamp:    timestamp,
		Source:       event.GetSource(),
		TenantID:     tenantID,
		UserID:       event.GetUserId(),
		ResourceType: event.GetResourceType(),
		ResourceID:   event.GetResourceId(),
		Data:         data,
	}

	payload, err := json.Marshal(serviceEvent)
	if err != nil {
		if s.metrics != nil {
			s.metrics.GRPCRequests.WithLabelValues("SendServiceEvent", "conversion_error").Inc()
		}
		return nil, fmt.Errorf("failed to marshal service event: %w", err)
	}

	headers := map[string]string{
		"source":     serviceEvent.Source,
		"event_type": serviceEvent.EventType,
	}
	if tenantID != "" {
		headers["tenant_id"] = tenantID
	}

	kafkaStart := time.Now()
	if err := s.producer.ProduceMessage(s.serviceEventsTopic, []byte(eventID), payload, headers); err != nil {
		if s.metrics != nil {
			s.metrics.GRPCRequests.WithLabelValues("SendServiceEvent", "kafka_error").Inc()
			s.metrics.KafkaMessages.WithLabelValues(s.serviceEventsTopic, "publish", "error").Inc()
		}
		return nil, fmt.Errorf("failed to publish service event: %w", err)
	}

	if s.metrics != nil {
		s.metrics.EventsIngested.WithLabelValues(eventType, "processed").Inc()
		s.metrics.ProcessingDuration.WithLabelValues(eventType).Observe(time.Since(start).Seconds())
		s.metrics.GRPCRequests.WithLabelValues("SendServiceEvent", "success").Inc()
		s.metrics.KafkaMessages.WithLabelValues(s.serviceEventsTopic, "publish", "success").Inc()
		s.metrics.KafkaDuration.WithLabelValues("publish").Observe(time.Since(kafkaStart).Seconds())
	}

	s.logger.WithFields(logging.Fields{
		"event_type": eventType,
		"tenant_id":  tenantID,
		"event_id":   eventID,
		"topic":      s.serviceEventsTopic,
	}).Debug("Service event sent to Kafka")

	return &emptypb.Empty{}, nil
}

func serviceEventPayloadToMap(event *pb.ServiceEvent) (map[string]interface{}, error) {
	if event == nil {
		return map[string]interface{}{}, nil
	}

	switch payload := event.GetPayload().(type) {
	case *pb.ServiceEvent_ApiRequestBatch:
		return protoMessageToMap(payload.ApiRequestBatch)
	case *pb.ServiceEvent_AuthEvent:
		return protoMessageToMap(payload.AuthEvent)
	case *pb.ServiceEvent_TenantEvent:
		return protoMessageToMap(payload.TenantEvent)
	case *pb.ServiceEvent_ClusterEvent:
		return protoMessageToMap(payload.ClusterEvent)
	case *pb.ServiceEvent_StreamChangeEvent:
		return protoMessageToMap(payload.StreamChangeEvent)
	case *pb.ServiceEvent_StreamKeyEvent:
		return protoMessageToMap(payload.StreamKeyEvent)
	case *pb.ServiceEvent_BillingEvent:
		return protoMessageToMap(payload.BillingEvent)
	case *pb.ServiceEvent_SupportEvent:
		return protoMessageToMap(payload.SupportEvent)
	case *pb.ServiceEvent_ArtifactEvent:
		return protoMessageToMap(payload.ArtifactEvent)
	default:
		return map[string]interface{}{}, nil
	}
}

func protoMessageToMap(msg proto.Message) (map[string]interface{}, error) {
	if msg == nil {
		return map[string]interface{}{}, nil
	}

	b, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("marshal payload: %w", err)
	}

	var out map[string]interface{}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("unmarshal payload: %w", err)
	}

	return out, nil
}

// unwrapMistTrigger picks the inner payload and canonical (current-compatible) event type.
// Note: We publish only the inner payload to Kafka Data to avoid consumer confusion.
func (s *DecklogServer) unwrapMistTrigger(trigger *pb.MistTrigger) (proto.Message, string, string) {
	tenantID := ""
	if trigger.TenantId != nil {
		tenantID = *trigger.TenantId
	}
	var eventType string

	switch payload := trigger.GetTriggerPayload().(type) {
	case *pb.MistTrigger_PushRewrite:
		eventType = "push_rewrite"
	case *pb.MistTrigger_PlayRewrite:
		eventType = "play_rewrite"
	case *pb.MistTrigger_StreamSource:
		eventType = "stream_source"
	case *pb.MistTrigger_PushOutStart:
		eventType = "push_out_start"
	case *pb.MistTrigger_PushEnd:
		eventType = "push_end"
	case *pb.MistTrigger_ViewerConnect:
		eventType = "viewer_connect"
	case *pb.MistTrigger_ViewerDisconnect:
		eventType = "viewer_disconnect"
	case *pb.MistTrigger_StreamBuffer:
		eventType = "stream_buffer"
	case *pb.MistTrigger_StreamEnd:
		eventType = "stream_end"
	case *pb.MistTrigger_TrackList:
		eventType = "stream_track_list"
	case *pb.MistTrigger_RecordingComplete:
		eventType = "recording_complete"
	case *pb.MistTrigger_StreamLifecycleUpdate:
		eventType = "stream_lifecycle_update"
		if payload.StreamLifecycleUpdate.TenantId != nil {
			tenantID = *payload.StreamLifecycleUpdate.TenantId
		}
	case *pb.MistTrigger_ClientLifecycleUpdate:
		eventType = "client_lifecycle_update"
		if payload.ClientLifecycleUpdate.TenantId != nil {
			tenantID = *payload.ClientLifecycleUpdate.TenantId
		}
	case *pb.MistTrigger_NodeLifecycleUpdate:
		eventType = "node_lifecycle_update"
		if payload.NodeLifecycleUpdate.TenantId != nil {
			tenantID = *payload.NodeLifecycleUpdate.TenantId
		}
	case *pb.MistTrigger_LoadBalancingData:
		eventType = "load_balancing"
		if payload.LoadBalancingData.TenantId != nil {
			tenantID = *payload.LoadBalancingData.TenantId
		}
	case *pb.MistTrigger_ClipLifecycleData:
		eventType = "clip_lifecycle"
		if payload.ClipLifecycleData.TenantId != nil {
			tenantID = *payload.ClipLifecycleData.TenantId
		}
	case *pb.MistTrigger_DvrLifecycleData:
		eventType = "dvr_lifecycle"
		if payload.DvrLifecycleData.TenantId != nil {
			tenantID = *payload.DvrLifecycleData.TenantId
		}
	case *pb.MistTrigger_StorageLifecycleData:
		eventType = "storage_lifecycle"
		if payload.StorageLifecycleData.TenantId != nil {
			tenantID = *payload.StorageLifecycleData.TenantId
		}
	case *pb.MistTrigger_ProcessBilling:
		eventType = "process_billing"
		if payload.ProcessBilling.TenantId != nil {
			tenantID = *payload.ProcessBilling.TenantId
		}
	case *pb.MistTrigger_StorageSnapshot:
		eventType = "storage_snapshot"
		if payload.StorageSnapshot.TenantId != nil {
			tenantID = *payload.StorageSnapshot.TenantId
		}
	case *pb.MistTrigger_VodLifecycleData:
		eventType = "vod_lifecycle"
		if payload.VodLifecycleData.TenantId != nil {
			tenantID = *payload.VodLifecycleData.TenantId
		}
	default:
		eventType = "unknown"
	}

	return trigger, eventType, tenantID
}

// SendEvent handles all enriched events through a unified MistTrigger envelope
func (s *DecklogServer) SendEvent(ctx context.Context, trigger *pb.MistTrigger) (*emptypb.Empty, error) {
	start := time.Now()

	// Track gRPC request
	if s.metrics != nil {
		s.metrics.GRPCRequests.WithLabelValues("SendEvent", "requested").Inc()
	}

	// Unwrap inner payload and determine event type + tenant
	msg, eventType, tenantID := s.unwrapMistTrigger(trigger)
	if s.metrics != nil && s.metrics.EventsIngested != nil {
		s.metrics.EventsIngested.WithLabelValues(eventType, "received").Inc()
	}

	// Convert to analytics event with transparent protobuf serialization of the full envelope
	analyticsEvent, err := s.convertProtobufToKafkaEvent(msg, eventType, "foghorn", tenantID)
	if err != nil {
		s.logger.WithError(err).WithFields(logging.Fields{
			"trigger_type": trigger.GetTriggerType(),
			"node_id":      trigger.GetNodeId(),
		}).Error("Failed to convert event to analytics message")
		if s.metrics != nil {
			if s.metrics.EventsIngested != nil {
				s.metrics.EventsIngested.WithLabelValues(eventType, "conversion_error").Inc()
			}
			s.metrics.GRPCRequests.WithLabelValues("SendEvent", "conversion_error").Inc()
		}
		return nil, err
	}

	// Note: stream-level fields are in the protobuf data, not top-level Kafka fields

	// Publish to Kafka
	kafkaStart := time.Now()
	if err := s.producer.PublishTypedEvent(analyticsEvent); err != nil {
		s.logger.WithError(err).WithFields(logging.Fields{
			"trigger_type": trigger.GetTriggerType(),
			"node_id":      trigger.GetNodeId(),
		}).Error("Failed to publish event")
		if s.metrics != nil {
			if s.metrics.EventsIngested != nil {
				s.metrics.EventsIngested.WithLabelValues(eventType, "publish_error").Inc()
			}
			s.metrics.GRPCRequests.WithLabelValues("SendEvent", "kafka_error").Inc()
			s.metrics.KafkaMessages.WithLabelValues("analytics_events", "publish", "error").Inc()
		}
		return nil, err
	}

	// Track success
	if s.metrics != nil {
		// Operation label = publish, topic accounted in KafkaMessages
		s.metrics.KafkaDuration.WithLabelValues("publish").Observe(time.Since(kafkaStart).Seconds())
		// event_type label should use the derived eventType, not the source
		s.metrics.EventsIngested.WithLabelValues(eventType, "processed").Inc()
		s.metrics.ProcessingDuration.WithLabelValues(eventType).Observe(time.Since(start).Seconds())
		s.metrics.GRPCRequests.WithLabelValues("SendEvent", "success").Inc()
		s.metrics.KafkaMessages.WithLabelValues("analytics_events", "publish", "success").Inc()
	}

	s.logger.WithFields(logging.Fields{
		"trigger_type": trigger.GetTriggerType(),
		"node_id":      trigger.GetNodeId(),
		"tenant_id":    tenantID,
		"event_id":     analyticsEvent.EventID,
	}).Debug("Event sent to Kafka")

	return &emptypb.Empty{}, nil
}

// GRPCServerConfig contains configuration for creating a Decklog gRPC server
type GRPCServerConfig struct {
	Producer      kafka.ProducerInterface
	Logger        logging.Logger
	Metrics       *DecklogMetrics
	CertFile      string
	KeyFile       string
	AllowInsecure bool
	ServiceToken  string
	// ServiceEventsTopic overrides the topic for ServiceEvent publishing.
	ServiceEventsTopic string
}

// NewGRPCServer creates a new gRPC server with proper TLS configuration
func NewGRPCServer(cfg GRPCServerConfig) (*grpc.Server, error) {
	var opts []grpc.ServerOption

	if !cfg.AllowInsecure {
		// Load TLS credentials
		creds, err := credentials.NewServerTLSFromFile(cfg.CertFile, cfg.KeyFile)
		if err != nil {
			return nil, err
		}
		opts = append(opts, grpc.Creds(creds))
	}

	// Chain auth interceptor with logging interceptor
	authInterceptor := middleware.GRPCAuthInterceptor(middleware.GRPCAuthConfig{
		ServiceToken: cfg.ServiceToken,
		Logger:       cfg.Logger,
		SkipMethods: []string{
			"/grpc.health.v1.Health/Check",
			"/grpc.health.v1.Health/Watch",
		},
	})

	// Add interceptors for logging, metrics, etc.
	opts = append(opts, grpc.ChainUnaryInterceptor(authInterceptor, unaryInterceptor(cfg.Logger)))
	opts = append(opts, grpc.StreamInterceptor(streamInterceptor(cfg.Logger)))

	server := grpc.NewServer(opts...)
	pb.RegisterDecklogServiceServer(server, NewDecklogServer(cfg.Producer, cfg.Logger, cfg.Metrics, cfg.ServiceEventsTopic))
	// Register gRPC health checking service
	hs := health.NewServer()
	grpc_health_v1.RegisterHealthServer(server, hs)
	return server, nil
}

// Interceptors for logging and metrics
func unaryInterceptor(logger logging.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		start := time.Now()
		resp, err := handler(ctx, req)
		logger.WithFields(logging.Fields{
			"method":   info.FullMethod,
			"duration": time.Since(start),
			"error":    err,
		}).Debug("gRPC request processed")
		return resp, err
	}
}

func streamInterceptor(logger logging.Logger) grpc.StreamServerInterceptor {
	return func(srv interface{}, stream grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		start := time.Now()
		err := handler(srv, stream)
		logger.WithFields(logging.Fields{
			"method":   info.FullMethod,
			"duration": time.Since(start),
			"error":    err,
		}).Debug("gRPC stream processed")
		return err
	}
}
