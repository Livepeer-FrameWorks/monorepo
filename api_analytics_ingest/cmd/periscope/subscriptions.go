package main

import (
	"context"
	"strings"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/topology"
)

type messageHandler = func(context.Context, kafka.Message) error

type handlerWrapper = func(consumerName string, handler messageHandler) messageHandler

type handlerRegistrar interface {
	AddHandler(topic string, handler kafka.Handler)
}

// ingestSubscription binds one local topic to its consumer name, handler, and
// failure policy. Mirrored copies of the topic reuse all three.
type ingestSubscription struct {
	topic   string
	name    string
	handler messageHandler
	wrap    handlerWrapper
}

type ingestTopics struct {
	analytics     string
	serviceEvents string
	domainEvents  string
	rawTriggers   string
}

type ingestHandlers struct {
	analytics     messageHandler
	serviceEvents messageHandler
	domainEvents  messageHandler
	rawTriggers   messageHandler
}

// ingestSubscriptions returns the local topics Periscope-Ingest consumes.
// Analytics, service, and domain events dead-letter poison messages; a
// transient dependency failure is retried in place by both wrappers. Raw final
// triggers never dead-letter: final facts and metering are projected from them,
// so a ClickHouse outage must never commit an offset past an unprojected
// final. An empty or "-" raw topic disables the raw journal on this instance.
func ingestSubscriptions(topics ingestTopics, handlers ingestHandlers, withDLQ, retryOnly handlerWrapper) []ingestSubscription {
	subs := []ingestSubscription{
		{topic: topics.analytics, name: "periscope-ingest-analytics", handler: handlers.analytics, wrap: withDLQ},
		{topic: topics.serviceEvents, name: "periscope-ingest-service", handler: handlers.serviceEvents, wrap: withDLQ},
		{topic: topics.domainEvents, name: "periscope-ingest-domain", handler: handlers.domainEvents, wrap: withDLQ},
	}
	if raw := strings.TrimSpace(topics.rawTriggers); raw != "" && raw != "-" {
		subs = append(subs, ingestSubscription{
			topic:   raw,
			name:    "periscope-ingest-raw-triggers",
			handler: handlers.rawTriggers,
			wrap:    retryOnly,
		})
	}
	return subs
}

// dlqHeaders returns the headers of the DLQ record for msg: the consumer and
// original topic, plus the tenant and event type when msg carries them. A
// domain.events record names its type in ce_type and has no event_type header,
// so ce_type is lifted into event_type.
func dlqHeaders(consumerName string, msg kafka.Message) map[string]string {
	headers := map[string]string{
		"source":         consumerName,
		"original_topic": msg.Topic,
	}
	if tenantID, ok := msg.Headers[events.HeaderTenantID]; ok {
		headers["tenant_id"] = tenantID
	}
	if eventType, ok := msg.Headers["event_type"]; ok {
		headers["event_type"] = eventType
	} else if eventType, ok := msg.Headers[events.HeaderType]; ok {
		headers["event_type"] = eventType
	}
	if eventID, ok := msg.Headers[events.HeaderID]; ok {
		headers[events.HeaderID] = eventID
	}
	return headers
}

// registerIngestSubscriptions subscribes to every local topic and to the copy
// MirrorMaker2 writes into the aggregator for each region prefix. Mirrored
// copies are derived from the same subscription list, so every local topic is
// also consumed from every source region. It returns the registered topics.
func registerIngestSubscriptions(reg handlerRegistrar, subs []ingestSubscription, prefixes []string) []string {
	topics := make([]string, 0, len(subs)*(len(prefixes)+1))
	for _, sub := range subs {
		reg.AddHandler(sub.topic, sub.wrap(sub.name, sub.handler))
		topics = append(topics, sub.topic)
	}
	for _, prefix := range prefixes {
		for _, sub := range subs {
			topic := topology.MirroredTopicName(prefix, sub.topic)
			reg.AddHandler(topic, sub.wrap(sub.name+"-mirror:"+prefix, sub.handler))
			topics = append(topics, topic)
		}
	}
	return topics
}
