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
// detached from that session. The session is recorded afresh only if it is still live
// once the settle window has passed: when Mist's buffer shuts down it ends the recording
// before it closes the publisher, and that close must win. A recording ended with its
// session is left bound.
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
	settlePasses := func(t *testing.T, gen string) {
		t.Helper()
		if _, err := db.Exec(`UPDATE foghorn.ingest_sessions SET dvr_intent_lease_until = NOW() - INTERVAL '1 second' WHERE id = $1::uuid`, gen); err != nil {
			t.Fatal(err)
		}
	}

	// staging rc11 edge-eu: Mist's buffer hit its input timeout and ended the recording
	// with "Live stream source shut down"; the stopped report reached Foghorn before the
	// publisher's PUSH_INPUT_CLOSE, and the session was recorded afresh on a stream that no
	// longer existed, leaving a failed recording.
	t.Run("the source ended and its close lands after the stopped report", func(t *testing.T) {
		gen := liveRecording(t, "live+srcend", "h-srcend", 310)
		if got, err := ClaimUnstartedDVRIntents(ctx, time.Minute, 50); err != nil || len(got) != 0 {
			t.Fatalf("a live session with an active recording must not be listed: %+v %v", got, err)
		}

		if detached := detachEndedWriterFromLiveSession(ctx, "h-srcend", lg); detached != gen {
			t.Fatalf("detached session = %q, want %q", detached, gen)
		}
		if sessionClaims(t, gen) {
			t.Fatal("the session was claimed for a fresh recording before its close could land")
		}
		if scanClaims(t, gen) {
			t.Fatal("the recovery scan claimed the session before its close could land")
		}

		closed, err := FinalizeIngestSessionClose(ctx, ingA, "node-a", 310, 5000, "live+srcend", lg)
		if err != nil || closed.EndedSessionID != gen {
			t.Fatalf("PUSH_INPUT_CLOSE ended %q (%v), want %q", closed.EndedSessionID, err, gen)
		}
		settlePasses(t, gen)
		if sessionClaims(t, gen) || scanClaims(t, gen) {
			t.Fatal("a session its publisher's close ended was claimed for a fresh recording")
		}
	})

	t.Run("a recorder that died while the source publishes is recorded afresh", func(t *testing.T) {
		gen := liveRecording(t, "live+recdied", "h-recdied", 320)
		if detached := detachEndedWriterFromLiveSession(ctx, "h-recdied", lg); detached != gen {
			t.Fatalf("detached session = %q, want %q", detached, gen)
		}
		var bound sql.NullString
		if err := db.QueryRow(`SELECT ingest_generation::text FROM foghorn.artifacts WHERE artifact_hash='h-recdied'`).Scan(&bound); err != nil {
			t.Fatal(err)
		}
		if bound.Valid {
			t.Fatalf("recording still bound to %s", bound.String)
		}
		var leaseSeconds float64
		if err := db.QueryRow(`SELECT EXTRACT(EPOCH FROM dvr_intent_lease_until - NOW()) FROM foghorn.ingest_sessions WHERE id = $1::uuid`, gen).Scan(&leaseSeconds); err != nil {
			t.Fatal(err)
		}
		if want := EndedWriterSettle.Seconds(); leaseSeconds < want-2 || leaseSeconds > want {
			t.Fatalf("settle lease ends in %.1fs, want about %.0fs", leaseSeconds, want)
		}
		if sessionClaims(t, gen) {
			t.Fatal("the session was claimed inside the settle window")
		}

		// A session that reconnected seconds ago is inside the scan's grace, yet its
		// recording already ended: once the settle window passes the session claim takes
		// it without waiting for the grace.
		settlePasses(t, gen)
		if _, err := db.Exec(`UPDATE foghorn.ingest_sessions SET started_at = NOW() WHERE id = $1::uuid`, gen); err != nil {
			t.Fatal(err)
		}
		if scanClaims(t, gen) {
			t.Fatal("the scan must leave a session inside its grace")
		}
		if !sessionClaims(t, gen) {
			t.Fatal("the live session must be claimed for a fresh recording after the settle window")
		}
		if sessionClaims(t, gen) {
			t.Fatal("a claimed intent is leased and must not be claimed twice")
		}
	})

	t.Run("a recording ended with its session stays bound", func(t *testing.T) {
		gen := liveRecording(t, "live+ended", "h-ended", 330)
		if _, err := db.Exec(`UPDATE foghorn.ingest_sessions SET ended_at=NOW(), ended_at_unix_millis=2000 WHERE id=$1::uuid`, gen); err != nil {
			t.Fatal(err)
		}
		if detached := detachEndedWriterFromLiveSession(ctx, "h-ended", lg); detached != "" {
			t.Fatalf("a recording ended with its session must not be detached, got session %q", detached)
		}
		var bound sql.NullString
		if err := db.QueryRow(`SELECT ingest_generation::text FROM foghorn.artifacts WHERE artifact_hash='h-ended'`).Scan(&bound); err != nil {
			t.Fatal(err)
		}
		if !bound.Valid || bound.String != gen {
			t.Fatalf("a recording ended with its session must stay bound, got %v", bound)
		}
	})
}
