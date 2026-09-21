//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events/outbox"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	eventspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
)

// recordingPublisher records every batch the relay sends and fails while
// failNext is positive, the way a publish whose acknowledgement was lost
// looks to the relay.
type recordingPublisher struct {
	mu       sync.Mutex
	batches  [][]*eventspb.DomainEvent
	failNext int
}

func (p *recordingPublisher) PublishDomainEvents(_ context.Context, batch *eventspb.DomainEventBatch) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.batches = append(p.batches, batch.GetEvents())
	if p.failNext > 0 {
		p.failNext--
		return errors.New("acknowledgement lost")
	}
	return nil
}

func (p *recordingPublisher) take() [][]*eventspb.DomainEvent {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := p.batches
	p.batches = nil
	return out
}

func startQuartermasterDomainEventsRealPG(t *testing.T) *sql.DB {
	t.Helper()
	name := fmt.Sprintf("fw-qm-domain-events-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = dockerpg.CLI("rm", "-fv", name) })
	image, err := dockerpg.PostgresImage()
	if err != nil {
		t.Fatal(err)
	}
	if output, err := dockerpg.Run("run", "-d", "--name", name, "-P", "-e", "POSTGRES_PASSWORD=harness", image); err != nil {
		t.Fatalf("start PostgreSQL: %v\n%s", err, output)
	}
	port, err := dockerpg.DiscoverPublishedHostPort(name, "5432/tcp")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("postgres", fmt.Sprintf("postgres://postgres:harness@127.0.0.1:%s/postgres?sslmode=disable", port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := dockerpg.WaitReady(db, name); err != nil {
		t.Fatal(err)
	}
	schema, err := dbsql.Content.ReadFile("schema/quartermaster.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(schema)); err != nil {
		t.Fatal(err)
	}
	return db
}

// Quartermaster's domain events commit with their state change, keep one ID
// across redelivery on both the domain relay and the legacy dispatcher, stay
// ordered per aggregate, and store no tenant when platform-scoped.
func TestQuartermasterDomainEventOutbox_RealPG(t *testing.T) { //nolint:funlen // One engine fixture walks the producer contract end to end.
	db := startQuartermasterDomainEventsRealPG(t)
	ctx := context.Background()
	const (
		tenantID = "11111111-1111-4111-8111-111111111111"
		ownerID  = "22222222-2222-4222-8222-222222222222"
	)
	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.Exec(query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := db.QueryRow(query, args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return n
	}
	exec(`INSERT INTO quartermaster.tenants (id, name) VALUES ($1::uuid, 'Events')`, tenantID)
	hasher, err := events.NewTokenHasher("realpg-usage-hash-secret")
	if err != nil {
		t.Fatal(err)
	}
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)
	server.SetEventTokenHasher(hasher)
	svcCtx := context.WithValue(ctx, ctxkeys.KeyAuthType, "service")
	updateModel := func(model string) error {
		_, err := server.UpdateTenantCluster(svcCtx, &quartermasterpb.UpdateTenantClusterRequest{TenantId: tenantID, DeploymentModel: &model})
		return err
	}

	// A failed domain outbox insert rolls the tenant update and its legacy
	// service event back with it.
	exec(`ALTER TABLE quartermaster.domain_event_outbox RENAME TO domain_event_outbox_unavailable`)
	if err := updateModel("dedicated"); err == nil {
		t.Fatal("tenant update committed without its domain event")
	}
	exec(`ALTER TABLE quartermaster.domain_event_outbox_unavailable RENAME TO domain_event_outbox`)
	var model sql.NullString
	if err := db.QueryRow(`SELECT deployment_model FROM quartermaster.tenants WHERE id = $1::uuid`, tenantID).Scan(&model); err != nil {
		t.Fatal(err)
	}
	if model.String == "dedicated" {
		t.Fatal("tenant update survived the failed domain event insert")
	}
	if n := count(`SELECT count(*) FROM quartermaster.service_event_outbox`); n != 0 {
		t.Fatalf("legacy rows after the rolled-back update = %d", n)
	}

	// Two updates of one tenant: the legacy rows carry the domain IDs.
	if err := updateModel("dedicated"); err != nil {
		t.Fatal(err)
	}
	if err := updateModel("shared"); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`SELECT d.event_id::text, d.event_type, d.aggregate_id, d.tenant_id::text, d.actor_auth_type
		FROM quartermaster.domain_event_outbox d ORDER BY d.enqueued_at, d.event_id`)
	if err != nil {
		t.Fatal(err)
	}
	var domainIDs []string
	for rows.Next() {
		var id, eventType, aggregateID, tenant, authType string
		if err := rows.Scan(&id, &eventType, &aggregateID, &tenant, &authType); err != nil {
			t.Fatal(err)
		}
		if eventType != "tenant.updated" || aggregateID != tenantID || tenant != tenantID || authType != "service" {
			t.Fatalf("domain row = %s %s %s %s", eventType, aggregateID, tenant, authType)
		}
		domainIDs = append(domainIDs, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	_ = rows.Close()
	if len(domainIDs) != 2 {
		t.Fatalf("domain rows = %v", domainIDs)
	}
	for _, id := range domainIDs {
		if n := count(`SELECT count(*) FROM quartermaster.service_event_outbox
			WHERE event_id::text = $1::text AND event_type = 'tenant_updated' AND payload->>'eventId' = $1::text`, id); n != 1 {
			t.Fatalf("legacy rows carrying domain event %s = %d", id, n)
		}
	}

	// Legacy dispatcher: a claim whose acknowledgement was lost is claimed
	// again after the lease and sends the same ID.
	claimLegacy := func() map[string]string {
		t.Helper()
		batch, err := server.claimQMOutboxBatch(ctx)
		if err != nil {
			t.Fatal(err)
		}
		ids := map[string]string{}
		for _, row := range batch {
			event, err := serviceEventForDispatch(row)
			if err != nil {
				t.Fatal(err)
			}
			ids[row.id] = event.GetEventId()
		}
		return ids
	}
	first := claimLegacy()
	exec(`UPDATE quartermaster.service_event_outbox SET claimed_at = NOW() - INTERVAL '1 hour'`)
	second := claimLegacy()
	if len(first) != 2 || len(second) != 2 {
		t.Fatalf("legacy claims = %v then %v", first, second)
	}
	for rowID, id := range first {
		if second[rowID] != id || (id != domainIDs[0] && id != domainIDs[1]) {
			t.Fatalf("legacy row %s dispatched as %s then %s; domain IDs %v", rowID, id, second[rowID], domainIDs)
		}
	}

	// Domain relay: the second row of the tenant waits while the first is
	// undelivered, and the redelivery of the first keeps its ID.
	publisher := &recordingPublisher{failNext: 1}
	relay, err := outbox.NewRelay(db, "quartermaster", publisher, logging.NewLogger())
	if err != nil {
		t.Fatal(err)
	}
	dispatch := func() []*eventspb.DomainEvent {
		t.Helper()
		if _, err := relay.DispatchOnce(ctx); err != nil {
			t.Fatal(err)
		}
		var sent []*eventspb.DomainEvent
		for _, batch := range publisher.take() {
			sent = append(sent, batch...)
		}
		return sent
	}
	if sent := dispatch(); len(sent) != 1 || sent[0].GetId() != domainIDs[0] {
		t.Fatalf("first dispatch sent %v, want only %s", sent, domainIDs[0])
	}
	if sent := dispatch(); len(sent) != 0 {
		t.Fatalf("second row of the tenant was sent while the first backs off: %v", sent)
	}
	exec(`UPDATE quartermaster.domain_event_outbox SET next_attempt_at = NOW() WHERE event_id = $1::uuid`, domainIDs[0])
	if sent := dispatch(); len(sent) != 1 || sent[0].GetId() != domainIDs[0] {
		t.Fatalf("redelivery sent %v, want %s again", sent, domainIDs[0])
	}
	if sent := dispatch(); len(sent) != 1 || sent[0].GetId() != domainIDs[1] {
		t.Fatalf("after the first row completed the relay sent %v, want %s", sent, domainIDs[1])
	}

	// cluster.created and cluster.updated are platform-scoped: the domain row
	// stores no tenant and names the owner in the payload, while the legacy
	// row keeps the owner as its tenant.
	exec(`TRUNCATE quartermaster.domain_event_outbox, quartermaster.service_event_outbox`)
	for _, eventType := range []string{eventClusterCreated, eventClusterUpdated} {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := server.emitClusterEventTx(svcCtx, tx, eventType, ownerID, "", "cluster-a", "cluster", "cluster-a", "", "", ""); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	if err := server.emitClusterEventTx(svcCtx, nil, eventClusterCreated, "", "", "cluster-b", "cluster", "cluster-b", "", "", ""); err == nil {
		t.Fatal("a cluster event was written without the mutation's transaction")
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := server.emitClusterEventTx(svcCtx, tx, eventClusterCreated, "", "", "cluster-b", "cluster", "cluster-b", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if n := count(`SELECT count(*) FROM quartermaster.domain_event_outbox
		WHERE scope = 'platform' AND tenant_id IS NULL AND event_type IN ('cluster.created', 'cluster.updated')`); n != 3 {
		t.Fatalf("platform domain rows = %d, want 3", n)
	}
	if n := count(`SELECT count(*) FROM quartermaster.service_event_outbox s
		JOIN quartermaster.domain_event_outbox d ON d.event_id = s.event_id`); n != 3 {
		t.Fatalf("legacy rows sharing a domain event ID = %d, want 3", n)
	}
	if n := count(`SELECT count(*) FROM quartermaster.service_event_outbox WHERE tenant_id = $1::uuid AND scope = 'tenant'`, ownerID); n != 2 {
		t.Fatalf("legacy owner rows = %d, want 2", n)
	}
	if n := count(`SELECT count(*) FROM quartermaster.service_event_outbox WHERE tenant_id IS NULL AND scope = 'platform'`); n != 1 {
		t.Fatalf("legacy ownerless rows = %d, want 1", n)
	}
	var payload []byte
	if err := db.QueryRow(`SELECT payload FROM quartermaster.domain_event_outbox WHERE event_type = 'cluster.created' AND aggregate_id = 'cluster-a'`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	_, msg, err := events.Decode("cluster.created", payload)
	if err != nil {
		t.Fatal(err)
	}
	if created, ok := msg.(*internalv1.ClusterCreated); !ok || created.GetOwnerTenantId() != ownerID || created.GetClusterId() != "cluster-a" {
		t.Fatalf("cluster.created payload = %v", msg)
	}
}
