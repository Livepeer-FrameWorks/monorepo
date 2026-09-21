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
	errDomainHandled    = errors.New("domain handler")
	errRawHandled       = errors.New("raw trigger handler")
)

func productionIngestTopics() ingestTopics {
	return ingestTopics{
		analytics:     topology.TopicAnalyticsEvents,
		serviceEvents: topology.TopicServiceEvents,
		domainEvents:  topology.TopicDomainEvents,
		rawTriggers:   topology.TopicRawMistTriggers,
	}
}

func distinctIngestHandlers() ingestHandlers {
	return ingestHandlers{
		analytics:     func(context.Context, kafka.Message) error { return errAnalyticsHandled },
		serviceEvents: func(context.Context, kafka.Message) error { return errServiceHandled },
		domainEvents:  func(context.Context, kafka.Message) error { return errDomainHandled },
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
	got := registerIngestSubscriptions(reg, subs, []string{"us-east", "ap-tokyo"})

	want := []string{
		"analytics_events",
		"service_events",
		"domain.events",
		"analytics.raw_mist_triggers",
		"us-east.analytics_events",
		"us-east.service_events",
		"us-east.domain.events",
		"us-east.analytics.raw_mist_triggers",
		"ap-tokyo.analytics_events",
		"ap-tokyo.service_events",
		"ap-tokyo.domain.events",
		"ap-tokyo.analytics.raw_mist_triggers",
	}
	if !slices.Equal(got, want) || !slices.Equal(reg.topics, want) {
		t.Fatalf("registered topics = %v (registrar %v), want %v", got, reg.topics, want)
	}

	for topic, wantErr := range map[string]error{
		"us-east.analytics_events":             errAnalyticsHandled,
		"us-east.service_events":               errServiceHandled,
		"domain.events":                        errDomainHandled,
		"us-east.domain.events":                errDomainHandled,
		"ap-tokyo.domain.events":               errDomainHandled,
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
		"periscope-ingest-domain":                       "dlq",
		"periscope-ingest-domain-mirror:us-east":        "dlq",
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
		want := []string{"analytics_events", "service_events", "domain.events", "us-east.analytics_events", "us-east.service_events", "us-east.domain.events"}
		if !slices.Equal(got, want) {
			t.Fatalf("raw topic %q registered %v, want %v", raw, got, want)
		}
	}
}

func TestDLQHeadersLiftDomainEventType(t *testing.T) {
	domain := kafka.Message{Topic: "us-east.domain.events", Headers: map[string]string{
		"ce_type": "clip.ready", "ce_id": "0190f5c2-7b1e-7cc0-9a51-2f0c3b8d4e61",
		"tenant_id": "5b0a1d2c-3e4f-4a5b-8c6d-7e8f9a0b1c2d",
	}}
	got := dlqHeaders("periscope-ingest-domain-mirror:us-east", domain)
	want := map[string]string{
		"source": "periscope-ingest-domain-mirror:us-east", "original_topic": "us-east.domain.events",
		"event_type": "clip.ready", "tenant_id": "5b0a1d2c-3e4f-4a5b-8c6d-7e8f9a0b1c2d",
		"ce_id": "0190f5c2-7b1e-7cc0-9a51-2f0c3b8d4e61",
	}
	if len(got) != len(want) {
		t.Fatalf("domain DLQ headers = %v, want %v", got, want)
	}
	for key, value := range want {
		if got[key] != value {
			t.Fatalf("domain DLQ header %s = %q, want %q (all: %v)", key, got[key], value, got)
		}
	}

	legacy := dlqHeaders("periscope-ingest-service", kafka.Message{Topic: "service_events", Headers: map[string]string{
		"event_type": "stream_created", "ce_type": "stream.created",
	}})
	if legacy["event_type"] != "stream_created" {
		t.Fatalf("an event_type header must win over ce_type, got %v", legacy)
	}
}
