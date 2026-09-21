//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"

	"frameworks/api_billing/internal/billingevents"
	"frameworks/api_billing/internal/handlers"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	eventoutbox "github.com/Livepeer-FrameWorks/monorepo/pkg/events/outbox"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	eventspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	purserpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/purser"
	"github.com/google/uuid"
)

// purserRecordingPublisher stands in for Decklog: it records every batch,
// fails any batch holding an ID in failIDs, and runs afterPublish once the
// batch was recorded.
type purserRecordingPublisher struct {
	mu           sync.Mutex
	published    []*eventspb.DomainEvent
	failIDs      map[string]bool
	afterPublish func()
}

func (p *purserRecordingPublisher) PublishDomainEvents(_ context.Context, batch *eventspb.DomainEventBatch) error {
	p.mu.Lock()
	var err error
	for _, ev := range batch.GetEvents() {
		p.published = append(p.published, ev)
		if p.failIDs[ev.GetId()] {
			err = errors.New("decklog rejected the batch")
		}
	}
	after := p.afterPublish
	p.mu.Unlock()
	if after != nil {
		after()
	}
	return err
}

func (p *purserRecordingPublisher) ids() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, 0, len(p.published))
	for _, ev := range p.published {
		out = append(out, ev.GetId())
	}
	return out
}

type purserDomainRow struct {
	eventID, eventType, aggregateType, aggregateID string
	authType, userID, tokenHash                    string
}

func purserDomainRows(t *testing.T, db *sql.DB, tenantID string) []purserDomainRow {
	t.Helper()
	rows, err := db.Query(`
		SELECT event_id::text, event_type, aggregate_type, aggregate_id, actor_auth_type, actor_user_id, actor_token_hash
		FROM purser.domain_event_outbox WHERE tenant_id = $1::uuid ORDER BY enqueued_at, event_id`, tenantID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []purserDomainRow
	for rows.Next() {
		var r purserDomainRow
		if err := rows.Scan(&r.eventID, &r.eventType, &r.aggregateType, &r.aggregateID, &r.authType, &r.userID, &r.tokenHash); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func purserLegacyEventIDs(t *testing.T, db *sql.DB, tenantID, eventType string) []string {
	t.Helper()
	rows, err := db.Query(`SELECT id::text FROM purser.billing_event_outbox WHERE tenant_id = $1::uuid AND event_type = $2`, tenantID, eventType)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func purserCount(t *testing.T, db *sql.DB, query string, args ...any) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// rejectPurserDomainEvents makes every insert into purser.domain_event_outbox
// fail until the returned function drops the trigger.
func rejectPurserDomainEvents(t *testing.T, db *sql.DB) func() {
	t.Helper()
	if _, err := db.Exec(`
		CREATE OR REPLACE FUNCTION purser.test_reject_domain_event() RETURNS trigger AS $$
		BEGIN RAISE EXCEPTION 'domain outbox unavailable'; END $$ LANGUAGE plpgsql;
		CREATE TRIGGER test_reject_domain_event BEFORE INSERT ON purser.domain_event_outbox
			FOR EACH ROW EXECUTE FUNCTION purser.test_reject_domain_event();`); err != nil {
		t.Fatal(err)
	}
	return func() {
		if _, err := db.Exec(`DROP TRIGGER test_reject_domain_event ON purser.domain_event_outbox`); err != nil {
			t.Fatal(err)
		}
	}
}

func completeAllPurserDomainEvents(t *testing.T, db *sql.DB) {
	t.Helper()
	if _, err := db.Exec(`UPDATE purser.domain_event_outbox SET completed_at = now() WHERE completed_at IS NULL`); err != nil {
		t.Fatal(err)
	}
}

func newPurserRelay(t *testing.T, db *sql.DB, pub eventoutbox.Publisher) *eventoutbox.Relay {
	t.Helper()
	relay, err := eventoutbox.NewRelay(db, billingevents.Schema, pub, logging.NewLogger())
	if err != nil {
		t.Fatal(err)
	}
	return relay
}

func TestPurserDomainEventOutbox_RealPG(t *testing.T) { //nolint:funlen // One database follows every producer guarantee.
	db := startPurserTransitionRealPG(t)
	ctx := context.Background()
	hasher, err := events.NewTokenHasher("purser-domain-event-test-secret")
	if err != nil {
		t.Fatal(err)
	}
	server := &PurserServer{db: db, logger: logging.NewLogger(), tokenHasher: hasher}
	tierID := uuid.NewString()
	if _, err := db.Exec(`
		INSERT INTO purser.billing_tiers (id, tier_name, display_name, base_price, currency, metering_enabled)
		VALUES ($1, $2, 'Domain events', 10.00, 'EUR', false)`, tierID, "domain-events-"+tierID); err != nil {
		t.Fatal(err)
	}
	userID := uuid.NewString()
	apiTokenCtx := context.WithValue(ctx, ctxkeys.KeyAuthType, "api_token")
	apiTokenCtx = context.WithValue(apiTokenCtx, ctxkeys.KeyUserID, userID)
	apiTokenCtx = context.WithValue(apiTokenCtx, ctxkeys.KeyAPITokenID, "token-record-1")
	createSubscription := func(t *testing.T, tenantID string) (*purserpb.TenantSubscription, error) {
		t.Helper()
		return server.CreateSubscription(apiTokenCtx, &purserpb.CreateSubscriptionRequest{
			TenantId: tenantID, TierId: tierID, BillingEmail: "billing@example.test", BillingModel: "prepaid",
		})
	}

	t.Run("a failed outbox insert rolls back the subscription and its legacy event", func(t *testing.T) {
		tenantID := uuid.NewString()
		allow := rejectPurserDomainEvents(t, db)
		_, err := createSubscription(t, tenantID)
		allow()
		if err == nil || !strings.Contains(err.Error(), "domain outbox unavailable") {
			t.Fatalf("CreateSubscription err = %v, want the outbox failure", err)
		}
		if n := purserCount(t, db, `SELECT COUNT(*) FROM purser.tenant_subscriptions WHERE tenant_id = $1::uuid`, tenantID); n != 0 {
			t.Fatalf("%d subscriptions survived the failed outbox insert", n)
		}
		if n := purserCount(t, db, `SELECT COUNT(*) FROM purser.billing_event_outbox WHERE tenant_id = $1::uuid`, tenantID); n != 0 {
			t.Fatalf("%d legacy events survived the failed outbox insert", n)
		}
	})

	t.Run("the change, its domain event, and its legacy row commit under one event id", func(t *testing.T) {
		tenantID := uuid.NewString()
		sub, err := createSubscription(t, tenantID)
		if err != nil {
			t.Fatal(err)
		}
		rows := purserDomainRows(t, db, tenantID)
		if len(rows) != 1 {
			t.Fatalf("domain rows = %+v, want one", rows)
		}
		row := rows[0]
		wantHash := strconv.FormatUint(hasher.Hash("token-record-1"), 10)
		if row.eventType != "billing.subscription_created" || row.aggregateType != "subscriptions" || row.aggregateID != sub.GetId() ||
			row.authType != "api_token" || row.userID != userID || row.tokenHash != wantHash {
			t.Fatalf("domain row = %+v, want subscription %s by the api token", row, sub.GetId())
		}
		if legacy := purserLegacyEventIDs(t, db, tenantID, "subscription_created"); len(legacy) != 1 || legacy[0] != row.eventID {
			t.Fatalf("legacy subscription_created ids = %v, want the domain event id %s", legacy, row.eventID)
		}
	})

	t.Run("the event id is stable across a redispatch after a lost acknowledgment", func(t *testing.T) {
		completeAllPurserDomainEvents(t, db)
		tenantID := uuid.NewString()
		if _, err := createSubscription(t, tenantID); err != nil {
			t.Fatal(err)
		}
		eventID := purserDomainRows(t, db, tenantID)[0].eventID

		dispatchCtx, cancel := context.WithCancel(ctx)
		lost := &purserRecordingPublisher{afterPublish: cancel}
		if n, err := newPurserRelay(t, db, lost).DispatchOnce(dispatchCtx); err != nil || n != 1 {
			t.Fatalf("first dispatch = %d, %v", n, err)
		}
		if _, err := db.Exec(`UPDATE purser.domain_event_outbox SET claimed_at = now() - interval '2 minutes' WHERE event_id = $1::uuid`, eventID); err != nil {
			t.Fatal(err)
		}
		retry := &purserRecordingPublisher{}
		if n, err := newPurserRelay(t, db, retry).DispatchOnce(ctx); err != nil || n != 1 {
			t.Fatalf("redispatch = %d, %v", n, err)
		}
		first, second := lost.ids(), retry.ids()
		if len(first) != 1 || len(second) != 1 || first[0] != eventID || second[0] != eventID {
			t.Fatalf("published %v then %v, want %s both times", first, second, eventID)
		}
		if legacy := purserLegacyEventIDs(t, db, tenantID, "subscription_created"); len(legacy) != 1 || legacy[0] != eventID {
			t.Fatalf("legacy ids = %v, want the redispatched id %s", legacy, eventID)
		}
		if _, _, err := events.Validate(retry.published[0]); err != nil {
			t.Fatalf("redispatched envelope is not Decklog-valid: %v", err)
		}
	})

	t.Run("a second event of a subscription waits for the first", func(t *testing.T) {
		completeAllPurserDomainEvents(t, db)
		tenantID := uuid.NewString()
		if _, err := createSubscription(t, tenantID); err != nil {
			t.Fatal(err)
		}
		if _, err := server.CancelSubscription(apiTokenCtx, &purserpb.CancelSubscriptionRequest{TenantId: tenantID}); err != nil {
			t.Fatal(err)
		}
		rows := purserDomainRows(t, db, tenantID)
		if len(rows) != 2 || rows[0].aggregateID != rows[1].aggregateID || rows[1].eventType != "billing.subscription_updated" {
			t.Fatalf("domain rows = %+v, want created then updated for one subscription", rows)
		}
		if legacy := purserLegacyEventIDs(t, db, tenantID, "subscription_canceled"); len(legacy) != 1 || legacy[0] != rows[1].eventID {
			t.Fatalf("legacy subscription_canceled ids = %v, want %s", legacy, rows[1].eventID)
		}

		failing := &purserRecordingPublisher{failIDs: map[string]bool{rows[0].eventID: true}}
		if _, err := newPurserRelay(t, db, failing).DispatchOnce(ctx); err != nil {
			t.Fatal(err)
		}
		if got := failing.ids(); len(got) != 1 || got[0] != rows[0].eventID {
			t.Fatalf("published while the head failed = %v, want only %s", got, rows[0].eventID)
		}
		if _, err := db.Exec(`UPDATE purser.domain_event_outbox SET next_attempt_at = now() WHERE completed_at IS NULL`); err != nil {
			t.Fatal(err)
		}
		healthy := &purserRecordingPublisher{}
		for range 2 {
			if _, err := newPurserRelay(t, db, healthy).DispatchOnce(ctx); err != nil {
				t.Fatal(err)
			}
		}
		if got := healthy.ids(); len(got) != 2 || got[0] != rows[0].eventID || got[1] != rows[1].eventID {
			t.Fatalf("published after recovery = %v, want %s then %s", got, rows[0].eventID, rows[1].eventID)
		}
	})

	t.Run("platform rows store no tenant", func(t *testing.T) {
		clusterID := uuid.NewString()
		ev, err := events.New(billingevents.Source, "", clusterID, &internalv1.ClusterCreated{ClusterId: clusterID})
		if err != nil {
			t.Fatal(err)
		}
		if err := eventoutbox.Enqueue(ctx, db, billingevents.Schema, ev); err != nil {
			t.Fatal(err)
		}
		var scope string
		var tenant sql.NullString
		if err := db.QueryRow(`SELECT scope, tenant_id::text FROM purser.domain_event_outbox WHERE event_id = $1::uuid`, ev.ID).Scan(&scope, &tenant); err != nil {
			t.Fatal(err)
		}
		if scope != "platform" || tenant.Valid {
			t.Fatalf("platform row scope = %q tenant = %v, want platform with no tenant", scope, tenant)
		}
		if _, err := db.Exec(`
			INSERT INTO purser.domain_event_outbox (event_id, event_type, source, aggregate_type, aggregate_id, scope, tenant_id, occurred_at, payload)
			VALUES ($1::uuid, 'cluster.created', 'purser', 'clusters', $2, 'platform', $3::uuid, now(), '\x')`,
			uuid.Must(uuid.NewV7()).String(), clusterID, uuid.NewString()); err == nil {
			t.Fatal("a platform row with a tenant was stored")
		}
		if n := purserCount(t, db, `SELECT COUNT(*) FROM purser.domain_event_outbox WHERE event_type <> 'cluster.created' AND (scope <> 'tenant' OR tenant_id IS NULL)`); n != 0 {
			t.Fatalf("%d Purser billing events were stored without a tenant", n)
		}
	})

	t.Run("suspension commits account.suspended; a failed insert leaves the tenant active", func(t *testing.T) {
		tenantID := uuid.NewString()
		if _, err := db.Exec(`
			INSERT INTO purser.tenant_subscriptions (tenant_id, tier_id, status, billing_model, billing_email)
			VALUES ($1::uuid, $2::uuid, 'active', 'prepaid', 'billing@example.test')`, tenantID, tierID); err != nil {
			t.Fatal(err)
		}
		enforcer := handlers.NewThresholdEnforcer(db, logging.NewLogger(), nil, nil, nil, nil)
		subscriptionStatus := func() string {
			var status string
			if err := db.QueryRow(`SELECT status FROM purser.tenant_subscriptions WHERE tenant_id = $1::uuid`, tenantID).Scan(&status); err != nil {
				t.Fatal(err)
			}
			return status
		}

		allow := rejectPurserDomainEvents(t, db)
		err := enforcer.EnforcePrepaidThresholds(ctx, tenantID, -500, -1001)
		allow()
		if err == nil || subscriptionStatus() != "active" {
			t.Fatalf("failed event insert: err = %v, status = %s, want an error and an active tenant", err, subscriptionStatus())
		}

		if err := enforcer.EnforcePrepaidThresholds(ctx, tenantID, -500, -1001); err != nil {
			t.Fatal(err)
		}
		if err := enforcer.EnforcePrepaidThresholds(ctx, tenantID, -1001, -1500); err != nil {
			t.Fatal(err)
		}
		rows := purserDomainRows(t, db, tenantID)
		if subscriptionStatus() != "suspended" || len(rows) != 1 || rows[0].eventType != "account.suspended" ||
			rows[0].aggregateType != "tenants" || rows[0].aggregateID != tenantID || rows[0].authType != "" {
			t.Fatalf("status = %s, domain rows = %+v, want one system account.suspended for the tenant", subscriptionStatus(), rows)
		}
	})
}
