package main

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/topology"
)

type recordingRegistrar struct {
	topics   []string
	handlers map[string]kafka.Handler
}

func (r *recordingRegistrar) AddHandler(topic string, handler kafka.Handler) {
	if r.handlers == nil {
		r.handlers = make(map[string]kafka.Handler)
	}
	r.topics = append(r.topics, topic)
	r.handlers[topic] = handler
}

var (
	errAnalyticsHandled = errors.New("analytics handler")
	errServiceHandled   = errors.New("service handler")
	errRawHandled       = errors.New("raw trigger handler")
)

func productionIngestTopics() ingestTopics {
	return ingestTopics{
		analytics:     topology.TopicAnalyticsEvents,
		serviceEvents: topology.TopicServiceEvents,
		rawTriggers:   topology.TopicRawMistTriggers,
	}
}

func distinctIngestHandlers() ingestHandlers {
	return ingestHandlers{
		analytics:     func(context.Context, kafka.Message) error { return errAnalyticsHandled },
		serviceEvents: func(context.Context, kafka.Message) error { return errServiceHandled },
		rawTriggers:   func(context.Context, kafka.Message) error { return errRawHandled },
	}
}

func TestRegisterIngestSubscriptionsConsumesMirroredRawTriggersRetryOnly(t *testing.T) {
	policy := map[string]string{}
	withDLQ := func(name string, handler messageHandler) messageHandler {
		policy[name] = "dlq"
		return handler
	}
	retryOnly := func(name string, handler messageHandler) messageHandler {
		policy[name] = "retry"
		return handler
	}

	reg := &recordingRegistrar{}
	subs := ingestSubscriptions(productionIngestTopics(), distinctIngestHandlers(), withDLQ, retryOnly)
	got := registerIngestSubscriptions(reg, subs, splitMirrorPrefixes(" us-east, ,ap-tokyo "))

	want := []string{
		"analytics_events",
		"service_events",
		"analytics.raw_mist_triggers",
		"us-east.analytics_events",
		"us-east.service_events",
		"us-east.analytics.raw_mist_triggers",
		"ap-tokyo.analytics_events",
		"ap-tokyo.service_events",
		"ap-tokyo.analytics.raw_mist_triggers",
	}
	if !slices.Equal(got, want) || !slices.Equal(reg.topics, want) {
		t.Fatalf("registered topics = %v (registrar %v), want %v", got, reg.topics, want)
	}

	for topic, wantErr := range map[string]error{
		"us-east.analytics_events":             errAnalyticsHandled,
		"us-east.service_events":               errServiceHandled,
		"us-east.analytics.raw_mist_triggers":  errRawHandled,
		"ap-tokyo.analytics.raw_mist_triggers": errRawHandled,
	} {
		if err := reg.handlers[topic](context.Background(), kafka.Message{Topic: topic}); !errors.Is(err, wantErr) {
			t.Fatalf("handler for %s returned %v, want %v", topic, err, wantErr)
		}
	}

	for name, wantPolicy := range map[string]string{
		"periscope-ingest-raw-triggers":                 "retry",
		"periscope-ingest-raw-triggers-mirror:us-east":  "retry",
		"periscope-ingest-raw-triggers-mirror:ap-tokyo": "retry",
		"periscope-ingest-analytics-mirror:us-east":     "dlq",
		"periscope-ingest-service-mirror:ap-tokyo":      "dlq",
	} {
		if policy[name] != wantPolicy {
			t.Fatalf("consumer %s policy = %q, want %q", name, policy[name], wantPolicy)
		}
	}
}

func TestIngestSubscriptionsDisabledRawJournalHasNoMirror(t *testing.T) {
	identity := func(_ string, handler messageHandler) messageHandler { return handler }
	for _, raw := range []string{"", "-", "  "} {
		topics := productionIngestTopics()
		topics.rawTriggers = raw
		reg := &recordingRegistrar{}
		got := registerIngestSubscriptions(reg, ingestSubscriptions(topics, distinctIngestHandlers(), identity, identity), []string{"us-east"})
		want := []string{"analytics_events", "service_events", "us-east.analytics_events", "us-east.service_events"}
		if !slices.Equal(got, want) {
			t.Fatalf("raw topic %q registered %v, want %v", raw, got, want)
		}
	}
}

func TestSplitMirrorPrefixes(t *testing.T) {
	if got := splitMirrorPrefixes(""); len(got) != 0 {
		t.Fatalf("splitMirrorPrefixes(\"\") = %v, want empty", got)
	}
	if got := splitMirrorPrefixes("us-east, eu-west,,"); !slices.Equal(got, []string{"us-east", "eu-west"}) {
		t.Fatalf("splitMirrorPrefixes = %v", got)
	}
}
