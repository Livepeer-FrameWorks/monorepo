package handlers

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	eventspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	publicv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/public/v1"
	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"google.golang.org/protobuf/proto"
)

// domainEventMessage encodes an event the way Decklog produces it onto
// domain.events.
func domainEventMessage(t *testing.T, topic string, ev events.Event) kafka.Message {
	t.Helper()
	key, headers, value, err := events.EncodeRecord(ev.Envelope())
	if err != nil {
		t.Fatalf("encode %s: %v", ev.Type, err)
	}
	msg := kafka.Message{Topic: topic, Key: key, Value: value, Headers: map[string]string{}}
	for _, header := range headers {
		msg.Headers[header.Key] = string(header.Value)
	}
	return msg
}

func newDomainEvent(t *testing.T, tenantID, aggregateID string, msg proto.Message, opts ...events.Option) events.Event {
	t.Helper()
	ev, err := events.New("foghorn", tenantID, aggregateID, msg, opts...)
	if err != nil {
		t.Fatalf("new %T: %v", msg, err)
	}
	return ev
}

func domainEventMetrics() *PeriscopeMetrics {
	return &PeriscopeMetrics{DomainEvents: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "domain_events_total"}, []string{"event_type", "status"})}
}

func TestDomainEventOfUnknownTypeIsCountedAndNotStored(t *testing.T) {
	conn := newFakeClickhouseConn()
	metrics := domainEventMetrics()
	h := NewAnalyticsHandler(conn, logging.NewLogger(), metrics)

	tenantID := uuid.NewString()
	msg := domainEventMessage(t, "domain.events", newDomainEvent(t, tenantID, "clip-hash-1",
		&publicv1.ClipReady{Artifact: &publicv1.Artifact{ArtifactId: "clip-hash-1", Kind: publicv1.ArtifactKind_ARTIFACT_KIND_CLIP}}))
	msg.Headers[events.HeaderType] = "clip.teleported"

	if err := h.HandleDomainEventMessage(context.Background(), msg); err != nil {
		t.Fatalf("an unknown type must not fail the partition, got %v", err)
	}
	if got := testutil.ToFloat64(metrics.DomainEvents.WithLabelValues("clip.teleported", domainEventUnknownType)); got != 1 {
		t.Fatalf("unknown_type count = %v, want 1", got)
	}
	if len(conn.prepares) != 0 {
		t.Fatalf("an unknown type must write nothing, prepared %v", conn.prepares)
	}
}

func TestUndecodableDomainEventReturnsAnError(t *testing.T) {
	conn := newFakeClickhouseConn()
	metrics := domainEventMetrics()
	h := NewAnalyticsHandler(conn, logging.NewLogger(), metrics)

	msg := domainEventMessage(t, "domain.events", newDomainEvent(t, uuid.NewString(), "clip-hash-2",
		&publicv1.ClipReady{Artifact: &publicv1.Artifact{ArtifactId: "clip-hash-2"}}))
	msg.Value = []byte{0xff, 0xff, 0xff}

	err := h.HandleDomainEventMessage(context.Background(), msg)
	if !errors.Is(err, events.ErrInvalidPayload) {
		t.Fatalf("an undecodable payload must reach the DLQ wrapper as an error, got %v", err)
	}
	if got := testutil.ToFloat64(metrics.DomainEvents.WithLabelValues("clip.ready", domainEventInvalid)); got != 1 {
		t.Fatalf("invalid count = %v, want 1", got)
	}
	if len(conn.prepares) != 0 {
		t.Fatalf("an undecodable record must write nothing, prepared %v", conn.prepares)
	}
}

func TestDomainArtifactEventWritesAuditEventAndState(t *testing.T) {
	conn := newFakeClickhouseConn()
	h := NewAnalyticsHandler(conn, logging.NewLogger(), domainEventMetrics())

	tenantID, streamID := uuid.NewString(), uuid.NewString()
	ev := newDomainEvent(t, tenantID, "clip-hash-3", &publicv1.ClipFailed{
		Artifact: &publicv1.Artifact{ArtifactId: "clip-hash-3", Kind: publicv1.ArtifactKind_ARTIFACT_KIND_CLIP, StreamId: streamID},
		Reason:   publicv1.MediaFailureReason_MEDIA_FAILURE_REASON_PROCESSING_FAILED,
	}, events.WithActor(events.Actor{AuthType: "api_token", UserID: uuid.NewString(), TokenHash: 42}))

	if err := h.HandleDomainEventMessage(context.Background(), domainEventMessage(t, "domain.events", ev)); err != nil {
		t.Fatalf("handle: %v", err)
	}
	audit := conn.batches["api_events"]
	if audit == nil || len(audit.rows) != 1 {
		t.Fatalf("api_events rows = %#v, want 1", audit)
	}
	if row := audit.rows[0]; row[0] != uuid.MustParse(ev.ID) || row[2] != "clip.failed" || row[5] != "artifacts" || row[14] != "api_token" || row[15] != uint64(42) {
		t.Fatalf("api_events row = %#v", row)
	}
	artifactEvents := conn.batches["artifact_events"]
	if artifactEvents == nil || len(artifactEvents.rows) != 1 {
		t.Fatalf("artifact_events rows = %#v, want 1", artifactEvents)
	}
	if row := artifactEvents.rows[0]; row[3] != "clip-hash-3" || row[4] != "failed" || row[5] != "clip" || row[10] != ev.ID || row[11] != "domain_events" {
		t.Fatalf("artifact_events row = %#v", row)
	}
	state := conn.batches["artifact_state_current_v2"]
	if state == nil || len(state.rows) != 1 {
		t.Fatalf("artifact_state_current_v2 rows = %#v, want 1", state)
	}
	if row := state.rows[0]; row[1] != "clip-hash-3" || row[4] != "failed" || row[6] != "processing_failed" || row[13] != artifactStateVersion(ev.ID, 0) {
		t.Fatalf("artifact_state_current_v2 row = %#v", row)
	}
}

func TestDomainEventWithoutArtifactProjectionWritesOnlyTheAuditRow(t *testing.T) {
	conn := newFakeClickhouseConn()
	h := NewAnalyticsHandler(conn, logging.NewLogger(), domainEventMetrics())

	ev := newDomainEvent(t, uuid.NewString(), streamIDForTest, &publicv1.StreamCreated{StreamId: streamIDForTest})
	if err := h.HandleDomainEventMessage(context.Background(), domainEventMessage(t, "eu.domain.events", ev)); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if conn.batches["api_events"] == nil || conn.batches["artifact_events"] != nil || conn.batches["artifact_state_current_v2"] != nil {
		t.Fatalf("stream.created must write only api_events, prepared %v", conn.prepares)
	}
}

const streamIDForTest = "5b0c1f0e-7a55-4c55-9d7c-2f3f7c0f0a11"

// notAuditedInternalTypes are the tenant-scoped internal event types that must
// never reach the tenant audit table.
var notAuditedInternalTypes = map[string]string{
	"artifact.node_copy_changed": "names the storage node holding a transient copy",
	"recording.chapter_ready":    "a processing step of a recording, not a tenant action or a tenant-visible state change",
}

// TestInternalDomainEventsReachTheAuditTableOnlyWhenAudited decides every
// registered tenant-scoped internal type: a new internal type fails here until
// it is placed in auditedInternalTypes or notAuditedInternalTypes.
func TestInternalDomainEventsReachTheAuditTableOnlyWhenAudited(t *testing.T) {
	for _, spec := range events.Specs() {
		if spec.Public() || spec.Scope != eventspb.Scope_SCOPE_TENANT {
			continue
		}
		_, excluded := notAuditedInternalTypes[spec.Type]
		audited := auditedInternalTypes[spec.Type]
		if audited == excluded {
			t.Errorf("internal type %s: audited=%v excluded=%v; decide it in exactly one list", spec.Type, audited, excluded)
			continue
		}

		conn := newFakeClickhouseConn()
		h := NewAnalyticsHandler(conn, logging.NewLogger(), domainEventMetrics())
		msg := spec.NewMessage()
		ev := newDomainEvent(t, uuid.NewString(), "aggregate-1", msg)
		if err := h.HandleDomainEventMessage(context.Background(), domainEventMessage(t, "domain.events", ev)); err != nil {
			t.Fatalf("handle %s: %v", spec.Type, err)
		}
		wrote := conn.batches["api_events"] != nil
		if wrote != audited {
			t.Errorf("%s wrote an api_events row = %v, want %v", spec.Type, wrote, audited)
		}
	}
}

func TestNodeCopyChangedNeverReachesTheTenantAuditTable(t *testing.T) {
	conn := newFakeClickhouseConn()
	h := NewAnalyticsHandler(conn, logging.NewLogger(), domainEventMetrics())

	ev := newDomainEvent(t, uuid.NewString(), "clip-hash-copy", &internalv1.ArtifactNodeCopyChanged{
		ArtifactHash: "clip-hash-copy", NodeId: "edge-node-7", Role: "cache",
		Transition: internalv1.NodeCopyTransition_NODE_COPY_TRANSITION_GAINED, IsComplete: true,
	})
	if err := h.HandleDomainEventMessage(context.Background(), domainEventMessage(t, "domain.events", ev)); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if len(conn.prepares) != 0 {
		t.Fatalf("artifact.node_copy_changed must write nothing, prepared %v", conn.prepares)
	}
}

func TestPlatformDomainEventHasNoAuditRow(t *testing.T) {
	conn := newFakeClickhouseConn()
	h := NewAnalyticsHandler(conn, logging.NewLogger(), domainEventMetrics())

	ev := newDomainEvent(t, "", "cluster-1", &internalv1.ClusterCreated{ClusterId: "cluster-1"})
	if err := h.HandleDomainEventMessage(context.Background(), domainEventMessage(t, "domain.events", ev)); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if len(conn.prepares) != 0 {
		t.Fatalf("a platform event must write no tenant-partitioned row, prepared %v", conn.prepares)
	}
}

func TestArtifactStateVersionOrdersEventsOfOneProducer(t *testing.T) {
	var previous uint64
	for i := 0; i < 1000; i++ {
		id, err := uuid.NewV7()
		if err != nil {
			t.Fatal(err)
		}
		version := artifactStateVersion(id.String(), 0)
		if version <= previous {
			t.Fatalf("version %d of event %d is not above %d", version, i, previous)
		}
		previous = version
	}
	if previous >= stampedVersionBit {
		t.Fatalf("an event-ID version %d must sort below every stamped version", previous)
	}
	if artifactStateVersion(uuid.NewString(), 1) != stampedVersionBit|1 || artifactStateVersion(uuid.NewString(), 2) <= artifactStateVersion(uuid.NewString(), 1) {
		t.Fatal("stamped versions must order by aggregate version above every unstamped one")
	}
	if got := artifactStateVersion(uuid.NewString(), 0); got != 0 {
		t.Fatalf("a non-v7 event ID has no order, got %s", strconv.FormatUint(got, 10))
	}
}
