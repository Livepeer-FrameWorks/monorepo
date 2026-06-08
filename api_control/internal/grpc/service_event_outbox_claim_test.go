package grpc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/sirupsen/logrus"
)

// claimCommodoreServiceOutboxBatch must select uncompleted, unexpired-lease rows
// oldest-first and lease each one (claimed_at = NOW()) in the SAME transaction,
// so a peer replica's claim predicate skips them. Pins the at-least-once,
// no-double-dispatch ordering contract.
func TestClaimCommodoreServiceOutboxBatch(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close() //nolint:errcheck

	created := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	mock.ExpectBegin()
	mock.ExpectQuery("FROM commodore.service_event_outbox").
		WillReturnRows(sqlmock.NewRows([]string{"id", "payload", "attempts", "created_at"}).
			AddRow("outbox-1", `{"k":"v"}`, 3, created))
	mock.ExpectExec("SET claimed_at = NOW").
		WithArgs(sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	server := &CommodoreServer{db: db, logger: logrus.New()}
	rows, err := server.claimCommodoreServiceOutboxBatch(context.Background())
	if err != nil {
		t.Fatalf("claimCommodoreServiceOutboxBatch: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].id != "outbox-1" || rows[0].attempts != 3 || string(rows[0].payload) != `{"k":"v"}` {
		t.Fatalf("unexpected row: %+v", rows[0])
	}
	if mErr := mock.ExpectationsWereMet(); mErr != nil {
		t.Fatalf("expectations: %v", mErr)
	}
}

// An empty due-set must issue NO lease UPDATE (no spurious writes) and commit.
func TestClaimCommodoreServiceOutboxBatchEmpty(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close() //nolint:errcheck

	mock.ExpectBegin()
	mock.ExpectQuery("FROM commodore.service_event_outbox").
		WillReturnRows(sqlmock.NewRows([]string{"id", "payload", "attempts", "created_at"}))
	mock.ExpectCommit()

	server := &CommodoreServer{db: db, logger: logrus.New()}
	rows, err := server.claimCommodoreServiceOutboxBatch(context.Background())
	if err != nil {
		t.Fatalf("claimCommodoreServiceOutboxBatch: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("rows = %d, want 0", len(rows))
	}
	if mErr := mock.ExpectationsWereMet(); mErr != nil {
		t.Fatalf("expectations: %v", mErr)
	}
}

// recordCommodoreServiceOutboxFailure must RELEASE the lease (claimed_at = NULL)
// while bumping attempts and recording the cause, so the row is re-claimable on
// the next sweep rather than stranded under a dead lease.
func TestRecordCommodoreServiceOutboxFailureReleasesLease(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close() //nolint:errcheck

	mock.ExpectExec(`SET attempts = \$2, last_error = \$3, claimed_at = NULL`).
		WithArgs("outbox-1", 4, "decklog unreachable").
		WillReturnResult(sqlmock.NewResult(0, 1))

	server := &CommodoreServer{db: db, logger: logrus.New()}
	server.recordCommodoreServiceOutboxFailure(context.Background(), "outbox-1", 4, errors.New("decklog unreachable"))

	if mErr := mock.ExpectationsWereMet(); mErr != nil {
		t.Fatalf("expectations: %v", mErr)
	}
}
