//go:build schema_verify

package outbox

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	eventspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	publicv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/public/v1"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	_ "github.com/yugabyte/pgx/v5/stdlib"
)

const (
	testSchema = "svc"
	tenantA    = "5b0f1e5e-2a55-4b0f-9d3c-6f1a2b3c4d5e"
	streamOne  = "a3c7d9e1-0000-4000-8000-000000000001"
	streamTwo  = "a3c7d9e1-0000-4000-8000-000000000002"
)

// startOutboxRealPG runs PostgreSQL in Docker and creates the outbox from
// TableDDL, the definition service migrations copy.
func startOutboxRealPG(t *testing.T) *sql.DB {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("fw-domain-event-outbox-realpg-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = dockerpg.CLI("rm", "-fv", name) })
	image, err := dockerpg.PostgresImage()
	if err != nil {
		t.Fatal(err)
	}
	if output, err := dockerpg.Run("run", "-d", "--name", name, "-P", "-e", "POSTGRES_PASSWORD=harness", image); err != nil {
		t.Fatalf("docker run: %v\n%s", err, output)
	}
	port, err := dockerpg.DiscoverPublishedHostPort(name, "5432/tcp")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("pgx", fmt.Sprintf("postgres://postgres:harness@127.0.0.1:%s/postgres?sslmode=disable", port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := dockerpg.WaitReady(db, name); err != nil {
		t.Fatal(err)
	}
	ddl, err := TableDDL(testSchema)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE SCHEMA ` + testSchema + `; ` + ddl); err != nil {
		t.Fatal(err)
	}
	return db
}

// recordingPublisher records every batch it receives. failIDs makes the
// publish of any batch containing one of those events fail. afterPublish runs
// after a batch was recorded and before the publish returns.
type recordingPublisher struct {
	mu           sync.Mutex
	batches      [][]*eventspb.DomainEvent
	failIDs      map[string]bool
	afterPublish func()
	err          error
}

func (p *recordingPublisher) PublishDomainEvents(_ context.Context, batch *eventspb.DomainEventBatch) error {
	p.mu.Lock()
	p.batches = append(p.batches, batch.GetEvents())
	after, err := p.afterPublish, p.err
	for _, ev := range batch.GetEvents() {
		if p.failIDs[ev.GetId()] {
			err = errors.New("decklog rejected the batch")
		}
	}
	p.mu.Unlock()
	if after != nil {
		after()
	}
	return err
}

func (p *recordingPublisher) publishedIDs() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	var ids []string
	for _, batch := range p.batches {
		for _, ev := range batch {
			ids = append(ids, ev.GetId())
		}
	}
	return ids
}

func enqueueTx(t *testing.T, db *sql.DB, evs ...events.Event) {
	t.Helper()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for _, ev := range evs {
		if err := Enqueue(context.Background(), tx, testSchema, ev); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func streamEvent(t *testing.T, stream string) events.Event {
	t.Helper()
	ev, err := events.New("commodore", tenantA, stream, &publicv1.StreamUpdated{StreamId: stream, ChangedFields: []string{"name"}})
	if err != nil {
		t.Fatal(err)
	}
	return ev
}

func newTestRelay(t *testing.T, db *sql.DB, pub Publisher) *Relay {
	t.Helper()
	relay, err := NewRelay(db, testSchema, pub, logging.NewLogger())
	if err != nil {
		t.Fatal(err)
	}
	return relay
}

func rowState(t *testing.T, db *sql.DB, id string) (attempts int, completed bool) {
	t.Helper()
	if err := db.QueryRow(`SELECT attempts, completed_at IS NOT NULL FROM `+testSchema+`.domain_event_outbox WHERE event_id = $1::uuid`, id).
		Scan(&attempts, &completed); err != nil {
		t.Fatal(err)
	}
	return attempts, completed
}

func makeDue(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`UPDATE ` + testSchema + `.domain_event_outbox SET next_attempt_at = now() WHERE completed_at IS NULL`); err != nil {
		t.Fatal(err)
	}
}

func TestDomainEventOutbox_RealPG(t *testing.T) {
	db := startOutboxRealPG(t)
	ctx := context.Background()
	reset := func(t *testing.T) {
		t.Helper()
		if _, err := db.Exec(`DELETE FROM ` + testSchema + `.domain_event_outbox`); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("cancelled dispatch does not start per-row fallback", func(t *testing.T) {
		reset(t)
		first, second := streamEvent(t, streamOne), streamEvent(t, streamTwo)
		enqueueTx(t, db, first, second)
		dispatchCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		pub := &recordingPublisher{afterPublish: cancel, err: errors.New("batch rejected")}
		if n, err := newTestRelay(t, db, pub).DispatchOnce(dispatchCtx); n != 2 || !errors.Is(err, context.Canceled) {
			t.Fatalf("dispatch = %d, %v", n, err)
		}
		if len(pub.batches) != 1 {
			t.Fatalf("started %d publishes after cancellation", len(pub.batches)-1)
		}
		for _, ev := range []events.Event{first, second} {
			if _, completed := rowState(t, db, ev.ID); completed {
				t.Fatal("unacknowledged event completed")
			}
		}
	})

	t.Run("event id is stable across a redispatch after a lost acknowledgment", func(t *testing.T) {
		reset(t)
		ev := streamEvent(t, streamOne)
		enqueueTx(t, db, ev)

		// Decklog wrote the batch but the relay never learned it: its context
		// ends before completion is recorded, so the row stays leased.
		dispatchCtx, cancel := context.WithCancel(ctx)
		lost := &recordingPublisher{afterPublish: cancel}
		if n, err := newTestRelay(t, db, lost).DispatchOnce(dispatchCtx); err != nil || n != 1 {
			t.Fatalf("first dispatch = %d, %v", n, err)
		}
		if _, completed := rowState(t, db, ev.ID); completed {
			t.Fatal("row completed although the acknowledgment was lost")
		}

		// Another replica cannot claim the row while the lease holds.
		retry := &recordingPublisher{}
		if n, err := newTestRelay(t, db, retry).DispatchOnce(ctx); err != nil || n != 0 {
			t.Fatalf("dispatch during the lease = %d, %v", n, err)
		}
		if _, err := db.Exec(`UPDATE `+testSchema+`.domain_event_outbox SET claimed_at = now() - interval '2 minutes' WHERE event_id = $1::uuid`, ev.ID); err != nil {
			t.Fatal(err)
		}
		if n, err := newTestRelay(t, db, retry).DispatchOnce(ctx); err != nil || n != 1 {
			t.Fatalf("dispatch after the lease = %d, %v", n, err)
		}
		first, second := lost.publishedIDs(), retry.publishedIDs()
		if len(first) != 1 || len(second) != 1 || first[0] != ev.ID || second[0] != ev.ID {
			t.Fatalf("published ids %v then %v, want %s both times", first, second, ev.ID)
		}
		if _, completed := rowState(t, db, ev.ID); !completed {
			t.Fatal("row not completed after the acknowledged redispatch")
		}
		env := retry.batches[0][0]
		if env.GetType() != "stream.updated" || env.GetTenantId() != tenantA || env.GetAggregateId() != streamOne ||
			env.GetSource() != "commodore" || !env.GetTime().AsTime().Equal(ev.Time) {
			t.Fatalf("redispatched envelope = %+v", env)
		}
		if _, _, err := events.Validate(env); err != nil {
			t.Fatalf("redispatched envelope is not valid for Decklog: %v", err)
		}
	})

	t.Run("a failed publish keeps the id and records the attempt", func(t *testing.T) {
		reset(t)
		ev := streamEvent(t, streamOne)
		enqueueTx(t, db, ev)
		failing := &recordingPublisher{err: errors.New("decklog unavailable")}
		if _, err := newTestRelay(t, db, failing).DispatchOnce(ctx); err != nil {
			t.Fatal(err)
		}
		if attempts, completed := rowState(t, db, ev.ID); attempts != 1 || completed {
			t.Fatalf("after failure attempts=%d completed=%v", attempts, completed)
		}
		if n, _ := newTestRelay(t, db, &recordingPublisher{}).DispatchOnce(ctx); n != 0 {
			t.Fatal("row claimed again before its backoff elapsed")
		}
		makeDue(t, db)
		ok := &recordingPublisher{}
		if n, err := newTestRelay(t, db, ok).DispatchOnce(ctx); err != nil || n != 1 {
			t.Fatalf("retry = %d, %v", n, err)
		}
		if ids := ok.publishedIDs(); len(ids) != 1 || ids[0] != ev.ID || failing.publishedIDs()[0] != ev.ID {
			t.Fatalf("retry published %v after %v, want %s", ids, failing.publishedIDs(), ev.ID)
		}
	})

	t.Run("a second row of an aggregate waits for the first", func(t *testing.T) {
		reset(t)
		first, second := streamEvent(t, streamOne), streamEvent(t, streamOne)
		other := streamEvent(t, streamTwo)
		enqueueTx(t, db, first, second, other)

		pub := &recordingPublisher{failIDs: map[string]bool{first.ID: true}}
		relay := newTestRelay(t, db, pub)
		if n, err := relay.DispatchOnce(ctx); err != nil || n != 2 {
			t.Fatalf("first dispatch claimed %d, %v; want the head of each aggregate", n, err)
		}
		for _, id := range pub.publishedIDs() {
			if id == second.ID {
				t.Fatal("second row of the aggregate was published while the first was incomplete")
			}
		}
		if _, completed := rowState(t, db, other.ID); !completed {
			t.Fatal("the failing aggregate held back another aggregate's event")
		}
		// The head fails again; the second row stays unclaimed although it
		// is due.
		makeDue(t, db)
		if n, err := relay.DispatchOnce(ctx); err != nil || n != 1 {
			t.Fatalf("retry of the head claimed %d rows, %v", n, err)
		}
		for _, id := range pub.publishedIDs() {
			if id == second.ID {
				t.Fatal("second row claimed while the first keeps failing")
			}
		}
		if attempts, _ := rowState(t, db, first.ID); attempts != 2 {
			t.Fatalf("head attempts = %d, want 2", attempts)
		}

		pub.mu.Lock()
		pub.failIDs = nil
		pub.mu.Unlock()
		makeDue(t, db)
		var order []string
		for i := 0; i < 3; i++ {
			before := len(pub.publishedIDs())
			if _, err := relay.DispatchOnce(ctx); err != nil {
				t.Fatal(err)
			}
			order = append(order, pub.publishedIDs()[before:]...)
		}
		if len(order) != 2 || order[0] != first.ID || order[1] != second.ID {
			t.Fatalf("delivery after recovery = %v, want [%s %s]", order, first.ID, second.ID)
		}
	})

	t.Run("enqueue commits and rolls back with the state change", func(t *testing.T) {
		reset(t)
		ev := streamEvent(t, streamOne)
		tx, err := db.Begin()
		if err != nil {
			t.Fatal(err)
		}
		if err := Enqueue(ctx, tx, testSchema, ev); err != nil {
			t.Fatal(err)
		}
		if err := tx.Rollback(); err != nil {
			t.Fatal(err)
		}
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM ` + testSchema + `.domain_event_outbox`).Scan(&count); err != nil || count != 0 {
			t.Fatalf("rolled-back enqueue left %d rows (%v)", count, err)
		}
	})

	t.Run("platform rows store no tenant and tenant rows require one", func(t *testing.T) {
		reset(t)
		ev, err := events.New("quartermaster", "", "cluster-a", &internalv1.ClusterCreated{ClusterId: "cluster-a", OwnerTenantId: tenantA})
		if err != nil {
			t.Fatal(err)
		}
		enqueueTx(t, db, ev)
		var scope string
		var tenant sql.NullString
		if err := db.QueryRow(`SELECT scope, tenant_id::text FROM `+testSchema+`.domain_event_outbox WHERE event_id = $1::uuid`, ev.ID).Scan(&scope, &tenant); err != nil {
			t.Fatal(err)
		}
		if scope != "platform" || tenant.Valid {
			t.Fatalf("platform row scope=%q tenant=%v", scope, tenant)
		}
		_, err = db.Exec(`INSERT INTO `+testSchema+`.domain_event_outbox (event_id, event_type, source, aggregate_type, aggregate_id, scope, tenant_id, occurred_at, payload)
			VALUES ('0192f5e4-0000-7000-8000-00000000abcd', 'stream.updated', 'commodore', 'streams', $1, 'tenant', NULL, now(), '\x')`, streamOne)
		if err == nil {
			t.Fatal("tenant-scoped row without a tenant was accepted")
		}
		pub := &recordingPublisher{}
		if n, err := newTestRelay(t, db, pub).DispatchOnce(ctx); err != nil || n != 1 {
			t.Fatalf("platform dispatch = %d, %v", n, err)
		}
		if env := pub.batches[0][0]; env.GetTenantId() != "" || env.GetAggregateId() != "cluster-a" {
			t.Fatalf("platform envelope = %+v", env)
		}
	})

	t.Run("completed rows are pruned after retention", func(t *testing.T) {
		reset(t)
		ev := streamEvent(t, streamOne)
		enqueueTx(t, db, ev)
		if _, err := newTestRelay(t, db, &recordingPublisher{}).DispatchOnce(ctx); err != nil {
			t.Fatal(err)
		}
		relay := newTestRelay(t, db, &recordingPublisher{})
		if n, err := relay.PruneCompleted(ctx); err != nil || n != 0 {
			t.Fatalf("fresh completed row pruned: %d, %v", n, err)
		}
		if _, err := db.Exec(`UPDATE ` + testSchema + `.domain_event_outbox SET completed_at = now() - interval '8 days'`); err != nil {
			t.Fatal(err)
		}
		if n, err := relay.PruneCompleted(ctx); err != nil || n != 1 {
			t.Fatalf("expired completed row not pruned: %d, %v", n, err)
		}
	})
}
