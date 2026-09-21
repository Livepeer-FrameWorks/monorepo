package events

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	eventspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events"
	internalv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	publicv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/public/v1"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const (
	testTenant = "5b0f1e5e-2a55-4b0f-9d3c-6f1a2b3c4d5e"
	testStream = "a3c7d9e1-0000-4000-8000-000000000001"
)

func TestNewEnforcesScope(t *testing.T) {
	if _, err := New("commodore", "", testStream, &publicv1.StreamCreated{StreamId: testStream}); !errors.Is(err, ErrScopeMismatch) {
		t.Fatalf("tenant event without tenant: %v", err)
	}
	if _, err := New("commodore", "not-a-uuid", testStream, &publicv1.StreamCreated{StreamId: testStream}); !errors.Is(err, ErrScopeMismatch) {
		t.Fatalf("tenant event with invalid tenant: %v", err)
	}
	if _, err := New("quartermaster", testTenant, "cluster-a", &internalv1.ClusterCreated{ClusterId: "cluster-a"}); !errors.Is(err, ErrScopeMismatch) {
		t.Fatalf("platform event with tenant: %v", err)
	}
	if _, err := New("quartermaster", "", "cluster-a", &internalv1.ClusterCreated{ClusterId: "cluster-a", OwnerTenantId: testTenant}); err != nil {
		t.Fatalf("platform event: %v", err)
	}
	if _, err := New("commodore", testTenant, "", &publicv1.StreamCreated{}); !errors.Is(err, ErrInvalidEnvelope) {
		t.Fatalf("missing aggregate id: %v", err)
	}
	if _, err := New("commodore", testTenant, testStream, &publicv1.Artifact{}); !errors.Is(err, ErrUnknownType) {
		t.Fatalf("submessage accepted as an event: %v", err)
	}
}

func TestNewAssignsUUIDv7(t *testing.T) {
	ev, err := New("commodore", testTenant, testStream, &publicv1.StreamDeleted{StreamId: testStream})
	if err != nil {
		t.Fatal(err)
	}
	id, err := uuid.Parse(ev.ID)
	if err != nil || id.Version() != 7 {
		t.Fatalf("event id %q is not a UUIDv7", ev.ID)
	}
	if ev.Type != "stream.deleted" || ev.Time.IsZero() {
		t.Fatalf("event = %+v", ev)
	}
}

func TestRecordRoundTripCarriesCloudEventsHeaders(t *testing.T) {
	occurred := time.Date(2026, 9, 18, 12, 0, 0, 123456789, time.UTC)
	actor := Actor{AuthType: "api_token", UserID: "user-1", TokenHash: 1<<63 + 5}
	ev, err := New("commodore", testTenant, testStream, &publicv1.StreamUpdated{StreamId: testStream, ChangedFields: []string{"name"}},
		WithActor(actor), WithAggregateVersion(7), WithTime(occurred))
	if err != nil {
		t.Fatal(err)
	}
	env := ev.Envelope()
	env.SourceRegion, env.SourceClusterId = "eu-west", "cell-eu"
	key, headers, value, err := EncodeRecord(env)
	if err != nil {
		t.Fatal(err)
	}
	if string(key) != "streams/"+testStream {
		t.Fatalf("key = %q", key)
	}
	got := map[string]string{}
	for _, h := range headers {
		got[h.Key] = string(h.Value)
	}
	for k, want := range map[string]string{
		HeaderSpecVersion:      "1.0",
		HeaderID:               ev.ID,
		HeaderType:             "stream.updated",
		HeaderSource:           "commodore",
		HeaderTime:             "2026-09-18T12:00:00.123456Z",
		HeaderSubject:          "streams/" + testStream,
		HeaderDataSchema:       "frameworks.events.public.v1.StreamUpdated",
		HeaderContentType:      "application/protobuf",
		HeaderVisibility:       "public",
		HeaderTenantID:         testTenant,
		HeaderSourceRegion:     "eu-west",
		HeaderSourceClusterID:  "cell-eu",
		HeaderActorAuthType:    "api_token",
		HeaderActorUserID:      "user-1",
		HeaderActorTokenHash:   strconv.FormatUint(1<<63+5, 10),
		HeaderAggregateVersion: "7",
	} {
		if got[k] != want {
			t.Fatalf("header %s = %q, want %q", k, got[k], want)
		}
	}

	rec, err := ParseRecord(key, headers, value)
	if err != nil {
		t.Fatal(err)
	}
	if rec.ID != ev.ID || rec.TenantID != testTenant || rec.AggregateID != testStream || rec.AggregateVersion != 7 ||
		rec.Actor != actor || !rec.Time.Equal(occurred.Truncate(time.Microsecond)) || rec.SourceRegion != "eu-west" || !rec.Spec.Public() {
		t.Fatalf("record = %+v", rec)
	}
	if !proto.Equal(rec.Message, &publicv1.StreamUpdated{StreamId: testStream, ChangedFields: []string{"name"}}) {
		t.Fatalf("message = %v", rec.Message)
	}
}

func TestPlatformRecordHasNoTenantHeader(t *testing.T) {
	ev, err := New("quartermaster", "", "cluster-a", &internalv1.ClusterCreated{ClusterId: "cluster-a"})
	if err != nil {
		t.Fatal(err)
	}
	key, headers, _, err := EncodeRecord(ev.Envelope())
	if err != nil {
		t.Fatal(err)
	}
	if string(key) != "clusters/cluster-a" {
		t.Fatalf("key = %q", key)
	}
	for _, h := range headers {
		if h.Key == HeaderTenantID {
			t.Fatal("platform event carries a tenant header")
		}
		if h.Key == HeaderVisibility && string(h.Value) != VisibilityInternal {
			t.Fatalf("visibility = %q", h.Value)
		}
	}
}

func validEnvelope(t *testing.T) *eventspb.DomainEvent {
	t.Helper()
	ev, err := New("commodore", testTenant, testStream, &publicv1.StreamCreated{StreamId: testStream, Name: "Main"})
	if err != nil {
		t.Fatal(err)
	}
	return ev.Envelope()
}

func TestValidateRejections(t *testing.T) {
	cases := map[string]struct {
		mutate func(*eventspb.DomainEvent)
		want   error
	}{
		"missing id":       {func(e *eventspb.DomainEvent) { e.Id = "" }, ErrMissingID},
		"v4 id":            {func(e *eventspb.DomainEvent) { e.Id = uuid.NewString() }, ErrInvalidID},
		"unknown type":     {func(e *eventspb.DomainEvent) { e.Type = "stream.exploded" }, ErrUnknownType},
		"missing tenant":   {func(e *eventspb.DomainEvent) { e.TenantId = "" }, ErrScopeMismatch},
		"missing source":   {func(e *eventspb.DomainEvent) { e.Source = "" }, ErrInvalidEnvelope},
		"missing time":     {func(e *eventspb.DomainEvent) { e.Time = nil }, ErrInvalidEnvelope},
		"missing agg id":   {func(e *eventspb.DomainEvent) { e.AggregateId = "" }, ErrInvalidEnvelope},
		"garbage payload":  {func(e *eventspb.DomainEvent) { e.Data = []byte{0xff, 0xff, 0xff} }, ErrInvalidPayload},
		"wrong schema":     {func(e *eventspb.DomainEvent) { e.Type = "stream.deleted" }, ErrInvalidPayload},
		"invalid utf8":     {func(e *eventspb.DomainEvent) { e.Data = []byte{0x0a, 0x02, 0xc3, 0x28} }, ErrInvalidPayload},
		"platform, tenant": {func(e *eventspb.DomainEvent) { e.Type = "cluster.created" }, ErrScopeMismatch},
	}
	for name, tc := range cases {
		env := validEnvelope(t)
		if _, _, err := Validate(env); err != nil {
			t.Fatalf("%s: baseline envelope rejected: %v", name, err)
		}
		tc.mutate(env)
		if _, _, err := Validate(env); !errors.Is(err, tc.want) {
			t.Fatalf("%s: Validate error = %v, want %v", name, err, tc.want)
		}
	}
}

func TestDecodeKeepsFieldsFromNewerProducers(t *testing.T) {
	data, err := proto.Marshal(&publicv1.StreamCreated{StreamId: testStream, Name: "Main", PlaybackId: "pb"})
	if err != nil {
		t.Fatal(err)
	}
	// StreamDeleted only declares stream_id, standing in for an older consumer
	// schema reading a newer payload.
	_, msg, err := Decode("stream.deleted", data)
	if err != nil {
		t.Fatalf("consumer decode rejected newer fields: %v", err)
	}
	if msg.(*publicv1.StreamDeleted).GetStreamId() != testStream {
		t.Fatalf("decoded %v", msg)
	}
	if _, _, err := Decode("stream.exploded", data); !errors.Is(err, ErrUnknownType) {
		t.Fatalf("unknown type decode = %v", err)
	}
}

func TestValidateRejectsUnknownFieldsInNestedMessages(t *testing.T) {
	ev, err := New("foghorn", testTenant, "hash-1", &publicv1.ClipReady{Artifact: &publicv1.Artifact{ArtifactId: "hash-1"}})
	if err != nil {
		t.Fatal(err)
	}
	env := ev.Envelope()
	// Field 1 (artifact) carrying a field 9 that Artifact does not declare.
	env.Data = []byte{0x0a, 0x04, 0x48, 0x01, 0x10, 0x02}
	if _, _, err := Validate(env); !errors.Is(err, ErrInvalidPayload) {
		t.Fatalf("nested unknown field accepted: %v", err)
	}
}

func TestParseRecordRejectsMissingEnvelopeAttributes(t *testing.T) {
	key, headers, value, err := EncodeRecord(validEnvelope(t))
	if err != nil {
		t.Fatal(err)
	}
	without := func(name string) []Header {
		var out []Header
		for _, h := range headers {
			if h.Key != name {
				out = append(out, h)
			}
		}
		return out
	}
	if _, err := ParseRecord(key, without(HeaderID), value); !errors.Is(err, ErrMissingID) {
		t.Fatalf("missing ce_id: %v", err)
	}
	if _, err := ParseRecord(key, without(HeaderSpecVersion), value); !errors.Is(err, ErrInvalidEnvelope) {
		t.Fatalf("missing ce_specversion: %v", err)
	}
	if _, err := ParseRecord([]byte("clusters/x"), without(HeaderSubject), value); !errors.Is(err, ErrInvalidEnvelope) {
		t.Fatalf("key of another aggregate: %v", err)
	}
}

func TestHashIdentifierIsBridgeUsageHash(t *testing.T) {
	secret := []byte("usage-secret")
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte("token-123"))
	want := binary.BigEndian.Uint64(mac.Sum(nil)[:8])
	if got := HashIdentifier(secret, "token-123"); got != want {
		t.Fatalf("HashIdentifier = %d, want %d", got, want)
	}
	if HashIdentifier(secret, "") != 0 {
		t.Fatal("empty identifier must hash to zero")
	}
}

func TestActorFromContext(t *testing.T) {
	if _, err := NewTokenHasher(""); err == nil {
		t.Fatal("empty hash secret accepted")
	}
	hasher, err := NewTokenHasher("usage-secret")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "api_token")
	ctx = context.WithValue(ctx, ctxkeys.KeyUserID, "user-1")
	ctx = context.WithValue(ctx, ctxkeys.KeyAPITokenID, "token-123")
	got := ActorFromContext(ctx, hasher)
	want := Actor{AuthType: "api_token", UserID: "user-1", TokenHash: HashIdentifier([]byte("usage-secret"), "token-123")}
	if got != want || got.TokenHash == 0 {
		t.Fatalf("api token actor = %+v, want %+v", got, want)
	}

	jwt := context.WithValue(context.WithValue(context.Background(), ctxkeys.KeyAuthType, "jwt"), ctxkeys.KeyUserID, "user-2")
	jwt = context.WithValue(jwt, ctxkeys.KeyAPITokenID, "token-123")
	if got := ActorFromContext(jwt, hasher); got != (Actor{AuthType: "jwt", UserID: "user-2"}) {
		t.Fatalf("jwt actor = %+v", got)
	}
}

// An actor carried on a service request reads back as the actor the calling
// service computed; a request that names none yields no actor.
func TestRequestActorRoundTrip(t *testing.T) {
	actor := Actor{AuthType: "api_token", UserID: "user-1", TokenHash: HashIdentifier([]byte("usage-secret"), "token-123")}
	got, ok := ActorFromRequest(RequestActor(actor))
	if !ok || got != actor {
		t.Fatalf("round trip = %+v, %v; want %+v", got, ok, actor)
	}
	if RequestActor(Actor{}) != nil {
		t.Fatal("a zero actor was carried on the request")
	}
	if got, ok := ActorFromRequest(nil); ok || got != (Actor{}) {
		t.Fatalf("absent request actor = %+v, %v", got, ok)
	}
}

func TestEnvelopeTimeSurvivesTimestampConversion(t *testing.T) {
	ev, err := New("commodore", testTenant, testStream, &publicv1.StreamLive{StreamId: testStream})
	if err != nil {
		t.Fatal(err)
	}
	if !ev.Envelope().GetTime().AsTime().Equal(ev.Time) {
		t.Fatal("envelope time differs from event time")
	}
	env := ev.Envelope()
	env.Time = timestamppb.New(time.Unix(0, 0))
	if _, _, err := Validate(env); !errors.Is(err, ErrInvalidEnvelope) {
		t.Fatalf("epoch time accepted: %v", err)
	}
}
