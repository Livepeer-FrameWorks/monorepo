package grpc

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/grpcutil"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"

	"github.com/prometheus/client_golang/prometheus"

	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	pb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto"
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
	// SourceRegion / SourceClusterID identify this Decklog instance for
	// envelope v2 backfill. When a producer emits an event without stamping
	// source_region or source_cluster_id, Decklog fills them with these
	// values so MirrorMaker fan-in consumers always see a non-empty source.
	sourceRegion    string
	sourceClusterID string
}

// DecklogServerConfig carries optional construction parameters for the
// Decklog gRPC server. Zero-value fields fall back to defaults documented
// inline in NewDecklogServerWithConfig.
type DecklogServerConfig struct {
	ServiceEventsTopic string
	SourceRegion       string
	SourceClusterID    string
}

func NewDecklogServer(producer kafka.ProducerInterface, logger logging.Logger, metrics *DecklogMetrics, serviceEventsTopic string) *DecklogServer {
	return NewDecklogServerWithConfig(producer, logger, metrics, DecklogServerConfig{ServiceEventsTopic: serviceEventsTopic})
}

func NewDecklogServerWithConfig(producer kafka.ProducerInterface, logger logging.Logger, metrics *DecklogMetrics, cfg DecklogServerConfig) *DecklogServer {
	topic := cfg.ServiceEventsTopic
	if topic == "" {
		topic = "service_events"
	}
	return &DecklogServer{
		producer:           producer,
		logger:             logger,
		metrics:            metrics,
		serviceEventsTopic: topic,
		sourceRegion:       cfg.SourceRegion,
		sourceClusterID:    cfg.SourceClusterID,
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

	protoMsg, ok := msg.(proto.Message)
	if !ok {
		return nil, fmt.Errorf("unexpected message type %T", msg)
	}

	jsonBytes, err := marshaler.Marshal(protoMsg)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal protobuf message: %w", err)
	}

	// Parse JSON into map for Data field - this is the transparent representation
	var dataMap map[string]interface{}
	if err := json.Unmarshal(jsonBytes, &dataMap); err != nil {
		return nil, fmt.Errorf("failed to parse JSON to map: %w", err)
	}

	event := &kafka.AnalyticsEvent{
		EventID:         generateEventID(),
		EventType:       eventType,
		Timestamp:       time.Now(),
		Source:          source,
		TenantID:        normalized,
		Data:            dataMap, // Transparent protobuf message as JSON
		SourceRegion:    s.sourceRegion,
		SourceClusterID: s.sourceClusterID,
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
		EventID:               eventID,
		EventType:             eventType,
		Timestamp:             timestamp,
		Source:                event.GetSource(),
		TenantID:              tenantID,
		UserID:                event.GetUserId(),
		ResourceType:          event.GetResourceType(),
		ResourceID:            event.GetResourceId(),
		Data:                  data,
		SourceRegion:          event.GetSourceRegion(),
		SourceClusterID:       event.GetSourceClusterId(),
		StreamOriginRegion:    event.GetStreamOriginRegion(),
		StreamOriginClusterID: event.GetStreamOriginClusterId(),
		SchemaVersion:         event.GetSchemaVersion(),
		CorrelationID:         event.GetCorrelationId(),
		CausationID:           event.GetCausationId(),
	}
	// Envelope v2 backfill: legacy producers don't stamp source_region or
	// source_cluster_id; fall back to Decklog's own identity so MirrorMaker
	// fan-in consumers always see a non-empty source.
	if serviceEvent.SourceRegion == "" {
		serviceEvent.SourceRegion = s.sourceRegion
	}
	if serviceEvent.SourceClusterID == "" {
		serviceEvent.SourceClusterID = s.sourceClusterID
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
	if serviceEvent.SourceRegion != "" {
		headers["source_region"] = serviceEvent.SourceRegion
	}
	if serviceEvent.SourceClusterID != "" {
		headers["source_cluster_id"] = serviceEvent.SourceClusterID
	}
	if serviceEvent.StreamOriginRegion != "" {
		headers["stream_origin_region"] = serviceEvent.StreamOriginRegion
	}
	if serviceEvent.StreamOriginClusterID != "" {
		headers["stream_origin_cluster_id"] = serviceEvent.StreamOriginClusterID
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
	case *pb.MistTrigger_FederationEventData:
		eventType = "federation_event"
		if payload.FederationEventData.TenantId != nil {
			tenantID = *payload.FederationEventData.TenantId
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
	if trigger == nil {
		if s.metrics != nil {
			s.metrics.GRPCRequests.WithLabelValues("SendEvent", "invalid_request").Inc()
		}
		return nil, fmt.Errorf("mist trigger cannot be nil")
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

	// Envelope v2 fields from the MistTrigger override Decklog-level defaults.
	// cluster_id (33) / origin_cluster_id (37) on the trigger play the
	// source_cluster_id / stream_origin_cluster_id roles; the new region
	// fields complete the pair. Producer-stamped values win; otherwise the
	// Decklog instance's local source identity stands (set in convert).
	if v := trigger.GetSourceRegion(); v != "" {
		analyticsEvent.SourceRegion = v
	}
	if v := trigger.GetClusterId(); v != "" {
		analyticsEvent.SourceClusterID = v
	}
	if v := trigger.GetStreamOriginRegion(); v != "" {
		analyticsEvent.StreamOriginRegion = v
	}
	if v := trigger.GetOriginClusterId(); v != "" {
		analyticsEvent.StreamOriginClusterID = v
	}
	if v := trigger.GetSchemaVersion(); v != 0 {
		analyticsEvent.SchemaVersion = v
	}
	if v := trigger.GetCorrelationId(); v != "" {
		analyticsEvent.CorrelationID = v
	}
	if v := trigger.GetCausationId(); v != "" {
		analyticsEvent.CausationID = v
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

// SendGatewayTelemetry handles per-orchestrator telemetry from Livepeer
// gateways. Discovery/state events are attributed to the cluster owner;
// transcode/AI outcome events are attributed to the stream tenant and also
// carry cluster-owner metadata for dual-attribution joins.
func (s *DecklogServer) SendGatewayTelemetry(ctx context.Context, event *pb.GatewayTelemetryEvent) (*emptypb.Empty, error) {
	start := time.Now()

	if s.metrics != nil {
		s.metrics.GRPCRequests.WithLabelValues("SendGatewayTelemetry", "requested").Inc()
	}
	if event == nil {
		if s.metrics != nil {
			s.metrics.GRPCRequests.WithLabelValues("SendGatewayTelemetry", "invalid_request").Inc()
		}
		return nil, fmt.Errorf("gateway telemetry event cannot be nil")
	}

	clusterOwnerTenantID := event.GetClusterOwnerTenantId()
	if !isValidUUID(clusterOwnerTenantID) {
		s.logger.WithFields(logging.Fields{
			"gateway_id":              event.GetGatewayId(),
			"cluster_id":              event.GetClusterId(),
			"stream_tenant_id":        event.GetStreamTenantId(),
			"cluster_owner_tenant_id": clusterOwnerTenantID,
		}).Error("Rejected gateway telemetry: missing or invalid cluster_owner_tenant_id")
		if s.metrics != nil {
			s.metrics.GRPCRequests.WithLabelValues("SendGatewayTelemetry", "tenant_rejected").Inc()
		}
		return nil, fmt.Errorf("gateway telemetry: cluster_owner_tenant_id (valid UUID) is required")
	}

	var (
		payload           proto.Message
		eventType         string
		effectiveTenantID = clusterOwnerTenantID
	)
	switch p := event.GetPayload().(type) {
	case *pb.GatewayTelemetryEvent_Discovery:
		payload, eventType = p.Discovery, "orchestrator_discovery_observed"
	case *pb.GatewayTelemetryEvent_State:
		payload, eventType = p.State, "orchestrator_state_update"
	case *pb.GatewayTelemetryEvent_Transcode:
		payload, eventType = p.Transcode, "orchestrator_transcode_outcome"
		if !isValidUUID(event.GetStreamTenantId()) {
			s.logger.WithFields(logging.Fields{
				"gateway_id":              event.GetGatewayId(),
				"cluster_id":              event.GetClusterId(),
				"stream_tenant_id":        event.GetStreamTenantId(),
				"cluster_owner_tenant_id": clusterOwnerTenantID,
			}).Error("Rejected gateway transcode telemetry: missing or invalid stream_tenant_id")
			if s.metrics != nil {
				s.metrics.GRPCRequests.WithLabelValues("SendGatewayTelemetry", "tenant_rejected").Inc()
			}
			return nil, fmt.Errorf("gateway telemetry: stream_tenant_id (valid UUID) is required for transcode outcomes")
		}
		effectiveTenantID = event.GetStreamTenantId()
	case *pb.GatewayTelemetryEvent_Ai:
		payload, eventType = p.Ai, "orchestrator_ai_outcome"
		if !isValidUUID(event.GetStreamTenantId()) {
			s.logger.WithFields(logging.Fields{
				"gateway_id":              event.GetGatewayId(),
				"cluster_id":              event.GetClusterId(),
				"stream_tenant_id":        event.GetStreamTenantId(),
				"cluster_owner_tenant_id": clusterOwnerTenantID,
			}).Error("Rejected gateway AI telemetry: missing or invalid stream_tenant_id")
			if s.metrics != nil {
				s.metrics.GRPCRequests.WithLabelValues("SendGatewayTelemetry", "tenant_rejected").Inc()
			}
			return nil, fmt.Errorf("gateway telemetry: stream_tenant_id (valid UUID) is required for AI outcomes")
		}
		effectiveTenantID = event.GetStreamTenantId()
	default:
		if s.metrics != nil {
			s.metrics.GRPCRequests.WithLabelValues("SendGatewayTelemetry", "unknown_event_type").Inc()
		}
		return nil, fmt.Errorf("gateway telemetry: payload oneof must be one of discovery|state|transcode|ai")
	}
	if payload == nil {
		if s.metrics != nil {
			s.metrics.GRPCRequests.WithLabelValues("SendGatewayTelemetry", "unknown_event_type").Inc()
		}
		return nil, fmt.Errorf("gateway telemetry: payload oneof contains nil %s payload", eventType)
	}

	if s.metrics != nil && s.metrics.EventsIngested != nil {
		s.metrics.EventsIngested.WithLabelValues(eventType, "received").Inc()
	}

	// Serialize the payload using the same transparent protojson approach
	// SendEvent uses, then supplement Data with the gateway/cluster identity
	// so Periscope can query without re-parsing the original wire envelope.
	dataMap, err := protoMessageToMap(payload)
	if err != nil {
		s.logger.WithError(err).WithFields(logging.Fields{
			"event_type": eventType,
			"gateway_id": event.GetGatewayId(),
		}).Error("Failed to serialise gateway telemetry payload")
		if s.metrics != nil {
			if s.metrics.EventsIngested != nil {
				s.metrics.EventsIngested.WithLabelValues(eventType, "conversion_error").Inc()
			}
			s.metrics.GRPCRequests.WithLabelValues("SendGatewayTelemetry", "conversion_error").Inc()
		}
		return nil, err
	}
	dataMap["gateway_id"] = event.GetGatewayId()
	dataMap["gateway_region"] = event.GetGatewayRegion()
	dataMap["cluster_id"] = event.GetClusterId()
	dataMap["cluster_owner_tenant_id"] = event.GetClusterOwnerTenantId()
	if streamTenant := event.GetStreamTenantId(); streamTenant != "" {
		dataMap["stream_tenant_id"] = streamTenant
	}

	ts := time.Now()
	if event.GetTimestamp() != nil {
		ts = event.GetTimestamp().AsTime()
	}

	analyticsEvent := &kafka.AnalyticsEvent{
		EventID:   generateEventID(),
		EventType: eventType,
		Timestamp: ts,
		Source:    "livepeer-gateway",
		TenantID:  effectiveTenantID,
		Data:      dataMap,
	}

	kafkaStart := time.Now()
	if err := s.producer.PublishTypedEvent(analyticsEvent); err != nil {
		s.logger.WithError(err).WithFields(logging.Fields{
			"event_type": eventType,
			"gateway_id": event.GetGatewayId(),
		}).Error("Failed to publish gateway telemetry")
		if s.metrics != nil {
			if s.metrics.EventsIngested != nil {
				s.metrics.EventsIngested.WithLabelValues(eventType, "publish_error").Inc()
			}
			s.metrics.GRPCRequests.WithLabelValues("SendGatewayTelemetry", "kafka_error").Inc()
			s.metrics.KafkaMessages.WithLabelValues("analytics_events", "publish", "error").Inc()
		}
		return nil, err
	}

	if s.metrics != nil {
		s.metrics.KafkaDuration.WithLabelValues("publish").Observe(time.Since(kafkaStart).Seconds())
		s.metrics.EventsIngested.WithLabelValues(eventType, "processed").Inc()
		s.metrics.ProcessingDuration.WithLabelValues(eventType).Observe(time.Since(start).Seconds())
		s.metrics.GRPCRequests.WithLabelValues("SendGatewayTelemetry", "success").Inc()
		s.metrics.KafkaMessages.WithLabelValues("analytics_events", "publish", "success").Inc()
	}

	s.logger.WithFields(logging.Fields{
		"event_type": eventType,
		"gateway_id": event.GetGatewayId(),
		"orch_addr":  dataMap["orch_addr"],
		"tenant_id":  effectiveTenantID,
	}).Debug("Gateway telemetry sent to Kafka")

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
	// SourceRegion / SourceClusterID identify this Decklog instance for
	// envelope v2 backfill. Empty values mean "no backfill at this instance"
	// and the consumer-side dedupe relies on producer-stamped values.
	SourceRegion    string
	SourceClusterID string
}

// NewGRPCServer creates a new gRPC server with proper TLS configuration
func NewGRPCServer(cfg GRPCServerConfig) (*grpc.Server, error) {
	var opts []grpc.ServerOption

	tlsCfg := grpcutil.ServerTLSConfig{
		CertFile:      cfg.CertFile,
		KeyFile:       cfg.KeyFile,
		AllowInsecure: cfg.AllowInsecure,
	}
	waitCtx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := grpcutil.WaitForServerTLSFiles(waitCtx, tlsCfg, cfg.Logger); err != nil {
		return nil, fmt.Errorf("wait for Decklog gRPC TLS files: %w", err)
	}
	tlsOpt, err := grpcutil.ServerTLS(tlsCfg, cfg.Logger)
	if err != nil {
		return nil, err
	}
	if tlsOpt != nil {
		opts = append(opts, tlsOpt)
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
	opts = append(opts, grpc.ChainUnaryInterceptor(unaryInterceptor(cfg.Logger), authInterceptor))
	opts = append(opts, grpc.StreamInterceptor(streamInterceptor(cfg.Logger)))

	server := grpc.NewServer(opts...)
	pb.RegisterDecklogServiceServer(server, NewDecklogServerWithConfig(cfg.Producer, cfg.Logger, cfg.Metrics, DecklogServerConfig{
		ServiceEventsTopic: cfg.ServiceEventsTopic,
		SourceRegion:       cfg.SourceRegion,
		SourceClusterID:    cfg.SourceClusterID,
	}))
	// Register gRPC health checking service
	hs := health.NewServer()
	grpc_health_v1.RegisterHealthServer(server, hs)
	reflection.Register(server)
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
		return resp, grpcutil.SanitizeError(err)
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
		return grpcutil.SanitizeError(err)
	}
}
