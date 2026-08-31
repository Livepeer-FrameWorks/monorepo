//go:build schema_verify

package control

import (
	"context"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

func tombstoneCount(t *testing.T, tenant, node string, pid int64, stream string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`
		SELECT count(*) FROM foghorn.ingest_close_tombstones
		 WHERE tenant_id=$1::uuid AND node_id=$2 AND connector_pid=$3 AND stream_internal_name=$4
	`, tenant, node, pid, stream).Scan(&n); err != nil {
		t.Fatalf("count tombstones: %v", err)
	}
	return n
}

// TestIngestCloseTombstone_RealPG proves the close-before-insert fix against the real schema: a
// PUSH_INPUT_CLOSE that beats its own PUSH_REWRITE records a tombstone; the late rewrite is then DENIED
// (the dead publisher is never resurrected as an active session); a genuine later reconnect on the
// reused connector is NOT blocked; and the tombstone is swept by the TTL purge.
func TestIngestCloseTombstone_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })
	ctx := context.Background()
	lg := logging.NewLogger()

	const node, stream = "node-cbt", "live+cbt"
	const pid int64 = 100

	// 1. Close arrives before any session exists → finalizes nothing, records a tombstone.
	res, err := FinalizeIngestSessionClose(ctx, ingA, node, pid, 5000, stream, lg)
	if err != nil {
		t.Fatalf("close-before-insert: %v", err)
	}
	if res.EndedSessionID != "" {
		t.Fatalf("close-before-insert must finalize nothing, got %+v", res)
	}
	if c := tombstoneCount(t, ingA, node, pid, stream); c != 1 {
		t.Fatalf("expected one close tombstone, got %d", c)
	}

	// 2. The late rewrite (started at or before the recorded close) is DENIED — no active session.
	_, outcome, err := CreateIngestSession(ctx, ingA, node, stream, pid, "uuid-late", 4000, nil, "cell-a", lg)
	if err != nil {
		t.Fatalf("late rewrite: %v", err)
	}
	if outcome != IngestSessionAlreadyEnded {
		t.Fatalf("a rewrite racing behind its own close must be AlreadyEnded, got %v", outcome)
	}
	if c := activeSessionCount(t, ingA, node, pid); c != 0 {
		t.Fatalf("a dead publisher must not be resurrected as an active session, got %d active", c)
	}

	// 3. A genuine reconnect that started AFTER the close is NOT blocked by the older tombstone.
	id, outcome, err := CreateIngestSession(ctx, ingA, node, stream, pid, "uuid-reconnect", 6000, nil, "cell-a", lg)
	if err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	if outcome != IngestSessionActive || id == "" {
		t.Fatalf("a later reconnect must not be blocked by an older close tombstone, got outcome=%v id=%q", outcome, id)
	}

	// 4. The TTL purge sweeps the tombstone (olderThan=0 → anything created before now).
	if _, err := PurgeExpiredCloseTombstones(ctx, 0); err != nil {
		t.Fatalf("purge: %v", err)
	}
	if c := tombstoneCount(t, ingA, node, pid, stream); c != 0 {
		t.Fatalf("tombstone not purged, %d remain", c)
	}
}

// A close tombstone for the newer connector must deny that connector without restoring the stale
// same-PID incumbent. Retirement, DVR stop, and offline projection are one committed decision.
func TestIngestCloseTombstoneRetiresPIDReuseIncumbent_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })
	ctx := context.Background()
	lg := logging.NewLogger()

	const node, stream = "node-cbt-reuse", "live+cbt-reuse"
	const pid int64 = 101
	incumbent, outcome, err := CreateIngestSession(ctx, ingA, node, stream, pid, "uuid-incumbent", 1000, nil, "cell-a", lg)
	if err != nil || outcome != IngestSessionActive || incumbent == "" {
		t.Fatalf("seed incumbent: id=%q outcome=%v err=%v", incumbent, outcome, err)
	}
	insertDVR(t, "cbt-reuse-dvr", ingA, stream, incumbent)
	if _, err := db.Exec(`UPDATE foghorn.artifacts SET status='recording' WHERE artifact_hash='cbt-reuse-dvr'`); err != nil {
		t.Fatalf("mark incumbent DVR recording: %v", err)
	}
	if _, err := db.Exec(`INSERT INTO foghorn.ingest_close_tombstones
		(tenant_id, node_id, connector_pid, stream_internal_name, close_unix_millis)
		VALUES ($1::uuid, $2, $3, $4, 3000)`, ingA, node, pid, stream); err != nil {
		t.Fatalf("seed successor close tombstone: %v", err)
	}

	_, outcome, err = CreateIngestSession(ctx, ingA, node, stream, pid, "uuid-successor", 2000, nil, "cell-a", lg)
	if err != nil || outcome != IngestSessionAlreadyEnded {
		t.Fatalf("tombstoned successor: outcome=%v err=%v", outcome, err)
	}
	var ended bool
	if err := db.QueryRow(`SELECT ended_at IS NOT NULL FROM foghorn.ingest_sessions WHERE id=$1::uuid`, incumbent).Scan(&ended); err != nil {
		t.Fatalf("read incumbent: %v", err)
	}
	if !ended {
		t.Fatal("tombstoned successor restored the PID-reused incumbent")
	}
	var successorRows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM foghorn.ingest_sessions WHERE tenant_id=$1::uuid AND start_trigger_uuid='uuid-successor'`, ingA).Scan(&successorRows); err != nil {
		t.Fatalf("count successor rows: %v", err)
	}
	if successorRows != 0 {
		t.Fatalf("tombstoned successor rows=%d, want 0", successorRows)
	}
	var dvrStatus string
	if err := db.QueryRow(`SELECT status FROM foghorn.artifacts WHERE artifact_hash='cbt-reuse-dvr'`).Scan(&dvrStatus); err != nil {
		t.Fatalf("read incumbent DVR: %v", err)
	}
	if dvrStatus != "stopping" {
		t.Fatalf("incumbent DVR status=%q, want stopping", dvrStatus)
	}
	var offlineEffects int
	if err := db.QueryRow(`SELECT COUNT(*) FROM foghorn.ingest_offline_effects WHERE source_generation=$1::uuid`, incumbent).Scan(&offlineEffects); err != nil {
		t.Fatalf("count offline effects: %v", err)
	}
	if offlineEffects != 1 {
		t.Fatalf("offline effects=%d, want 1", offlineEffects)
	}
}
