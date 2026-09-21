package main

import (
	"context"
	"net"
	"testing"
	"time"

	signalmangrpc "frameworks/api_realtime/internal/grpc"
	"frameworks/api_realtime/internal/metrics"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	publicv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/public/v1"
	signalmanpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/signalman"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
)

const testServiceToken = "service-token"

// startSignalman serves a real Signalman hub over bufconn behind the real
// stream auth interceptor, so subscribers carry their tenant the way the
// Gateway's streams do.
func startSignalman(t *testing.T) (*signalmangrpc.SignalmanServer, signalmanpb.SignalmanServiceClient) {
	t.Helper()
	server := signalmangrpc.NewSignalmanServer(logging.NewLogger(), nil)
	lis := bufconn.Listen(1 << 20)
	grpcServer := grpc.NewServer(grpc.ChainStreamInterceptor(middleware.GRPCStreamAuthInterceptor(middleware.GRPCAuthConfig{
		ServiceToken:   testServiceToken,
		MetadataPolicy: middleware.MetadataPolicyAllow,
		Logger:         logging.NewLogger(),
	})))
	signalmanpb.RegisterSignalmanServiceServer(grpcServer, server)
	go func() { _ = grpcServer.Serve(lis) }()
	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
		server.Shutdown()
		grpcServer.Stop()
		_ = lis.Close()
	})
	return server, signalmanpb.NewSignalmanServiceClient(conn)
}

// subscribeTenant opens a stream for tenantID on channels and waits for its
// confirmation.
func subscribeTenant(t *testing.T, client signalmanpb.SignalmanServiceClient, tenantID string, channels ...signalmanpb.Channel) signalmanpb.SignalmanService_SubscribeClient {
	t.Helper()
	md := metadata.Pairs("authorization", "Bearer "+testServiceToken)
	if tenantID != "" {
		md.Append("x-tenant-id", tenantID)
	}
	ctx, cancel := context.WithCancel(metadata.NewOutgoingContext(context.Background(), md))
	t.Cleanup(cancel)
	stream, err := client.Subscribe(ctx)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	if err := stream.Send(&signalmanpb.ClientMessage{Message: &signalmanpb.ClientMessage_Subscribe{
		Subscribe: &signalmanpb.SubscribeRequest{Channels: channels},
	}}); err != nil {
		t.Fatalf("send subscribe: %v", err)
	}
	for {
		msg, err := stream.Recv()
		if err != nil {
			t.Fatalf("receive confirmation: %v", err)
		}
		if msg.GetSubscriptionConfirmed() != nil {
			return stream
		}
	}
}

func waitForConnections(t *testing.T, client signalmanpb.SignalmanServiceClient, want int32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		stats, err := client.GetHubStats(metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+testServiceToken), &signalmanpb.GetHubStatsRequest{})
		if err == nil && stats.GetTotalConnections() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("hub did not reach %d connections", want)
}

// streamEvents forwards every event a stream receives.
func streamEvents(stream signalmanpb.SignalmanService_SubscribeClient) <-chan *signalmanpb.SignalmanEvent {
	out := make(chan *signalmanpb.SignalmanEvent, 16)
	go func() {
		defer close(out)
		for {
			msg, err := stream.Recv()
			if err != nil {
				return
			}
			if event := msg.GetEvent(); event != nil {
				out <- event
			}
		}
	}()
	return out
}

func nextEvent(events <-chan *signalmanpb.SignalmanEvent, timeout time.Duration) *signalmanpb.SignalmanEvent {
	select {
	case event := <-events:
		return event
	case <-time.After(timeout):
		return nil
	}
}

type topicRegistrar struct{ handlers map[string]kafka.Handler }

func (r *topicRegistrar) AddHandler(topic string, handler kafka.Handler) {
	if r.handlers == nil {
		r.handlers = map[string]kafka.Handler{}
	}
	r.handlers[topic] = handler
}

func testDomainMetrics() *metrics.Metrics {
	return &metrics.Metrics{
		KafkaDuplicateEvents: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "kafka_duplicate_events_total"}, []string{"topic"}),
		DomainEventsDropped:  prometheus.NewCounterVec(prometheus.CounterOpts{Name: "domain_events_dropped_total"}, []string{"reason"}),
	}
}

// wireDomainEvents registers the production handler on the bare and mirrored
// topics against the real hub.
func wireDomainEvents(t *testing.T, server *signalmangrpc.SignalmanServer, m *metrics.Metrics, prefixes ...string) map[string]kafka.Handler {
	t.Helper()
	reg := &topicRegistrar{}
	handler := newDomainEventHandler(server.GetHub(), newEventIDWindow(signalmanEventDedupCapacity, signalmanEventDedupTTL), m, logging.NewLogger())
	topics := registerDomainEventSubscriptions(reg, prefixes, handler, func(_ string, h kafka.Handler) kafka.Handler { return h })
	if len(topics) != len(prefixes)+1 || len(reg.handlers) != len(topics) {
		t.Fatalf("registered topics %v, want the bare topic and one per prefix", topics)
	}
	return reg.handlers
}

func domainRecord(t *testing.T, topic, tenantID, aggregateID string, msg proto.Message) (kafka.Message, events.Event) {
	t.Helper()
	ev, err := events.New("foghorn", tenantID, aggregateID, msg)
	if err != nil {
		t.Fatalf("new %T: %v", msg, err)
	}
	key, headers, value, err := events.EncodeRecord(ev.Envelope())
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	record := kafka.Message{Topic: topic, Key: key, Value: value, Headers: map[string]string{}}
	for _, header := range headers {
		record.Headers[header.Key] = string(header.Value)
	}
	return record, ev
}

func TestPublicDomainEventReachesItsTenantOnceAcrossBareAndMirroredTopics(t *testing.T) {
	server, client := startSignalman(t)
	m := testDomainMetrics()
	handlers := wireDomainEvents(t, server, m, "eu", "us-east")

	tenantA, tenantB, streamID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	owner := streamEvents(subscribeTenant(t, client, tenantA, signalmanpb.Channel_CHANNEL_EVENTS))
	otherTenant := streamEvents(subscribeTenant(t, client, tenantB, signalmanpb.Channel_CHANNEL_EVENTS, signalmanpb.Channel_CHANNEL_ALL))
	ownerAllOnly := streamEvents(subscribeTenant(t, client, tenantA, signalmanpb.Channel_CHANNEL_ALL))
	tenantless := streamEvents(subscribeTenant(t, client, "", signalmanpb.Channel_CHANNEL_EVENTS, signalmanpb.Channel_CHANNEL_ALL))
	waitForConnections(t, client, 4)

	live := &publicv1.StreamLive{StreamId: streamID}
	record, ev := domainRecord(t, "domain.events", tenantA, streamID, live)
	for _, topic := range []string{"domain.events", "eu.domain.events", "us-east.domain.events", "eu.domain.events"} {
		copied := record
		copied.Topic = topic
		if err := handlers[topic](context.Background(), copied); err != nil {
			t.Fatalf("deliver via %s: %v", topic, err)
		}
	}

	got := nextEvent(owner, 2*time.Second)
	if got == nil || got.GetChannel() != signalmanpb.Channel_CHANNEL_EVENTS || got.GetEventType() != signalmanpb.EventType_EVENT_TYPE_TENANT_EVENT || got.GetTenantId() != tenantA {
		t.Fatalf("owner received %v, want one tenant event on CHANNEL_EVENTS", got)
	}
	tenantEvent := got.GetData().GetTenantEvent()
	if tenantEvent.GetId() != ev.ID || tenantEvent.GetType() != "stream.live" || tenantEvent.GetSubject() != "streams/"+streamID {
		t.Fatalf("tenant event = %v", tenantEvent)
	}
	payload, err := tenantEvent.GetData().UnmarshalNew()
	if err != nil || !proto.Equal(payload, live) {
		t.Fatalf("payload = %v (%v), want the producer's message %v", payload, err, live)
	}
	if again := nextEvent(owner, 200*time.Millisecond); again != nil {
		t.Fatalf("owner received a second copy %v", again)
	}
	for name, stream := range map[string]<-chan *signalmanpb.SignalmanEvent{
		"another tenant": otherTenant, "CHANNEL_ALL subscriber": ownerAllOnly, "tenantless stream": tenantless,
	} {
		if leaked := nextEvent(stream, 100*time.Millisecond); leaked != nil {
			t.Fatalf("%s received %v", name, leaked)
		}
	}
	for topic, want := range map[string]float64{"eu.domain.events": 2, "us-east.domain.events": 1, "domain.events": 0} {
		if got := testutil.ToFloat64(m.KafkaDuplicateEvents.WithLabelValues(topic)); got != want {
			t.Fatalf("duplicates on %s = %v, want %v", topic, got, want)
		}
	}
}

func TestInternalDomainEventsNeverReachTenants(t *testing.T) {
	server, client := startSignalman(t)
	m := testDomainMetrics()
	handlers := wireDomainEvents(t, server, m, "eu")

	tenantID, streamID := uuid.NewString(), uuid.NewString()
	subscriber := streamEvents(subscribeTenant(t, client, tenantID, signalmanpb.Channel_CHANNEL_EVENTS, signalmanpb.Channel_CHANNEL_ALL))
	waitForConnections(t, client, 1)
	deliver := func(topic string, record kafka.Message) {
		t.Helper()
		if err := handlers[topic](context.Background(), record); err != nil {
			t.Fatalf("deliver via %s: %v", topic, err)
		}
	}
	chapter := &internalv1.RecordingChapterReady{
		Artifact:  &publicv1.Artifact{ArtifactId: "rec-1", Kind: publicv1.ArtifactKind_ARTIFACT_KIND_RECORDING, StreamId: streamID},
		ChapterId: "chapter-1",
	}

	internal, _ := domainRecord(t, "domain.events", tenantID, "rec-1", chapter)
	if internal.Headers[events.HeaderVisibility] != events.VisibilityInternal {
		t.Fatalf("an internal event must be produced with ce_visibility=internal, got %q", internal.Headers[events.HeaderVisibility])
	}
	deliver("domain.events", internal)

	// Dropped on the header alone: a payload that cannot decode is not an error.
	undecodable, _ := domainRecord(t, "eu.domain.events", tenantID, "rec-1", chapter)
	undecodable.Value = []byte{0xff, 0xff, 0xff}
	deliver("eu.domain.events", undecodable)

	// A header claiming public does not override the registry.
	forged, _ := domainRecord(t, "eu.domain.events", tenantID, "rec-1", chapter)
	forged.Headers[events.HeaderVisibility] = events.VisibilityPublic
	deliver("eu.domain.events", forged)

	// A public type without the visibility header is not delivered either.
	unlabelled, _ := domainRecord(t, "domain.events", tenantID, streamID, &publicv1.StreamIdle{StreamId: streamID})
	delete(unlabelled.Headers, events.HeaderVisibility)
	deliver("domain.events", unlabelled)

	if leaked := nextEvent(subscriber, 200*time.Millisecond); leaked != nil {
		t.Fatalf("tenant received %v", leaked)
	}
	if got := testutil.ToFloat64(m.DomainEventsDropped.WithLabelValues(dropNotPublic)); got != 3 {
		t.Fatalf("not_public drops = %v, want 3", got)
	}
	if got := testutil.ToFloat64(m.DomainEventsDropped.WithLabelValues(dropRegistryInternal)); got != 1 {
		t.Fatalf("registry_internal drops = %v, want 1", got)
	}

	// The same subscriber receives a public event, so the silence above is
	// the filter and not a broken stream.
	public, ev := domainRecord(t, "domain.events", tenantID, streamID, &publicv1.StreamIdle{StreamId: streamID})
	deliver("domain.events", public)
	if got := nextEvent(subscriber, 2*time.Second); got.GetData().GetTenantEvent().GetId() != ev.ID {
		t.Fatalf("subscriber received %v, want public event %s", got, ev.ID)
	}
}
