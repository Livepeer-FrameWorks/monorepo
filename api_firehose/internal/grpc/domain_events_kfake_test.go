package grpc

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	eventspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	publicv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/public/v1"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/topology"
	"github.com/google/uuid"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const (
	domainTenant  = "5b0f1e5e-2a55-4b0f-9d3c-6f1a2b3c4d5e"
	domainStream1 = "a3c7d9e1-0000-4000-8000-000000000001"
	domainStream2 = "a3c7d9e1-0000-4000-8000-000000000002"
)

type domainHarness struct {
	server *DecklogServer
	admin  *kadm.Client
	reader *kgo.Client
}

// newDomainHarness runs Decklog's real Kafka producer against an in-process
// Kafka (kfake) that has domain.events with its canonical partition count.
func newDomainHarness(t *testing.T, seedTopic bool) *domainHarness {
	t.Helper()
	opts := []kfake.Opt{kfake.NumBrokers(1)}
	if seedTopic {
		spec, _ := topology.CanonicalTopic(topology.TopicDomainEvents)
		opts = append(opts, kfake.SeedTopics(int32(spec.Partitions), topology.TopicDomainEvents))
	}
	cluster, err := kfake.NewCluster(opts...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cluster.Close)
	logger := logging.NewLogger()
	producer, err := kafka.NewKafkaProducer(cluster.ListenAddrs(), topology.TopicAnalyticsEvents, "test", logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = producer.Close() })
	reader, err := kgo.NewClient(kgo.SeedBrokers(cluster.ListenAddrs()...),
		kgo.ConsumeTopics(topology.TopicDomainEvents), kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(reader.Close)
	return &domainHarness{
		server: NewDecklogServerWithConfig(producer, logger, nil, DecklogServerConfig{SourceRegion: "eu-west", SourceClusterID: "cell-eu"}),
		admin:  kadm.NewClient(reader),
		reader: reader,
	}
}

func (h *domainHarness) recordCount(t *testing.T) int64 {
	t.Helper()
	offsets, err := h.admin.ListEndOffsets(context.Background(), topology.TopicDomainEvents)
	if err != nil {
		t.Fatal(err)
	}
	var total int64
	offsets.Each(func(o kadm.ListedOffset) { total += o.Offset })
	return total
}

func (h *domainHarness) consume(t *testing.T, n int) []*kgo.Record {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var out []*kgo.Record
	for len(out) < n {
		fetches := h.reader.PollFetches(ctx)
		if ctx.Err() != nil {
			t.Fatalf("consumed %d of %d records", len(out), n)
		}
		fetches.EachRecord(func(r *kgo.Record) { out = append(out, r) })
	}
	return out
}

func mustEnvelope(t *testing.T, source, tenant, aggregateID string, msg proto.Message, opts ...events.Option) *eventspb.DomainEvent {
	t.Helper()
	ev, err := events.New(source, tenant, aggregateID, msg, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return ev.Envelope()
}

func TestPublishDomainEventsProducesKeyedCloudEvents(t *testing.T) {
	h := newDomainHarness(t, true)
	actor := events.Actor{AuthType: "api_token", UserID: "user-1", TokenHash: 42}
	batch := &eventspb.DomainEventBatch{Events: []*eventspb.DomainEvent{
		mustEnvelope(t, "commodore", domainTenant, domainStream1, &publicv1.StreamCreated{StreamId: domainStream1, Name: "Main"}, events.WithActor(actor)),
		mustEnvelope(t, "commodore", domainTenant, domainStream2, &publicv1.StreamCreated{StreamId: domainStream2}),
		mustEnvelope(t, "commodore", domainTenant, domainStream1, &publicv1.StreamUpdated{StreamId: domainStream1, ChangedFields: []string{"name"}}, events.WithAggregateVersion(2)),
		mustEnvelope(t, "quartermaster", "", "cluster-a", &internalv1.ClusterCreated{ClusterId: "cluster-a", OwnerTenantId: domainTenant}),
	}}
	resp, err := h.server.PublishDomainEvents(context.Background(), batch)
	if err != nil {
		t.Fatal(err)
	}
	// The RPC returns after Kafka acknowledged the whole batch, so every
	// record is already readable.
	if resp.GetPublished() != 4 || h.recordCount(t) != 4 {
		t.Fatalf("published=%d, records in topic=%d, want 4", resp.GetPublished(), h.recordCount(t))
	}

	byID := map[string]*kgo.Record{}
	for _, r := range h.consume(t, 4) {
		rec, err := events.ParseRecord(r.Key, headersOf(r), r.Value)
		if err != nil {
			t.Fatalf("record %s does not parse: %v", r.Key, err)
		}
		byID[rec.ID] = r
	}
	for _, env := range batch.GetEvents() {
		r, ok := byID[env.GetId()]
		if !ok {
			t.Fatalf("event %s (%s) missing from Kafka", env.GetId(), env.GetType())
		}
		spec, _ := events.Lookup(env.GetType())
		if string(r.Key) != spec.Aggregate+"/"+env.GetAggregateId() {
			t.Fatalf("%s key = %q", env.GetType(), r.Key)
		}
		hdr := map[string]string{}
		for _, x := range r.Headers {
			hdr[x.Key] = string(x.Value)
		}
		if hdr[events.HeaderID] != env.GetId() || hdr[events.HeaderType] != env.GetType() ||
			hdr[events.HeaderSpecVersion] != "1.0" || hdr[events.HeaderDataSchema] != string(spec.MessageName) ||
			hdr[events.HeaderSourceRegion] != "eu-west" || hdr[events.HeaderSourceClusterID] != "cell-eu" {
			t.Fatalf("%s headers = %v", env.GetType(), hdr)
		}
		if _, hasTenant := hdr[events.HeaderTenantID]; hasTenant != (env.GetTenantId() != "") {
			t.Fatalf("%s tenant header presence = %v", env.GetType(), hasTenant)
		}
		if !bytes.Equal(r.Value, env.GetData()) {
			t.Fatalf("%s value is not the producer payload", env.GetType())
		}
	}
	first, second := byID[batch.Events[0].GetId()], byID[batch.Events[2].GetId()]
	if first.Partition != second.Partition || first.Offset >= second.Offset {
		t.Fatalf("stream events at partition/offset %d/%d and %d/%d; want one partition in batch order",
			first.Partition, first.Offset, second.Partition, second.Offset)
	}
	actorHeaders := map[string]string{}
	for _, x := range first.Headers {
		actorHeaders[x.Key] = string(x.Value)
	}
	if actorHeaders[events.HeaderActorAuthType] != "api_token" || actorHeaders[events.HeaderActorTokenHash] != "42" {
		t.Fatalf("actor headers = %v", actorHeaders)
	}
}

func headersOf(r *kgo.Record) []events.Header {
	out := make([]events.Header, len(r.Headers))
	for i, h := range r.Headers {
		out[i] = events.Header{Key: h.Key, Value: h.Value}
	}
	return out
}

func TestPublishDomainEventsRejectsBeforeProducingAnything(t *testing.T) {
	h := newDomainHarness(t, true)
	valid := func() *eventspb.DomainEvent {
		return mustEnvelope(t, "commodore", domainTenant, domainStream1, &publicv1.StreamCreated{StreamId: domainStream1, Name: "Main"})
	}
	cases := map[string]struct {
		mutate func(*eventspb.DomainEvent)
		code   codes.Code
	}{
		"missing id":           {func(e *eventspb.DomainEvent) { e.Id = "" }, codes.InvalidArgument},
		"non-v7 id":            {func(e *eventspb.DomainEvent) { e.Id = uuid.NewString() }, codes.InvalidArgument},
		"unknown type":         {func(e *eventspb.DomainEvent) { e.Type = "stream.exploded" }, codes.FailedPrecondition},
		"payload of another":   {func(e *eventspb.DomainEvent) { e.Type = "stream.deleted" }, codes.InvalidArgument},
		"undecodable payload":  {func(e *eventspb.DomainEvent) { e.Data = []byte{0xff, 0xff} }, codes.InvalidArgument},
		"tenant event no ten":  {func(e *eventspb.DomainEvent) { e.TenantId = "" }, codes.InvalidArgument},
		"tenant not a uuid":    {func(e *eventspb.DomainEvent) { e.TenantId = "tenant-a" }, codes.InvalidArgument},
		"missing aggregate id": {func(e *eventspb.DomainEvent) { e.AggregateId = "" }, codes.InvalidArgument},
	}
	for name, tc := range cases {
		bad := valid()
		tc.mutate(bad)
		batch := &eventspb.DomainEventBatch{Events: []*eventspb.DomainEvent{valid(), bad}}
		_, err := h.server.PublishDomainEvents(context.Background(), batch)
		if status.Code(err) != tc.code {
			t.Fatalf("%s: code = %v (%v), want %v", name, status.Code(err), err, tc.code)
		}
		if n := h.recordCount(t); n != 0 {
			t.Fatalf("%s: rejected batch produced %d records", name, n)
		}
	}

	platformWithTenant := mustEnvelope(t, "quartermaster", "", "cluster-a", &internalv1.ClusterCreated{ClusterId: "cluster-a"})
	platformWithTenant.TenantId = domainTenant
	if _, err := h.server.PublishDomainEvents(context.Background(), &eventspb.DomainEventBatch{Events: []*eventspb.DomainEvent{platformWithTenant}}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("platform event with tenant: %v", err)
	}
	if _, err := h.server.PublishDomainEvents(context.Background(), &eventspb.DomainEventBatch{}); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("empty batch: %v", err)
	}
	if n := h.recordCount(t); n != 0 {
		t.Fatalf("rejections produced %d records", n)
	}

	// The same valid event is accepted, so the rejections above came from the
	// mutated fields.
	if _, err := h.server.PublishDomainEvents(context.Background(), &eventspb.DomainEventBatch{Events: []*eventspb.DomainEvent{valid()}}); err != nil {
		t.Fatalf("valid event rejected: %v", err)
	}
	if n := h.recordCount(t); n != 1 {
		t.Fatalf("valid event produced %d records", n)
	}
}

func TestPublishDomainEventsIsUnavailableUntilKafkaAcknowledges(t *testing.T) {
	// The topic does not exist and kfake does not auto-create it, so Kafka
	// never acknowledges the record.
	h := newDomainHarness(t, false)
	batch := &eventspb.DomainEventBatch{Events: []*eventspb.DomainEvent{
		mustEnvelope(t, "commodore", domainTenant, domainStream1, &publicv1.StreamDeleted{StreamId: domainStream1}),
	}}
	_, err := h.server.PublishDomainEvents(context.Background(), batch)
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("unacknowledged produce returned %v (%v), want Unavailable", status.Code(err), err)
	}
}
