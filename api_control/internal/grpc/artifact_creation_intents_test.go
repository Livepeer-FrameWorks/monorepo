package grpc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"testing"
	"time"

	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"

	"github.com/DATA-DOG/go-sqlmock"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeCreationStatusClient records AckArtifactCreationCommand calls so a test can assert
// the durable ack-drain worker's branch on the returned outcome. ackOutcome is the outcome
// the fake returns (COMMITTED/REJECTED discharge; anything else backs off). ackErr, when
// set, makes the ack RPC fail so a test can assert the obligation is backed off, not
// cleared.
type fakeCreationStatusClient struct {
	ackCalls   int
	lastAck    *sharedpb.AckArtifactCreationCommandRequest
	ackErr     error
	ackOutcome sharedpb.ArtifactCreationOutcome
}

func (f *fakeCreationStatusClient) GetArtifactCreationStatus(ctx context.Context, in *sharedpb.GetArtifactCreationStatusRequest, opts ...grpc.CallOption) (*sharedpb.GetArtifactCreationStatusResponse, error) {
	return &sharedpb.GetArtifactCreationStatusResponse{}, nil
}

func (f *fakeCreationStatusClient) AckArtifactCreationCommand(ctx context.Context, in *sharedpb.AckArtifactCreationCommandRequest, opts ...grpc.CallOption) (*sharedpb.AckArtifactCreationCommandResponse, error) {
	f.ackCalls++
	f.lastAck = in
	if f.ackErr != nil {
		return nil, f.ackErr
	}
	return &sharedpb.AckArtifactCreationCommandResponse{Outcome: f.ackOutcome}, nil
}

// The sweep's terminal action is decided solely by Foghorn's explicit outcome (plus the
// missing deadline): COMMITTED commits, REJECTED aborts, ACCEPTED is in-flight and NEVER
// aborts at any age, and only a past-deadline MISSING is bounded-aborted.
func TestCreationIntentActionForOutcome(t *testing.T) {
	commit := sharedpb.ArtifactCreationOutcome_ARTIFACT_CREATION_OUTCOME_COMMITTED
	reject := sharedpb.ArtifactCreationOutcome_ARTIFACT_CREATION_OUTCOME_REJECTED
	accept := sharedpb.ArtifactCreationOutcome_ARTIFACT_CREATION_OUTCOME_ACCEPTED
	missing := sharedpb.ArtifactCreationOutcome_ARTIFACT_CREATION_OUTCOME_MISSING
	unspec := sharedpb.ArtifactCreationOutcome_ARTIFACT_CREATION_OUTCOME_UNSPECIFIED
	mismatch := sharedpb.ArtifactCreationOutcome_ARTIFACT_CREATION_OUTCOME_IDENTITY_MISMATCH

	cases := []struct {
		name    string
		outcome sharedpb.ArtifactCreationOutcome
		past    bool
		want    creationIntentAction
	}{
		{"committed commits", commit, false, creationIntentActionCommit},
		{"committed commits past deadline too", commit, true, creationIntentActionCommit},
		{"rejected aborts", reject, false, creationIntentActionAbortRejected},
		{"accepted never aborts before deadline", accept, false, creationIntentActionPending},
		{"accepted never aborts even past deadline", accept, true, creationIntentActionPending},
		{"missing waits before deadline", missing, false, creationIntentActionPending},
		{"missing bounded-aborts past deadline", missing, true, creationIntentActionAbortMissing},
		{"unspecified waits", unspec, true, creationIntentActionPending},
		{"identity mismatch never aborts even past deadline", mismatch, true, creationIntentActionPending},
	}
	for _, c := range cases {
		if got := creationIntentActionForOutcome(c.outcome, c.past); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

// A terminal-consumed COMMITTED outcome discharges the obligation: the worker clears
// command_ack_pending=FALSE (command_acked_at set) and issues NO backoff. The ack carries
// the intent's full identity so Foghorn matches the exact command row.
func TestAckAndClearCommand_CommittedClearsObligation(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()

	mock.ExpectExec(`UPDATE commodore\.artifact_creation_intents\s+SET command_ack_pending = FALSE`).
		WithArgs("t1", creationIntentKindVOD, "vh1", "tok-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	fake := &fakeCreationStatusClient{ackOutcome: sharedpb.ArtifactCreationOutcome_ARTIFACT_CREATION_OUTCOME_COMMITTED}
	s.ackAndClearCommand(context.Background(), context.Background(), fake,
		ackPendingRow{tenantID: "t1", kind: creationIntentKindVOD, artifactHash: "vh1", requestID: "req-1", originClusterID: "c1", leaseToken: "tok-1"})

	if fake.ackCalls != 1 {
		t.Fatalf("expected exactly one ack, got %d", fake.ackCalls)
	}
	if fake.lastAck == nil || fake.lastAck.GetRequestId() != "req-1" {
		t.Fatalf("ack must carry the intent's request_id, got %+v", fake.lastAck)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A REJECTED outcome likewise discharges: clear, no backoff.
func TestAckAndClearCommand_RejectedClearsObligation(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()

	mock.ExpectExec(`UPDATE commodore\.artifact_creation_intents\s+SET command_ack_pending = FALSE`).
		WithArgs("t1", creationIntentKindVOD, "vh1", "tok-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	fake := &fakeCreationStatusClient{ackOutcome: sharedpb.ArtifactCreationOutcome_ARTIFACT_CREATION_OUTCOME_REJECTED}
	s.ackAndClearCommand(context.Background(), context.Background(), fake,
		ackPendingRow{tenantID: "t1", kind: creationIntentKindVOD, artifactHash: "vh1", requestID: "req-1", originClusterID: "c1", leaseToken: "tok-1"})

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A DeadlineExceeded ack must STILL durably record its backoff: the settlement write is fenced
// off from the RPC deadline. Both contexts here are already cancelled, yet the backoff UPDATE
// must reach the DB because settleDBContext detaches from cancellation. database/sql fails a
// cancelled-context Exec before the driver, so an unmet mock expectation means the settlement
// did not run under a fresh context.
func TestAckAndClearCommand_DeadlineExceededStillPersistsBackoff(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()

	mock.ExpectExec(`UPDATE commodore\.artifact_creation_intents\s+SET command_ack_attempts = command_ack_attempts \+ 1`).
		WithArgs("t1", creationIntentKindVOD, "vh1", "tok-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	deadSettle, cancelSettle := context.WithCancel(context.Background())
	cancelSettle()
	deadRPC, cancelRPC := context.WithCancel(context.Background())
	cancelRPC()

	fake := &fakeCreationStatusClient{ackErr: status.Error(codes.DeadlineExceeded, "context deadline exceeded")}
	s.ackAndClearCommand(deadSettle, deadRPC, fake,
		ackPendingRow{tenantID: "t1", kind: creationIntentKindVOD, artifactHash: "vh1", requestID: "req-1", originClusterID: "c1", leaseToken: "tok-1"})

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("backoff must persist after a DeadlineExceeded ack: %v", err)
	}
}

// Non-discharging outcomes and RPC errors must NOT clear the obligation; instead they push
// the RETRY backoff (attempts++, next_at forward) and release the lease — the claim itself
// never touches the schedule, so the backoff lives entirely on this path. ACCEPTED (command
// not yet terminal), IDENTITY_MISMATCH (fail closed), an unknown outcome, and an ack RPC error
// all issue exactly one backoff UPDATE and NEVER the clear.
func TestAckAndClearCommand_NonDischargingOutcomesBackOff(t *testing.T) {
	cases := []struct {
		name    string
		outcome sharedpb.ArtifactCreationOutcome
		ackErr  error
	}{
		{"accepted", sharedpb.ArtifactCreationOutcome_ARTIFACT_CREATION_OUTCOME_ACCEPTED, nil},
		{"identity_mismatch", sharedpb.ArtifactCreationOutcome_ARTIFACT_CREATION_OUTCOME_IDENTITY_MISMATCH, nil},
		{"unknown", sharedpb.ArtifactCreationOutcome_ARTIFACT_CREATION_OUTCOME_UNSPECIFIED, nil},
		{"rpc_error", sharedpb.ArtifactCreationOutcome_ARTIFACT_CREATION_OUTCOME_UNSPECIFIED, errors.New("foghorn down")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s, mock, done := newMockServer(t)
			defer done()

			// A non-discharge pushes the backoff; it must NEVER clear the obligation.
			mock.ExpectExec(`UPDATE commodore\.artifact_creation_intents\s+SET command_ack_attempts = command_ack_attempts \+ 1`).
				WithArgs("t1", creationIntentKindVOD, "vh1", "tok-1").
				WillReturnResult(sqlmock.NewResult(0, 1))

			fake := &fakeCreationStatusClient{ackOutcome: c.outcome, ackErr: c.ackErr}
			s.ackAndClearCommand(context.Background(), context.Background(), fake,
				ackPendingRow{tenantID: "t1", kind: creationIntentKindVOD, artifactHash: "vh1", requestID: "req-1", originClusterID: "c1", leaseToken: "tok-1"})

			if fake.ackCalls != 1 {
				t.Fatalf("expected exactly one ack attempt, got %d", fake.ackCalls)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// A MISSING ack on a terminalized intent is an IDEMPOTENT DISCHARGE: command_ack_pending is
// only ever set on an already-terminal local intent, so a MISSING command means Foghorn
// consumed+GC'd it past the retention horizon — nothing left to converge. The drain clears the
// obligation (the same clear COMMITTED/REJECTED use), never a backoff.
func TestAckAndClearCommand_MissingDischargesIdempotently(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()

	mock.ExpectExec(`UPDATE commodore\.artifact_creation_intents\s+SET command_ack_pending = FALSE`).
		WithArgs("t1", creationIntentKindVOD, "vh1", "tok-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	fake := &fakeCreationStatusClient{ackOutcome: sharedpb.ArtifactCreationOutcome_ARTIFACT_CREATION_OUTCOME_MISSING}
	s.ackAndClearCommand(context.Background(), context.Background(), fake,
		ackPendingRow{tenantID: "t1", kind: creationIntentKindVOD, artifactHash: "vh1", requestID: "req-1", originClusterID: "c1", leaseToken: "tok-1"})

	if fake.ackCalls != 1 {
		t.Fatalf("expected exactly one ack, got %d", fake.ackCalls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// The ack-drain claim is a SINGLE atomic UPDATE...RETURNING that stamps the LEASE
// (command_ack_leased_until) and NOTHING else: a FOR UPDATE SKIP LOCKED CTE selects rows that
// are both retry-due (command_ack_next_at past/NULL) AND unleased (command_ack_leased_until
// past/NULL), and the enclosing UPDATE sets command_ack_leased_until forward, RETURNING the
// claimed rows. The claim must NOT touch command_ack_attempts or command_ack_next_at — those
// are the retry axis, pushed only by a non-discharge. An empty due-set does no downstream work.
func TestDrainCreationCommandAcks_AtomicClaimStampsLease(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()

	mock.ExpectQuery(`WITH due AS[\s\S]+command_ack_pending[\s\S]+command_ack_next_at IS NULL OR command_ack_next_at <= NOW\(\)[\s\S]+command_ack_leased_until IS NULL OR command_ack_leased_until <= NOW\(\)[\s\S]+ORDER BY command_ack_next_at[\s\S]+FOR UPDATE SKIP LOCKED[\s\S]+UPDATE commodore\.artifact_creation_intents i[\s\S]+command_ack_leased_until = NOW\(\) \+[\s\S]+command_ack_lease_token = \$3::uuid[\s\S]+RETURNING`).
		WithArgs(creationIntentSweepBatch, intervalSeconds(creationIntentAckLease), sqlmock.AnyArg()).
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id", "kind", "artifact_hash", "request_id", "origin_cluster_id", "command_ack_attempts"}))

	s.drainCreationCommandAcks(context.Background())

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// The ack lease must cover the WHOLE budget: the batch-claim query (its NOW() starts the lease
// clock), a scheduling margin, and the per-item worst case — RPC plus its one settlement write
// (clear OR backoff) — for a batch processed creationIntentAckWorkers at a time. Omitting the
// claim time understates the budget and lets another replica reclaim a still-in-flight batch.
func TestAckLeaseCoversWorstCaseBatch(t *testing.T) {
	waves := (creationIntentSweepBatch + creationIntentAckWorkers - 1) / creationIntentAckWorkers
	perItem := creationIntentRPCTimeout + creationIntentDBSettleTimeout
	budget := creationIntentClaimTimeout + creationIntentLeaseMargin + time.Duration(waves)*perItem
	if budget >= creationIntentAckLease {
		t.Fatalf("ack budget %s (claim %s + margin %s + %d waves * %s) must be < ack lease %s",
			budget, creationIntentClaimTimeout, creationIntentLeaseMargin, waves, perItem, creationIntentAckLease)
	}
}

// The convergence sweep lease must cover the WHOLE budget: the batch-claim query (its NOW()
// starts the lease clock), a scheduling margin, and the per-item worst case — one status RPC
// plus BOTH settlement writes a single item can incur (a failed terminal transition followed by
// the attempt note recording it) — for a batch processed creationIntentSweepWorkers at a time.
func TestSweepLeaseCoversWorstCaseBatch(t *testing.T) {
	waves := (creationIntentSweepBatch + creationIntentSweepWorkers - 1) / creationIntentSweepWorkers
	perItem := creationIntentRPCTimeout + 2*creationIntentDBSettleTimeout
	budget := creationIntentClaimTimeout + creationIntentLeaseMargin + time.Duration(waves)*perItem
	if budget >= creationIntentLeaseTTL {
		t.Fatalf("convergence budget %s (claim %s + margin %s + %d waves * %s) must be < sweep lease %s",
			budget, creationIntentClaimTimeout, creationIntentLeaseMargin, waves, perItem, creationIntentLeaseTTL)
	}
}

// A driver error mid-iteration over the claimed batch must fail the whole sweep pass —
// the truncated batch is NOT processed. Here the claim query yields one row then errors;
// with the rows.Err() guard the pass returns before converging anything, so no downstream
// statement is issued (any would fail ExpectationsWereMet).
func TestSweepCreationIntents_RowsErrFailsPass(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()

	rows := sqlmock.NewRows([]string{"tenant_id", "kind", "artifact_hash", "request_id", "origin_cluster_id", "payload", "past_deadline"}).
		AddRow("t1", "vod", "vh1", "req-1", "c1", nil, false).
		AddRow("t2", "vod", "vh2", "req-2", "c1", nil, false).
		RowError(1, errors.New("driver boom"))
	mock.ExpectQuery(`UPDATE commodore\.artifact_creation_intents AS i`).
		WillReturnRows(rows)

	s.sweepCreationIntentsOnce(context.Background())

	// No convergence work followed (no status RPC, no terminalize) — the claim query is
	// the only interaction.
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// The delete-or-not decision on a create RPC error rests entirely on this
// classifier: only application-level rejections are definitive; every transport
// error / timeout / Internal is ambiguous, so the local row is NEVER deleted for
// them.
func TestCreationCreateErrorIsDefinitive(t *testing.T) {
	definitive := []codes.Code{
		codes.InvalidArgument, codes.FailedPrecondition, codes.NotFound,
		codes.PermissionDenied, codes.Unimplemented, codes.OutOfRange, codes.AlreadyExists,
	}
	for _, c := range definitive {
		if !creationCreateErrorIsDefinitive(status.Error(c, "x")) {
			t.Errorf("%s should be definitive", c)
		}
	}
	ambiguous := []codes.Code{
		codes.Unavailable, codes.DeadlineExceeded, codes.Canceled, codes.Internal,
		codes.Unknown, codes.ResourceExhausted, codes.Aborted, codes.DataLoss,
	}
	for _, c := range ambiguous {
		if creationCreateErrorIsDefinitive(status.Error(c, "x")) {
			t.Errorf("%s must be ambiguous (never delete the local row)", c)
		}
	}
	if creationCreateErrorIsDefinitive(nil) {
		t.Error("nil error is not definitive")
	}
}

// Lost success: Foghorn committed the clip but Commodore never saw the response,
// so no commodore.clips row exists. Convergence CAS-transitions the intent to
// committed and, under the per-artifact advisory lock + deletion-marker check,
// writes the clips row from the captured payload plus Foghorn's fulfilled timing —
// no phantom, no dangling Foghorn artifact.
func TestConvergeCommittedIntent_ClipWritesCatalogRow(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()

	payload, _ := json.Marshal(clipCreationPayload{
		ClipID:       "clip-id-1",
		UserID:       "user-1",
		StreamID:     "stream-1",
		InternalName: "vod+abc",
		PlaybackID:   "pb-1",
		ClipMode:     "absolute",
	})
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE commodore\.artifact_creation_intents`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT deletion_revision FROM commodore\.artifact_catalog_tombstones`).
		WillReturnError(sql.ErrNoRows)
	// Parent-deletion fence: the clip's parent stream is still live.
	mock.ExpectQuery(`SELECT deleted_at IS NULL FROM commodore\.streams WHERE id = .* AND tenant_id = .* FOR UPDATE`).
		WithArgs("stream-1", "t1").
		WillReturnRows(sqlmock.NewRows([]string{"live"}).AddRow(true))
	mock.ExpectExec(`INSERT INTO commodore\.clips`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	s.convergeCommittedIntent(context.Background(),
		creationIntentRow{tenantID: "t1", kind: creationIntentKindClip, artifactHash: "clip1", originClusterID: "c1", payload: payload},
		&sharedpb.GetArtifactCreationStatusResponse{EffectiveStartMs: 100000, EffectiveDurationMs: 30000})

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A lost-success clip whose parent stream is deleted mid-convergence must be COMPENSATED, never abandoned. The
// commit hits the parent-deletion fence and rolls back; convergence then tries to durably delete the orphaned
// Foghorn artifact (compensation) before aborting the intent. When Foghorn is UNREACHABLE (no pool here), the
// compensation cannot proceed, so the intent stays pending/recoverable (attempts bumped) rather than aborting while
// the artifact is still live — it is never left pending FOREVER, and never aborted with a live orphan. The
// successful compensate→abort path is covered by the wired integration test (real Foghorn).
func TestConvergeCommittedIntent_ClipDeletedParentStaysRecoverable(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()

	payload, _ := json.Marshal(clipCreationPayload{
		ClipID:       "clip-id-1",
		UserID:       "user-1",
		StreamID:     "stream-1",
		InternalName: "vod+abc",
		PlaybackID:   "pb-1",
		ClipMode:     "absolute",
	})
	// tx1: the commit attempt hits the parent-deletion fence (parent not live) → rolls back with no catalog row.
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE commodore\.artifact_creation_intents`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(`SELECT deletion_revision FROM commodore\.artifact_catalog_tombstones`).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`SELECT deleted_at IS NULL FROM commodore\.streams WHERE id = .* AND tenant_id = .* FOR UPDATE`).
		WithArgs("stream-1", "t1").
		WillReturnRows(sqlmock.NewRows([]string{"live"}).AddRow(false))
	mock.ExpectRollback()
	// Compensation is attempted (resolveFoghornForArtifact) but there is no Foghorn pool here → it fails, so the
	// intent is only NOTED (attempts bumped, still 'pending') — NOT aborted while the artifact may still be live.
	mock.ExpectExec(`UPDATE commodore\.artifact_creation_intents\s+SET attempts = attempts \+ 1`).
		WillReturnResult(sqlmock.NewResult(0, 1))

	s.convergeCommittedIntent(context.Background(),
		creationIntentRow{tenantID: "t1", kind: creationIntentKindClip, artifactHash: "clip1", originClusterID: "c1", payload: payload},
		&sharedpb.GetArtifactCreationStatusResponse{EffectiveStartMs: 100000, EffectiveDurationMs: 30000})

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A committed VOD already has its business row; convergence only CAS-transitions
// the intent (no INSERT, no delete).
func TestConvergeCommittedIntent_VodMarksOnly(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE commodore\.artifact_creation_intents`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	s.convergeCommittedIntent(context.Background(),
		creationIntentRow{tenantID: "t1", kind: creationIntentKindVOD, artifactHash: "vh1", originClusterID: "c1"},
		&sharedpb.GetArtifactCreationStatusResponse{})

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Definitive rejection: the catalog-only VOD row is removed and the intent aborted,
// atomically and CAS-guarded — the terminal state is a clean absence. No tombstone
// marker is written (an aborted create never had a Foghorn artifact/revision).
func TestConvergeRejectedIntent_RemovesRowNoTombstone(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE commodore\.artifact_creation_intents`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM commodore\.vod_assets`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	s.convergeRejectedIntent(context.Background(),
		creationIntentRow{tenantID: "t1", kind: creationIntentKindVOD, artifactHash: "vh1", originClusterID: "c1"})

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Bounded-missing convergence: an intent Foghorn holds no command for, past the missing
// deadline, is aborted — the catalog-only business row (here a DVR recording) is removed
// and the intent terminalized to 'aborted', atomically and CAS-guarded. This is the
// terminal outcome for a create RPC lost in transit or a stranded RegisterDVR response.
func TestConvergeMissingIntent_AbortsAndRemovesRow(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE commodore\.artifact_creation_intents`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`DELETE FROM commodore\.dvr_recordings`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	s.convergeMissingIntent(context.Background(),
		creationIntentRow{tenantID: "t1", kind: creationIntentKindDVR, artifactHash: "dh1", originClusterID: "c1", pastMissingDeadline: true})

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A bounded-missing abort that loses the lease CAS (another sweeper won) is a silent
// no-op: the guarded transition matches 0 rows, the tx rolls back with no delete.
func TestConvergeMissingIntent_LostCASNoOps(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()

	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE commodore\.artifact_creation_intents`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()

	s.convergeMissingIntent(context.Background(),
		creationIntentRow{tenantID: "t1", kind: creationIntentKindDVR, artifactHash: "dh1", leaseToken: "00000000-0000-0000-0000-000000000042", pastMissingDeadline: true})

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// Two sweepers race the same claimed intent: the loser's CAS-guarded transition
// matches 0 rows, so the terminalizer rolls back and performs NO destructive effect
// (no business-row delete, no marker) and reports errIntentCASMiss.
func TestTerminalizeCreationIntent_CASMissDoesNothing(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()

	mock.ExpectBegin()
	// Lease-CAS matched no row (another worker already terminalized under a fresh lease).
	mock.ExpectExec(`UPDATE commodore\.artifact_creation_intents`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()

	err := s.abortCreationIntent(context.Background(),
		creationIntentRow{tenantID: "t1", kind: creationIntentKindVOD, artifactHash: "vh1", leaseToken: "00000000-0000-0000-0000-000000000042"},
		"00000000-0000-0000-0000-000000000042", "rejected", true)
	if !errors.Is(err, errIntentCASMiss) {
		t.Fatalf("expected errIntentCASMiss, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A committed clip whose hash carries a deletion marker must NOT insert a live row
// behind that marker: the mutator skips the INSERT while the intent still
// terminalizes to committed.
func TestConvergeCommittedIntent_ClipSkipsInsertBehindMarker(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()

	payload, _ := json.Marshal(clipCreationPayload{ClipID: "clip-id-1", ClipMode: "absolute"})
	mock.ExpectBegin()
	mock.ExpectExec(`UPDATE commodore\.artifact_creation_intents`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`SELECT pg_advisory_xact_lock`).
		WillReturnResult(sqlmock.NewResult(0, 0))
	// Deletion marker present → no clips INSERT is issued.
	mock.ExpectQuery(`SELECT deletion_revision FROM commodore\.artifact_catalog_tombstones`).
		WillReturnRows(sqlmock.NewRows([]string{"deletion_revision"}).AddRow(int64(5)))
	mock.ExpectCommit()

	s.convergeCommittedIntent(context.Background(),
		creationIntentRow{tenantID: "t1", kind: creationIntentKindClip, artifactHash: "clip1", payload: payload},
		&sharedpb.GetArtifactCreationStatusResponse{EffectiveStartMs: 100000, EffectiveDurationMs: 30000})

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// upsertCreationIntent is idempotent on the artifact identity and RETURNS the
// PERSISTED request_id: on conflict the ON CONFLICT ... DO UPDATE self-assigns the
// stored request_id and returns it, so a retry keys Foghorn on the request_id the
// intent actually carries (here the pre-existing one), never a freshly minted
// mismatch.
func TestUpsertCreationIntent_ReturnsPersistedRequestID(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()

	const persisted = "00000000-0000-0000-0000-0000000000aa"
	mock.ExpectQuery(`INSERT INTO commodore\.artifact_creation_intents`).
		WithArgs("t1", creationIntentKindVOD, "vh1", "00000000-0000-0000-0000-000000000009", "c1", nil).
		WillReturnRows(sqlmock.NewRows([]string{"request_id"}).AddRow(persisted))

	got, err := upsertCreationIntent(context.Background(), s.db, "t1", creationIntentKindVOD, "vh1", "00000000-0000-0000-0000-000000000009", "c1", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != persisted {
		t.Fatalf("request_id = %q, want persisted %q", got, persisted)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// An empty origin_cluster_id can never converge (the sweep and ack drain cannot resolve a
// Foghorn for it), so upsertCreationIntent rejects it BEFORE any INSERT — a fail-closed guard
// that keeps an unconvergeable pending intent out of the ledger. No DB statement is issued.
func TestUpsertCreationIntent_RejectsEmptyOriginCluster(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()

	// No ExpectQuery: the guard must reject before touching the database.
	_, err := upsertCreationIntent(context.Background(), s.db, "t1", creationIntentKindDVR, "dh1", "00000000-0000-0000-0000-000000000009", "", nil)
	if err == nil {
		t.Fatal("expected an error for empty origin_cluster_id, got nil")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// A duplicated request_id under a DIFFERENT (tenant, kind, hash) identity is NOT caught
// by the ON CONFLICT (tenant, kind, hash) target, so the UNIQUE(request_id) constraint
// fires at the database and the insert errors. upsertCreationIntent surfaces that error
// so the create fails rather than forking a second intent that only one Foghorn ledger
// row matches. (The constraint's presence in the catalog is proved by verify-schema; this
// asserts the code fails closed on the violation.)
func TestUpsertCreationIntent_DuplicateRequestIDErrors(t *testing.T) {
	s, mock, done := newMockServer(t)
	defer done()

	mock.ExpectQuery(`INSERT INTO commodore\.artifact_creation_intents`).
		WithArgs("t2", creationIntentKindClip, "ch9", "00000000-0000-0000-0000-000000000009", "c1", nil).
		WillReturnError(errors.New(`duplicate key value violates unique constraint "artifact_creation_intents_request_id_key"`))

	_, err := upsertCreationIntent(context.Background(), s.db, "t2", creationIntentKindClip, "ch9", "00000000-0000-0000-0000-000000000009", "c1", nil)
	if err == nil {
		t.Fatal("expected the unique-violation error to surface, got nil")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
