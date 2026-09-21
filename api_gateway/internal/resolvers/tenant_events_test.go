package resolvers

import (
	"context"
	"errors"
	"strings"
	"testing"

	"frameworks/api_gateway/internal/demo"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/globalid"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	publicv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/public/v1"
	signalmanpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/signalman"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	tenantEventsTenantA = "tenant-a"
	tenantEventsTenantB = "tenant-b"
	tenantEventsStream  = "5eedfeed-0000-4000-8000-000000000001"
	tenantEventsOther   = "5eedfeed-0000-4000-8000-000000000002"
)

func publicTenantEvent(t *testing.T, id, eventType string, data proto.Message) *signalmanpb.TenantEvent {
	t.Helper()
	payload, err := anypb.New(data)
	if err != nil {
		t.Fatalf("pack %s: %v", eventType, err)
	}
	return &signalmanpb.TenantEvent{Id: id, Type: eventType, Time: timestamppb.Now(), Subject: "x/" + id, Data: payload}
}

func onEventsChannel(tenantID *string, event *signalmanpb.TenantEvent) *signalmanpb.SignalmanEvent {
	return &signalmanpb.SignalmanEvent{
		EventType: signalmanpb.EventType_EVENT_TYPE_TENANT_EVENT,
		Channel:   signalmanpb.Channel_CHANNEL_EVENTS,
		TenantId:  tenantID,
		Data:      &signalmanpb.EventData{Payload: &signalmanpb.EventData_TenantEvent{TenantEvent: event}},
	}
}

func clipArtifact(streamID string) *publicv1.Artifact {
	return &publicv1.Artifact{ArtifactId: "clip-1", Kind: publicv1.ArtifactKind_ARTIFACT_KIND_CLIP, StreamId: streamID}
}

// receiveIDs drains exactly the expected event IDs in order and then requires
// silence, so a delivered event that should have been filtered fails.
func receiveIDs(t *testing.T, updates <-chan *signalmanpb.TenantEvent, want ...string) {
	t.Helper()
	for _, id := range want {
		if got := receiveUpdate(t, updates); got.GetId() != id {
			t.Fatalf("received event %q, want %q", got.GetId(), id)
		}
	}
	expectNoUpdate(t, updates)
}

func TestTenantEventsSubscribesEventsChannelWithCallerTenant(t *testing.T) {
	opener := newFakeOpener()
	sm := testSubscriptionManager(t, opener, SubscriptionManagerConfig{})
	if _, err := sm.SubscribeToTenantEvents(context.Background(), ConnectionConfig{TenantID: tenantEventsTenantA}, TenantEventFilter{}); err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	upstream := opener.waitOpenedByChannel(t, 1)[signalmanpb.Channel_CHANNEL_EVENTS]
	if upstream == nil || upstream.key.TenantID != tenantEventsTenantA {
		t.Fatalf("upstream = %+v, want CHANNEL_EVENTS for %s", upstream, tenantEventsTenantA)
	}
	if _, err := sm.SubscribeToTenantEvents(context.Background(), ConnectionConfig{}, TenantEventFilter{}); err == nil {
		t.Fatal("a tenantless subscription must be refused")
	}
}

func TestTenantEventsTypesFilterMatchesExactType(t *testing.T) {
	opener := newFakeOpener()
	sm := testSubscriptionManager(t, opener, SubscriptionManagerConfig{})
	filter, err := NewTenantEventFilter([]string{"clip.ready", "stream.live"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	updates, err := sm.SubscribeToTenantEvents(context.Background(), ConnectionConfig{TenantID: tenantEventsTenantA}, filter)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	upstream := opener.waitOpened(t)
	tenant := tenantEventsTenantA
	upstream.push(t, onEventsChannel(&tenant, publicTenantEvent(t, "requested", "clip.requested", &publicv1.ClipRequested{Artifact: clipArtifact(tenantEventsStream)})))
	upstream.push(t, onEventsChannel(&tenant, publicTenantEvent(t, "ready", "clip.ready", &publicv1.ClipReady{Artifact: clipArtifact(tenantEventsStream)})))
	upstream.push(t, onEventsChannel(&tenant, publicTenantEvent(t, "idle", "stream.idle", &publicv1.StreamIdle{StreamId: tenantEventsStream})))
	upstream.push(t, onEventsChannel(&tenant, publicTenantEvent(t, "live", "stream.live", &publicv1.StreamLive{StreamId: tenantEventsStream})))
	receiveIDs(t, updates, "ready", "live")
}

func TestTenantEventsStreamFilterReadsThePayloadStream(t *testing.T) {
	opener := newFakeOpener()
	sm := testSubscriptionManager(t, opener, SubscriptionManagerConfig{})
	relayID := globalid.Encode(globalid.TypeStream, tenantEventsStream)
	filter, err := NewTenantEventFilter(nil, &relayID)
	if err != nil {
		t.Fatal(err)
	}
	if filter.StreamID != tenantEventsStream {
		t.Fatalf("relay ID normalized to %q, want %q", filter.StreamID, tenantEventsStream)
	}
	updates, err := sm.SubscribeToTenantEvents(context.Background(), ConnectionConfig{TenantID: tenantEventsTenantA}, filter)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	upstream := opener.waitOpened(t)
	tenant := tenantEventsTenantA
	push := func(id, eventType string, data proto.Message) {
		upstream.push(t, onEventsChannel(&tenant, publicTenantEvent(t, id, eventType, data)))
	}
	push("stream-own", "stream.live", &publicv1.StreamLive{StreamId: tenantEventsStream})
	push("stream-other", "stream.live", &publicv1.StreamLive{StreamId: tenantEventsOther})
	push("clip-own", "clip.ready", &publicv1.ClipReady{Artifact: clipArtifact(tenantEventsStream)})
	push("clip-other", "clip.ready", &publicv1.ClipReady{Artifact: clipArtifact(tenantEventsOther)})
	push("upload", "upload.ready", &publicv1.UploadReady{Artifact: &publicv1.Artifact{ArtifactId: "vod-1", Kind: publicv1.ArtifactKind_ARTIFACT_KIND_UPLOAD}})
	push("token", "api_token.revoked", &publicv1.ApiTokenRevoked{TokenId: "tok-1"})
	push("push-own", "multistream.status_changed", &publicv1.MultistreamStatusChanged{StreamId: tenantEventsStream, Status: publicv1.MultistreamStatus_MULTISTREAM_STATUS_PUSHING})
	receiveIDs(t, updates, "stream-own", "clip-own", "push-own")
}

func TestTenantEventsDeliverOnlyTheSubscribersTenant(t *testing.T) {
	opener := newFakeOpener()
	sm := testSubscriptionManager(t, opener, SubscriptionManagerConfig{})
	updates, err := sm.SubscribeToTenantEvents(context.Background(), ConnectionConfig{TenantID: tenantEventsTenantA}, TenantEventFilter{})
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	upstream := opener.waitOpened(t)
	own, other, empty := tenantEventsTenantA, tenantEventsTenantB, ""
	event := func(id string) *signalmanpb.TenantEvent {
		return publicTenantEvent(t, id, "stream.live", &publicv1.StreamLive{StreamId: tenantEventsStream})
	}
	upstream.push(t, onEventsChannel(&other, event("other-tenant")))
	upstream.push(t, onEventsChannel(nil, event("tenantless")))
	upstream.push(t, onEventsChannel(&empty, event("empty-tenant")))
	upstream.push(t, onEventsChannel(&own, event("own")))
	receiveIDs(t, updates, "own")
}

func TestTenantEventsRefuseInternalEvents(t *testing.T) {
	opener := newFakeOpener()
	sm := testSubscriptionManager(t, opener, SubscriptionManagerConfig{})
	filter, err := NewTenantEventFilter([]string{"recording.chapter_ready", "recording.ready"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	updates, err := sm.SubscribeToTenantEvents(context.Background(), ConnectionConfig{TenantID: tenantEventsTenantA}, filter)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	upstream := opener.waitOpened(t)
	tenant := tenantEventsTenantA
	chapter := &internalv1.RecordingChapterReady{Artifact: clipArtifact(tenantEventsStream), ChapterId: "chapter-1"}
	internal := publicTenantEvent(t, "internal", "recording.chapter_ready", chapter)
	forged := publicTenantEvent(t, "forged", "recording.ready", chapter)
	upstream.push(t, onEventsChannel(&tenant, internal))
	upstream.push(t, onEventsChannel(&tenant, forged))
	upstream.push(t, onEventsChannel(&tenant, publicTenantEvent(t, "public", "recording.ready", &publicv1.RecordingReady{Artifact: clipArtifact(tenantEventsStream)})))
	receiveIDs(t, updates, "public")

	for _, event := range []*signalmanpb.TenantEvent{internal, forged, {Id: "no-data", Type: "stream.live"}} {
		if refused, refuseErr := PublicEventPayload(event); !errors.Is(refuseErr, ErrNotPublicEvent) || refused != nil {
			t.Fatalf("PublicEventPayload(%s) = (%v, %v), want ErrNotPublicEvent", event.GetId(), refused, refuseErr)
		}
	}
	mislabeled := publicTenantEvent(t, "mislabeled", "clip.ready", &publicv1.StreamLive{StreamId: tenantEventsStream})
	if _, mislabeledErr := PublicEventPayload(mislabeled); !errors.Is(mislabeledErr, ErrNotPublicEvent) {
		t.Fatalf("a public payload under another type's name = %v, want ErrNotPublicEvent", mislabeledErr)
	}
	msg, err := PublicEventPayload(publicTenantEvent(t, "ok", "stream.live", &publicv1.StreamLive{StreamId: tenantEventsStream}))
	if err != nil {
		t.Fatalf("public payload refused: %v", err)
	}
	if live, ok := msg.(*publicv1.StreamLive); !ok || live.GetStreamId() != tenantEventsStream {
		t.Fatalf("payload = %T %v, want *publicv1.StreamLive", msg, msg)
	}
}

func TestTenantEventsCountTowardTheTenantSubscriptionCap(t *testing.T) {
	opener := newFakeOpener()
	sm := testSubscriptionManager(t, opener, SubscriptionManagerConfig{MaxSubscriptionsPerTenant: 1})
	config := ConnectionConfig{TenantID: tenantEventsTenantA}
	if _, err := sm.SubscribeToTenantEvents(context.Background(), config, TenantEventFilter{}); err != nil {
		t.Fatalf("first subscription: %v", err)
	}
	if _, err := sm.SubscribeToTenantEvents(context.Background(), config, TenantEventFilter{}); err == nil || !strings.Contains(err.Error(), "max number of active subscriptions") {
		t.Fatalf("second subscription err = %v, want tenant limit", err)
	}
	if _, err := sm.SubscribeToTenantEvents(context.Background(), ConnectionConfig{TenantID: tenantEventsTenantB}, TenantEventFilter{}); err != nil {
		t.Fatalf("another tenant is not limited by tenant-a: %v", err)
	}
}

func TestEventTimestampIsNilWhenUnset(t *testing.T) {
	if got := EventTimestamp(nil); got != nil {
		t.Fatalf("EventTimestamp(nil) = %v, want nil", got)
	}
	ts := timestamppb.Now()
	if got := EventTimestamp(ts); got == nil || !got.Equal(ts.AsTime()) {
		t.Fatalf("EventTimestamp(%v) = %v", ts.AsTime(), got)
	}
}

func TestDemoTenantEventsArePublicAndFilterable(t *testing.T) {
	events := demo.GenerateTenantEvents()
	if len(events) == 0 {
		t.Fatal("demo generator returned no events")
	}
	for _, event := range events {
		if !isRegisteredPublic(event) {
			t.Fatalf("demo event %s (%s) is not a registered public event", event.GetId(), event.GetType())
		}
		if _, err := PublicEventPayload(event); err != nil {
			t.Fatalf("demo event %s: %v", event.GetId(), err)
		}
	}
	filter, err := NewTenantEventFilter([]string{"clip.ready"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	matched := 0
	for _, event := range events {
		if filter.Matches(event) {
			matched++
		}
	}
	if matched != 1 {
		t.Fatalf("demo clip.ready events = %d, want 1", matched)
	}
}
