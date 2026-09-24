//go:build schema_verify

package integration

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"sync"
	"testing"
	"time"

	"frameworks/api_webhooks/internal/consumer"
	"frameworks/api_webhooks/internal/delivery"
	"frameworks/api_webhooks/internal/grpcserver"
	"frameworks/api_webhooks/internal/ledger"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	bosunpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/bosun"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	publicv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/public/v1"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/topology"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// TestBosunMirroredDuplicatesCollapse_RealPG reads one event from the local
// topic, two mirrored copies, and the local topic again. It is stored once
// with one delivery per subscribed enabled endpoint that existed when it
// occurred.
func TestBosunMirroredDuplicatesCollapse_RealPG(t *testing.T) {
	db := startPostgres(t)
	store := newStore(t, db)
	ctx := context.Background()
	tenant := newTenant()
	all := createEndpoint(t, store, tenant, "https://all.example/hook")
	live := createEndpoint(t, store, tenant, "https://live.example/hook", "stream.live")
	createEndpoint(t, store, tenant, "https://clips.example/hook", "clip.ready")
	disabled := createEndpoint(t, store, tenant, "https://disabled.example/hook")
	if _, err := store.DisableEndpoint(ctx, tenant, disabled.ID); err != nil {
		t.Fatal(err)
	}
	backdateEndpoints(t, db)
	ev, local := streamLiveRecord(t, tenant, topology.TopicDomainEvents)
	// Created after the event occurred: never fanned out to. Its creation time
	// is set from the event time because the database clock and the producer
	// clock are different clocks.
	late := createEndpoint(t, store, tenant, "https://late.example/hook")
	if _, err := db.Exec(`UPDATE bosun.webhook_endpoints SET created_at = $1 WHERE id = $2`,
		ev.Time.Add(time.Minute), late.ID); err != nil {
		t.Fatal(err)
	}

	handler := &consumer.Handler{Recorder: store}
	copies := []string{
		topology.TopicDomainEvents,
		topology.MirroredTopicName("eu", topology.TopicDomainEvents),
		topology.MirroredTopicName("us-east", topology.TopicDomainEvents),
		topology.TopicDomainEvents,
	}
	for _, topic := range copies {
		msg := local
		msg.Topic = topic
		if err := handler.Handle(ctx, msg); err != nil {
			t.Fatalf("handle copy from %s: %v", topic, err)
		}
	}
	if n := countRows(t, db, `SELECT count(*) FROM bosun.webhook_events WHERE event_id = $1`, ev.ID); n != 1 {
		t.Fatalf("event rows = %d, want 1 across %d copies", n, len(copies))
	}
	rows, err := db.Query(`SELECT endpoint_id::text FROM bosun.webhook_deliveries WHERE event_id = $1 ORDER BY endpoint_id`, ev.ID)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		got = append(got, id)
	}
	_ = rows.Close()
	want := []string{all.ID, live.ID}
	sort.Strings(want)
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("deliveries went to %v, want exactly the subscribed enabled endpoints %v", got, want)
	}
	// A tenant without endpoints stores nothing.
	quiet := newTenant()
	_, quietMsg := streamLiveRecord(t, quiet, topology.TopicDomainEvents)
	if err := handler.Handle(ctx, quietMsg); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, db, `SELECT count(*) FROM bosun.webhook_events WHERE tenant_id = $1`, quiet); n != 0 {
		t.Fatalf("stored %d events for a tenant with no endpoint", n)
	}
}

// TestBosunTenantIsolation_RealPG checks every API call of one tenant against
// another tenant's objects, and that events fan out within their tenant only.
func TestBosunTenantIsolation_RealPG(t *testing.T) {
	db := startPostgres(t)
	store := newStore(t, db)
	tenantA, tenantB := newTenant(), newTenant()
	epA := createEndpoint(t, store, tenantA, "https://a.example/hook")
	createEndpoint(t, store, tenantB, "https://b.example/hook")
	backdateEndpoints(t, db)
	handler := &consumer.Handler{Recorder: store}
	evA, msgA := streamLiveRecord(t, tenantA, topology.TopicDomainEvents)
	if err := handler.Handle(context.Background(), msgA); err != nil {
		t.Fatal(err)
	}
	_, msgB := streamLiveRecord(t, tenantB, topology.TopicDomainEvents)
	if err := handler.Handle(context.Background(), msgB); err != nil {
		t.Fatal(err)
	}
	var deliveryA string
	if err := db.QueryRow(`SELECT id::text FROM bosun.webhook_deliveries WHERE event_id = $1`, evA.ID).Scan(&deliveryA); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, db, `SELECT count(*) FROM bosun.webhook_deliveries WHERE endpoint_id = $1`, epA.ID); n != 1 {
		t.Fatalf("tenant A's endpoint has %d deliveries, want only its own tenant's event", n)
	}

	srv := &grpcserver.Server{Store: store, Tester: &delivery.Worker{Store: store}}
	asB := context.WithValue(context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service"), ctxkeys.KeyTenantID, tenantB)
	notFound := func(name string, err error) {
		t.Helper()
		if status.Code(err) != codes.NotFound {
			t.Errorf("%s across tenants = %v, want NotFound", name, err)
		}
	}
	_, err := srv.GetWebhookEndpoint(asB, &bosunpb.GetWebhookEndpointRequest{EndpointId: epA.ID})
	notFound("GetWebhookEndpoint", err)
	desc := "taken"
	_, err = srv.UpdateWebhookEndpoint(asB, &bosunpb.UpdateWebhookEndpointRequest{EndpointId: epA.ID, Description: &desc})
	notFound("UpdateWebhookEndpoint", err)
	_, err = srv.DisableWebhookEndpoint(asB, &bosunpb.DisableWebhookEndpointRequest{EndpointId: epA.ID})
	notFound("DisableWebhookEndpoint", err)
	_, err = srv.EnableWebhookEndpoint(asB, &bosunpb.EnableWebhookEndpointRequest{EndpointId: epA.ID})
	notFound("EnableWebhookEndpoint", err)
	_, err = srv.RotateWebhookEndpointSecret(asB, &bosunpb.RotateWebhookEndpointSecretRequest{EndpointId: epA.ID})
	notFound("RotateWebhookEndpointSecret", err)
	_, err = srv.TestWebhookEndpoint(asB, &bosunpb.TestWebhookEndpointRequest{EndpointId: epA.ID})
	notFound("TestWebhookEndpoint", err)
	_, err = srv.GetWebhookDelivery(asB, &bosunpb.GetWebhookDeliveryRequest{DeliveryId: deliveryA})
	notFound("GetWebhookDelivery", err)
	_, err = srv.ReplayWebhookDelivery(asB, &bosunpb.ReplayWebhookDeliveryRequest{DeliveryId: deliveryA})
	notFound("ReplayWebhookDelivery", err)
	_, err = srv.ReplayWebhookDeliveries(asB, &bosunpb.ReplayWebhookDeliveriesRequest{
		EndpointId: epA.ID, CreatedAfter: timestamppb.New(time.Now().Add(-time.Hour)), CreatedBefore: timestamppb.New(time.Now().Add(time.Hour)),
	})
	notFound("ReplayWebhookDeliveries", err)
	_, err = srv.DeleteWebhookEndpoint(asB, &bosunpb.DeleteWebhookEndpointRequest{EndpointId: epA.ID})
	notFound("DeleteWebhookEndpoint", err)

	list, err := srv.ListWebhookEndpoints(asB, &bosunpb.ListWebhookEndpointsRequest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, ep := range list.GetEndpoints() {
		if ep.GetId() == epA.ID {
			t.Fatal("tenant B lists tenant A's endpoint")
		}
	}
	deliveries, err := srv.ListWebhookDeliveries(asB, &bosunpb.ListWebhookDeliveriesRequest{EndpointId: epA.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(deliveries.GetDeliveries()) != 0 || deliveries.GetPagination().GetTotalCount() != 0 {
		t.Fatalf("tenant B sees %d of tenant A's deliveries", len(deliveries.GetDeliveries()))
	}

	asA := context.WithValue(context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service"), ctxkeys.KeyTenantID, tenantA)
	own, err := srv.ListWebhookDeliveries(asA, &bosunpb.ListWebhookDeliveriesRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(own.GetDeliveries()) != 1 || own.GetDeliveries()[0].GetId() != deliveryA {
		t.Fatalf("tenant A lists %v, want its one delivery", own.GetDeliveries())
	}
	if _, err := srv.GetWebhookEndpoint(asA, &bosunpb.GetWebhookEndpointRequest{EndpointId: epA.ID}); err != nil {
		t.Fatalf("positive control: tenant A reads its endpoint: %v", err)
	}
	if _, err := srv.ListWebhookEndpoints(context.Background(), &bosunpb.ListWebhookEndpointsRequest{}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("a call without a tenant = %v, want PermissionDenied", err)
	}
}

func TestBosunLeaseReclaimNoDoubleDelivery_RealPG(t *testing.T) {
	runLeaseReclaimNoDoubleDelivery(t, startPostgres(t))
}

func TestBosunLeaseReclaimNoDoubleDelivery_RealYugabyte(t *testing.T) {
	runLeaseReclaimNoDoubleDelivery(t, startYugabyte(t))
}

// runLeaseReclaimNoDoubleDelivery leases a delivery to worker A, lets the
// lease expire, and has worker B reclaim it. A neither sends nor settles; B's
// single send is the only request the receiver sees. It also checks the
// per-endpoint cap of leased deliveries across claimers.
func runLeaseReclaimNoDoubleDelivery(t *testing.T, db *sql.DB) {
	store := newStore(t, db)
	ctx := context.Background()
	recv := newReceiver(t)
	tenant := newTenant()
	ep := createEndpoint(t, store, tenant, recv.server.URL+"/hook")
	backdateEndpoints(t, db)
	handler := &consumer.Handler{Recorder: store}
	_, msg := streamLiveRecord(t, tenant, topology.TopicDomainEvents)
	if err := handler.Handle(ctx, msg); err != nil {
		t.Fatal(err)
	}

	claimsA, err := store.Claim(ctx, 10)
	if err != nil || len(claimsA) != 1 {
		t.Fatalf("worker A claim = %v, %v", claimsA, err)
	}
	if again, err := store.Claim(ctx, 10); err != nil || len(again) != 0 {
		t.Fatalf("a leased delivery was claimed again before its lease ended: %v, %v", again, err)
	}
	// Worker A stalls past its lease.
	if _, err := db.Exec(`UPDATE bosun.webhook_deliveries SET leased_until = now() - interval '1 second' WHERE id = $1`, claimsA[0].DeliveryID); err != nil {
		t.Fatal(err)
	}
	claimsB, err := store.Claim(ctx, 10)
	if err != nil || len(claimsB) != 1 || claimsB[0].DeliveryID != claimsA[0].DeliveryID || claimsB[0].LeaseToken == claimsA[0].LeaseToken {
		t.Fatalf("worker B reclaim = %+v, %v; want the same delivery under a new token", claimsB, err)
	}

	workerA := &delivery.Worker{Store: store, Sender: &delivery.Sender{HTTP: recv.testClient(t)}}
	workerA.Process(claimsA[0], time.Now().Add(-time.Second))
	if recv.hits.Load() != 0 {
		t.Fatal("worker A sent after its lease ended")
	}
	if res, err := store.Settle(ctx, claimsA[0], ledger.Outcome{Success: true, StatusCode: 200, AttemptedAt: time.Now()}, fixedJitter); err != nil || res.Settled {
		t.Fatalf("worker A's stale settlement = %+v, %v; want refused", res, err)
	}

	workerB := &delivery.Worker{Store: store, Sender: &delivery.Sender{HTTP: recv.testClient(t)}}
	workerB.Process(claimsB[0], time.Now().Add(ledger.Lease))
	if hits := recv.hits.Load(); hits != 1 {
		t.Fatalf("receiver got %d requests, want exactly 1", hits)
	}
	if res, err := store.Settle(ctx, claimsA[0], ledger.Outcome{Success: false, StatusCode: 500, ErrorClass: "http_status", AttemptedAt: time.Now()}, fixedJitter); err != nil || res.Settled {
		t.Fatalf("worker A's settlement after B finished = %+v, %v; want refused", res, err)
	}
	var st string
	var attempts int
	if err := db.QueryRow(`SELECT status, attempts FROM bosun.webhook_deliveries WHERE id = $1`, claimsB[0].DeliveryID).Scan(&st, &attempts); err != nil {
		t.Fatal(err)
	}
	if st != ledger.DeliverySucceeded || attempts != 1 {
		t.Fatalf("delivery = %s after %d attempts, want succeeded after 1", st, attempts)
	}
	if n := countRows(t, db, `SELECT count(*) FROM bosun.webhook_delivery_attempts WHERE delivery_id = $1`, claimsB[0].DeliveryID); n != 1 {
		t.Fatalf("attempt rows = %d, want 1", n)
	}

	// Seven due deliveries of one endpoint: two claimers together lease five.
	for range 7 {
		_, m := streamLiveRecord(t, tenant, topology.TopicDomainEvents)
		if err := handler.Handle(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	first, err := store.Claim(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.Claim(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != ledger.MaxInFlightPerEndpoint || len(second) != 0 {
		t.Fatalf("claims = %d then %d, want %d then 0 for one endpoint", len(first), len(second), ledger.MaxInFlightPerEndpoint)
	}
	// The saturated endpoint still has older due deliveries; a claim of one
	// must go to another endpoint instead of stalling behind them.
	idle := createEndpoint(t, store, tenant, recv.server.URL+"/idle", "stream.idle")
	backdateEndpoints(t, db)
	streamID := uuid.NewString()
	idleEvent, err := events.New("foghorn", tenant, streamID, &publicv1.StreamIdle{StreamId: streamID})
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.Handle(ctx, recordFor(t, idleEvent, topology.TopicDomainEvents)); err != nil {
		t.Fatal(err)
	}
	other, err := store.Claim(ctx, 1)
	if err != nil || len(other) != 1 || other[0].EndpointID != idle.ID {
		t.Fatalf("claim of one = %+v, %v; want the other endpoint's delivery, not a stall behind the saturated endpoint", other, err)
	}
	if _, err := store.Settle(ctx, first[0], ledger.Outcome{Success: true, StatusCode: 200, AttemptedAt: time.Now()}, fixedJitter); err != nil {
		t.Fatal(err)
	}
	third, err := store.Claim(ctx, 10)
	if err != nil || len(third) != 1 {
		t.Fatalf("after one settlement the claimer gets %d, want 1 (%v)", len(third), err)
	}
	if third[0].EndpointID != ep.ID {
		t.Fatalf("claimed endpoint %s, want %s", third[0].EndpointID, ep.ID)
	}
}

func TestBosunBackoffAndAutoDisable_RealPG(t *testing.T) {
	runBackoffAndAutoDisable(t, startPostgres(t))
}

func TestBosunBackoffAndAutoDisable_RealYugabyte(t *testing.T) {
	runBackoffAndAutoDisable(t, startYugabyte(t))
}

// runBackoffAndAutoDisable drives a delivery through the whole retry schedule
// against a failing receiver, settling every attempt, and then disables an
// endpoint whose failure streak reached both thresholds.
func runBackoffAndAutoDisable(t *testing.T, db *sql.DB) {
	store := newStore(t, db)
	ctx := context.Background()
	recv := newReceiver(t)
	recv.status.Store(500)
	tenant := newTenant()
	ep := createEndpoint(t, store, tenant, recv.server.URL+"/hook")
	backdateEndpoints(t, db)
	handler := &consumer.Handler{Recorder: store}
	_, msg := streamLiveRecord(t, tenant, topology.TopicDomainEvents)
	if err := handler.Handle(ctx, msg); err != nil {
		t.Fatal(err)
	}
	worker := &delivery.Worker{Store: store, Sender: &delivery.Sender{HTTP: recv.testClient(t)}, Jitter: fixedJitter}
	var deliveryID string
	for attempt := 1; attempt <= ledger.MaxAttempts; attempt++ {
		claims, err := store.Claim(ctx, 10)
		if err != nil || len(claims) != 1 {
			t.Fatalf("attempt %d claim = %d, %v", attempt, len(claims), err)
		}
		deliveryID = claims[0].DeliveryID
		worker.Process(claims[0], time.Now().Add(ledger.Lease))
		var st string
		var delay float64
		if err := db.QueryRow(`SELECT status, EXTRACT(EPOCH FROM next_attempt_at - updated_at)::float8 FROM bosun.webhook_deliveries WHERE id = $1`, deliveryID).Scan(&st, &delay); err != nil {
			t.Fatal(err)
		}
		if attempt < ledger.MaxAttempts {
			want := ledger.RetrySchedule[attempt-1].Seconds()
			if st != ledger.DeliveryPending || delay < want-1 || delay > want+1 {
				t.Fatalf("after attempt %d: %s, next in %.0fs; want pending, next in %.0fs", attempt, st, delay, want)
			}
			// The retry is due now instead of after the delay.
			if _, err := db.Exec(`UPDATE bosun.webhook_deliveries SET next_attempt_at = now() WHERE id = $1`, deliveryID); err != nil {
				t.Fatal(err)
			}
		} else if st != ledger.DeliveryFailed {
			t.Fatalf("after the last attempt the delivery is %s, want failed", st)
		}
	}
	if hits := recv.hits.Load(); hits != int64(ledger.MaxAttempts) {
		t.Fatalf("receiver got %d requests, want %d", hits, ledger.MaxAttempts)
	}
	got, err := store.GetEndpoint(ctx, tenant, ep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != ledger.StatusEnabled || got.ConsecutiveFailures != ledger.MaxAttempts || got.FailingSince == nil {
		t.Fatalf("after %d failures in minutes: %+v; want enabled with the streak recorded", ledger.MaxAttempts, got)
	}

	fail := func() ledger.SettleResult {
		t.Helper()
		_, m := streamLiveRecord(t, tenant, topology.TopicDomainEvents)
		if err := handler.Handle(ctx, m); err != nil {
			t.Fatal(err)
		}
		claims, err := store.Claim(ctx, 1)
		if err != nil || len(claims) != 1 {
			t.Fatalf("claim = %d, %v", len(claims), err)
		}
		res, err := store.Settle(ctx, claims[0], ledger.Outcome{StatusCode: 500, ErrorClass: delivery.ClassHTTPStatus, AttemptedAt: time.Now()}, fixedJitter)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	// Many failures, but the streak is younger than five days.
	if _, err := db.Exec(`UPDATE bosun.webhook_endpoints SET consecutive_failures = 40, failing_since = now() - interval '1 day' WHERE id = $1`, ep.ID); err != nil {
		t.Fatal(err)
	}
	if res := fail(); res.AutoDisabled {
		t.Fatal("disabled with a one-day streak")
	}
	// Five days old, but one failure short.
	if _, err := db.Exec(`UPDATE bosun.webhook_endpoints SET consecutive_failures = 18, failing_since = now() - interval '6 days' WHERE id = $1`, ep.ID); err != nil {
		t.Fatal(err)
	}
	if res := fail(); res.AutoDisabled {
		t.Fatal("disabled after 19 consecutive failures")
	}
	// A pending delivery that the disable must skip.
	_, pending := streamLiveRecord(t, tenant, topology.TopicDomainEvents)
	if err := handler.Handle(ctx, pending); err != nil {
		t.Fatal(err)
	}
	res := fail()
	if !res.AutoDisabled || res.SkippedDeliveries < 1 {
		t.Fatalf("the 20th failure over 6 days = %+v, want disabled with pending deliveries skipped", res)
	}
	got, err = store.GetEndpoint(ctx, tenant, ep.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != ledger.StatusDisabled || got.DisabledReason != ledger.DisabledByFailing {
		t.Fatalf("endpoint = %s/%s, want disabled/failing", got.Status, got.DisabledReason)
	}
	if n := countRows(t, db, `SELECT count(*) FROM bosun.webhook_deliveries WHERE endpoint_id = $1 AND status = 'pending'`, ep.ID); n != 0 {
		t.Fatalf("%d deliveries still pending after the disable", n)
	}
	if n := countRows(t, db, `SELECT count(*) FROM bosun.webhook_notification_outbox WHERE tenant_id = $1 AND endpoint_id = $2 AND kind = 'endpoint_disabled'`, tenant, ep.ID); n != 1 {
		t.Fatalf("notification rows = %d, want 1", n)
	}
	var eventType string
	var payload []byte
	if err := db.QueryRow(`SELECT event_type, payload FROM bosun.domain_event_outbox WHERE tenant_id = $1 AND aggregate_id = $2`, tenant, ep.ID).Scan(&eventType, &payload); err != nil {
		t.Fatalf("audit event: %v", err)
	}
	_, msgOut, err := events.Decode(eventType, payload)
	if err != nil {
		t.Fatal(err)
	}
	audit, ok := msgOut.(*internalv1.WebhookEndpointAutoDisabled)
	if eventType != "webhook.endpoint_auto_disabled" || !ok || audit.GetEndpointId() != ep.ID || audit.GetConsecutiveFailures() != 20 {
		t.Fatalf("audit event = %s %v", eventType, msgOut)
	}
	if _, _, err := store.ReplayRange(ctx, tenant, ep.ID, time.Now().Add(-time.Hour), time.Now().Add(time.Hour)); !errors.Is(err, ledger.ErrPrecondition) {
		t.Fatalf("replay into a disabled endpoint = %v, want a precondition failure", err)
	}

	// A success ends a streak.
	recv.status.Store(200)
	other := createEndpoint(t, store, tenant, recv.server.URL+"/other", "stream.live")
	if _, err := db.Exec(`UPDATE bosun.webhook_endpoints SET created_at = now() - interval '1 hour', consecutive_failures = 3, failing_since = now() - interval '1 hour' WHERE id = $1`, other.ID); err != nil {
		t.Fatal(err)
	}
	_, m := streamLiveRecord(t, tenant, topology.TopicDomainEvents)
	if err := handler.Handle(ctx, m); err != nil {
		t.Fatal(err)
	}
	claims, err := store.Claim(ctx, 10)
	if err != nil || len(claims) != 1 {
		t.Fatalf("claim = %d, %v", len(claims), err)
	}
	worker.Process(claims[0], time.Now().Add(ledger.Lease))
	got, err = store.GetEndpoint(ctx, tenant, other.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ConsecutiveFailures != 0 || got.FailingSince != nil || got.LastSuccessAt == nil {
		t.Fatalf("after a success: %+v, want the streak reset", got)
	}
}

func TestBosunReplayKeepsID_RealPG(t *testing.T) {
	runReplayKeepsID(t, startPostgres(t))
}

func TestBosunReplayKeepsID_RealYugabyte(t *testing.T) {
	runReplayKeepsID(t, startYugabyte(t))
}

// runReplayKeepsID replays a failed delivery and a range of failed
// deliveries. The replayed request carries the same webhook-id and body id as
// the original.
func runReplayKeepsID(t *testing.T, db *sql.DB) {
	store := newStore(t, db)
	ctx := context.Background()
	recv := newReceiver(t)
	recv.status.Store(500)
	tenant := newTenant()
	ep := createEndpoint(t, store, tenant, recv.server.URL+"/hook")
	backdateEndpoints(t, db)
	handler := &consumer.Handler{Recorder: store}
	ev, msg := streamLiveRecord(t, tenant, topology.TopicDomainEvents)
	if err := handler.Handle(ctx, msg); err != nil {
		t.Fatal(err)
	}
	// One attempt left in the schedule.
	if _, err := db.Exec(`UPDATE bosun.webhook_deliveries SET attempts = $1 WHERE event_id = $2`, ledger.MaxAttempts-1, ev.ID); err != nil {
		t.Fatal(err)
	}
	worker := &delivery.Worker{Store: store, Sender: &delivery.Sender{HTTP: recv.testClient(t)}, Jitter: fixedJitter}
	claims, err := store.Claim(ctx, 10)
	if err != nil || len(claims) != 1 {
		t.Fatalf("claim = %d, %v", len(claims), err)
	}
	deliveryID := claims[0].DeliveryID
	worker.Process(claims[0], time.Now().Add(ledger.Lease))
	failed, _, err := store.GetDelivery(ctx, tenant, deliveryID)
	if err != nil || failed.Status != ledger.DeliveryFailed {
		t.Fatalf("delivery = %+v, %v; want failed", failed, err)
	}

	recv.status.Store(200)
	replayed, err := store.ReplayDelivery(ctx, tenant, deliveryID)
	if err != nil {
		t.Fatal(err)
	}
	if replayed.ID != deliveryID || replayed.Status != ledger.DeliveryPending || replayed.Attempts != 0 || replayed.ReplayCount != 1 || replayed.EventID != ev.ID {
		t.Fatalf("replayed = %+v; want the same ID pending with a restarted schedule", replayed)
	}
	if _, err := store.ReplayDelivery(ctx, tenant, deliveryID); !errors.Is(err, ledger.ErrPrecondition) {
		t.Fatalf("replaying a pending delivery = %v, want a precondition failure", err)
	}
	claims, err = store.Claim(ctx, 10)
	if err != nil || len(claims) != 1 || claims[0].DeliveryID != deliveryID {
		t.Fatalf("replay claim = %+v, %v", claims, err)
	}
	worker.Process(claims[0], time.Now().Add(ledger.Lease))
	if recv.hits.Load() != 2 {
		t.Fatalf("receiver got %d requests, want the failed one and the replay", recv.hits.Load())
	}
	firstBody, firstHead := recv.request(0)
	replayBody, replayHead := recv.request(1)
	if firstHead.Get("webhook-id") != ev.ID || replayHead.Get("webhook-id") != ev.ID {
		t.Fatalf("webhook-id = %q then %q, want the event ID %s both times", firstHead.Get("webhook-id"), replayHead.Get("webhook-id"), ev.ID)
	}
	if string(firstBody) != string(replayBody) {
		t.Fatalf("replayed body differs:\n%s\n%s", firstBody, replayBody)
	}
	done, attempts, err := store.GetDelivery(ctx, tenant, deliveryID)
	if err != nil || done.Status != ledger.DeliverySucceeded || len(attempts) != 2 || attempts[1].AttemptNumber != 2 {
		t.Fatalf("after replay: %+v attempts %+v, %v", done, attempts, err)
	}
	srv := &grpcserver.Server{Store: store}
	asTenant := context.WithValue(ctx, ctxkeys.KeyTenantID, tenant)
	batch, err := srv.ListAttemptsForDeliveries(asTenant, &bosunpb.ListAttemptsForDeliveriesRequest{
		DeliveryIds: []string{deliveryID, "not-a-uuid", uuid.NewString(), deliveryID},
	})
	if err != nil || len(batch.GetDeliveries()) != 1 || batch.GetDeliveries()[0].GetDeliveryId() != deliveryID {
		t.Fatalf("batched attempts = %+v, %v; want only the one delivery with attempts", batch.GetDeliveries(), err)
	}
	got := batch.GetDeliveries()[0].GetAttempts()
	if len(got) != 2 || int(got[0].GetAttemptNumber()) != attempts[0].AttemptNumber || got[1].GetId() != attempts[1].ID {
		t.Fatalf("batched attempts = %+v, want %+v oldest first", got, attempts)
	}
	asOther := context.WithValue(ctx, ctxkeys.KeyTenantID, newTenant())
	other, err := srv.ListAttemptsForDeliveries(asOther, &bosunpb.ListAttemptsForDeliveriesRequest{DeliveryIds: []string{deliveryID}})
	if err != nil || len(other.GetDeliveries()) != 0 {
		t.Fatalf("another tenant reads %+v, %v; want no attempts", other.GetDeliveries(), err)
	}

	// Range replay: 1001 failed deliveries in the range, one outside it.
	if _, err := db.Exec(`
		WITH ev AS (
			INSERT INTO bosun.webhook_events (tenant_id, event_id, event_type, schema_name, subject, payload, occurred_at)
			SELECT $1, gen_random_uuid(), 'stream.live', 'frameworks.events.public.v1.StreamLive', 'streams/x', '\x'::bytea, now()
			FROM generate_series(1, 1002)
			RETURNING event_id
		)
		INSERT INTO bosun.webhook_deliveries (id, tenant_id, endpoint_id, event_id, event_type, status, attempts, created_at)
		SELECT gen_random_uuid(), $1, $2, event_id, 'stream.live', 'failed', 12, now() - interval '2 hours'
		FROM ev`, tenant, ep.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE bosun.webhook_deliveries SET created_at = now() - interval '10 days'
		WHERE id = (SELECT id FROM bosun.webhook_deliveries WHERE status = 'failed' AND endpoint_id = $1 LIMIT 1)`, ep.ID); err != nil {
		t.Fatal(err)
	}
	from, to := time.Now().Add(-3*time.Hour), time.Now().Add(-time.Hour)
	n, more, err := store.ReplayRange(ctx, tenant, ep.ID, from, to)
	if err != nil || n != ledger.ReplayRangeLimit || !more {
		t.Fatalf("first range replay = %d, more %v, %v; want %d with more", n, more, err, ledger.ReplayRangeLimit)
	}
	n, more, err = store.ReplayRange(ctx, tenant, ep.ID, from, to)
	if err != nil || n != 1 || more {
		t.Fatalf("second range replay = %d, more %v, %v; want the last one", n, more, err)
	}
	if left := countRows(t, db, `SELECT count(*) FROM bosun.webhook_deliveries WHERE endpoint_id = $1 AND status = 'failed'`, ep.ID); left != 1 {
		t.Fatalf("%d failed deliveries left, want only the one outside the range", left)
	}
}

func TestBosunPruning_RealPG(t *testing.T) {
	runPruning(t, startPostgres(t))
}

func TestBosunPruning_RealYugabyte(t *testing.T) {
	runPruning(t, startYugabyte(t))
}

// runPruning ages rows past retention and prunes them.
func runPruning(t *testing.T, db *sql.DB) {
	store := newStore(t, db)
	ctx := context.Background()
	tenant := newTenant()
	ep := createEndpoint(t, store, tenant, "https://prune.example/hook")
	if _, _, err := store.RotateSecret(ctx, tenant, ep.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE bosun.webhook_endpoint_secrets SET expires_at = now() - interval '1 minute' WHERE state = 'previous'`); err != nil {
		t.Fatal(err)
	}
	insert := func(age, deliveryStatus string) string {
		t.Helper()
		var eventID string
		if err := db.QueryRow(`
			INSERT INTO bosun.webhook_events (tenant_id, event_id, event_type, schema_name, subject, payload, occurred_at, received_at)
			VALUES ($1, gen_random_uuid(), 'stream.live', 'frameworks.events.public.v1.StreamLive', 'streams/x', '\x'::bytea, now() - $2::interval, now() - $2::interval)
			RETURNING event_id::text`, tenant, age).Scan(&eventID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`
			INSERT INTO bosun.webhook_deliveries (id, tenant_id, endpoint_id, event_id, event_type, status, created_at)
			VALUES (gen_random_uuid(), $1, $2, $3, 'stream.live', $4, now() - $5::interval)`, tenant, ep.ID, eventID, deliveryStatus, age); err != nil {
			t.Fatal(err)
		}
		return eventID
	}
	old := insert("31 days", ledger.DeliverySucceeded)
	oldPending := insert("31 days", ledger.DeliveryPending)
	young := insert("29 days", ledger.DeliveryFailed)
	if _, err := db.Exec(`
		INSERT INTO bosun.webhook_deliveries (id, tenant_id, endpoint_id, event_type, kind, status, created_at)
		VALUES (gen_random_uuid(), $1, $2, 'webhook.test', 'test', 'succeeded', now() - interval '31 days')`, tenant, ep.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO bosun.webhook_notification_outbox (id, tenant_id, endpoint_id, kind, endpoint_url, completed_at)
		VALUES (gen_random_uuid(), $1, $2, 'endpoint_disabled', 'https://prune.example/hook', now() - interval '31 days')`, tenant, ep.ID); err != nil {
		t.Fatal(err)
	}

	result, err := store.Prune(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if result.Events != 1 || result.TestDeliveries != 1 || result.Notifications != 1 || result.Secrets != 1 {
		t.Fatalf("pruned %+v, want one of each", result)
	}
	for _, c := range []struct {
		id   string
		want int
	}{{old, 0}, {oldPending, 1}, {young, 1}} {
		if n := countRows(t, db, `SELECT count(*) FROM bosun.webhook_events WHERE event_id = $1`, c.id); n != c.want {
			t.Errorf("event %s: %d rows, want %d", c.id, n, c.want)
		}
	}
	if n := countRows(t, db, `SELECT count(*) FROM bosun.webhook_deliveries WHERE event_id = $1`, old); n != 0 {
		t.Fatalf("the pruned event's delivery survived")
	}
	if n := countRows(t, db, `SELECT count(*) FROM bosun.webhook_endpoint_secrets WHERE endpoint_id = $1`, ep.ID); n != 1 {
		t.Fatalf("secrets = %d, want only the active one", n)
	}
}

// TestBosunEndpointLimitAndSecrets_RealPG caps concurrent creates at ten per
// tenant, keeps signing secrets encrypted at rest, and signs with both
// secrets during a rotation.
func TestBosunEndpointLimitAndSecrets_RealPG(t *testing.T) {
	db := startPostgres(t)
	store := newStore(t, db)
	ctx := context.Background()
	tenant := newTenant()
	var wg sync.WaitGroup
	var mu sync.Mutex
	created, limited := 0, 0
	for i := range 14 {
		wg.Go(func() {
			_, _, err := store.CreateEndpoint(ctx, tenant, ledger.NewEndpoint{URL: "https://limit.example/" + string(rune('a'+i)), EventTypes: []string{"*"}, APIVersion: "v1"})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				created++
			case errors.Is(err, ledger.ErrEndpointLimit):
				limited++
			default:
				t.Errorf("create: %v", err)
			}
		})
	}
	wg.Wait()
	if created != ledger.MaxEndpointsPerTenant || limited != 4 {
		t.Fatalf("concurrent creates: %d created, %d limited; want 10 and 4", created, limited)
	}
	endpoints, err := store.ListEndpoints(ctx, tenant)
	if err != nil || len(endpoints) != 10 {
		t.Fatalf("list = %d, %v", len(endpoints), err)
	}
	if err := store.DeleteEndpoint(ctx, tenant, endpoints[0].ID); err != nil {
		t.Fatal(err)
	}
	ep, key, err := store.CreateEndpoint(ctx, tenant, ledger.NewEndpoint{URL: "https://limit.example/new", EventTypes: []string{"*"}, APIVersion: "v1"})
	if err != nil {
		t.Fatalf("create after a delete: %v", err)
	}
	var ciphertext string
	if err := db.QueryRow(`SELECT secret_ciphertext FROM bosun.webhook_endpoint_secrets WHERE endpoint_id = $1`, ep.ID).Scan(&ciphertext); err != nil {
		t.Fatal(err)
	}
	if ciphertext == key.String() || len(ciphertext) < 8 || ciphertext[:7] != "enc:v3:" {
		t.Fatalf("stored secret %q is not a field-encrypted envelope", ciphertext)
	}
	keys, err := store.SigningKeys(ctx, tenant, ep.ID)
	if err != nil || len(keys) != 1 || keys[0].String() != key.String() {
		t.Fatalf("signing keys = %v, %v", keys, err)
	}
	_, rotated, err := store.RotateSecret(ctx, tenant, ep.ID, false)
	if err != nil {
		t.Fatal(err)
	}
	keys, err = store.SigningKeys(ctx, tenant, ep.ID)
	if err != nil || len(keys) != 2 || keys[0].String() != rotated.String() || keys[1].String() != key.String() {
		t.Fatalf("during rotation the keys are %v, %v; want the new then the previous", keys, err)
	}
	got, err := store.GetEndpoint(ctx, tenant, ep.ID)
	if err != nil || got.PreviousSecretExpiresAt == nil || time.Until(*got.PreviousSecretExpiresAt) < 23*time.Hour {
		t.Fatalf("previous secret expiry = %v, %v; want about 24 hours", got.PreviousSecretExpiresAt, err)
	}
	_, revoked, err := store.RotateSecret(ctx, tenant, ep.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	keys, err = store.SigningKeys(ctx, tenant, ep.ID)
	if err != nil || len(keys) != 1 || keys[0].String() != revoked.String() {
		t.Fatalf("after revoking, keys = %v, %v; want only the newest", keys, err)
	}
}
