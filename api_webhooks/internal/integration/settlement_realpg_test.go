//go:build schema_verify

package integration

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"frameworks/api_webhooks/internal/consumer"
	"frameworks/api_webhooks/internal/delivery"
	"frameworks/api_webhooks/internal/ledger"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/topology"
)

type deliveryState struct {
	status, errorClass string
	attempts           int
	leased             bool
	nextAttemptAt      time.Time
}

func readDeliveryState(t *testing.T, db *sql.DB, deliveryID string) deliveryState {
	t.Helper()
	var s deliveryState
	var class sql.NullString
	if err := db.QueryRow(`
		SELECT status, last_error_class, attempts, lease_token IS NOT NULL, next_attempt_at
		FROM bosun.webhook_deliveries WHERE id = $1`, deliveryID).
		Scan(&s.status, &class, &s.attempts, &s.leased, &s.nextAttemptAt); err != nil {
		t.Fatal(err)
	}
	s.errorClass = class.String
	return s
}

func TestBosunInternalFailureSettles_RealPG(t *testing.T) {
	runInternalFailureSettles(t, startPostgres(t))
}

func TestBosunInternalFailureSettles_RealYugabyte(t *testing.T) {
	runInternalFailureSettles(t, startYugabyte(t))
}

// runInternalFailureSettles drives Worker.Process into Bosun-side failures. A
// permanent one (an undecryptable signing secret, a stored schema that does
// not match the registry) fails the delivery at once; a transient one (a type
// this replica does not register) releases the lease and retries later
// without spending an attempt. Neither holds a lease slot or counts against
// the endpoint's failure streak.
func runInternalFailureSettles(t *testing.T, db *sql.DB) {
	store := newStore(t, db)
	ctx := context.Background()
	recv := newReceiver(t)
	handler := &consumer.Handler{Recorder: store}
	worker := &delivery.Worker{Store: store, Sender: &delivery.Sender{HTTP: recv.testClient(t)}, Jitter: fixedJitter}

	claimOne := func(t *testing.T, tenant string) ledger.Claim {
		t.Helper()
		_, msg := streamLiveRecord(t, tenant, topology.TopicDomainEvents)
		if err := handler.Handle(ctx, msg); err != nil {
			t.Fatal(err)
		}
		claims, err := store.Claim(ctx, 10)
		if err != nil || len(claims) != 1 {
			t.Fatalf("claim = %d, %v", len(claims), err)
		}
		return claims[0]
	}
	streak := func(t *testing.T, endpointID string) int {
		t.Helper()
		return countRows(t, db, `SELECT consecutive_failures FROM bosun.webhook_endpoints WHERE id = $1`, endpointID)
	}

	t.Run("an undecryptable secret fails the delivery", func(t *testing.T) {
		tenant := newTenant()
		ep := createEndpoint(t, store, tenant, recv.server.URL+"/undecryptable")
		backdateEndpoints(t, db)
		if _, err := db.Exec(`UPDATE bosun.webhook_endpoint_secrets SET secret_ciphertext = 'not-a-ciphertext' WHERE endpoint_id = $1`, ep.ID); err != nil {
			t.Fatal(err)
		}
		c := claimOne(t, tenant)
		worker.Process(c, time.Now().Add(ledger.Lease))
		got := readDeliveryState(t, db, c.DeliveryID)
		if got.status != ledger.DeliveryFailed || got.errorClass != delivery.ClassInternal || got.leased || got.attempts != 0 {
			t.Fatalf("delivery = %+v; want failed with class %q, no lease, no attempt", got, delivery.ClassInternal)
		}
		if n := streak(t, ep.ID); n != 0 {
			t.Fatalf("endpoint failure streak = %d, want 0: a Bosun-side failure is not the endpoint's", n)
		}
		if recv.hits.Load() != 0 {
			t.Fatal("an unsigned delivery reached the receiver")
		}
	})

	t.Run("a stored schema the registry does not match fails the delivery", func(t *testing.T) {
		tenant := newTenant()
		ep := createEndpoint(t, store, tenant, recv.server.URL+"/schema")
		backdateEndpoints(t, db)
		_, msg := streamLiveRecord(t, tenant, topology.TopicDomainEvents)
		if err := handler.Handle(ctx, msg); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE bosun.webhook_events SET schema_name = 'frameworks.events.public.v1.StreamIdle' WHERE tenant_id = $1`, tenant); err != nil {
			t.Fatal(err)
		}
		claims, err := store.Claim(ctx, 10)
		if err != nil || len(claims) != 1 {
			t.Fatalf("claim = %d, %v", len(claims), err)
		}
		worker.Process(claims[0], time.Now().Add(ledger.Lease))
		got := readDeliveryState(t, db, claims[0].DeliveryID)
		if got.status != ledger.DeliveryFailed || got.errorClass != delivery.ClassInternal || got.leased {
			t.Fatalf("delivery = %+v; want failed with class %q and no lease", got, delivery.ClassInternal)
		}
		if n := streak(t, ep.ID); n != 0 {
			t.Fatalf("endpoint failure streak = %d, want 0", n)
		}
	})

	t.Run("a type this replica does not register is retried later", func(t *testing.T) {
		tenant := newTenant()
		ep := createEndpoint(t, store, tenant, recv.server.URL+"/unknown")
		backdateEndpoints(t, db)
		_, msg := streamLiveRecord(t, tenant, topology.TopicDomainEvents)
		if err := handler.Handle(ctx, msg); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE bosun.webhook_events SET event_type = 'stream.teleported' WHERE tenant_id = $1`, tenant); err != nil {
			t.Fatal(err)
		}
		claims, err := store.Claim(ctx, 10)
		if err != nil || len(claims) != 1 {
			t.Fatalf("claim = %d, %v", len(claims), err)
		}
		before := time.Now()
		worker.Process(claims[0], time.Now().Add(ledger.Lease))
		got := readDeliveryState(t, db, claims[0].DeliveryID)
		if got.status != ledger.DeliveryPending || got.errorClass != delivery.ClassInternal || got.leased || got.attempts != 0 {
			t.Fatalf("delivery = %+v; want pending, unleased, class %q, no attempt spent", got, delivery.ClassInternal)
		}
		if !got.nextAttemptAt.After(before.Add(10 * time.Second)) {
			t.Fatalf("next attempt at %s, want a backoff after %s", got.nextAttemptAt, before)
		}
		if n := streak(t, ep.ID); n != 0 {
			t.Fatalf("endpoint failure streak = %d, want 0", n)
		}
		if again, err := store.Claim(ctx, 10); err != nil || len(again) != 0 {
			t.Fatalf("the backed-off delivery was claimed again at once: %+v, %v", again, err)
		}
	})
}

// waitForLockWaiters blocks until at least n backends of this database wait on
// a heavyweight lock.
func waitForLockWaiters(t *testing.T, db *sql.DB, n int) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		if countRows(t, db, `SELECT count(*) FROM pg_stat_activity WHERE datname = current_database() AND wait_event_type = 'Lock'`) >= n {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("fewer than %d backends waiting on a lock", n)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestBosunPruneKeepsReplayedDelivery_RealPG interleaves Prune with a replay
// of a failed delivery whose event is past retention. Prune has picked the
// event (no pending delivery) and is held on the event row; the replay then
// returns the delivery to pending. Either the replay waits for Prune and then
// finds the delivery gone, or Prune sees the pending delivery and keeps the
// event: a replay that reported success is never deleted.
func TestBosunPruneKeepsReplayedDelivery_RealPG(t *testing.T) {
	db := startPostgres(t)
	store := newStore(t, db)
	ctx := context.Background()
	tenant := newTenant()
	ep := createEndpoint(t, store, tenant, "https://prune-replay.example/hook")

	var eventID, deliveryID string
	if err := db.QueryRow(`
		INSERT INTO bosun.webhook_events (tenant_id, event_id, event_type, schema_name, subject, payload, occurred_at, received_at)
		VALUES ($1, gen_random_uuid(), 'stream.live', 'frameworks.events.public.v1.StreamLive', 'streams/x', '\x'::bytea, now() - interval '31 days', now() - interval '31 days')
		RETURNING event_id::text`, tenant).Scan(&eventID); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`
		INSERT INTO bosun.webhook_deliveries (id, tenant_id, endpoint_id, event_id, event_type, status, attempts, created_at)
		VALUES (gen_random_uuid(), $1, $2, $3, 'stream.live', 'failed', 12, now() - interval '31 days')
		RETURNING id::text`, tenant, ep.ID, eventID).Scan(&deliveryID); err != nil {
		t.Fatal(err)
	}

	// The blocker holds the event row, so Prune stops at its delete after it
	// chose the event.
	blocker, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Rollback() }()
	if _, err := blocker.ExecContext(ctx, `SELECT 1 FROM bosun.webhook_events WHERE event_id = $1 FOR UPDATE`, eventID); err != nil {
		t.Fatal(err)
	}

	pruneDone := make(chan error, 1)
	go func() {
		_, pruneErr := store.Prune(ctx, 100)
		pruneDone <- pruneErr
	}()
	waitForLockWaiters(t, db, 1)

	type replayResult struct {
		d   ledger.Delivery
		err error
	}
	replayDone := make(chan replayResult, 1)
	go func() {
		d, replayErr := store.ReplayDelivery(ctx, tenant, deliveryID)
		replayDone <- replayResult{d, replayErr}
	}()
	// The replay either commits at once or waits behind Prune's endpoint lock.
	select {
	case r := <-replayDone:
		replayDone <- r
	case <-time.After(2 * time.Second):
		waitForLockWaiters(t, db, 2)
	}

	if err := blocker.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-pruneDone; err != nil {
		t.Fatalf("prune: %v", err)
	}
	r := <-replayDone

	remaining := countRows(t, db, `SELECT count(*) FROM bosun.webhook_deliveries WHERE id = $1 AND status = 'pending'`, deliveryID)
	switch {
	case r.err == nil:
		if r.d.Status != ledger.DeliveryPending || remaining != 1 {
			t.Fatalf("replay reported %+v, but %d pending rows remain after prune; want the replayed delivery kept", r.d, remaining)
		}
		if n := countRows(t, db, `SELECT count(*) FROM bosun.webhook_events WHERE event_id = $1`, eventID); n != 1 {
			t.Fatalf("the replayed delivery's event was pruned")
		}
	case errors.Is(r.err, ledger.ErrNotFound):
		if n := countRows(t, db, `SELECT count(*) FROM bosun.webhook_deliveries WHERE id = $1`, deliveryID); n != 0 {
			t.Fatalf("replay reported not found, but the delivery exists")
		}
	default:
		t.Fatalf("replay: %v; want success or not found", r.err)
	}
}
