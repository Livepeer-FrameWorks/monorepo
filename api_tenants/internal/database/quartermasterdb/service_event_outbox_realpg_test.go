//go:build schema_verify

package quartermasterdb

import (
	"context"
	"database/sql"
	"testing"
)

func TestServiceEventOutboxScopeAndLeaseToken_RealPG(t *testing.T) {
	db := startQuartermasterQueryCatalogRealPG(t)
	ctx := context.Background()
	q := New(db)
	const (
		tenantID   = "11111111-1111-4111-8111-111111111111"
		claimToken = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		staleToken = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	)
	type outboxState struct {
		scope     string
		tenantNil bool
		attempts  int
		claimed   bool
		completed bool
	}
	state := func(id string) outboxState {
		t.Helper()
		var s outboxState
		var claimedAt, completedAt sql.NullTime
		if err := db.QueryRowContext(ctx, `
			SELECT scope, tenant_id IS NULL, attempts, claimed_at, completed_at
			FROM quartermaster.service_event_outbox WHERE id = $1::uuid`, id).
			Scan(&s.scope, &s.tenantNil, &s.attempts, &claimedAt, &completedAt); err != nil {
			t.Fatal(err)
		}
		s.claimed, s.completed = claimedAt.Valid, completedAt.Valid
		return s
	}

	const (
		tenantEventID   = "01900000-0000-7000-8000-000000000001"
		platformEventID = "01900000-0000-7000-8000-000000000002"
	)
	tenantRow, err := q.EnqueueServiceEvent(ctx, EnqueueServiceEventParams{EventID: tenantEventID, EventType: "cluster_invite_created", TenantID: tenantID, Scope: "tenant", Payload: "{}"})
	if err != nil {
		t.Fatal(err)
	}
	platformRow, err := q.EnqueueServiceEvent(ctx, EnqueueServiceEventParams{EventID: platformEventID, EventType: "cluster_created", Scope: "platform", Payload: "{}"})
	if err != nil {
		t.Fatalf("platform-scoped event without tenant: %v", err)
	}
	if got := state(platformRow); got.scope != "platform" || !got.tenantNil {
		t.Fatalf("platform row = %+v", got)
	}
	if _, err := q.EnqueueServiceEvent(ctx, EnqueueServiceEventParams{EventID: "01900000-0000-7000-8000-000000000003", EventType: "cluster_invite_created", Scope: "tenant", Payload: "{}"}); err == nil {
		t.Fatal("tenant-scoped event without tenant_id was accepted")
	}
	// A row written before event_id existed claims with an empty event ID.
	if _, err := db.ExecContext(ctx, `INSERT INTO quartermaster.service_event_outbox (event_type, tenant_id, scope, payload)
		VALUES ('tenant_updated', $1::uuid, 'tenant', '{}')`, tenantID); err != nil {
		t.Fatal(err)
	}

	rows, err := q.ClaimServiceEventOutboxBatch(ctx, ClaimServiceEventOutboxBatchParams{LeaseInterval: "60 seconds", BatchSize: 100})
	if err != nil {
		t.Fatal(err)
	}
	claimable := map[string]string{}
	for _, row := range rows {
		claimable[row.ID] = row.EventID
	}
	if claimable[tenantRow] != tenantEventID || claimable[platformRow] != platformEventID || len(rows) != 3 {
		t.Fatalf("claim batch %+v misses the enqueued rows or their event IDs", rows)
	}
	for _, row := range rows {
		if row.ID != tenantRow && row.ID != platformRow && row.EventID != "" {
			t.Fatalf("pre-event_id row claimed with event ID %q", row.EventID)
		}
	}
	if err := q.MarkServiceEventOutboxClaimed(ctx, MarkServiceEventOutboxClaimedParams{LeaseToken: claimToken, Ids: []string{tenantRow, platformRow}}); err != nil {
		t.Fatal(err)
	}

	if err := q.CompleteServiceEventOutbox(ctx, CompleteServiceEventOutboxParams{ID: tenantRow, LeaseToken: staleToken}); err != nil {
		t.Fatal(err)
	}
	if err := q.FailServiceEventOutbox(ctx, FailServiceEventOutboxParams{ID: tenantRow, Attempts: 3, LastError: "stale worker", LeaseToken: staleToken}); err != nil {
		t.Fatal(err)
	}
	if got := state(tenantRow); got.completed || got.attempts != 0 || !got.claimed {
		t.Fatalf("stale lease token settled the row: %+v", got)
	}
	if err := q.CompleteServiceEventOutbox(ctx, CompleteServiceEventOutboxParams{ID: tenantRow, LeaseToken: claimToken}); err != nil {
		t.Fatal(err)
	}
	if got := state(tenantRow); !got.completed {
		t.Fatalf("current lease token did not complete the row: %+v", got)
	}

	if err := q.CompleteServiceEventOutbox(ctx, CompleteServiceEventOutboxParams{ID: platformRow}); err != nil {
		t.Fatal(err)
	}
	if got := state(platformRow); got.completed {
		t.Fatalf("an empty lease token completed the row: %+v", got)
	}
	if err := q.FailServiceEventOutbox(ctx, FailServiceEventOutboxParams{ID: platformRow, Attempts: 1, LastError: "decklog unavailable", LeaseToken: claimToken}); err != nil {
		t.Fatal(err)
	}
	if got := state(platformRow); got.attempts != 1 || got.claimed || got.completed {
		t.Fatalf("current lease token did not record the failure: %+v", got)
	}
}
