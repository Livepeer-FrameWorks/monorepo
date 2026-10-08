//go:build schema_verify

package integration

import (
	"context"
	"testing"
	"time"

	"frameworks/api_webhooks/internal/consumer"
	"frameworks/api_webhooks/internal/delivery"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/topology"
)

// TestBosunClaimSkipsLockedEndpoint_RealPG holds one endpoint's row lock in
// another transaction, as a peer replica's claim or a settlement does, and
// claims. The claim takes the other endpoint's delivery at once instead of
// queueing behind the lock, and the held endpoint's delivery is claimed once
// the lock is released.
func TestBosunClaimSkipsLockedEndpoint_RealPG(t *testing.T) {
	db := startPostgres(t)
	store := newStore(t, db)
	ctx := context.Background()
	recv := newReceiver(t)
	tenant := newTenant()
	held := createEndpoint(t, store, tenant, recv.server.URL+"/held")
	free := createEndpoint(t, store, tenant, recv.server.URL+"/free")
	backdateEndpoints(t, db)
	handler := &consumer.Handler{Recorder: store}
	_, msg := streamLiveRecord(t, tenant, topology.TopicDomainEvents)
	if err := handler.Handle(ctx, msg); err != nil {
		t.Fatal(err)
	}

	peer, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = peer.Rollback() }()
	if _, err := peer.ExecContext(ctx, `SELECT id FROM bosun.webhook_endpoints WHERE tenant_id = $1 AND id = $2 FOR UPDATE`, tenant, held.ID); err != nil {
		t.Fatal(err)
	}

	claimCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	start := time.Now()
	claims, err := store.Claim(claimCtx, 10)
	if err != nil {
		t.Fatalf("claim while a peer holds one endpoint = %v after %s; want the other endpoint's delivery without waiting", err, time.Since(start))
	}
	if len(claims) != 1 || claims[0].EndpointID != free.ID {
		t.Fatalf("claims = %+v, want exactly the unlocked endpoint's delivery", claims)
	}

	if err := peer.Rollback(); err != nil {
		t.Fatal(err)
	}
	again, err := store.Claim(ctx, 10)
	if err != nil || len(again) != 1 || again[0].EndpointID != held.ID {
		t.Fatalf("claim after the peer released = %+v, %v; want the held endpoint's delivery", again, err)
	}
}

// TestBosunWorkerClaimRunsOneRetryLayer_RealPG aborts every claim transaction
// with SQLSTATE 40001 when it leases a delivery, as a YugabyteDB conflict
// does, and counts the transactions one worker poll runs. Store.Claim replays
// its own transaction, so a poll runs DefaultRetryAttempts transactions and
// then waits for the next poll, instead of replaying the whole replay loop.
func TestBosunWorkerClaimRunsOneRetryLayer_RealPG(t *testing.T) {
	db := startPostgres(t)
	store := newStore(t, db)
	ctx := context.Background()
	recv := newReceiver(t)
	tenant := newTenant()
	createEndpoint(t, store, tenant, recv.server.URL+"/hook")
	backdateEndpoints(t, db)
	handler := &consumer.Handler{Recorder: store}
	_, msg := streamLiveRecord(t, tenant, topology.TopicDomainEvents)
	if err := handler.Handle(ctx, msg); err != nil {
		t.Fatal(err)
	}
	// nextval is not rolled back with the aborted transaction, so the
	// sequence counts the claim transactions that reached the lease.
	for _, stmt := range []string{
		`CREATE SEQUENCE bosun.test_claim_attempts`,
		`CREATE FUNCTION bosun.test_abort_lease() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN
			PERFORM nextval('bosun.test_claim_attempts');
			RAISE EXCEPTION 'restart read required' USING ERRCODE = '40001';
		END $$`,
		`CREATE TRIGGER test_abort_lease BEFORE UPDATE OF lease_token ON bosun.webhook_deliveries
		FOR EACH ROW EXECUTE FUNCTION bosun.test_abort_lease()`,
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}

	runCtx, stop := context.WithCancel(ctx)
	worker := &delivery.Worker{Store: store, Sender: &delivery.Sender{HTTP: recv.testClient(t)}, PollInterval: time.Hour}
	done := make(chan struct{})
	go func() {
		defer close(done)
		worker.Run(runCtx)
	}()
	// One poll's claim, replays included, ends well inside this window; a
	// second retry layer would still be replaying.
	time.Sleep(8 * time.Second)
	stop()
	<-done

	var attempts int64
	if err := db.QueryRowContext(ctx, `SELECT CASE WHEN is_called THEN last_value ELSE 0 END FROM bosun.test_claim_attempts`).Scan(&attempts); err != nil {
		t.Fatal(err)
	}
	if attempts != database.DefaultRetryAttempts {
		t.Fatalf("one worker poll ran %d claim transactions, want %d: one retry layer", attempts, database.DefaultRetryAttempts)
	}
	if hits := recv.hits.Load(); hits != 0 {
		t.Fatalf("receiver got %d requests although every claim aborted", hits)
	}
}
