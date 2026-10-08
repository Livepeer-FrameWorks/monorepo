package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"frameworks/api_incidents/internal/incidents"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
)

func purposeSettings(t *testing.T, raw string) Settings {
	t.Helper()
	var s Settings
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatal(err)
	}
	return s
}

type purposeTransport struct{ targets []string }

func (p *purposeTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	p.targets = append(p.targets, r.URL.String())
	return &http.Response{StatusCode: 204, Body: io.NopCloser(strings.NewReader("")), Header: make(http.Header)}, nil
}

func TestPurposeIncidentUsesOpsDestination(t *testing.T) {
	s := purposeSettings(t, `{"SlackWebhookURL":"https://legacy.example/hook","OpsSlackWebhookURL":"https://ops.example/hook","BusinessPulseSlackWebhookURL":"https://business.example/hook"}`)
	transport := &purposeTransport{}
	d := &Dispatcher{Settings: staticSettings(s), HTTP: &http.Client{Transport: transport}}
	_, err := d.Dispatch(context.Background(), Delivery{Channel: incidents.ChannelSlack, Payload: json.RawMessage(`{"incident_id":"i","severity":"warning","event":"opened","purpose":"ops","destination_id":"ops.slack.primary"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if len(transport.targets) != 1 || transport.targets[0] != "https://ops.example/hook" {
		t.Fatalf("incident targets = %v, want only ops", transport.targets)
	}
}

func TestPurposeActivityUsesPinnedBusinessDestination(t *testing.T) {
	s := purposeSettings(t, `{"SlackWebhookURL":"https://legacy.example/hook","OpsSlackWebhookURL":"https://ops.example/hook","BusinessPulseSlackWebhookURL":"https://business.example/hook"}`)
	transport := &purposeTransport{}
	d := &ActivityDispatcher{Webhook: &Dispatcher{Settings: func() Settings { return s }, HTTP: &http.Client{Transport: transport}}}
	delivery := ActivityDelivery{Channel: incidents.ChannelSlack, Payload: json.RawMessage(`{"headline":"Signup","category":"growth","purpose":"business_pulse","destination_id":"business_pulse.slack.primary"}`)}
	if _, err := d.Dispatch(context.Background(), delivery); err != nil {
		t.Fatal(err)
	}
	s = purposeSettings(t, `{"SlackWebhookURL":"https://legacy.example/hook","OpsSlackWebhookURL":"https://ops.example/hook"}`)
	if _, err := d.Dispatch(context.Background(), delivery); err != nil {
		t.Fatal(err)
	}
	if len(transport.targets) != 1 || transport.targets[0] != "https://business.example/hook" {
		t.Fatalf("retry targets = %v, disabled business must not reach ops/legacy", transport.targets)
	}
}

func TestPurposeConsumerSelectsBusinessIndependentOfOps(t *testing.T) {
	s := purposeSettings(t, `{"BusinessPulseDiscordWebhookURL":"https://business.example/hook","DisableLegacyFallback":true}`)
	sink := &activitySinkStub{}
	c := &ActivityConsumer{Sink: sink, Channels: Router{Settings: staticSettings(s)}}
	e := kafka.ServiceEvent{EventID: "signup", EventType: "tenant_created", Source: "quartermaster", TenantID: "11111111-1111-4111-8111-111111111111"}
	if err := c.Handle(context.Background(), activityMessage(t, e)); err != nil {
		t.Fatal(err)
	}
	if len(sink.activities) != 1 || len(sink.channels[0]) != 1 || sink.channels[0][0] != incidents.ChannelDiscord {
		t.Fatalf("business deliveries = %+v / %v", sink.activities, sink.channels)
	}
	raw, _ := json.Marshal(sink.activities[0].Payload)
	if !strings.Contains(string(raw), `"purpose":"business_pulse"`) {
		t.Fatalf("missing persisted purpose: %s", raw)
	}
}

func TestPurposeLegacyRowsKeepLegacyDestination(t *testing.T) {
	s := purposeSettings(t, `{"SlackWebhookURL":"https://legacy.example/hook","OpsSlackWebhookURL":"https://ops.example/hook"}`)
	transport := &purposeTransport{}
	d := &Dispatcher{Settings: staticSettings(s), HTTP: &http.Client{Transport: transport}}
	if _, err := d.Dispatch(context.Background(), Delivery{Channel: incidents.ChannelSlack, Payload: json.RawMessage(`{"incident_id":"old","event":"opened"}`)}); err != nil {
		t.Fatal(err)
	}
	if len(transport.targets) != 1 || transport.targets[0] != "https://legacy.example/hook" {
		t.Fatalf("legacy delivery moved: %v", transport.targets)
	}
}
func TestDisabledPurposeSkipsItsPinnedLegacyFallback(t *testing.T) {
	s := Settings{SlackWebhookURL: "https://legacy.example/hook", BusinessPulseNotificationsDisabled: true}
	transport := &purposeTransport{}
	d := &ActivityDispatcher{Webhook: &Dispatcher{Settings: staticSettings(s), HTTP: &http.Client{Transport: transport}}}
	_, err := d.Dispatch(context.Background(), ActivityDelivery{Channel: incidents.ChannelSlack, Payload: json.RawMessage(`{"headline":"Signup","purpose":"business_pulse","destination_id":"legacy.slack"}`)})
	if err != nil {
		t.Fatal(err)
	}
	if len(transport.targets) != 0 {
		t.Fatal("disabled business purpose still delivered through its previously selected legacy fallback")
	}
}
