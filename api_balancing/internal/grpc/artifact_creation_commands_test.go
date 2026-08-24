package grpc

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// An empty request_id (direct-Foghorn caller with no Commodore intent) records
// nothing — the ledger is only written for reconcilable create attempts — and resolves
// to the accepted state so the create proceeds.
func TestRecordCreationCommand_EmptyRequestIDNoOps(t *testing.T) {
	srv, mock, cleanup := newStatusServer(t, &fakeVodS3Client{})
	defer cleanup()

	if state, err := recordCreationCommandAccepted(context.Background(), srv.db, "", "t1", "clip", "h1"); err != nil || state != creationCommandAccepted {
		t.Fatalf("empty request_id must proceed, got state=%v err=%v", state, err)
	}
	if applied, err := recordCreationCommandRejected(context.Background(), srv.db, "", "t1", "clip", "h1"); err != nil || applied {
		t.Fatalf("empty request_id must no-op, got applied=%v err=%v", applied, err)
	}
	// No DB expectations were set; any statement would fail ExpectationsWereMet.
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// 'accepted' inserts idempotently (ON CONFLICT DO NOTHING) then reads back the stored
// row's identity and status; a matching 'accepted' row resolves to the accepted state
// (a legitimate retry that resumes the create).
func TestRecordCreationCommandAccepted_Idempotent(t *testing.T) {
	srv, mock, cleanup := newStatusServer(t, &fakeVodS3Client{})
	defer cleanup()

	mock.ExpectExec(`INSERT INTO foghorn\.artifact_creation_commands`).
		WithArgs("req-1", "t1", "clip", "h1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT COALESCE\(tenant_id`).
		WithArgs("t1", "clip", "h1", "req-1").
		WillReturnRows(sqlmock.NewRows([]string{"identity_ok", "status"}).AddRow(true, "accepted"))

	state, err := recordCreationCommandAccepted(context.Background(), srv.db, "req-1", "t1", "clip", "h1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != creationCommandAccepted {
		t.Fatalf("state = %v, want accepted", state)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A retry whose stored row is already 'committed' resolves to the committed state so the
// handler short-circuits to the idempotent result instead of redoing external work.
func TestRecordCreationCommandAccepted_CommittedRetryShortCircuits(t *testing.T) {
	srv, mock, cleanup := newStatusServer(t, &fakeVodS3Client{})
	defer cleanup()

	mock.ExpectExec(`INSERT INTO foghorn\.artifact_creation_commands`).
		WithArgs("req-1", "t1", "vod", "h1").
		WillReturnResult(sqlmock.NewResult(0, 0)) // conflict: row already exists
	mock.ExpectQuery(`SELECT COALESCE\(tenant_id`).
		WithArgs("t1", "vod", "h1", "req-1").
		WillReturnRows(sqlmock.NewRows([]string{"identity_ok", "status"}).AddRow(true, "committed"))

	state, err := recordCreationCommandAccepted(context.Background(), srv.db, "req-1", "t1", "vod", "h1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != creationCommandCommitted {
		t.Fatalf("state = %v, want committed", state)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A retry whose stored row is already 'rejected' resolves to the rejected state so the
// handler terminally rejects the create rather than proceeding.
func TestRecordCreationCommandAccepted_RejectedRetryDoesNotProceed(t *testing.T) {
	srv, mock, cleanup := newStatusServer(t, &fakeVodS3Client{})
	defer cleanup()

	mock.ExpectExec(`INSERT INTO foghorn\.artifact_creation_commands`).
		WithArgs("req-1", "t1", "clip", "h1").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT COALESCE\(tenant_id`).
		WithArgs("t1", "clip", "h1", "req-1").
		WillReturnRows(sqlmock.NewRows([]string{"identity_ok", "status"}).AddRow(true, "rejected"))

	state, err := recordCreationCommandAccepted(context.Background(), srv.db, "req-1", "t1", "clip", "h1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if state != creationCommandRejected {
		t.Fatalf("state = %v, want rejected", state)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A request_id whose stored row carries a DIFFERENT (tenant, kind, artifact_hash) was
// reused for another artifact: the identity read reports a mismatch and the create
// fails rather than proceeding against an unrelated intent.
func TestRecordCreationCommandAccepted_IdentityMismatchFails(t *testing.T) {
	srv, mock, cleanup := newStatusServer(t, &fakeVodS3Client{})
	defer cleanup()

	mock.ExpectExec(`INSERT INTO foghorn\.artifact_creation_commands`).
		WithArgs("req-1", "t1", "clip", "h2").
		WillReturnResult(sqlmock.NewResult(0, 0)) // conflict: row already exists
	mock.ExpectQuery(`SELECT COALESCE\(tenant_id`).
		WithArgs("t1", "clip", "h2", "req-1").
		WillReturnRows(sqlmock.NewRows([]string{"identity_ok", "status"}).AddRow(false, "accepted"))

	_, err := recordCreationCommandAccepted(context.Background(), srv.db, "req-1", "t1", "clip", "h2")
	if !errors.Is(err, errCreationCommandIdentityMismatch) {
		t.Fatalf("expected identity mismatch, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// 'committed' is a CAS on the attempt's own still-'accepted' row; one affected row is
// the terminal transition.
func TestRecordCreationCommandCommitted(t *testing.T) {
	srv, mock, cleanup := newStatusServer(t, &fakeVodS3Client{})
	defer cleanup()

	mock.ExpectExec(`UPDATE foghorn\.artifact_creation_commands`).
		WithArgs("vh1", "t1", "vod", "req-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := recordCreationCommandCommitted(context.Background(), srv.db, "req-1", "t1", "vod", "vh1"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// When the deadline expiry already rejected the attempt, the commit CAS matches no
// 'accepted' row (zero affected) and returns errCreationCommandNotAccepted so the
// artifact-insert tx rolls back — no artifact is persisted behind a rejected ledger.
func TestRecordCreationCommandCommitted_LostToExpiryReturnsError(t *testing.T) {
	srv, mock, cleanup := newStatusServer(t, &fakeVodS3Client{})
	defer cleanup()

	mock.ExpectExec(`UPDATE foghorn\.artifact_creation_commands`).
		WithArgs("vh1", "t1", "vod", "req-1").
		WillReturnResult(sqlmock.NewResult(0, 0))

	err := recordCreationCommandCommitted(context.Background(), srv.db, "req-1", "t1", "vod", "vh1")
	if !errors.Is(err, errCreationCommandNotAccepted) {
		t.Fatalf("expected not-accepted error, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// 'rejected' CAS-flips the attempt's own still-'accepted' row; a flipped row reports
// applied.
func TestRecordCreationCommandRejected_Applied(t *testing.T) {
	srv, mock, cleanup := newStatusServer(t, &fakeVodS3Client{})
	defer cleanup()

	mock.ExpectExec(`UPDATE foghorn\.artifact_creation_commands`).
		WithArgs("req-1", "t1", "clip", "h1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	applied, err := recordCreationCommandRejected(context.Background(), srv.db, "req-1", "t1", "clip", "h1")
	if err != nil || !applied {
		t.Fatalf("expected applied reject, got applied=%v err=%v", applied, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A 'rejected' CAS that matches nothing — the row is already terminal (a racing
// commit won, or a prior rejection) or is not ours — reports applied=false with no
// error, so the caller treats it as an already-converged no-op.
func TestRecordCreationCommandRejected_NoRowsWhenTerminal(t *testing.T) {
	srv, mock, cleanup := newStatusServer(t, &fakeVodS3Client{})
	defer cleanup()

	mock.ExpectExec(`UPDATE foghorn\.artifact_creation_commands`).
		WithArgs("req-1", "t1", "clip", "h1").
		WillReturnResult(sqlmock.NewResult(0, 0))

	applied, err := recordCreationCommandRejected(context.Background(), srv.db, "req-1", "t1", "clip", "h1")
	if err != nil || applied {
		t.Fatalf("expected no-op reject, got applied=%v err=%v", applied, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// An accepted create that fails pre-commit with an ambiguous gRPC code (Internal)
// must still reach a terminal 'rejected' ledger outcome: the handler's control flow
// proves the artifact was not inserted, so the gRPC code is irrelevant. The finalizer
// CAS-rejects the still-'accepted' row and preserves the handler's original error.
func TestFinalizeCreationCommand_PreCommitInternalRecordsRejected(t *testing.T) {
	srv, mock, cleanup := newStatusServer(t, &fakeVodS3Client{})
	defer cleanup()

	mock.ExpectExec(`UPDATE foghorn\.artifact_creation_commands`).
		WithArgs("req-1", "t1", "clip", "h1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	prog := &creationLedgerProgress{accepted: true}
	retErr := status.Error(codes.Internal, "boom")
	got := srv.finalizeCreationCommand("req-1", "t1", "clip", "h1", prog, retErr)
	if status.Code(got) != codes.Internal {
		t.Fatalf("finalize must preserve the handler error, got %v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A finalizer reject that races a concurrent commit (the CAS matches nothing) is not
// an error: the row is already terminal, the handler error is preserved, and no retry
// storms. The single CAS statement affecting zero rows is the whole interaction.
func TestFinalizeCreationCommand_LostRejectRaceIsNoError(t *testing.T) {
	srv, mock, cleanup := newStatusServer(t, &fakeVodS3Client{})
	defer cleanup()

	mock.ExpectExec(`UPDATE foghorn\.artifact_creation_commands`).
		WithArgs("req-1", "t1", "clip", "h1").
		WillReturnResult(sqlmock.NewResult(0, 0))

	prog := &creationLedgerProgress{accepted: true}
	retErr := status.Error(codes.Internal, "boom")
	got := srv.finalizeCreationCommand("req-1", "t1", "clip", "h1", prog, retErr)
	if status.Code(got) != codes.Internal {
		t.Fatalf("finalize must preserve the handler error, got %v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A committed create that then fails a later step (e.g. DVR start dispatch) must NOT
// be terminalized 'rejected' — the artifact was durably inserted. No ledger write is
// issued (any statement would fail ExpectationsWereMet).
func TestFinalizeCreationCommand_CommittedNeverRejected(t *testing.T) {
	srv, mock, cleanup := newStatusServer(t, &fakeVodS3Client{})
	defer cleanup()

	prog := &creationLedgerProgress{accepted: true, committed: true}
	retErr := status.Error(codes.Internal, "post-commit boom")
	if got := srv.finalizeCreationCommand("req-1", "t1", "dvr", "h1", prog, retErr); status.Code(got) != codes.Internal {
		t.Fatalf("finalize must preserve the handler error, got %v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A failure before 'accepted' was durably recorded writes NO ledger row: the absence
// is in-flight and the deadline expiry converges it, rather than a premature
// 'rejected'.
func TestFinalizeCreationCommand_NotAcceptedWritesNothing(t *testing.T) {
	srv, mock, cleanup := newStatusServer(t, &fakeVodS3Client{})
	defer cleanup()

	prog := &creationLedgerProgress{} // accepted never set
	retErr := status.Error(codes.Unavailable, "accept write failed")
	if got := srv.finalizeCreationCommand("req-1", "t1", "vod", "h1", prog, retErr); status.Code(got) != codes.Unavailable {
		t.Fatalf("finalize must preserve the handler error, got %v", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// The DVR artifact insert and its 'committed' CAS share ONE transaction: when the CAS
// matches no 'accepted' row (the deadline expiry won the race) it errors, rolling the
// whole tx back, so no DVR artifact row survives behind a rejected ledger row.
func TestDVRArtifactInsertAndCommittedShareTx(t *testing.T) {
	srv, mock, cleanup := newStatusServer(t, &fakeVodS3Client{})
	defer cleanup()

	ctx := context.Background()
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO foghorn\.artifacts`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE foghorn\.artifact_creation_commands`).
		WithArgs("dh1", "t1", "dvr", "req-dvr").
		WillReturnResult(sqlmock.NewResult(0, 0)) // expiry already rejected: zero rows
	mock.ExpectRollback()

	err := srv.withArtifactLifecycleTx(ctx, func(tx *sql.Tx) error {
		if _, execErr := tx.ExecContext(ctx, `INSERT INTO foghorn.artifacts (artifact_hash) VALUES ($1)`, "dh1"); execErr != nil {
			return execErr
		}
		return recordCreationCommandCommitted(ctx, tx, "req-dvr", "t1", "dvr", "dh1")
	})
	if !errors.Is(err, errCreationCommandNotAccepted) {
		t.Fatalf("expected the tx to fail with not-accepted, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
