package grpc

import (
	"context"
	"encoding/json"
	"io"
	"strconv"
	"strings"
	"time"

	"frameworks/pkg/config"
	"frameworks/pkg/logging"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	pb "frameworks/api_firehose/proto"
	"frameworks/pkg/kafka"
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
	default:
		return validation.EventType("unknown")
	}
}

type DecklogServer struct {
	pb.UnimplementedDecklogServiceServer
	producer  kafka.ProducerInterface
	validator *validation.EventValidator
	logger    logging.Logger
}

func NewDecklogServer(producer kafka.ProducerInterface, logger logging.Logger) *DecklogServer {
	return &DecklogServer{
		producer:  producer,
		validator: validation.NewEventValidator(),
		logger:    logger,
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
	for {
		event, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			s.logger.WithError(err).Error("Failed to receive event")
			return status.Error(codes.Internal, "failed to receive event")
		}

		// Convert proto event to validation.BatchedEvents
		batch := &validation.BatchedEvents{
			BatchID:   event.BatchId,
			Source:    event.Source,
			Timestamp: event.Timestamp.AsTime(),
			Events:    make([]validation.BaseEvent, len(event.Events)),
		}

		for i, e := range event.Events {
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
				Data:          convertStringMapToInterface(e.Data),
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
			// Build data payload and merge typed extras
			data := convertStringMapToInterface(e.Data)
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
		if err := s.producer.PublishBatch(batchEnvelope); err != nil {
			s.logger.WithError(err).Error("Failed to publish events to Kafka")
			if err := stream.Send(&pb.EventResponse{Status: "error", Message: "failed to publish events"}); err != nil {
				return err
			}
			continue
		}

		s.logger.WithFields(logging.Fields{
			"batch_id": event.BatchId,
			"source":   event.Source,
			"count":    len(event.Events),
		}).Info("Published analytics events to Kafka")

		// Send success response
		if err := stream.Send(&pb.EventResponse{
			Status:         "success",
			ProcessedCount: uint32(len(event.Events)),
		}); err != nil {
			return err
		}
	}
}

// SendBalancingEvent handles single events from Foghorn
func (s *DecklogServer) SendBalancingEvent(ctx context.Context, event *pb.BalancingEvent) (*pb.EventResponse, error) {
	// Convert to Kafka event format
	kafkaEvent := &kafka.Event{
		ID:   event.EventId,
		Type: "load-balancing",
		Data: map[string]interface{}{
			"internal_name":    event.StreamId,
			"selected_node":    event.SelectedNode,
			"tenant_id":        event.TenantId,
			"client_latitude":  event.Latitude,
			"client_longitude": event.Longitude,
			"status":           event.Status,
			"details":          event.Details,
			"timestamp":        event.Timestamp.AsTime(),
			"client_ip":        event.ClientIp,
			"client_country":   event.ClientCountry,
			"score":            event.Score,
			"region":           event.Region,
		},
		Timestamp: event.Timestamp.AsTime(),
	}

	// Convert to JSON for Kafka
	value, err := json.Marshal(kafkaEvent)
	if err != nil {
		s.logger.WithError(err).Error("Failed to marshal event")
		return nil, status.Error(codes.Internal, "failed to marshal event")
	}

	// Publish to main analytics topic with tenant header
	if err := s.producer.ProduceMessage("analytics_events", []byte(kafkaEvent.ID), value, map[string]string{
		"source":     "foghorn",
		"event_type": "load-balancing",
		"tenant_id":  event.TenantId,
	}); err != nil {
		s.logger.WithError(err).Error("Failed to publish balancing event")
		return nil, status.Error(codes.Internal, "failed to publish event")
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
		Version:   config.GetEnv("VERSION", "1.0.0"),
		Timestamp: timestamppb.Now(),
	}, nil
}

// NewGRPCServer creates a new gRPC server with proper TLS configuration
func NewGRPCServer(producer kafka.ProducerInterface, logger logging.Logger, certFile, keyFile string, allowInsecure bool) (*grpc.Server, error) {
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
	pb.RegisterDecklogServiceServer(server, NewDecklogServer(producer, logger))
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
