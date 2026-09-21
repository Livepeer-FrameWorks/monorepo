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
// oldest-first and lease each one (claimed_at = NOW() plus a per-claim token)
// in the SAME transaction, so a peer replica's claim predicate skips them.
// Pins the at-least-once, no-double-dispatch ordering contract.
func TestClaimCommodoreServiceOutboxBatch(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close() //nolint:errcheck

	created := time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC)
	mock.ExpectBegin()
	mock.ExpectQuery("FROM commodore.service_event_outbox").
		WillReturnRows(sqlmock.NewRows([]string{"id", "event_id", "payload", "attempts", "created_at"}).
			AddRow("outbox-1", "event-1", `{"k":"v"}`, 3, created))
	mock.ExpectExec("SET claimed_at = NOW").
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg()).
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
	if rows[0].id != "outbox-1" || rows[0].eventID != "event-1" || rows[0].attempts != 3 || string(rows[0].payload) != `{"k":"v"}` {
		t.Fatalf("unexpected row: %+v", rows[0])
	}
	if rows[0].leaseToken == "" {
		t.Fatal("claimed row carries no lease token")
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
		WillReturnRows(sqlmock.NewRows([]string{"id", "event_id", "payload", "attempts", "created_at"}))
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
// the next sweep rather than stranded under a dead lease. It is fenced by the
// claim's lease token.
func TestRecordCommodoreServiceOutboxFailureReleasesLease(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close() //nolint:errcheck

	mock.ExpectExec(`SET attempts = \$1, last_error = \$2, claimed_at = NULL`).
		WithArgs(4, "decklog unreachable", "outbox-1", "lease-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	server := &CommodoreServer{db: db, logger: logrus.New()}
	server.recordCommodoreServiceOutboxFailure(context.Background(), "outbox-1", 4, errors.New("decklog unreachable"), "lease-1")

	if mErr := mock.ExpectationsWereMet(); mErr != nil {
		t.Fatalf("expectations: %v", mErr)
	}
}

// The dispatched event carries the row's stored event ID, not the ID in the
// payload or a fresh one, so every attempt sends the same ID.
func TestDecodeCommodoreServiceOutboxRowUsesStoredEventID(t *testing.T) {
	event, err := decodeCommodoreServiceOutboxRow(commodoreServiceOutboxRow{
		id: "outbox-1", eventID: "0190a0a0-0000-7000-8000-000000000001",
		payload: []byte(`{"eventType":"stream_updated","tenantId":"t"}`),
	})
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if event.GetEventId() != "0190a0a0-0000-7000-8000-000000000001" || event.GetEventType() != "stream_updated" {
		t.Fatalf("decoded event = %v", event)
	}
	if _, err := decodeCommodoreServiceOutboxRow(commodoreServiceOutboxRow{id: "outbox-2", payload: []byte(`{}`)}); err == nil {
		t.Fatal("row without an event id decoded")
	}
}
