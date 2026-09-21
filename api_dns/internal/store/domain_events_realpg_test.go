//go:build schema_verify

package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	eventoutbox "github.com/Livepeer-FrameWorks/monorepo/pkg/events/outbox"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	eventspb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/internalv1"
	publicv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/public/v1"
	"github.com/google/uuid"
	"google.golang.org/protobuf/proto"
)

// navigatorRecordingPublisher stands in for Decklog: it records every batch,
// fails any batch holding an ID in failIDs, and runs afterPublish once the
// batch was recorded.
type navigatorRecordingPublisher struct {
	mu           sync.Mutex
	published    []*eventspb.DomainEvent
	failIDs      map[string]bool
	afterPublish func()
}

func (p *navigatorRecordingPublisher) PublishDomainEvents(_ context.Context, batch *eventspb.DomainEventBatch) error {
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

func (p *navigatorRecordingPublisher) ids() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, 0, len(p.published))
	for _, ev := range p.published {
		out = append(out, ev.GetId())
	}
	return out
}

type navigatorDomainRow struct {
	eventID, eventType, aggregateType string
	payload                           []byte
}

func navigatorDomainRows(t *testing.T, db *sql.DB, domain string) []navigatorDomainRow {
	t.Helper()
	rows, err := db.Query(`
		SELECT event_id::text, event_type, aggregate_type, payload FROM navigator.domain_event_outbox
		WHERE aggregate_id = $1 ORDER BY enqueued_at, event_id`, domain)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []navigatorDomainRow
	for rows.Next() {
		var r navigatorDomainRow
		if err := rows.Scan(&r.eventID, &r.eventType, &r.aggregateType, &r.payload); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func failureReason(t *testing.T, row navigatorDomainRow) publicv1.CustomDomainFailureReason {
	t.Helper()
	msg := &publicv1.CustomDomainFailed{}
	if err := proto.Unmarshal(row.payload, msg); err != nil {
		t.Fatal(err)
	}
	return msg.GetReason()
}

func customDomainStatus(t *testing.T, db *sql.DB, tenantID, domain string) string {
	t.Helper()
	var status string
	if err := db.QueryRow(`SELECT status FROM navigator.tenant_custom_domains WHERE tenant_id = $1::uuid AND domain = $2`, tenantID, domain).Scan(&status); err != nil {
		t.Fatal(err)
	}
	return status
}

// rejectNavigatorDomainEvents makes every insert into
// navigator.domain_event_outbox fail until the returned function drops the
// trigger.
func rejectNavigatorDomainEvents(t *testing.T, db *sql.DB) func() {
	t.Helper()
	if _, err := db.Exec(`
		CREATE OR REPLACE FUNCTION navigator.test_reject_domain_event() RETURNS trigger AS $$
		BEGIN RAISE EXCEPTION 'domain outbox unavailable'; END $$ LANGUAGE plpgsql;
		CREATE TRIGGER test_reject_domain_event BEFORE INSERT ON navigator.domain_event_outbox
			FOR EACH ROW EXECUTE FUNCTION navigator.test_reject_domain_event();`); err != nil {
		t.Fatal(err)
	}
	return func() {
		if _, err := db.Exec(`DROP TRIGGER test_reject_domain_event ON navigator.domain_event_outbox`); err != nil {
			t.Fatal(err)
		}
	}
}

func newNavigatorRelay(t *testing.T, db *sql.DB, pub eventoutbox.Publisher) *eventoutbox.Relay {
	t.Helper()
	relay, err := eventoutbox.NewRelay(db, DomainEventSchema, pub, logging.NewLogger())
	if err != nil {
		t.Fatal(err)
	}
	return relay
}

func TestNavigatorDomainEventOutbox_RealPG(t *testing.T) { //nolint:funlen // One database follows every producer guarantee.
	db := startNavigatorStoreRealPG(t)
	st := NewStore(db, nil)
	ctx := context.Background()
	tenantID := uuid.NewString()
	ensure := func(t *testing.T, domain string) {
		t.Helper()
		if _, err := st.EnsureTenantCustomDomain(ctx, tenantID, domain, "slug-"+strings.ReplaceAll(domain, ".", "-")); err != nil {
			t.Fatal(err)
		}
	}
	toIssuing := func(t *testing.T, domain string) {
		t.Helper()
		if ok, err := st.SetTenantCustomDomainStatus(ctx, tenantID, domain, "verified", "cert_issuing", ""); err != nil || !ok {
			t.Fatalf("verified -> cert_issuing = %v, %v", ok, err)
		}
	}

	t.Run("first verification commits custom_domain.verified; a failed insert keeps the domain pending", func(t *testing.T) {
		domain := "verify.example.test"
		ensure(t, domain)
		allow := rejectNavigatorDomainEvents(t, db)
		_, err := st.MarkTenantCustomDomainVerified(ctx, tenantID, domain, "pending_verification")
		allow()
		if err == nil || customDomainStatus(t, db, tenantID, domain) != "pending_verification" {
			t.Fatalf("failed event insert: err = %v, status = %s", err, customDomainStatus(t, db, tenantID, domain))
		}
		if ok, err := st.MarkTenantCustomDomainVerified(ctx, tenantID, domain, "pending_verification"); err != nil || !ok {
			t.Fatalf("verify = %v, %v", ok, err)
		}
		rows := navigatorDomainRows(t, db, domain)
		if len(rows) != 1 || rows[0].eventType != "custom_domain.verified" || rows[0].aggregateType != "custom_domains" {
			t.Fatalf("domain rows = %+v, want one custom_domain.verified", rows)
		}
	})

	t.Run("entry into cert_failed reports once; retry cycles report nothing", func(t *testing.T) {
		domain := "certfail.example.test"
		ensure(t, domain)
		if _, err := st.MarkTenantCustomDomainVerified(ctx, tenantID, domain, "pending_verification"); err != nil {
			t.Fatal(err)
		}
		toIssuing(t, domain)

		allow := rejectNavigatorDomainEvents(t, db)
		_, err := st.FailTenantCustomDomainIssuance(ctx, tenantID, domain, "order failed", time.Minute)
		allow()
		if err == nil || customDomainStatus(t, db, tenantID, domain) != "cert_issuing" {
			t.Fatalf("failed event insert: err = %v, status = %s", err, customDomainStatus(t, db, tenantID, domain))
		}
		if ok, err := st.FailTenantCustomDomainIssuance(ctx, tenantID, domain, "order failed", time.Minute); err != nil || !ok {
			t.Fatalf("fail issuance = %v, %v", ok, err)
		}
		// The retry: re-verification, a new order, and its failure.
		if ok, err := st.MarkTenantCustomDomainVerified(ctx, tenantID, domain, "cert_failed"); err != nil || !ok {
			t.Fatalf("re-verify = %v, %v", ok, err)
		}
		toIssuing(t, domain)
		if ok, err := st.FailTenantCustomDomainIssuance(ctx, tenantID, domain, "order failed again", time.Minute); err != nil || !ok {
			t.Fatalf("retry failure = %v, %v", ok, err)
		}
		rows := navigatorDomainRows(t, db, domain)
		if len(rows) != 2 || rows[0].eventType != "custom_domain.verified" || rows[1].eventType != "custom_domain.failed" ||
			failureReason(t, rows[1]) != publicv1.CustomDomainFailureReason_CUSTOM_DOMAIN_FAILURE_REASON_CERTIFICATE_FAILED {
			t.Fatalf("domain rows = %+v, want verified then one certificate failure", rows)
		}
	})

	t.Run("pending verification expires after the period and fails once", func(t *testing.T) {
		stale, fresh := "stale.example.test", "fresh.example.test"
		ensure(t, stale)
		ensure(t, fresh)
		if _, err := db.Exec(`UPDATE navigator.tenant_custom_domains SET verification_started_at = now() - interval '8 days' WHERE domain = $1`, stale); err != nil {
			t.Fatal(err)
		}
		expired, err := st.ExpireTenantCustomDomainVerifications(ctx, 7*24*time.Hour)
		if err != nil || expired != 1 {
			t.Fatalf("expired = %d, %v, want 1", expired, err)
		}
		if again, err := st.ExpireTenantCustomDomainVerifications(ctx, 7*24*time.Hour); err != nil || again != 0 {
			t.Fatalf("second expiry pass = %d, %v, want 0", again, err)
		}
		if s := customDomainStatus(t, db, tenantID, stale); s != "verification_failed" {
			t.Fatalf("stale status = %s", s)
		}
		if s := customDomainStatus(t, db, tenantID, fresh); s != "pending_verification" {
			t.Fatalf("fresh status = %s", s)
		}
		rows := navigatorDomainRows(t, db, stale)
		if len(rows) != 1 || rows[0].eventType != "custom_domain.failed" ||
			failureReason(t, rows[0]) != publicv1.CustomDomainFailureReason_CUSTOM_DOMAIN_FAILURE_REASON_VERIFICATION_EXPIRED {
			t.Fatalf("stale domain rows = %+v, want one expired failure", rows)
		}
		if rows := navigatorDomainRows(t, db, fresh); len(rows) != 0 {
			t.Fatalf("fresh domain rows = %+v, want none", rows)
		}

		// Requesting the domain again restarts verification with a new period.
		row, err := st.EnsureTenantCustomDomain(ctx, tenantID, stale, "ignored")
		if err != nil || row.Status != "pending_verification" || row.FailureReportedAt.Valid || time.Since(row.VerificationStartedAt) > time.Minute {
			t.Fatalf("reactivated row = %+v, %v", row, err)
		}
	})

	t.Run("the event id is stable across a redispatch after a lost acknowledgment", func(t *testing.T) {
		if _, err := db.Exec(`UPDATE navigator.domain_event_outbox SET completed_at = now() WHERE completed_at IS NULL`); err != nil {
			t.Fatal(err)
		}
		domain := "relay.example.test"
		ensure(t, domain)
		if _, err := st.MarkTenantCustomDomainVerified(ctx, tenantID, domain, "pending_verification"); err != nil {
			t.Fatal(err)
		}
		eventID := navigatorDomainRows(t, db, domain)[0].eventID

		dispatchCtx, cancel := context.WithCancel(ctx)
		lost := &navigatorRecordingPublisher{afterPublish: cancel}
		if n, err := newNavigatorRelay(t, db, lost).DispatchOnce(dispatchCtx); err != nil || n != 1 {
			t.Fatalf("first dispatch = %d, %v", n, err)
		}
		if _, err := db.Exec(`UPDATE navigator.domain_event_outbox SET claimed_at = now() - interval '2 minutes' WHERE event_id = $1::uuid`, eventID); err != nil {
			t.Fatal(err)
		}
		retry := &navigatorRecordingPublisher{}
		if n, err := newNavigatorRelay(t, db, retry).DispatchOnce(ctx); err != nil || n != 1 {
			t.Fatalf("redispatch = %d, %v", n, err)
		}
		first, second := lost.ids(), retry.ids()
		if len(first) != 1 || len(second) != 1 || first[0] != eventID || second[0] != eventID {
			t.Fatalf("published %v then %v, want %s both times", first, second, eventID)
		}
		if _, _, err := events.Validate(retry.published[0]); err != nil {
			t.Fatalf("redispatched envelope is not Decklog-valid: %v", err)
		}
	})

	t.Run("a second event of a domain waits for the first", func(t *testing.T) {
		if _, err := db.Exec(`UPDATE navigator.domain_event_outbox SET completed_at = now() WHERE completed_at IS NULL`); err != nil {
			t.Fatal(err)
		}
		domain := "ordered.example.test"
		ensure(t, domain)
		if _, err := st.MarkTenantCustomDomainVerified(ctx, tenantID, domain, "pending_verification"); err != nil {
			t.Fatal(err)
		}
		toIssuing(t, domain)
		if _, err := st.FailTenantCustomDomainIssuance(ctx, tenantID, domain, "order failed", time.Minute); err != nil {
			t.Fatal(err)
		}
		rows := navigatorDomainRows(t, db, domain)
		if len(rows) != 2 {
			t.Fatalf("domain rows = %+v, want verified then failed", rows)
		}
		failing := &navigatorRecordingPublisher{failIDs: map[string]bool{rows[0].eventID: true}}
		if _, err := newNavigatorRelay(t, db, failing).DispatchOnce(ctx); err != nil {
			t.Fatal(err)
		}
		if got := failing.ids(); len(got) != 1 || got[0] != rows[0].eventID {
			t.Fatalf("published while the head failed = %v, want only %s", got, rows[0].eventID)
		}
		if _, err := db.Exec(`UPDATE navigator.domain_event_outbox SET next_attempt_at = now() WHERE completed_at IS NULL`); err != nil {
			t.Fatal(err)
		}
		healthy := &navigatorRecordingPublisher{}
		for range 2 {
			if _, err := newNavigatorRelay(t, db, healthy).DispatchOnce(ctx); err != nil {
				t.Fatal(err)
			}
		}
		if got := healthy.ids(); len(got) != 2 || got[0] != rows[0].eventID || got[1] != rows[1].eventID {
			t.Fatalf("published after recovery = %v, want %s then %s", got, rows[0].eventID, rows[1].eventID)
		}
	})

	t.Run("platform rows store no tenant", func(t *testing.T) {
		clusterID := uuid.NewString()
		ev, err := events.New(DomainEventSource, "", clusterID, &internalv1.ClusterCreated{ClusterId: clusterID})
		if err != nil {
			t.Fatal(err)
		}
		if err := eventoutbox.Enqueue(ctx, db, DomainEventSchema, ev); err != nil {
			t.Fatal(err)
		}
		var scope string
		var tenant sql.NullString
		if err := db.QueryRow(`SELECT scope, tenant_id::text FROM navigator.domain_event_outbox WHERE event_id = $1::uuid`, ev.ID).Scan(&scope, &tenant); err != nil {
			t.Fatal(err)
		}
		if scope != "platform" || tenant.Valid {
			t.Fatalf("platform row scope = %q tenant = %v, want platform with no tenant", scope, tenant)
		}
		var custom int
		if err := db.QueryRow(`SELECT COUNT(*) FROM navigator.domain_event_outbox WHERE event_type LIKE 'custom_domain.%' AND (scope <> 'tenant' OR tenant_id <> $1::uuid)`, tenantID).Scan(&custom); err != nil {
			t.Fatal(err)
		}
		if custom != 0 {
			t.Fatalf("%d custom domain events are not stored under their tenant", custom)
		}
	})
}
