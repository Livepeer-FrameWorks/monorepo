package grpc

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	eventspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/topology"
	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// maxDomainEventBatch bounds one PublishDomainEvents call. Producer relays
// claim at most 100 rows per batch.
const maxDomainEventBatch = 500

// domainEventsProduceTimeout bounds the wait for Kafka's acknowledgment of a
// whole batch.
const domainEventsProduceTimeout = 10 * time.Second

// PublishDomainEvents validates every event of the batch, produces all of
// them to domain.events keyed "<aggregate>/<aggregate_id>", and returns only
// after Kafka acknowledged every record. Nothing is produced when any event is
// rejected, so the producer's retry never sees a partly written batch caused
// by validation. Decklog never assigns an event ID: the producer's stored ID
// is the consumers' dedup key.
func (s *DecklogServer) PublishDomainEvents(ctx context.Context, batch *eventspb.DomainEventBatch) (*eventspb.PublishDomainEventsResponse, error) {
	start := time.Now()
	s.countRequest("PublishDomainEvents", "requested")
	envs := batch.GetEvents()
	if len(envs) == 0 {
		s.countRequest("PublishDomainEvents", "invalid")
		return nil, status.Error(codes.InvalidArgument, "domain event batch is empty")
	}
	if len(envs) > maxDomainEventBatch {
		s.countRequest("PublishDomainEvents", "invalid")
		return nil, status.Errorf(codes.InvalidArgument, "domain event batch has %d events, the limit is %d", len(envs), maxDomainEventBatch)
	}

	records := make([]*kgo.Record, 0, len(envs))
	for i, env := range envs {
		if env == nil {
			s.countRequest("PublishDomainEvents", "invalid")
			return nil, status.Errorf(codes.InvalidArgument, "event %d is nil", i)
		}
		stamped := proto.CloneOf(env)
		if stamped.SourceRegion == "" {
			stamped.SourceRegion = s.sourceRegion
		}
		if stamped.SourceClusterId == "" {
			stamped.SourceClusterId = s.sourceClusterID
		}
		key, headers, value, err := events.EncodeRecord(stamped)
		if err != nil {
			code := codes.InvalidArgument
			if errors.Is(err, events.ErrUnknownType) {
				code = codes.FailedPrecondition
			}
			label := env.GetType()
			if _, known := events.Lookup(label); !known {
				label = "unregistered"
			}
			s.countEvent(label, "rejected")
			s.countRequest("PublishDomainEvents", "invalid")
			return nil, status.Errorf(code, "event %d (%s %s): %v", i, env.GetType(), env.GetId(), err)
		}
		record := &kgo.Record{Topic: topology.TopicDomainEvents, Key: key, Value: value}
		for _, h := range headers {
			record.Headers = append(record.Headers, kgo.RecordHeader{Key: h.Key, Value: h.Value})
		}
		records = append(records, record)
	}

	produceCtx, cancel := context.WithTimeout(ctx, domainEventsProduceTimeout)
	defer cancel()
	kafkaStart := time.Now()
	if err := s.producer.ProduceRecords(produceCtx, records); err != nil {
		s.countRequest("PublishDomainEvents", "kafka_error")
		if s.metrics != nil && s.metrics.KafkaMessages != nil {
			s.metrics.KafkaMessages.WithLabelValues(topology.TopicDomainEvents, "publish", "error").Add(float64(len(records)))
		}
		s.logger.WithError(err).WithField("events", len(records)).Warn("Domain event batch was not acknowledged by Kafka")
		return nil, status.Error(codes.Unavailable, fmt.Sprintf("publish domain events: %v", err))
	}

	for _, env := range envs {
		s.countEvent(env.GetType(), "processed")
	}
	s.countRequest("PublishDomainEvents", "success")
	if s.metrics != nil {
		if s.metrics.KafkaMessages != nil {
			s.metrics.KafkaMessages.WithLabelValues(topology.TopicDomainEvents, "publish", "success").Add(float64(len(records)))
		}
		if s.metrics.KafkaDuration != nil {
			s.metrics.KafkaDuration.WithLabelValues("publish").Observe(time.Since(kafkaStart).Seconds())
		}
	}
	s.logger.WithFields(logging.Fields{
		"events":   len(records),
		"duration": time.Since(start),
	}).Debug("Domain events published")
	return &eventspb.PublishDomainEventsResponse{Published: uint32(len(records))}, nil
}

func (s *DecklogServer) countRequest(method, result string) {
	if s.metrics != nil && s.metrics.GRPCRequests != nil {
		s.metrics.GRPCRequests.WithLabelValues(method, result).Inc()
	}
}

func (s *DecklogServer) countEvent(eventType, result string) {
	if s.metrics != nil && s.metrics.EventsIngested != nil {
		s.metrics.EventsIngested.WithLabelValues(eventType, result).Inc()
	}
}
