package grpc

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// jsonContains matches a []byte/string SQL arg whose text contains substr. Used
// to pin the protojson payload written to billing_event without asserting an
// exact byte sequence (field ordering in protojson is not contractual).
type jsonContains struct{ substr string }

func (m jsonContains) Match(v driver.Value) bool {
	switch b := v.(type) {
	case []byte:
		return strings.Contains(string(b), m.substr)
	case string:
		return strings.Contains(b, m.substr)
	default:
		return false
	}
}

func TestEnqueueBillingEventTxInsertsAndBackfillsTenant(t *testing.T) {
	s, mock := newReadServer(t, true)

	// payload.TenantId is empty: the method must backfill it from the tenantID
	// arg before marshaling, so the persisted JSON carries the tenant.
	mock.ExpectQuery(`INSERT INTO purser\.billing_event_outbox`).
		WithArgs("payment_succeeded", "tenant-1", "user-1", "payment", "pay-9", jsonContains{"tenant-1"}).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("outbox-1"))

	id, err := s.EnqueueBillingEventTx(
		context.Background(), s.db,
		"payment_succeeded", "tenant-1", "user-1", "payment", "pay-9",
		&ipcpb.BillingEvent{},
	)
	if err != nil {
		t.Fatalf("EnqueueBillingEventTx: %v", err)
	}
	if id != "outbox-1" {
		t.Fatalf("id = %q, want outbox-1", id)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestEnqueueBillingEventTxNilPayloadDefaults(t *testing.T) {
	s, mock := newReadServer(t, true)

	// nil payload must not panic; it is replaced with an empty event and the
	// tenant backfilled into it.
	mock.ExpectQuery(`INSERT INTO purser\.billing_event_outbox`).
		WithArgs("topup_created", "tenant-2", "", "topup", "tp-1", jsonContains{"tenant-2"}).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("outbox-2"))

	id, err := s.EnqueueBillingEventTx(
		context.Background(), s.db,
		"topup_created", "tenant-2", "", "topup", "tp-1", nil,
	)
	if err != nil {
		t.Fatalf("EnqueueBillingEventTx: %v", err)
	}
	if id != "outbox-2" {
		t.Fatalf("id = %q, want outbox-2", id)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestEnqueueBillingEventTxScanErrorWrapped(t *testing.T) {
	s, mock := newReadServer(t, true)
	mock.ExpectQuery(`INSERT INTO purser\.billing_event_outbox`).
		WillReturnError(errors.New("boom"))

	_, err := s.EnqueueBillingEventTx(
		context.Background(), s.db,
		"x", "tenant-1", "", "r", "rid", &ipcpb.BillingEvent{},
	)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "insert billing event outbox row") {
		t.Fatalf("error not wrapped with context: %v", err)
	}
}

func TestEnqueueBillingEventShortCircuits(t *testing.T) {
	// nil db: returns silently, never touches the database.
	nilDB := &PurserServer{db: nil, logger: logging.NewLogger()}
	nilDB.enqueueBillingEvent(context.Background(), "evt", "tenant-1", "", "r", "rid", &ipcpb.BillingEvent{})

	// empty tenant: also short-circuits before issuing any query.
	s, mock := newReadServer(t, true)
	s.enqueueBillingEvent(context.Background(), "evt", "", "", "r", "rid", &ipcpb.BillingEvent{})
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("empty-tenant path should issue no query: %v", err)
	}
}

func TestEnqueueBillingEventHappyPath(t *testing.T) {
	s, mock := newReadServer(t, true)
	mock.ExpectQuery(`INSERT INTO purser\.billing_event_outbox`).
		WithArgs("evt", "tenant-1", "u", "r", "rid", jsonContains{"tenant-1"}).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("ok"))

	s.enqueueBillingEvent(context.Background(), "evt", "tenant-1", "u", "r", "rid", &ipcpb.BillingEvent{})
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestClaimBillingOutboxBatchMapsRowsAndClaims(t *testing.T) {
	s, mock := newReadServer(t, true)
	now := time.Now()

	rows := sqlmock.NewRows([]string{
		"id", "event_type", "tenant_id", "user_id",
		"resource_type", "resource_id", "billing_event", "attempts", "created_at",
	}).
		AddRow("outbox-1", "payment_succeeded", "tenant-1", "user-1", "payment", "pay-1", `{"tenant_id":"tenant-1"}`, 0, now).
		AddRow("outbox-2", "topup_created", "tenant-2", "", "topup", "tp-2", `{"tenant_id":"tenant-2"}`, 3, now)

	mock.ExpectBegin()
	mock.ExpectQuery(`FROM purser\.billing_event_outbox\s+WHERE completed_at IS NULL`).
		WillReturnRows(rows)
	// Claimed ids are stamped in one UPDATE keyed by the {a,b} uuid[] literal.
	mock.ExpectExec(`UPDATE purser\.billing_event_outbox\s+SET claimed_at = NOW`).
		WithArgs("{outbox-1,outbox-2}").
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectCommit()

	out, err := s.claimBillingOutboxBatch(context.Background())
	if err != nil {
		t.Fatalf("claimBillingOutboxBatch: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("got %d rows, want 2", len(out))
	}
	if out[0].id != "outbox-1" || out[0].eventType != "payment_succeeded" || out[0].tenantID != "tenant-1" {
		t.Fatalf("row0 mapping wrong: %+v", out[0])
	}
	if out[1].attempts != 3 {
		t.Fatalf("row1 attempts = %d, want 3", out[1].attempts)
	}
	if string(out[0].billingJSON) != `{"tenant_id":"tenant-1"}` {
		t.Fatalf("billingJSON not carried from billing_event::text: %q", out[0].billingJSON)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestClaimBillingOutboxBatchEmptyIssuesNoUpdate(t *testing.T) {
	s, mock := newReadServer(t, true)

	empty := sqlmock.NewRows([]string{
		"id", "event_type", "tenant_id", "user_id",
		"resource_type", "resource_id", "billing_event", "attempts", "created_at",
	})
	mock.ExpectBegin()
	mock.ExpectQuery(`FROM purser\.billing_event_outbox`).WillReturnRows(empty)
	// No ExpectExec: an empty batch must not issue the claim UPDATE.
	mock.ExpectCommit()

	out, err := s.claimBillingOutboxBatch(context.Background())
	if err != nil {
		t.Fatalf("claimBillingOutboxBatch: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("got %d rows, want 0", len(out))
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestMarkBillingOutboxCompleted(t *testing.T) {
	s, mock := newReadServer(t, true)
	mock.ExpectExec(`UPDATE purser\.billing_event_outbox\s+SET completed_at = NOW`).
		WithArgs("outbox-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	s.markBillingOutboxCompleted(context.Background(), "outbox-1")
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestMarkBillingOutboxCompletedSwallowsError(t *testing.T) {
	s, mock := newReadServer(t, true)
	// A failed UPDATE is logged, not propagated (no return value).
	mock.ExpectExec(`UPDATE purser\.billing_event_outbox`).
		WithArgs("outbox-1").
		WillReturnError(errors.New("db down"))

	s.markBillingOutboxCompleted(context.Background(), "outbox-1")
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestRecordBillingOutboxFailure(t *testing.T) {
	s, mock := newReadServer(t, true)
	// cause is stored as last_error; attempts persisted; claimed_at cleared.
	mock.ExpectExec(`UPDATE purser\.billing_event_outbox\s+SET attempts = \$2, last_error = \$3, claimed_at = NULL`).
		WithArgs("outbox-1", 4, "decklog timeout").
		WillReturnResult(sqlmock.NewResult(0, 1))

	s.recordBillingOutboxFailure(context.Background(), "outbox-1", 4, errors.New("decklog timeout"))
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestRecordBillingOutboxFailureNilCauseAndAlertThreshold(t *testing.T) {
	s, mock := newReadServer(t, true)
	// nil cause -> empty last_error; attempts >= billingOutboxAlertAfterAttempts
	// (12) exercises the repeated-failure alert-log branch.
	mock.ExpectExec(`UPDATE purser\.billing_event_outbox`).
		WithArgs("outbox-1", billingOutboxAlertAfterAttempts, "").
		WillReturnResult(sqlmock.NewResult(0, 1))

	s.recordBillingOutboxFailure(context.Background(), "outbox-1", billingOutboxAlertAfterAttempts, nil)
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestDispatchBillingOutboxRowRequiresDecklogClient(t *testing.T) {
	// newReadServer leaves decklogClient nil; dispatch must refuse rather than
	// nil-panic, so the row stays claimable for a replica that has the client.
	s, _ := newReadServer(t, true)
	_, err := s.dispatchBillingOutboxRow(context.Background(), billingOutboxRow{id: "outbox-1"})
	if err == nil || !strings.Contains(err.Error(), "decklog client not configured") {
		t.Fatalf("want decklog-not-configured error, got %v", err)
	}
}
