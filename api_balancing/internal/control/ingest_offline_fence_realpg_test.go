//go:build schema_verify

package control

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"testing"
	"time"

	"frameworks/api_balancing/internal/database/foghorndb"
	fieldcrypto "github.com/Livepeer-FrameWorks/monorepo/pkg/crypto"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

const sourceProjectionCounterBase = int64(4503599627370496)

func TestSourceProjectionRepairAllocatorKeyScoped_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const tenantID = "10000000-0000-0000-0000-000000000001"
	const internalName = "repair-stream"
	params := foghorndb.NextSourceProjectionRevisionParams{TenantID: tenantID, StreamInternalName: internalName}
	first, err := foghorndb.New(conn).NextSourceProjectionRevision(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	repaired, err := foghorndb.AllocateSourceProjectionRevisionAfter(ctx, conn, tenantID, internalName, first+1000)
	if err != nil {
		t.Fatal(err)
	}
	next, err := foghorndb.New(conn).NextSourceProjectionRevision(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	if !(first < repaired && repaired < next) {
		t.Fatalf("source revisions first=%d repaired=%d next=%d, want strict order", first, repaired, next)
	}
}

// TestFenceOfflineBackstop_RealPG proves the offline backstop is DB-authoritative and serializes with
// admission on the (tenant, stream) advisory lock: while an ingest session is active in the DB the
// fence SUPPRESSES and does NOT flip the (locally-active) registry source — so a stale replica's local
// view cannot drive an offline flip that would clobber a live reconnect; only when the DB has no active
// session does it flip and proceed. The advisory lock (the same one CreateIngestSession takes, proven
// HA-safe by TestIngestSessionConcurrentCrossNodeAdmission_RealPG) is what orders the flip against a
// concurrent admission across replicas.
func TestFenceOfflineBackstop_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })
	ctx := context.Background()

	reg := NewStreamRegistry(nil, "cluster-A", time.Minute) // in-memory, no Redis
	const node, stream = "node-off", "live+off"

	sid := seedOpenIngestSession(t, ingA, node, stream, "u-sess", 100, 1000)
	if applied, _, err := ProjectSourceIfCurrent(ctx, reg, ingA, node, stream, 100, "u-sess", sid, AdmissionEffectIntent{}); err != nil || !applied {
		t.Fatalf("project active session: applied=%v err=%v", applied, err)
	}
	// 1. An active DB session exists → the fence must SUPPRESS (the DB, not the local registry, is
	//    authoritative) and must NOT flip the registry.
	proceed, rev, err := FenceOfflineBackstop(ctx, reg, ingA, node, stream, OfflineEffectIntent{})
	if err != nil {
		t.Fatalf("fence (active session): %v", err)
	}
	if proceed {
		t.Fatal("fence must suppress while an ingest session is active in the DB")
	}
	if rev != 0 {
		t.Fatalf("a suppressed fence must draw no revision, got %d", rev)
	}
	if gen, active, ok := reg.SourceGenerationSnapshot(stream, node); !ok || !active || gen != sid {
		t.Fatalf("registry source must stay active while a session is live (gen=%q active=%v ok=%v)", gen, active, ok)
	}

	// 2. End the session → the fence flips the registry inactive and proceeds with the offline effects.
	if _, err := db.Exec(`UPDATE foghorn.ingest_sessions SET ended_at = NOW() WHERE id = $1::uuid`, sid); err != nil {
		t.Fatalf("end session: %v", err)
	}
	proceed, rev, err = FenceOfflineBackstop(ctx, reg, ingA, node, stream, OfflineEffectIntent{})
	if err != nil {
		t.Fatalf("fence (no session): %v", err)
	}
	if !proceed {
		t.Fatal("fence must proceed once no ingest session is active")
	}
	if rev == 0 {
		t.Fatal("a proceeding fence must draw a non-zero revision for the effects guard")
	}
	if _, active, _ := reg.SourceGenerationSnapshot(stream, node); active {
		t.Fatal("fence must have flipped the registry source inactive")
	}
}

// TestOfflineEffectSerializesWithAdmission_RealPG proves the durable worker holds the same stream
// lock as admission across the complete external-effect callback. A reconnect cannot commit between
// the final no-active-session check and teardown; it waits, then becomes the later source transition.
func TestOfflineEffectSerializesWithAdmission_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })
	ctx := context.Background()
	logger := logging.NewLogger()
	const node, stream = "node-effect", "live+effect-lock"

	sessionID := seedOpenIngestSession(t, ingA, node, stream, "effect-old", 101, 1000)
	registry := NewStreamRegistry(nil, "cluster-A", time.Minute)
	if applied, _, err := ProjectSourceIfCurrent(ctx, registry, ingA, node, stream, 101, "effect-old", sessionID, AdmissionEffectIntent{}); err != nil || !applied {
		t.Fatalf("project old session: applied=%v err=%v", applied, err)
	}
	if _, err := db.Exec(`UPDATE foghorn.ingest_sessions SET ended_at=NOW() WHERE id=$1::uuid`, sessionID); err != nil {
		t.Fatalf("end old session: %v", err)
	}
	if ok, _, err := FenceOfflineBackstop(ctx, registry, ingA, node, stream, OfflineEffectIntent{TeardownStream: true}); err != nil || !ok {
		t.Fatalf("enqueue offline effect: ok=%v err=%v", ok, err)
	}
	effects, err := ClaimOfflineEffects(ctx, 1, time.Minute, "test-instance")
	if err != nil || len(effects) != 1 {
		t.Fatalf("claim offline effect: count=%d err=%v", len(effects), err)
	}

	callbackEntered := make(chan struct{})
	releaseCallback := make(chan struct{})
	applyDone := make(chan error, 1)
	go func() {
		_, err := ApplyClaimedOfflineEffect(ctx, effects[0], func(context.Context, OfflineEffect) error {
			close(callbackEntered)
			<-releaseCallback
			return nil
		})
		applyDone <- err
	}()
	<-callbackEntered

	createDone := make(chan error, 1)
	go func() {
		_, outcome, err := CreateIngestSession(ctx, ingA, "node-reconnect", stream, 202, "effect-new", 2000, nil, "cluster-A", logger)
		if err == nil && outcome != IngestSessionActive {
			err = fmt.Errorf("reconnect outcome=%v, want active", outcome)
		}
		createDone <- err
	}()
	select {
	case err := <-createDone:
		t.Fatalf("admission completed while offline effects held the stream lock: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(releaseCallback)
	if err := <-applyDone; err != nil {
		t.Fatalf("apply offline effect: %v", err)
	}
	if err := <-createDone; err != nil {
		t.Fatalf("reconnect after offline effect: %v", err)
	}
}

func TestOfflineEffectSupersededByReconnect_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })
	ctx := context.Background()
	const node, stream = "node-superseded", "live+effect-superseded"

	oldSession := seedOpenIngestSession(t, ingA, node, stream, "effect-superseded-old", 301, 1000)
	registry := NewStreamRegistry(nil, "cluster-A", time.Minute)
	if applied, _, err := ProjectSourceIfCurrent(ctx, registry, ingA, node, stream, 301, "effect-superseded-old", oldSession, AdmissionEffectIntent{}); err != nil || !applied {
		t.Fatalf("project old session: applied=%v err=%v", applied, err)
	}
	if _, err := db.Exec(`UPDATE foghorn.ingest_sessions SET ended_at=NOW() WHERE id=$1::uuid`, oldSession); err != nil {
		t.Fatalf("end old session: %v", err)
	}
	if ok, _, err := FenceOfflineBackstop(ctx, registry, ingA, node, stream, OfflineEffectIntent{TeardownStream: true}); err != nil || !ok {
		t.Fatalf("enqueue offline effect: ok=%v err=%v", ok, err)
	}
	effects, err := ClaimOfflineEffects(ctx, 1, time.Minute, "test-instance")
	if err != nil || len(effects) != 1 {
		t.Fatalf("claim offline effect: count=%d err=%v", len(effects), err)
	}

	seedOpenIngestSession(t, ingA, "node-reconnected", stream, "effect-superseded-new", 302, 2000)
	called := false
	completed, err := ApplyClaimedOfflineEffect(ctx, effects[0], func(context.Context, OfflineEffect) error {
		called = true
		return nil
	})
	if err != nil || !completed {
		t.Fatalf("supersede offline effect: completed=%v err=%v", completed, err)
	}
	if called {
		t.Fatal("offline effects ran after a reconnect became authoritative")
	}
	var state string
	if err := db.QueryRow(`SELECT state FROM foghorn.ingest_offline_effects WHERE id=$1`, effects[0].ID).Scan(&state); err != nil {
		t.Fatalf("read offline effect state: %v", err)
	}
	if state != "superseded" {
		t.Fatalf("offline effect state = %q, want superseded", state)
	}
}

func TestProjectSourceFailureAbortsPendingSession_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })
	ctx := context.Background()
	const node, stream = "node-projection-failure", "live+projection-failure"

	sessionID := seedOpenIngestSession(t, ingA, node, stream, "projection-failure", 401, 1000)
	store, _, _ := newTestRedis(t)
	const newerRevision = sourceProjectionCounterBase + 1_000_000_000
	newer := StreamEntry{InternalName: stream, Locations: map[string]Location{
		"cluster-test": {ClusterID: "cluster-test", SourceActive: true, OwnerNodeID: "node-newer", SourceRevision: newerRevision},
	}}
	change := RegistryChange{InstanceID: "peer", Entity: RegistryEntitySource, Operation: RegistryOpUpsert, Key: stream, SourceRevision: newerRevision}
	if applied, err := store.SetSourceRevisioned(ctx, newer, change, newerRevision); err != nil || !applied {
		t.Fatalf("seed newer Redis source: applied=%v err=%v", applied, err)
	}
	registry := NewStreamRegistry(nil, "cluster-A", time.Minute)
	registry.mu.Lock()
	registry.redisStore = store
	registry.instanceID = "projection-failure-test"
	registry.mu.Unlock()

	applied, _, err := ProjectSourceIfCurrent(ctx, registry, ingA, node, stream, 401, "projection-failure", sessionID, AdmissionEffectIntent{})
	if err == nil || applied {
		t.Fatalf("projection behind newer Redis revision: applied=%v err=%v, want denied error", applied, err)
	}
	ended, reason := sessionEnded(t, sessionID)
	if !ended || reason != "projection_failed" {
		t.Fatalf("pending session after projection failure: ended=%v reason=%q", ended, reason)
	}
	var effects int
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM foghorn.ingest_offline_effects
		 WHERE tenant_id=$1::uuid AND stream_internal_name=$2 AND source_generation=$3::uuid AND state='pending'
	`, ingA, stream, sessionID).Scan(&effects); err != nil {
		t.Fatalf("count projection cleanup effects: %v", err)
	}
	if effects != 1 {
		t.Fatalf("projection cleanup effects = %d, want 1", effects)
	}
}

// TestProjectSourceIfCurrent_RealPG proves the ordered-projection fix: a projection whose session is
// no longer the active one (its goroutine was delayed past its own close + a newer admission) is
// DROPPED under the advisory lock rather than clobbering the current publisher and draining it as a
// phantom prior owner.
func TestProjectSourceIfCurrent_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })
	ctx := context.Background()

	reg := NewStreamRegistry(nil, "cluster-A", time.Minute)
	const nodeA, nodeB, stream = "node-A", "node-B", "live+proj"

	// Publisher B is the current live publisher (its session is the active one).
	sB := seedOpenIngestSession(t, ingA, nodeB, stream, "u-b", 200, 2000)
	appliedB, _, err := ProjectSourceIfCurrent(ctx, reg, ingA, nodeB, stream, 200, "u-b", sB, AdmissionEffectIntent{})
	if err != nil {
		t.Fatalf("project B: %v", err)
	}
	if !appliedB {
		t.Fatalf("first projection: applied=%v, want true", appliedB)
	}
	// The obligation persisted with B's confirmation carries no prior owner (a fresh source).
	var priorB string
	if err := db.QueryRow(`SELECT prior_owner_node_id FROM foghorn.ingest_admission_effects WHERE source_generation=$1::uuid`, sB).Scan(&priorB); err != nil {
		t.Fatalf("read B's admission obligation: %v", err)
	}
	if priorB != "" {
		t.Fatalf("fresh source obligation must carry no prior owner, got %q", priorB)
	}
	if gen, active, ok := reg.SourceGenerationSnapshot(stream, nodeB); !ok || !active || gen != sB {
		t.Fatalf("B must be projected active (gen=%q active=%v ok=%v)", gen, active, ok)
	}

	// Publisher A's DELAYED projection: A's session was already ended and superseded by B, so its
	// generation is NOT the active session. It must be DROPPED with applied=false (the caller denies) —
	// no clobber, no prior owner to drain.
	sA := "99999999-9999-9999-9999-999999999999" // a generation that is not the active session
	appliedA, _, err := ProjectSourceIfCurrent(ctx, reg, ingA, nodeA, stream, 100, "u-a", sA, AdmissionEffectIntent{})
	if err != nil {
		t.Fatalf("stale project A: %v", err)
	}
	if appliedA {
		t.Fatal("a stale projection must report applied=false so the caller denies (no side effects)")
	}
	// And it must not have persisted any obligation (which would drain the live publisher).
	var staleObligations int
	if err := db.QueryRow(`SELECT COUNT(*) FROM foghorn.ingest_admission_effects WHERE source_generation=$1::uuid`, sA).Scan(&staleObligations); err != nil {
		t.Fatalf("count stale obligations: %v", err)
	}
	if staleObligations != 0 {
		t.Fatalf("a dropped stale projection persisted %d obligation(s), want 0", staleObligations)
	}
	if gen, active, ok := reg.SourceGenerationSnapshot(stream, nodeB); !ok || !active || gen != sB {
		t.Fatalf("stale projection clobbered the live publisher B (gen=%q active=%v ok=%v)", gen, active, ok)
	}
}

// TestPushRewriteRetry_IdempotentResumedProjection_RealPG proves the durable retry protocol for a
// lost accept response end-to-end against real Postgres: the SAME accepted trigger runs the full
// mint+project sequence twice and gets identical success — one session row, the second projection
// resolved as RESUMED (no re-confirmation, no revision change), and EXACTLY ONE durable admission
// obligation (the once-only effects are owed by that row, so a crash cannot lose them and a retry
// cannot duplicate them). A same-UUID replay with a different connector PID is rejected — it is not
// this connection's retry.
func TestPushRewriteRetry_IdempotentResumedProjection_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })
	ctx := context.Background()
	logger := logging.NewLogger()
	const node, stream, uuid = "node-retry", "live+retry-resume", "retry-resume-uuid"

	registry := NewStreamRegistry(nil, "cluster-A", time.Minute)

	// First pass: mint + project + confirm.
	sid1, outcome1, err := CreateIngestSession(ctx, ingA, node, stream, 501, uuid, 1000, nil, "cluster-A", logger)
	if err != nil || outcome1 != IngestSessionActive {
		t.Fatalf("first mint: outcome=%v err=%v", outcome1, err)
	}
	applied1, resumed1, err := ProjectSourceIfCurrent(ctx, registry, ingA, node, stream, 501, uuid, sid1, AdmissionEffectIntent{BroadcastLive: true})
	if err != nil || !applied1 || resumed1 {
		t.Fatalf("first projection: applied=%v resumed=%v err=%v, want applied+fresh", applied1, resumed1, err)
	}
	var stateAfterFirst string
	var revAfterFirst int64
	if err := db.QueryRow(`SELECT projection_state, source_revision FROM foghorn.ingest_sessions WHERE id=$1::uuid`, sid1).Scan(&stateAfterFirst, &revAfterFirst); err != nil {
		t.Fatalf("read after first pass: %v", err)
	}
	if stateAfterFirst != "active" || revAfterFirst <= 0 {
		t.Fatalf("first pass must confirm active with a revision, got state=%q rev=%d", stateAfterFirst, revAfterFirst)
	}

	// Second pass: the SAME trigger re-fires (blocking-trigger retry after a lost response).
	sid2, outcome2, err := CreateIngestSession(ctx, ingA, node, stream, 501, uuid, 1000, nil, "cluster-A", logger)
	if err != nil || outcome2 != IngestSessionActive {
		t.Fatalf("retry mint: outcome=%v err=%v, want the idempotent Active", outcome2, err)
	}
	if sid2 != sid1 {
		t.Fatalf("retry minted a NEW session %s, want the existing %s", sid2, sid1)
	}
	applied2, resumed2, err := ProjectSourceIfCurrent(ctx, registry, ingA, node, stream, 501, uuid, sid2, AdmissionEffectIntent{BroadcastLive: true})
	if err != nil {
		t.Fatalf("retry projection must succeed, got: %v", err)
	}
	if !applied2 || !resumed2 {
		t.Fatalf("retry projection: applied=%v resumed=%v, want applied+resumed", applied2, resumed2)
	}
	// EXACTLY ONE durable obligation exists for the generation — persisted atomically with the
	// first confirmation, not duplicated by the retry. This is what survives a crash at any point
	// after confirmation: the admission-effects worker replays it.
	var obligations int
	if err := db.QueryRow(`SELECT COUNT(*) FROM foghorn.ingest_admission_effects WHERE source_generation=$1::uuid`, sid1).Scan(&obligations); err != nil {
		t.Fatalf("count admission obligations: %v", err)
	}
	if obligations != 1 {
		t.Fatalf("admission obligations for the generation = %d, want exactly 1", obligations)
	}
	// A same-UUID replay carrying a DIFFERENT connector PID is not this connection's retry — reject.
	_, outcomePID, err := CreateIngestSession(ctx, ingA, node, stream, 999, uuid, 1000, nil, "cluster-A", logger)
	if err != nil || outcomePID != IngestSessionRejectedDuplicate {
		t.Fatalf("same-UUID/different-PID replay: outcome=%v err=%v, want RejectedDuplicate", outcomePID, err)
	}

	// Exactly one session; state and revision untouched by the retry.
	var sessions int
	if err := db.QueryRow(`SELECT COUNT(*) FROM foghorn.ingest_sessions WHERE tenant_id=$1::uuid AND stream_internal_name=$2`, ingA, stream).Scan(&sessions); err != nil {
		t.Fatalf("count sessions: %v", err)
	}
	if sessions != 1 {
		t.Fatalf("retry duplicated the session: %d rows, want 1", sessions)
	}
	var stateAfterRetry string
	var revAfterRetry int64
	if err := db.QueryRow(`SELECT projection_state, source_revision FROM foghorn.ingest_sessions WHERE id=$1::uuid`, sid1).Scan(&stateAfterRetry, &revAfterRetry); err != nil {
		t.Fatalf("read after retry: %v", err)
	}
	if stateAfterRetry != "active" || revAfterRetry != revAfterFirst {
		t.Fatalf("retry mutated projection state: state=%q rev=%d, want active/rev=%d unchanged", stateAfterRetry, revAfterRetry, revAfterFirst)
	}
}

// A RESUMED retry whose active DB generation finds a divergent newer Redis
// watermark repairs the cache from DB authority. It advances both the session
// and its durable admission-effect fence before publishing above that watermark.
func TestResumedProjectionRepairsWhenRegistryHoldsNewerRevision_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })
	ctx := context.Background()
	logger := logging.NewLogger()
	const node, stream, uuid = "node-resumed-cas", "live+resumed-cas", "resumed-cas-uuid"

	store, _, _ := newTestRedis(t)
	registry := NewStreamRegistry(nil, "cluster-A", time.Minute)
	registry.mu.Lock()
	registry.redisStore = store
	registry.instanceID = "resumed-cas-test"
	registry.mu.Unlock()

	// First pass completes normally: mint, project (CAS applies), confirm.
	sid, outcome, err := CreateIngestSession(ctx, ingA, node, stream, 601, uuid, 1000, nil, "cluster-A", logger)
	if err != nil || outcome != IngestSessionActive {
		t.Fatalf("mint: outcome=%v err=%v", outcome, err)
	}
	applied, resumed, err := ProjectSourceIfCurrent(ctx, registry, ingA, node, stream, 601, uuid, sid, AdmissionEffectIntent{})
	if err != nil || !applied || resumed {
		t.Fatalf("first projection: applied=%v resumed=%v err=%v", applied, resumed, err)
	}

	// A strictly newer source transition lands in the shared registry (e.g. a fence or a newer
	// publisher on another replica whose DB state this probe cannot see in this scenario).
	const newerRevision = sourceProjectionCounterBase + 2_000_000_000
	newer := StreamEntry{InternalName: stream, Locations: map[string]Location{
		"cluster-A": {ClusterID: "cluster-A", SourceActive: true, OwnerNodeID: "node-newer", SourceRevision: newerRevision},
	}}
	change := RegistryChange{InstanceID: "peer", Entity: RegistryEntitySource, Operation: RegistryOpUpsert, Key: stream, SourceRevision: newerRevision}
	if ok, seedErr := store.SetSourceRevisioned(ctx, newer, change, newerRevision); seedErr != nil || !ok {
		t.Fatalf("seed newer Redis source: applied=%v err=%v", ok, seedErr)
	}

	// The retry's first re-publish loses the CAS. The stream-locked repair then
	// advances durable authority above the foreign watermark and converges Redis.
	appliedRetry, resumedRetry, err := ProjectSourceIfCurrent(ctx, registry, ingA, node, stream, 601, uuid, sid, AdmissionEffectIntent{})
	if err != nil {
		t.Fatalf("resumed CAS-loss repair: %v", err)
	}
	if !appliedRetry || !resumedRetry {
		t.Fatalf("resumed repair = applied:%v resumed:%v, want true/true", appliedRetry, resumedRetry)
	}
	var repairedSessionRevision, repairedEffectRevision int64
	if err := db.QueryRow(`SELECT source_revision FROM foghorn.ingest_sessions WHERE id=$1::uuid`, sid).Scan(&repairedSessionRevision); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT source_revision FROM foghorn.ingest_admission_effects WHERE source_generation=$1::uuid`, sid).Scan(&repairedEffectRevision); err != nil {
		t.Fatal(err)
	}
	if repairedSessionRevision <= newerRevision || repairedEffectRevision != repairedSessionRevision {
		t.Fatalf("repaired revisions session=%d effect=%d, want equal and > %d", repairedSessionRevision, repairedEffectRevision, newerRevision)
	}
}

// A settled admission effect may be retention-purged while its ingest remains
// active. Repair advances the active-session authority without recreating
// already-completed side effects or rejecting that publisher.
func TestResumedProjectionRepairAllowsPurgedTerminalEffect_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })
	ctx := context.Background()
	logger := logging.NewLogger()
	const node, stream, triggerUUID = "node-resumed-missing-effect", "live+resumed-missing-effect", "resumed-missing-effect-uuid"

	store, _, _ := newTestRedis(t)
	registry := NewStreamRegistry(nil, "cluster-A", time.Minute)
	registry.mu.Lock()
	registry.redisStore = store
	registry.instanceID = "resumed-missing-effect-test"
	registry.mu.Unlock()

	sessionID, outcome, err := CreateIngestSession(ctx, ingA, node, stream, 602, triggerUUID, 1000, nil, "cluster-A", logger)
	if err != nil || outcome != IngestSessionActive {
		t.Fatalf("mint: outcome=%v err=%v", outcome, err)
	}
	applied, resumed, err := ProjectSourceIfCurrent(ctx, registry, ingA, node, stream, 602, triggerUUID, sessionID, AdmissionEffectIntent{})
	if err != nil || !applied || resumed {
		t.Fatalf("first projection: applied=%v resumed=%v err=%v", applied, resumed, err)
	}
	var originalRevision int64
	if err := conn.QueryRowContext(ctx, `SELECT source_revision FROM foghorn.ingest_sessions WHERE id=$1::uuid`, sessionID).Scan(&originalRevision); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `DELETE FROM foghorn.ingest_admission_effects WHERE source_generation=$1::uuid`, sessionID); err != nil {
		t.Fatal(err)
	}

	const divergentRevision = sourceProjectionCounterBase + 2_100_000_000
	divergent := StreamEntry{InternalName: stream, Locations: map[string]Location{
		"cluster-A": {ClusterID: "cluster-A", SourceActive: true, OwnerNodeID: "node-divergent", SourceRevision: divergentRevision},
	}}
	change := RegistryChange{InstanceID: "peer", Entity: RegistryEntitySource, Operation: RegistryOpUpsert, Key: stream, SourceRevision: divergentRevision}
	if ok, seedErr := store.SetSourceRevisioned(ctx, divergent, change, divergentRevision); seedErr != nil || !ok {
		t.Fatalf("seed divergent Redis source: applied=%v err=%v", ok, seedErr)
	}

	applied, resumed, err = ProjectSourceIfCurrent(ctx, registry, ingA, node, stream, 602, triggerUUID, sessionID, AdmissionEffectIntent{})
	if err != nil || !applied || !resumed {
		t.Fatalf("purged effect repair: applied=%v resumed=%v err=%v", applied, resumed, err)
	}
	var revisionAfter int64
	if err := conn.QueryRowContext(ctx, `SELECT source_revision FROM foghorn.ingest_sessions WHERE id=$1::uuid`, sessionID).Scan(&revisionAfter); err != nil {
		t.Fatal(err)
	}
	if revisionAfter <= divergentRevision || revisionAfter <= originalRevision {
		t.Fatalf("session revision after repair=%d, want > max(%d,%d)", revisionAfter, divergentRevision, originalRevision)
	}
}

// The admission-effects worker with LEG semantics: an obligation whose generation has ENDED runs no
// activation/broadcast (mooted) and supersedes once nothing is owed; an ACTIVE generation's
// obligation runs its owed legs — the broadcast completes locally, the drain leg stays owed until
// Helmsman's DrainStreamResponse acknowledgement marks it, and only then does the row settle.
func TestAdmissionEffectApplyAndSupersede_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })
	ctx := context.Background()
	logger := logging.NewLogger()
	const stream = "live+admission-apply"

	registry := NewStreamRegistry(nil, "cluster-A", time.Minute)

	// Publisher A admits + projects (empty intent → every leg born done), then B replaces it — B's
	// obligation carries A as prior owner (drain leg owed) and a live broadcast.
	sidA, outcomeA, err := CreateIngestSession(ctx, ingA, "node-A", stream, 701, "adm-a", 1000, nil, "cluster-A", logger)
	if err != nil || outcomeA != IngestSessionActive {
		t.Fatalf("mint A: outcome=%v err=%v", outcomeA, err)
	}
	if applied, _, err := ProjectSourceIfCurrent(ctx, registry, ingA, "node-A", stream, 701, "adm-a", sidA, AdmissionEffectIntent{}); err != nil || !applied {
		t.Fatalf("project A: applied=%v err=%v", applied, err)
	}
	if _, err := db.Exec(`UPDATE foghorn.ingest_sessions SET ended_at=NOW() WHERE id=$1::uuid`, sidA); err != nil {
		t.Fatalf("end A: %v", err)
	}
	sidB, outcomeB, err := CreateIngestSession(ctx, ingA, "node-B", stream, 702, "adm-b", 2000, nil, "cluster-A", logger)
	if err != nil || outcomeB != IngestSessionActive {
		t.Fatalf("mint B: outcome=%v err=%v", outcomeB, err)
	}
	peerHints := []AdmissionPeerHint{{ClusterID: "peer-cluster", Addr: "peer:18019", AlwaysOn: true}}
	if applied, _, err := ProjectSourceIfCurrent(ctx, registry, ingA, "node-B", stream, 702, "adm-b", sidB, AdmissionEffectIntent{BroadcastLive: true, PeerHints: peerHints}); err != nil || !applied {
		t.Fatalf("project B: applied=%v err=%v", applied, err)
	}

	effects, err := ClaimAdmissionEffects(ctx, 10, time.Minute, "test-instance")
	if err != nil || len(effects) != 2 {
		t.Fatalf("claim admission effects: count=%d err=%v, want 2", len(effects), err)
	}
	var appliedGens []string
	for _, effect := range effects {
		_, applyErr := ApplyClaimedAdmissionEffect(ctx, effect, func(_ context.Context, e AdmissionEffect) (AdmissionEffectLegResults, error) {
			appliedGens = append(appliedGens, e.SourceGeneration)
			if e.SourceGeneration == sidB && e.PriorOwnerNodeID != "node-A" {
				t.Fatalf("B's obligation prior owner = %q, want node-A", e.PriorOwnerNodeID)
			}
			if e.SourceGeneration == sidB && e.PriorOwnerSourceGeneration != sidA {
				t.Fatalf("B's obligation prior generation = %q, want %q", e.PriorOwnerSourceGeneration, sidA)
			}
			if e.SourceGeneration == sidB && !reflect.DeepEqual(e.PeerHints, peerHints) {
				t.Fatalf("B's durable peer hints = %+v, want %+v", e.PeerHints, peerHints)
			}
			// The broadcast leg completes locally; the drain leg is dispatch-only.
			return AdmissionEffectLegResults{BroadcastDone: true}, nil
		})
		if applyErr != nil {
			t.Fatalf("apply admission effect %s: %v", effect.SourceGeneration, applyErr)
		}
	}
	if len(appliedGens) != 1 || appliedGens[0] != sidB {
		t.Fatalf("callback ran for %v, want only the active generation %s", appliedGens, sidB)
	}
	var stateA, stateB string
	if err := db.QueryRow(`SELECT state FROM foghorn.ingest_admission_effects WHERE source_generation=$1::uuid`, sidA).Scan(&stateA); err != nil {
		t.Fatalf("read A obligation state: %v", err)
	}
	if err := db.QueryRow(`SELECT state FROM foghorn.ingest_admission_effects WHERE source_generation=$1::uuid`, sidB).Scan(&stateB); err != nil {
		t.Fatalf("read B obligation state: %v", err)
	}
	if stateA != "superseded" {
		t.Fatalf("A's obligation state = %q, want superseded (nothing owed, generation ended)", stateA)
	}
	// B stays PENDING: the drain leg is only completed by the Helmsman acknowledgement — dispatch is
	// not completion.
	if stateB != "pending" {
		t.Fatalf("B's obligation state = %q, want pending until the drain acknowledgement", stateB)
	}
	// A DELAYED/DUPLICATED acknowledgement carrying a DIFFERENT (stale) generation must not touch
	// B's obligation — correlation is by the exact generation the response echoes.
	if err := MarkAdmissionDrainDone(ctx, "node-A", sidA); err != nil {
		t.Fatalf("stale drain acknowledgement: %v", err)
	}
	var drainDoneB bool
	if err := db.QueryRow(`SELECT drain_done FROM foghorn.ingest_admission_effects WHERE source_generation=$1::uuid`, sidB).Scan(&drainDoneB); err != nil {
		t.Fatalf("read B drain flag: %v", err)
	}
	if drainDoneB {
		t.Fatal("a stale generation's acknowledgement completed B's drain leg (mis-correlation)")
	}
	// The CORRECT acknowledgement (B's generation) completes the leg; the marker only sets the
	// flag — the WORKER terminalizes on its next pass, under the lock, with the right label.
	if err := MarkAdmissionDrainDone(ctx, "node-A", sidB); err != nil {
		t.Fatalf("drain acknowledgement: %v", err)
	}
	if err := db.QueryRow(`SELECT state, drain_done FROM foghorn.ingest_admission_effects WHERE source_generation=$1::uuid`, sidB).Scan(&stateB, &drainDoneB); err != nil {
		t.Fatalf("re-read B obligation state: %v", err)
	}
	if !drainDoneB || stateB != "pending" {
		t.Fatalf("after the ack: drain_done=%v state=%q, want done flag set and worker-owned terminalization", drainDoneB, stateB)
	}
	if _, err := db.Exec(`UPDATE foghorn.ingest_admission_effects SET leased_until=NULL, lease_token=NULL, next_attempt_at=NOW() WHERE source_generation=$1::uuid`, sidB); err != nil {
		t.Fatalf("release B lease for the terminalizing pass: %v", err)
	}
	final, err := ClaimAdmissionEffects(ctx, 10, time.Minute, "test-instance")
	if err != nil || len(final) != 1 {
		t.Fatalf("claim for terminalizing pass: count=%d err=%v", len(final), err)
	}
	completed, err := ApplyClaimedAdmissionEffect(ctx, final[0], func(context.Context, AdmissionEffect) (AdmissionEffectLegResults, error) {
		t.Fatal("no legs are owed; the callback must not run")
		return AdmissionEffectLegResults{}, nil
	})
	if err != nil || !completed {
		t.Fatalf("terminalizing pass: completed=%v err=%v", completed, err)
	}
	var pushTargetsB []byte
	if err := db.QueryRow(`SELECT state, push_targets FROM foghorn.ingest_admission_effects WHERE source_generation=$1::uuid`, sidB).Scan(&stateB, &pushTargetsB); err != nil {
		t.Fatalf("final read of B: %v", err)
	}
	if stateB != "applied" {
		t.Fatalf("B's final obligation state = %q, want applied", stateB)
	}
	if len(pushTargetsB) != 0 {
		t.Fatal("terminal rows must not retain the push-target payload (embedded credentials)")
	}
}

// An undecodable durable payload poisons ONLY ITS OWN LEG: the leg is settled with diagnostics
// retained in last_error while unrelated valid legs (here the Decklog event) still converge — a
// corrupt push-target intent must not discard a still-owed drain or ingest event.
func TestAdmissionEffectPoisonSettlesLegOnly_RealPG(t *testing.T) {
	if err := ConfigureAdmissionEffectEncryption("test-foghorn-state-key"); err != nil {
		t.Fatal(err)
	}
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })
	ctx := context.Background()
	logger := logging.NewLogger()
	const node, stream, uuid = "node-poison", "live+poison", "poison-uuid"

	registry := NewStreamRegistry(nil, "cluster-A", time.Minute)
	sid, outcome, err := CreateIngestSession(ctx, ingA, node, stream, 801, uuid, 1000, nil, "cluster-A", logger)
	if err != nil || outcome != IngestSessionActive {
		t.Fatalf("mint: outcome=%v err=%v", outcome, err)
	}
	if applied, _, err := ProjectSourceIfCurrent(ctx, registry, ingA, node, stream, 801, uuid, sid, AdmissionEffectIntent{PushTargets: []byte("not-a-proto"), DecklogTrigger: []byte("valid-by-callback")}); err != nil || !applied {
		t.Fatalf("project: applied=%v err=%v", applied, err)
	}
	effects, err := ClaimAdmissionEffects(ctx, 10, time.Minute, "test-instance")
	if err != nil || len(effects) != 1 {
		t.Fatalf("claim: count=%d err=%v", len(effects), err)
	}
	completed, err := ApplyClaimedAdmissionEffect(ctx, effects[0], func(_ context.Context, e AdmissionEffect) (AdmissionEffectLegResults, error) {
		// The processor-side behavior: activation payload undecodable → leg abandoned with a
		// note; the Decklog leg delivers normally.
		return AdmissionEffectLegResults{
			ActivationPoisoned: true,
			PoisonNote:         "push-target payload undecodable: test",
			DecklogDone:        true,
		}, nil
	})
	if err != nil || !completed {
		t.Fatalf("per-leg poison apply: completed=%v err=%v", completed, err)
	}
	var state string
	var lastError sql.NullString
	var payload []byte
	if err := db.QueryRow(`SELECT state, last_error, push_targets FROM foghorn.ingest_admission_effects WHERE source_generation=$1::uuid`, sid).Scan(&state, &lastError, &payload); err != nil {
		t.Fatalf("read settled row: %v", err)
	}
	if state != admissionStateAppliedV2 {
		t.Fatalf("state=%q, want %s (valid legs converged despite the poisoned one)", state, admissionStateAppliedV2)
	}
	if !lastError.Valid || lastError.String == "" {
		t.Fatal("per-leg poison diagnostics must be retained in last_error")
	}
	if len(payload) != 0 {
		t.Fatal("settled rows must not retain the push-target payload")
	}
}

// A completed multistream activation is process-local proof, not permanent proof. A newer
// authenticated Helmsman connection re-arms the exact target set for every still-open publisher,
// rejects a delayed acknowledgement from the retired connection, and retains the payload only
// until the generation ends.
func TestActivePushTargetsRearmAcrossNodeReconnect_RealPG(t *testing.T) {
	if err := ConfigureAdmissionEffectEncryption("test-foghorn-state-key"); err != nil {
		t.Fatal(err)
	}
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })
	ctx := context.Background()
	logger := logging.NewLogger()
	const node, stream, triggerUUID = "node-output-restart", "live+output-restart", "output-restart-trigger"
	const initialFence, reconnectFence int64 = 10, 11
	exactTargets := []byte("exact-admitted-target-set")

	registry := NewStreamRegistry(nil, "cluster-A", time.Minute)
	sid, outcome, err := CreateIngestSession(ctx, ingA, node, stream, 901, triggerUUID, 1000, nil, "cluster-A", logger)
	if err != nil || outcome != IngestSessionActive {
		t.Fatalf("mint: outcome=%v err=%v", outcome, err)
	}
	if applied, _, projectErr := ProjectSourceIfCurrent(ctx, registry, ingA, node, stream, 901, triggerUUID, sid, AdmissionEffectIntent{PushTargets: exactTargets}); projectErr != nil || !applied {
		t.Fatalf("project: applied=%v err=%v", applied, projectErr)
	}

	effects, err := ClaimAdmissionEffects(ctx, 1, time.Minute, "test-instance")
	if err != nil || len(effects) != 1 {
		t.Fatalf("claim initial activation: count=%d err=%v", len(effects), err)
	}
	if err := MarkAdmissionActivationDone(ctx, node, sid, initialFence); err != nil {
		t.Fatalf("ack initial activation: %v", err)
	}
	completed, err := ApplyClaimedAdmissionEffect(ctx, effects[0], func(context.Context, AdmissionEffect) (AdmissionEffectLegResults, error) {
		t.Fatal("the acknowledged activation owes no callback work")
		return AdmissionEffectLegResults{}, nil
	})
	if err != nil || !completed {
		t.Fatalf("settle initial activation: completed=%v err=%v", completed, err)
	}

	var state string
	var activationDone bool
	var connectionFence int64
	var retained []byte
	if err := db.QueryRow(`SELECT state, activation_done, activation_connection_fence, push_targets
		FROM foghorn.ingest_admission_effects WHERE source_generation=$1::uuid`, sid).
		Scan(&state, &activationDone, &connectionFence, &retained); err != nil {
		t.Fatalf("read settled activation: %v", err)
	}
	if state != admissionStateAppliedV2 || !activationDone || connectionFence != initialFence || !fieldcrypto.IsEncrypted(string(retained)) || reflect.DeepEqual(retained, exactTargets) {
		t.Fatalf("initial activation = state:%q done:%v fence:%d payload:%q", state, activationDone, connectionFence, retained)
	}

	rearmed, err := RequeueActivePushTargetActivationsForNode(ctx, node, reconnectFence, "test-instance")
	if err != nil || rearmed != 1 {
		t.Fatalf("rearm on reconnect: rows=%d err=%v", rearmed, err)
	}
	if err := MarkAdmissionActivationDone(ctx, node, sid, initialFence); err != nil {
		t.Fatalf("record delayed retired-connection ACK: %v", err)
	}
	if err := db.QueryRow(`SELECT state, activation_done, activation_connection_fence
		FROM foghorn.ingest_admission_effects WHERE source_generation=$1::uuid`, sid).
		Scan(&state, &activationDone, &connectionFence); err != nil {
		t.Fatalf("read rearmed activation: %v", err)
	}
	if state != admissionStatePendingV2 || activationDone || connectionFence != reconnectFence {
		t.Fatalf("retired ACK crossed reconnect fence: state=%q done=%v fence=%d", state, activationDone, connectionFence)
	}
	if err := MarkAdmissionActivationDone(ctx, node, sid, reconnectFence); err != nil {
		t.Fatalf("ack replay on current connection: %v", err)
	}
	replayed, err := ClaimAdmissionEffects(ctx, 1, time.Minute, "test-instance")
	if err != nil || len(replayed) != 1 {
		t.Fatalf("claim replayed activation: count=%d err=%v", len(replayed), err)
	}
	completed, err = ApplyClaimedAdmissionEffect(ctx, replayed[0], func(context.Context, AdmissionEffect) (AdmissionEffectLegResults, error) {
		t.Fatal("the replay acknowledgement already completed the only leg")
		return AdmissionEffectLegResults{}, nil
	})
	if err != nil || !completed {
		t.Fatalf("settle replayed activation: completed=%v err=%v", completed, err)
	}

	if _, err := db.Exec(`UPDATE foghorn.ingest_admission_effects SET updated_at=NOW()-INTERVAL '1 hour'
		WHERE source_generation=$1::uuid`, sid); err != nil {
		t.Fatalf("age active activation: %v", err)
	}
	if purged, purgeErr := PurgeTerminalAdmissionEffects(ctx, time.Minute); purgeErr != nil || purged != 0 {
		t.Fatalf("active output authority was purgeable: rows=%d err=%v", purged, purgeErr)
	}
	if _, err := db.Exec(`UPDATE foghorn.ingest_sessions SET ended_at=NOW() WHERE id=$1::uuid`, sid); err != nil {
		t.Fatalf("end publisher generation: %v", err)
	}
	if purged, purgeErr := PurgeTerminalAdmissionEffects(ctx, time.Minute); purgeErr != nil || purged != 1 {
		t.Fatalf("ended output authority was not purged: rows=%d err=%v", purged, purgeErr)
	}
}

// An acknowledgement must complete IMMEDIATELY while the worker is mid-dispatch: the worker never
// holds the obligation row or stream advisory lock across external I/O (phase split), so an ack
// arriving during a slow leg cannot time out behind the worker's transaction — the failure mode
// that previously re-dispatched nuke_stream after its own successful drain.
func TestAdmissionAckCompletesWhileWorkerPaused_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })
	ctx := context.Background()
	logger := logging.NewLogger()
	const stream = "live+paused-ack"

	registry := NewStreamRegistry(nil, "cluster-A", time.Minute)
	sidA, outcomeA, err := CreateIngestSession(ctx, ingA, "node-A", stream, 901, "paused-a", 1000, nil, "cluster-A", logger)
	if err != nil || outcomeA != IngestSessionActive {
		t.Fatalf("mint A: outcome=%v err=%v", outcomeA, err)
	}
	if applied, _, err := ProjectSourceIfCurrent(ctx, registry, ingA, "node-A", stream, 901, "paused-a", sidA, AdmissionEffectIntent{}); err != nil || !applied {
		t.Fatalf("project A: applied=%v err=%v", applied, err)
	}
	if _, err := db.Exec(`UPDATE foghorn.ingest_sessions SET ended_at=NOW() WHERE id=$1::uuid`, sidA); err != nil {
		t.Fatalf("end A: %v", err)
	}
	sidB, outcomeB, err := CreateIngestSession(ctx, ingA, "node-B", stream, 902, "paused-b", 2000, nil, "cluster-A", logger)
	if err != nil || outcomeB != IngestSessionActive {
		t.Fatalf("mint B: outcome=%v err=%v", outcomeB, err)
	}
	if applied, _, err := ProjectSourceIfCurrent(ctx, registry, ingA, "node-B", stream, 902, "paused-b", sidB, AdmissionEffectIntent{}); err != nil || !applied {
		t.Fatalf("project B: applied=%v err=%v", applied, err)
	}

	// Claim B's obligation (A's settles as superseded during its own claim).
	effects, err := ClaimAdmissionEffects(ctx, 10, time.Minute, "test-instance")
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	var effectB AdmissionEffect
	for _, e := range effects {
		if e.SourceGeneration == sidB {
			effectB = e
		} else if _, aerr := ApplyClaimedAdmissionEffect(ctx, e, func(context.Context, AdmissionEffect) (AdmissionEffectLegResults, error) {
			return AdmissionEffectLegResults{}, nil
		}); aerr != nil {
			t.Fatalf("settle sibling obligation: %v", aerr)
		}
	}
	if effectB.ID == 0 {
		t.Fatal("B's obligation not claimed")
	}

	paused := make(chan struct{})
	release := make(chan struct{})
	applyDone := make(chan error, 1)
	go func() {
		_, aerr := ApplyClaimedAdmissionEffect(ctx, effectB, func(context.Context, AdmissionEffect) (AdmissionEffectLegResults, error) {
			close(paused)
			<-release // a deliberately slow dispatch leg
			return AdmissionEffectLegResults{}, nil
		})
		applyDone <- aerr
	}()
	<-paused

	// The acknowledgement must land while the callback is paused — bounded well under any lock
	// timeout, because the row is not locked during phase 2.
	ackCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	ackErr := MarkAdmissionDrainDone(ackCtx, "node-A", sidB)
	cancel()
	if ackErr != nil {
		t.Fatalf("ack while worker paused: %v", ackErr)
	}
	var drainDone bool
	if err := db.QueryRow(`SELECT drain_done FROM foghorn.ingest_admission_effects WHERE source_generation=$1::uuid`, sidB).Scan(&drainDone); err != nil {
		t.Fatalf("read drain flag mid-pause: %v", err)
	}
	if !drainDone {
		t.Fatal("the ack must set the drain flag while the worker is mid-dispatch")
	}

	close(release)
	if aerr := <-applyDone; aerr != nil {
		t.Fatalf("apply after resume: %v", aerr)
	}
	var stateB string
	if err := db.QueryRow(`SELECT state FROM foghorn.ingest_admission_effects WHERE source_generation=$1::uuid`, sidB).Scan(&stateB); err != nil {
		t.Fatalf("final read: %v", err)
	}
	if stateB != "applied" {
		t.Fatalf("state=%q, want applied (phase 3 merged the mid-flight ack)", stateB)
	}
}

// Durable authority routing: a deferral records the AUTHORITY's instance as the row's claim
// affinity — only that instance may claim it (so N-1 wrong replicas cannot alternate claims while
// the authority never wins the SKIP LOCKED race), with a staleness escape that reopens the row to
// everyone if the affinity target dies or the authority moves.
func TestAdmissionClaimAffinityRoutesToAuthority_RealPG(t *testing.T) {
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })
	ctx := context.Background()
	logger := logging.NewLogger()
	const node, stream, uuid = "node-deferred", "live+deferred", "deferred-uuid"

	registry := NewStreamRegistry(nil, "cluster-A", time.Minute)
	sid, outcome, err := CreateIngestSession(ctx, ingA, node, stream, 951, uuid, 1000, nil, "cluster-A", logger)
	if err != nil || outcome != IngestSessionActive {
		t.Fatalf("mint: outcome=%v err=%v", outcome, err)
	}
	if applied, _, err := ProjectSourceIfCurrent(ctx, registry, ingA, node, stream, 951, uuid, sid, AdmissionEffectIntent{BroadcastLive: true}); err != nil || !applied {
		t.Fatalf("project: applied=%v err=%v", applied, err)
	}

	claimed, err := ClaimAdmissionEffects(ctx, 10, time.Minute, "instance-wrong-1")
	if err != nil || len(claimed) != 1 {
		t.Fatalf("initial claim: count=%d err=%v", len(claimed), err)
	}
	// Defer with the AUTHORITY recorded (e.g. the federation leader's instance).
	if err := ReleaseAdmissionEffectNotOwner(ctx, claimed[0], "instance-authority"); err != nil {
		t.Fatalf("defer: %v", err)
	}
	// NEITHER wrong replica can claim while the affinity is fresh.
	for _, wrong := range []string{"instance-wrong-1", "instance-wrong-2"} {
		got, err := ClaimAdmissionEffects(ctx, 10, time.Minute, wrong)
		if err != nil {
			t.Fatalf("wrong-instance claim (%s): %v", wrong, err)
		}
		if len(got) != 0 {
			t.Fatalf("non-authority %s claimed an affinity-routed row (%d rows)", wrong, len(got))
		}
	}
	// The AUTHORITY claims it (and the claim clears the affinity).
	got, err := ClaimAdmissionEffects(ctx, 10, time.Minute, "instance-authority")
	if err != nil || len(got) != 1 {
		t.Fatalf("authority claim: count=%d err=%v", len(got), err)
	}
	// Staleness escape: a dead authority must not strand the row. Re-defer, age the affinity past
	// the escape window, and any instance may claim again.
	if err := ReleaseAdmissionEffectNotOwner(ctx, got[0], "instance-departed"); err != nil {
		t.Fatalf("re-defer: %v", err)
	}
	if _, err := db.Exec(`UPDATE foghorn.ingest_admission_effects SET updated_at = NOW() - INTERVAL '11 seconds' WHERE source_generation=$1::uuid`, sid); err != nil {
		t.Fatalf("age affinity: %v", err)
	}
	escaped, err := ClaimAdmissionEffects(ctx, 10, time.Minute, "instance-wrong-1")
	if err != nil || len(escaped) != 1 {
		t.Fatalf("stale-affinity escape claim: count=%d err=%v", len(escaped), err)
	}
}
