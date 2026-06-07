package grpc

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// TestClaimAliasOutboxBatchClaimsReturnedRows exercises the claim-UPDATE branch:
// when the per-tenant predicate yields rows, they are stamped claimed_at in the
// same transaction (so a peer replica's lease predicate skips them) and returned
// with their fields parsed. The existing HonorsNextRetryAt test covers the
// empty-result branch; this covers the non-empty path.
func TestClaimAliasOutboxBatchClaimsReturnedRows(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	mock.ExpectBegin()
	mock.ExpectQuery(`FROM quartermaster\.navigator_tenant_alias_outbox o`).
		WillReturnRows(sqlmock.NewRows([]string{"id", "tenant_id", "subdomain", "cluster_id", "reason", "action", "attempts"}).
			AddRow("outbox-1", "tenant-1", "acme", "", "rename", "ensure", 0))
	mock.ExpectExec(`UPDATE quartermaster\.navigator_tenant_alias_outbox\s+SET claimed_at = NOW\(\)`).
		WithArgs(sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	rows, err := server.claimAliasOutboxBatch(context.Background())
	if err != nil {
		t.Fatalf("claimAliasOutboxBatch: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].id != "outbox-1" || rows[0].tenantID != "tenant-1" || rows[0].action != "ensure" {
		t.Fatalf("unexpected row scan: %+v", rows[0])
	}
	if mErr := mock.ExpectationsWereMet(); mErr != nil {
		t.Fatalf("expectations: %v", mErr)
	}
}

func TestMarkAliasOutboxCompletedSuccess(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	// Completion clears last_error and next_retry_at so a previously-failing row
	// leaves the retry queue cleanly.
	mock.ExpectExec(`UPDATE quartermaster\.navigator_tenant_alias_outbox\s+SET completed_at = NOW\(\), last_error = NULL, next_retry_at = NULL`).
		WithArgs("outbox-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := server.markAliasOutboxCompleted(context.Background(), "outbox-1"); err != nil {
		t.Fatalf("markAliasOutboxCompleted: %v", err)
	}
	if mErr := mock.ExpectationsWereMet(); mErr != nil {
		t.Fatalf("expectations: %v", mErr)
	}
}

// TestDispatchAliasOutboxRowNilClient pins the disabled-worker guard: with no
// Navigator client configured, dispatch reports "navigator" as failed rather
// than panicking, so the row stays pending for a replica that has the client.
func TestDispatchAliasOutboxRowNilClient(t *testing.T) {
	db, _, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer func() { _ = db.Close() }()
	server := NewQuartermasterServer(db, logging.NewLogger(), nil, nil, nil, nil, nil)

	failed, dispatchErr := server.dispatchAliasOutboxRow(context.Background(), aliasOutboxRow{action: "ensure", tenantID: "tenant-1", subdomain: "acme"})
	if dispatchErr == nil {
		t.Fatal("expected error when navigator client is nil")
	}
	if len(failed) != 1 || failed[0] != "navigator" {
		t.Fatalf("failed = %v, want [navigator]", failed)
	}
}
