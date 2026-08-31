//go:build schema_verify

package control

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

const (
	ingA = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	ingB = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
)

func activeSessionCount(t *testing.T, tenant, node string, pid int64) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`
		SELECT count(*) FROM foghorn.ingest_sessions
		 WHERE tenant_id=$1::uuid AND node_id=$2 AND connector_pid=$3 AND ended_at IS NULL
	`, tenant, node, pid).Scan(&n); err != nil {
		t.Fatalf("count active: %v", err)
	}
	return n
}

// The live-Postgres proof of the ingest-session identity fence: same-node reconnect,
// idempotent duplicates, event-time PID-reuse fencing, concurrency (partial unique
// index), cross-node distinctness, and tenant isolation — all against the REAL schema
// and the REAL production functions, which sqlmock cannot evaluate.
func TestIngestSessionIdentity_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })
	ctx := context.Background()
	lg := logging.NewLogger()

	// A duplicate PUSH_REWRITE (same trigger UUID) is idempotent: same session id, one active row.
	s1, _, err := CreateIngestSession(ctx, ingA, "node-1", "live+s1", 1234, "uuid-1", 1000, nil, "cell-a", lg)
	if err != nil {
		t.Fatalf("first create: %v", err)
	}
	s1dup, _, err := CreateIngestSession(ctx, ingA, "node-1", "live+s1", 1234, "uuid-1", 1500, nil, "cell-a", lg)
	if err != nil {
		t.Fatalf("dup create: %v", err)
	}
	if s1 != s1dup {
		t.Fatalf("duplicate PUSH_REWRITE minted a new session: %q vs %q", s1, s1dup)
	}
	if c := activeSessionCount(t, ingA, "node-1", 1234); c != 1 {
		t.Fatalf("duplicate should leave exactly one active session, got %d", c)
	}

	// Same-node reconnect: OS reuses the PID for a NEWER connector (new UUID, later start).
	// The stale session is ended and a FRESH generation is minted; only the new one is active.
	s2, _, err := CreateIngestSession(ctx, ingA, "node-1", "live+s1", 1234, "uuid-2", 5000, nil, "cell-a", lg)
	if err != nil {
		t.Fatalf("reconnect create: %v", err)
	}
	if s2 == s1 {
		t.Fatalf("same-node reconnect must mint a fresh generation, reused %q", s1)
	}
	if c := activeSessionCount(t, ingA, "node-1", 1234); c != 1 {
		t.Fatalf("after reconnect exactly one active session, got %d", c)
	}

	// A delayed close for the OLD generation (event time before the NEW session's start)
	// is fenced: it must NOT end the newer active session.
	oldClose, err := FinalizeIngestSessionClose(ctx, ingA, "node-1", 1234, 2000, "live+s1", lg)
	if err != nil {
		t.Fatalf("old close: %v", err)
	}
	if oldClose.EndedSessionID != "" {
		t.Fatalf("a delayed old-generation close must be fenced (return empty), got %q", oldClose.EndedSessionID)
	}
	if c := activeSessionCount(t, ingA, "node-1", 1234); c != 1 {
		t.Fatalf("fenced old close must leave the new session active, got %d active", c)
	}

	// The real close for the new generation (event time after its start) ends it and
	// returns the generation to stop.
	ended, err := FinalizeIngestSessionClose(ctx, ingA, "node-1", 1234, 6000, "live+s1", lg)
	if err != nil {
		t.Fatalf("new close: %v", err)
	}
	if ended.EndedSessionID != s2 {
		t.Fatalf("close should finalize the active generation %q, got %q", s2, ended.EndedSessionID)
	}
	if c := activeSessionCount(t, ingA, "node-1", 1234); c != 0 {
		t.Fatalf("after close no active session, got %d", c)
	}

	// HA single-publisher-per-stream: the SAME stream on a DIFFERENT node is a DUPLICATE — the
	// second node is rejected while the first holds the stream. DIFFERENT
	// streams on different nodes are distinct sessions.
	nodeA, _, err := CreateIngestSession(ctx, ingA, "node-A", "live+s9", 777, "u-a", 100, nil, "cell-a", lg)
	if err != nil {
		t.Fatalf("nodeA: %v", err)
	}
	if _, oc, err := CreateIngestSession(ctx, ingA, "node-B", "live+s9", 778, "u-b", 100, nil, "cell-a", lg); err != nil || oc != IngestSessionRejectedDuplicate {
		t.Fatalf("the same stream on a second node must be RejectedDuplicate, got outcome=%v err=%v", oc, err)
	}
	distinct, _, err := CreateIngestSession(ctx, ingA, "node-B", "live+s9b", 778, "u-b2", 100, nil, "cell-a", lg)
	if err != nil {
		t.Fatalf("distinct stream: %v", err)
	}
	if distinct == nodeA {
		t.Fatalf("a different stream must be a distinct session, got %q", nodeA)
	}

	// Tenant isolation: another tenant with the same (node, pid) is a distinct session,
	// and one tenant's close never ends another tenant's session.
	tA, _, err := CreateIngestSession(ctx, ingA, "node-T", "live+s5", 42, "u", 1, nil, "cell-a", lg)
	if err != nil {
		t.Fatalf("tenant A: %v", err)
	}
	tB, _, err := CreateIngestSession(ctx, ingB, "node-T", "live+s5", 42, "u", 1, nil, "cell-a", lg)
	if err != nil {
		t.Fatalf("tenant B: %v", err)
	}
	if tA == tB {
		t.Fatalf("same (node,pid) across tenants must be distinct, got %q", tA)
	}
	if _, err := FinalizeIngestSessionClose(ctx, ingB, "node-T", 42, 9, "live+s5", lg); err != nil {
		t.Fatalf("tenant B close: %v", err)
	}
	if c := activeSessionCount(t, ingA, "node-T", 42); c != 1 {
		t.Fatalf("tenant A session must survive tenant B's close, got %d active", c)
	}
}

// HA admission, proven against real Postgres: N concurrent admissions for the SAME
// stream on DIFFERENT nodes — the exact two-Foghorn-replica race, since the stream-scoped advisory
// lock is held in the shared per-cell DB — resolve to EXACTLY ONE winner (Active); every other is
// RejectedDuplicate, and the DB holds exactly one active session for the stream. This is what the
// process-local StreamRegistry could not guarantee.
func TestIngestSessionConcurrentCrossNodeAdmission_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })
	ctx := context.Background()
	lg := logging.NewLogger()

	const n = 16
	const stream = "live+ha"
	var wg sync.WaitGroup
	outcomes := make([]IngestSessionOutcome, n)
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			node := "node-" + string(rune('a'+i))
			uuid := "uuid-" + string(rune('a'+i))
			_, outcomes[i], errs[i] = CreateIngestSession(ctx, ingA, node, stream, int64(1000+i), uuid, int64(500+i), nil, "cell-a", lg)
		}(i)
	}
	wg.Wait()

	active := 0
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("goroutine %d errored: %v", i, errs[i])
		}
		switch outcomes[i] {
		case IngestSessionActive:
			active++
		case IngestSessionRejectedDuplicate:
			// expected loser
		default:
			t.Fatalf("goroutine %d: unexpected outcome %v", i, outcomes[i])
		}
	}
	if active != 1 {
		t.Fatalf("exactly ONE cross-node admission for one stream must win, got %d", active)
	}
	var c int
	if err := db.QueryRow(`SELECT count(*) FROM foghorn.ingest_sessions WHERE tenant_id=$1::uuid AND stream_internal_name=$2 AND ended_at IS NULL`, ingA, stream).Scan(&c); err != nil {
		t.Fatalf("count active: %v", err)
	}
	if c != 1 {
		t.Fatalf("the DB must hold exactly one active session per stream, got %d", c)
	}
}

// A genuine concurrent race of duplicate PUSH_REWRITEs for one connection resolves to
// exactly ONE active session (the partial unique index + ON CONFLICT), every caller
// getting that same id — no duplicate active rows, no error.
func TestIngestSessionConcurrentCreate_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })
	ctx := context.Background()
	lg := logging.NewLogger()

	const n = 12
	ids := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ids[i], _, errs[i] = CreateIngestSession(ctx, ingA, "node-race", "live+r", 9001, "uuid-race", 1000, nil, "cell-a", lg)
		}(i)
	}
	wg.Wait()

	first := ""
	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("concurrent create %d errored: %v", i, errs[i])
		}
		if first == "" {
			first = ids[i]
		} else if ids[i] != first {
			t.Fatalf("concurrent creates disagree: %q vs %q", first, ids[i])
		}
	}
	if c := activeSessionCount(t, ingA, "node-race", 9001); c != 1 {
		t.Fatalf("concurrent duplicates must leave exactly one active session, got %d", c)
	}
}

// The close-before-start fence, proven at the SQL layer sqlmock cannot evaluate: the
// requested->starting transition matches zero rows when the bound ingest generation is
// already ended, and matches for an unbound (NULL generation) or still-active row.
func TestDVRCloseBeforeStartFence_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })
	ctx := context.Background()
	lg := logging.NewLogger()

	// Bind a DVR to an ACTIVE generation: the transition must succeed.
	genActive, _, err := CreateIngestSession(ctx, ingA, "node-x", "live+cbs", 1, "u1", 10, nil, "cell-a", lg)
	if err != nil {
		t.Fatalf("active gen: %v", err)
	}
	insertDVR(t, "hash-active", ingA, "live+cbs", genActive)
	if !transitionStarting(t, "hash-active", ingA) {
		t.Fatal("transition must succeed for an active ingest generation")
	}

	// Bind a DVR to an ENDED generation: the fence blocks the transition (0 rows).
	genEnded, _, err := CreateIngestSession(ctx, ingA, "node-y", "live+cbs2", 2, "u2", 10, nil, "cell-a", lg)
	if err != nil {
		t.Fatalf("ended gen: %v", err)
	}
	if _, err := FinalizeIngestSessionClose(ctx, ingA, "node-y", 2, 20, "live+cbs2", lg); err != nil {
		t.Fatalf("end gen: %v", err)
	}
	insertDVR(t, "hash-ended", ingA, "live+cbs2", genEnded)
	if transitionStarting(t, "hash-ended", ingA) {
		t.Fatal("close-before-start fence must block the transition for an ended generation")
	}

	// An unbound DVR (NULL generation) is unaffected by the fence.
	insertDVR(t, "hash-nogen", ingA, "live+cbs3", "")
	if !transitionStarting(t, "hash-nogen", ingA) {
		t.Fatal("a DVR with no bound ingest generation must transition normally")
	}
}

func insertDVR(t *testing.T, hash, tenant, internal, ingestGen string) {
	t.Helper()
	_, err := db.Exec(`
		INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, stream_internal_name, tenant_id, status, dvr_start_dispatch, ingest_generation)
		VALUES ($1, 'dvr', $2, $3::uuid, 'requested', '{"state":"pending"}'::jsonb, NULLIF($4,'')::uuid)
	`, hash, internal, tenant, ingestGen)
	if err != nil {
		t.Fatalf("insert dvr %s: %v", hash, err)
	}
}

// transitionStarting runs the SAME guarded requested->starting SQL the production
// startDVR path uses (including the close-before-start NOT EXISTS fence) and reports
// whether it advanced the row.
func transitionStarting(t *testing.T, hash, tenant string) bool {
	t.Helper()
	res, err := db.Exec(`
		UPDATE foghorn.artifacts
		   SET status = 'starting', updated_at = NOW()
		 WHERE artifact_hash = $1 AND artifact_type = 'dvr' AND tenant_id = $2::uuid AND status = 'requested'
		   AND NOT EXISTS (
		       SELECT 1 FROM foghorn.ingest_sessions s
		        WHERE s.id = foghorn.artifacts.ingest_generation AND s.ended_at IS NOT NULL
		   )
	`, hash, tenant)
	if err != nil {
		t.Fatalf("transition %s: %v", hash, err)
	}
	n, _ := res.RowsAffected()
	return n == 1
}

// The ingest-session schema invariants proven against real Postgres: the composite FK
// rejects a DVR bound to a nonexistent or cross-tenant generation, the partial unique
// index rejects a second active DVR for one generation, and the CHECK constraints
// reject an unidentifiable session.
func TestIngestSessionSchemaInvariants_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })
	ctx := context.Background()
	lg := logging.NewLogger()

	// FK: a DVR bound to a nonexistent generation is rejected.
	_, err := db.Exec(`
		INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, stream_internal_name, tenant_id, status, dvr_start_dispatch, ingest_generation)
		VALUES ('h-badgen', 'dvr', 'live+x', $1::uuid, 'requested', '{}'::jsonb, gen_random_uuid())
	`, ingA)
	if err == nil {
		t.Fatal("FK must reject a DVR bound to a nonexistent ingest generation")
	}

	// FK: a DVR bound to another tenant's generation is rejected.
	gen, _, err := CreateIngestSession(ctx, ingA, "node-fk", "live+fk", 5, "u", 10, nil, "cell-a", lg)
	if err != nil {
		t.Fatalf("create gen: %v", err)
	}
	_, err = db.Exec(`
		INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, stream_internal_name, tenant_id, status, dvr_start_dispatch, ingest_generation)
		VALUES ('h-crosstenant', 'dvr', 'live+fk', $1::uuid, 'requested', '{}'::jsonb, $2::uuid)
	`, ingB, gen) // tenant ingB binding tenant ingA's generation
	if err == nil {
		t.Fatal("FK must reject a DVR binding another tenant's ingest generation")
	}

	// Unique index: two ACTIVE DVRs for one generation are rejected.
	if _, err := db.Exec(`
		INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, stream_internal_name, tenant_id, status, dvr_start_dispatch, ingest_generation)
		VALUES ('h-dvr1', 'dvr', 'live+fk', $1::uuid, 'recording', '{}'::jsonb, $2::uuid)
	`, ingA, gen); err != nil {
		t.Fatalf("first active DVR insert: %v", err)
	}
	if _, err := db.Exec(`
		INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, stream_internal_name, tenant_id, status, dvr_start_dispatch, ingest_generation)
		VALUES ('h-dvr2', 'dvr', 'live+fk', $1::uuid, 'starting', '{}'::jsonb, $2::uuid)
	`, ingA, gen); err == nil {
		t.Fatal("a second ACTIVE DVR for one generation must be rejected")
	}
	// A terminal row for the same generation is allowed (the rejected/failed history).
	if _, err := db.Exec(`
		INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, stream_internal_name, tenant_id, status, dvr_start_dispatch, ingest_generation)
		VALUES ('h-dvr-terminal', 'dvr', 'live+fk', $1::uuid, 'failed', '{}'::jsonb, $2::uuid)
	`, ingA, gen); err != nil {
		t.Fatalf("a terminal DVR for the same generation must be allowed: %v", err)
	}

	// CHECK constraints: an unidentifiable session is rejected at the row level.
	for _, bad := range []struct {
		name string
		pid  int64
		uuid string
		ms   int64
	}{
		{"zero pid", 0, "u", 1},
		{"empty uuid", 5, "", 1},
		{"zero millis", 5, "u", 0},
	} {
		if _, err := db.Exec(`
			INSERT INTO foghorn.ingest_sessions (tenant_id, node_id, stream_internal_name, connector_pid, start_trigger_uuid, started_at_unix_millis)
			VALUES ($1::uuid, 'n', 's', $2, $3, $4)
		`, ingA, bad.pid, bad.uuid, bad.ms); err == nil {
			t.Fatalf("CHECK must reject %s", bad.name)
		}
	}
}

// The generation-scoped duplicate re-check: with a bound generation the
// re-check matches only the SAME generation's active row, so a same-node reconnect
// (same source node, new generation) does NOT adopt the prior recording.
func TestDVRRecheckGenerationScoped_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })
	ctx := context.Background()
	lg := logging.NewLogger()

	genOld, _, err := CreateIngestSession(ctx, ingA, "node-same", "live+rc", 100, "u-old", 10, nil, "cell-a", lg)
	if err != nil {
		t.Fatalf("old gen: %v", err)
	}
	// The prior session's active recording, on this same source node.
	if _, err := db.Exec(`
		INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, stream_internal_name, tenant_id, status, dvr_start_dispatch, ingest_generation)
		VALUES ('h-old', 'dvr', 'live+rc', $1::uuid, 'recording', $2::jsonb, $3::uuid)
	`, ingA, `{"source_node_id":"node-same"}`, genOld); err != nil {
		t.Fatalf("insert old recording: %v", err)
	}
	// The old session ends (its close), THEN a same-node reconnect mints a new generation — the DB
	// stream authority rejects a reconnect while the incumbent is still active, so a real reconnect
	// only happens after the incumbent is ended.
	if _, err := db.Exec(`UPDATE foghorn.ingest_sessions SET ended_at=NOW(), ended_at_unix_millis=4000 WHERE id=$1::uuid`, genOld); err != nil {
		t.Fatalf("end old gen: %v", err)
	}
	genNew, _, err := CreateIngestSession(ctx, ingA, "node-same", "live+rc", 200, "u-new", 5000, nil, "cell-a", lg)
	if err != nil {
		t.Fatalf("new gen: %v", err)
	}

	recheck := func(gen string) (string, bool) {
		var hash, status string
		err := db.QueryRowContext(ctx, `
			SELECT artifact_hash, status FROM foghorn.artifacts
			WHERE stream_internal_name=$1 AND artifact_type='dvr'
			      AND status IN ('requested','starting','recording')
			      AND tenant_id=$2
			      AND (
			          ($4 <> '' AND ingest_generation = $4::uuid)
			          OR ($4 = '' AND dvr_start_dispatch->>'source_node_id'=$3)
			      )
			ORDER BY created_at DESC LIMIT 1
		`, "live+rc", ingA, "node-same", gen).Scan(&hash, &status)
		if err != nil {
			return "", false
		}
		return hash, true
	}

	// Keyed by the NEW generation: no match → the reconnect records fresh (not adopted).
	if h, found := recheck(genNew); found {
		t.Fatalf("same-node reconnect (new generation) must NOT match the old recording, matched %q", h)
	}
	// Keyed by the OLD generation: matches the prior recording (a true retry/duplicate).
	if h, found := recheck(genOld); !found || h != "h-old" {
		t.Fatalf("re-check for the old generation must match its own recording, got %q found=%v", h, found)
	}
}

// The durable DVR-intent recovery seed proven against real Postgres: a
// record:true session that carries an intent but has no bound DVR artifact is listed
// (older than the grace); a session whose recording exists, or one with no intent, is not.
func TestListUnstartedDVRIntents_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })
	ctx := context.Background()
	lg := logging.NewLogger()

	intent := []byte(`{"tenantId":"` + ingA + `","internalName":"live+i"}`)

	// (1) record:true session, no artifact yet → an unstarted intent.
	genPending, _, err := CreateIngestSession(ctx, ingA, "node-i", "live+i", 10, "u", 1, intent, "cell-a", lg)
	if err != nil {
		t.Fatalf("pending intent session: %v", err)
	}
	// (2) record:true session whose recording already exists → NOT unstarted.
	genStarted, _, err := CreateIngestSession(ctx, ingA, "node-i", "live+j", 11, "u", 1, intent, "cell-a", lg)
	if err != nil {
		t.Fatalf("started intent session: %v", err)
	}
	insertDVR(t, "hash-j", ingA, "live+j", genStarted)
	// (3) non-recording session (no intent) → NOT unstarted.
	if _, _, err := CreateIngestSession(ctx, ingA, "node-i", "live+k", 12, "u", 1, nil, "cell-a", lg); err != nil {
		t.Fatalf("non-recording session: %v", err)
	}

	// Backdate all sessions past the grace so the scan picks them up.
	if _, err := db.Exec(`UPDATE foghorn.ingest_sessions SET started_at = NOW() - INTERVAL '1 hour'`); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	got, err := ClaimUnstartedDVRIntents(ctx, time.Minute, 50)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("expected exactly one unstarted intent, got %d: %+v", len(got), got)
	}
	if got[0].SessionID != genPending {
		t.Fatalf("expected the pending session %q, got %q", genPending, got[0].SessionID)
	}
	if got[0].NodeID != "node-i" || string(got[0].Intent) == "" || got[0].Attempts != 1 {
		t.Fatalf("claimed intent missing node/blob/attempt: %+v", got[0])
	}

	// The claim LEASED the row: an immediate re-claim excludes it (HA-safe / backoff).
	if again, err := ClaimUnstartedDVRIntents(ctx, time.Minute, 50); err != nil {
		t.Fatalf("re-claim: %v", err)
	} else if len(again) != 0 {
		t.Fatalf("a leased intent must not be re-claimed until the lease lapses, got %d", len(again))
	}

	// A terminal error (FailDVRIntent) permanently excludes an intent from the claim.
	if err := FailDVRIntent(ctx, ingA, genPending, "undecodable"); err != nil {
		t.Fatalf("fail intent: %v", err)
	}
	if _, err := db.Exec(`UPDATE foghorn.ingest_sessions SET dvr_intent_lease_until = NULL`); err != nil {
		t.Fatalf("expire lease: %v", err)
	}
	if after, err := ClaimUnstartedDVRIntents(ctx, time.Minute, 50); err != nil {
		t.Fatalf("claim after fail: %v", err)
	} else if len(after) != 0 {
		t.Fatalf("a terminally-errored intent must never be re-claimed, got %d", len(after))
	}

	// The grace excludes a just-created intent (fast path gets first crack).
	if _, _, err := CreateIngestSession(ctx, ingA, "node-i", "live+fresh", 13, "u", 1, intent, "cell-a", lg); err != nil {
		t.Fatalf("fresh session: %v", err)
	}
	fresh, err := ClaimUnstartedDVRIntents(ctx, time.Hour, 50)
	if err != nil {
		t.Fatalf("claim fresh: %v", err)
	}
	for _, it := range fresh {
		if it.InternalName == "live+fresh" {
			t.Fatal("a within-grace intent must NOT be claimed yet")
		}
	}
}

// PID-reuse / stale-trigger handling and the claim-before-send active-status guard, proven
// against real Postgres.
func TestIngestSessionReuseAndStopClaim_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })
	ctx := context.Background()
	lg := logging.NewLogger()

	// Same trigger UUID but a DIFFERENT stream is an anomaly → RejectedDuplicate (fail closed).
	if _, _, err := CreateIngestSession(ctx, ingA, "node-r", "live+one", 50, "u-same", 100, nil, "cell-a", lg); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, oc, err := CreateIngestSession(ctx, ingA, "node-r", "live+TWO", 50, "u-same", 200, nil, "cell-a", lg); err != nil || oc != IngestSessionRejectedDuplicate {
		t.Fatalf("same UUID on a different stream must be RejectedDuplicate, got outcome=%v err=%v", oc, err)
	}

	// A different-UUID OLDER trigger for the same reused (node, PID) must be rejected, not adopt the
	// active session (only a NEWER same-(node,PID) trigger supersedes).
	active, _, err := CreateIngestSession(ctx, ingA, "node-r2", "live+s", 60, "u-new", 5000, nil, "cell-a", lg)
	if err != nil {
		t.Fatalf("active: %v", err)
	}
	if _, oc, err := CreateIngestSession(ctx, ingA, "node-r2", "live+s", 60, "u-old", 1000, nil, "cell-a", lg); err != nil || oc != IngestSessionRejectedDuplicate {
		t.Fatalf("a stale (older, different-UUID) reordered trigger must be RejectedDuplicate, got outcome=%v err=%v", oc, err)
	}
	if c := activeSessionCount(t, ingA, "node-r2", 60); c != 1 {
		t.Fatalf("the active session must survive a rejected stale trigger, got %d", c)
	}

	// A newer connector reusing the PID ends the stale session AND claims its orphaned
	// DVR's stop obligation atomically.
	insertDVR(t, "dvr-orphan", ingA, "live+s", active) // bound to the soon-to-be-stale session
	if _, err := db.Exec(`UPDATE foghorn.artifacts SET status='recording' WHERE artifact_hash='dvr-orphan'`); err != nil {
		t.Fatalf("mark recording: %v", err)
	}
	fresh, _, err := CreateIngestSession(ctx, ingA, "node-r2", "live+s", 60, "u-newer", 9000, nil, "cell-a", lg)
	if err != nil {
		t.Fatalf("reuse: %v", err)
	}
	if fresh == active {
		t.Fatal("PID reuse must mint a fresh session")
	}
	var orphanStatus string
	if err := db.QueryRow(`SELECT status FROM foghorn.artifacts WHERE artifact_hash='dvr-orphan'`).Scan(&orphanStatus); err != nil {
		t.Fatalf("read orphan: %v", err)
	}
	if orphanStatus != "stopping" {
		t.Fatalf("the stale session's orphaned DVR must be claimed to 'stopping', got %q", orphanStatus)
	}

	// (1) ClaimDVRStops active-status guard: a terminal row is NOT re-claimed.
	insertDVR(t, "dvr-terminal", ingA, "live+s", fresh)
	if _, err := db.Exec(`UPDATE foghorn.artifacts SET status='completed' WHERE artifact_hash='dvr-terminal'`); err != nil {
		t.Fatalf("terminalize: %v", err)
	}
	claims, err := ClaimDVRStops(ctx, db, `artifact_hash = $1 AND tenant_id::text = $2`, "dvr-terminal", ingA)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if len(claims) != 0 {
		t.Fatalf("a terminal DVR must not be re-claimed to stopping, got %+v", claims)
	}
	var termStatus string
	if err := db.QueryRow(`SELECT status FROM foghorn.artifacts WHERE artifact_hash='dvr-terminal'`).Scan(&termStatus); err != nil {
		t.Fatalf("read terminal: %v", err)
	}
	if termStatus != "completed" {
		t.Fatalf("a terminal DVR must stay terminal, got %q", termStatus)
	}
}

// Concurrent DIFFERENT-session triggers for one (tenant,node,pid) — a mix of
// distinct UUIDs and start times racing — are serialized by the advisory lock, so the full
// identity comparison runs against each committed predecessor. The result is exactly ONE
// active session, and it is the NEWEST (highest start time) — no borrowing the wrong
// generation, no duplicate active rows.
func TestIngestSessionConcurrentDifferentSessions_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })
	ctx := context.Background()
	lg := logging.NewLogger()

	const n = 16
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			// Each goroutine is a distinct connection (distinct UUID) with a distinct start
			// time; they race on the same (tenant, node, pid).
			uuid := "uuid-" + string(rune('a'+i))
			millis := int64(1000 + i*100)
			_, _, errs[i] = CreateIngestSession(ctx, ingA, "node-diff", "live+d", 4242, uuid, millis, nil, "cell-a", lg)
		}(i)
	}
	wg.Wait()

	// Some late-arriving older triggers may be rejected (stale reordered) — that's expected and
	// not a failure. What must hold: exactly one ACTIVE session survives.
	if c := activeSessionCount(t, ingA, "node-diff", 4242); c != 1 {
		t.Fatalf("concurrent different-session race must leave exactly ONE active session, got %d", c)
	}
	// And the survivor is the newest start time (2500 = 1000 + 15*100).
	var survivorMillis int64
	if err := db.QueryRow(`
		SELECT started_at_unix_millis FROM foghorn.ingest_sessions
		 WHERE tenant_id=$1::uuid AND node_id='node-diff' AND connector_pid=4242 AND ended_at IS NULL
	`, ingA).Scan(&survivorMillis); err != nil {
		t.Fatalf("read survivor: %v", err)
	}
	if survivorMillis != int64(1000+(n-1)*100) {
		t.Fatalf("the surviving active session must be the newest (%d), got %d", 1000+(n-1)*100, survivorMillis)
	}
}

// The advisory-lock keys must be valid PostgreSQL text because hashtext rejects NUL bytes. This
// executes the same lock SQL as the DVR and ingest-session paths against PostgreSQL.
func TestAdvisoryLockKeysAreValidPGText_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })
	ctx := context.Background()

	// DVRStartLockKey is the key every startDVR call uses.
	if _, err := conn.ExecContext(ctx, `SELECT pg_advisory_xact_lock(hashtext($1)::bigint)`,
		DVRStartLockKey("live+stream-1", "node-1")); err != nil {
		t.Fatalf("DVRStartLockKey produced an invalid PG advisory-lock key: %v", err)
	}
	if got := DVRStartLockKey("live+stream-1", "node-1"); strings.ContainsRune(got, 0) {
		t.Fatalf("DVRStartLockKey still contains a NUL byte: %q", got)
	}
	// The CreateIngestSession identity mints a real session (which acquires its own lock).
	if _, _, err := CreateIngestSession(ctx, ingA, "node-lock", "live+lk", 7, "u", 1, nil, "demo-media", logging.NewLogger()); err != nil {
		t.Fatalf("CreateIngestSession advisory lock failed against real PG: %v", err)
	}
}

// insertDVRForSource inserts a recording DVR whose dispatch names a source node, bound to a
// given ingest generation ("" = NULL), so StopDVRForEndedSource's generation fence can be
// exercised against the real schema.
func insertDVRForSource(t *testing.T, hash, tenant, internal, sourceNode, ingestGen string) {
	t.Helper()
	// node_id is left empty so DispatchDVRStops skips the best-effort gRPC send (unwired in
	// this harness); the fence under test is the durable CLAIM (status→stopping), not the send.
	_, err := db.Exec(`
		INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, stream_internal_name, tenant_id, status, dvr_start_dispatch, ingest_generation)
		VALUES ($1, 'dvr', $2, $3::uuid, 'recording',
		        jsonb_build_object('state','recording','source_node_id',$4::text,'node_id',''),
		        NULLIF($5,'')::uuid)
	`, hash, internal, tenant, sourceNode, ingestGen)
	if err != nil {
		t.Fatalf("insert dvr %s: %v", hash, err)
	}
}

func dvrStatus(t *testing.T, hash string) string {
	t.Helper()
	var s string
	if err := db.QueryRow(`SELECT status FROM foghorn.artifacts WHERE artifact_hash=$1`, hash).Scan(&s); err != nil {
		t.Fatalf("read status %s: %v", hash, err)
	}
	return s
}

// STREAM_END/vanish claim stop obligations node-locally, but a DVR bound to a
// STILL-ACTIVE ingest generation (a live reconnect that already re-minted on this node) must
// NOT be stopped. StopDVRForEndedSource fences on the bound generation: it stops DVRs whose
// generation is ENDED or NULL and leaves an active-generation DVR running.
func TestStopDVRForEndedSourceGenerationFence_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })
	ctx := context.Background()
	lg := logging.NewLogger()
	const node, stream = "node-fence", "live+fence"

	// An ENDED generation on this node, and a LIVE one (a reconnect).
	ended, _, err := CreateIngestSession(ctx, ingA, node, stream, 71, "u-ended", 1000, nil, "cell-a", lg)
	if err != nil {
		t.Fatalf("ended session: %v", err)
	}
	if _, err := db.Exec(`UPDATE foghorn.ingest_sessions SET ended_at=NOW() WHERE id=$1::uuid`, ended); err != nil {
		t.Fatalf("end session: %v", err)
	}
	live, _, err := CreateIngestSession(ctx, ingA, node, stream, 72, "u-live", 2000, nil, "cell-a", lg)
	if err != nil {
		t.Fatalf("live session: %v", err)
	}

	insertDVRForSource(t, "dvr-ended", ingA, stream, node, ended) // bound to ENDED → must stop
	insertDVRForSource(t, "dvr-live", ingA, stream, node, live)   // bound to ACTIVE → must NOT stop
	insertDVRForSource(t, "dvr-null", ingA, stream, node, "")     // unbound → must stop

	if err := StopDVRForEndedSource(stream, ingA, node, lg); err != nil {
		t.Fatalf("StopDVRForEndedSource: %v", err)
	}

	if got := dvrStatus(t, "dvr-ended"); got != "stopping" {
		t.Fatalf("DVR bound to an ended generation must be stopped, got %q", got)
	}
	if got := dvrStatus(t, "dvr-null"); got != "stopping" {
		t.Fatalf("unbound DVR must be stopped, got %q", got)
	}
	if got := dvrStatus(t, "dvr-live"); got != "recording" {
		t.Fatalf("DVR bound to a LIVE generation must NOT be stopped by an aggregate STREAM_END, got %q", got)
	}
}

// A PUSH_REWRITE retry that races its OWN PUSH_INPUT_CLOSE and arrives AFTER the close ended the
// session must be reported as AlreadyEnded (returning the ended id), NOT resurrected as a fresh
// active session — and the same trigger UUID on a DIFFERENT stream is RejectedDuplicate (fail
// closed), enforced by the (tenant, node, UUID) uniqueness.
func TestIngestSessionAlreadyEndedIdempotency_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })
	ctx := context.Background()
	lg := logging.NewLogger()
	const node = "node-ended"

	sid, outcome, err := CreateIngestSession(ctx, ingA, node, "live+ea", 500, "uuid-e", 1000, nil, "cell-a", lg)
	if err != nil || outcome != IngestSessionActive {
		t.Fatalf("mint: id=%q outcome=%v err=%v", sid, outcome, err)
	}
	fin, err := FinalizeIngestSessionClose(ctx, ingA, node, 500, 2000, "live+ea", lg)
	if err != nil || fin.EndedSessionID != sid {
		t.Fatalf("close: %+v err=%v", fin, err)
	}

	// A retry of the SAME trigger on the SAME stream after its close → AlreadyEnded, returning sid.
	gotID, outcome, err := CreateIngestSession(ctx, ingA, node, "live+ea", 500, "uuid-e", 3000, nil, "cell-a", lg)
	if err != nil {
		t.Fatalf("retry after close: %v", err)
	}
	if outcome != IngestSessionAlreadyEnded {
		t.Fatalf("a retry after its own close must report AlreadyEnded, got %v", outcome)
	}
	if gotID != sid {
		t.Fatalf("AlreadyEnded must return the ended session id %q, got %q", sid, gotID)
	}
	if c := activeSessionCount(t, ingA, node, 500); c != 0 {
		t.Fatalf("an already-ended retry must NOT resurrect an active session, got %d", c)
	}

	// F6 (schema uniqueness): a Mist trigger UUID is unique per (tenant, node) connection, so the
	// SAME UUID on a DIFFERENT stream is an anomaly — RejectedDuplicate (fail closed), NOT a fresh
	// mint. Enforced by uq_foghorn_ingest_sessions_trigger_uuid + the UUID-first admission lookup.
	_, outcome, err = CreateIngestSession(ctx, ingA, node, "live+eb", 500, "uuid-e", 4000, nil, "cell-a", lg)
	if err != nil {
		t.Fatalf("different-stream: %v", err)
	}
	if outcome != IngestSessionRejectedDuplicate {
		t.Fatalf("same UUID on a different stream must be RejectedDuplicate (fail closed), got %v", outcome)
	}
}

// STREAM_END reaper, proven against real Postgres: STREAM_END ends a session whose
// close was LOST (event-time fenced), so admission is no longer wedged; but a reconnect that came up
// AFTER the STREAM_END event is preserved (its session started later).
func TestEndIngestSessionsForStreamEnd_ReapsLostCloseFencedByEventTime_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })
	ctx := context.Background()
	lg := logging.NewLogger()
	const node, stream = "node-reap", "live+reap"

	// A session whose PUSH_INPUT_CLOSE was LOST — still active, started at t=100.
	s1, _, err := CreateIngestSession(ctx, ingA, node, stream, 10, "u-s1", 100, nil, "cell-a", lg)
	if err != nil {
		t.Fatalf("s1: %v", err)
	}
	// STREAM_END fires with event time 200 (>= s1 start) → reaps s1.
	reaped, err := EndIngestSessionsForStreamEnd(ctx, ingA, node, stream, 200, lg)
	if err != nil {
		t.Fatalf("reap: %v", err)
	}
	if reaped != 1 {
		t.Fatalf("STREAM_END must reap the lost-close session, reaped %d", reaped)
	}
	if c := activeSessionCount(t, ingA, node, 10); c != 0 {
		t.Fatalf("the reaped session must be ended, still %d active", c)
	}
	// The stream is now free — a reconnect that came up AFTER the STREAM_END event mints (started at
	// t=250). Admission is no longer wedged.
	s2, oc, err := CreateIngestSession(ctx, ingA, node, stream, 20, "u-s2", 250, nil, "cell-a", lg)
	if err != nil || oc != IngestSessionActive {
		t.Fatalf("reconnect after reap must be admitted, got outcome=%v err=%v", oc, err)
	}
	if s2 == s1 {
		t.Fatal("reconnect must be a fresh session")
	}
	// A DUPLICATE/late STREAM_END with the OLD event time (200) must NOT reap the reconnect (started
	// 250 > 200) — the event-time fence preserves it.
	reaped, err = EndIngestSessionsForStreamEnd(ctx, ingA, node, stream, 200, lg)
	if err != nil {
		t.Fatalf("late reap: %v", err)
	}
	if reaped != 0 {
		t.Fatalf("a late STREAM_END must NOT reap a reconnect started after its event, reaped %d", reaped)
	}
	if c := activeSessionCount(t, ingA, node, 20); c != 1 {
		t.Fatalf("the reconnect must remain active after a late STREAM_END, got %d", c)
	}

	// eventMillis <= 0 (old Mist / missing header) is a no-op.
	if reaped, err := EndIngestSessionsForStreamEnd(ctx, ingA, node, stream, 0, lg); err != nil || reaped != 0 {
		t.Fatalf("missing event time must be a no-op, reaped=%d err=%v", reaped, err)
	}
}

func TestIngestSessionCapacityAuthority_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })
	ctx := context.Background()
	lg := logging.NewLogger()
	limit := IngestAuthoritySnapshot{CapacityMaxStreams: 1}

	first, outcome, err := CreateIngestSession(ctx, ingA, "cap-node-a", "live+cap-a", 8101, "cap-a", 1000, nil, "cell-a", lg, limit)
	if err != nil || outcome != IngestSessionActive || first == "" {
		t.Fatalf("first capped admission: id=%q outcome=%v err=%v", first, outcome, err)
	}
	duplicate, duplicateOutcome, duplicateErr := CreateIngestSession(ctx, ingA, "cap-node-a", "live+cap-a", 8101, "cap-a", 1000, nil, "cell-a", lg, limit)
	if duplicateErr != nil || duplicateOutcome != IngestSessionActive || duplicate != first {
		t.Fatalf("idempotent capped admission: id=%q outcome=%v err=%v", duplicate, duplicateOutcome, duplicateErr)
	}
	denied, deniedOutcome, deniedErr := CreateIngestSession(ctx, ingA, "cap-node-b", "live+cap-b", 8102, "cap-b", 1100, nil, "cell-a", lg, limit)
	if deniedErr != nil || deniedOutcome != IngestSessionRejectedCapacity || denied != "" {
		t.Fatalf("second capped admission: id=%q outcome=%v err=%v", denied, deniedOutcome, deniedErr)
	}

	if _, closeErr := FinalizeIngestSessionClose(ctx, ingA, "cap-node-a", 8101, 2000, "live+cap-a", lg); closeErr != nil {
		t.Fatalf("close first capped session: %v", closeErr)
	}
	second, secondOutcome, secondErr := CreateIngestSession(ctx, ingA, "cap-node-b", "live+cap-b", 8102, "cap-b-2", 2100, nil, "cell-a", lg, limit)
	if secondErr != nil || secondOutcome != IngestSessionActive || second == "" {
		t.Fatalf("admission after capacity release: id=%q outcome=%v err=%v", second, secondOutcome, secondErr)
	}

	// Different streams for one tenant race through distinct stream locks. The
	// tenant-capacity advisory lock must nevertheless serialize the count and
	// insert so exactly one admission consumes the single slot.
	type result struct {
		id      string
		outcome IngestSessionOutcome
		err     error
	}
	start := make(chan struct{})
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for i, candidate := range []struct {
		node, stream, uuid string
		pid                int64
	}{
		{node: "cap-race-node-a", stream: "live+cap-race-a", uuid: "cap-race-a", pid: 8201},
		{node: "cap-race-node-b", stream: "live+cap-race-b", uuid: "cap-race-b", pid: 8202},
	} {
		wg.Add(1)
		go func(i int, candidate struct {
			node, stream, uuid string
			pid                int64
		}) {
			defer wg.Done()
			<-start
			id, outcome, err := CreateIngestSession(
				ctx, ingB, candidate.node, candidate.stream, candidate.pid,
				candidate.uuid, int64(3000+i), nil, "cell-a", lg, limit,
			)
			results <- result{id: id, outcome: outcome, err: err}
		}(i, candidate)
	}
	close(start)
	wg.Wait()
	close(results)

	active, rejected := 0, 0
	for got := range results {
		if got.err != nil {
			t.Fatalf("concurrent capped admission: %v", got.err)
		}
		switch got.outcome {
		case IngestSessionActive:
			active++
			if got.id == "" {
				t.Fatal("active concurrent admission returned an empty session id")
			}
		case IngestSessionRejectedCapacity:
			rejected++
			if got.id != "" {
				t.Fatalf("capacity rejection returned session id %q", got.id)
			}
		default:
			t.Fatalf("unexpected concurrent admission outcome %v", got.outcome)
		}
	}
	if active != 1 || rejected != 1 {
		t.Fatalf("cap=1 concurrent admissions: active=%d rejected=%d", active, rejected)
	}
}

// Capacity rejection must be atomic with PID-reuse handling. A newer connector on the same
// (node, PID) proves the incumbent generation is stale even when a downgraded tenant cannot admit
// the replacement. The stale generation must retire with durable cleanup; it must not be restored.
func TestIngestSessionCapacityRejectionRetiresPIDReuseIncumbent_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })
	ctx := context.Background()
	lg := logging.NewLogger()

	// Seed two sessions without a cap, modelling a later plan downgrade to one stream.
	if _, outcome, err := CreateIngestSession(ctx, ingA, "cap-retained-node", "live+cap-retained", 8301, "cap-retained", 1000, nil, "cell-a", lg); err != nil || outcome != IngestSessionActive {
		t.Fatalf("seed retained session: outcome=%v err=%v", outcome, err)
	}
	incumbent, outcome, err := CreateIngestSession(ctx, ingA, "cap-reuse-node", "live+cap-reuse", 8302, "cap-reuse-old", 1100, nil, "cell-a", lg)
	if err != nil || outcome != IngestSessionActive || incumbent == "" {
		t.Fatalf("seed reuse incumbent: id=%q outcome=%v err=%v", incumbent, outcome, err)
	}
	insertDVR(t, "cap-reuse-dvr", ingA, "live+cap-reuse", incumbent)
	if _, err := db.Exec(`UPDATE foghorn.artifacts SET status='recording' WHERE artifact_hash='cap-reuse-dvr'`); err != nil {
		t.Fatalf("mark incumbent DVR recording: %v", err)
	}

	denied, deniedOutcome, deniedErr := CreateIngestSession(
		ctx, ingA, "cap-reuse-node", "live+cap-reuse", 8302, "cap-reuse-new", 2100,
		nil, "cell-a", lg, IngestAuthoritySnapshot{CapacityMaxStreams: 1},
	)
	if deniedErr != nil || deniedOutcome != IngestSessionRejectedCapacity || denied != "" {
		t.Fatalf("PID-reuse capacity rejection: id=%q outcome=%v err=%v", denied, deniedOutcome, deniedErr)
	}

	var incumbentEnded bool
	if err := db.QueryRow(`SELECT ended_at IS NOT NULL FROM foghorn.ingest_sessions WHERE id=$1::uuid`, incumbent).Scan(&incumbentEnded); err != nil {
		t.Fatalf("read incumbent: %v", err)
	}
	if !incumbentEnded {
		t.Fatal("capacity rejection resurrected the PID-reused incumbent")
	}
	var newRows int
	if err := db.QueryRow(`SELECT COUNT(*) FROM foghorn.ingest_sessions WHERE tenant_id=$1::uuid AND start_trigger_uuid='cap-reuse-new'`, ingA).Scan(&newRows); err != nil {
		t.Fatalf("count rejected successor: %v", err)
	}
	if newRows != 0 {
		t.Fatalf("capacity-rejected successor rows=%d, want 0", newRows)
	}
	var dvrStatus string
	if err := db.QueryRow(`SELECT status FROM foghorn.artifacts WHERE artifact_hash='cap-reuse-dvr'`).Scan(&dvrStatus); err != nil {
		t.Fatalf("read incumbent DVR: %v", err)
	}
	if dvrStatus != "stopping" {
		t.Fatalf("capacity rejection left incumbent DVR in %q, want stopping", dvrStatus)
	}
	var offlineEffects int
	if err := db.QueryRow(`SELECT COUNT(*) FROM foghorn.ingest_offline_effects WHERE source_generation=$1::uuid`, incumbent).Scan(&offlineEffects); err != nil {
		t.Fatalf("count incumbent offline effects: %v", err)
	}
	if offlineEffects != 1 {
		t.Fatalf("capacity rejection offline effects=%d, want 1", offlineEffects)
	}
}
