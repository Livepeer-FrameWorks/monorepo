package main

import (
	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/topology"
	"strings"
)

type gatewayRegistrar interface {
	AddHandler(string, kafka.Handler)
}

func registerGatewaySubscriptions(consumer gatewayRegistrar, handler kafka.Handler, prefixes []string) {
	consumer.AddHandler(topology.TopicAnalyticsEvents, handler)
	seen := map[string]bool{topology.TopicAnalyticsEvents: true}
	for _, prefix := range prefixes {
		prefix = strings.TrimSpace(prefix)
		if prefix == "" {
			continue
		}
		topic := topology.MirroredTopicName(prefix, topology.TopicAnalyticsEvents)
		if !seen[topic] {
			consumer.AddHandler(topic, handler)
			seen[topic] = true
		}
	}
}
