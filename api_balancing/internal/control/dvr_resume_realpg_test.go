//go:build schema_verify

package control

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"frameworks/api_balancing/internal/database/foghorndb"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// A publisher that reconnects to the same Mist buffer continues the recording its
// previous session left running on that source node. Proven on the real schema: the
// generation FK, the one-active-DVR-per-generation index and the dispatch JSON.
func TestAdoptResumedDVR_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })
	ctx := context.Background()
	lg := logging.NewLogger()
	q := foghorndb.New(db)

	session := func(t *testing.T, stream, node string, pid int64, startMillis int64) string {
		t.Helper()
		gen, outcome, err := CreateIngestSession(ctx, ingA, node, stream, pid, fmt.Sprintf("u-%s-%d", stream, pid), startMillis, nil, "cell-a", lg)
		if err != nil || gen == "" {
			t.Fatalf("session %s/%d: gen=%q outcome=%v err=%v", stream, pid, gen, outcome, err)
		}
		return gen
	}
	endSession := func(t *testing.T, gen string, atMillis int64) {
		t.Helper()
		if _, err := db.Exec(`UPDATE foghorn.ingest_sessions SET ended_at=NOW(), ended_at_unix_millis=$2 WHERE id=$1::uuid`, gen, atMillis); err != nil {
			t.Fatalf("end %s: %v", gen, err)
		}
	}
	recording := func(t *testing.T, hash, stream, node, status, gen string) {
		t.Helper()
		if _, err := db.Exec(`
			INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, stream_internal_name, tenant_id, status, dvr_start_dispatch, ingest_generation)
			VALUES ($1, 'dvr', $2, $3::uuid, $4, jsonb_build_object('source_node_id', $5::text, 'ingest_generation', $6::text), $6::uuid)
		`, hash, stream, ingA, status, node, gen); err != nil {
			t.Fatalf("insert recording %s: %v", hash, err)
		}
	}
	adopt := func(t *testing.T, stream, node, gen string) (string, bool) {
		t.Helper()
		row, err := q.AdoptResumedDVR(ctx, foghorndb.AdoptResumedDVRParams{
			NewGeneration: gen, TenantID: ingA, StreamInternalName: sql.NullString{String: stream, Valid: true}, SourceNodeID: node,
		})
		if errors.Is(err, sql.ErrNoRows) {
			return "", false
		}
		if err != nil {
			t.Fatalf("adopt %s: %v", stream, err)
		}
		return row.ArtifactHash, true
	}

	t.Run("same node reconnect continues the recording", func(t *testing.T) {
		old := session(t, "live+resume", "node-a", 100, 1000)
		recording(t, "h-resume", "live+resume", "node-a", "recording", old)
		endSession(t, old, 2000)
		next := session(t, "live+resume", "node-a", 200, 3000)

		hash, ok := adopt(t, "live+resume", "node-a", next)
		if !ok || hash != "h-resume" {
			t.Fatalf("adopt = %q %v, want h-resume", hash, ok)
		}
		var bound, dispatched string
		if err := db.QueryRow(`SELECT ingest_generation::text, dvr_start_dispatch->>'ingest_generation' FROM foghorn.artifacts WHERE artifact_hash='h-resume'`).Scan(&bound, &dispatched); err != nil {
			t.Fatal(err)
		}
		if bound != next || dispatched != next {
			t.Fatalf("recording bound to %s (dispatch %s), want %s", bound, dispatched, next)
		}
		// The old session's ended-source backstop no longer claims the adopted recording.
		claims, err := ClaimDVRStops(ctx, db,
			`stream_internal_name = $1 AND dvr_start_dispatch->>'source_node_id' = $2 AND tenant_id::text = $3
		 AND (ingest_generation IS NULL OR NOT EXISTS (
		     SELECT 1 FROM foghorn.ingest_sessions s
		      WHERE s.id = foghorn.artifacts.ingest_generation AND s.ended_at IS NULL))`,
			"live+resume", "node-a", ingA)
		if err != nil || len(claims) != 0 {
			t.Fatalf("ended-source backstop claimed the resumed recording: %+v %v", claims, err)
		}
	})

	t.Run("a recording whose stop is claimed is not adopted", func(t *testing.T) {
		old := session(t, "live+stopping", "node-a", 110, 1000)
		recording(t, "h-stopping", "live+stopping", "node-a", "stopping", old)
		endSession(t, old, 2000)
		next := session(t, "live+stopping", "node-a", 210, 3000)
		if hash, ok := adopt(t, "live+stopping", "node-a", next); ok {
			t.Fatalf("adopted a stopping recording %q", hash)
		}
	})

	t.Run("a reconnect on another node records fresh", func(t *testing.T) {
		old := session(t, "live+moved", "node-a", 120, 1000)
		recording(t, "h-moved", "live+moved", "node-a", "recording", old)
		endSession(t, old, 2000)
		next := session(t, "live+moved", "node-b", 220, 3000)
		if hash, ok := adopt(t, "live+moved", "node-b", next); ok {
			t.Fatalf("adopted another node's recording %q", hash)
		}
	})
}

// A recording whose writer ended on its own while its session still looks live is
// detached and its session recorded afresh at once, unless the recording node reported
// that the recording's source ended: Mist's buffer ends its recordings when it shuts down
// and closes the publisher only after them, and that close must end the session. A
// recording ended with its session is left bound.
func TestDetachEndedWriterFromLiveSession_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })
	ctx := context.Background()
	lg := logging.NewLogger()

	liveRecording := func(t *testing.T, stream, hash string, pid int64) string {
		t.Helper()
		intent := []byte(`{"tenantId":"` + ingA + `","internalName":"` + stream + `"}`)
		gen, _, err := CreateIngestSession(ctx, ingA, "node-a", stream, pid, "u-"+hash, 1000, intent, "cell-a", lg)
		if err != nil || gen == "" {
			t.Fatalf("session %s: gen=%q err=%v", stream, gen, err)
		}
		if _, err := db.Exec(`
			INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, stream_internal_name, tenant_id, status, dvr_start_dispatch, ingest_generation)
			VALUES ($1, 'dvr', $2, $3::uuid, 'recording', '{"source_node_id":"node-a"}'::jsonb, $4::uuid)
		`, hash, stream, ingA, gen); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`UPDATE foghorn.ingest_sessions SET started_at = NOW() - INTERVAL '1 hour' WHERE id = $1::uuid`, gen); err != nil {
			t.Fatal(err)
		}
		return gen
	}
	boundTo := func(t *testing.T, hash string) sql.NullString {
		t.Helper()
		var bound sql.NullString
		if err := db.QueryRow(`SELECT ingest_generation::text FROM foghorn.artifacts WHERE artifact_hash=$1`, hash).Scan(&bound); err != nil {
			t.Fatal(err)
		}
		return bound
	}
	scanClaims := func(t *testing.T, gen string) bool {
		t.Helper()
		got, err := ClaimUnstartedDVRIntents(ctx, time.Minute, 50)
		if err != nil {
			t.Fatal(err)
		}
		for _, it := range got {
			if it.SessionID == gen {
				return true
			}
		}
		return false
	}
	sessionClaims := func(t *testing.T, gen string) bool {
		t.Helper()
		got, err := ClaimDVRIntentForSession(ctx, gen)
		if err != nil {
			t.Fatal(err)
		}
		return len(got) == 1 && got[0].SessionID == gen
	}

	// staging rc11 edge-eu: Mist's buffer hit its input timeout and ended the recording
	// with "Live stream source shut down"; the stopped report reached Foghorn before the
	// publisher's PUSH_INPUT_CLOSE, and the session was recorded afresh on a stream that no
	// longer existed, leaving a failed recording.
	t.Run("the source ended and its close lands after the stopped report", func(t *testing.T) {
		gen := liveRecording(t, "live+srcend", "h-srcend", 310)
		if scanClaims(t, gen) {
			t.Fatal("a live session with an active recording must not be listed")
		}

		if detached := detachEndedWriterFromLiveSession(ctx, "h-srcend", ipcpb.DVRSourceState_DVR_SOURCE_STATE_ENDED, lg); detached != "" {
			t.Fatalf("a recording whose source ended was detached from session %q", detached)
		}
		if bound := boundTo(t, "h-srcend"); !bound.Valid || bound.String != gen {
			t.Fatalf("a recording whose source ended must stay bound to %s, got %v", gen, bound)
		}
		if sessionClaims(t, gen) || scanClaims(t, gen) {
			t.Fatal("the session was claimed for a fresh recording before its close landed")
		}

		closed, err := FinalizeIngestSessionClose(ctx, ingA, "node-a", 310, 5000, "live+srcend", lg)
		if err != nil || closed.EndedSessionID != gen {
			t.Fatalf("PUSH_INPUT_CLOSE ended %q (%v), want %q", closed.EndedSessionID, err, gen)
		}
		if sessionClaims(t, gen) || scanClaims(t, gen) {
			t.Fatal("a session its publisher's close ended was claimed for a fresh recording")
		}
	})

	t.Run("a recorder that died while the source publishes is recorded afresh at once", func(t *testing.T) {
		gen := liveRecording(t, "live+recdied", "h-recdied", 320)
		if detached := detachEndedWriterFromLiveSession(ctx, "h-recdied", ipcpb.DVRSourceState_DVR_SOURCE_STATE_LIVE, lg); detached != gen {
			t.Fatalf("detached session = %q, want %q", detached, gen)
		}
		if bound := boundTo(t, "h-recdied"); bound.Valid {
			t.Fatalf("recording still bound to %s", bound.String)
		}
		// A session that reconnected seconds ago is inside the scan's grace, yet its
		// recording already ended: the immediate claim takes it without waiting.
		if _, err := db.Exec(`UPDATE foghorn.ingest_sessions SET started_at = NOW() WHERE id = $1::uuid`, gen); err != nil {
			t.Fatal(err)
		}
		if scanClaims(t, gen) {
			t.Fatal("the scan must leave a session inside its grace")
		}
		if !sessionClaims(t, gen) {
			t.Fatal("the live session must be claimed for a fresh recording at once")
		}
		if sessionClaims(t, gen) {
			t.Fatal("a claimed intent is leased and must not be claimed twice")
		}
	})

	t.Run("an unreported source keeps the session-row decision", func(t *testing.T) {
		gen := liveRecording(t, "live+unknown", "h-unknown", 330)
		if detached := detachEndedWriterFromLiveSession(ctx, "h-unknown", ipcpb.DVRSourceState_DVR_SOURCE_STATE_UNSPECIFIED, lg); detached != gen {
			t.Fatalf("detached session = %q, want %q", detached, gen)
		}
		if !sessionClaims(t, gen) {
			t.Fatal("a live session whose source state is not known must be claimed for a fresh recording at once")
		}
	})

	t.Run("a recording ended with its session stays bound", func(t *testing.T) {
		gen := liveRecording(t, "live+ended", "h-ended", 340)
		if _, err := db.Exec(`UPDATE foghorn.ingest_sessions SET ended_at=NOW(), ended_at_unix_millis=2000 WHERE id=$1::uuid`, gen); err != nil {
			t.Fatal(err)
		}
		if detached := detachEndedWriterFromLiveSession(ctx, "h-ended", ipcpb.DVRSourceState_DVR_SOURCE_STATE_LIVE, lg); detached != "" {
			t.Fatalf("a recording ended with its session must not be detached, got session %q", detached)
		}
		if bound := boundTo(t, "h-ended"); !bound.Valid || bound.String != gen {
			t.Fatalf("a recording ended with its session must stay bound, got %v", bound)
		}
	})
}
