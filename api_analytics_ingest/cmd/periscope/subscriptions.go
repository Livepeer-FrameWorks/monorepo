package main

import (
	"context"
	"strings"

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
	rawTriggers   string
}

type ingestHandlers struct {
	analytics     messageHandler
	serviceEvents messageHandler
	rawTriggers   messageHandler
}

// ingestSubscriptions returns the local topics Periscope-Ingest consumes.
// Analytics and service events dead-letter poison messages. Raw final triggers
// retry instead: final facts and metering are projected from them, so a
// ClickHouse outage must never commit an offset past an unprojected final. An
// empty or "-" raw topic disables the raw journal on this instance.
func ingestSubscriptions(topics ingestTopics, handlers ingestHandlers, withDLQ, retryOnly handlerWrapper) []ingestSubscription {
	subs := []ingestSubscription{
		{topic: topics.analytics, name: "periscope-ingest-analytics", handler: handlers.analytics, wrap: withDLQ},
		{topic: topics.serviceEvents, name: "periscope-ingest-service", handler: handlers.serviceEvents, wrap: withDLQ},
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

// splitMirrorPrefixes parses MIRROR_REGION_PREFIXES, a comma-separated list of
// MirrorMaker2 source cluster aliases.
func splitMirrorPrefixes(raw string) []string {
	var prefixes []string
	for prefix := range strings.SplitSeq(raw, ",") {
		if prefix = strings.TrimSpace(prefix); prefix != "" {
			prefixes = append(prefixes, prefix)
		}
	}
	return prefixes
}
