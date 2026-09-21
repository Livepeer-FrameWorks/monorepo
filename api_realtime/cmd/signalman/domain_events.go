package main

import (
	"context"
	"errors"
	"fmt"

	"frameworks/api_realtime/internal/metrics"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	signalmanpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/signalman"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/topology"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Reasons a domain event is withheld from tenant subscribers.
const (
	dropNotPublic        = "not_public"
	dropUnknownType      = "unknown_type"
	dropRegistryInternal = "registry_internal"
	dropNoTenant         = "no_tenant"
)

// tenantEventHub is the hub operation domain events are delivered through.
type tenantEventHub interface {
	BroadcastToTenant(tenantID string, eventType signalmanpb.EventType, channel signalmanpb.Channel, data *signalmanpb.EventData)
}

// handlerRegistrar is the consumer operation that subscribes a topic.
type handlerRegistrar interface {
	AddHandler(topic string, handler kafka.Handler)
}

// registerDomainEventSubscriptions subscribes handler to the local
// domain.events topic and to the copy MirrorMaker2 writes for each region
// prefix, and returns the registered topics. Every copy shares handler, so its
// event-ID window spans them.
func registerDomainEventSubscriptions(reg handlerRegistrar, prefixes []string, handler kafka.Handler, wrap func(string, kafka.Handler) kafka.Handler) []string {
	topics := []string{topology.TopicDomainEvents}
	reg.AddHandler(topology.TopicDomainEvents, wrap("signalman-domain", handler))
	for _, prefix := range prefixes {
		topic := topology.MirroredTopicName(prefix, topology.TopicDomainEvents)
		reg.AddHandler(topic, wrap("signalman-domain-mirror:"+prefix, handler))
		topics = append(topics, topic)
	}
	return topics
}

// newDomainEventHandler delivers public domain events to the subscribers of
// their tenant on CHANNEL_EVENTS, once per event ID within the window.
//
// A record whose ce_visibility header is not public is dropped before its
// payload is decoded. A record that claims to be public is delivered only if
// this binary's registry also registers its type as public and it names a
// tenant, so an internal event never reaches a tenant even with a wrong header.
// A type this binary does not register is dropped: it cannot be checked. An
// undecodable record returns an error for the DLQ wrapper.
//
// The window is separate from the one the legacy topics use: while producers
// dual-write, a legacy service event carries the same ID as its domain event,
// and each belongs on its own channel.
func newDomainEventHandler(hub tenantEventHub, window *eventIDWindow, m *metrics.Metrics, logger logging.Logger) kafka.Handler {
	return func(_ context.Context, msg kafka.Message) error {
		if msg.Headers[events.HeaderVisibility] != events.VisibilityPublic {
			m.RecordDomainEventDropped(dropNotPublic)
			return nil
		}
		headers := make([]events.Header, 0, len(msg.Headers))
		for key, value := range msg.Headers {
			headers = append(headers, events.Header{Key: key, Value: []byte(value)})
		}
		rec, err := events.ParseRecord(msg.Key, headers, msg.Value)
		if err != nil {
			if errors.Is(err, events.ErrUnknownType) {
				m.RecordDomainEventDropped(dropUnknownType)
				logger.WithError(err).WithFields(logging.Fields{
					"event_type": msg.Headers[events.HeaderType],
					"topic":      msg.Topic,
				}).Warn("Dropping domain event of a type this Signalman does not register")
				return nil
			}
			return fmt.Errorf("parse domain event: %w", err)
		}
		if !rec.Spec.Public() {
			m.RecordDomainEventDropped(dropRegistryInternal)
			logger.WithFields(logging.Fields{
				"event_type": rec.Type,
				"event_id":   rec.ID,
				"topic":      msg.Topic,
			}).Error("Dropping domain event whose visibility header says public but whose registered type is internal")
			return nil
		}
		if rec.TenantID == "" {
			m.RecordDomainEventDropped(dropNoTenant)
			return nil
		}
		if window.seen(rec.ID) {
			m.RecordDuplicateEvent(msg.Topic)
			return nil
		}
		data, err := anypb.New(rec.Message)
		if err != nil {
			return fmt.Errorf("pack %s: %w", rec.Type, err)
		}
		hub.BroadcastToTenant(rec.TenantID, signalmanpb.EventType_EVENT_TYPE_TENANT_EVENT, signalmanpb.Channel_CHANNEL_EVENTS,
			&signalmanpb.EventData{Payload: &signalmanpb.EventData_TenantEvent{TenantEvent: &signalmanpb.TenantEvent{
				Id:      rec.ID,
				Type:    rec.Type,
				Time:    timestamppb.New(rec.Time),
				Subject: rec.Subject,
				Data:    data,
			}}})
		return nil
	}
}
