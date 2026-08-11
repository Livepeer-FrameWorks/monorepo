package grpc

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/sirupsen/logrus"
)

// finalizeStreamDeletion is the two-phase deletion FINALIZE (run only after Foghorn acks the tombstone): it
// HARD-deletes the soft-deleted stream row AND marks the outbox row completed. This is what the DeleteStream
// fast-path and the outbox worker's MarkCompleted both call, so a delivery outage converges the deletion.
func TestFinalizeStreamDeletion(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close() //nolint:errcheck

	// Ownership gate FIRST: the token-fenced outbox settlement must affect a row before any hard-delete.
	// Then attribution read, hard-delete, and terminal-event enqueue — ONE transaction.
	mock.ExpectBegin()
	// The token-fenced settlement RETURNs the obligation's tenant_id — the authoritative attribution that fences
	// the attribution read and the hard-delete below.
	mock.ExpectQuery("SET status = 'completed'").
		WithArgs("stream-1", "", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id"}).AddRow("tenant-1"))
	mock.ExpectQuery("SELECT COALESCE.user_id.*FROM commodore.streams WHERE id = .* AND tenant_id =").
		WithArgs("stream-1", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"user_id"}).AddRow("user-1"))
	mock.ExpectExec("DELETE FROM commodore.streams WHERE id = .* AND tenant_id = .* AND deleted_at IS NOT NULL").
		WithArgs("stream-1", "tenant-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	// The hard-delete affected a row (1) + tenant present → the TERMINAL stream_deleted event is enqueued in-tx.
	mock.ExpectQuery("INSERT INTO commodore.service_event_outbox").
		WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow("evt-1"))
	mock.ExpectCommit()

	server := &CommodoreServer{db: db, logger: logrus.New()}
	if fErr := server.finalizeStreamDeletion(context.Background(), "stream-1", "tenant-1", ""); fErr != nil {
		t.Fatalf("finalize: %v", fErr)
	}
	if mErr := mock.ExpectationsWereMet(); mErr != nil {
		t.Fatalf("expectations: %v", mErr)
	}
}

// A stale worker whose lease lapsed (its token no longer matches the re-claimed row) must NOT finalize: the
// token-fenced outbox settlement affects ZERO rows, so finalize performs NO hard-delete and emits NO terminal event
// — it rolls back cleanly. This is the ownership gate that keeps a lost-lease worker from deleting the stream out
// from under the current owner.
func TestFinalizeStreamDeletion_LostTokenIsNoOp(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close() //nolint:errcheck

	mock.ExpectBegin()
	// The settlement matches no row (stale token) → RETURNING yields no rows (sql.ErrNoRows).
	mock.ExpectQuery("SET status = 'completed'").
		WithArgs("stream-1", "stale-token", "tenant-1").
		WillReturnError(sql.ErrNoRows)
	// No attribution read, NO DELETE, NO event enqueue may follow — the tx rolls back.
	mock.ExpectRollback()

	server := &CommodoreServer{db: db, logger: logrus.New()}
	if fErr := server.finalizeStreamDeletion(context.Background(), "stream-1", "tenant-1", "stale-token"); fErr != nil {
		t.Fatalf("lost-token finalize must be a clean no-op, got: %v", fErr)
	}
	if mErr := mock.ExpectationsWereMet(); mErr != nil {
		t.Fatalf("expectations: %v", mErr)
	}
}

// The opaque claim identity carries tenant + stream so settlement is tenant-scoped straight from the claim, with no
// mutable side-state. It round-trips exactly; a malformed id (no separator) decodes to an EMPTY tenant, which the
// settle guards reject rather than writing tenantlessly.
func TestStreamCleanupClaimID_RoundTrip(t *testing.T) {
	tenant, stream := "tenant-1", "stream-1"
	if gt, gs := parseStreamCleanupClaimID(streamCleanupClaimID(tenant, stream)); gt != tenant || gs != stream {
		t.Fatalf("round-trip = (%q,%q), want (%q,%q)", gt, gs, tenant, stream)
	}
	if gt, gs := parseStreamCleanupClaimID("bare-stream"); gt != "" || gs != "bare-stream" {
		t.Fatalf("malformed decode = (%q,%q), want (\"\",\"bare-stream\")", gt, gs)
	}
}

// A settlement whose claim carries NO tenant must FAIL CLOSED — return an error before any DB write, leaving the row
// for lease-expiry/retry — never fall back to a tenantless write, across all three settle paths. sqlmock registers
// NO expectations, so any DB call would fail the test.
func TestStreamCleanupSettlement_MissingTenantFailsClosed(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close() //nolint:errcheck
	server := &CommodoreServer{db: db, logger: logrus.New()}

	if err := server.finalizeStreamDeletion(context.Background(), "stream-1", "", ""); err == nil {
		t.Fatal("finalize with empty tenant must error, not settle")
	}
	if err := server.recordStreamCleanupOutboxFailure(context.Background(), "stream-1", "", 1, errors.New("boom"), "tok"); err == nil {
		t.Fatal("record-failure with empty tenant must error, not settle")
	}
	if err := server.markStreamThumbnailCleanupAcked(context.Background(), "stream-1", ""); err == nil {
		t.Fatal("mark-acked with empty tenant must error, not settle")
	}
	if mErr := mock.ExpectationsWereMet(); mErr != nil {
		t.Fatalf("no DB call may be issued when tenant is missing: %v", mErr)
	}
}

// The serial batch's worst-case dispatch time must fit within the claim lease, or a peer reclaims the untouched
// tail while this worker is still dispatching. Pins BatchSize * ItemTimeout < Lease with headroom for settlement.
func TestStreamCleanupOutboxLeaseBudget(t *testing.T) {
	// The worker processes the batch SERIALLY. Per row the wall-clock worst case is: the RPC work bounded by
	// ItemTimeout (thumbnail fan-out + child cascade share one deadline), PLUS the phase-ack marker write on its own
	// fresh SettleTimeout-bounded context, PLUS the generic worker's settlement (also SettleTimeout). The last row must
	// finish within its (claim-time) lease.
	perItem := streamCleanupOutboxItemTimeout + 2*streamCleanupOutboxSettleTimeout
	worstCase := time.Duration(streamCleanupOutboxBatchSize) * perItem
	if worstCase >= streamCleanupOutboxLease {
		t.Fatalf("serial batch worst-case %s (batch %d * (dispatch %s + settle %s)) >= lease %s: a peer can reclaim the tail",
			worstCase, streamCleanupOutboxBatchSize, streamCleanupOutboxItemTimeout, streamCleanupOutboxSettleTimeout, streamCleanupOutboxLease)
	}
	if margin := streamCleanupOutboxLease - worstCase; margin < streamCleanupOutboxLease/4 {
		t.Fatalf("insufficient lease headroom: worst-case %s leaves only %s of the %s lease", worstCase, margin, streamCleanupOutboxLease)
	}
}

// deleteStreamChildMedia enumerates the stream's clips + DVR recordings; with none present it is a clean no-op
// success (the durable obligation acks). The per-child delete + fail-retry gating is covered by
// TestDeleteStreamChildMedia_CascadeCoordination via the childArtifactDeleteFn fake.
func TestDeleteStreamChildMedia_NoChildren(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close() //nolint:errcheck

	mock.ExpectQuery("FROM commodore.clips WHERE stream_id").
		WithArgs("s1", "t1").
		WillReturnRows(sqlmock.NewRows([]string{"clip_hash", "origin_cluster_id"}))
	mock.ExpectQuery("FROM commodore.dvr_recordings WHERE stream_id").
		WithArgs("s1", "t1").
		WillReturnRows(sqlmock.NewRows([]string{"dvr_hash", "origin_cluster_id"}))

	server := &CommodoreServer{db: db, logger: logrus.New()}
	if cErr := server.deleteStreamChildMedia(context.Background(), "s1", "t1"); cErr != nil {
		t.Fatalf("no children must be a clean no-op: %v", cErr)
	}
	if mErr := mock.ExpectationsWereMet(); mErr != nil {
		t.Fatalf("expectations: %v", mErr)
	}
}

// deleteStreamChildMedia enumerates clips + DVR recordings and deletes each through the origin-cluster Foghorn,
// returning on the FIRST failed child so the durable obligation retries (never finalizing the stream while a child
// is still live). This drives the cascade with a deterministic fake behind the childArtifactDeleteFn seam — no live
// Foghorn — to pin three coordination properties: (1) a mid-cascade child failure aborts the whole obligation with
// an error (so it stays pending); (2) a retry after the child recovers is a clean no-op success; (3) an
// idempotent-benign child (already gone) does NOT fail the obligation.
func TestDeleteStreamChildMedia_CascadeCoordination(t *testing.T) {
	// enumRows sets up the two enumeration queries (clips then dvr) that deleteStreamChildMedia always issues.
	enumRows := func(mock sqlmock.Sqlmock, clipHashes, dvrHashes []string) {
		clips := sqlmock.NewRows([]string{"clip_hash", "origin_cluster_id"})
		for _, h := range clipHashes {
			clips.AddRow(h, "cluster-a")
		}
		mock.ExpectQuery("FROM commodore.clips WHERE stream_id").WithArgs("s1", "t1").WillReturnRows(clips)
		dvrs := sqlmock.NewRows([]string{"dvr_hash", "origin_cluster_id"})
		for _, h := range dvrHashes {
			dvrs.AddRow(h, "cluster-a")
		}
		mock.ExpectQuery("FROM commodore.dvr_recordings WHERE stream_id").WithArgs("s1", "t1").WillReturnRows(dvrs)
	}

	t.Run("mid_cascade_child_failure_aborts_obligation", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		defer db.Close() //nolint:errcheck
		// Only the clips enumeration runs: the first clip fails, so the cascade returns before the dvr enumeration.
		clips := sqlmock.NewRows([]string{"clip_hash", "origin_cluster_id"}).AddRow("clip-1", "cluster-a").AddRow("clip-2", "cluster-a")
		mock.ExpectQuery("FROM commodore.clips WHERE stream_id").WithArgs("s1", "t1").WillReturnRows(clips)

		var deleted []string
		server := &CommodoreServer{db: db, logger: logrus.New()}
		server.childArtifactDeleteFn = func(_ context.Context, kind, hash, _, _ string) error {
			deleted = append(deleted, kind+":"+hash)
			return errors.New("foghorn unreachable")
		}
		if cErr := server.deleteStreamChildMedia(context.Background(), "s1", "t1"); cErr == nil {
			t.Fatal("a failed child must abort the obligation with an error so it retries")
		}
		// The cascade must stop at the FIRST failure, not plough through the rest of the children.
		if len(deleted) != 1 || deleted[0] != "clip:clip-1" {
			t.Fatalf("cascade should abort after first failed child, deleted=%v", deleted)
		}
		if mErr := mock.ExpectationsWereMet(); mErr != nil {
			t.Fatalf("expectations: %v", mErr)
		}
	})

	t.Run("recovered_retry_completes_all_children", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		defer db.Close() //nolint:errcheck
		enumRows(mock, []string{"clip-1", "clip-2"}, []string{"dvr-1"})

		var deleted []string
		server := &CommodoreServer{db: db, logger: logrus.New()}
		server.childArtifactDeleteFn = func(_ context.Context, kind, hash, _, _ string) error {
			deleted = append(deleted, kind+":"+hash)
			return nil // Foghorn recovered: every child acks.
		}
		if cErr := server.deleteStreamChildMedia(context.Background(), "s1", "t1"); cErr != nil {
			t.Fatalf("recovered cascade must succeed: %v", cErr)
		}
		want := []string{"clip:clip-1", "clip:clip-2", "dvr:dvr-1"}
		if len(deleted) != len(want) {
			t.Fatalf("all children must be deleted, got %v want %v", deleted, want)
		}
		for i := range want {
			if deleted[i] != want[i] {
				t.Fatalf("child order %v want %v", deleted, want)
			}
		}
		if mErr := mock.ExpectationsWereMet(); mErr != nil {
			t.Fatalf("expectations: %v", mErr)
		}
	})

	t.Run("idempotent_benign_child_does_not_fail", func(t *testing.T) {
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		defer db.Close() //nolint:errcheck
		enumRows(mock, []string{"clip-gone"}, nil)

		server := &CommodoreServer{db: db, logger: logrus.New()}
		server.childArtifactDeleteFn = func(_ context.Context, _, _, _, _ string) error {
			// A child already reconciled away on a prior partial run: the real path maps NotFound / "already
			// deleted" to nil via childDeleteAcked, so a benign child returns nil here too.
			return nil
		}
		if cErr := server.deleteStreamChildMedia(context.Background(), "s1", "t1"); cErr != nil {
			t.Fatalf("an already-gone child must not fail the obligation: %v", cErr)
		}
		if mErr := mock.ExpectationsWereMet(); mErr != nil {
			t.Fatalf("expectations: %v", mErr)
		}
	})
}

// claimStreamCleanupOutboxBatch selects due pending rows oldest-first (SKIP LOCKED) and leases each one forward in
// the SAME transaction so a peer replica's `next_attempt_at <= NOW()` predicate skips it. Pins the at-least-once,
// no-double-dispatch ordering contract.
func TestClaimStreamCleanupOutboxBatch(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close() //nolint:errcheck

	mock.ExpectBegin()
	mock.ExpectQuery("FROM commodore.stream_cleanup_outbox").
		WillReturnRows(sqlmock.NewRows([]string{"stream_id", "tenant_id", "attempts", "thumbnail_cleanup_acked"}).
			AddRow("stream-1", "tenant-1", 2, false))
	// The lease RETURNS a freshly-minted lease_token (a Query, not an Exec) and is TENANT-FENCED ($3 = tenant-1).
	mock.ExpectQuery("SET next_attempt_at = NOW.*lease_token = gen_random_uuid").
		WithArgs(sqlmock.AnyArg(), "stream-1", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"lease_token"}).AddRow("tok-1"))
	mock.ExpectCommit()

	server := &CommodoreServer{db: db, logger: logrus.New()}
	rows, err := server.claimStreamCleanupOutboxBatch(context.Background())
	if err != nil {
		t.Fatalf("claimStreamCleanupOutboxBatch: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	if rows[0].streamID != "stream-1" || rows[0].tenantID != "tenant-1" || rows[0].attempts != 2 {
		t.Fatalf("unexpected row: %+v", rows[0])
	}
	if mErr := mock.ExpectationsWereMet(); mErr != nil {
		t.Fatalf("expectations: %v", mErr)
	}
}

// An empty due-set must issue NO lease UPDATE and commit cleanly.
func TestClaimStreamCleanupOutboxBatchEmpty(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close() //nolint:errcheck

	mock.ExpectBegin()
	mock.ExpectQuery("FROM commodore.stream_cleanup_outbox").
		WillReturnRows(sqlmock.NewRows([]string{"stream_id", "tenant_id", "attempts", "thumbnail_cleanup_acked"}))
	mock.ExpectCommit()

	server := &CommodoreServer{db: db, logger: logrus.New()}
	rows, err := server.claimStreamCleanupOutboxBatch(context.Background())
	if err != nil {
		t.Fatalf("claimStreamCleanupOutboxBatch: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("rows = %d, want 0", len(rows))
	}
	if mErr := mock.ExpectationsWereMet(); mErr != nil {
		t.Fatalf("expectations: %v", mErr)
	}
}

// enqueueStreamCleanupOutbox is idempotent: ON CONFLICT DO NOTHING yields no RETURNING row (sql.ErrNoRows), which
// must be treated as a successful no-op (a re-delete does not error the deletion tx).
func TestEnqueueStreamCleanupOutboxIdempotent(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close() //nolint:errcheck

	// Conflict path: the INSERT returns no rows.
	mock.ExpectQuery("INSERT INTO commodore.stream_cleanup_outbox").
		WithArgs("stream-1", "tenant-1").
		WillReturnRows(sqlmock.NewRows([]string{"stream_id"})) // empty ⇒ ErrNoRows on Scan

	server := &CommodoreServer{db: db, logger: logrus.New()}
	if err := server.enqueueStreamCleanupOutbox(context.Background(), db, "stream-1", "tenant-1"); err != nil {
		t.Fatalf("conflict/no-row insert must be a no-op, got: %v", err)
	}
	if mErr := mock.ExpectationsWereMet(); mErr != nil {
		t.Fatalf("expectations: %v", mErr)
	}
}

// recordStreamCleanupOutboxFailure bumps attempts + reschedules with backoff, guarded on status='pending'.
func TestRecordStreamCleanupOutboxFailure(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close() //nolint:errcheck

	mock.ExpectExec("SET attempts =").
		WithArgs(3, sqlmock.AnyArg(), "foghorn unreachable", "stream-1", "tok-1", "tenant-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	server := &CommodoreServer{db: db, logger: logrus.New()}
	server.recordStreamCleanupOutboxFailure(context.Background(), "stream-1", "tenant-1", 2, errors.New("foghorn unreachable"), "tok-1")
	if mErr := mock.ExpectationsWereMet(); mErr != nil {
		t.Fatalf("expectations: %v", mErr)
	}
}
