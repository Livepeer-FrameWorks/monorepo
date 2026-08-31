package control

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"frameworks/api_balancing/internal/state"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// chapterHash32 is a 32-char artifact hash: the chapter resolvers reject any
// resolved hash whose length isn't exactly 32 (the artifact-hash addressing
// invariant), so the fixtures must satisfy it.
const chapterHash32 = "0123456789abcdef0123456789abcdef"
const chapterFinalizeAttempt int32 = 3

func TestChapterFinalizeIdentityFromJobID(t *testing.T) {
	chapterID, attempt, ok := chapterFinalizeIdentityFromJobID("chapter-finalize-v2-12-chapter-with-dashes")
	if !ok || chapterID != "chapter-with-dashes" || attempt != 12 {
		t.Fatalf("identity = chapter:%q attempt:%d ok:%v", chapterID, attempt, ok)
	}
	chapterID, attempt, ok = chapterFinalizeIdentityFromJobID("chapter-finalize-legacy-chapter")
	if !ok || chapterID != "legacy-chapter" || attempt != 0 {
		t.Fatalf("legacy identity = chapter:%q attempt:%d ok:%v", chapterID, attempt, ok)
	}
	chapterID, attempt, ok = chapterFinalizeIdentityFromJobID("chapter-finalize-12-numeric-legacy-id")
	if !ok || chapterID != "12-numeric-legacy-id" || attempt != 0 {
		t.Fatalf("numeric legacy identity = chapter:%q attempt:%d ok:%v", chapterID, attempt, ok)
	}
	chapterID, attempt, ok = chapterFinalizeIdentityFromJobID("chapter-finalize-v2-bad-attempt")
	if !ok || chapterID != "v2-bad-attempt" || attempt != 0 {
		t.Fatalf("malformed v2 identity = chapter:%q attempt:%d ok:%v", chapterID, attempt, ok)
	}
}

// chapterArtifactContentCols matches resolveChapterArtifactContent's SELECT:
// origin_type, origin_id, tenant_id, internal_name, requires_auth(bool).
func chapterArtifactContentRow(originType, originID, tenantID string) *sqlmock.Rows {
	return sqlmock.NewRows([]string{"origin_type", "origin_id", "tenant_id", "internal_name", "requires_auth"}).
		AddRow(originType, originID, tenantID, "", true)
}

// chapterArtifactPlaybackRow matches resolveChapterArtifactPlaybackResp's SELECT:
// origin_type, origin_id, tenant_id, internal_name.
func chapterArtifactPlaybackRow(originType, originID, tenantID string) *sqlmock.Rows {
	return sqlmock.NewRows([]string{"origin_type", "origin_id", "tenant_id", "internal_name"}).
		AddRow(originType, originID, tenantID, "")
}

// playableChapterRow builds a foghorn.dvr_chapters GetChapter row in the given
// state with the given parent DVR artifact_hash.
func playableChapterRow(chapterID, parentDVRHash, chapterState string) *sqlmock.Rows {
	return sqlmock.NewRows(chapterRowCols()).AddRow(
		chapterID, parentDVRHash, "window_sized_chapters", nil,
		int64(1000), int64(2000), false,
		chapterState, chapterHash32, "pb-id", int64(0),
		nil, nil, nil,
		nil, nil,
		int64(5), false,
		nil, nil,
		time.Unix(1700000000, 0),
	)
}

// INVARIANT: a "completed" chapter-finalize result runs ONE atomic transaction — lock the
// chapter + its playback artifact, update the artifact to ready/mkv, upsert vod_metadata,
// register the origin artifact (+ node-copy event), transition the chapter finalizing →
// finalized (exactly one row), and enqueue the completion lifecycle — all committed together.
// The job is never left with a ready artifact but an un-transitioned chapter (or vice versa).
func TestHandleChapterFinalizeResult_CompletedAdvancesToFinalized(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	startFakeCommodoreServer(t, &fakeCommodoreInternal{})

	// Make node-1 an active balancer node so the post-commit warm-cache assertion
	// (FindNodesByArtifactHash) can observe the artifact. A live producing node has already sent its
	// poller inventory, so simulate a versioned snapshot to lift the artifact-readiness cordon (without
	// it, the node is excluded from artifact routing until its first versioned report).
	state.DefaultManager().SetNodeInfo("node-1", "https://n1.example.com", true, nil, nil, "ams", "", nil)
	state.DefaultManager().TouchNode("node-1", true)
	state.DefaultManager().SetNodeArtifacts("node-1", nil, state.ArtifactReportOrder{Fence: 1, Seq: 1})

	const chapterID = "chap-fin-1"
	const tenant = "5eed517e-ba5e-da7a-517e-ba5eda7a0001"
	outputs := map[string]string{
		"artifact_hash":         chapterHash32,
		"chapter_segment_count": "7",
		"chapter_has_gaps":      "false",
		"duration_ms":           "12000",
		"resolution":            "1280x720",
	}
	result := &ipcpb.ProcessingJobResult{
		JobId:           chapterFinalizeJobIDPrefix + chapterID,
		Outputs:         outputs,
		OutputPath:      "/data/vod/" + chapterHash32 + ".mkv",
		OutputSizeBytes: 4096,
	}

	mock.ExpectBegin()
	// Lock the chapter + allocated artifact + parent; confirm chapter='finalizing',
	// artifact='finalizing', parent not deleted.
	mock.ExpectQuery(`SELECT c.state, COALESCE\(c.playback_artifact_hash`).
		WithArgs(chapterID).
		WillReturnRows(sqlmock.NewRows([]string{"state", "playback_artifact_hash", "tenant_id", "status", "parent_status", "finalize_node_id", "finalize_attempts"}).
			AddRow(ChapterStateFinalizing, chapterHash32, tenant, "finalizing", "ready", "node-1", chapterFinalizeAttempt))
	// Artifacts row → ready/mkv/pending/local, guarded on status='finalizing' (exactly one row).
	mock.ExpectExec(`UPDATE foghorn.artifacts\s+SET status = 'ready'`).
		WithArgs(int64(4096), false, "[]", int64(0), chapterHash32).
		WillReturnResult(sqlmock.NewResult(0, 1))
	// vod_metadata upsert (outputs non-empty).
	mock.ExpectExec(`INSERT INTO foghorn.vod_metadata`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	// RegisterOriginArtifactTx: prior-read (none) → origin upsert (inserted, complete) → GAINED
	// node-copy (tenant → version → stamp → outbox).
	expectPlacementParentLock(mock)
	mock.ExpectQuery(`SELECT role, is_complete, is_orphaned FROM foghorn.artifact_nodes`).
		WithArgs(chapterHash32, "node-1").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(`INSERT INTO foghorn.artifact_nodes`).
		WillReturnRows(sqlmock.NewRows([]string{"is_complete"}).AddRow(true))
	mock.ExpectQuery(`SELECT tenant_id::text FROM foghorn.artifacts WHERE artifact_hash`).
		WithArgs(chapterHash32).
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id"}).AddRow(tenant))
	mock.ExpectQuery(`INSERT INTO foghorn.artifact_node_copy_version_counter`).
		WithArgs(chapterHash32, "node-1").
		WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(int64(1)))
	mock.ExpectExec(`UPDATE foghorn.artifact_nodes SET last_emitted_version`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO foghorn.artifact_event_outbox`).
		WillReturnResult(sqlmock.NewResult(0, 1)) // node-copy event
	// MarkChapterFinalizedTx: finalizing → finalized, exactly one row.
	mock.ExpectExec(`UPDATE foghorn.dvr_chapters\s+SET state\s+= 'finalized'`).
		WithArgs(int32(7), false, nil, nil, chapterID, chapterFinalizeAttempt).
		WillReturnResult(sqlmock.NewResult(0, 1))
	// Completion lifecycle enqueue (same tx).
	mock.ExpectExec(`INSERT INTO foghorn.artifact_event_outbox`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	handleChapterFinalizeResult(context.Background(), chapterID, "completed", chapterFinalizeAttempt, result, "node-1", logging.NewLogger())

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sqlmock expectations: %v", err)
	}
	// Post-commit: the in-memory state manager carries the warm copy on the producing node.
	if nodes := state.DefaultManager().FindNodesByArtifactHash(chapterHash32); len(nodes) != 1 || nodes[0].NodeID != "node-1" {
		t.Fatalf("expected the chapter artifact registered on node-1 in state, got %+v", nodes)
	}
}

// INVARIANT: a duplicate/late completion (the chapter is no longer 'finalizing') is an
// ignored no-op — the tx rolls back with NO artifact/chapter writes and does NOT bounce the
// chapter to closed.
func TestHandleChapterFinalizeResult_DuplicateCompletionIsNoOp(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	startFakeCommodoreServer(t, &fakeCommodoreInternal{})

	const chapterID = "chap-dup-1"
	result := &ipcpb.ProcessingJobResult{
		JobId:           chapterFinalizeJobIDPrefix + chapterID,
		Outputs:         map[string]string{"artifact_hash": chapterHash32},
		OutputPath:      "/data/vod/" + chapterHash32 + ".mkv",
		OutputSizeBytes: 4096,
	}

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT c.state, COALESCE\(c.playback_artifact_hash`).
		WithArgs(chapterID).
		WillReturnRows(sqlmock.NewRows([]string{"state", "playback_artifact_hash", "tenant_id", "status", "parent_status", "finalize_node_id", "finalize_attempts"}).
			AddRow(ChapterStateFinalized, chapterHash32, "t1", "ready", "ready", "node-1", chapterFinalizeAttempt)) // already finalized
	mock.ExpectRollback()

	handleChapterFinalizeResult(context.Background(), chapterID, "completed", chapterFinalizeAttempt, result, "node-1", logging.NewLogger())

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sqlmock expectations: %v", err)
	}
}

func TestHandleChapterFinalizeResult_RejectsPriorAttemptOnSameNode(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	startFakeCommodoreServer(t, &fakeCommodoreInternal{})
	const chapterID = "chap-attempt-aba"
	result := &ipcpb.ProcessingJobResult{
		JobId: chapterFinalizeJobIDPrefix + "3-" + chapterID, Outputs: map[string]string{"artifact_hash": chapterHash32},
		OutputPath: "/data/vod/" + chapterHash32 + ".mkv", OutputSizeBytes: 4096,
	}
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT c.state, COALESCE\(c.playback_artifact_hash`).WithArgs(chapterID).
		WillReturnRows(sqlmock.NewRows([]string{"state", "playback_artifact_hash", "tenant_id", "status", "parent_status", "finalize_node_id", "finalize_attempts"}).
			AddRow(ChapterStateFinalizing, chapterHash32, "t1", "finalizing", "ready", "node-1", int32(4)))
	mock.ExpectRollback()

	handleChapterFinalizeResult(context.Background(), chapterID, "completed", 3, result, "node-1", logging.NewLogger())
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("stale same-node attempt performed a write: %v", err)
	}
}

// INVARIANT: a malformed completion — no playback hash OR no output path — must NOT silently
// leave the chapter 'finalizing' (strand) nor finalize without an origin copy. It bounces the
// chapter finalizing → closed so the queue re-dispatches (bounded by finalize_attempts).
func TestHandleChapterFinalizeResult_MalformedCompletionBouncesToClosed(t *testing.T) {
	t.Run("no playback hash", func(t *testing.T) {
		mock, _, _ := setupArtifactTestDeps(t)
		startFakeCommodoreServer(t, &fakeCommodoreInternal{})
		mock.ExpectExec(`UPDATE foghorn.dvr_chapters\s+SET state\s+= 'closed'`).
			WithArgs(sqlmock.AnyArg(), "chap-nohash", chapterFinalizeAttempt, "node-1").
			WillReturnResult(sqlmock.NewResult(0, 1))
		handleChapterFinalizeResult(context.Background(), "chap-nohash", "completed", chapterFinalizeAttempt,
			&ipcpb.ProcessingJobResult{JobId: chapterFinalizeJobIDPrefix + "chap-nohash", Outputs: map[string]string{}}, "node-1", logging.NewLogger())
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet: %v", err)
		}
	})
	t.Run("hash but no output path", func(t *testing.T) {
		mock, _, _ := setupArtifactTestDeps(t)
		startFakeCommodoreServer(t, &fakeCommodoreInternal{})
		mock.ExpectExec(`UPDATE foghorn.dvr_chapters\s+SET state\s+= 'closed'`).
			WithArgs(sqlmock.AnyArg(), "chap-noout", chapterFinalizeAttempt, "node-1").
			WillReturnResult(sqlmock.NewResult(0, 1))
		handleChapterFinalizeResult(context.Background(), "chap-noout", "completed", chapterFinalizeAttempt,
			&ipcpb.ProcessingJobResult{JobId: chapterFinalizeJobIDPrefix + "chap-noout", Outputs: map[string]string{"artifact_hash": chapterHash32}}, "node-1", logging.NewLogger())
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet: %v", err)
		}
	})
}

// INVARIANT: a chapter-finalize result from a node OTHER than the one the finalize was dispatched to is
// rejected at the authorization SELECT — no lock, no finalize, no origin registration. A node that merely
// learned the chapter id cannot finalize the artifact and become its recorded origin.
func TestHandleChapterFinalizeResult_RejectsForeignNode(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	startFakeCommodoreServer(t, &fakeCommodoreInternal{})
	// The authorization is read UNDER the finalize tx's FOR UPDATE lock: the locked row shows the attempt is
	// assigned to "assigned-node", so a completion from "attacker-node" rolls back with NO writes (no artifact
	// readiness, no origin registration, no bounce).
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT c.state, COALESCE\(c.playback_artifact_hash`).
		WithArgs("chap-foreign").
		WillReturnRows(sqlmock.NewRows([]string{"state", "playback_artifact_hash", "tenant_id", "status", "parent_status", "finalize_node_id", "finalize_attempts"}).
			AddRow(ChapterStateFinalizing, chapterHash32, "t1", "finalizing", "ready", "assigned-node", chapterFinalizeAttempt))
	mock.ExpectRollback()
	handleChapterFinalizeResult(context.Background(), "chap-foreign", "completed", chapterFinalizeAttempt,
		&ipcpb.ProcessingJobResult{JobId: chapterFinalizeJobIDPrefix + "chap-foreign", Outputs: map[string]string{"artifact_hash": chapterHash32}, OutputPath: "/data/vod/" + chapterHash32 + ".mkv"},
		"attacker-node", logging.NewLogger())
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sqlmock expectations: %v", err)
	}
}

// INVARIANT: a late completion whose allocated artifact is no longer 'finalizing' (e.g. the
// parent DVR delete cascade soft-deleted it) must NOT resurrect it to 'ready'. The lock read
// sees the artifact status and treats it as an ignored no-op — no writes, no bounce.
func TestHandleChapterFinalizeResult_DoesNotResurrectDeletedArtifact(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	startFakeCommodoreServer(t, &fakeCommodoreInternal{})

	const chapterID = "chap-deleted-art"
	result := &ipcpb.ProcessingJobResult{
		JobId:           chapterFinalizeJobIDPrefix + chapterID,
		Outputs:         map[string]string{"artifact_hash": chapterHash32},
		OutputPath:      "/data/vod/" + chapterHash32 + ".mkv",
		OutputSizeBytes: 4096,
	}
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT c.state, COALESCE\(c.playback_artifact_hash`).
		WithArgs(chapterID).
		WillReturnRows(sqlmock.NewRows([]string{"state", "playback_artifact_hash", "tenant_id", "status", "parent_status", "finalize_node_id", "finalize_attempts"}).
			AddRow(ChapterStateFinalizing, chapterHash32, "t1", "deleted", "deleted", "node-1", chapterFinalizeAttempt)) // artifact + parent gone
	mock.ExpectRollback()

	handleChapterFinalizeResult(context.Background(), chapterID, "completed", chapterFinalizeAttempt, result, "node-1", logging.NewLogger())

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sqlmock expectations: %v", err)
	}
}

// INVARIANT: a transient persist failure inside the finalize tx rolls the whole tx back AND
// bounces the chapter finalizing → closed (RetryChapterFinalize) so the queue re-dispatches —
// the chapter is never left half-finalized.
func TestHandleChapterFinalizeResult_TransientPersistFailureBouncesToClosed(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	startFakeCommodoreServer(t, &fakeCommodoreInternal{})

	const chapterID = "chap-persist-fail"
	const tenant = "5eed517e-ba5e-da7a-517e-ba5eda7a0001"
	result := &ipcpb.ProcessingJobResult{
		JobId:           chapterFinalizeJobIDPrefix + chapterID,
		Outputs:         map[string]string{"artifact_hash": chapterHash32},
		OutputPath:      "/data/vod/" + chapterHash32 + ".mkv",
		OutputSizeBytes: 4096,
	}

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT c.state, COALESCE\(c.playback_artifact_hash`).
		WithArgs(chapterID).
		WillReturnRows(sqlmock.NewRows([]string{"state", "playback_artifact_hash", "tenant_id", "status", "parent_status", "finalize_node_id", "finalize_attempts"}).
			AddRow(ChapterStateFinalizing, chapterHash32, tenant, "finalizing", "ready", "node-1", chapterFinalizeAttempt))
	mock.ExpectExec(`UPDATE foghorn.artifacts\s+SET status = 'ready'`).
		WillReturnError(sql.ErrConnDone)
	mock.ExpectRollback()
	// Bounce finalizing → closed for retry.
	mock.ExpectExec(`UPDATE foghorn.dvr_chapters\s+SET state\s+= 'closed'`).
		WithArgs(sqlmock.AnyArg(), chapterID, chapterFinalizeAttempt, "node-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	handleChapterFinalizeResult(context.Background(), chapterID, "completed", chapterFinalizeAttempt, result, "node-1", logging.NewLogger())

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sqlmock expectations: %v", err)
	}
}

// INVARIANT: a "failed" result with a terminal source_missing signal marks the
// chapter failed_source_missing (no retry); it must NOT roll the row back to
// closed for another finalize attempt. This is the terminal-vs-transient gate.
func TestHandleChapterFinalizeResult_TerminalFailureMarksFailed(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	startFakeCommodoreServer(t, &fakeCommodoreInternal{})

	const chapterID = "chap-fail-1"
	result := &ipcpb.ProcessingJobResult{
		JobId: chapterFinalizeJobIDPrefix + chapterID,
		Outputs: map[string]string{
			"chapter_failure":        "source_missing",
			"chapter_failure_detail": "segments gone",
		},
	}

	// MarkChapterFailed is now one tx: transition the chapter (RETURNING its playback hash), fail
	// the allocated child artifact (RETURNING tenant), and enqueue the failed lifecycle to the
	// outbox — all committed together (no separate loss-prone emit).
	mock.ExpectBegin()
	mock.ExpectQuery(`UPDATE foghorn.dvr_chapters\s+SET state\s+= \$1.*RETURNING playback_artifact_hash`).
		WithArgs(ChapterStateFailedSourceMissing, "segments gone", chapterID, chapterFinalizeAttempt, "node-1").
		WillReturnRows(sqlmock.NewRows([]string{"playback_artifact_hash"}).AddRow(chapterHash32))
	mock.ExpectQuery(`UPDATE foghorn.artifacts\s+SET status = 'failed'.*RETURNING tenant_id`).
		WithArgs(chapterHash32, "segments gone").
		WillReturnRows(sqlmock.NewRows([]string{"tenant_id"}).AddRow("5eed517e-ba5e-da7a-517e-ba5eda7a0001"))
	mock.ExpectExec(`INSERT INTO foghorn.artifact_event_outbox`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	handleChapterFinalizeResult(context.Background(), chapterID, "failed", chapterFinalizeAttempt, result, "node-1", logging.NewLogger())

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sqlmock expectations: %v", err)
	}
}

// INVARIANT: a "failed" result with a transient (non-source-missing) error rolls
// the chapter finalizing → closed via RetryChapterFinalize so the queue retries,
// and never marks it terminally failed.
func TestHandleChapterFinalizeResult_TransientFailureRetries(t *testing.T) {
	mock, _, _ := setupArtifactTestDeps(t)
	startFakeCommodoreServer(t, &fakeCommodoreInternal{})

	const chapterID = "chap-retry-1"
	result := &ipcpb.ProcessingJobResult{
		JobId: chapterFinalizeJobIDPrefix + chapterID,
		Error: "network blip",
	}

	// RetryChapterFinalize rolls finalizing → closed (the only DB write), bound to the reporting node.
	mock.ExpectExec(`UPDATE foghorn.dvr_chapters`).
		WithArgs("network blip", chapterID, chapterFinalizeAttempt, "node-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	handleChapterFinalizeResult(context.Background(), chapterID, "failed", chapterFinalizeAttempt, result, "node-1", logging.NewLogger())

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sqlmock expectations: %v", err)
	}
}

// (A no-hash/no-output completion is NOT a silent no-op — it bounces the chapter to 'closed'
// for retry; see TestHandleChapterFinalizeResult_MalformedCompletionBouncesToClosed.)

// INVARIANT: chapterPlaybackArtifactHashFromOutputs prefers an explicit
// outputs["artifact_hash"], else derives the hash from the .mkv filename
// (Helmsman's vod/<hash>.mkv layout), else empty.
func TestChapterPlaybackArtifactHashFromOutputs(t *testing.T) {
	if got := chapterPlaybackArtifactHashFromOutputs(map[string]string{"artifact_hash": "abc"}, "/x/y.mkv"); got != "abc" {
		t.Fatalf("explicit hash should win, got %q", got)
	}
	if got := chapterPlaybackArtifactHashFromOutputs(nil, "/data/vod/deadbeef.mkv"); got != "deadbeef" {
		t.Fatalf("derive from filename failed, got %q", got)
	}
	if got := chapterPlaybackArtifactHashFromOutputs(nil, ""); got != "" {
		t.Fatalf("empty path should yield empty hash, got %q", got)
	}
}

// INVARIANT: updateChapterVodMetadataTx is a no-op success when outputs is empty (no
// stream-info to fill) — the finalize tx must still commit — and otherwise upserts the row.
func TestUpdateChapterVodMetadataTx(t *testing.T) {
	t.Run("empty outputs is a no-op", func(t *testing.T) {
		mock := setupChapterTest(t)
		mock.ExpectBegin()
		mock.ExpectRollback()
		tx, err := db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		if err := updateChapterVodMetadataTx(context.Background(), tx, chapterHash32, nil); err != nil {
			t.Fatalf("empty outputs must succeed: %v", err)
		}
		if err := tx.Rollback(); err != nil {
			t.Fatalf("rollback: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("empty outputs must not query: %v", err)
		}
	})

	t.Run("non-empty outputs upserts", func(t *testing.T) {
		mock := setupChapterTest(t)
		mock.ExpectBegin()
		mock.ExpectExec(`INSERT INTO foghorn.vod_metadata`).
			WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectCommit()
		tx, err := db.BeginTx(context.Background(), nil)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		if err := updateChapterVodMetadataTx(context.Background(), tx,
			chapterHash32, map[string]string{"duration_ms": "5000", "resolution": "1920x1080"}); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("commit: %v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("unmet expectations: %v", err)
		}
	})
}

// INVARIANT: chapterArtifactLifecycleIdentity joins the chapter to its playback
// artifact to recover (artifact_hash, tenant_id); a missing join surfaces the
// scan error rather than silently emitting an empty-identity lifecycle event.
func TestChapterArtifactLifecycleIdentity(t *testing.T) {
	t.Run("nil db errors", func(t *testing.T) {
		prev := db
		db = nil
		t.Cleanup(func() { db = prev })
		if _, _, err := chapterArtifactLifecycleIdentity(context.Background(), "c"); err == nil {
			t.Fatal("nil db must error")
		}
	})

	t.Run("resolves hash and tenant from join", func(t *testing.T) {
		mock := setupChapterTest(t)
		mock.ExpectQuery(`SELECT c.playback_artifact_hash, a.tenant_id::text AS tenant_id\s+FROM foghorn.dvr_chapters c\s+JOIN foghorn.artifacts a`).
			WithArgs("c1").
			WillReturnRows(sqlmock.NewRows([]string{"playback_artifact_hash", "tenant_id"}).
				AddRow(chapterHash32, "tenant-9"))
		hash, tenant, err := chapterArtifactLifecycleIdentity(context.Background(), "c1")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if hash != chapterHash32 || tenant != "tenant-9" {
			t.Fatalf("got (%q,%q)", hash, tenant)
		}
	})
}

// INVARIANT: ResolveChapterArtifactByHash returns parent-DVR routing context for
// a chapter-origin artifact (tenant/origin cluster/stream from the parent DVR),
// and returns nil for any non-chapter artifact. The parent-DVR is the security +
// routing authority for chapter VODs, not the raw artifact row.
func TestResolveChapterArtifactByHash(t *testing.T) {
	t.Run("rejects non-32-char hash without touching db", func(t *testing.T) {
		mock := setupChapterTest(t)
		startFakeCommodoreServer(t, &fakeCommodoreInternal{})
		if got := ResolveChapterArtifactByHash(context.Background(), "short"); got != nil {
			t.Fatalf("short hash must be rejected, got %+v", got)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("must not query for an invalid hash: %v", err)
		}
	})

	t.Run("non-chapter origin returns nil", func(t *testing.T) {
		mock := setupChapterTest(t)
		startFakeCommodoreServer(t, &fakeCommodoreInternal{})
		mock.ExpectQuery(`SELECT origin_type, origin_id, tenant_id::text AS tenant_id,\s+COALESCE\(origin_cluster_id`).
			WithArgs(chapterHash32).
			WillReturnRows(sqlmock.NewRows([]string{"origin_type", "origin_id", "tenant_id", "origin_cluster_id"}).
				AddRow("clip", "x", "t1", "c1"))
		if got := ResolveChapterArtifactByHash(context.Background(), chapterHash32); got != nil {
			t.Fatalf("clip-origin artifact must not resolve as chapter, got %+v", got)
		}
	})

	t.Run("chapter origin resolves parent-DVR context", func(t *testing.T) {
		mock := setupChapterTest(t)
		startFakeCommodoreServer(t, &fakeCommodoreInternal{
			dvrHash: func(_ context.Context, _ *commodorepb.ResolveDVRHashRequest) (*commodorepb.ResolveDVRHashResponse, error) {
				return &commodorepb.ResolveDVRHashResponse{
					Found: true, TenantId: "parent-tenant", StreamId: "parent-stream", OriginClusterId: "parent-cluster",
				}, nil
			},
		})
		mock.ExpectQuery(`SELECT origin_type, origin_id, tenant_id::text AS tenant_id,\s+COALESCE\(origin_cluster_id`).
			WithArgs(chapterHash32).
			WillReturnRows(sqlmock.NewRows([]string{"origin_type", "origin_id", "tenant_id", "origin_cluster_id"}).
				AddRow("dvr_chapter", "chap-7", "row-tenant", "row-cluster"))
		// GetChapter for origin_id chap-7.
		mock.ExpectQuery(`FROM foghorn.dvr_chapters c\s+WHERE c.chapter_id = \$1`).
			WithArgs("chap-7").
			WillReturnRows(playableChapterRow("chap-7", "parent-dvr-hash", ChapterStateFinalized))

		got := ResolveChapterArtifactByHash(context.Background(), chapterHash32)
		if got == nil {
			t.Fatal("chapter-origin artifact must resolve")
		}
		// Parent-DVR is authority: tenant/cluster/stream come from it, not the row.
		if got.TenantID != "parent-tenant" || got.OriginClusterID != "parent-cluster" || got.StreamID != "parent-stream" {
			t.Fatalf("expected parent-DVR context, got %+v", got)
		}
		if got.ArtifactHash != chapterHash32 {
			t.Fatalf("artifact hash mismatch: %q", got.ArtifactHash)
		}
	})

	t.Run("parent-DVR lookup miss falls back to row context", func(t *testing.T) {
		mock := setupChapterTest(t)
		startFakeCommodoreServer(t, &fakeCommodoreInternal{
			dvrHash: func(_ context.Context, _ *commodorepb.ResolveDVRHashRequest) (*commodorepb.ResolveDVRHashResponse, error) {
				return &commodorepb.ResolveDVRHashResponse{Found: false}, nil
			},
		})
		mock.ExpectQuery(`SELECT origin_type, origin_id, tenant_id::text AS tenant_id,\s+COALESCE\(origin_cluster_id`).
			WithArgs(chapterHash32).
			WillReturnRows(sqlmock.NewRows([]string{"origin_type", "origin_id", "tenant_id", "origin_cluster_id"}).
				AddRow("dvr_chapter", "chap-8", "row-tenant", "row-cluster"))
		mock.ExpectQuery(`FROM foghorn.dvr_chapters c\s+WHERE c.chapter_id = \$1`).
			WithArgs("chap-8").
			WillReturnRows(playableChapterRow("chap-8", "parent-dvr-hash", ChapterStateFinalized))

		got := ResolveChapterArtifactByHash(context.Background(), chapterHash32)
		if got == nil {
			t.Fatal("expected row-level fallback context")
		}
		// Falls back to the foghorn row's tenant/cluster when the parent
		// DVR can't be resolved.
		if got.TenantID != "row-tenant" || got.OriginClusterID != "row-cluster" {
			t.Fatalf("expected row fallback context, got %+v", got)
		}
	})
}
