package consumer

import (
	"context"
	"errors"
	"testing"
	"time"

	"frameworks/api_webhooks/internal/ledger"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	publicv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/public/v1"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/topology"

	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
)

type fakeRecorder struct {
	failures int
	calls    int
	got      []ledger.IncomingEvent
}

func (f *fakeRecorder) RecordEvent(_ context.Context, ev ledger.IncomingEvent) (ledger.RecordResult, error) {
	f.calls++
	if f.calls <= f.failures {
		return ledger.RecordResult{}, errors.New("database unavailable")
	}
	f.got = append(f.got, ev)
	return ledger.RecordResult{Deliveries: 1}, nil
}

func message(t *testing.T, tenantID string, msg proto.Message) kafka.Message {
	t.Helper()
	ev, err := events.New("test", tenantID, uuid.NewString(), msg)
	if err != nil {
		t.Fatal(err)
	}
	key, headers, value, err := events.EncodeRecord(ev.Envelope())
	if err != nil {
		t.Fatal(err)
	}
	out := kafka.Message{Key: key, Value: value, Topic: topology.TopicDomainEvents, Headers: map[string]string{}}
	for _, h := range headers {
		out.Headers[h.Key] = string(h.Value)
	}
	return out
}

func TestHandleStoresPublicTenantEvents(t *testing.T) {
	rec := &fakeRecorder{}
	h := &Handler{Recorder: rec}
	tenant := uuid.NewString()
	msg := message(t, tenant, &publicv1.StreamLive{StreamId: "s1"})
	if err := h.Handle(context.Background(), msg); err != nil {
		t.Fatal(err)
	}
	if len(rec.got) != 1 {
		t.Fatalf("recorded %d events", len(rec.got))
	}
	got := rec.got[0]
	if got.TenantID != tenant || got.Type != "stream.live" || got.SchemaName != "frameworks.events.public.v1.StreamLive" || got.ID != msg.Headers[events.HeaderID] {
		t.Fatalf("incoming = %+v", got)
	}
	var decoded publicv1.StreamLive
	if err := proto.Unmarshal(got.Payload, &decoded); err != nil || decoded.GetStreamId() != "s1" {
		t.Fatalf("payload = %v, %v", &decoded, err)
	}
}

func TestHandleDropsWhatIsNotForDelivery(t *testing.T) {
	tenant := uuid.NewString()
	internal := message(t, tenant, &internalv1.RecordingChapterReady{ChapterId: "c1"})
	// Not public by header: dropped before decoding, even with a garbage body.
	undecodableInternal := internal
	undecodableInternal.Value = []byte{0xff, 0xff}
	// An internal type under a forged public header.
	forged := message(t, tenant, &internalv1.RecordingChapterReady{ChapterId: "c1"})
	forged.Headers[events.HeaderVisibility] = events.VisibilityPublic
	// A public type the registry does not know.
	unknown := message(t, tenant, &publicv1.StreamLive{StreamId: "s1"})
	unknown.Headers[events.HeaderType] = "stream.teleported"
	for name, msg := range map[string]kafka.Message{"internal": internal, "internal undecodable": undecodableInternal, "forged public header": forged, "unknown type": unknown} {
		rec := &fakeRecorder{}
		if err := (&Handler{Recorder: rec}).Handle(context.Background(), msg); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if rec.calls != 0 {
			t.Errorf("%s reached the ledger", name)
		}
	}
}

func TestHandleSendsUndecodablePublicRecordsToTheDLQ(t *testing.T) {
	msg := message(t, uuid.NewString(), &publicv1.StreamLive{StreamId: "s1"})
	msg.Value = []byte{0xff, 0xff, 0xff}
	rec := &fakeRecorder{}
	err := (&Handler{Recorder: rec}).Handle(context.Background(), msg)
	if !errors.Is(err, ErrUndecodable) {
		t.Fatalf("undecodable = %v, want ErrUndecodable", err)
	}
	dlq := &fakeDLQ{failures: 1}
	wrapped := DeadLetter(dlq, "decklog_events_dlq", nil)("bosun-domain", (&Handler{Recorder: rec}).Handle)
	if err := wrapped(context.Background(), msg); err != nil {
		t.Fatalf("dead-lettered record = %v, want handled", err)
	}
	if dlq.calls != 2 || dlq.topic != "decklog_events_dlq" || dlq.headers["event_type"] != "stream.live" || dlq.headers[events.HeaderID] == "" {
		t.Fatalf("dlq = %+v; want one retried write with the event type and ID", dlq)
	}
	if rec.calls != 0 {
		t.Fatal("an undecodable record reached the ledger")
	}
}

type fakeDLQ struct {
	failures int
	calls    int
	topic    string
	headers  map[string]string
}

func (f *fakeDLQ) ProduceMessage(topic string, _ []byte, _ []byte, headers map[string]string) error {
	f.calls++
	f.topic, f.headers = topic, headers
	if f.calls <= f.failures {
		return errors.New("broker down")
	}
	return nil
}

func TestHandleRetriesTheLedgerInPlace(t *testing.T) {
	rec := &fakeRecorder{failures: 3}
	h := &Handler{Recorder: rec, RetryDelay: time.Millisecond}
	if err := h.Handle(context.Background(), message(t, uuid.NewString(), &publicv1.StreamLive{StreamId: "s1"})); err != nil {
		t.Fatal(err)
	}
	if rec.calls != 4 || len(rec.got) != 1 {
		t.Fatalf("calls = %d, stored %d; want three failures then one success before returning", rec.calls, len(rec.got))
	}
	rec = &fakeRecorder{failures: 1 << 30}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := (&Handler{Recorder: rec, RetryDelay: time.Millisecond}).Handle(ctx, message(t, uuid.NewString(), &publicv1.StreamLive{StreamId: "s1"})); err == nil {
		t.Fatal("a handler whose ledger never recovers returned nil, which would commit the offset")
	}
}

func TestHandleDropsTenantlessPublicEvents(t *testing.T) {
	msg := message(t, uuid.NewString(), &publicv1.StreamLive{StreamId: "s1"})
	delete(msg.Headers, events.HeaderTenantID)
	rec := &fakeRecorder{}
	if err := (&Handler{Recorder: rec}).Handle(context.Background(), msg); err != nil || rec.calls != 0 {
		t.Fatalf("tenantless = %v, %d calls", err, rec.calls)
	}
}

type registrar struct{ topics []string }

func (r *registrar) AddHandler(topic string, _ kafka.Handler) { r.topics = append(r.topics, topic) }

func TestRegisterSubscribesLocalAndMirroredCopies(t *testing.T) {
	r := &registrar{}
	names := []string{}
	got := Register(r, []string{"eu", "us-east"}, func(context.Context, kafka.Message) error { return nil }, func(name string, h kafka.Handler) kafka.Handler {
		names = append(names, name)
		return h
	})
	want := []string{"domain.events", "eu.domain.events", "us-east.domain.events"}
	if len(got) != 3 || got[0] != want[0] || got[1] != want[1] || got[2] != want[2] || len(r.topics) != 3 {
		t.Fatalf("topics = %v", got)
	}
	if names[1] != "bosun-domain-mirror:eu" {
		t.Fatalf("consumer names = %v", names)
	}
}
