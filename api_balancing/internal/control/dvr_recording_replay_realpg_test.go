//go:build schema_verify

package control

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	publicv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/public/v1"
	"google.golang.org/protobuf/proto"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
)

type replayRecording struct {
	hash, status string
	mode         sql.NullString
	interval     sql.NullInt32
	segment      string
	start, end   time.Time
}

func insertReplayRecording(t *testing.T, conn *sql.DB, r replayRecording) {
	t.Helper()
	if _, err := conn.ExecContext(t.Context(), `INSERT INTO foghorn.artifacts
		(artifact_hash, artifact_type, tenant_id, stream_id, status, dvr_start_dispatch, dvr_window_seconds,
		 dvr_chapter_mode, dvr_chapter_interval, started_at, ended_at)
		VALUES ($1, 'dvr', $2::uuid, $3::uuid, $4, '{"node_id":"owner"}', 60, $5, $6, $7, $8)`,
		r.hash, domainTenant, domainStream, r.status, r.mode, r.interval, r.start, r.end); err != nil {
		t.Fatalf("insert recording %s: %v", r.hash, err)
	}
	if r.segment == "" {
		return
	}
	if _, err := conn.ExecContext(t.Context(), `INSERT INTO foghorn.dvr_segments
		(artifact_hash, segment_name, sequence, media_start_ms, media_end_ms, duration_ms, s3_key, status)
		VALUES ($1, 'segment.ts', 1, $2::bigint, $2::bigint + 6000, 6000, 'test/segment.ts', $3)`,
		r.hash, r.start.UnixMilli(), r.segment); err != nil {
		t.Fatalf("insert segment %s: %v", r.hash, err)
	}
}

func chapterCount(t *testing.T, conn *sql.DB, hash string) int {
	t.Helper()
	var n int
	if err := conn.QueryRowContext(t.Context(), `SELECT count(*) FROM foghorn.dvr_chapters WHERE artifact_hash = $1`, hash).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// recording.ready announces that a recording can be replayed. Stopping a
// chaptered recording closes its chapters but emits no ready; the first
// finalized chapter emits exactly one, carrying that chapter's playback id,
// and later chapters emit none. A live-rewind-only recording (no chapter
// mode) finalizes without chapters or ready.
func TestRecordingReadyRequiresChapters_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })
	prevRetry := FinalizeRetrySeconds
	FinalizeRetrySeconds = 0
	t.Cleanup(func() { FinalizeRetrySeconds = prevRetry })

	start := time.Now().UTC().Add(-3 * time.Minute).Truncate(time.Second)
	end := start.Add(150 * time.Second)
	const chaptered, rewindOnly = "dvrreplayready000000000000000001", "dvrreplayready000000000000000002"
	insertReplayRecording(t, conn, replayRecording{hash: chaptered, status: "recording", segment: "uploaded", start: start, end: end,
		mode: sql.NullString{String: ChapterModeWindowSized, Valid: true}})
	insertReplayRecording(t, conn, replayRecording{hash: rewindOnly, status: "recording", segment: "uploaded", start: start, end: end})

	for _, hash := range []string{chaptered, rewindOnly} {
		res, err := FinalizeDVR(t.Context(), hash, FinalizeOptions{})
		if err != nil || res.ArtifactStatus != "completed" {
			t.Fatalf("finalize %s: %+v, %v", hash, res, err)
		}
	}
	readyFor := func(hash string) int {
		n := 0
		for _, row := range domainRows(t, conn, "recording.ready") {
			if row.aggregateID == hash {
				n++
			}
		}
		return n
	}
	if got := chapterCount(t, conn, chaptered); got != 3 {
		t.Fatalf("window-sized recording chapters = %d, want 3", got)
	}
	if got := readyFor(chaptered); got != 0 {
		t.Fatalf("recording.ready emitted %d times at stop, before any chapter was replayable", got)
	}
	if got := chapterCount(t, conn, rewindOnly); got != 0 {
		t.Fatalf("live-rewind-only recording chapters = %d, want 0", got)
	}
	if got := readyFor(rewindOnly); got != 0 {
		t.Fatalf("live-rewind-only recording emitted recording.ready %d times", got)
	}

	rows, err := conn.QueryContext(t.Context(), `SELECT chapter_id FROM foghorn.dvr_chapters WHERE artifact_hash = $1 ORDER BY start_ms`, chaptered)
	if err != nil {
		t.Fatal(err)
	}
	var chapters []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		chapters = append(chapters, id)
	}
	_ = rows.Close()

	for i, chapterID := range chapters[:2] {
		playbackHash := fmt.Sprintf("dvrreadychapterpb00000000000000%d", i)
		playbackID := fmt.Sprintf("pbchapter%d", i)
		if _, err := conn.ExecContext(t.Context(), `INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, tenant_id, status, origin_type, origin_id, library_visible)
			VALUES ($1, 'vod', $2::uuid, 'finalizing', 'dvr_chapter', $3, false)`, playbackHash, domainTenant, chapterID); err != nil {
			t.Fatalf("allocate chapter artifact: %v", err)
		}
		if _, err := conn.ExecContext(t.Context(), `UPDATE foghorn.dvr_chapters
			SET state = 'finalizing', finalize_node_id = 'owner', finalize_attempts = 1, playback_artifact_hash = $2, playback_id = $3
			WHERE chapter_id = $1`, chapterID, playbackHash, playbackID); err != nil {
			t.Fatalf("mark chapter finalizing: %v", err)
		}
		if _, err := finalizeChapterArtifactTx(t.Context(), chapterID, playbackHash, "", "owner", 1,
			1000, 10, false, 0, 60000, 60000, false, "", nil, logging.NewLogger(), logging.Fields{}); err != nil {
			t.Fatalf("finalize chapter %d: %v", i, err)
		}
		if got := readyFor(chaptered); got != 1 {
			t.Fatalf("after %d finalized chapter(s) recording.ready = %d, want exactly 1", i+1, got)
		}
	}
	var ready publicv1.RecordingReady
	for _, row := range domainRows(t, conn, "recording.ready") {
		if row.aggregateID == chaptered {
			if err := proto.Unmarshal(row.payload, &ready); err != nil {
				t.Fatal(err)
			}
		}
	}
	if ready.GetArtifact().GetPlaybackId() != "pbchapter0" || ready.GetArtifact().GetArtifactId() != chaptered {
		t.Fatalf("recording.ready artifact = %+v, want the recording with its first chapter's playback id", ready.GetArtifact())
	}
}

// The v0.3.11 backfill gives finalized recordings without a chapter mode
// window-sized chapters, and the sweeper's terminal backfill then
// materializes them once, only where stored segments remain and no terminal
// chapter exists yet.
func TestTerminalChapterBackfill_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })

	start := time.Now().UTC().Add(-time.Hour).Truncate(time.Second)
	end := start.Add(150 * time.Second)
	const (
		legacy    = "dvrbackfill000000000000000000001"
		reclaimed = "dvrbackfill000000000000000000002"
		closed    = "dvrbackfill000000000000000000003"
		deleted   = "dvrbackfill000000000000000000004"
	)
	insertReplayRecording(t, conn, replayRecording{hash: legacy, status: "completed", segment: "deleted_local", start: start, end: end})
	insertReplayRecording(t, conn, replayRecording{hash: reclaimed, status: "completed", segment: "reclaimed", start: start, end: end})
	insertReplayRecording(t, conn, replayRecording{hash: closed, status: "completed", segment: "uploaded", start: start, end: end,
		mode: sql.NullString{String: ChapterModeWindowSized, Valid: true}, interval: sql.NullInt32{Int32: 7200, Valid: true}})
	insertReplayRecording(t, conn, replayRecording{hash: deleted, status: "deleted", segment: "uploaded", start: start, end: end})
	if _, err := conn.ExecContext(t.Context(), `INSERT INTO foghorn.dvr_chapters
		(chapter_id, artifact_hash, mode, interval_seconds, start_ms, end_ms, state)
		VALUES ('existing-terminal', $1, 'window_sized_chapters', 7200, $2, $3, 'finalized')`,
		closed, start.UnixMilli(), end.UnixMilli()); err != nil {
		t.Fatal(err)
	}

	migration, err := dbsql.Content.ReadFile("migrations/foghorn/v0.3.11/postdeploy/003_backfill_dvr_chapter_mode.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), string(migration)); err != nil {
		t.Fatalf("apply backfill migration: %v", err)
	}

	for range 2 {
		if _, err := BackfillTerminalChapters(t.Context(), 100, logging.NewLogger()); err != nil {
			t.Fatal(err)
		}
	}
	if got := chapterCount(t, conn, legacy); got != 3 {
		t.Fatalf("legacy recording chapters = %d, want 3 window-sized chapters", got)
	}
	if got := chapterCount(t, conn, reclaimed); got != 0 {
		t.Fatalf("recording without stored segments got %d chapters", got)
	}
	if got := chapterCount(t, conn, closed); got != 1 {
		t.Fatalf("recording with a terminal chapter got %d chapters, want its existing 1", got)
	}
	var deletedMode sql.NullString
	if err := conn.QueryRowContext(t.Context(), `SELECT dvr_chapter_mode FROM foghorn.artifacts WHERE artifact_hash = $1`, deleted).Scan(&deletedMode); err != nil {
		t.Fatal(err)
	}
	if deletedMode.Valid || chapterCount(t, conn, deleted) != 0 {
		t.Fatalf("deleted recording was backfilled: mode=%v", deletedMode)
	}
	var pending int
	if err := conn.QueryRowContext(t.Context(), `SELECT count(*) FROM foghorn.artifacts
		WHERE artifact_type = 'dvr' AND dvr_chapter_mode IS NOT NULL AND dvr_chapter_backfill_complete = false`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("%d finalized recordings still pending terminal backfill", pending)
	}
}

// A chapter keeps its id from the moment it opens until it is finalized: the
// stop truncates its end in place. A chapter row written before ids were
// start-derived is matched by its range start, so the sweeper neither
// duplicates it nor closes it early, and the stop keeps its id too.
func TestChapterIDStableAcrossStop_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })

	start := time.Now().UTC().Add(-90 * time.Second).Truncate(time.Second)
	stop := start.Add(90 * time.Second)
	window := sql.NullString{String: ChapterModeWindowSized, Valid: true}
	const fresh, upgraded = "dvrchapterid00000000000000000001", "dvrchapterid00000000000000000002"
	insertReplayRecording(t, conn, replayRecording{hash: fresh, status: "recording", mode: window, start: start, end: stop})
	insertReplayRecording(t, conn, replayRecording{hash: upgraded, status: "recording", mode: window, start: start, end: stop})

	// The upgraded recording's in-flight chapter was opened by the previous
	// derivation, which hashed the scheduled end as well.
	const legacyID = "legacyopenchapter000000000000001"
	if _, err := conn.ExecContext(t.Context(), `INSERT INTO foghorn.dvr_chapters
		(chapter_id, artifact_hash, mode, interval_seconds, start_ms, end_ms, is_current, state)
		VALUES ($1, $2, $3, 60, $4, $5, true, 'open')`,
		legacyID, upgraded, ChapterModeWindowSized, start.Add(60*time.Second).UnixMilli(), start.Add(120*time.Second).UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(t.Context(), `INSERT INTO foghorn.dvr_chapters
		(chapter_id, artifact_hash, mode, interval_seconds, start_ms, end_ms, is_current, state)
		VALUES ('legacyclosedchapter0000000000001', $1, $2, 60, $3, $4, false, 'closed')`,
		upgraded, ChapterModeWindowSized, start.UnixMilli(), start.Add(60*time.Second).UnixMilli()); err != nil {
		t.Fatal(err)
	}

	openID := func(hash string) string {
		t.Helper()
		var id string
		if err := conn.QueryRowContext(t.Context(), `SELECT chapter_id FROM foghorn.dvr_chapters
			WHERE artifact_hash = $1 AND is_current AND state = 'open'`, hash).Scan(&id); err != nil {
			t.Fatalf("current open chapter of %s: %v", hash, err)
		}
		return id
	}
	for _, hash := range []string{fresh, upgraded} {
		if err := BackfillChaptersThrough(t.Context(), hash, ChapterModeWindowSized, 60, start.UnixMilli(), start.Add(80*time.Second).UnixMilli()); err != nil {
			t.Fatalf("sweep %s: %v", hash, err)
		}
	}
	freshOpen := openID(fresh)
	if got := openID(upgraded); got != legacyID {
		t.Fatalf("sweeper replaced the upgraded recording's open chapter %s with %s", legacyID, got)
	}
	if got := chapterCount(t, conn, upgraded); got != 2 {
		t.Fatalf("upgraded recording has %d chapter rows, want its 2 existing ranges without duplicates", got)
	}

	for _, hash := range []string{fresh, upgraded} {
		if err := CloseTerminalChapter(t.Context(), hash, stop.UnixMilli(), logging.NewLogger()); err != nil {
			t.Fatalf("terminal close %s: %v", hash, err)
		}
	}
	for hash, wantID := range map[string]string{fresh: freshOpen, upgraded: legacyID} {
		var state string
		var endMs int64
		if err := conn.QueryRowContext(t.Context(), `SELECT state, end_ms FROM foghorn.dvr_chapters WHERE chapter_id = $1`, wantID).Scan(&state, &endMs); err != nil {
			t.Fatalf("chapter %s of %s after stop: %v", wantID, hash, err)
		}
		if state != ChapterStateClosed || endMs != stop.UnixMilli() {
			t.Fatalf("chapter %s after stop: state=%s end=%d, want closed at the stop %d", wantID, state, endMs, stop.UnixMilli())
		}
		if got := chapterCount(t, conn, hash); got != 2 {
			t.Fatalf("%s has %d chapters after stop, want 2", hash, got)
		}
	}
}
