package control

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

// TestClassifyFinalCounts pins the segment-tally that drives DVR finalize's
// success/partial/failed decision: 'uploaded' and 'deleted_local' both count as
// durably-stored, while 'lost_local' counts as lost. Miscategorizing either
// bucket flips a complete recording to "failed" or hides real data loss. A nil
// querier short-circuits to ErrConnDone so the sweep doesn't treat a missing DB
// as zero segments.
func TestClassifyFinalCounts(t *testing.T) {
	t.Run("nil db returns ErrConnDone", func(t *testing.T) {
		prev := db
		db = nil
		t.Cleanup(func() { db = prev })

		up, lost, err := classifyFinalCounts(context.Background(), "art")
		if !errors.Is(err, sql.ErrConnDone) {
			t.Fatalf("err = %v, want sql.ErrConnDone", err)
		}
		if up != 0 || lost != 0 {
			t.Fatalf("got (%d,%d), want (0,0)", up, lost)
		}
	})

	t.Run("splits uploaded/deleted_local from lost_local", func(t *testing.T) {
		mock := setupChapterTest(t)
		mock.ExpectQuery(`COUNT\(\*\) FILTER \(WHERE status IN \('uploaded', 'deleted_local'\)\)`).
			WithArgs("art").
			WillReturnRows(sqlmock.NewRows([]string{"uploaded", "lost"}).AddRow(5, 2))

		up, lost, err := classifyFinalCounts(context.Background(), "art")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if up != 5 || lost != 2 {
			t.Fatalf("got (uploaded=%d, lost=%d), want (5, 2)", up, lost)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}

const dvrNodeAuthSelectRe = `SELECT status, COALESCE\(dvr_start_dispatch->>'node_id', ''\)\s+FROM foghorn.artifacts\s+WHERE artifact_hash = \$1 AND artifact_type = 'dvr' AND tenant_id = \$2`

const dvrOwnerTenantRe = `SELECT tenant_id::text\s+FROM foghorn.artifacts\s+WHERE artifact_hash = \$1 AND artifact_type = 'dvr'`

const dvrBackfillLockRe = `SELECT COALESCE\(dvr_start_dispatch->>'node_id', ''\)\s+FROM foghorn.artifacts\s+WHERE artifact_hash = \$1 AND artifact_type = 'dvr' AND tenant_id = \$2\s+FOR UPDATE`

const dvrBackfillReadRe = `SELECT COALESCE\(dvr_start_dispatch->>'node_id', ''\)\s+FROM foghorn.artifacts\s+WHERE artifact_hash = \$1 AND artifact_type = 'dvr' AND tenant_id = \$2`

const dvrOriginSelectRe = `SELECT CASE WHEN COUNT\(\*\) = 1 THEN MAX\(node_id\) ELSE '' END\s+FROM foghorn.artifact_nodes\s+WHERE artifact_hash = \$1 AND role = 'origin' AND is_orphaned = false AND node_id <> ''`

const dvrOwnerBackfillRe = `UPDATE foghorn.artifacts\s+SET dvr_start_dispatch = jsonb_set`

// dvrReportNodeAuthorized binds a node-reported op to the durable dispatch owner, scoped to the owning
// tenant. dvrAuthStrict authorizes ONLY on an owner match regardless of lifecycle status — a missing
// row or a terminal row with a mismatched owner REJECTS. dvrAuthIdempotentStop is identical for any
// EXISTING row (terminal rows still require the owner match); it differs ONLY for a genuinely absent row,
// which it treats as a safe no-op success (see the missing-row subtests). An empty owner on an active
// row is handled by the backfill subtests below, not this table.
func TestDVRReportNodeAuthorized(t *testing.T) {
	t.Run("nil db fails closed", func(t *testing.T) {
		prev := db
		db = nil
		t.Cleanup(func() { db = prev })
		ok, err := dvrReportNodeAuthorized(context.Background(), "dvr-1", "tenant-1", "node-1", dvrAuthStrict)
		if ok || !errors.Is(err, sql.ErrConnDone) {
			t.Fatalf("got (ok=%v, err=%v), want (false, ErrConnDone)", ok, err)
		}
	})

	cases := []struct {
		name         string
		status       string
		dispatchNode string
		reporting    string
		mode         dvrAuthMode
		want         bool
	}{
		{"strict active recording, matching node", "recording", "node-1", "node-1", dvrAuthStrict, true},
		{"strict active recording, mismatched node rejected", "recording", "owner-node", "rogue-node", dvrAuthStrict, false},
		{"strict active starting, mismatched node rejected", "starting", "owner-node", "rogue-node", dvrAuthStrict, false},
		// A terminal row does NOT grant a strict (segment/progress/eviction) op: the owner must still match.
		{"strict terminal row, mismatched node REJECTED", "completed", "owner-node", "rogue-node", dvrAuthStrict, false},
		{"strict terminal row, matching owner allowed", "completed", "owner-node", "owner-node", dvrAuthStrict, true},
		// The stop/finalize path is idempotent ONLY for a genuinely ABSENT row (see the missing-row
		// subtest). An EXISTING terminal row still requires the owner match, so a wrong-node stop against a
		// terminal-with-retained-owner row is rejected (it must not clear the real owner's stop drain).
		{"idempotent-stop terminal row, mismatched node REJECTED", "completed", "owner-node", "rogue-node", dvrAuthIdempotentStop, false},
		{"idempotent-stop terminal row, matching owner allowed", "completed", "owner-node", "owner-node", dvrAuthIdempotentStop, true},
		{"idempotent-stop active recording, mismatched node rejected", "recording", "owner-node", "rogue-node", dvrAuthIdempotentStop, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock := setupChapterTest(t)
			mock.ExpectQuery(dvrNodeAuthSelectRe).
				WithArgs("dvr-1", "tenant-1").
				WillReturnRows(sqlmock.NewRows([]string{"status", "dispatch_node"}).AddRow(tc.status, tc.dispatchNode))
			ok, err := dvrReportNodeAuthorized(context.Background(), "dvr-1", "tenant-1", tc.reporting, tc.mode)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if ok != tc.want {
				t.Fatalf("got ok=%v, want %v", ok, tc.want)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}

	t.Run("strict missing row rejected", func(t *testing.T) {
		mock := setupChapterTest(t)
		mock.ExpectQuery(dvrNodeAuthSelectRe).WithArgs("dvr-1", "tenant-1").WillReturnError(sql.ErrNoRows)
		ok, err := dvrReportNodeAuthorized(context.Background(), "dvr-1", "tenant-1", "any-node", dvrAuthStrict)
		if err != nil || ok {
			t.Fatalf("got (ok=%v, err=%v), want (false, nil)", ok, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("idempotent-stop missing row allowed", func(t *testing.T) {
		mock := setupChapterTest(t)
		mock.ExpectQuery(dvrNodeAuthSelectRe).WithArgs("dvr-1", "tenant-1").WillReturnError(sql.ErrNoRows)
		ok, err := dvrReportNodeAuthorized(context.Background(), "dvr-1", "tenant-1", "any-node", dvrAuthIdempotentStop)
		if err != nil || !ok {
			t.Fatalf("got (ok=%v, err=%v), want (true, nil)", ok, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("query error fails closed", func(t *testing.T) {
		mock := setupChapterTest(t)
		mock.ExpectQuery(dvrNodeAuthSelectRe).WithArgs("dvr-1", "tenant-1").WillReturnError(errors.New("db down"))
		ok, err := dvrReportNodeAuthorized(context.Background(), "dvr-1", "tenant-1", "any-node", dvrAuthStrict)
		if ok || err == nil {
			t.Fatalf("got (ok=%v, err=%v), want (false, err)", ok, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	// An active recording with an empty dispatch owner backfills the owner from the unique
	// recording-origin artifact_nodes row (tenant-scoped, atomic CAS), persists it, and then enforces
	// the match — the reporting node is authorized only when it IS that derived origin.
	t.Run("empty owner backfills unique origin and matches", func(t *testing.T) {
		mock := setupChapterTest(t)
		mock.ExpectQuery(dvrNodeAuthSelectRe).
			WithArgs("dvr-1", "tenant-1").
			WillReturnRows(sqlmock.NewRows([]string{"status", "dispatch_node"}).AddRow("recording", ""))
		mock.ExpectBegin()
		mock.ExpectQuery(dvrBackfillLockRe).
			WithArgs("dvr-1", "tenant-1").
			WillReturnRows(sqlmock.NewRows([]string{"node_id"}).AddRow(""))
		mock.ExpectQuery(dvrOriginSelectRe).
			WithArgs("dvr-1", "tenant-1").
			WillReturnRows(sqlmock.NewRows([]string{"node_id"}).AddRow("origin-node"))
		mock.ExpectExec(dvrOwnerBackfillRe).
			WithArgs("dvr-1", "tenant-1", "origin-node").
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
		ok, err := dvrReportNodeAuthorized(context.Background(), "dvr-1", "tenant-1", "origin-node", dvrAuthStrict)
		if err != nil || !ok {
			t.Fatalf("got (ok=%v, err=%v), want (true, nil)", ok, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	// A concurrent caller won the CAS first (RowsAffected==0): this caller re-reads the persisted owner
	// and authorizes against the WINNER, never its own candidate.
	t.Run("empty owner CAS lost re-reads persisted winner", func(t *testing.T) {
		mock := setupChapterTest(t)
		mock.ExpectQuery(dvrNodeAuthSelectRe).
			WithArgs("dvr-1", "tenant-1").
			WillReturnRows(sqlmock.NewRows([]string{"status", "dispatch_node"}).AddRow("recording", ""))
		mock.ExpectBegin()
		mock.ExpectQuery(dvrBackfillLockRe).
			WithArgs("dvr-1", "tenant-1").
			WillReturnRows(sqlmock.NewRows([]string{"node_id"}).AddRow(""))
		mock.ExpectQuery(dvrOriginSelectRe).
			WithArgs("dvr-1", "tenant-1").
			WillReturnRows(sqlmock.NewRows([]string{"node_id"}).AddRow("origin-node"))
		mock.ExpectExec(dvrOwnerBackfillRe).
			WithArgs("dvr-1", "tenant-1", "origin-node").
			WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(dvrBackfillReadRe).
			WithArgs("dvr-1", "tenant-1").
			WillReturnRows(sqlmock.NewRows([]string{"node_id"}).AddRow("winner-node"))
		mock.ExpectCommit()
		// The reporter is the CAS winner, so it authorizes; our own candidate ("origin-node") would not.
		ok, err := dvrReportNodeAuthorized(context.Background(), "dvr-1", "tenant-1", "winner-node", dvrAuthStrict)
		if err != nil || !ok {
			t.Fatalf("got (ok=%v, err=%v), want (true, nil)", ok, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	// No unique recording-origin row (zero or several collapses COUNT(*)!=1 to '') means the owner
	// cannot be attributed: fail closed rather than accept an arbitrary node.
	t.Run("empty owner with ambiguous origin fails closed", func(t *testing.T) {
		mock := setupChapterTest(t)
		mock.ExpectQuery(dvrNodeAuthSelectRe).
			WithArgs("dvr-1", "tenant-1").
			WillReturnRows(sqlmock.NewRows([]string{"status", "dispatch_node"}).AddRow("recording", ""))
		mock.ExpectBegin()
		mock.ExpectQuery(dvrBackfillLockRe).
			WithArgs("dvr-1", "tenant-1").
			WillReturnRows(sqlmock.NewRows([]string{"node_id"}).AddRow(""))
		mock.ExpectQuery(dvrOriginSelectRe).
			WithArgs("dvr-1", "tenant-1").
			WillReturnRows(sqlmock.NewRows([]string{"node_id"}).AddRow(""))
		mock.ExpectRollback()
		ok, err := dvrReportNodeAuthorized(context.Background(), "dvr-1", "tenant-1", "node-a", dvrAuthStrict)
		if err != nil || ok {
			t.Fatalf("got (ok=%v, err=%v), want (false, nil)", ok, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}

// A DVRStopped from a node OTHER than the dispatched recording owner is rejected BEFORE any state
// change — FinalizeDVR resolves the owning tenant, runs the node-auth read, returns NoOp+error, and
// never claims the row into 'finalizing'.
func TestFinalizeDVR_NonOwningNodeRejectedBeforeClaim(t *testing.T) {
	mock := setupChapterTest(t)
	mock.ExpectQuery(dvrOwnerTenantRe).
		WithArgs("dvr-1").
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id"}).AddRow("tenant-1"))
	mock.ExpectQuery(dvrNodeAuthSelectRe).
		WithArgs("dvr-1", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"status", "dispatch_node"}).AddRow("recording", "owner-node"))
	// No claim UPDATE is expected — the guard rejects first.

	res, err := FinalizeDVR(context.Background(), "dvr-1", FinalizeOptions{ReportingNodeID: "rogue-node"})
	if err == nil {
		t.Fatal("expected rejection error from a non-owning reporting node, got nil")
	}
	if !res.NoOp {
		t.Fatalf("expected NoOp result on rejection, got %+v", res)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// The owning node passes the node-auth guard and proceeds to claim the row. Here the claim finds the
// row already terminal (idempotent NoOp path), proving the guard let the owner through to the mutation.
func TestFinalizeDVR_OwningNodeProceedsToClaim(t *testing.T) {
	mock := setupChapterTest(t)
	mock.ExpectQuery(dvrOwnerTenantRe).
		WithArgs("dvr-1").
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id"}).AddRow("tenant-1"))
	mock.ExpectQuery(dvrNodeAuthSelectRe).
		WithArgs("dvr-1", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"status", "dispatch_node"}).AddRow("recording", "owner-node"))
	// Guard passed → claim attempted. Row already terminal → ErrNoRows → idempotent NoOp path.
	mock.ExpectQuery(`UPDATE foghorn.artifacts\s+SET status = 'finalizing'.*RETURNING status, tenant_id::text`).
		WithArgs("dvr-1", sqlmock.AnyArg()).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`SELECT status FROM foghorn.artifacts WHERE artifact_hash = \$1 AND artifact_type = 'dvr'`).
		WithArgs("dvr-1").
		WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("completed"))
	// Terminal NoOp clears the stop obligation tenant-scoped: resolve the owner tenant, then the clear.
	mock.ExpectQuery(dvrOwnerTenantRe).
		WithArgs("dvr-1").
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id"}).AddRow("tenant-1"))
	mock.ExpectExec(`UPDATE foghorn.artifacts\s+SET dvr_start_dispatch = CASE`).
		WithArgs("dvr-1", "tenant-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT retention_until\s+FROM foghorn.artifacts WHERE artifact_hash = \$1 AND artifact_type = 'dvr'`).
		WithArgs("dvr-1").
		WillReturnRows(sqlmock.NewRows([]string{"retention_until"}).AddRow(nil))

	res, err := FinalizeDVR(context.Background(), "dvr-1", FinalizeOptions{ReportingNodeID: "owner-node"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.NoOp || res.ArtifactStatus != "completed" {
		t.Fatalf("got %+v, want NoOp completed", res)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// The recovery hard-grace path finalizes a DVR 'failed' while RETAINING its stop obligation (the
// recording may still be running). A stop report from a DIFFERENT node must be REJECTED (idempotent-stop
// no longer blanket-allows a terminal row) and must NOT clear the obligation — otherwise it would kill the
// compensating-stop drain aimed at the REAL owner. The owner's own stop is authorized and clears it.
func TestFinalizeDVR_HardGraceRetainedObligation(t *testing.T) {
	t.Run("wrong-node stop rejected, obligation not cleared", func(t *testing.T) {
		mock := setupChapterTest(t)
		mock.ExpectQuery(dvrOwnerTenantRe).
			WithArgs("dvr-hg").
			WillReturnRows(sqlmock.NewRows([]string{"tenant_id"}).AddRow("tenant-1"))
		// Terminal 'failed' row that retains its owner + obligation. Idempotent-stop now requires the owner
		// match for an EXISTING row, so a mismatched node is rejected.
		mock.ExpectQuery(dvrNodeAuthSelectRe).
			WithArgs("dvr-hg", "tenant-1").
			WillReturnRows(sqlmock.NewRows([]string{"status", "dispatch_node"}).AddRow("failed", "owner-node"))
		// No claim, no clearDVRStopObligation — FinalizeDVR rejects before touching the row.

		res, err := FinalizeDVR(context.Background(), "dvr-hg", FinalizeOptions{ReportingNodeID: "rogue-node"})
		if err == nil {
			t.Fatal("expected rejection error from a non-owning stop against a terminal-with-retained-obligation row")
		}
		if !res.NoOp {
			t.Fatalf("expected NoOp result on rejection, got %+v", res)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("owner stop authorized and clears the obligation", func(t *testing.T) {
		mock := setupChapterTest(t)
		mock.ExpectQuery(dvrOwnerTenantRe).
			WithArgs("dvr-hg").
			WillReturnRows(sqlmock.NewRows([]string{"tenant_id"}).AddRow("tenant-1"))
		mock.ExpectQuery(dvrNodeAuthSelectRe).
			WithArgs("dvr-hg", "tenant-1").
			WillReturnRows(sqlmock.NewRows([]string{"status", "dispatch_node"}).AddRow("failed", "owner-node"))
		// Owner matches → proceeds to claim. Row already terminal → ErrNoRows → NoOp terminal path.
		mock.ExpectQuery(`UPDATE foghorn.artifacts\s+SET status = 'finalizing'.*RETURNING status, tenant_id::text`).
			WithArgs("dvr-hg", sqlmock.AnyArg()).
			WillReturnError(sql.ErrNoRows)
		mock.ExpectQuery(`SELECT status FROM foghorn.artifacts WHERE artifact_hash = \$1 AND artifact_type = 'dvr'`).
			WithArgs("dvr-hg").
			WillReturnRows(sqlmock.NewRows([]string{"status"}).AddRow("failed"))
		// clearDVRStopObligation resolves the owner tenant, then retains the owner while clearing the
		// obligation (SET ... = CASE ...), tenant-scoped.
		mock.ExpectQuery(dvrOwnerTenantRe).
			WithArgs("dvr-hg").
			WillReturnRows(sqlmock.NewRows([]string{"tenant_id"}).AddRow("tenant-1"))
		mock.ExpectExec(`UPDATE foghorn.artifacts\s+SET dvr_start_dispatch = CASE`).
			WithArgs("dvr-hg", "tenant-1").
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectQuery(`SELECT retention_until\s+FROM foghorn.artifacts WHERE artifact_hash = \$1 AND artifact_type = 'dvr'`).
			WithArgs("dvr-hg").
			WillReturnRows(sqlmock.NewRows([]string{"retention_until"}).AddRow(nil))

		res, err := FinalizeDVR(context.Background(), "dvr-hg", FinalizeOptions{ReportingNodeID: "owner-node"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !res.NoOp || res.ArtifactStatus != "failed" {
			t.Fatalf("got %+v, want NoOp failed", res)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}

// A DB error resolving the owning tenant must abort FinalizeDVR before the claim: no 'finalizing' claim,
// no terminal transition, no lifecycle event. The result is an empty (non-NoOp) abort carrying the error.
func TestFinalizeDVR_TenantLookupErrorFailsClosed(t *testing.T) {
	mock := setupChapterTest(t)
	mock.ExpectQuery(dvrOwnerTenantRe).
		WithArgs("dvr-err").
		WillReturnError(errors.New("db down"))
	// No claim UPDATE — FinalizeDVR aborts on the tenant-lookup error.

	res, err := FinalizeDVR(context.Background(), "dvr-err", FinalizeOptions{ReportingNodeID: "node-1"})
	if err == nil {
		t.Fatal("expected fail-closed error on tenant-lookup failure")
	}
	if res.NoOp || res.ArtifactStatus != "" {
		t.Fatalf("expected empty aborted result (no mutation), got %+v", res)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
