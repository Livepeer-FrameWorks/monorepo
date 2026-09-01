package grpc

import (
	"context"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	decklogclient "github.com/Livepeer-FrameWorks/monorepo/pkg/clients/decklog"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

const (
	billingOutboxID1 = "91000000-0000-4000-8000-000000000001"
	billingOutboxID2 = "91000000-0000-4000-8000-000000000002"
	billingTenantID1 = "92000000-0000-4000-8000-000000000001"
	billingTenantID2 = "92000000-0000-4000-8000-000000000002"
)

// jsonContains matches a []byte/string SQL arg whose text contains substr. Used
// to pin the protojson payload written to billing_event without asserting an
// exact byte sequence (field ordering in protojson is not contractual).
type jsonContains struct{ substr string }

type capturingServiceEventSender struct {
	events []*ipcpb.ServiceEvent
	err    error
}

func (s *capturingServiceEventSender) SendServiceEvent(event *ipcpb.ServiceEvent) error {
	s.events = append(s.events, event)
	return s.err
}

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
		WithArgs(sqlmock.AnyArg(), "payment_succeeded", "tenant-1", "user-1", "payment", "pay-9", jsonContains{"tenant-1"}).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(billingOutboxID1))

	id, err := s.EnqueueBillingEventTx(
		context.Background(), s.db,
		"payment_succeeded", "tenant-1", "user-1", "payment", "pay-9",
		&ipcpb.BillingEvent{},
	)
	if err != nil {
		t.Fatalf("EnqueueBillingEventTx: %v", err)
	}
	if id != billingOutboxID1 {
		t.Fatalf("id = %q, want %s", id, billingOutboxID1)
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
		WithArgs(sqlmock.AnyArg(), "topup_created", "tenant-2", "", "topup", "tp-1", jsonContains{"tenant-2"}).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(billingOutboxID2))

	id, err := s.EnqueueBillingEventTx(
		context.Background(), s.db,
		"topup_created", "tenant-2", "", "topup", "tp-1", nil,
	)
	if err != nil {
		t.Fatalf("EnqueueBillingEventTx: %v", err)
	}
	if id != billingOutboxID2 {
		t.Fatalf("id = %q, want %s", id, billingOutboxID2)
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
		WithArgs(sqlmock.AnyArg(), "evt", "tenant-1", "u", "r", "rid", jsonContains{"tenant-1"}).
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(billingOutboxID1))

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
		AddRow(billingOutboxID1, "payment_succeeded", billingTenantID1, "user-1", "payment", "pay-1", []byte(`{"tenant_id":"`+billingTenantID1+`"}`), 0, now).
		AddRow(billingOutboxID2, "topup_created", billingTenantID2, "", "topup", "tp-2", []byte(`{"tenant_id":"`+billingTenantID2+`"}`), 3, now)

	mock.ExpectBegin()
	mock.ExpectQuery(`FROM purser\.billing_event_outbox\s+WHERE completed_at IS NULL`).
		WillReturnRows(rows)
	// Claimed ids are stamped in one generated UUID-array update.
	mock.ExpectExec(`UPDATE purser\.billing_event_outbox\s+SET claimed_at = NOW`).
		WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg()).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectCommit()

	claims, err := (&billingOutboxStore{server: s}).ClaimBatch(context.Background(), 2, time.Second)
	if err != nil {
		t.Fatalf("ClaimBatch: %v", err)
	}
	if len(claims) != 2 {
		t.Fatalf("got %d rows, want 2", len(claims))
	}
	out := []billingOutboxRow{claims[0].Payload, claims[1].Payload}
	if claims[0].LeaseToken == "" || claims[0].LeaseToken != out[0].leaseToken {
		t.Fatalf("claim lease token was not propagated: %+v", claims[0])
	}
	if out[0].id != billingOutboxID1 || out[0].eventType != "payment_succeeded" || out[0].tenantID != billingTenantID1 {
		t.Fatalf("row0 mapping wrong: %+v", out[0])
	}
	if out[1].attempts != 3 {
		t.Fatalf("row1 attempts = %d, want 3", out[1].attempts)
	}
	if string(out[0].billingJSON) != `{"tenant_id":"`+billingTenantID1+`"}` {
		t.Fatalf("billingJSON not carried from billing_event: %q", out[0].billingJSON)
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

	if err := (&billingOutboxStore{server: s}).MarkCompleted(context.Background(), "outbox-1"); err != nil {
		t.Fatalf("MarkCompleted: %v", err)
	}
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
	mock.ExpectExec(`UPDATE purser\.billing_event_outbox\s+SET attempts = attempts \+ 1, last_error = \$1, claimed_at = NULL`).
		WithArgs("decklog timeout", "outbox-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := (&billingOutboxStore{server: s}).RecordFailure(context.Background(), "outbox-1", 4, nil, errors.New("decklog timeout"), 0); err != nil {
		t.Fatalf("RecordFailure: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestRecordBillingOutboxFailureNilCauseAndAlertThreshold(t *testing.T) {
	s, mock := newReadServer(t, true)
	// nil cause -> empty last_error; attempts >= billingOutboxAlertAfterAttempts
	// (12) exercises the repeated-failure alert-log branch.
	mock.ExpectExec(`UPDATE purser\.billing_event_outbox`).
		WithArgs("", "outbox-1").
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

func TestNewPurserServerDoesNotBoxTypedNilDecklogClient(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()
	mock.ExpectQuery(`SELECT xpub\s+FROM purser\.hd_wallet_state`).
		WillReturnRows(sqlmock.NewRows([]string{"xpub"}).AddRow("test-xpub"))

	var decklogClient *decklogclient.BatchedClient
	server := NewPurserServer(db, logging.NewLogger(), nil, nil, nil, nil, nil, decklogClient, nil)
	if server.decklogClient != nil {
		t.Fatalf("typed-nil Decklog client was boxed as non-nil: %#v", server.decklogClient)
	}
	if _, err := server.dispatchBillingOutboxRow(context.Background(), billingOutboxRow{id: "outbox-1"}); err == nil || !strings.Contains(err.Error(), "decklog client not configured") {
		t.Fatalf("dispatch with typed-nil client = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}

func TestDispatchBillingOutboxRowPreservesStableEventIdentity(t *testing.T) {
	s, _ := newReadServer(t, true)
	capture := &capturingServiceEventSender{}
	s.decklogClient = capture
	createdAt := time.Date(2026, 8, 31, 10, 0, 0, 0, time.UTC)
	row := billingOutboxRow{
		id: billingOutboxID1, eventType: "payment_succeeded", tenantID: billingTenantID1,
		resourceType: "payment", resourceID: "pay-1", billingJSON: []byte(`{}`), createdAt: createdAt,
	}

	dispatcher := &billingOutboxDispatcher{server: s}
	for attempt := 0; attempt < 2; attempt++ {
		if failed, err := dispatcher.Dispatch(context.Background(), row); err != nil || len(failed) != 0 {
			t.Fatalf("dispatch %d = failed %v, error %v", attempt, failed, err)
		}
	}
	if len(capture.events) != 2 {
		t.Fatalf("captured %d events, want 2", len(capture.events))
	}
	for _, event := range capture.events {
		if event.GetEventId() != billingOutboxID1 {
			t.Fatalf("event id = %q, want stable outbox id", event.GetEventId())
		}
		if event.GetTenantId() != billingTenantID1 || event.GetBillingEvent().GetTenantId() != billingTenantID1 {
			t.Fatalf("tenant identity was not preserved: %+v", event)
		}
		if !event.GetTimestamp().AsTime().Equal(createdAt) {
			t.Fatalf("timestamp = %v, want %v", event.GetTimestamp().AsTime(), createdAt)
		}
	}
}

func TestRunBillingOutboxWorkerWithoutDecklogReturns(t *testing.T) {
	s, _ := newReadServer(t, true)
	s.runBillingOutboxWorker(context.Background())
}

func TestBillingOutboxTokenSettlementRejectsStaleLease(t *testing.T) {
	s, mock := newReadServer(t, true)
	store := &billingOutboxStore{server: s}

	mock.ExpectExec(`UPDATE purser\.billing_event_outbox[\s\S]*lease_token = NULL[\s\S]*lease_token = \$2`).
		WithArgs("outbox-1", "lease-current").
		WillReturnResult(sqlmock.NewResult(0, 0))
	if err := store.MarkCompletedToken(context.Background(), "outbox-1", "lease-current"); err == nil {
		t.Fatal("stale completion should report a lost lease")
	}

	mock.ExpectExec(`UPDATE purser\.billing_event_outbox[\s\S]*attempts = attempts \+ 1[\s\S]*lease_token = \$3`).
		WithArgs("decklog down", "outbox-1", "lease-current").
		WillReturnResult(sqlmock.NewResult(0, 0))
	if err := store.RecordFailureToken(context.Background(), "outbox-1", 0, nil, errors.New("decklog down"), 0, "lease-current"); err == nil {
		t.Fatal("stale failure should report a lost lease")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}
}
