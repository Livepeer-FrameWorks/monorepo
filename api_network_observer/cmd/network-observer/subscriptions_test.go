package main

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
)

type recordingGatewayRegistrar struct {
	topics   []string
	handlers map[string]kafka.Handler
}

func (r *recordingGatewayRegistrar) AddHandler(topic string, handler kafka.Handler) {
	r.topics = append(r.topics, topic)
	if r.handlers == nil {
		r.handlers = make(map[string]kafka.Handler)
	}
	r.handlers[topic] = handler
}

func TestGatewaySubscriptionsIncludeRegionalMirrorsExactlyOnce(t *testing.T) {
	for _, tc := range []struct {
		name     string
		prefixes []string
		want     []string
	}{
		{"local", nil, []string{"analytics_events"}},
		{"regional", []string{"us-east", "ap-south"}, []string{"analytics_events", "us-east.analytics_events", "ap-south.analytics_events"}},
		{"duplicates", []string{"us-east", "", " us-east ", "ap-south"}, []string{"analytics_events", "us-east.analytics_events", "ap-south.analytics_events"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := &recordingGatewayRegistrar{}
			accepted := errors.New("gateway observation accepted")
			registerGatewaySubscriptions(reg, func(context.Context, kafka.Message) error { return accepted }, tc.prefixes)
			if !slices.Equal(reg.topics, tc.want) {
				t.Fatalf("gateway discovery subscriptions = %v, want %v", reg.topics, tc.want)
			}
			for _, topic := range tc.want {
				if err := reg.handlers[topic](context.Background(), kafka.Message{Topic: topic}); !errors.Is(err, accepted) {
					t.Errorf("%s does not use the inventory gateway handler: %v", topic, err)
				}
			}
		})
	}
}
