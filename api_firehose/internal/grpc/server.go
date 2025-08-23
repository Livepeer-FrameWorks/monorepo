package grpc

import (
	"context"
	"io"
	"time"

	"frameworks/pkg/logging"

	"github.com/prometheus/client_golang/prometheus"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"frameworks/pkg/geoip"
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

// convertProtoEventToAnalytics converts a protobuf EventData to kafka.AnalyticsEvent
func convertProtoEventToAnalytics(protoEvent *pb.EventData, _ string, batchTenantID string) (*kafka.AnalyticsEvent, error) {
	event := &kafka.AnalyticsEvent{
		EventID:       protoEvent.EventId,
		EventType:     string(mapProtoEventTypeToValidation(protoEvent.EventType)),
		Timestamp:     protoEvent.Timestamp.AsTime(),
		Source:        protoEvent.Source,
		TenantID:      batchTenantID,
		SchemaVersion: protoEvent.SchemaVersion,
		Region:        protoEvent.Region,
	}

	// Set optional string fields
	if protoEvent.StreamId != nil {
		event.StreamID = protoEvent.StreamId
	}
	if protoEvent.UserId != nil {
		event.UserID = protoEvent.UserId
	}
	if protoEvent.PlaybackId != nil {
		event.PlaybackID = protoEvent.PlaybackId
	}
	if protoEvent.InternalName != nil {
		event.InternalName = protoEvent.InternalName
	}
	if protoEvent.NodeUrl != nil {
		event.NodeURL = protoEvent.NodeUrl
	}

	// Populate typed payloads from protobuf oneof
	switch protoEvent.EventType {
	case pb.EventType_EVENT_TYPE_LOAD_BALANCING:
		lb := protoEvent.GetLoadBalancingData()
		if lb != nil {
			event.Data.LoadBalancing = &validation.LoadBalancingPayload{
				StreamID:          getOptionalString(protoEvent.StreamId),
				TenantID:          batchTenantID,
				SelectedNode:      lb.GetSelectedNode(),
				SelectedNodeID:    getOptionalString(lb.SelectedNodeId),
				Latitude:          lb.GetLatitude(),
				Longitude:         lb.GetLongitude(),
				Status:            lb.GetStatus(),
				Details:           lb.GetDetails(),
				Score:             lb.GetScore(),
				ClientIP:          lb.GetClientIp(),
				ClientCountry:     lb.GetClientCountry(),
				NodeLatitude:      lb.GetNodeLatitude(),
				NodeLongitude:     lb.GetNodeLongitude(),
				NodeName:          lb.GetNodeName(),
				RoutingDistanceKm: lb.GetRoutingDistanceKm(),
			}
		}
	case pb.EventType_EVENT_TYPE_NODE_LIFECYCLE:
		nm := protoEvent.GetNodeMonitoringData()
		if nm != nil {
			event.Data.NodeLifecycle = &validation.NodeLifecyclePayload{
				NodeID:         getOptionalString(nm.NodeId),
				IsHealthy:      getOptionalBool(nm.IsHealthy),
				GeoData:        geoip.GeoData{CountryCode: getOptionalString(nm.CountryCode), City: getOptionalString(nm.City), Latitude: nm.GetLatitude(), Longitude: nm.GetLongitude()},
				Location:       getOptionalString(nm.Location),
				CPUUsage:       float64(nm.GetCpuLoad()),
				RAMMax:         nm.GetMemoryTotal(),
				RAMCurrent:     nm.GetMemoryUsed(),
				BandwidthUp:    nm.GetNetworkOutBps(),
				BandwidthDown:  nm.GetNetworkInBps(),
				ActiveStreams:  int(nm.GetActiveStreams()),
				BandwidthLimit: getOptionalUint64(nm.BandwidthLimitBps),
			}
		}
	}

	return event, nil
}

func getOptionalString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
func getOptionalBool(p *bool) bool {
	if p == nil {
		return false
	}
	return *p
}
func getOptionalUint64(p *uint64) uint64 {
	if p == nil {
		return 0
	}
	return *p
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

// StreamEvents handles streaming events from Helmsman
func (s *DecklogServer) StreamEvents(stream pb.DecklogService_StreamEventsServer) error {
	// Track gRPC request
	if s.metrics != nil {
		s.metrics.GRPCRequests.WithLabelValues("StreamEvents", "requested").Inc()
	}

	for {
		start := time.Now()
		protoEvent, err := stream.Recv()
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
			s.metrics.EventsIngested.WithLabelValues(protoEvent.Source, "received").Add(float64(len(protoEvent.Events)))
		}

		// Convert protobuf events to typed kafka events
		var analyticsEvents []kafka.AnalyticsEvent
		for _, protoEventData := range protoEvent.Events {
			ke, err := convertProtoEventToAnalytics(protoEventData, protoEvent.Source, protoEvent.TenantId)
			if err != nil {
				s.logger.WithError(err).WithFields(logging.Fields{
					"event_id":   protoEventData.EventId,
					"event_type": protoEventData.EventType,
				}).Error("Failed to convert proto event to analytics event")
				continue
			}
			analyticsEvents = append(analyticsEvents, *ke)
		}

		// Skip empty batches
		if len(analyticsEvents) == 0 {
			if err := stream.Send(&pb.EventResponse{
				Status:  "error",
				Message: "no valid events in batch",
			}); err != nil {
				return err
			}
			continue
		}

		// Validate events using existing validation logic
		// Convert to BatchedEvents for validation compatibility
		batch := &validation.BatchedEvents{
			BatchID:   protoEvent.BatchId,
			Source:    protoEvent.Source,
			Timestamp: protoEvent.Timestamp.AsTime(),
			Events:    make([]validation.BaseEvent, len(analyticsEvents)),
		}

		// Convert AnalyticsEvents to BaseEvents for validation
		for i, analyticsEvent := range analyticsEvents {
			be := validation.BaseEvent{
				EventID:       analyticsEvent.EventID,
				EventType:     validation.EventType(analyticsEvent.EventType),
				Timestamp:     analyticsEvent.Timestamp,
				Source:        analyticsEvent.Source,
				StreamID:      analyticsEvent.StreamID,
				UserID:        analyticsEvent.UserID,
				PlaybackID:    analyticsEvent.PlaybackID,
				InternalName:  analyticsEvent.InternalName,
				NodeURL:       analyticsEvent.NodeURL,
				SchemaVersion: analyticsEvent.SchemaVersion,
			}
			// Map typed payloads into BaseEvent for validator
			switch be.EventType {
			case validation.EventStreamIngest:
				be.StreamIngest = analyticsEvent.Data.StreamIngest
			case validation.EventStreamView:
				be.StreamView = analyticsEvent.Data.StreamView
			case validation.EventStreamLifecycle:
				be.StreamLifecycle = analyticsEvent.Data.StreamLifecycle
			case validation.EventStreamBuffer:
				be.StreamLifecycle = analyticsEvent.Data.StreamLifecycle
			case validation.EventStreamEnd:
				be.StreamLifecycle = analyticsEvent.Data.StreamLifecycle
			case validation.EventUserConnection:
				be.UserConnection = analyticsEvent.Data.UserConnection
			case validation.EventClientLifecycle:
				be.ClientLifecycle = analyticsEvent.Data.ClientLifecycle
			case validation.EventTrackList:
				be.TrackList = analyticsEvent.Data.TrackList
			case validation.EventRecordingLifecycle:
				be.Recording = analyticsEvent.Data.Recording
			case validation.EventPushLifecycle:
				be.PushLifecycle = analyticsEvent.Data.PushLifecycle
			case validation.EventNodeLifecycle:
				be.NodeLifecycle = analyticsEvent.Data.NodeLifecycle
			case validation.EventBandwidthThreshold:
				be.BandwidthThreshold = analyticsEvent.Data.BandwidthThreshold
			case validation.EventLoadBalancing:
				be.LoadBalancing = analyticsEvent.Data.LoadBalancing
			}
			batch.Events[i] = be
		}

		// Validate event batch
		if err := s.validator.ValidateBatch(batch); err != nil {
			s.logger.WithError(err).Error("Event validation failed")
			if s.metrics != nil {
				s.metrics.EventsIngested.WithLabelValues(protoEvent.Source, "validation_error").Add(float64(len(protoEvent.Events)))
			}
			if err := stream.Send(&pb.EventResponse{
				Status:  "error",
				Message: err.Error(),
			}); err != nil {
				return err
			}
			continue
		}

		// Publish typed events to Kafka
		kafkaStart := time.Now()
		if s.metrics != nil {
			s.metrics.KafkaMessages.WithLabelValues("analytics_events", "publish", "attempt").Inc()
		}

		if err := s.producer.PublishTypedBatch(analyticsEvents); err != nil {
			s.logger.WithError(err).Error("Failed to publish typed events to Kafka")
			if s.metrics != nil {
				s.metrics.KafkaMessages.WithLabelValues("analytics_events", "publish", "error").Inc()
				s.metrics.EventsIngested.WithLabelValues(protoEvent.Source, "kafka_error").Add(float64(len(protoEvent.Events)))
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
			"batch_id": protoEvent.BatchId,
			"source":   protoEvent.Source,
			"count":    len(analyticsEvents),
		}).Info("Published typed analytics events to Kafka")

		// Track successful processing
		if s.metrics != nil {
			s.metrics.EventsIngested.WithLabelValues(protoEvent.Source, "processed").Add(float64(len(analyticsEvents)))
			s.metrics.ProcessingDuration.WithLabelValues(protoEvent.Source).Observe(time.Since(start).Seconds())
			s.metrics.GRPCRequests.WithLabelValues("StreamEvents", "success").Inc()
		}

		// Send success response
		if err := stream.Send(&pb.EventResponse{
			Status:         "success",
			ProcessedCount: uint32(len(analyticsEvents)),
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
	protoEventData := event.Events[0]

	// Convert to typed event
	analyticsEvent, err := convertProtoEventToAnalytics(protoEventData, event.Source, event.TenantId)
	if err != nil {
		s.logger.WithError(err).Error("Failed to convert proto event to analytics event")
		if s.metrics != nil {
			s.metrics.GRPCRequests.WithLabelValues("SendEvent", "conversion_error").Inc()
		}
		return &pb.EventResponse{
			Status:  "error",
			Message: "failed to convert event",
		}, nil
	}

	// Track Kafka publishing
	if s.metrics != nil {
		s.metrics.KafkaMessages.WithLabelValues("analytics_events", "publish", "single_event").Inc()
		s.metrics.EventsIngested.WithLabelValues(event.Source, "received").Inc()
	}

	kafkaStart := time.Now()
	// Publish typed event to Kafka
	if err := s.producer.PublishTypedEvent(analyticsEvent); err != nil {
		s.logger.WithError(err).Error("Failed to publish typed event")
		if s.metrics != nil {
			s.metrics.GRPCRequests.WithLabelValues("SendEvent", "kafka_error").Inc()
			s.metrics.EventsIngested.WithLabelValues(event.Source, "kafka_error").Inc()
		}
		return nil, status.Error(codes.Internal, "failed to publish event")
	}

	// Track successful processing
	if s.metrics != nil {
		s.metrics.KafkaDuration.WithLabelValues("analytics_events").Observe(time.Since(kafkaStart).Seconds())
		s.metrics.EventsIngested.WithLabelValues(event.Source, "processed").Inc()
		s.metrics.ProcessingDuration.WithLabelValues(event.Source).Observe(time.Since(start).Seconds())
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
