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

// A recording whose writer ended on its own while its session is still live (a reconnect
// adopted a recording whose push Mist had just ended) is detached, and its live session is
// claimed at once for a fresh recording. A recording ended with its session is left bound.
func TestDetachEndedWriterFromLiveSession_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })
	ctx := context.Background()
	lg := logging.NewLogger()
	intent := []byte(`{"tenantId":"` + ingA + `","internalName":"live+detach"}`)

	live, _, err := CreateIngestSession(ctx, ingA, "node-a", "live+detach", 300, "u-live", 1000, intent, "cell-a", lg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, stream_internal_name, tenant_id, status, dvr_start_dispatch, ingest_generation)
		VALUES ('h-detach', 'dvr', 'live+detach', $1::uuid, 'recording', '{"source_node_id":"node-a"}'::jsonb, $2::uuid)
	`, ingA, live); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE foghorn.ingest_sessions SET started_at = NOW() - INTERVAL '1 hour'`); err != nil {
		t.Fatal(err)
	}
	if got, err := ClaimUnstartedDVRIntents(ctx, time.Minute, 50); err != nil || len(got) != 0 {
		t.Fatalf("a live session with an active recording must not be listed: %+v %v", got, err)
	}

	if detached := detachEndedWriterFromLiveSession(ctx, "h-detach", lg); detached != live {
		t.Fatalf("detached session = %q, want %q", detached, live)
	}

	var bound sql.NullString
	if err := db.QueryRow(`SELECT ingest_generation::text FROM foghorn.artifacts WHERE artifact_hash='h-detach'`).Scan(&bound); err != nil {
		t.Fatal(err)
	}
	if bound.Valid {
		t.Fatalf("recording still bound to %s", bound.String)
	}
	// A session that reconnected seconds ago is inside the scan's grace, yet its recording
	// already ended: the immediate claim takes it without waiting for the grace.
	if _, err := db.Exec(`UPDATE foghorn.ingest_sessions SET started_at = NOW() WHERE id = $1::uuid`, live); err != nil {
		t.Fatal(err)
	}
	if got, err := ClaimUnstartedDVRIntents(ctx, time.Minute, 50); err != nil || len(got) != 0 {
		t.Fatalf("the scan must leave a session inside its grace: %+v %v", got, err)
	}
	got, err := ClaimDVRIntentForSession(ctx, live)
	if err != nil || len(got) != 1 || got[0].SessionID != live {
		t.Fatalf("the live session must be claimed for a fresh recording: %+v %v", got, err)
	}
	if again, err := ClaimDVRIntentForSession(ctx, live); err != nil || len(again) != 0 {
		t.Fatalf("a claimed intent is leased and must not be claimed twice: %+v %v", again, err)
	}

	// A recording whose session already ended keeps its binding.
	ended, _, err := CreateIngestSession(ctx, ingA, "node-a", "live+ended", 301, "u-ended", 1000, intent, "cell-a", lg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, stream_internal_name, tenant_id, status, dvr_start_dispatch, ingest_generation)
		VALUES ('h-ended', 'dvr', 'live+ended', $1::uuid, 'recording', '{"source_node_id":"node-a"}'::jsonb, $2::uuid)
	`, ingA, ended); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE foghorn.ingest_sessions SET ended_at=NOW(), ended_at_unix_millis=2000 WHERE id=$1::uuid`, ended); err != nil {
		t.Fatal(err)
	}
	if detached := detachEndedWriterFromLiveSession(ctx, "h-ended", lg); detached != "" {
		t.Fatalf("a recording ended with its session must not be detached, got session %q", detached)
	}
	if err := db.QueryRow(`SELECT ingest_generation::text FROM foghorn.artifacts WHERE artifact_hash='h-ended'`).Scan(&bound); err != nil {
		t.Fatal(err)
	}
	if !bound.Valid || bound.String != ended {
		t.Fatalf("a recording ended with its session must stay bound, got %v", bound)
	}
}
