//go:build schema_verify

package control

import (
	"database/sql"
	"fmt"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// A recording has no media file of its own, so the chapter-finalize
// transaction writes the finalized chapter's tracks to the parent recording
// row, which re-dirties it for the catalog projection. A completion without
// tracks leaves the recording's tracks alone.
func TestChapterFinalizeSetsRecordingTracks_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })
	prevRetry := FinalizeRetrySeconds
	FinalizeRetrySeconds = 0
	t.Cleanup(func() { FinalizeRetrySeconds = prevRetry })

	start := time.Now().UTC().Add(-3 * time.Minute).Truncate(time.Second)
	const recording = "dvrrecordingtracks00000000000001"
	insertReplayRecording(t, conn, replayRecording{hash: recording, status: "recording", segment: "uploaded", start: start, end: start.Add(150 * time.Second),
		mode: sql.NullString{String: ChapterModeWindowSized, Valid: true}})
	if res, err := FinalizeDVR(t.Context(), recording, FinalizeOptions{}); err != nil || res.ArtifactStatus != "completed" {
		t.Fatalf("finalize recording: %+v, %v", res, err)
	}
	readParent := func() (sql.NullString, int64) {
		t.Helper()
		var tracks sql.NullString
		var revision int64
		if err := conn.QueryRowContext(t.Context(), `SELECT tracks::text, catalog_revision FROM foghorn.artifacts
			WHERE artifact_hash = $1 AND tenant_id = $2::uuid`, recording, domainTenant).Scan(&tracks, &revision); err != nil {
			t.Fatal(err)
		}
		return tracks, revision
	}
	rows, err := conn.QueryContext(t.Context(), `SELECT chapter_id FROM foghorn.dvr_chapters WHERE artifact_hash = $1 ORDER BY start_ms`, recording)
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
	if len(chapters) < 2 {
		t.Fatalf("recording chapters = %d, want at least 2", len(chapters))
	}

	const tracksJSON = `[{"type":"video","codec":"H264","width":1280,"height":720},{"type":"audio","codec":"AAC","channels":2}]`
	finalize := func(i int, present bool, tracks string) {
		t.Helper()
		chapterID := chapters[i]
		playbackHash := fmt.Sprintf("dvrtrackschapterpb000000000000%02d", i)
		if _, err := conn.ExecContext(t.Context(), `INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, tenant_id, status, origin_type, origin_id, library_visible)
			VALUES ($1, 'vod', $2::uuid, 'finalizing', 'dvr_chapter', $3, false)`, playbackHash, domainTenant, chapterID); err != nil {
			t.Fatalf("allocate chapter artifact: %v", err)
		}
		if _, err := conn.ExecContext(t.Context(), `UPDATE foghorn.dvr_chapters
			SET state = 'finalizing', finalize_node_id = 'owner', finalize_attempts = 1, playback_artifact_hash = $2
			WHERE chapter_id = $1`, chapterID, playbackHash); err != nil {
			t.Fatalf("mark chapter finalizing: %v", err)
		}
		if _, err := finalizeChapterArtifactTx(t.Context(), chapterID, playbackHash, "", "owner", 1,
			1000, 10, false, 0, 60000, 60000, present, tracks, nil, logging.NewLogger(), logging.Fields{}); err != nil {
			t.Fatalf("finalize chapter %d: %v", i, err)
		}
	}

	_, revisionBefore := readParent()
	finalize(0, true, tracksJSON)
	tracks, revisionAfter := readParent()
	var same bool
	if err := conn.QueryRowContext(t.Context(), `SELECT $1::jsonb = $2::jsonb`, tracks.String, tracksJSON).Scan(&same); err != nil {
		t.Fatal(err)
	}
	if !tracks.Valid || !same {
		t.Fatalf("recording tracks = %v, want the finalized chapter's tracks", tracks)
	}
	if revisionAfter <= revisionBefore {
		t.Fatalf("recording catalog revision %d did not advance past %d", revisionAfter, revisionBefore)
	}

	finalize(1, false, "[]")
	if kept, _ := readParent(); kept != tracks {
		t.Fatalf("completion without tracks changed recording tracks to %v", kept)
	}
}
