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
		nil, nil,
		nil, nil,
		int64(5), false,
		nil, nil,
		time.Unix(1700000000, 0),
	)
}

// INVARIANT: a "completed" chapter-finalize result advances the chapter row to
// 'finalized' (and only then), updates the artifact row to ready/mkv, registers
// the origin artifact in state, and upserts vod_metadata — mirroring the VOD
// processing branch but skipping the processing_jobs UPDATE (chapter job_ids
// are non-UUID). This pins the success state-transition contract.
func TestHandleChapterFinalizeResult_CompletedAdvancesToFinalized(t *testing.T) {
	mock, _, repo := setupArtifactTestDeps(t)
	startFakeCommodoreServer(t, &fakeCommodoreInternal{})

	// Make node-1 an active balancer node so the post-registration warm-cache
	// assertion (FindNodesByArtifactHash) can observe the artifact.
	state.DefaultManager().SetNodeInfo("node-1", "https://n1.example.com", true, nil, nil, "ams", "", nil)
	state.DefaultManager().TouchNode("node-1", true)

	const chapterID = "chap-fin-1"
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

	// 1) artifacts row UPDATE → ready/mkv/pending/local.
	mock.ExpectExec(`UPDATE foghorn.artifacts\s+SET status = 'ready'`).
		WithArgs(chapterHash32, int64(4096)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	// 2) projectArtifactSizeToCommodore lookup — return no row so it returns
	//    early without an UpdateArtifactSize RPC (keeps the test focused on
	//    the state transition, not the projection round-trip).
	mock.ExpectQuery(`SELECT artifact_type, tenant_id::text, size_bytes\s+FROM foghorn.artifacts`).
		WithArgs(chapterHash32).
		WillReturnError(sql.ErrNoRows)
	// 3) MarkChapterFinalized: finalizing → finalized, guarded by WHERE state='finalizing'.
	mock.ExpectExec(`UPDATE foghorn.dvr_chapters\s+SET state\s+= 'finalized'`).
		WithArgs(chapterID, int32(7), false, nil, nil).
		WillReturnResult(sqlmock.NewResult(0, 1))
	// 4) updateChapterVodMetadata upsert.
	mock.ExpectExec(`INSERT INTO foghorn.vod_metadata`).
		WillReturnResult(sqlmock.NewResult(0, 1))
	// 5) emitChapterVodLifecycle identity lookup (join chapters→artifacts).
	mock.ExpectQuery(`SELECT c.playback_artifact_hash, a.tenant_id::text\s+FROM foghorn.dvr_chapters c`).
		WithArgs(chapterID).
		WillReturnRows(sqlmock.NewRows([]string{"playback_artifact_hash", "tenant_id"}).
			AddRow(chapterHash32, "t1"))

	handleChapterFinalizeResult(context.Background(), chapterID, "completed", result, "node-1", logging.NewLogger())

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sqlmock expectations: %v", err)
	}
	// Origin artifact registered with role=origin, is_complete=true so the
	// chapter VOD is immediately serveable on the producing node.
	if len(repo.originArtifactCalls) != 1 {
		t.Fatalf("expected 1 origin artifact registration, got %d", len(repo.originArtifactCalls))
	}
	got := repo.originArtifactCalls[0]
	if got.Hash != chapterHash32 || got.NodeID != "node-1" || !got.Complete {
		t.Fatalf("unexpected origin registration: %+v", got)
	}
	// And the in-memory state manager carries the warm copy on the producing node.
	if nodes := state.DefaultManager().FindNodesByArtifactHash(chapterHash32); len(nodes) != 1 || nodes[0].NodeID != "node-1" {
		t.Fatalf("expected the chapter artifact registered on node-1 in state, got %+v", nodes)
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

	// MarkChapterFailed: → failed_source_missing, guarded by state IN ('closed','finalizing').
	mock.ExpectExec(`UPDATE foghorn.dvr_chapters\s+SET state\s+= \$2`).
		WithArgs(chapterID, ChapterStateFailedSourceMissing, "segments gone").
		WillReturnResult(sqlmock.NewResult(0, 1))
	// emitChapterVodLifecycle identity lookup (STATUS_FAILED path).
	mock.ExpectQuery(`SELECT c.playback_artifact_hash, a.tenant_id::text`).
		WithArgs(chapterID).
		WillReturnRows(sqlmock.NewRows([]string{"playback_artifact_hash", "tenant_id"}).
			AddRow(chapterHash32, "t1"))

	handleChapterFinalizeResult(context.Background(), chapterID, "failed", result, "node-1", logging.NewLogger())

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

	// RetryChapterFinalize rolls finalizing → closed (the only DB write).
	mock.ExpectExec(`UPDATE foghorn.dvr_chapters`).
		WithArgs(chapterID, "network blip").
		WillReturnResult(sqlmock.NewResult(0, 1))

	handleChapterFinalizeResult(context.Background(), chapterID, "failed", result, "node-1", logging.NewLogger())

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet sqlmock expectations: %v", err)
	}
}

// INVARIANT: a completed result with NO playback artifact hash (empty outputs +
// empty output path) is a no-op — it must not advance the chapter or touch the
// artifact row, because there's nothing to register.
func TestHandleChapterFinalizeResult_NoPlaybackHashIsNoOp(t *testing.T) {
	mock, _, repo := setupArtifactTestDeps(t)
	startFakeCommodoreServer(t, &fakeCommodoreInternal{})

	result := &ipcpb.ProcessingJobResult{
		JobId:   chapterFinalizeJobIDPrefix + "chap-x",
		Outputs: map[string]string{},
	}
	handleChapterFinalizeResult(context.Background(), "chap-x", "completed", result, "node-1", logging.NewLogger())

	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expected no DB calls: %v", err)
	}
	if len(repo.originArtifactCalls) != 0 {
		t.Fatal("no-op result must not register any origin artifact")
	}
}

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

// INVARIANT: updateChapterVodMetadata is a no-op when outputs is empty (no
// stream-info to fill), and otherwise upserts the metadata row.
func TestUpdateChapterVodMetadata(t *testing.T) {
	t.Run("empty outputs is a no-op", func(t *testing.T) {
		mock := setupChapterTest(t)
		updateChapterVodMetadata(context.Background(), logging.NewLogger(), logging.Fields{}, chapterHash32, nil)
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("empty outputs must not query: %v", err)
		}
	})

	t.Run("non-empty outputs upserts", func(t *testing.T) {
		mock := setupChapterTest(t)
		mock.ExpectExec(`INSERT INTO foghorn.vod_metadata`).
			WillReturnResult(sqlmock.NewResult(0, 1))
		updateChapterVodMetadata(context.Background(), logging.NewLogger(), logging.Fields{},
			chapterHash32, map[string]string{"duration_ms": "5000", "resolution": "1920x1080"})
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
		mock.ExpectQuery(`SELECT c.playback_artifact_hash, a.tenant_id::text\s+FROM foghorn.dvr_chapters c\s+JOIN foghorn.artifacts a`).
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
		mock.ExpectQuery(`SELECT origin_type, origin_id, tenant_id::text,\s+COALESCE\(origin_cluster_id`).
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
		mock.ExpectQuery(`SELECT origin_type, origin_id, tenant_id::text,\s+COALESCE\(origin_cluster_id`).
			WithArgs(chapterHash32).
			WillReturnRows(sqlmock.NewRows([]string{"origin_type", "origin_id", "tenant_id", "origin_cluster_id"}).
				AddRow("dvr_chapter", "chap-7", "row-tenant", "row-cluster"))
		// GetChapter for origin_id chap-7.
		mock.ExpectQuery(`FROM foghorn.dvr_chapters\s+WHERE chapter_id = \$1`).
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
		mock.ExpectQuery(`SELECT origin_type, origin_id, tenant_id::text,\s+COALESCE\(origin_cluster_id`).
			WithArgs(chapterHash32).
			WillReturnRows(sqlmock.NewRows([]string{"origin_type", "origin_id", "tenant_id", "origin_cluster_id"}).
				AddRow("dvr_chapter", "chap-8", "row-tenant", "row-cluster"))
		mock.ExpectQuery(`FROM foghorn.dvr_chapters\s+WHERE chapter_id = \$1`).
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
