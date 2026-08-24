package jobs

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"frameworks/api_balancing/internal/control"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"

	"github.com/DATA-DOG/go-sqlmock"
)

// dvrRecoverySeams captures the worker's control-plane calls so the deadline/dispatch logic can be
// exercised without a live gRPC control plane.
type dvrRecoverySeams struct {
	mu               sync.Mutex
	startCalls       int
	stopCalls        int
	registerCalls    int
	finalizeCalls    int
	finalizeReason   string
	finalizeRetained bool
	startErr         error
	stopErr          error
	registerErr      error
}

func (s *dvrRecoverySeams) wire(j *DVRStartingRecoveryJob) {
	j.sendStart = func(string, *ipcpb.DVRStartRequest) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.startCalls++
		return s.startErr
	}
	j.sendStop = func(string, *ipcpb.DVRStopRequest) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.stopCalls++
		return s.stopErr
	}
	j.registerOrigin = func(context.Context, string, string, string) error {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.registerCalls++
		return s.registerErr
	}
	j.finalizeDVR = func(_ context.Context, _ string, opts control.FinalizeOptions) (control.FinalizeResult, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.finalizeCalls++
		s.finalizeReason = opts.ReportedError
		s.finalizeRetained = opts.RetainStopObligation
		return control.FinalizeResult{ArtifactStatus: "failed"}, nil
	}
}

func newDVRRecoveryJob(t *testing.T) (*DVRStartingRecoveryJob, sqlmock.Sqlmock, func()) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	j := NewDVRStartingRecoveryJob(DVRStartingRecoveryConfig{DB: db, Logger: logging.NewLogger()})
	return j, mock, func() { _ = db.Close() }
}

func dvrRecoveryScanRow(status, state string, dispatchedAt int64) *sqlmock.Rows {
	d, _ := json.Marshal(DVRStartDispatch{
		State:             state,
		NodeID:            "node-1",
		NodeBaseURL:       "http://node-1",
		SourceRuntimeName: "live+stream1",
		SourceBaseURL:     "dtsc://node-1/live+stream1",
		SegmentSeconds:    6,
		WindowSeconds:     120,
		MaxEntries:        20,
		StreamID:          "stream-uuid",
		InternalName:      "stream1",
		DispatchedAt:      dispatchedAt,
	})
	return sqlmock.NewRows([]string{"artifact_hash", "tenant_id", "status", "dvr_start_dispatch"}).
		AddRow("dvr-1", "t1", status, string(d))
}

// (a) A 'pending' row past the hard grace with no node progress surfaces 'failed' to the USER but does
// NOT abandon a possibly-running recording: it persists a durable stop obligation BEFORE sending a
// best-effort compensating stop, finalizes with the obligation RETAINED (so the row stays in the drain
// scan), and never re-dispatches start. The deadline is evaluated independently of send success.
func TestDVRStartingRecovery_PastDeadlineSurfacesFailedAndRetainsStopObligation(t *testing.T) {
	j, mock, cleanup := newDVRRecoveryJob(t)
	defer cleanup()
	seams := &dvrRecoverySeams{startErr: nil}
	seams.wire(j)

	pastDispatch := time.Now().Add(-30 * time.Minute).Unix() // > 15m default failAfter
	mock.ExpectQuery(`FROM foghorn\.artifacts a`).WillReturnRows(dvrRecoveryScanRow("starting", DVRDispatchStatePending, pastDispatch))
	// The stop obligation is persisted (durable-before-send) before the compensating stop is dispatched.
	mock.ExpectExec(`UPDATE foghorn\.artifacts`).WillReturnResult(sqlmock.NewResult(0, 1))

	j.reconcile()

	if seams.finalizeCalls != 1 {
		t.Fatalf("expected exactly one finalize, got %d", seams.finalizeCalls)
	}
	if !seams.finalizeRetained {
		t.Fatalf("expected finalize to RETAIN the stop obligation at the hard grace")
	}
	if seams.startCalls != 0 {
		t.Fatalf("expected NO start re-dispatch past the hard deadline, got %d", seams.startCalls)
	}
	if seams.stopCalls != 1 {
		t.Fatalf("expected one best-effort compensating stop, got %d", seams.stopCalls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// (a2) Stop-send-SUCCEEDS-but-ack-lost: a 'stop_pending' row still reconciles to STOP — the recovery
// worker re-sends the compensating stop and never re-dispatches start, because a successful send is not
// an acknowledgement (only the node's DVRStopped is). No finalize is driven by the worker.
func TestDVRStartingRecovery_StopPendingAckLostResendsStopNeverStarts(t *testing.T) {
	j, mock, cleanup := newDVRRecoveryJob(t)
	defer cleanup()
	seams := &dvrRecoverySeams{stopErr: nil} // send succeeds, but ack is (modeled as) lost
	seams.wire(j)

	// Even PAST the hard grace, a stop_pending row drains stop — it is NOT finalized-and-removed.
	pastDispatch := time.Now().Add(-30 * time.Minute).Unix()
	mock.ExpectQuery(`FROM foghorn\.artifacts a`).WillReturnRows(dvrRecoveryScanRow("failed", DVRDispatchStateStopPending, pastDispatch))

	j.reconcile()

	if seams.stopCalls != 1 {
		t.Fatalf("expected one stop re-send on ack-lost, got %d", seams.stopCalls)
	}
	if seams.startCalls != 0 {
		t.Fatalf("expected NO start re-dispatch for a stop_pending row, got %d", seams.startCalls)
	}
	if seams.finalizeCalls != 0 {
		t.Fatalf("expected the worker to NOT finalize a stop_pending row (node DVRStopped does), got %d", seams.finalizeCalls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// (b) A 'starting'/pending row WITHIN the deadline re-registers the origin and re-dispatches the start;
// it is not finalized.
func TestDVRStartingRecovery_WithinDeadlineRedispatches(t *testing.T) {
	j, mock, cleanup := newDVRRecoveryJob(t)
	defer cleanup()
	seams := &dvrRecoverySeams{}
	seams.wire(j)

	recentDispatch := time.Now().Unix()
	mock.ExpectQuery(`FROM foghorn\.artifacts a`).WillReturnRows(dvrRecoveryScanRow("starting", DVRDispatchStatePending, recentDispatch))

	j.reconcile()

	if seams.registerCalls != 1 || seams.startCalls != 1 {
		t.Fatalf("expected origin re-register + start re-dispatch, got register=%d start=%d", seams.registerCalls, seams.startCalls)
	}
	if seams.finalizeCalls != 0 {
		t.Fatalf("expected no finalize within the deadline, got %d", seams.finalizeCalls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// (c) A 'stop_pending' row within the deadline re-sends the compensating stop and is not finalized.
func TestDVRStartingRecovery_StopPendingResendsStop(t *testing.T) {
	j, mock, cleanup := newDVRRecoveryJob(t)
	defer cleanup()
	seams := &dvrRecoverySeams{}
	seams.wire(j)

	recentDispatch := time.Now().Unix()
	mock.ExpectQuery(`FROM foghorn\.artifacts a`).WillReturnRows(dvrRecoveryScanRow("recording", DVRDispatchStateStopPending, recentDispatch))

	j.reconcile()

	if seams.stopCalls != 1 {
		t.Fatalf("expected one stop re-send, got %d", seams.stopCalls)
	}
	if seams.startCalls != 0 || seams.finalizeCalls != 0 {
		t.Fatalf("expected no start/finalize on stop_pending, got start=%d finalize=%d", seams.startCalls, seams.finalizeCalls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// (d) A 'requested' row carrying a descriptor (crash between the insert and the 'starting' transition)
// is resumed: origin re-registered + start re-dispatched, exactly like a 'starting'/pending row.
func TestDVRStartingRecovery_RequestedWithDescriptorResumed(t *testing.T) {
	j, mock, cleanup := newDVRRecoveryJob(t)
	defer cleanup()
	seams := &dvrRecoverySeams{}
	seams.wire(j)

	recentDispatch := time.Now().Unix()
	mock.ExpectQuery(`FROM foghorn\.artifacts a`).WillReturnRows(dvrRecoveryScanRow("requested", DVRDispatchStatePending, recentDispatch))

	j.reconcile()

	if seams.registerCalls != 1 || seams.startCalls != 1 {
		t.Fatalf("expected requested row to be resumed (register+start), got register=%d start=%d", seams.registerCalls, seams.startCalls)
	}
	if seams.finalizeCalls != 0 {
		t.Fatalf("expected no finalize for a fresh requested row, got %d", seams.finalizeCalls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// dvrRecoveryScanRowWithGen is dvrRecoveryScanRow plus a bound ingest generation, for the
// start-redelivery supersession fence.
func dvrRecoveryScanRowWithGen(status, state string, dispatchedAt int64, ingestGen string) *sqlmock.Rows {
	d, _ := json.Marshal(DVRStartDispatch{
		State:             state,
		NodeID:            "node-1",
		NodeBaseURL:       "http://node-1",
		SourceRuntimeName: "live+stream1",
		SourceBaseURL:     "dtsc://node-1/live+stream1",
		IngestGeneration:  ingestGen,
		SegmentSeconds:    6,
		WindowSeconds:     120,
		MaxEntries:        20,
		StreamID:          "stream-uuid",
		InternalName:      "stream1",
		DispatchedAt:      dispatchedAt,
	})
	return sqlmock.NewRows([]string{"artifact_hash", "tenant_id", "status", "dvr_start_dispatch"}).
		AddRow("dvr-1", "t1", status, string(d))
}

// B2 restart-safety: a stranded 'pending' start WITHIN the deadline whose ingest generation
// has already ended (the publisher's PUSH_INPUT_CLOSE landed) must NOT be re-dispatched —
// the durable ended-generation state, not the lost in-memory tombstone, supersedes it. It is
// converted to a durable stop obligation and a compensating stop is sent instead.
func TestDVRStartingRecovery_EndedIngestGenerationSupersedesRedispatch(t *testing.T) {
	j, mock, cleanup := newDVRRecoveryJob(t)
	defer cleanup()
	seams := &dvrRecoverySeams{}
	seams.wire(j)

	recentDispatch := time.Now().Unix()
	mock.ExpectQuery(`FROM foghorn\.artifacts a`).WillReturnRows(dvrRecoveryScanRowWithGen("starting", DVRDispatchStatePending, recentDispatch, "gen-x"))
	// The generation is looked up and found ENDED.
	mock.ExpectQuery(`SELECT \(ended_at IS NOT NULL\)::boolean AS ended FROM foghorn\.ingest_sessions`).
		WithArgs("gen-x", "t1").
		WillReturnRows(sqlmock.NewRows([]string{"ended"}).AddRow(true))
	// The stop obligation is persisted durable-before-send.
	mock.ExpectExec(`UPDATE foghorn\.artifacts`).WillReturnResult(sqlmock.NewResult(0, 1))

	j.reconcile()

	if seams.startCalls != 0 {
		t.Fatalf("an ended ingest generation must NOT re-dispatch start, got %d", seams.startCalls)
	}
	if seams.registerCalls != 0 {
		t.Fatalf("a superseded start must not re-register origin, got %d", seams.registerCalls)
	}
	if seams.stopCalls != 1 {
		t.Fatalf("expected one compensating stop for the ended generation, got %d", seams.stopCalls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}

// A stranded 'pending' start whose ingest generation is still ACTIVE re-dispatches normally:
// the fence only suppresses a provably-superseded start.
func TestDVRStartingRecovery_ActiveIngestGenerationStillRedispatches(t *testing.T) {
	j, mock, cleanup := newDVRRecoveryJob(t)
	defer cleanup()
	seams := &dvrRecoverySeams{}
	seams.wire(j)

	recentDispatch := time.Now().Unix()
	mock.ExpectQuery(`FROM foghorn\.artifacts a`).WillReturnRows(dvrRecoveryScanRowWithGen("starting", DVRDispatchStatePending, recentDispatch, "gen-live"))
	mock.ExpectQuery(`SELECT \(ended_at IS NOT NULL\)::boolean AS ended FROM foghorn\.ingest_sessions`).
		WithArgs("gen-live", "t1").
		WillReturnRows(sqlmock.NewRows([]string{"ended"}).AddRow(false))

	j.reconcile()

	if seams.registerCalls != 1 || seams.startCalls != 1 {
		t.Fatalf("an active generation must re-dispatch, got register=%d start=%d", seams.registerCalls, seams.startCalls)
	}
	if seams.stopCalls != 0 {
		t.Fatalf("an active generation must not send a stop, got %d", seams.stopCalls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet SQL expectations: %v", err)
	}
}
