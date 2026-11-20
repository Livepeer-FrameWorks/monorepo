//go:build schema_verify

package control

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"
	"time"

	"frameworks/api_balancing/internal/database/foghorndb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"github.com/google/uuid"
)

func installPullSessionStore(t *testing.T, conn *sql.DB) *StreamRegistry {
	t.Helper()
	previous, previousRegistry := db, StreamRegistryInstance
	SetDB(conn)
	r := NewStreamRegistry(nil, "cell-a", time.Minute)
	StreamRegistryInstance = r
	t.Cleanup(func() { SetDB(previous); StreamRegistryInstance = previousRegistry })
	return r
}

func TestPullIngestSessions_RealPG(t *testing.T) { testPullIngestSessions(t, startRealPG(t)) }
func TestPullIngestSessions_RealYugabyte(t *testing.T) {
	conn, _, ok := dockerpg.OpenSharedYugabyteBaselineWithDSN(t, "pull_sessions", "foghorn")
	if !ok {
		t.Fatal("requires shared Yugabyte fixture")
	}
	testPullIngestSessions(t, conn)
}
func testPullIngestSessions(t *testing.T, conn *sql.DB) {
	t.Helper()
	reg := installPullSessionStore(t, conn)
	ctx := t.Context()
	tenant, stream := uuid.NewString(), uuid.NewString()
	const internal = "pull-session-proof"
	reg.UpsertLocalSource(StreamEntry{TenantID: tenant, StreamID: stream, InternalName: internal, IngestMode: IngestPull})
	for _, node := range []string{"source-a", "source-b"} {
		if allowed, err := AdmitPullUpstream(ctx, tenant, stream, internal, node, "cluster-a", 1); err != nil || !allowed {
			t.Fatalf("admit %s: allowed=%v err=%v", node, allowed, err)
		}
	}
	if allowed, err := AdmitPullUpstream(ctx, tenant, uuid.NewString(), "over-cap", "source-c", "cluster-a", 1); err != nil || allowed {
		t.Fatalf("distinct pull exceeded cap: allowed=%v err=%v", allowed, err)
	}
	now := time.Now().Add(-time.Second).UnixMilli()
	base := PullIngestObservation{TenantID: tenant, StreamID: stream, InternalName: internal, NodeID: "source-a", ClusterID: "cluster-a", BufferPID: 4242, EventID: uuid.NewString(), EventMillis: now}
	// FULL and periodic backstop can arrive at different replicas at once.
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Go(func() { _, err := ObservePlayablePullBuffer(ctx, base); results <- err })
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	second := base
	second.NodeID = "source-b"
	second.BufferPID = 4343
	second.EventMillis++
	if minted, err := ObservePlayablePullBuffer(ctx, second); err != nil || !minted {
		t.Fatalf("second buffer: minted=%v err=%v", minted, err)
	}
	counts := func(wantConnected, wantLive, wantIdle int) {
		t.Helper()
		var connected, live, idle int
		if err := conn.QueryRowContext(ctx, `SELECT count(*) FILTER(WHERE event_type='stream.connected'),count(*) FILTER(WHERE event_type='stream.live'),count(*) FILTER(WHERE event_type='stream.idle') FROM foghorn.domain_event_outbox WHERE tenant_id=$1::uuid AND aggregate_id=$2`, tenant, stream).Scan(&connected, &live, &idle); err != nil {
			t.Fatal(err)
		}
		if connected != wantConnected || live != wantLive || idle != wantIdle {
			t.Fatalf("events connected/live/idle=%d/%d/%d want %d/%d/%d", connected, live, idle, wantConnected, wantLive, wantIdle)
		}
	}
	counts(1, 1, 0)

	insertDVRForSource(t, "pull-dvr-source-b", tenant, internal, "source-b", "")
	if err := StopDVRForEndedSource(internal, tenant, "source-b", logging.NewLogger()); err != nil {
		t.Fatal(err)
	}
	if status := dvrStatus(t, "pull-dvr-source-b"); status != "recording" {
		t.Errorf("delayed end stopped a replacement pull DVR: status=%q", status)
	}
	claims, err := activeIngestSessionClaims(ctx)
	if err != nil || len(claims) != 2 {
		t.Fatalf("active pull renewal claims=%v err=%v", claims, err)
	}
	for _, claim := range claims {
		if claim.ClaimToken != "pull:"+stream || claim.ClusterID != "cluster-a" {
			t.Fatalf("pull renewal identity=%+v", claim)
		}
	}
	if n, err := EndPullIngestSessionsForStreamEnd(ctx, tenant, "source-a", internal, now+10, logging.NewLogger()); err != nil || n != 1 {
		t.Fatalf("first end: n=%d err=%v", n, err)
	}
	counts(1, 1, 0)
	if n, err := EndPullIngestSessionsForStreamEnd(ctx, tenant, "source-b", internal, now+20, logging.NewLogger()); err != nil || n != 1 {
		t.Fatalf("last end: n=%d err=%v", n, err)
	}
	counts(1, 1, 1)
	publicSession := ""
	for _, kind := range []string{"stream.connected", "stream.live", "stream.idle", "stream.ended"} {
		rows := domainRows(t, conn, kind)
		if len(rows) != 1 {
			t.Fatalf("%s rows=%d, want one cell event", kind, len(rows))
		}
		_, message, err := events.Decode(kind, rows[0].payload)
		if err != nil {
			t.Fatal(err)
		}
		id := message.(interface{ GetSessionId() string }).GetSessionId()
		if id == "" {
			t.Fatalf("%s has no public session identity", kind)
		}
		if publicSession == "" {
			publicSession = id
		} else if publicSession != id {
			t.Fatalf("two pull nodes split lifecycle identity: %s has %s, want %s", kind, id, publicSession)
		}
	}

	if status := dvrStatus(t, "pull-dvr-source-b"); status != "stopping" {
		t.Errorf("ended pull buffer did not queue its DVR stop: status=%q", status)
	}
	if minted, err := ObservePlayablePullBuffer(ctx, base); err != nil || minted {
		t.Fatalf("ended buffer reminted: minted=%v err=%v", minted, err)
	}
	stale := second
	stale.BufferPID++
	stale.EventMillis = now + 20
	if minted, err := ObservePlayablePullBuffer(ctx, stale); err != nil || minted {
		t.Fatalf("pre-end sample reminted: minted=%v err=%v", minted, err)
	}
	fresh := stale
	fresh.EventMillis = now + 30
	if minted, err := ObservePlayablePullBuffer(ctx, fresh); err != nil || !minted {
		t.Fatalf("new lifetime: minted=%v err=%v", minted, err)
	}
	counts(2, 2, 1)
	// A snapshot begun before this buffer's FULL cannot retire it as absent.
	if n, err := ReconcileNodePullSessions(ctx, fresh.NodeID, nil, time.UnixMilli(now+25), nil, logging.NewLogger()); err != nil || n != 0 {
		t.Fatalf("older inventory retired newer buffer: n=%d err=%v", n, err)
	}
	inventory := []*ipcpb.LiveSourceBuffer{{RuntimeName: "pull+" + internal, BufferPid: fresh.BufferPID}}
	if n, err := ReconcileNodePullSessions(ctx, fresh.NodeID, inventory, time.UnixMilli(now+40), nil, logging.NewLogger()); err != nil || n != 0 {
		t.Fatalf("matching inventory ended buffer: n=%d err=%v", n, err)
	}
	if n, err := ReconcileNodePullSessions(ctx, fresh.NodeID, nil, time.UnixMilli(now+40), func() bool { return false }, logging.NewLogger()); err != nil || n != 0 {
		t.Fatalf("retired registration changed buffer: n=%d err=%v", n, err)
	}
	if n, err := ReconcileNodePullSessions(ctx, fresh.NodeID, nil, time.UnixMilli(now+40), nil, logging.NewLogger()); err != nil || n != 1 {
		t.Fatalf("authoritative absence: n=%d err=%v", n, err)
	}
	counts(2, 2, 2)
	relay := base
	relay.NodeID = "relay-node"
	relay.BufferPID = 9000
	relay.EventMillis = now + 50
	markReplicatingForTest(t, reg, internal, "other-cell", "dtsc://origin/pull+"+internal, relay.NodeID, "https://relay.example", "origin-node")
	if minted, err := ObservePlayablePullBuffer(ctx, relay); err != nil || minted {
		t.Fatalf("relay minted direct ingest: minted=%v err=%v", minted, err)
	}
	counts(2, 2, 2)
	lost := PullIngestObservation{TenantID: tenant, StreamID: uuid.NewString(), InternalName: "lost-direct-pull", NodeID: "lost-direct-node", ClusterID: "cluster-a", BufferPID: 9999, EventMillis: time.Now().UnixMilli()}
	reg.UpsertLocalSource(StreamEntry{TenantID: tenant, StreamID: lost.StreamID, InternalName: lost.InternalName, IngestMode: IngestPull})
	if minted, err := ObservePlayablePullBuffer(ctx, lost); err != nil || !minted {
		t.Fatalf("lost-node fixture minted=%v err=%v", minted, err)
	}
	insertDVRForSource(t, "pull-dvr-lost", tenant, lost.InternalName, lost.NodeID, "")
	clock := time.Now()
	absent := IngestNodeAbsence{lost.NodeID: clock.Add(-IngestNodeLostAfter - time.Second)}
	deps := IngestNodeLostDeps{Present: func(context.Context, string) (bool, error) { return false, nil }, Guard: func(context.Context, string) (func(), error) { return func() {}, nil }, Evidence: func(context.Context, string, []string) (string, string, bool, error) { return "", "", false, nil }}
	if ended, err := ReapLostNodeIngestSessionsOnce(ctx, deps, absent, clock, logging.NewLogger()); err != nil || ended != 1 {
		t.Fatalf("lost pull node ended=%d err=%v", ended, err)
	}
	if status := dvrStatus(t, "pull-dvr-lost"); status != "stopping" {
		t.Errorf("lost pull node did not durably stop its DVR: status=%q", status)
	}
	var idle int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM foghorn.domain_event_outbox WHERE tenant_id=$1::uuid AND aggregate_id=$2 AND event_type='stream.idle'`, tenant, lost.StreamID).Scan(&idle); err != nil || idle != 1 {
		t.Fatalf("lost pull idle=%d err=%v", idle, err)
	}
}

func TestPullBufferPIDReuse_RealPG(t *testing.T) {
	testPullBufferPIDReuse(t, startRealPG(t))
}

func TestPullBufferPIDReuse_RealYugabyte(t *testing.T) {
	conn, _, ok := dockerpg.OpenSharedYugabyteBaselineWithDSN(t, "pull_pid_reuse", "foghorn")
	if !ok {
		t.Fatal("requires shared Yugabyte fixture")
	}
	testPullBufferPIDReuse(t, conn)
}

func testPullBufferPIDReuse(t *testing.T, conn *sql.DB) {
	t.Helper()
	reg := installPullSessionStore(t, conn)
	tenant, stream := uuid.NewString(), uuid.NewString()
	internal := "pid-reuse-" + stream
	reg.UpsertLocalSource(StreamEntry{TenantID: tenant, StreamID: stream, InternalName: internal, IngestMode: IngestPull})
	now := time.Now().Add(-time.Second).UnixMilli()
	observation := PullIngestObservation{TenantID: tenant, StreamID: stream, InternalName: internal, NodeID: "pid-reuse-node", ClusterID: "cluster-a", BufferPID: 42, EventID: uuid.NewString(), EventMillis: now}
	if minted, err := ObservePlayablePullBuffer(t.Context(), observation); err != nil || !minted {
		t.Fatalf("initial lifetime: minted=%v error=%v", minted, err)
	}
	if _, err := EndPullIngestSessionsForStreamEnd(t.Context(), tenant, observation.NodeID, internal, now+10, logging.NewLogger()); err != nil {
		t.Fatal(err)
	}
	stale := observation
	stale.EventID, stale.EventMillis = "", now+20
	if minted, err := ObservePlayablePullBuffer(t.Context(), stale); err != nil || minted {
		t.Fatalf("freshly sampled retired process reminted without a new buffer or admission: %v %v", minted, err)
	}
	restarted := observation
	restarted.EventID, restarted.EventMillis = uuid.NewString(), now+30
	if minted, err := ObservePlayablePullBuffer(t.Context(), restarted); err != nil || !minted {
		t.Fatalf("a new FULL after PID reuse cannot mint its buffer: minted=%v error=%v", minted, err)
	}
	if minted, err := ObservePlayablePullBuffer(t.Context(), restarted); err != nil || minted {
		t.Fatalf("replayed new FULL minted twice: %v %v", minted, err)
	}
	if _, err := EndPullIngestSessionsForStreamEnd(t.Context(), tenant, observation.NodeID, internal, now+40, logging.NewLogger()); err != nil {
		t.Fatal(err)
	}
	if allowed, err := AdmitPullUpstream(t.Context(), tenant, stream, internal, observation.NodeID, observation.ClusterID, 1); err != nil || !allowed {
		t.Fatalf("new source dial after PID reuse: %v %v", allowed, err)
	}
	healed := observation
	healed.EventID, healed.EventMillis = "", now+50
	if minted, err := ObservePlayablePullBuffer(t.Context(), healed); err != nil || !minted {
		t.Fatalf("new admission cannot heal a missed FULL after PID reuse: %v %v", minted, err)
	}
	delayedFull := observation
	delayedFull.EventID, delayedFull.EventMillis = uuid.NewString(), now+45
	if minted, err := ObservePlayablePullBuffer(t.Context(), delayedFull); err != nil || minted {
		t.Fatalf("FULL sampled before its healing report duplicated the buffer: %v %v", minted, err)
	}
	crashed := observation
	crashed.EventID, crashed.EventMillis = uuid.NewString(), now+60
	if minted, err := ObservePlayablePullBuffer(t.Context(), crashed); err != nil || !minted {
		t.Fatalf("new FULL cannot replace the unended lifetime of a reused PID: %v %v", minted, err)
	}
	var total, open int
	if err := conn.QueryRowContext(t.Context(), `SELECT count(*),count(*) FILTER(WHERE ended_at IS NULL) FROM foghorn.pull_ingest_sessions WHERE tenant_id=$1 AND stream_id=$2`, tenant, stream).Scan(&total, &open); err != nil || total != 4 || open != 1 {
		t.Fatalf("buffer lifetimes were collapsed or duplicated: total=%d open=%d error=%v", total, open, err)
	}
}

func TestPullAndPushShareTenantStreamCap_RealPG(t *testing.T) {
	testPullAndPushShareTenantStreamCap(t, startRealPG(t))
}
func TestPullAndPushShareTenantStreamCap_RealYugabyte(t *testing.T) {
	conn, _, ok := dockerpg.OpenSharedYugabyteBaselineWithDSN(t, "pull_capacity", "foghorn")
	if !ok {
		t.Fatal("requires shared Yugabyte fixture")
	}
	testPullAndPushShareTenantStreamCap(t, conn)
}
func testPullAndPushShareTenantStreamCap(t *testing.T, conn *sql.DB) {
	t.Helper()
	installPullSessionStore(t, conn)
	ctx := context.Background()
	logger := logging.NewLogger()
	tenant := uuid.NewString()
	if allowed, err := AdmitPullUpstream(ctx, tenant, uuid.NewString(), "capacity-pull", "pull-node", "cluster-a", 1); err != nil || !allowed {
		t.Fatalf("pull admission allowed=%v err=%v", allowed, err)
	}
	if id, outcome, err := CreateIngestSession(ctx, tenant, "push-node", "capacity-push", 8000, uuid.NewString(), time.Now().UnixMilli(), nil, "cluster-a", logger, IngestAuthoritySnapshot{CapacityMaxStreams: 1}); err != nil || outcome != IngestSessionRejectedCapacity || id != "" {
		t.Fatalf("legacy push bypassed pull cap: id=%q outcome=%v err=%v", id, outcome, err)
	}
	request := IngestSessionRequest{TenantID: tenant, StreamID: uuid.NewString(), InternalName: "publisher-capacity-push", NodeID: "publisher-node", TriggerUUID: uuid.NewString(), ConnectorPID: 8100, StartedAtMillis: time.Now().UnixMilli(), IngestClusterID: "cluster-a", Authority: &IngestAuthoritySnapshot{CapacityMaxStreams: 1}}
	if prepared, err := NewPublisherAdmissionRepository(conn).Prepare(ctx, request, logger); err != nil || prepared.Outcome != IngestSessionRejectedCapacity {
		t.Fatalf("publisher admission bypassed pull cap: result=%+v err=%v", prepared, err)
	}
	// Reverse direction: an existing push consumes the same cell-local budget.
	other := uuid.NewString()
	seedOpenIngestSession(t, other, "existing-push", "existing-stream", uuid.NewString(), 9000, time.Now().UnixMilli())
	if allowed, err := AdmitPullUpstream(ctx, other, uuid.NewString(), "blocked-pull", "pull-node", "cluster-a", 1); err != nil || allowed {
		t.Fatalf("pull bypassed push cap: allowed=%v err=%v", allowed, err)
	}
	// Pending dials release their slot if no buffer binds within the input wait.
	if _, err := conn.ExecContext(ctx, `UPDATE foghorn.pull_ingest_sessions SET started_at=NOW()-INTERVAL '61 seconds' WHERE tenant_id=$1::uuid`, tenant); err != nil {
		t.Fatal(err)
	}
	if err := ReapLapsedPullAdmissions(ctx, logger); err != nil {
		t.Fatal(err)
	}
	if allowed, err := AdmitPullUpstream(ctx, tenant, uuid.NewString(), "capacity-recovered", "another-node", "cluster-a", 1); err != nil || !allowed {
		t.Fatalf("lapsed dial retained slot: allowed=%v err=%v", allowed, err)
	}
	q := foghorndb.New(conn)
	count, err := q.CountActiveTenantIngestSessions(ctx, tenant)
	if err != nil || count != 1 {
		t.Fatalf("shared count=%d err=%v", count, err)
	}
	concurrentTenant := uuid.NewString()
	barrier := make(chan struct{})
	type admissionResult struct {
		accepted bool
		err      error
	}
	results := make(chan admissionResult, 3)
	actions := []func() (bool, error){
		func() (bool, error) {
			return AdmitPullUpstream(ctx, concurrentTenant, uuid.NewString(), "concurrent-pull-a", "concurrent-node-a", "cluster-a", 1)
		},
		func() (bool, error) {
			return AdmitPullUpstream(ctx, concurrentTenant, uuid.NewString(), "concurrent-pull-b", "concurrent-node-b", "cluster-a", 1)
		},
		func() (bool, error) {
			req := IngestSessionRequest{TenantID: concurrentTenant, StreamID: uuid.NewString(), InternalName: "concurrent-push", NodeID: "concurrent-push-node", TriggerUUID: uuid.NewString(), ConnectorPID: 9100, StartedAtMillis: time.Now().UnixMilli(), IngestClusterID: "cluster-a", Authority: &IngestAuthoritySnapshot{CapacityMaxStreams: 1}}
			prepared, err := NewPublisherAdmissionRepository(conn).Prepare(ctx, req, logger)
			if err == nil && prepared.Outcome != IngestSessionActive && prepared.Outcome != IngestSessionRejectedCapacity {
				return false, fmt.Errorf("unexpected concurrent push outcome %v", prepared.Outcome)
			}
			return prepared.Outcome == IngestSessionActive, err
		},
	}
	var waiting sync.WaitGroup
	for _, action := range actions {
		waiting.Go(func() { <-barrier; accepted, err := action(); results <- admissionResult{accepted, err} })
	}
	close(barrier)
	waiting.Wait()
	close(results)
	accepted := 0
	for result := range results {
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.accepted {
			accepted++
		}
	}
	if accepted != 1 {
		t.Fatalf("two concurrent pulls and a push admitted %d streams under cap 1", accepted)
	}
	if count, err := q.CountActiveTenantIngestSessions(ctx, concurrentTenant); err != nil || count != 1 {
		t.Fatalf("concurrent durable count=%d err=%v", count, err)
	}

}
