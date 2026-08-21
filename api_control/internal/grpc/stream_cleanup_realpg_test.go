//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"fmt"
	"os/exec"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/outbox"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	_ "github.com/lib/pq"
	"github.com/sirupsen/logrus"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// startCommodoreRealPG spins up a throwaway Postgres, applies the REAL embedded commodore.sql baseline, and returns a
// live *sql.DB. It drives the ACTUAL stream-cleanup outbox worker (claim → dispatch → retry → finalize) against the
// ACTUAL deployed schema, so the two-phase deletion saga's DB coordination cannot drift from the schema undetected.
func startCommodoreRealPG(t *testing.T) *sql.DB {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("fw-commodore-realpg-%d", time.Now().UnixNano())
	run := dockerpg.CLI
	// Register cleanup BEFORE `docker run`: the name is fixed, and a run that times out (e.g. a slow image pull) may
	// still have created the container, so cleanup must be armed even if run returns an error. -v also removes the
	// container's anonymous data volume (the postgres image declares /var/lib/postgresql/data as a VOLUME); `rm -f`
	// alone leaks it until the Docker VM hits ENOSPC.
	t.Cleanup(func() { _, _ = run("rm", "-fv", name) })
	image, err := dockerpg.PostgresImage()
	if err != nil {
		t.Fatalf("resolve PostgreSQL test image: %v", err)
	}
	if out, err := dockerpg.Run("run", "-d", "--name", name, "-P", "-e", "POSTGRES_PASSWORD=harness", image); err != nil {
		t.Fatalf("docker run: %v\n%s", err, out)
	}

	port, err := dockerpg.DiscoverPublishedHostPort(name, "5432/tcp")
	if err != nil {
		t.Fatalf("%v", err)
	}

	dsn := fmt.Sprintf("postgres://postgres:harness@127.0.0.1:%s/postgres?sslmode=disable", port)
	conn, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	if err := dockerpg.WaitReady(conn, name); err != nil {
		t.Fatalf("%v", err)
	}

	schema, rerr := dbsql.Content.ReadFile("schema/commodore.sql")
	if rerr != nil {
		t.Fatalf("read commodore.sql: %v", rerr)
	}
	if _, err := conn.Exec(string(schema)); err != nil {
		t.Fatalf("apply commodore.sql: %v", err)
	}
	return conn
}

// TestStreamCleanupOutboxLoop_DeliveryOutageConverges_RealPG is the wired end-to-end proof of the two-phase deletion
// saga's durability against a Foghorn delivery outage — the exact scenario the design names: DeleteStream has already
// soft-deleted the stream and enqueued the durable obligation; the outbox worker then drives it to completion even
// though Foghorn is unreachable for the first attempt.
//
// It runs the ACTUAL outbox.Worker (real claim/lease/SKIP-LOCKED, real settlement, real finalize) over a real
// Postgres on the real commodore.sql schema. The ONLY fakes are the two external Foghorn RPCs — the thumbnail-cleanup
// call and the per-child cascade delete — injected through the streamThumbnailDeleteFn / childArtifactDeleteFn seams.
// This tests OUR coordination logic (the DB saga), not Foghorn's transport.
//
// Attempt 1: both Foghorn seams FAIL (outage). The obligation must stay pending with attempts bumped, the stream must
// remain soft-deleted (NOT finalized), and no terminal event may be emitted. Attempt 2 (after the backoff elapses and
// Foghorn recovers): the cascade acks, the stream is hard-deleted, the obligation flips to completed, and EXACTLY ONE
// terminal stream_deleted event is emitted.
func TestStreamCleanupOutboxLoop_DeliveryOutageConverges_RealPG(t *testing.T) {
	conn := startCommodoreRealPG(t)
	ctx := context.Background()

	const (
		tenantID = "11111111-1111-1111-1111-111111111111"
		userID   = "22222222-2222-2222-2222-222222222222"
		streamID = "33333333-3333-3333-3333-333333333333"
	)

	// A stream that DeleteStream already soft-deleted (deleted_at set), with one cascade-owned clip child.
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO commodore.streams (id, tenant_id, user_id, stream_key, playback_id, internal_name, title, deleted_at, thumbnail_serving_cluster_ids)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'sk-1', 'pb-1', 'intname-1', 'title', NOW(), '{cluster-a}')
	`, streamID, tenantID, userID); err != nil {
		t.Fatalf("seed soft-deleted stream: %v", err)
	}
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO commodore.clips (tenant_id, user_id, stream_id, clip_hash, internal_name, playback_id, start_time, duration, origin_cluster_id)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'cliphash01', 'clipint-1', 'clippb-1', 0, 1000, 'cluster-a')
	`, tenantID, userID, streamID); err != nil {
		t.Fatalf("seed clip child: %v", err)
	}

	server := &CommodoreServer{db: conn, logger: logrus.New()}
	if err := server.enqueueStreamCleanupOutbox(ctx, conn, streamID, tenantID); err != nil {
		t.Fatalf("enqueue obligation: %v", err)
	}

	// Track that the recovered attempt exercises BOTH Foghorn touchpoints (thumbnail RPC + child cascade).
	var thumbCalls, childCalls int
	var foghornUp bool
	server.streamThumbnailDeleteFn = func(_ context.Context, gotStream, gotTenant, gotCluster string) error {
		thumbCalls++
		if gotStream != streamID || gotTenant != tenantID {
			t.Fatalf("thumbnail seam args = (%q,%q), want (%q,%q)", gotStream, gotTenant, streamID, tenantID)
		}
		// Cleanup dispatches to the recorded owning cell (never the removed tenant-primary fallback).
		if gotCluster != "cluster-a" {
			t.Fatalf("thumbnail seam cluster = %q, want cluster-a (the recorded serving cell)", gotCluster)
		}
		if !foghornUp {
			return fmt.Errorf("foghorn unreachable")
		}
		return nil
	}
	server.childArtifactDeleteFn = func(_ context.Context, kind, hash, _, gotTenant string) error {
		childCalls++
		if gotTenant != tenantID {
			t.Fatalf("child seam tenant = %q, want %q", gotTenant, tenantID)
		}
		if kind != "clip" || hash != "cliphash01" {
			t.Fatalf("unexpected child (%q,%q)", kind, hash)
		}
		if !foghornUp {
			return fmt.Errorf("foghorn unreachable")
		}
		return nil // recovered: child acked
	}

	cfg := streamCleanupOutboxConfig()
	cfg.AlertAfterAttempts = 0
	worker := &outbox.Worker[streamCleanupOutboxRow]{
		Config:     cfg,
		Store:      &streamCleanupOutboxStore{server: server},
		Dispatcher: &streamCleanupOutboxDispatcher{server: server},
		Logger:     server.logger,
		AlertLabel: "stream thumbnail cleanup",
	}

	outboxState := func() (status string, attempts int, lastErr sql.NullString) {
		if err := conn.QueryRowContext(ctx,
			`SELECT status, attempts, last_error FROM commodore.stream_cleanup_outbox WHERE stream_id = $1::uuid`, streamID).
			Scan(&status, &attempts, &lastErr); err != nil {
			t.Fatalf("read outbox state: %v", err)
		}
		return
	}
	streamExists := func() bool {
		var n int
		if err := conn.QueryRowContext(ctx, `SELECT COUNT(*) FROM commodore.streams WHERE id = $1::uuid`, streamID).Scan(&n); err != nil {
			t.Fatalf("count stream: %v", err)
		}
		return n > 0
	}
	terminalEvents := func() int {
		var n int
		if err := conn.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM commodore.service_event_outbox WHERE resource_id = $1 AND event_type = $2`, streamID, eventStreamDeleted).
			Scan(&n); err != nil {
			t.Fatalf("count terminal events: %v", err)
		}
		return n
	}

	// --- Attempt 1: Foghorn is DOWN. The dispatch fails, the obligation must survive as pending, and nothing may be
	// finalized. ---
	foghornUp = false
	worker.ProcessBatch(ctx)

	if st, attempts, lastErr := outboxState(); st != "pending" || attempts != 1 || !lastErr.Valid {
		t.Fatalf("after outage attempt: status=%q attempts=%d last_error=%q; want pending/1/set", st, attempts, lastErr.String)
	}
	if !streamExists() {
		t.Fatal("stream must NOT be finalized while the cascade is unacked")
	}
	if n := terminalEvents(); n != 0 {
		t.Fatalf("no terminal event may be emitted before finalize, got %d", n)
	}
	if thumbCalls != 1 {
		t.Fatalf("thumbnail seam should have been attempted once, got %d", thumbCalls)
	}
	// The cascade aborts at the failed thumbnail step, so no child delete is attempted yet.
	if childCalls != 0 {
		t.Fatalf("child cascade must not run until the thumbnail step acks, got %d calls", childCalls)
	}

	// --- Attempt 2: the backoff has elapsed (make the row due again) and Foghorn has RECOVERED. The worker re-claims,
	// the full cascade acks, and the saga finalizes. ---
	if _, err := conn.ExecContext(ctx,
		`UPDATE commodore.stream_cleanup_outbox SET next_attempt_at = NOW() - INTERVAL '1 second' WHERE stream_id = $1::uuid`, streamID); err != nil {
		t.Fatalf("make row due: %v", err)
	}
	foghornUp = true
	worker.ProcessBatch(ctx)

	if st, _, _ := outboxState(); st != "completed" {
		t.Fatalf("after recovery the obligation must be completed, got %q", st)
	}
	if streamExists() {
		t.Fatal("stream must be hard-deleted once the cascade acks")
	}
	if n := terminalEvents(); n != 1 {
		t.Fatalf("exactly one terminal stream_deleted event must be emitted, got %d", n)
	}
	if childCalls != 1 {
		t.Fatalf("recovered cascade must delete the one child exactly once, got %d", childCalls)
	}

	// --- Idempotent redelivery: a completed obligation re-driven (e.g. a duplicate tick) must be a no-op — no second
	// event, no error. ---
	if _, err := conn.ExecContext(ctx,
		`UPDATE commodore.stream_cleanup_outbox SET next_attempt_at = NOW() - INTERVAL '1 second' WHERE stream_id = $1::uuid`, streamID); err != nil {
		t.Fatalf("re-arm for idempotency check: %v", err)
	}
	worker.ProcessBatch(ctx)
	if n := terminalEvents(); n != 1 {
		t.Fatalf("a completed obligation must not re-emit the terminal event, got %d", n)
	}
}

// Live thumbnails are minted on the INGEST cell (its own per-cell Foghorn database), so a deletion must reach EVERY
// cell the stream ever ingested on — not the tenant primary. This exercises the per-cell dispatch: a soft-deleted
// stream with two recorded serving cells (+ the current active_ingest, which must DEDUP against the set) dispatches
// the cleanup RPC to each owning cell, fails the WHOLE obligation if any cell fails (so it retries), and succeeds only
// once every cell acks.
func TestStreamThumbnailCleanup_DispatchesEveryOwningCell_RealPG(t *testing.T) {
	conn := startCommodoreRealPG(t)
	ctx := context.Background()
	const (
		tenantID = "11111111-1111-1111-1111-111111111111"
		userID   = "22222222-2222-2222-2222-222222222222"
		streamID = "44444444-4444-4444-4444-444444444444"
	)
	// Two owning cells recorded in the durable set — the sole source of cleanup targets (active_ingest is NOT unioned).
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO commodore.streams
			(id, tenant_id, user_id, stream_key, playback_id, internal_name, title, deleted_at, thumbnail_serving_cluster_ids)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'sk-x', 'pb-x', 'int-x', 'title', NOW(), '{cluster-eu,cluster-us}')
	`, streamID, tenantID, userID); err != nil {
		t.Fatalf("seed stream: %v", err)
	}

	server := &CommodoreServer{db: conn, logger: logrus.New()}
	var mu sync.Mutex
	dispatched := map[string]int{}
	failUS := true // read-only within a dispatch; toggled only between passes (no concurrent write)
	server.streamThumbnailDeleteFn = func(_ context.Context, gotStream, gotTenant, gotCluster string) error {
		mu.Lock()
		dispatched[gotCluster]++
		mu.Unlock()
		if gotStream != streamID || gotTenant != tenantID {
			t.Errorf("seam args = (%q,%q), want (%q,%q)", gotStream, gotTenant, streamID, tenantID)
		}
		if gotCluster == "" {
			t.Error("a stream WITH recorded serving cells must dispatch per-cell, never the tenant-primary fallback")
		}
		if failUS && gotCluster == "cluster-us" {
			return fmt.Errorf("cluster-us foghorn unreachable")
		}
		return nil
	}
	count := func(cell string) int { mu.Lock(); defer mu.Unlock(); return dispatched[cell] }

	// Attempt 1: cluster-us fails → the WHOLE obligation errors so the outbox retries (no partial finalize).
	if err := server.deleteStreamThumbnails(ctx, streamID, tenantID); err == nil {
		t.Fatal("a failing owning cell must fail the whole obligation (so it retries), got nil")
	}
	// Attempt 2: cluster-us recovers → every cell acks, no error.
	failUS = false
	if err := server.deleteStreamThumbnails(ctx, streamID, tenantID); err != nil {
		t.Fatalf("once every cell acks the dispatch must succeed, got: %v", err)
	}
	// Both recorded owning cells were dispatched exactly once per pass — two passes → two each.
	if count("cluster-eu") != 2 {
		t.Fatalf("cluster-eu must be dispatched once per pass (2 total), got %d", count("cluster-eu"))
	}
	if count("cluster-us") != 2 {
		t.Fatalf("cluster-us must be dispatched once per pass (2 total), got %d", count("cluster-us"))
	}
}

// A cleanup dispatch to one owning cell that HANGS must not starve the others: the fan-out is concurrent under the
// shared deadline, so a healthy cell receives its tombstone even while a stuck cell blocks. (Under a serial loop a
// stable-ordered stuck first cell would leave every later cell invoked only with an already-cancelled context.)
func TestStreamThumbnailCleanup_HangingCellDoesNotStarveSiblings_RealPG(t *testing.T) {
	conn := startCommodoreRealPG(t)
	ctx := context.Background()
	const (
		tenantID = "11111111-1111-1111-1111-111111111111"
		userID   = "22222222-2222-2222-2222-222222222222"
		streamID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	)
	// cluster-slow is FIRST in the recorded set, so a serial loop would gate cluster-fast behind it.
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO commodore.streams (id, tenant_id, user_id, stream_key, playback_id, internal_name, title, thumbnail_serving_cluster_ids)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'sk-h', 'pb-h', 'int-h', 'title', '{cluster-slow,cluster-fast}')
	`, streamID, tenantID, userID); err != nil {
		t.Fatalf("seed stream: %v", err)
	}
	server := &CommodoreServer{db: conn, logger: logrus.New()}
	release := make(chan struct{})
	var slowStarted, fastDone int32
	server.streamThumbnailDeleteFn = func(_ context.Context, _, _, cl string) error {
		switch cl {
		case "cluster-slow":
			atomic.StoreInt32(&slowStarted, 1)
			<-release // hang until the test lets go
			return fmt.Errorf("cluster-slow timed out")
		case "cluster-fast":
			atomic.StoreInt32(&fastDone, 1)
			return nil
		default:
			t.Errorf("unexpected cell %q", cl)
			return nil
		}
	}

	done := make(chan error, 1)
	go func() { done <- server.deleteStreamThumbnails(ctx, streamID, tenantID) }()

	// The fast cell must complete WHILE the slow cell is still blocked — proof the dispatch is concurrent.
	deadline := time.Now().Add(3 * time.Second)
	for atomic.LoadInt32(&fastDone) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if atomic.LoadInt32(&fastDone) != 1 {
		t.Fatal("the healthy cell was starved by the hanging cell (serial fan-out)")
	}
	if atomic.LoadInt32(&slowStarted) != 1 {
		t.Fatal("the slow cell should have started concurrently")
	}
	close(release)
	if err := <-done; err == nil {
		t.Fatal("a stuck cell must still fail the obligation (aggregate error) so it retries")
	}
}

// RecordStreamActiveCluster records ACTIVE-INGEST placement only, and is SERVICE-TOKEN fenced. It must NOT write
// thumbnail_serving_cluster_ids — the cleanup-authority set has exactly one writer, the service-fenced
// register-before-mint. A JWT caller is refused.
func TestRecordStreamActiveCluster_ServiceOnly_DoesNotTouchServingSet_RealPG(t *testing.T) {
	conn := startCommodoreRealPG(t)
	svcCtx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service")
	const (
		tenantID = "11111111-1111-1111-1111-111111111111"
		userID   = "22222222-2222-2222-2222-222222222222"
		streamID = "55555555-5555-5555-5555-555555555555"
	)
	if _, err := conn.ExecContext(svcCtx, `
		INSERT INTO commodore.streams (id, tenant_id, user_id, stream_key, playback_id, internal_name, title)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'sk-y', 'pb-y', 'int-y', 'title')
	`, streamID, tenantID, userID); err != nil {
		t.Fatalf("seed stream: %v", err)
	}
	server := &CommodoreServer{db: conn, logger: logrus.New()}

	// JWT caller refused.
	jwtCtx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "jwt")
	if _, err := server.RecordStreamActiveCluster(jwtCtx, &commodorepb.RecordStreamActiveClusterRequest{StreamId: streamID, TenantId: tenantID, ClusterId: "cluster-eu"}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("JWT-auth RecordStreamActiveCluster must be PermissionDenied, got: %v", err)
	}

	// A service call scoped to the WRONG tenant matches no row (no cross-tenant placement).
	if resp, err := server.RecordStreamActiveCluster(svcCtx, &commodorepb.RecordStreamActiveClusterRequest{StreamId: streamID, TenantId: "99999999-9999-9999-9999-999999999999", ClusterId: "cluster-eu"}); err != nil || resp.GetUpdated() {
		t.Fatalf("wrong-tenant RecordStreamActiveCluster must be a no-op, got updated=%v err=%v", resp.GetUpdated(), err)
	}

	// Service call records active ingest but leaves the serving set EMPTY.
	if _, err := server.RecordStreamActiveCluster(svcCtx, &commodorepb.RecordStreamActiveClusterRequest{StreamId: streamID, TenantId: tenantID, ClusterId: "cluster-eu"}); err != nil {
		t.Fatalf("record eu: %v", err)
	}
	var active, set string
	if err := conn.QueryRowContext(svcCtx,
		`SELECT COALESCE(active_ingest_cluster_id, ''), array_to_string(thumbnail_serving_cluster_ids, ',') FROM commodore.streams WHERE id = $1::uuid`, streamID).Scan(&active, &set); err != nil {
		t.Fatalf("read row: %v", err)
	}
	if active != "cluster-eu" {
		t.Fatalf("active_ingest_cluster_id = %q, want cluster-eu", active)
	}
	if set != "" {
		t.Fatalf("RecordStreamActiveCluster must NOT write the serving set, got %q", set)
	}
}

// Register-before-mint: Foghorn takes this fence BEFORE minting a live thumbnail. It durably records the serving cell
// on a LIVE stream (registered=true, idempotent, unions cells) and REFUSES on a deleted stream (registered=false), so
// it serializes with DeleteStream — the deletion either includes the cell or the mint is refused. Tenant-scoped.
func TestRegisterStreamThumbnailServingCell_FencesOnDeletion_RealPG(t *testing.T) {
	conn := startCommodoreRealPG(t)
	// The RPC mutates durable cleanup authority and is SERVICE-TOKEN only (Foghorn).
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service")
	const (
		tenantID = "11111111-1111-1111-1111-111111111111"
		userID   = "22222222-2222-2222-2222-222222222222"
		streamID = "88888888-8888-8888-8888-888888888888"
	)
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO commodore.streams (id, tenant_id, user_id, stream_key, playback_id, internal_name, title)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'sk-z', 'pb-z', 'int-z', 'title')
	`, streamID, tenantID, userID); err != nil {
		t.Fatalf("seed stream: %v", err)
	}
	server := &CommodoreServer{db: conn, logger: logrus.New()}

	// A JWT (non-service) caller is refused — it must not be able to poison cleanup ownership.
	jwtCtx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "jwt")
	if _, err := server.RegisterStreamThumbnailServingCell(jwtCtx, &commodorepb.RegisterStreamThumbnailServingCellRequest{StreamId: streamID, TenantId: tenantID, ClusterId: "cluster-eu"}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("JWT-auth register must be PermissionDenied, got: %v", err)
	}
	set := func() string {
		var s string
		if err := conn.QueryRowContext(ctx,
			`SELECT array_to_string(thumbnail_serving_cluster_ids, ',') FROM commodore.streams WHERE id = $1::uuid`, streamID).Scan(&s); err != nil {
			t.Fatalf("read set: %v", err)
		}
		return s
	}
	reg := func(cluster, tenant string) bool {
		resp, err := server.RegisterStreamThumbnailServingCell(ctx, &commodorepb.RegisterStreamThumbnailServingCellRequest{StreamId: streamID, TenantId: tenant, ClusterId: cluster})
		if err != nil {
			t.Fatalf("register %s: %v", cluster, err)
		}
		return resp.GetRegistered()
	}

	// Live stream: registration succeeds and records the cell; idempotent; unions a second cell.
	if !reg("cluster-eu", tenantID) || set() != "cluster-eu" {
		t.Fatalf("register on a live stream must record the cell; set=%q", set())
	}
	if !reg("cluster-eu", tenantID) || set() != "cluster-eu" {
		t.Fatalf("re-register must be idempotent (registered=true, no dup); set=%q", set())
	}
	if !reg("cluster-us", tenantID) || set() != "cluster-eu,cluster-us" {
		t.Fatalf("a second cell must union; set=%q", set())
	}
	// Wrong tenant → refused, no change.
	if reg("cluster-zz", "99999999-9999-9999-9999-999999999999") {
		t.Fatal("registration under the wrong tenant must be refused")
	}
	if set() != "cluster-eu,cluster-us" {
		t.Fatalf("wrong-tenant register must not mutate the set; set=%q", set())
	}
	// Soft-delete the stream, then registration must FAIL closed (deletion won the race) and not mutate the set.
	if _, err := conn.ExecContext(ctx, `UPDATE commodore.streams SET deleted_at = NOW() WHERE id = $1::uuid`, streamID); err != nil {
		t.Fatalf("soft-delete: %v", err)
	}
	if reg("cluster-late", tenantID) {
		t.Fatal("registration on a deleted stream must return registered=false (Foghorn must not mint)")
	}
	if set() != "cluster-eu,cluster-us" {
		t.Fatalf("a refused registration must not add a cell to a deleted stream; set=%q", set())
	}
}

// Register and DeleteStream LINEARIZE on the stream row (the deleted_at IS NULL fence): under real concurrency, a
// registration is either included in the deletion (registered=true ⇒ the cell is in the durable set the cleanup reads)
// or refused (registered=false ⇒ the cell is absent). It is never "registered=true but absent" — which would strand a
// minted thumbnail past deletion.
func TestRegisterVsDeleteStream_Linearizes_RealPG(t *testing.T) {
	conn := startCommodoreRealPG(t)
	svcCtx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "service")
	const (
		tenantID = "11111111-1111-1111-1111-111111111111"
		userID   = "22222222-2222-2222-2222-222222222222"
	)
	server := &CommodoreServer{db: conn, logger: logrus.New()}

	// Repeat to exercise both interleavings (register-first and delete-first).
	for i := 0; i < 20; i++ {
		streamID := fmt.Sprintf("bbbbbbbb-0000-0000-0000-%012d", i)
		if _, err := conn.ExecContext(svcCtx, `
			INSERT INTO commodore.streams (id, tenant_id, user_id, stream_key, playback_id, internal_name, title)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6, 'title')
		`, streamID, tenantID, userID, fmt.Sprintf("sk-%d", i), fmt.Sprintf("pb-%d", i), fmt.Sprintf("int-%d", i)); err != nil {
			t.Fatalf("seed %s: %v", streamID, err)
		}
		var registered int32
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			resp, err := server.RegisterStreamThumbnailServingCell(svcCtx, &commodorepb.RegisterStreamThumbnailServingCellRequest{StreamId: streamID, TenantId: tenantID, ClusterId: "cluster-eu"})
			if err != nil {
				t.Errorf("register: %v", err)
				return
			}
			if resp.GetRegistered() {
				atomic.StoreInt32(&registered, 1)
			}
		}()
		go func() {
			defer wg.Done()
			if _, err := conn.ExecContext(svcCtx, `UPDATE commodore.streams SET deleted_at = NOW() WHERE id = $1::uuid`, streamID); err != nil {
				t.Errorf("soft-delete: %v", err)
			}
		}()
		wg.Wait()

		var inSet bool
		if err := conn.QueryRowContext(svcCtx,
			`SELECT 'cluster-eu' = ANY(thumbnail_serving_cluster_ids) FROM commodore.streams WHERE id = $1::uuid`, streamID).Scan(&inSet); err != nil {
			t.Fatalf("read set: %v", err)
		}
		if (atomic.LoadInt32(&registered) == 1) != inSet {
			t.Fatalf("iter %d: register/delete not linearized — registered=%v but inSet=%v", i, atomic.LoadInt32(&registered) == 1, inSet)
		}
	}
}

// A slow-but-successful thumbnail cell must not re-consume the item budget on every retry and starve child cleanup.
// Once the thumbnail phase acks, dispatchStreamCleanupOutboxRow durably stamps thumbnail_cleanup_acked_at and SKIPS the
// phase on later ticks — so a re-dispatched (thumbnailCleanupAcked=true) obligation never re-runs it.
func TestStreamCleanupOutbox_ThumbnailPhaseMarkedThenSkipped_RealPG(t *testing.T) {
	conn := startCommodoreRealPG(t)
	ctx := context.Background()
	const (
		tenantID = "11111111-1111-1111-1111-111111111111"
		userID   = "22222222-2222-2222-2222-222222222222"
		streamID = "cccccccc-cccc-cccc-cccc-cccccccccccc"
	)
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO commodore.streams (id, tenant_id, user_id, stream_key, playback_id, internal_name, title, deleted_at, thumbnail_serving_cluster_ids)
		VALUES ($1::uuid, $2::uuid, $3::uuid, 'sk-c', 'pb-c', 'int-c', 'title', NOW(), '{cluster-a}')
	`, streamID, tenantID, userID); err != nil {
		t.Fatalf("seed stream: %v", err)
	}
	server := &CommodoreServer{db: conn, logger: logrus.New()}
	if err := server.enqueueStreamCleanupOutbox(ctx, conn, streamID, tenantID); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	var thumbCalls int32
	server.streamThumbnailDeleteFn = func(_ context.Context, _, _, _ string) error {
		atomic.AddInt32(&thumbCalls, 1)
		return nil
	}

	// Pass 1 (thumbnailCleanupAcked=false): the phase runs and is durably marked.
	if failed, err := server.dispatchStreamCleanupOutboxRow(ctx, streamCleanupOutboxRow{streamID: streamID, tenantID: tenantID}); err != nil || len(failed) != 0 {
		t.Fatalf("pass 1 dispatch: failed=%v err=%v", failed, err)
	}
	if got := atomic.LoadInt32(&thumbCalls); got != 1 {
		t.Fatalf("thumbnail phase must run once on pass 1, got %d", got)
	}
	var marked bool
	if err := conn.QueryRowContext(ctx, `SELECT thumbnail_cleanup_acked_at IS NOT NULL FROM commodore.stream_cleanup_outbox WHERE stream_id = $1::uuid`, streamID).Scan(&marked); err != nil {
		t.Fatalf("read marker: %v", err)
	}
	if !marked {
		t.Fatal("a successful thumbnail phase must durably stamp thumbnail_cleanup_acked_at")
	}

	// Pass 2 (thumbnailCleanupAcked=true — as the next claim would load it): the phase is SKIPPED.
	if failed, err := server.dispatchStreamCleanupOutboxRow(ctx, streamCleanupOutboxRow{streamID: streamID, tenantID: tenantID, thumbnailCleanupAcked: true}); err != nil || len(failed) != 0 {
		t.Fatalf("pass 2 dispatch: failed=%v err=%v", failed, err)
	}
	if got := atomic.LoadInt32(&thumbCalls); got != 1 {
		t.Fatalf("thumbnail phase must be SKIPPED on a marked retry, but it ran again (total %d)", got)
	}
}

// End-to-end DeleteStream: the SYNCHRONOUS phase-two delivery must route through the same multi-cell dispatcher as the
// outbox (not the tenant primary alone), so a cross-cell stream finalizes ONLY once every owning cell acks. A partial
// ack must leave the stream soft-deleted (deletion_pending), never hard-deleted while an ingest cell's objects survive.
func TestDeleteStream_RoutesToEveryServingCell_RealPG(t *testing.T) {
	conn := startCommodoreRealPG(t)
	const (
		tenantID = "11111111-1111-1111-1111-111111111111"
		userID   = "22222222-2222-2222-2222-222222222222"
	)
	ctx := context.WithValue(context.WithValue(context.Background(), ctxkeys.KeyUserID, userID), ctxkeys.KeyTenantID, tenantID)

	seed := func(streamID string) {
		if _, err := conn.ExecContext(ctx, `
			INSERT INTO commodore.streams
				(id, tenant_id, user_id, stream_key, playback_id, internal_name, title, thumbnail_serving_cluster_ids)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6, 'title', '{cluster-eu,cluster-us}')
		`, streamID, tenantID, userID, "sk-"+streamID[:8], "pb-"+streamID[:8], "int-"+streamID[:8]); err != nil {
			t.Fatalf("seed %s: %v", streamID, err)
		}
	}
	exists := func(streamID string) bool {
		var n int
		if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM commodore.streams WHERE id = $1::uuid`, streamID).Scan(&n); err != nil {
			t.Fatalf("exists: %v", err)
		}
		return n > 0
	}

	server := &CommodoreServer{db: conn, logger: logrus.New()}
	var mu sync.Mutex
	dispatched := map[string]int{}
	failUS := true // toggled only between DeleteStream calls (no concurrent write)
	server.streamThumbnailDeleteFn = func(_ context.Context, _, _, gotCluster string) error {
		mu.Lock()
		dispatched[gotCluster]++
		mu.Unlock()
		if gotCluster == "" {
			t.Error("a stream WITH serving cells must dispatch per-cell, never the tenant-primary fallback")
		}
		if failUS && gotCluster == "cluster-us" {
			return fmt.Errorf("cluster-us unreachable")
		}
		return nil
	}
	count := func(cell string) int { mu.Lock(); defer mu.Unlock(); return dispatched[cell] }

	// Partial ack (cluster-us down): DeleteStream must NOT finalize — pending, still soft-deleted, both cells attempted.
	const pendingStream = "66666666-6666-6666-6666-666666666666"
	seed(pendingStream)
	resp, err := server.DeleteStream(ctx, &commodorepb.DeleteStreamRequest{StreamId: pendingStream})
	if err != nil {
		t.Fatalf("DeleteStream (partial): %v", err)
	}
	if resp.GetDeletionStatus() != "deletion_pending" {
		t.Fatalf("a partial owning-cell ack must leave the stream pending, got %q", resp.GetDeletionStatus())
	}
	if !exists(pendingStream) {
		t.Fatal("a stream must NOT be hard-deleted while an owning cell has not acked")
	}
	if count("cluster-eu") == 0 || count("cluster-us") == 0 {
		t.Fatalf("DeleteStream must fan out to EVERY serving cell, got eu=%d us=%d", count("cluster-eu"), count("cluster-us"))
	}

	// All cells ack: DeleteStream finalizes — deleted + hard-deleted.
	failUS = false
	const okStream = "77777777-7777-7777-7777-777777777777"
	seed(okStream)
	mu.Lock()
	dispatched = map[string]int{}
	mu.Unlock()
	resp, err = server.DeleteStream(ctx, &commodorepb.DeleteStreamRequest{StreamId: okStream})
	if err != nil {
		t.Fatalf("DeleteStream (all ack): %v", err)
	}
	if resp.GetDeletionStatus() != "deleted" {
		t.Fatalf("once every owning cell acks, DeleteStream must finalize, got %q", resp.GetDeletionStatus())
	}
	if exists(okStream) {
		t.Fatal("stream must be hard-deleted once every owning cell acks")
	}
	if count("cluster-eu") != 1 || count("cluster-us") != 1 {
		t.Fatalf("each owning cell must be dispatched exactly once, got eu=%d us=%d", count("cluster-eu"), count("cluster-us"))
	}
}

// Equal-revision REPAIR: a Commodore that stored a revision while DISCARDING the serving cluster (mixed-version) must be
// able to backfill the NULL serving cluster at the SAME source_revision on retry — otherwise the strict less-than guard
// would reject the retry forever and the field would be lost. A conflicting non-null value fails loudly instead.
func TestUpdateArtifactCatalogSnapshot_ServingClusterEqualRevisionRepair_RealPG(t *testing.T) {
	conn := startCommodoreRealPG(t)
	ctx := context.Background()
	server := &CommodoreServer{db: conn, logger: logrus.New()}

	tenant := "11111111-1111-1111-1111-111111111111"
	hash := "abcdef0123456789abcdef0123456799"
	// Seed a VOD row at revision 5 with NO serving cluster — the state an old Commodore left after discarding field 21.
	// Distinct placeholders per column (vod_hash VARCHAR, internal_name VARCHAR, playback_id CITEXT): reusing one $N
	// across differently-typed columns makes Postgres fail parameter type inference (42P08).
	if _, err := conn.ExecContext(ctx, `
		INSERT INTO commodore.vod_assets (tenant_id, user_id, vod_hash, internal_name, playback_id, filename,
		            origin_cluster_id, has_thumbnails, catalog_revision)
		VALUES ($1::uuid, $1::uuid, $2::varchar, $3::varchar, $4::citext, 'f.mp4', 'media-us-1', true, 5)`, tenant, hash, hash, hash); err != nil {
		t.Fatalf("seed vod: %v", err)
	}

	srcCluster := "media-us-1"
	hasThumb := true
	srv := "media-official"
	// Retry at the SAME revision 5 WITH the serving cluster → the equal-revision repair applies and echoes it.
	resp, err := server.UpdateArtifactCatalogSnapshot(ctx, &commodorepb.UpdateArtifactCatalogSnapshotRequest{
		TenantId: tenant, AssetType: commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_VOD, AssetKey: hash,
		SourceRevision: 5, SourceClusterId: &srcCluster, HasThumbnails: &hasThumb, ThumbnailServingClusterId: &srv,
	})
	if err != nil {
		t.Fatalf("repair snapshot: %v", err)
	}
	if !resp.GetFound() || resp.GetThumbnailServingClusterId() != srv {
		t.Fatalf("equal-revision repair must apply + echo the serving cluster, got found=%v echo=%q", resp.GetFound(), resp.GetThumbnailServingClusterId())
	}
	var stored sql.NullString
	if err := conn.QueryRowContext(ctx, `SELECT thumbnail_serving_cluster_id FROM commodore.vod_assets WHERE vod_hash = $1`, hash).Scan(&stored); err != nil {
		t.Fatalf("read stored: %v", err)
	}
	if !stored.Valid || stored.String != srv {
		t.Fatalf("stored serving cluster must be backfilled, got %v", stored)
	}

	// A CONFLICTING non-null value at the same revision must fail loudly (write-once), not silently overwrite or loop.
	other := "media-eu"
	_, cErr := server.UpdateArtifactCatalogSnapshot(ctx, &commodorepb.UpdateArtifactCatalogSnapshotRequest{
		TenantId: tenant, AssetType: commodorepb.ArtifactAssetType_ARTIFACT_ASSET_TYPE_VOD, AssetKey: hash,
		SourceRevision: 5, SourceClusterId: &srcCluster, ThumbnailServingClusterId: &other,
	})
	if status.Code(cErr) != codes.FailedPrecondition {
		t.Fatalf("a conflicting serving cluster must fail FailedPrecondition, got %v", cErr)
	}
}

// Ownership acquisition (the claim lease) is TENANT-FENCED like every other outbox mutation: the lease UPDATE filters
// on tenant_id in addition to stream_id. This proves the fence MATCHES the claimed row and stamps a lease token — a
// broken fence would RETURN no row and fail the claim. Also confirms the claim carries the row's tenant for the
// opaque settlement identity.
func TestClaimStreamCleanupOutboxBatch_TenantFencedLease_RealPG(t *testing.T) {
	conn := startCommodoreRealPG(t)
	ctx := context.Background()

	const (
		tenantID = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
		streamID = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	)
	server := &CommodoreServer{db: conn, logger: logrus.New()}
	if err := server.enqueueStreamCleanupOutbox(ctx, conn, streamID, tenantID); err != nil {
		t.Fatalf("enqueue obligation: %v", err)
	}
	// Make the row due regardless of the column default.
	if _, err := conn.ExecContext(ctx,
		`UPDATE commodore.stream_cleanup_outbox SET next_attempt_at = NOW() - INTERVAL '1 minute' WHERE stream_id = $1::uuid`, streamID); err != nil {
		t.Fatalf("backdate next_attempt_at: %v", err)
	}

	rows, err := server.claimStreamCleanupOutboxBatch(ctx)
	if err != nil {
		t.Fatalf("claim batch: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 claimed row, got %d", len(rows))
	}
	if rows[0].streamID != streamID || rows[0].tenantID != tenantID {
		t.Fatalf("claimed (%q,%q), want (%q,%q)", rows[0].streamID, rows[0].tenantID, streamID, tenantID)
	}
	if rows[0].leaseToken == "" {
		t.Fatal("tenant-fenced lease UPDATE must stamp a lease token; empty means the fence did not match the row")
	}
	// The persisted row carries exactly that lease token under the SAME (stream, tenant) identity.
	var persisted string
	if err := conn.QueryRowContext(ctx,
		`SELECT lease_token FROM commodore.stream_cleanup_outbox WHERE stream_id = $1::uuid AND tenant_id = $2::uuid`, streamID, tenantID).Scan(&persisted); err != nil {
		t.Fatalf("read persisted lease: %v", err)
	}
	if persisted != rows[0].leaseToken {
		t.Fatalf("persisted lease %q != claimed %q", persisted, rows[0].leaseToken)
	}
}
