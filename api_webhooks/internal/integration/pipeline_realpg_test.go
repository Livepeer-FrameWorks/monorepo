//go:build schema_verify

package integration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"frameworks/api_webhooks/internal/consumer"
	"frameworks/api_webhooks/internal/delivery"
	"frameworks/api_webhooks/internal/ledger"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/kafka"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/topology"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/webhooksig"

	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kfake"
	"github.com/twmb/franz-go/pkg/kgo"
)

// gatedRecorder runs the real RecordEvent, but first waits on gate and fails
// the first failures calls, to hold a record between poll and commit.
type gatedRecorder struct {
	store    *ledger.Store
	gate     chan struct{}
	failures atomic.Int32
	calls    atomic.Int32
}

func (g *gatedRecorder) RecordEvent(ctx context.Context, ev ledger.IncomingEvent) (ledger.RecordResult, error) {
	g.calls.Add(1)
	select {
	case <-g.gate:
	case <-ctx.Done():
		return ledger.RecordResult{}, ctx.Err()
	}
	if g.failures.Add(-1) >= 0 {
		return ledger.RecordResult{}, errors.New("simulated database outage")
	}
	return g.store.RecordEvent(ctx, ev)
}

// TestBosunOffsetsCommitAfterTransaction_RealPG runs the production Kafka
// consumer against an in-process Kafka. While the event's Postgres
// transaction cannot commit (blocked, then failing), the group's committed
// offset stays at zero; it advances only once the event row exists.
func TestBosunOffsetsCommitAfterTransaction_RealPG(t *testing.T) {
	db := startPostgres(t)
	store := newStore(t, db)
	tenant := newTenant()
	createEndpoint(t, store, tenant, "https://offsets.example/hook")
	backdateEndpoints(t, db)

	cluster, err := kfake.NewCluster(kfake.NumBrokers(1), kfake.SeedTopics(1, topology.TopicDomainEvents))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cluster.Close)
	producer, err := kgo.NewClient(kgo.SeedBrokers(cluster.ListenAddrs()...))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(producer.Close)
	admin := kadm.NewClient(producer)

	ev, msg := streamLiveRecord(t, tenant, topology.TopicDomainEvents)
	record := &kgo.Record{Topic: topology.TopicDomainEvents, Key: msg.Key, Value: msg.Value}
	for k, v := range msg.Headers {
		record.Headers = append(record.Headers, kgo.RecordHeader{Key: k, Value: []byte(v)})
	}
	if err := producer.ProduceSync(context.Background(), record).FirstErr(); err != nil {
		t.Fatal(err)
	}

	recorder := &gatedRecorder{store: store, gate: make(chan struct{})}
	recorder.failures.Store(2)
	handler := &consumer.Handler{Recorder: recorder, RetryDelay: 50 * time.Millisecond}
	logger := logging.NewLogger()
	kc, err := kafka.NewConsumer(cluster.ListenAddrs(), consumer.GroupID, "test", "bosun-test", logger)
	if err != nil {
		t.Fatal(err)
	}
	consumer.Register(kc, nil, handler.Handle, consumer.DeadLetter(nil, "unused", logger))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = kc.Start(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
		_ = kc.Close()
	})

	committed := func() int64 {
		offsets, err := admin.FetchOffsets(context.Background(), consumer.GroupID)
		if err != nil {
			return -1
		}
		o, ok := offsets.Lookup(topology.TopicDomainEvents, 0)
		if !ok || o.Err != nil {
			return -1
		}
		return o.At
	}
	waitFor := func(what string, cond func() bool) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	eventRows := func() int {
		return countRows(t, db, `SELECT count(*) FROM bosun.webhook_events WHERE event_id = $1`, ev.ID)
	}

	waitFor("the handler to receive the record", func() bool { return recorder.calls.Load() >= 1 })
	time.Sleep(500 * time.Millisecond)
	if at := committed(); at > 0 {
		t.Fatalf("offset %d committed while the transaction was blocked", at)
	}
	close(recorder.gate)
	// Two failed transactions are retried in place; the offset must not move.
	waitFor("the in-place retries", func() bool { return recorder.calls.Load() >= 3 })
	waitFor("the committed offset", func() bool { return committed() == 1 })
	if eventRows() != 1 {
		t.Fatal("the offset committed but the event row does not exist")
	}
	if n := countRows(t, db, `SELECT count(*) FROM bosun.webhook_deliveries WHERE event_id = $1`, ev.ID); n != 1 {
		t.Fatalf("deliveries = %d, want 1", n)
	}
	if calls := recorder.calls.Load(); calls != 3 {
		t.Fatalf("RecordEvent ran %d times, want 2 failures then 1 commit", calls)
	}
}

// TestBosunDeliveryAgainstTLSReceiver_RealPG sends a stored event to a local
// TLS receiver through the test client and verifies what the receiver gets.
// The production client refuses the same receiver at connect time.
func TestBosunDeliveryAgainstTLSReceiver_RealPG(t *testing.T) {
	db := startPostgres(t)
	store := newStore(t, db)
	ctx := context.Background()
	recv := newReceiver(t)
	tenant := newTenant()
	ep, secret, err := store.CreateEndpoint(ctx, tenant, ledger.NewEndpoint{URL: recv.server.URL + "/hook", EventTypes: []string{"stream.live"}, APIVersion: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	backdateEndpoints(t, db)
	handler := &consumer.Handler{Recorder: store}
	ev, msg := streamLiveRecord(t, tenant, topology.TopicDomainEvents)
	if err := handler.Handle(ctx, msg); err != nil {
		t.Fatal(err)
	}

	production := &delivery.Worker{Store: store, Sender: &delivery.Sender{HTTP: recv.productionClient(t)}, Jitter: fixedJitter}
	claims, err := store.Claim(ctx, 10)
	if err != nil || len(claims) != 1 {
		t.Fatalf("claim = %d, %v", len(claims), err)
	}
	production.Process(claims[0], time.Now().Add(ledger.Lease))
	if recv.hits.Load() != 0 {
		t.Fatal("the production client reached a loopback receiver")
	}
	d, attempts, err := store.GetDelivery(ctx, tenant, claims[0].DeliveryID)
	if err != nil || len(attempts) != 1 || attempts[0].ErrorClass != delivery.ClassBlockedDestination || d.Status != ledger.DeliveryPending {
		t.Fatalf("production attempt = %+v %+v, %v; want one blocked_destination failure", d, attempts, err)
	}

	if _, err := db.Exec(`UPDATE bosun.webhook_deliveries SET next_attempt_at = now() WHERE id = $1`, d.ID); err != nil {
		t.Fatal(err)
	}
	test := &delivery.Worker{Store: store, Sender: &delivery.Sender{HTTP: recv.testClient(t)}, Jitter: fixedJitter}
	claims, err = store.Claim(ctx, 10)
	if err != nil || len(claims) != 1 {
		t.Fatalf("claim = %d, %v", len(claims), err)
	}
	test.Process(claims[0], time.Now().Add(ledger.Lease))
	if recv.hits.Load() != 1 {
		t.Fatalf("receiver got %d requests, want 1", recv.hits.Load())
	}
	body, head := recv.request(0)
	if err := secret.Verify(body, head, time.Now(), 0); err != nil {
		t.Fatalf("the receiver cannot verify the request with the secret returned at creation: %v", err)
	}
	if head.Get("Content-Type") != "application/json" || head.Get(webhooksig.HeaderID) != ev.ID {
		t.Fatalf("headers = %v", head)
	}
	var got struct {
		ID         string          `json:"id"`
		Type       string          `json:"type"`
		APIVersion string          `json:"api_version"`
		CreatedAt  string          `json:"created_at"`
		Data       json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	created, err := time.Parse(time.RFC3339Nano, got.CreatedAt)
	if err != nil || got.ID != ev.ID || got.Type != "stream.live" || got.APIVersion != "v1" || !created.Equal(ev.Time) {
		t.Fatalf("body = %s", body)
	}
	var data map[string]any
	if err := json.Unmarshal(got.Data, &data); err != nil || data["streamId"] != ev.AggregateID {
		t.Fatalf("data = %s, want the protojson StreamLive with streamId %s", got.Data, ev.AggregateID)
	}
	d, _, err = store.GetDelivery(ctx, tenant, d.ID)
	if err != nil || d.Status != ledger.DeliverySucceeded || d.Attempts != 2 || d.LastStatusCode != 200 {
		t.Fatalf("delivery = %+v, %v", d, err)
	}

	// A test delivery: synchronous, rate limited, not part of the streak.
	tested, err := store.ClaimTestSlot(ctx, tenant, ep.ID)
	if err != nil {
		t.Fatal(err)
	}
	td, ta, err := test.SendTest(ctx, tested)
	if err != nil || td.Kind != ledger.KindTest || td.Status != ledger.DeliverySucceeded || ta.StatusCode != 200 {
		t.Fatalf("test delivery = %+v %+v, %v", td, ta, err)
	}
	if _, err := store.ClaimTestSlot(ctx, tenant, ep.ID); !errors.Is(err, ledger.ErrTestRateLimited) {
		t.Fatalf("a second test within 10 s = %v, want rate limited", err)
	}
	testBody, testHead := recv.request(1)
	if err := secret.Verify(testBody, testHead, time.Now(), 0); err != nil {
		t.Fatalf("test request does not verify: %v", err)
	}
	var endpointRow sql.NullInt64
	if err := db.QueryRow(`SELECT consecutive_failures FROM bosun.webhook_endpoints WHERE id = $1`, ep.ID).Scan(&endpointRow); err != nil || endpointRow.Int64 != 0 {
		t.Fatalf("streak after success = %v, %v", endpointRow, err)
	}
}
