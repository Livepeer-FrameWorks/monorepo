//go:build schema_verify

package control

import (
	"context"
	"database/sql"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	_ "github.com/lib/pq"
)

const realPGImage = "pgvector/pgvector:pg15"

// startRealPG spins up a throwaway Postgres, applies the REAL embedded foghorn.sql baseline (trigger +
// CHECK constraints included), and returns a live *sql.DB. Unlike the cli SQL-smoke harness, this drives
// the ACTUAL production ClaimFreezeAttempt code against the ACTUAL deployed schema, so neither can drift
// from the other undetected.
func startRealPG(t *testing.T) *sql.DB {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("fw-freeze-realpg-%d", time.Now().UnixNano())
	run := func(args ...string) (string, error) {
		out, err := exec.Command("docker", args...).CombinedOutput()
		return string(out), err
	}
	if out, err := run("run", "-d", "--name", name, "-P", "-e", "POSTGRES_PASSWORD=harness", realPGImage); err != nil {
		t.Fatalf("docker run: %v\n%s", err, out)
	}
	t.Cleanup(func() { _, _ = run("rm", "-f", name) })

	portOut, err := run("port", name, "5432/tcp")
	if err != nil {
		t.Fatalf("docker port: %v\n%s", err, portOut)
	}
	// "0.0.0.0:49153\n" (possibly an IPv6 line too) → take the host port.
	port := ""
	for _, line := range strings.Split(strings.TrimSpace(portOut), "\n") {
		if i := strings.LastIndex(line, ":"); i >= 0 {
			port = strings.TrimSpace(line[i+1:])
			break
		}
	}
	if port == "" {
		t.Fatalf("could not parse host port from %q", portOut)
	}

	dsn := fmt.Sprintf("postgres://postgres:harness@127.0.0.1:%s/postgres?sslmode=disable", port)
	conn, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	deadline := time.Now().Add(90 * time.Second)
	for {
		if err := conn.Ping(); err == nil {
			break
		}
		if time.Now().After(deadline) {
			logs, _ := run("logs", "--tail", "40", name)
			t.Fatalf("postgres did not become ready:\n%s", logs)
		}
		time.Sleep(time.Second)
	}

	schema, rerr := dbsql.Content.ReadFile("schema/foghorn.sql")
	if rerr != nil {
		t.Fatalf("read foghorn.sql: %v", rerr)
	}
	if _, err := conn.Exec(string(schema)); err != nil {
		t.Fatalf("apply foghorn.sql: %v", err)
	}
	return conn
}

// TestClaimFreezeAttempt_RealPG drives the ACTUAL ClaimFreezeAttempt production function against a real
// Postgres running the real foghorn.sql schema, proving the properties sqlmock cannot: a genuine
// concurrent race resolves to exactly one winner, an idempotent re-claim with a changed key OR a changed
// cluster loses without mutating the row, and the schema's own CHECK constraints + terminal trigger hold.
func TestClaimFreezeAttempt_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })

	ctx := context.Background()
	const tid = "11111111-1111-1111-1111-111111111111"

	seedReady := func(hash string, nodes []string) {
		if _, err := conn.Exec(`INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, tenant_id, status, sync_status, storage_location)
			VALUES ($1, 'vod', $2::uuid, 'ready', 'pending', 'local')`, hash, tid); err != nil {
			t.Fatalf("seed artifact: %v", err)
		}
		for _, n := range nodes {
			if _, err := conn.Exec(`INSERT INTO foghorn.artifact_nodes (artifact_hash, node_id, is_complete, is_orphaned, cached_at)
				VALUES ($1, $2, true, false, NOW())`, hash, n); err != nil {
				t.Fatalf("seed node copy: %v", err)
			}
		}
	}
	scalar := func(q string, args ...any) string {
		var s sql.NullString
		if err := conn.QueryRow(q, args...).Scan(&s); err != nil {
			t.Fatalf("scalar %q: %v", q, err)
		}
		return s.String
	}

	// --- Concurrent race: 8 nodes, each holding a complete copy, all claim the SAME ready artifact at
	// once. Exactly one must win; the artifact ends in_progress under the winner's identity. ---
	const raceHash = "hash-race"
	nodes := []string{"n0", "n1", "n2", "n3", "n4", "n5", "n6", "n7"}
	seedReady(raceHash, nodes)
	const objKey = "vod/tenant/hash-race/hash-race.mp4"

	var wg sync.WaitGroup
	results := make([]bool, len(nodes))
	errs := make([]error, len(nodes))
	start := make(chan struct{})
	for i, n := range nodes {
		wg.Add(1)
		go func(i int, node string) {
			defer wg.Done()
			<-start // release all goroutines together to maximize the overlap
			results[i], errs[i] = ClaimFreezeAttempt(ctx, conn, raceHash, "req-"+node, node, tid, "", objKey)
		}(i, n)
	}
	close(start)
	wg.Wait()

	wins := 0
	for i := range nodes {
		if errs[i] != nil {
			t.Fatalf("claim from %s errored: %v", nodes[i], errs[i])
		}
		if results[i] {
			wins++
		}
	}
	if wins != 1 {
		t.Fatalf("exactly one concurrent claim must win, got %d winners", wins)
	}
	if got := scalar(`SELECT sync_status FROM foghorn.artifacts WHERE artifact_hash=$1`, raceHash); got != "in_progress" {
		t.Fatalf("artifact must be in_progress after the race, got %q", got)
	}
	winnerNode := scalar(`SELECT sync_node_id FROM foghorn.artifacts WHERE artifact_hash=$1`, raceHash)
	if winnerNode == "" {
		t.Fatal("winner node identity must be recorded")
	}

	// --- Idempotent re-claim: SAME winner req/node, but a CHANGED descriptor loses and leaves the row
	// untouched; a CHANGED cluster loses too; the identical key+cluster wins. ---
	winnerReq := "req-" + winnerNode
	if claimed, err := ClaimFreezeAttempt(ctx, conn, raceHash, winnerReq, winnerNode, tid, "", "vod/tenant/hash-race/CHANGED.mp4"); err != nil || claimed {
		t.Fatalf("re-claim with a changed key must lose: claimed=%v err=%v", claimed, err)
	}
	if got := scalar(`SELECT sync_object_key FROM foghorn.artifacts WHERE artifact_hash=$1`, raceHash); got != objKey {
		t.Fatalf("descriptor must be unchanged after a rejected re-claim, got %q", got)
	}
	if claimed, err := ClaimFreezeAttempt(ctx, conn, raceHash, winnerReq, winnerNode, tid, "some-remote-cluster", objKey); err != nil || claimed {
		t.Fatalf("re-claim with a changed cluster must lose: claimed=%v err=%v", claimed, err)
	}
	if claimed, err := ClaimFreezeAttempt(ctx, conn, raceHash, winnerReq, winnerNode, tid, "", objKey); err != nil || !claimed {
		t.Fatalf("idempotent re-claim with the identical key+cluster must win: claimed=%v err=%v", claimed, err)
	}

	// --- Schema constraints + terminal trigger (real engine). ---
	// (1) chk_active_freeze_state rejects an attempt identity written onto a non-freezing row.
	seedReady("hash-badstate", nil)
	if _, err := conn.Exec(`UPDATE foghorn.artifacts
		SET sync_request_id='r', sync_node_id='n' WHERE artifact_hash='hash-badstate'`); err == nil {
		t.Fatal("chk_active_freeze_state must reject an identity on a non-freezing row")
	} else if !strings.Contains(err.Error(), "chk_foghorn_artifacts_active_freeze_state") {
		t.Fatalf("expected active-freeze-state violation, got %v", err)
	}
	// (2) A terminal transition that keeps the identity is rescued by the trigger (identity cleared),
	// satisfying chk_terminal_no_identity.
	seedReady("hash-del", []string{"nd"})
	if _, err := ClaimFreezeAttempt(ctx, conn, "hash-del", "req-del", "nd", tid, "", "vod/tenant/hash-del/hash-del.mp4"); err != nil {
		t.Fatalf("claim for delete case: %v", err)
	}
	if _, err := conn.Exec(`UPDATE foghorn.artifacts SET status='deleted' WHERE artifact_hash='hash-del'`); err != nil {
		t.Fatalf("delete transition must succeed (trigger clears identity): %v", err)
	}
	if got := scalar(`SELECT COALESCE(sync_request_id,'<null>') FROM foghorn.artifacts WHERE artifact_hash='hash-del'`); got != "<null>" {
		t.Fatalf("terminal trigger must clear the identity, got %q", got)
	}
	if got := scalar(`SELECT sync_object_key FROM foghorn.artifacts WHERE artifact_hash='hash-del'`); got != "vod/tenant/hash-del/hash-del.mp4" {
		t.Fatalf("sync_object_key must be retained on the terminal row, got %q", got)
	}
	// Delete-vs-late-completion: the identity is now cleared, so ANY late completion (which the guard
	// matches on sync_request_id/sync_node_id + non-terminal status) can never match this row — the
	// completion is a guaranteed no-op. Confirm the completion guard's own predicate matches zero rows.
	if got := scalar(`SELECT count(*)::text FROM foghorn.artifacts
		WHERE artifact_hash='hash-del' AND status NOT IN ('deleted','expired','aborted')
		  AND sync_status='in_progress' AND sync_request_id='req-del' AND sync_node_id='nd'`); got != "0" {
		t.Fatalf("a late completion must match no row on the deleted artifact, got count=%q", got)
	}
	// (2b) PRODUCTION delete shape: DeleteVod clears NEW.sync_request_id/sync_node_id in the SAME terminal
	// UPDATE. The trigger WHEN is gated on OLD identity, so it STILL fires and enqueues the abandoned
	// attempt's staging + published-candidate keys before they become underivable.
	seedReady("hash-deldv", []string{"nd2"})
	if _, err := ClaimFreezeAttempt(ctx, conn, "hash-deldv", "req-deldv", "nd2", tid, "", "vod/tenant/hash-deldv/hash-deldv.mp4"); err != nil {
		t.Fatalf("claim for production-delete case: %v", err)
	}
	if _, err := conn.Exec(`UPDATE foghorn.artifacts
		SET status='deleted', sync_request_id=NULL, sync_node_id=NULL, dtsh_sync_request_id=NULL, dtsh_sync_node_id=NULL
		WHERE artifact_hash='hash-deldv'`); err != nil {
		t.Fatalf("production terminal delete must succeed: %v", err)
	}
	// The main attempt can BUNDLE a .dtsh, so the trigger must enqueue ALL FOUR keys it can produce —
	// staging + .dtsh staging + media candidate + .dtsh candidate — mirroring applySyncCompletionFailure.
	// (An earlier version omitted the bundled .dtsh staging key, leaking it.)
	for _, want := range []string{
		FreezeStagingKey("vod/tenant/hash-deldv/hash-deldv.mp4", "req-deldv"),
		FreezeStagingKey("vod/tenant/hash-deldv/hash-deldv.mp4.dtsh", "req-deldv"),
		FreezePublishKey("vod/tenant/hash-deldv/hash-deldv.mp4", "req-deldv"),
		FreezePublishDtshKey("vod/tenant/hash-deldv/hash-deldv.mp4", "req-deldv"),
	} {
		if got := scalar(`SELECT COALESCE((SELECT object_key FROM foghorn.staging_cleanup_queue WHERE object_key=$1),'')`, want); got != want {
			t.Fatalf("terminal trigger (production delete shape) must enqueue %q, got %q", want, got)
		}
	}

	// --- Processing-vs-freeze: the PRODUCTION claim must reject a not-yet-ready (processing) artifact,
	// even when a complete copy is already reported, and accept it once processing publishes it. ---
	if _, err := conn.Exec(`INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, tenant_id, status, sync_status, storage_location)
		VALUES ('hash-proc', 'vod', $1::uuid, 'processing', 'pending', 'local')`, tid); err != nil {
		t.Fatalf("seed processing artifact: %v", err)
	}
	if _, err := conn.Exec(`INSERT INTO foghorn.artifact_nodes (artifact_hash, node_id, is_complete, is_orphaned, cached_at)
		VALUES ('hash-proc', 'np', true, false, NOW())`); err != nil {
		t.Fatalf("seed processing node copy: %v", err)
	}
	if claimed, err := ClaimFreezeAttempt(ctx, conn, "hash-proc", "req-proc", "np", tid, "", "vod/tenant/hash-proc/hash-proc.mp4"); err != nil || claimed {
		t.Fatalf("a processing artifact must not be claimable: claimed=%v err=%v", claimed, err)
	}
	if _, err := conn.Exec(`UPDATE foghorn.artifacts SET status='ready' WHERE artifact_hash='hash-proc'`); err != nil {
		t.Fatalf("promote to ready: %v", err)
	}
	if claimed, err := ClaimFreezeAttempt(ctx, conn, "hash-proc", "req-proc", "np", tid, "", "vod/tenant/hash-proc/hash-proc.mp4"); err != nil || !claimed {
		t.Fatalf("once ready, the claim must win: claimed=%v err=%v", claimed, err)
	}
}

// TestClaimFreezeAttempt_LedgerAtomicity_RealPG proves the central claim invariant against a real engine,
// which sqlmock cannot: (1) a winning claim records EXACTLY the four deterministic publication-ledger rows
// (main+dtsh staging guarded=false, main+dtsh candidate guarded=true) in the SAME transaction as the claim;
// (2) if the ledger write fails, the whole transaction rolls back — the artifact keeps no attempt identity
// and stays claimable, and no ledger rows leak.
func TestClaimFreezeAttempt_LedgerAtomicity_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })

	ctx := context.Background()
	const tid = "11111111-1111-1111-1111-111111111111"

	seedReady := func(hash string) {
		if _, err := conn.Exec(`INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, tenant_id, status, sync_status, storage_location)
			VALUES ($1, 'vod', $2::uuid, 'ready', 'pending', 'local')`, hash, tid); err != nil {
			t.Fatalf("seed artifact: %v", err)
		}
		if _, err := conn.Exec(`INSERT INTO foghorn.artifact_nodes (artifact_hash, node_id, is_complete, is_orphaned, cached_at)
			VALUES ($1, 'nn', true, false, NOW())`, hash); err != nil {
			t.Fatalf("seed node copy: %v", err)
		}
	}

	// (1) Success: the claim commits AND records exactly the four deterministic ledger rows.
	const okHash = "hash-ledger-ok"
	const okReq = "req-ledger-ok"
	const okKey = "vod/tenant/hash-ledger-ok/hash-ledger-ok.mp4"
	seedReady(okHash)
	if claimed, err := ClaimFreezeAttempt(ctx, conn, okHash, okReq, "nn", tid, "", okKey); err != nil || !claimed {
		t.Fatalf("claim must win: claimed=%v err=%v", claimed, err)
	}
	var n int
	if err := conn.QueryRow(`SELECT count(*) FROM foghorn.freeze_publication_ledger WHERE request_id=$1`, okReq).Scan(&n); err != nil {
		t.Fatalf("count ledger rows: %v", err)
	}
	if n != 4 {
		t.Fatalf("a winning claim must record exactly 4 ledger rows, got %d", n)
	}
	for _, want := range []struct {
		key     string
		guarded bool
	}{
		{FreezeStagingKey(okKey, okReq), false},
		{FreezeStagingKey(okKey+".dtsh", okReq), false},
		{FreezePublishKey(okKey, okReq), true},
		{FreezePublishDtshKey(okKey, okReq), true},
	} {
		var g bool
		if err := conn.QueryRow(`SELECT guarded FROM foghorn.freeze_publication_ledger WHERE object_key=$1`, want.key).Scan(&g); err != nil {
			t.Fatalf("expected ledger row for %q: %v", want.key, err)
		}
		if g != want.guarded {
			t.Fatalf("ledger row %q guarded=%v, want %v", want.key, g, want.guarded)
		}
	}

	// (2) Atomic rollback: force the ledger INSERT to fail with a temporary always-false CHECK; the claim
	// UPDATE in the same tx must roll back. NOT VALID skips existing rows but still enforces new INSERTs.
	const failHash = "hash-ledger-fail"
	const failReq = "req-ledger-fail"
	const failKey = "vod/tenant/hash-ledger-fail/hash-ledger-fail.mp4"
	seedReady(failHash)
	if _, err := conn.Exec(`ALTER TABLE foghorn.freeze_publication_ledger ADD CONSTRAINT tmp_force_fail CHECK (false) NOT VALID`); err != nil {
		t.Fatalf("add forcing constraint: %v", err)
	}
	claimed, err := ClaimFreezeAttempt(ctx, conn, failHash, failReq, "nn", tid, "", failKey)
	if _, derr := conn.Exec(`ALTER TABLE foghorn.freeze_publication_ledger DROP CONSTRAINT tmp_force_fail`); derr != nil {
		t.Fatalf("drop forcing constraint: %v", derr)
	}
	if err == nil || claimed {
		t.Fatalf("a ledger-write failure must fail the claim: claimed=%v err=%v", claimed, err)
	}
	// The artifact must be untouched: still ready/pending, no attempt identity → still claimable.
	var status, syncStatus, syncReq sql.NullString
	if err := conn.QueryRow(`SELECT status, sync_status, sync_request_id FROM foghorn.artifacts WHERE artifact_hash=$1`, failHash).
		Scan(&status, &syncStatus, &syncReq); err != nil {
		t.Fatalf("read back failed-claim row: %v", err)
	}
	if status.String != "ready" || syncStatus.String != "pending" || syncReq.Valid {
		t.Fatalf("failed claim must leave the row unclaimed: status=%q sync_status=%q req_valid=%v", status.String, syncStatus.String, syncReq.Valid)
	}
	// No ledger rows may persist for the rolled-back attempt.
	if err := conn.QueryRow(`SELECT count(*) FROM foghorn.freeze_publication_ledger WHERE request_id=$1`, failReq).Scan(&n); err != nil {
		t.Fatalf("count failed ledger rows: %v", err)
	}
	if n != 0 {
		t.Fatalf("no ledger rows may persist for a rolled-back claim, got %d", n)
	}
}

// TestChapterFinalizeNodeBinding_RealPG proves the chapter-finalize reporting-node binding is enforced INSIDE
// the guarded transitions against a real engine (closing the TOCTOU the standalone-read version had): a
// node-reported terminal-fail / retry-bounce whose node does not match dvr_chapters.finalize_node_id is a
// no-op under the row lock, while the assigned node's transition succeeds. Internal recovery ("" node) is
// authoritative regardless of assignment.
func TestChapterFinalizeNodeBinding_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })

	ctx := context.Background()
	const tid = "11111111-1111-1111-1111-111111111111"

	// Parent DVR artifact the chapter references (playback_artifact_hash left NULL to keep the fail path simple).
	if _, err := conn.Exec(`INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, tenant_id, status, sync_status, storage_location)
		VALUES ('dvrparent', 'dvr', $1::uuid, 'completed', 'synced', 'local')`, tid); err != nil {
		t.Fatalf("seed parent dvr: %v", err)
	}
	seedFinalizing := func(chapterID string) {
		if _, err := conn.Exec(`INSERT INTO foghorn.dvr_chapters (chapter_id, artifact_hash, mode, start_ms, end_ms, state, finalize_node_id)
			VALUES ($1, 'dvrparent', 'window_sized_chapters', 0, 1000, 'finalizing', 'node-A')
			ON CONFLICT (chapter_id) DO UPDATE SET state = 'finalizing', finalize_node_id = 'node-A', last_failure_reason = NULL`, chapterID); err != nil {
			t.Fatalf("seed chapter %s: %v", chapterID, err)
		}
	}
	stateOf := func(chapterID string) string {
		var s string
		if err := conn.QueryRow(`SELECT state FROM foghorn.dvr_chapters WHERE chapter_id = $1`, chapterID).Scan(&s); err != nil {
			t.Fatalf("read state %s: %v", chapterID, err)
		}
		return s
	}

	// --- MarkChapterFailed: foreign node is a no-op; assigned node transitions. ---
	seedFinalizing("chap-mf")
	if err := MarkChapterFailed(ctx, "chap-mf", ChapterStateFailedSourceMissing, "foreign", "node-B"); err != nil {
		t.Fatalf("MarkChapterFailed(node-B): %v", err)
	}
	if got := stateOf("chap-mf"); got != "finalizing" {
		t.Fatalf("a foreign node's fail must be a no-op, state=%q", got)
	}
	if err := MarkChapterFailed(ctx, "chap-mf", ChapterStateFailedSourceMissing, "assigned", "node-A"); err != nil {
		t.Fatalf("MarkChapterFailed(node-A): %v", err)
	}
	if got := stateOf("chap-mf"); got != ChapterStateFailedSourceMissing {
		t.Fatalf("the assigned node's fail must transition, state=%q", got)
	}

	// --- RetryChapterFinalize: foreign node is a no-op; assigned node bounces to closed AND clears the
	// assignment so the retired node can no longer act. ---
	seedFinalizing("chap-rt")
	if err := RetryChapterFinalize(ctx, "chap-rt", "foreign", "node-B"); err != nil {
		t.Fatalf("RetryChapterFinalize(node-B): %v", err)
	}
	if got := stateOf("chap-rt"); got != "finalizing" {
		t.Fatalf("a foreign node's retry must be a no-op, state=%q", got)
	}
	if err := RetryChapterFinalize(ctx, "chap-rt", "assigned", "node-A"); err != nil {
		t.Fatalf("RetryChapterFinalize(node-A): %v", err)
	}
	if got := stateOf("chap-rt"); got != "closed" {
		t.Fatalf("the assigned node's retry must bounce to closed, state=%q", got)
	}
	var nodeAfter sql.NullString
	if err := conn.QueryRow(`SELECT finalize_node_id FROM foghorn.dvr_chapters WHERE chapter_id='chap-rt'`).Scan(&nodeAfter); err != nil {
		t.Fatalf("read finalize_node_id: %v", err)
	}
	if nodeAfter.Valid {
		t.Fatalf("retry must clear finalize_node_id, got %q", nodeAfter.String)
	}

	// --- The reviewer's deterministic stale-node sequence: after A's retry bounced the chapter to 'closed',
	// a DELAYED terminal-failure report from A must be a NO-OP (a node-reported terminal transition requires
	// state='finalizing'), so it cannot terminalize the re-queued chapter before redispatch. ---
	if err := MarkChapterFailed(ctx, "chap-rt", ChapterStateFailedSourceMissing, "delayed-A", "node-A"); err != nil {
		t.Fatalf("delayed MarkChapterFailed(node-A): %v", err)
	}
	if got := stateOf("chap-rt"); got != "closed" {
		t.Fatalf("a delayed report from the retired node must NOT terminalize the re-queued chapter, state=%q", got)
	}

	// --- Concurrency: with one assigned node (node-A) and many foreign reporters racing terminal-fail, the
	// row-locked guarded UPDATE admits EXACTLY the assigned node exactly once. ---
	seedFinalizing("chap-race")
	nodes := []string{"node-A", "node-B", "node-C", "node-D", "node-E", "node-F", "node-G", "node-H"}
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make([]error, len(nodes))
	for i, n := range nodes {
		wg.Add(1)
		go func(i int, node string) {
			defer wg.Done()
			<-start
			errs[i] = MarkChapterFailed(ctx, "chap-race", ChapterStateFailedSourceMissing, "race", node)
		}(i, n)
	}
	close(start)
	wg.Wait()
	for i := range nodes {
		if errs[i] != nil {
			t.Fatalf("MarkChapterFailed(%s) errored: %v", nodes[i], errs[i])
		}
	}
	if got := stateOf("chap-race"); got != ChapterStateFailedSourceMissing {
		t.Fatalf("the assigned node must win the race and terminalize, state=%q", got)
	}

	// --- Internal recovery ("" node) is authoritative regardless of the assignment, and may terminalize a
	// 'closed' row (e.g. max attempts exceeded before redispatch). ---
	seedFinalizing("chap-int")
	if err := RetryChapterFinalize(ctx, "chap-int", "internal", ""); err != nil {
		t.Fatalf("RetryChapterFinalize(internal): %v", err)
	}
	if got := stateOf("chap-int"); got != "closed" {
		t.Fatalf("internal recovery must bounce regardless of node, state=%q", got)
	}
	if err := MarkChapterFailed(ctx, "chap-int", ChapterStateFailedPermanent, "max attempts", ""); err != nil {
		t.Fatalf("internal MarkChapterFailed on closed: %v", err)
	}
	if got := stateOf("chap-int"); got != ChapterStateFailedPermanent {
		t.Fatalf("internal recovery must terminalize a closed row, state=%q", got)
	}
}

// TestFreezeConstraints_RealPG pins EVERY invalid freeze/dtsh-identity shape against the real foghorn.sql
// CHECK constraints, including the NULL-semantics cases a naive CHECK would let through (a CHECK passes on
// NULL): a NULL governing column can no longer let an active identity slip past.
func TestFreezeConstraints_RealPG(t *testing.T) {
	conn := startRealPG(t)
	const tid = "11111111-1111-1111-1111-111111111111"

	// insert builds a row from column=value pairs on top of a valid ready base, returning the DB error.
	insert := func(hash string, cols string) error {
		_, err := conn.Exec(`INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, tenant_id, `+colNames(cols)+`)
			VALUES ($1, 'vod', $2::uuid, `+colVals(cols)+`)`, hash, tid)
		return err
	}
	mustReject := func(name, hash, cols, constraint string) {
		t.Helper()
		err := insert(hash, cols)
		if err == nil {
			t.Fatalf("%s: expected rejection by %s, but the write succeeded", name, constraint)
		}
		if !strings.Contains(err.Error(), constraint) {
			t.Fatalf("%s: expected %s violation, got %v", name, constraint, err)
		}
	}

	// Active MAIN identity requires ready+freezing+in_progress+nonblank descriptor — NULL-safe.
	mustReject("null status", "c1",
		"status=NULL, storage_location='freezing', sync_status='in_progress', sync_object_key='k', sync_request_id='r', sync_node_id='n'",
		"chk_foghorn_artifacts_active_freeze_state")
	mustReject("null storage_location", "c2",
		"status='ready', storage_location=NULL, sync_status='in_progress', sync_object_key='k', sync_request_id='r', sync_node_id='n'",
		"chk_foghorn_artifacts_active_freeze_state")
	mustReject("blank descriptor", "c3",
		"status='ready', storage_location='freezing', sync_status='in_progress', sync_object_key='   ', sync_request_id='r', sync_node_id='n'",
		"chk_foghorn_artifacts_active_freeze_state")
	mustReject("half main identity", "c4",
		"status='ready', storage_location='freezing', sync_status='in_progress', sync_object_key='k', sync_request_id='r'",
		"chk_foghorn_artifacts_sync_identity_paired")

	// Active DTSH identity requires dtsh in_progress + synced + not-yet-dtsh-synced — NULL-safe.
	mustReject("null dtsh_synced", "d1",
		"sync_status='synced', dtsh_status='in_progress', dtsh_synced=NULL, dtsh_sync_request_id='r', dtsh_sync_node_id='n'",
		"chk_foghorn_artifacts_active_dtsh_state")
	mustReject("dtsh already synced", "d2",
		"sync_status='synced', dtsh_status='in_progress', dtsh_synced=true, dtsh_sync_request_id='r', dtsh_sync_node_id='n'",
		"chk_foghorn_artifacts_active_dtsh_state")
	mustReject("half dtsh identity", "d3",
		"status='ready', sync_status='synced', dtsh_status='in_progress', dtsh_synced=false, dtsh_sync_request_id='r'",
		"chk_foghorn_artifacts_dtsh_identity_paired")

	// Active DTSH requires a CLIP or VOD parent in 'ready' — whole-DVR .dtsh was retired, so a DVR parent (in
	// ANY status) must not hold an active DTSH identity, and neither may a NULL/processing/failed/requested one.
	dtshOnStatus := func(hash, status string) string {
		s := "NULL"
		if status != "" {
			s = "'" + status + "'"
		}
		return "status=" + s + ", sync_status='synced', dtsh_status='in_progress', dtsh_synced=false, dtsh_sync_request_id='r', dtsh_sync_node_id='n'"
	}
	mustReject("dtsh null status", "d4", dtshOnStatus("d4", ""), "chk_foghorn_artifacts_active_dtsh_state")
	mustReject("dtsh processing", "d5", dtshOnStatus("d5", "processing"), "chk_foghorn_artifacts_active_dtsh_state")
	mustReject("dtsh failed", "d6", dtshOnStatus("d6", "failed"), "chk_foghorn_artifacts_active_dtsh_state")
	mustReject("dtsh requested", "d7", dtshOnStatus("d7", "requested"), "chk_foghorn_artifacts_active_dtsh_state")
	// A DVR parent — even in its published 'completed' state — is now rejected (whole-DVR .dtsh is retired).
	// The insert helper hardcodes artifact_type='vod', so drive this one directly with artifact_type='dvr'.
	if _, err := conn.Exec(`INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, tenant_id, status, sync_status, dtsh_status, dtsh_synced, dtsh_sync_request_id, dtsh_sync_node_id)
		VALUES ('d8', 'dvr', $1::uuid, 'completed', 'synced', 'in_progress', false, 'r', 'n')`, tid); err == nil {
		t.Fatal("dtsh dvr completed: expected rejection by chk_foghorn_artifacts_active_dtsh_state, but the write succeeded")
	} else if !strings.Contains(err.Error(), "chk_foghorn_artifacts_active_dtsh_state") {
		t.Fatalf("dtsh dvr completed: expected chk_foghorn_artifacts_active_dtsh_state violation, got %v", err)
	}

	// A directly-inserted terminal row carrying identity is rejected (the trigger only fires on UPDATE).
	mustReject("terminal with identity", "t1",
		"status='deleted', storage_location='freezing', sync_status='in_progress', sync_object_key='k', sync_request_id='r', sync_node_id='n'",
		"chk_foghorn_artifacts_active_freeze_state")

	// Valid rows insert cleanly (sanity: the constraints don't reject legitimate state).
	if err := insert("ok1",
		"status='ready', storage_location='freezing', sync_status='in_progress', sync_object_key='k', sync_request_id='r', sync_node_id='n'"); err != nil {
		t.Fatalf("a valid active-freeze row must be accepted, got %v", err)
	}
	if err := insert("okdtsh-clip",
		"status='ready', sync_status='synced', dtsh_status='in_progress', dtsh_synced=false, dtsh_sync_request_id='r', dtsh_sync_node_id='n'"); err != nil {
		t.Fatalf("a valid clip active-dtsh row (status=ready) must be accepted, got %v", err)
	}
}

// colNames / colVals split a "col=val, col=val" spec into parallel INSERT column and VALUES lists so each
// invalid-shape case reads as one line. Values are inlined literals (NULL, quoted strings, booleans).
func colNames(spec string) string {
	var names []string
	for _, pair := range strings.Split(spec, ",") {
		names = append(names, strings.TrimSpace(strings.SplitN(pair, "=", 2)[0]))
	}
	return strings.Join(names, ", ")
}

func colVals(spec string) string {
	var vals []string
	for _, pair := range strings.Split(spec, ",") {
		kv := strings.SplitN(pair, "=", 2)
		vals = append(vals, strings.TrimSpace(kv[1]))
	}
	return strings.Join(vals, ", ")
}
