package grpc

import (
	"context"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"time"

	"frameworks/pkg/logging"
	"github.com/prometheus/client_golang/prometheus"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"frameworks/pkg/kafka"
	pb "frameworks/pkg/proto"
	"frameworks/pkg/validation"
)

// mapProtoEventTypeToValidation converts protobuf enum to our validation.EventType strings
func mapProtoEventTypeToValidation(t pb.EventType) validation.EventType {
	switch t {
	case pb.EventType_EVENT_TYPE_STREAM_INGEST:
		return validation.EventStreamIngest
	case pb.EventType_EVENT_TYPE_STREAM_VIEW:
		return validation.EventStreamView
	case pb.EventType_EVENT_TYPE_STREAM_LIFECYCLE:
		return validation.EventStreamLifecycle
	case pb.EventType_EVENT_TYPE_USER_CONNECTION:
		return validation.EventUserConnection
	case pb.EventType_EVENT_TYPE_PUSH_LIFECYCLE:
		return validation.EventPushLifecycle
	case pb.EventType_EVENT_TYPE_RECORDING_LIFECYCLE:
		return validation.EventRecordingLifecycle
	case pb.EventType_EVENT_TYPE_CLIENT_LIFECYCLE:
		return validation.EventClientLifecycle
	case pb.EventType_EVENT_TYPE_NODE_LIFECYCLE:
		return validation.EventNodeLifecycle
	case pb.EventType_EVENT_TYPE_LOAD_BALANCING:
		return validation.EventLoadBalancing
	case pb.EventType_EVENT_TYPE_TRACK_LIST:
		return validation.EventTrackList
	case pb.EventType_EVENT_TYPE_STREAM_BUFFER:
		return validation.EventStreamBuffer
	case pb.EventType_EVENT_TYPE_STREAM_END:
		return validation.EventStreamEnd
	case pb.EventType_EVENT_TYPE_BANDWIDTH_THRESHOLD:
		return validation.EventBandwidthThreshold
	default:
		return validation.EventType("unknown")
	}
}

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
	producer  kafka.ProducerInterface
	validator *validation.EventValidator
	logger    logging.Logger
	metrics   *DecklogMetrics
}

func NewDecklogServer(producer kafka.ProducerInterface, logger logging.Logger, metrics *DecklogMetrics) *DecklogServer {
	return &DecklogServer{
		producer:  producer,
		validator: validation.NewEventValidator(),
		logger:    logger,
		metrics:   metrics,
	}
}

// convertStringMapToInterface converts a map[string]string to map[string]interface{}
func convertStringMapToInterface(m map[string]string) map[string]interface{} {
	result := make(map[string]interface{}, len(m))
	for k, v := range m {
		// Try to convert numeric strings to numbers
		if num, err := strconv.ParseFloat(v, 64); err == nil {
			result[k] = num
			continue
		}
		// Try to convert boolean strings
		if v == "true" || v == "false" {
			result[k] = v == "true"
			continue
		}
		// Keep as string
		result[k] = v
	}
	return result
}

// StreamEvents handles streaming events from Helmsman
func (s *DecklogServer) StreamEvents(stream pb.DecklogService_StreamEventsServer) error {
	// Track gRPC request
	if s.metrics != nil {
		s.metrics.GRPCRequests.WithLabelValues("StreamEvents", "requested").Inc()
	}

	for {
		start := time.Now()
		event, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			s.logger.WithError(err).Error("Failed to receive event")
			if s.metrics != nil {
				s.metrics.GRPCRequests.WithLabelValues("StreamEvents", "error").Inc()
			}
			return status.Error(codes.Internal, "failed to receive event")
		}

		// Track events ingested
		if s.metrics != nil {
			s.metrics.EventsIngested.WithLabelValues(event.Source, "received").Add(float64(len(event.Events)))
		}

		// Convert proto event to validation.BatchedEvents
		batch := &validation.BatchedEvents{
			BatchID:   event.BatchId,
			Source:    event.Source,
			Timestamp: event.Timestamp.AsTime(),
			Events:    make([]validation.BaseEvent, len(event.Events)),
		}

		for i, e := range event.Events {
			// Build data map from typed event data
			data := make(map[string]interface{})

			// First, populate from batch-level metadata (contains original data fields)
			for key, value := range event.Metadata {
				// Try to convert string values back to appropriate types
				if num, err := strconv.ParseFloat(value, 64); err == nil {
					// Check if it's actually an integer
					if num == float64(int64(num)) {
						data[key] = int64(num)
					} else {
						data[key] = num
					}
				} else if value == "true" {
					data[key] = true
				} else if value == "false" {
					data[key] = false
				} else {
					data[key] = value
				}
			}

			// Then add/override with typed event data if available
			if e.EventData != nil {
				switch eventData := e.EventData.(type) {
				case *pb.EventData_StreamIngestData:
					data["stream_key"] = eventData.StreamIngestData.StreamKey
					data["protocol"] = eventData.StreamIngestData.Protocol
					data["ingest_url"] = eventData.StreamIngestData.IngestUrl
					if eventData.StreamIngestData.Encoder != nil {
						data["encoder"] = *eventData.StreamIngestData.Encoder
					}
				case *pb.EventData_StreamViewData:
					data["viewer_ip"] = eventData.StreamViewData.ViewerIp
					data["user_agent"] = eventData.StreamViewData.UserAgent
					if eventData.StreamViewData.Referrer != nil {
						data["referrer"] = *eventData.StreamViewData.Referrer
					}
				case *pb.EventData_LoadBalancingData:
					data["selected_node"] = eventData.LoadBalancingData.SelectedNode
					data["latitude"] = eventData.LoadBalancingData.Latitude
					data["longitude"] = eventData.LoadBalancingData.Longitude
					data["status"] = eventData.LoadBalancingData.Status
					data["details"] = eventData.LoadBalancingData.Details
					data["score"] = eventData.LoadBalancingData.Score
					data["client_ip"] = eventData.LoadBalancingData.ClientIp
					data["client_country"] = eventData.LoadBalancingData.ClientCountry
				case *pb.EventData_NodeMonitoringData:
					data["cpu_load"] = eventData.NodeMonitoringData.CpuLoad
					data["memory_used"] = eventData.NodeMonitoringData.MemoryUsed
					data["memory_total"] = eventData.NodeMonitoringData.MemoryTotal
					data["active_streams"] = eventData.NodeMonitoringData.ActiveStreams
				case *pb.EventData_StreamLifecycleData:
					data["state"] = eventData.StreamLifecycleData.State.String()
					if eventData.StreamLifecycleData.Reason != nil {
						data["reason"] = *eventData.StreamLifecycleData.Reason
					}
				case *pb.EventData_UserConnectionData:
					// Convert protobuf enum to expected string values for validation
					var actionStr string
					switch eventData.UserConnectionData.Action {
					case pb.UserConnectionData_ACTION_CONNECT:
						actionStr = "connect"
					case pb.UserConnectionData_ACTION_DISCONNECT:
						actionStr = "disconnect"
					default:
						actionStr = "unknown"
					}
					data["action"] = actionStr
					if eventData.UserConnectionData.DisconnectReason != nil {
						data["disconnect_reason"] = *eventData.UserConnectionData.DisconnectReason
					}
				case *pb.EventData_StreamMetricsData:
					data["bandwidth_bps"] = eventData.StreamMetricsData.BandwidthBps
					data["viewer_count"] = eventData.StreamMetricsData.ViewerCount
					if eventData.StreamMetricsData.CpuUsage != nil {
						data["cpu_usage"] = *eventData.StreamMetricsData.CpuUsage
					}
					if eventData.StreamMetricsData.MemoryBytes != nil {
						data["memory_bytes"] = *eventData.StreamMetricsData.MemoryBytes
					}
					if eventData.StreamMetricsData.PacketLoss != nil {
						data["packet_loss"] = *eventData.StreamMetricsData.PacketLoss
					}
				case *pb.EventData_ClientLifecycleData:
					data["action"] = eventData.ClientLifecycleData.Action
					data["client_ip"] = eventData.ClientLifecycleData.ClientIp
					if eventData.ClientLifecycleData.ClientCountry != nil {
						data["client_country"] = *eventData.ClientLifecycleData.ClientCountry
					}
					if eventData.ClientLifecycleData.ClientCity != nil {
						data["client_city"] = *eventData.ClientLifecycleData.ClientCity
					}
					if eventData.ClientLifecycleData.ClientLatitude != nil {
						data["client_latitude"] = *eventData.ClientLifecycleData.ClientLatitude
					}
					if eventData.ClientLifecycleData.ClientLongitude != nil {
						data["client_longitude"] = *eventData.ClientLifecycleData.ClientLongitude
					}
				}
			}

			batch.Events[i] = validation.BaseEvent{
				EventID:       e.EventId,
				EventType:     mapProtoEventTypeToValidation(e.EventType),
				Timestamp:     e.Timestamp.AsTime(),
				Source:        e.Source,
				StreamID:      e.StreamId,
				UserID:        e.UserId,
				PlaybackID:    e.PlaybackId,
				InternalName:  e.InternalName,
				Region:        e.Region,
				NodeURL:       e.NodeUrl,
				Data:          data,
				SchemaVersion: "1.0",
			}

			// Populate missing typed fields from data payload for compatibility
			be := &batch.Events[i]
			if be.InternalName == nil {
				if v, ok := be.Data["internal_name"].(string); ok && v != "" {
					be.InternalName = &v
				} else if v, ok := be.Data["stream_name"].(string); ok && v != "" {
					normalized := v
					if idx := strings.Index(v, "+"); idx != -1 && idx+1 < len(v) {
						normalized = v[idx+1:]
					}
					be.InternalName = &normalized
				}
			}
			if be.PlaybackID == nil {
				if v, ok := be.Data["playback_id"].(string); ok && v != "" {
					be.PlaybackID = &v
				}
			}
			if be.UserID == nil {
				if v, ok := be.Data["user_id"].(string); ok && v != "" {
					be.UserID = &v
				}
			}
		}

		// Validate event batch
		if err := s.validator.ValidateBatch(batch); err != nil {
			s.logger.WithError(err).Error("Event validation failed")
			if s.metrics != nil {
				s.metrics.EventsIngested.WithLabelValues(event.Source, "validation_error").Add(float64(len(event.Events)))
			}
			if err := stream.Send(&pb.EventResponse{
				Status:  "error",
				Message: err.Error(),
			}); err != nil {
				return err
			}
			continue
		}

		// Publish each event as an individual Kafka record using PublishBatch
		batchEnvelope := map[string]interface{}{
			"batch_id":  event.BatchId,
			"source":    event.Source,
			"tenant_id": event.TenantId,
		}
		var evts []map[string]interface{}
		for _, e := range event.Events {
			// Build data payload from typed event data
			data := make(map[string]interface{})
			if e.EventData != nil {
				switch eventData := e.EventData.(type) {
				case *pb.EventData_StreamIngestData:
					data["stream_key"] = eventData.StreamIngestData.StreamKey
					data["protocol"] = eventData.StreamIngestData.Protocol
					data["ingest_url"] = eventData.StreamIngestData.IngestUrl
					if eventData.StreamIngestData.Encoder != nil {
						data["encoder"] = *eventData.StreamIngestData.Encoder
					}
				case *pb.EventData_StreamViewData:
					data["viewer_ip"] = eventData.StreamViewData.ViewerIp
					data["user_agent"] = eventData.StreamViewData.UserAgent
					if eventData.StreamViewData.Referrer != nil {
						data["referrer"] = *eventData.StreamViewData.Referrer
					}
				case *pb.EventData_LoadBalancingData:
					data["selected_node"] = eventData.LoadBalancingData.SelectedNode
					data["latitude"] = eventData.LoadBalancingData.Latitude
					data["longitude"] = eventData.LoadBalancingData.Longitude
					data["status"] = eventData.LoadBalancingData.Status
					data["details"] = eventData.LoadBalancingData.Details
					data["score"] = eventData.LoadBalancingData.Score
					data["client_ip"] = eventData.LoadBalancingData.ClientIp
					data["client_country"] = eventData.LoadBalancingData.ClientCountry
				}
			}
			if e.StreamId != nil {
				data["stream_id"] = *e.StreamId
			}
			if e.UserId != nil {
				data["user_id"] = *e.UserId
			}
			if e.PlaybackId != nil {
				data["playback_id"] = *e.PlaybackId
			}
			if e.InternalName != nil {
				data["internal_name"] = *e.InternalName
			}
			if e.Region != "" {
				data["region"] = e.Region
			}
			if e.NodeUrl != nil {
				data["node_url"] = *e.NodeUrl
			}
			// Map to both id/type and event_id/event_type for downstream compatibility
			m := map[string]interface{}{
				"id":             e.EventId,
				"type":           string(mapProtoEventTypeToValidation(e.EventType)),
				"event_id":       e.EventId,
				"event_type":     string(mapProtoEventTypeToValidation(e.EventType)),
				"timestamp":      e.Timestamp.AsTime(),
				"source":         e.Source,
				"schema_version": "1.0",
				"data":           data,
			}
			evts = append(evts, m)
		}
		batchEnvelope["events"] = evts

		// Track Kafka publishing
		kafkaStart := time.Now()
		if s.metrics != nil {
			s.metrics.KafkaMessages.WithLabelValues("analytics_events", "publish", "attempt").Inc()
		}

		if err := s.producer.PublishBatch(batchEnvelope); err != nil {
			s.logger.WithError(err).Error("Failed to publish events to Kafka")
			if s.metrics != nil {
				s.metrics.KafkaMessages.WithLabelValues("analytics_events", "publish", "error").Inc()
				s.metrics.EventsIngested.WithLabelValues(event.Source, "kafka_error").Add(float64(len(event.Events)))
			}
			if err := stream.Send(&pb.EventResponse{Status: "error", Message: "failed to publish events"}); err != nil {
				return err
			}
			continue
		}

		// Track successful Kafka publishing
		if s.metrics != nil {
			s.metrics.KafkaMessages.WithLabelValues("analytics_events", "publish", "success").Inc()
			s.metrics.KafkaDuration.WithLabelValues("analytics_events").Observe(time.Since(kafkaStart).Seconds())
		}

		s.logger.WithFields(logging.Fields{
			"batch_id": event.BatchId,
			"source":   event.Source,
			"count":    len(event.Events),
		}).Info("Published analytics events to Kafka")

		// Track successful processing
		if s.metrics != nil {
			s.metrics.EventsIngested.WithLabelValues(event.Source, "processed").Add(float64(len(event.Events)))
			s.metrics.ProcessingDuration.WithLabelValues(event.Source).Observe(time.Since(start).Seconds())
			s.metrics.GRPCRequests.WithLabelValues("StreamEvents", "success").Inc()
		}

		// Send success response
		if err := stream.Send(&pb.EventResponse{
			Status:         "success",
			ProcessedCount: uint32(len(event.Events)),
		}); err != nil {
			return err
		}
	}
}

// SendEvent handles single events from Foghorn (replaces SendBalancingEvent)
func (s *DecklogServer) SendEvent(ctx context.Context, event *pb.Event) (*pb.EventResponse, error) {
	start := time.Now()

	// Track gRPC request
	if s.metrics != nil {
		s.metrics.GRPCRequests.WithLabelValues("SendEvent", "requested").Inc()
	}

	// Process single event the same way as batched events
	if len(event.Events) == 0 {
		if s.metrics != nil {
			s.metrics.GRPCRequests.WithLabelValues("SendEvent", "empty_batch").Inc()
		}
		return &pb.EventResponse{
			Status:  "error",
			Message: "no events in batch",
		}, nil
	}

	// Take the first (and should be only) event
	e := event.Events[0]

	// Build data from typed event data
	data := make(map[string]interface{})
	if e.EventData != nil {
		switch eventData := e.EventData.(type) {
		case *pb.EventData_LoadBalancingData:
			data["selected_node"] = eventData.LoadBalancingData.SelectedNode
			data["latitude"] = eventData.LoadBalancingData.Latitude
			data["longitude"] = eventData.LoadBalancingData.Longitude
			data["status"] = eventData.LoadBalancingData.Status
			data["details"] = eventData.LoadBalancingData.Details
			data["score"] = eventData.LoadBalancingData.Score
			data["client_ip"] = eventData.LoadBalancingData.ClientIp
			data["client_country"] = eventData.LoadBalancingData.ClientCountry
		}
	}

	// Add event metadata to data
	if e.StreamId != nil {
		data["stream_id"] = *e.StreamId
	}
	data["tenant_id"] = event.TenantId

	// Convert to Kafka event format
	kafkaEvent := &kafka.Event{
		ID:        e.EventId,
		Type:      "load-balancing",
		Data:      data,
		Timestamp: e.Timestamp.AsTime(),
	}

	// Convert to JSON for Kafka
	value, err := json.Marshal(kafkaEvent)
	if err != nil {
		s.logger.WithError(err).Error("Failed to marshal event")
		return nil, status.Error(codes.Internal, "failed to marshal event")
	}

	// Track Kafka publishing
	if s.metrics != nil {
		s.metrics.KafkaMessages.WithLabelValues("analytics_events", "publish", "single_event").Inc()
		s.metrics.EventsIngested.WithLabelValues("foghorn", "received").Inc()
	}

	kafkaStart := time.Now()
	// Publish to main analytics topic with tenant header
	if err := s.producer.ProduceMessage("analytics_events", []byte(kafkaEvent.ID), value, map[string]string{
		"source":     "foghorn",
		"event_type": "load-balancing",
		"tenant_id":  event.TenantId,
	}); err != nil {
		s.logger.WithError(err).Error("Failed to publish balancing event")
		if s.metrics != nil {
			s.metrics.GRPCRequests.WithLabelValues("SendEvent", "kafka_error").Inc()
			s.metrics.EventsIngested.WithLabelValues("foghorn", "kafka_error").Inc()
		}
		return nil, status.Error(codes.Internal, "failed to publish event")
	}

	// Track successful processing
	if s.metrics != nil {
		s.metrics.KafkaDuration.WithLabelValues("analytics_events").Observe(time.Since(kafkaStart).Seconds())
		s.metrics.EventsIngested.WithLabelValues("foghorn", "processed").Inc()
		s.metrics.ProcessingDuration.WithLabelValues("foghorn").Observe(time.Since(start).Seconds())
		s.metrics.GRPCRequests.WithLabelValues("SendEvent", "success").Inc()
	}

	return &pb.EventResponse{
		Status:         "success",
		ProcessedCount: 1,
	}, nil
}

// CheckHealth implements health check
func (s *DecklogServer) CheckHealth(ctx context.Context, req *pb.HealthRequest) (*pb.HealthResponse, error) {
	return &pb.HealthResponse{
		Status:    "healthy",
		Version:   "1.0.0",
		Timestamp: timestamppb.Now(),
	}, nil
}

// NewGRPCServer creates a new gRPC server with proper TLS configuration
func NewGRPCServer(producer kafka.ProducerInterface, logger logging.Logger, metrics *DecklogMetrics, certFile, keyFile string, allowInsecure bool) (*grpc.Server, error) {
	var opts []grpc.ServerOption

	if !allowInsecure {
		// Load TLS credentials
		creds, err := credentials.NewServerTLSFromFile(certFile, keyFile)
		if err != nil {
			return nil, err
		}
		opts = append(opts, grpc.Creds(creds))
	}

	// Add interceptors for logging, metrics, etc.
	opts = append(opts, grpc.UnaryInterceptor(unaryInterceptor(logger)))
	opts = append(opts, grpc.StreamInterceptor(streamInterceptor(logger)))

	server := grpc.NewServer(opts...)
	pb.RegisterDecklogServiceServer(server, NewDecklogServer(producer, logger, metrics))
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
		}).Info("gRPC request processed")
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
		}).Info("gRPC stream processed")
		return err
	}
}
