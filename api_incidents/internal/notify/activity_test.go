package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"frameworks/api_incidents/internal/incidents"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/serviceevents"
)

type activitySinkStub struct {
	activities []OperatorActivity
	channels   [][]string
	errs       []error
}

func (s *activitySinkStub) EnqueueActivity(_ context.Context, activity OperatorActivity, channels []string) error {
	if len(s.errs) > 0 {
		err := s.errs[0]
		s.errs = s.errs[1:]
		if err != nil {
			return err
		}
	}
	s.activities = append(s.activities, activity)
	s.channels = append(s.channels, append([]string(nil), channels...))
	return nil
}

type activityChannelsStub struct {
	enabled map[string]bool
}

func (s activityChannelsStub) Enabled(channel string) bool { return s.enabled[channel] }

func activityMessage(t *testing.T, event kafka.ServiceEvent) kafka.Message {
	t.Helper()
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	return kafka.Message{Topic: "service_events", Value: raw}
}

func TestActivityConsumerEnqueuesAllowlistedSourceEvent(t *testing.T) {
	sink := &activitySinkStub{}
	consumer := &ActivityConsumer{
		Sink: sink,
		Channels: activityChannelsStub{enabled: map[string]bool{
			incidents.ChannelSlack: true, incidents.ChannelDiscord: true,
		}},
	}
	event := kafka.ServiceEvent{
		EventID:   "evt-signup",
		EventType: "tenant_created",
		Source:    "quartermaster",
		TenantID:  "11111111-1111-4111-8111-111111111111",
		Timestamp: time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC),
		Data: map[string]any{"attribution": map[string]any{
			"signup_channel": "wallet", "utm_campaign": "launch",
		}},
	}
	if err := consumer.Handle(context.Background(), activityMessage(t, event)); err != nil {
		t.Fatal(err)
	}
	if len(sink.activities) != 1 {
		t.Fatalf("enqueued %d activities, want 1", len(sink.activities))
	}
	got := sink.activities[0]
	if got.Payload.Headline != "New tenant signup" || got.Payload.Category != "growth" {
		t.Fatalf("payload = %+v", got.Payload)
	}
	if len(sink.channels[0]) != 2 {
		t.Fatalf("channels = %v, want Slack and Discord", sink.channels[0])
	}
	raw, _ := json.Marshal(got.Payload)
	if !strings.Contains(string(raw), "wallet") || !strings.Contains(string(raw), "launch") {
		t.Fatalf("payload missing redacted attribution: %s", raw)
	}
}

func TestActivityConsumerIgnoresDuplicateAPIObservationAndUnknownEvents(t *testing.T) {
	sink := &activitySinkStub{}
	consumer := &ActivityConsumer{
		Sink:     sink,
		Channels: activityChannelsStub{enabled: map[string]bool{incidents.ChannelSlack: true}},
	}
	for _, event := range []kafka.ServiceEvent{
		{EventID: "evt-api", EventType: "tenant_created", Source: "bridge", TenantID: "11111111-1111-4111-8111-111111111111"},
		{EventID: "evt-login", EventType: "auth_login_succeeded", Source: "commodore", TenantID: "11111111-1111-4111-8111-111111111111"},
	} {
		if err := consumer.Handle(context.Background(), activityMessage(t, event)); err != nil {
			t.Fatal(err)
		}
	}
	if len(sink.activities) != 0 {
		t.Fatalf("unexpected activities: %+v", sink.activities)
	}
}

func TestActivityConsumerMarketingPayloadCannotLeakSubmittedPII(t *testing.T) {
	sink := &activitySinkStub{}
	consumer := &ActivityConsumer{
		Sink:     sink,
		Channels: activityChannelsStub{enabled: map[string]bool{incidents.ChannelSlack: true}},
	}
	event := kafka.ServiceEvent{
		EventID:   "evt-contact",
		EventType: serviceevents.MarketingContactDelivered,
		Source:    "steward",
		Data: map[string]any{
			"email": "private@example.com", "message": "secret contact body", "ip": "192.0.2.10",
		},
	}
	if err := consumer.Handle(context.Background(), activityMessage(t, event)); err != nil {
		t.Fatal(err)
	}
	if len(sink.activities) != 1 {
		t.Fatalf("enqueued %d activities, want 1", len(sink.activities))
	}
	raw, _ := json.Marshal(sink.activities[0].Payload)
	for _, forbidden := range []string{"private@example.com", "secret contact body", "192.0.2.10"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("activity payload leaked %q: %s", forbidden, raw)
		}
	}
}

func TestActivityConsumerRetriesSinkFailure(t *testing.T) {
	sink := &activitySinkStub{errs: []error{errors.New("database unavailable"), nil}}
	consumer := &ActivityConsumer{
		Sink:     sink,
		Channels: activityChannelsStub{enabled: map[string]bool{incidents.ChannelSlack: true}},
		Sleep:    func(context.Context, time.Duration) error { return nil },
	}
	event := kafka.ServiceEvent{
		EventID: "evt-stream", EventType: "stream_created", Source: "commodore",
		TenantID: "11111111-1111-4111-8111-111111111111",
	}
	if err := consumer.HandleUntilEnqueued(context.Background(), activityMessage(t, event)); err != nil {
		t.Fatal(err)
	}
	if len(sink.activities) != 1 {
		t.Fatalf("enqueued %d activities after retry, want 1", len(sink.activities))
	}
}

func TestActivityDispatcherPostsSlackMessage(t *testing.T) {
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	payload, err := json.Marshal(ActivityPayload{
		Headline: "New tenant signup", Category: "growth",
		Fields: []ActivityField{{Name: "Tenant", Value: "tenant-1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	dispatcher := &ActivityDispatcher{Webhook: &Dispatcher{
		Channels: activityChannelsStub{enabled: map[string]bool{incidents.ChannelSlack: true}},
		Settings: staticSettings(Settings{SlackWebhookURL: server.URL}),
		HTTP:     server.Client(),
	}}
	if _, err := dispatcher.Dispatch(context.Background(), ActivityDelivery{
		Channel: incidents.ChannelSlack, Payload: payload,
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(body, "New tenant signup") || !strings.Contains(body, "tenant-1") {
		t.Fatalf("Slack body = %s", body)
	}
}
