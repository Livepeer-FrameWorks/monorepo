package grpc

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/sirupsen/logrus"
)

// TestClaimInvalidationOutboxBatch pins the claim transaction: pending rows due
// now are selected (SKIP LOCKED, ordered by next_attempt_at) and each is leased
// forward in the SAME transaction so a peer replica's `next_attempt_at <= NOW()`
// predicate skips it. The internal_names jsonb must round-trip into the row.
func TestClaimInvalidationOutboxBatch(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close() //nolint:errcheck

	mock.ExpectBegin()
	mock.ExpectQuery("FROM commodore.playback_policy_invalidation_outbox").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "tenant_id", "reason", "internal_names", "attempts", "stream_id", "bundle_min_version",
		}).AddRow("outbox-1", "tenant-1", "key_revoked", []byte(`["stream-x","stream-y"]`), 2, "stream-1", int64(7)))
	// One lease UPDATE per claimed row.
	mock.ExpectExec("SET next_attempt_at = NOW").
		WithArgs(sqlmock.AnyArg(), "outbox-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	server := &CommodoreServer{db: db, logger: logrus.New()}
	rows, err := server.claimInvalidationOutboxBatch(context.Background())
	if err != nil {
		t.Fatalf("claimInvalidationOutboxBatch: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	r := rows[0]
	if r.id != "outbox-1" || r.tenantID != "tenant-1" || r.attempts != 2 || r.bundleMinVersion != 7 {
		t.Fatalf("unexpected row scan: %+v", r)
	}
	if len(r.internalNames) != 2 || r.internalNames[0] != "stream-x" {
		t.Fatalf("internal_names not parsed: %v", r.internalNames)
	}
	if mErr := mock.ExpectationsWereMet(); mErr != nil {
		t.Fatalf("expectations: %v", mErr)
	}
}

// TestClaimInvalidationOutboxBatchEmpty pins that an empty due-set issues no
// lease UPDATE and commits cleanly (no spurious writes when nothing is pending).
func TestClaimInvalidationOutboxBatchEmpty(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close() //nolint:errcheck

	mock.ExpectBegin()
	mock.ExpectQuery("FROM commodore.playback_policy_invalidation_outbox").
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "tenant_id", "reason", "internal_names", "attempts", "stream_id", "bundle_min_version",
		}))
	mock.ExpectCommit()

	server := &CommodoreServer{db: db, logger: logrus.New()}
	rows, err := server.claimInvalidationOutboxBatch(context.Background())
	if err != nil {
		t.Fatalf("claimInvalidationOutboxBatch: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("rows = %d, want 0", len(rows))
	}
	if mErr := mock.ExpectationsWereMet(); mErr != nil {
		t.Fatalf("expectations: %v", mErr)
	}
}

func TestMarkInvalidationOutboxCompleted(t *testing.T) {
	t.Run("empty_id_is_noop", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		defer db.Close() //nolint:errcheck
		// No expectations registered: any DB call would fail ExpectationsWereMet.
		server := &CommodoreServer{db: db, logger: logrus.New()}
		server.markInvalidationOutboxCompleted(context.Background(), "")
		if mErr := mock.ExpectationsWereMet(); mErr != nil {
			t.Fatalf("empty id should issue no DB call: %v", mErr)
		}
	})

	t.Run("completes_pending_row", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		defer db.Close() //nolint:errcheck
		// The status='pending' guard is the defence against overwriting a row a
		// peer worker already completed.
		mock.ExpectExec("SET status = 'completed'").
			WithArgs("outbox-1").
			WillReturnResult(sqlmock.NewResult(0, 1))

		server := &CommodoreServer{db: db, logger: logrus.New()}
		server.markInvalidationOutboxCompleted(context.Background(), "outbox-1")
		if mErr := mock.ExpectationsWereMet(); mErr != nil {
			t.Fatalf("expectations: %v", mErr)
		}
	})
}
