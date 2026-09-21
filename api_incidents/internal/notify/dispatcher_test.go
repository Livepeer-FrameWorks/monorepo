package notify

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/api_incidents/internal/incidents"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/email"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/outbox"
)

const testIncidentID = "40000000-0000-0000-0000-000000000001"

func testPayload(t *testing.T, event, severity, summary string) json.RawMessage {
	t.Helper()
	payload := incidents.DeliveryPayload{
		IncidentID: testIncidentID,
		ClusterID:  "central-eu",
		Region:     "eu-west",
		Alertname:  "EdgeDown",
		Severity:   severity,
		Title:      "Edge node down",
		Summary:    summary,
		Event:      event,
		StartedAt:  time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC),
	}
	if event == incidents.DeliveryEventResolved {
		resolvedAt := payload.StartedAt.Add(time.Hour)
		payload.Resolution = incidents.ResolutionManual
		payload.ResolvedAt = &resolvedAt
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// staticSettings serves one fixed Settings value in place of Lookout's live
// configuration.
func staticSettings(s Settings) SettingsSource {
	return func() Settings { return s }
}

// settingsDispatcher wires one settings source into the router and the
// dispatcher, the way main does.
func settingsDispatcher(s Settings, client *http.Client) *Dispatcher {
	source := staticSettings(s)
	return &Dispatcher{Channels: Router{Settings: source}, Settings: source, HTTP: client}
}

func TestRouterRoutesBySeverityAndConfiguredChannels(t *testing.T) {
	var settings Settings
	router := Router{Settings: func() Settings { return settings }}
	if got := router.ChannelsFor("critical"); len(got) != 0 {
		t.Fatalf("unconfigured critical channels = %v", got)
	}
	if got := (Router{}).ChannelsFor("critical"); len(got) != 0 {
		t.Fatalf("critical channels without a settings source = %v", got)
	}
	settings = Settings{
		EmailRecipients:   []string{"ops@example.test"},
		SlackWebhookURL:   "https://hooks.slack.test/x",
		DiscordWebhookURL: "https://discord.test/x",
	}
	if got, want := router.ChannelsFor("critical"), []string{incidents.ChannelEmail, incidents.ChannelSlack, incidents.ChannelDiscord}; !reflect.DeepEqual(got, want) {
		t.Fatalf("critical channels = %v, want %v", got, want)
	}
	if got, want := router.ChannelsFor("warning"), []string{incidents.ChannelSlack, incidents.ChannelDiscord}; !reflect.DeepEqual(got, want) {
		t.Fatalf("warning channels = %v, want %v", got, want)
	}
	settings.SlackWebhookURL = ""
	if got, want := router.ChannelsFor("warning"), []string{incidents.ChannelDiscord}; !reflect.DeepEqual(got, want) {
		t.Fatalf("warning channels without slack = %v, want %v", got, want)
	}
}

func captureJSONServer(t *testing.T, status int) (*httptest.Server, func() map[string]any) {
	t.Helper()
	var mu sync.Mutex
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("content type = %q", r.Header.Get("Content-Type"))
		}
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		_ = json.Unmarshal(raw, &body)
		mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return srv, func() map[string]any {
		mu.Lock()
		defer mu.Unlock()
		return body
	}
}

func TestSlackDeliveryUsesBlocksAndIncidentLink(t *testing.T) {
	srv, body := captureJSONServer(t, http.StatusOK)
	d := settingsDispatcher(Settings{SlackWebhookURL: srv.URL, WebappURL: "https://app.example.test"}, srv.Client())

	failed, err := d.Dispatch(context.Background(), Delivery{
		Channel:    incidents.ChannelSlack,
		IncidentID: testIncidentID,
		Payload:    testPayload(t, incidents.DeliveryEventOpened, "critical", "Load <script> exceeded"),
	})
	if err != nil || len(failed) != 0 {
		t.Fatalf("dispatch = %v, %v", failed, err)
	}
	blocks, _ := body()["blocks"].([]any)
	if len(blocks) != 4 {
		t.Fatalf("blocks = %#v", body()["blocks"])
	}
	header := blocks[0].(map[string]any)["text"].(map[string]any)["text"].(string)
	if header != "[CRITICAL] Edge node down" {
		t.Fatalf("header = %q", header)
	}
	summary := blocks[1].(map[string]any)["text"].(map[string]any)["text"].(string)
	if !strings.Contains(summary, "&lt;script&gt;") {
		t.Fatalf("summary not mrkdwn-escaped: %q", summary)
	}
	button := blocks[3].(map[string]any)["elements"].([]any)[0].(map[string]any)
	if button["url"] != "https://app.example.test/admin/incidents/"+testIncidentID {
		t.Fatalf("button url = %v", button["url"])
	}
}

func TestDiscordDeliveryUsesEmbedsWithoutMentions(t *testing.T) {
	srv, body := captureJSONServer(t, http.StatusNoContent)
	d := settingsDispatcher(Settings{DiscordWebhookURL: srv.URL}, srv.Client())

	if _, err := d.Dispatch(context.Background(), Delivery{
		Channel:    incidents.ChannelDiscord,
		IncidentID: testIncidentID,
		Payload:    testPayload(t, incidents.DeliveryEventResolved, "warning", "@everyone the edge recovered"),
	}); err != nil {
		t.Fatal(err)
	}
	got := body()
	mentions := got["allowed_mentions"].(map[string]any)["parse"].([]any)
	if len(mentions) != 0 {
		t.Fatalf("allowed_mentions.parse = %v, want empty", mentions)
	}
	embed := got["embeds"].([]any)[0].(map[string]any)
	if embed["title"] != "[RESOLVED] Edge node down (manual)" || int(embed["color"].(float64)) != colorResolved {
		t.Fatalf("embed = %#v", embed)
	}
	if _, hasURL := embed["url"]; hasURL {
		t.Fatal("embed has a link although WEBAPP_PUBLIC_URL is unset")
	}
	if fields := embed["fields"].([]any); len(fields) < 4 {
		t.Fatalf("fields = %#v", fields)
	}
}

func TestBuildMessageDoesNotInventPlacement(t *testing.T) {
	m := buildMessage(incidents.DeliveryPayload{
		IncidentID: testIncidentID,
		Alertname:  "ServiceDown",
		Severity:   "critical",
		Title:      "Service Down: signalman on regional-eu-1",
		Summary:    "The Signalman metrics endpoint is unavailable.",
		Event:      incidents.DeliveryEventOpened,
	}, "")
	for _, field := range m.Fields {
		if field.Name == "Cluster" || field.Name == "Region" {
			t.Fatalf("invented placement field = %+v", field)
		}
	}
}

type smtpMessage struct {
	To   []string
	Data string
}

func startFakeSMTP(t *testing.T) (host, port string, messages <-chan smtpMessage) {
	t.Helper()
	var listenConfig net.ListenConfig
	ln, err := listenConfig.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	out := make(chan smtpMessage, 10)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveFakeSMTP(conn, out)
		}
	}()
	host, port, _ = net.SplitHostPort(ln.Addr().String())
	return host, port, out
}

func serveFakeSMTP(conn net.Conn, out chan<- smtpMessage) {
	defer func() { _ = conn.Close() }()
	reader := bufio.NewReader(conn)
	reply := func(line string) { _, _ = conn.Write([]byte(line + "\r\n")) }
	reply("220 fake.test ESMTP")
	var msg smtpMessage
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return
		}
		command := strings.ToUpper(strings.TrimSpace(line))
		switch {
		case strings.HasPrefix(command, "EHLO"), strings.HasPrefix(command, "HELO"):
			reply("250 fake.test")
		case strings.HasPrefix(command, "RCPT TO:"):
			msg.To = append(msg.To, strings.Trim(strings.TrimSpace(line[len("RCPT TO:"):]), "<>"))
			reply("250 OK")
		case command == "DATA":
			reply("354 end data with <CR><LF>.<CR><LF>")
			var data strings.Builder
			for {
				dataLine, err := reader.ReadString('\n')
				if err != nil {
					return
				}
				if dataLine == ".\r\n" {
					break
				}
				data.WriteString(dataLine)
			}
			msg.Data = data.String()
			out <- msg
			msg = smtpMessage{}
			reply("250 queued")
		case command == "QUIT":
			reply("221 bye")
			return
		default:
			reply("250 OK")
		}
	}
}

func TestEmailDeliveryThroughSMTPToEveryRecipient(t *testing.T) {
	host, port, messages := startFakeSMTP(t)
	source := staticSettings(Settings{
		EmailRecipients: []string{"oncall@example.test", "lead@example.test"},
		SMTP: email.Config{
			Host:          host,
			Port:          port,
			From:          "lookout@example.test",
			FromName:      "FrameWorks",
			AllowInsecure: true,
		},
	})
	d := &Dispatcher{Channels: Router{Settings: source}, Settings: source, Mailer: SMTPMailer(source)}

	if _, err := d.Dispatch(context.Background(), Delivery{
		Channel:    incidents.ChannelEmail,
		IncidentID: testIncidentID,
		Payload:    testPayload(t, incidents.DeliveryEventOpened, "critical", "Edge <b>down</b>"),
	}); err != nil {
		t.Fatal(err)
	}
	recipients := []string{}
	for i := 0; i < 2; i++ {
		select {
		case msg := <-messages:
			recipients = append(recipients, msg.To...)
			if !strings.Contains(msg.Data, "[Lookout] [CRITICAL] Edge node down") {
				t.Fatalf("message lacks subject: %q", msg.Data)
			}
			if !strings.Contains(msg.Data, "&lt;b&gt;down&lt;/b&gt;") {
				t.Fatal("HTML alternative does not escape incident content")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for SMTP message")
		}
	}
	if want := []string{"oncall@example.test", "lead@example.test"}; !reflect.DeepEqual(recipients, want) {
		t.Fatalf("recipients = %v, want %v", recipients, want)
	}
}

func TestEmailDeliveryFailsWithoutSMTPHost(t *testing.T) {
	source := staticSettings(Settings{EmailRecipients: []string{"oncall@example.test"}})
	d := &Dispatcher{Channels: Router{Settings: source}, Settings: source, Mailer: SMTPMailer(source)}
	failed, err := d.Dispatch(context.Background(), Delivery{Channel: incidents.ChannelEmail, Payload: testPayload(t, incidents.DeliveryEventOpened, "critical", "x")})
	if err == nil || !reflect.DeepEqual(failed, []string{incidents.ChannelEmail}) {
		t.Fatalf("dispatch = %v, %v; want retryable failure", failed, err)
	}
}

func TestWebhookFailuresAreRetryableAndHideTheURL(t *testing.T) {
	srv, _ := captureJSONServer(t, http.StatusInternalServerError)
	d := settingsDispatcher(Settings{SlackWebhookURL: srv.URL}, srv.Client())
	failed, err := d.Dispatch(context.Background(), Delivery{Channel: incidents.ChannelSlack, Payload: testPayload(t, incidents.DeliveryEventOpened, "warning", "x")})
	if err == nil || !strings.Contains(err.Error(), "unexpected status 500") || !reflect.DeepEqual(failed, []string{incidents.ChannelSlack}) {
		t.Fatalf("dispatch = %v, %v", failed, err)
	}

	unreachable := Settings{DiscordWebhookURL: "http://127.0.0.1:1/api/webhooks/123/SECRET-TOKEN"}
	_, err = settingsDispatcher(unreachable, &http.Client{Timeout: time.Second}).Dispatch(context.Background(), Delivery{
		Channel: incidents.ChannelDiscord,
		Payload: testPayload(t, incidents.DeliveryEventOpened, "warning", "x"),
	})
	if err == nil || strings.Contains(err.Error(), "SECRET-TOKEN") {
		t.Fatalf("transport error = %v; must fail without exposing the webhook URL", err)
	}
}

func TestDispatchSkipsChannelNoLongerConfigured(t *testing.T) {
	d := settingsDispatcher(Settings{}, nil)
	failed, err := d.Dispatch(context.Background(), Delivery{Channel: incidents.ChannelSlack, Payload: testPayload(t, incidents.DeliveryEventOpened, "warning", "x")})
	if err != nil || len(failed) != 0 {
		t.Fatalf("dispatch = %v, %v; want settled skip", failed, err)
	}
}

type recordingProducer struct {
	topic   string
	key     []byte
	value   []byte
	headers map[string]string
}

func (p *recordingProducer) ProduceMessage(topic string, key, value []byte, headers map[string]string) error {
	p.topic, p.key, p.value, p.headers = topic, key, value, headers
	return nil
}

func TestKafkaPublicationCarriesTenantHeader(t *testing.T) {
	producer := &recordingProducer{}
	d := &Dispatcher{Producer: producer, Topic: "lookout.incidents"}
	value := []byte(`{"incident_id":"i","tenant_id":"t","cluster_id":"c","severity":"warning","summary":"s"}`)
	if _, err := d.Dispatch(context.Background(), Delivery{Channel: incidents.ChannelKafka, IncidentID: "i", TenantID: "t", Payload: value}); err != nil {
		t.Fatal(err)
	}
	if producer.topic != "lookout.incidents" || string(producer.key) != "i" || string(producer.value) != string(value) || producer.headers["tenant_id"] != "t" {
		t.Fatalf("produced %+v", producer)
	}
}

// memoryStore is a single-row token-fenced outbox store.
type memoryStore struct {
	mu        sync.Mutex
	delivery  Delivery
	token     string
	leased    bool
	delivered bool
	attempts  int
	tokens    int
}

func (s *memoryStore) ClaimBatch(context.Context, int, time.Duration) ([]outbox.Claim[Delivery], error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.delivered || s.leased {
		return nil, nil
	}
	s.tokens++
	s.token = "token-" + string(rune('0'+s.tokens))
	s.leased = true
	return []outbox.Claim[Delivery]{{ID: "/row", Attempts: s.attempts, LeaseToken: s.token, Payload: s.delivery}}, nil
}

func (s *memoryStore) MarkCompleted(context.Context, string) error { return errLeaseTokenRequired }

func (s *memoryStore) RecordFailure(context.Context, string, int, []string, error, time.Duration) error {
	return errLeaseTokenRequired
}

func (s *memoryStore) MarkCompletedToken(_ context.Context, _ string, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if token != s.token {
		return errLeaseLost
	}
	s.delivered, s.leased = true, false
	return nil
}

func (s *memoryStore) RecordFailureToken(_ context.Context, _ string, _ int, _ []string, _ error, _ time.Duration, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if token != s.token {
		return errLeaseLost
	}
	s.attempts++
	s.leased = false
	return nil
}

// reclaim simulates a peer worker taking over an expired lease.
func (s *memoryStore) reclaim() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.token = "peer-token"
}

func TestWorkerRetriesFailedWebhookThenDelivers(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if hits.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	store := &memoryStore{delivery: Delivery{Channel: incidents.ChannelSlack, Payload: testPayload(t, incidents.DeliveryEventOpened, "warning", "x")}}
	worker := &outbox.Worker[Delivery]{
		Config:     outbox.Config{BaseBackoff: time.Millisecond, MaxBackoff: time.Millisecond, BatchSize: 1, Lease: time.Minute},
		Store:      store,
		Dispatcher: settingsDispatcher(Settings{SlackWebhookURL: srv.URL}, srv.Client()),
	}
	worker.ProcessBatch(context.Background())
	if store.attempts != 1 || store.delivered {
		t.Fatalf("after failed attempt: attempts=%d delivered=%v", store.attempts, store.delivered)
	}
	worker.ProcessBatch(context.Background())
	if !store.delivered || hits.Load() != 2 {
		t.Fatalf("after retry: delivered=%v hits=%d", store.delivered, hits.Load())
	}
}

func TestWorkerCannotSettleAfterLeaseIsReclaimed(t *testing.T) {
	store := &memoryStore{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		store.reclaim()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	store.delivery = Delivery{Channel: incidents.ChannelSlack, Payload: testPayload(t, incidents.DeliveryEventOpened, "warning", "x")}
	worker := &outbox.Worker[Delivery]{
		Config:     outbox.Config{BaseBackoff: time.Millisecond, MaxBackoff: time.Millisecond, BatchSize: 1, Lease: time.Minute},
		Store:      store,
		Dispatcher: settingsDispatcher(Settings{SlackWebhookURL: srv.URL}, srv.Client()),
	}
	worker.ProcessBatch(context.Background())
	if store.delivered {
		t.Fatal("stale worker settled a row a peer re-claimed")
	}
	if err := store.MarkCompletedToken(context.Background(), "/row", "token-1"); !errors.Is(err, errLeaseLost) {
		t.Fatalf("stale settlement = %v, want errLeaseLost", err)
	}
}
