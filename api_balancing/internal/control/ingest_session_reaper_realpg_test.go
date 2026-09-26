//go:build schema_verify

package control

import (
	"context"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// seedOpenIngestSession inserts one active session (production CreateIngestSession is exercised
// elsewhere; here we only need rows for the lifecycle passes to evaluate).
func seedOpenIngestSession(t *testing.T, tenant, node, stream, uuid string, pid, millis int64) string {
	t.Helper()
	var id string
	if err := db.QueryRow(`
		INSERT INTO foghorn.ingest_sessions
			(tenant_id, node_id, stream_internal_name, connector_pid, start_trigger_uuid, started_at_unix_millis)
		VALUES ($1::uuid, $2, $3, $4, $5, $6)
		RETURNING id::text
	`, tenant, node, stream, pid, uuid, millis).Scan(&id); err != nil {
		t.Fatalf("seed ingest session: %v", err)
	}
	return id
}

// projectAged marks a session's source projection confirmed and ages it, so it is an established
// publisher from before any registration cutoff taken now.
func projectAged(t *testing.T, sessionID string, age time.Duration) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), `
		UPDATE foghorn.ingest_sessions
		   SET projection_state='active', started_at=NOW() - ($2::bigint * INTERVAL '1 millisecond')
		 WHERE id=$1::uuid
	`, sessionID, age.Milliseconds()); err != nil {
		t.Fatalf("project and age session: %v", err)
	}
}

func sessionEnded(t *testing.T, id string) (bool, string) {
	t.Helper()
	var ended bool
	var reason string
	if err := db.QueryRow(`
		SELECT ended_at IS NOT NULL, COALESCE(ended_reason,'')
		  FROM foghorn.ingest_sessions WHERE id = $1::uuid
	`, id).Scan(&ended, &reason); err != nil {
		t.Fatalf("read session state: %v", err)
	}
	return ended, reason
}

func offlineEffectCount(t *testing.T, tenant, stream string) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(), `
		SELECT COUNT(*) FROM foghorn.ingest_offline_effects
		 WHERE tenant_id=$1::uuid AND stream_internal_name=$2
	`, tenant, stream).Scan(&n); err != nil {
		t.Fatalf("count offline effects: %v", err)
	}
	return n
}

func useRealPG(t *testing.T) {
	t.Helper()
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })
}

// useConnOwnerStore wires the production conn_owner store (miniredis) so NodeRetireGuardLookup reads
// real presence. A node is absent until connectNode acquires its conn_owner.
func useConnOwnerStore(t *testing.T) (connectNode func(nodeID string)) {
	t.Helper()
	store, _ := newTestStore(t)
	setCommandRelay(t, buildRelay(t, store, "inst-self", "10.0.0.1:9090", &mockRelayPool{}))
	var fence int64
	return func(nodeID string) {
		fence++
		acquired, err := store.AcquireConnOwnerFenced(context.Background(), nodeID, "inst-peer", "10.0.0.2:9090", fence)
		if err != nil || !acquired {
			t.Fatalf("connect %s: acquired=%v err=%v", nodeID, acquired, err)
		}
	}
}

type recordedDrain struct {
	nodeID string
	req    *ipcpb.DrainStreamRequest
}

func recordDrains(out *[]recordedDrain) NodeIngestDrainFunc {
	return func(_ context.Context, nodeID string, req *ipcpb.DrainStreamRequest) error {
		*out = append(*out, recordedDrain{nodeID: nodeID, req: req})
		return nil
	}
}

// A re-registration listing the generation keeps its session exactly as it was.
func TestReregisterKeepsReportedLiveSession_RealPG(t *testing.T) {
	useRealPG(t)
	ctx := context.Background()
	const node, stream = "node-returns", "live+returns"
	sessionID := seedOpenIngestSession(t, ingA, node, stream, "u-returns", 200, 1000)
	projectAged(t, sessionID, 10*time.Minute)

	cutoff, err := IngestRegistrationCutoff(ctx)
	if err != nil {
		t.Fatalf("cutoff: %v", err)
	}
	var drains []recordedDrain
	result, err := ReconcileNodeIngestSessions(ctx, node, []*ipcpb.LiveIngestGeneration{{
		RuntimeName: stream, Generation: sessionID, ConnectorPid: 200,
	}}, cutoff, recordDrains(&drains), nil, logging.NewLogger())
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if result.Kept != 1 || result.Ended != 0 || result.Stopped != 0 || len(drains) != 0 {
		t.Fatalf("result=%+v drains=%d, want one kept session and no action", result, len(drains))
	}
	if ended, reason := sessionEnded(t, sessionID); ended {
		t.Fatalf("reported live session ended (reason %q)", reason)
	}
	if n := offlineEffectCount(t, ingA, stream); n != 0 {
		t.Fatalf("offline effects = %d, want 0", n)
	}
}

// A re-registration that does not list the generation ends it through the offline path. A session
// minted after the registration cutoff (an admission over the new connection) is not touched.
func TestReregisterEndsUnreportedSession_RealPG(t *testing.T) {
	useRealPG(t)
	ctx := context.Background()
	const node = "node-lost-publisher"
	const gone, fresh = "live+publisher-gone", "live+admitted-after-register"
	goneID := seedOpenIngestSession(t, ingA, node, gone, "u-gone", 300, 1000)
	projectAged(t, goneID, 10*time.Minute)

	cutoff, err := IngestRegistrationCutoff(ctx)
	if err != nil {
		t.Fatalf("cutoff: %v", err)
	}
	freshID := seedOpenIngestSession(t, ingA, node, fresh, "u-fresh", 301, 2000)
	if _, execErr := db.ExecContext(ctx, `UPDATE foghorn.ingest_sessions SET projection_state='active', started_at=$2::timestamptz + INTERVAL '1 second' WHERE id=$1::uuid`, freshID, cutoff); execErr != nil {
		t.Fatalf("stamp post-registration admission: %v", execErr)
	}

	result, err := ReconcileNodeIngestSessions(ctx, node, nil, cutoff, nil, nil, logging.NewLogger())
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if result.Ended != 1 || result.Kept != 0 {
		t.Fatalf("result=%+v, want exactly the pre-registration session ended", result)
	}
	if ended, reason := sessionEnded(t, goneID); !ended || reason != IngestEndedAbsentOnReregister {
		t.Fatalf("unreported session: ended=%v reason=%q, want %q", ended, reason, IngestEndedAbsentOnReregister)
	}
	if n := offlineEffectCount(t, ingA, gone); n != 1 {
		t.Fatalf("offline effects for the ended session = %d, want 1", n)
	}
	if ended, _ := sessionEnded(t, freshID); ended {
		t.Fatal("a session admitted after the registration cutoff was ended")
	}
}

// A competing admission on another node supersedes the session only while the incumbent's node is
// absent; while it is connected the newcomer is a duplicate.
func TestTakeoverSupersedesAbsentNodeSession_RealPG(t *testing.T) {
	useRealPG(t)
	connectNode := useConnOwnerStore(t)
	ctx := context.Background()
	lg := logging.NewLogger()

	const stream = "live+takeover"
	incumbent, outcome, err := CreateIngestSession(ctx, ingA, "node-old", stream, 400, "u-old", 1000, nil, "cell-a", lg)
	if err != nil || outcome != IngestSessionActive {
		t.Fatalf("incumbent: outcome=%v err=%v", outcome, err)
	}

	// Incumbent node connected: a second publisher elsewhere is refused and the incumbent kept.
	connectNode("node-old")
	if _, rivalOutcome, rivalErr := CreateIngestSession(ctx, ingA, "node-rival", stream, 401, "u-rival", 2000, nil, "cell-a", lg); rivalErr != nil || rivalOutcome != IngestSessionRejectedDuplicate {
		t.Fatalf("rival while incumbent connected: outcome=%v err=%v, want RejectedDuplicate", rivalOutcome, rivalErr)
	}
	if ended, _ := sessionEnded(t, incumbent); ended {
		t.Fatal("a connected incumbent was superseded")
	}

	// Incumbent node gone from every replica: the new node takes the stream over.
	useConnOwnerStore(t)
	successor, outcome, err := CreateIngestSession(ctx, ingA, "node-new", stream, 402, "u-new", 3000, nil, "cell-a", lg)
	if err != nil || outcome != IngestSessionActive || successor == "" || successor == incumbent {
		t.Fatalf("takeover: id=%q outcome=%v err=%v", successor, outcome, err)
	}
	if ended, reason := sessionEnded(t, incumbent); !ended || reason != IngestEndedSupersededByNewNode {
		t.Fatalf("incumbent after takeover: ended=%v reason=%q, want %q", ended, reason, IngestEndedSupersededByNewNode)
	}
	if n := offlineEffectCount(t, ingA, stream); n != 0 {
		t.Fatalf("takeover queued %d offline effects; the new projection replaces the source", n)
	}
	// The guard was released after commit, so the old node can register again.
	release, err := NodeRetireGuardLookup(ctx, "node-old")
	if err != nil || release == nil {
		t.Fatalf("takeover guard still held after commit (guard=%v err=%v)", release != nil, err)
	}
	release()
}

// Unreadable presence is not absence: the incumbent keeps the stream.
func TestTakeoverRefusedWhenPresenceUnknown_RealPG(t *testing.T) {
	useRealPG(t)
	setCommandRelay(t, nil)
	ctx := context.Background()
	lg := logging.NewLogger()
	const stream = "live+presence-unknown"
	incumbent, _, err := CreateIngestSession(ctx, ingA, "node-unknown", stream, 500, "u-inc", 1000, nil, "cell-a", lg)
	if err != nil {
		t.Fatalf("incumbent: %v", err)
	}
	if _, outcome, err := CreateIngestSession(ctx, ingA, "node-other", stream, 501, "u-other", 2000, nil, "cell-a", lg); err != nil || outcome != IngestSessionRejectedDuplicate {
		t.Fatalf("admission with unknown presence: outcome=%v err=%v, want RejectedDuplicate", outcome, err)
	}
	if ended, _ := sessionEnded(t, incumbent); ended {
		t.Fatal("incumbent superseded while its node's presence was unknown")
	}
}

// The superseded node, on return, still lists its old generation and is told to stop that input,
// fenced on the exact generation; the successor session is untouched.
func TestSupersededNodeToldToStopOnReturn_RealPG(t *testing.T) {
	useRealPG(t)
	useConnOwnerStore(t)
	ctx := context.Background()
	lg := logging.NewLogger()

	const stream = "live+returning-loser"
	oldGen, _, err := CreateIngestSession(ctx, ingA, "node-loser", stream, 600, "u-loser", 1000, nil, "cell-a", lg)
	if err != nil {
		t.Fatalf("incumbent: %v", err)
	}
	projectAged(t, oldGen, 10*time.Minute)
	newGen, outcome, err := CreateIngestSession(ctx, ingA, "node-winner", stream, 601, "u-winner", 2000, nil, "cell-a", lg)
	if err != nil || outcome != IngestSessionActive {
		t.Fatalf("takeover: outcome=%v err=%v", outcome, err)
	}

	cutoff, err := IngestRegistrationCutoff(ctx)
	if err != nil {
		t.Fatalf("cutoff: %v", err)
	}
	var drains []recordedDrain
	result, err := ReconcileNodeIngestSessions(ctx, "node-loser", []*ipcpb.LiveIngestGeneration{{
		RuntimeName: stream, Generation: oldGen, ConnectorPid: 600,
	}}, cutoff, recordDrains(&drains), nil, lg)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if result.Stopped != 1 || len(drains) != 1 {
		t.Fatalf("result=%+v drains=%d, want one stop command", result, len(drains))
	}
	got := drains[0]
	if got.nodeID != "node-loser" || got.req.GetRuntimeName() != stream ||
		got.req.GetPriorOwnerSourceGeneration() != oldGen || got.req.GetSourceGeneration() != newGen {
		t.Fatalf("drain = node %q %+v; want node-loser, runtime %q, prior %q, obligation %q", got.nodeID, got.req, stream, oldGen, newGen)
	}
	if ended, _ := sessionEnded(t, newGen); ended {
		t.Fatal("the successor session was ended by the loser's return")
	}
}

func TestNeverProjectedSessionReaperQueuesInactiveProjectionRealPG(t *testing.T) {
	conn := startRealPG(t)
	prev := db
	SetDB(conn)
	t.Cleanup(func() { SetDB(prev) })
	ctx := context.Background()
	const node, stream = "node-never-projected", "live+never-projected"
	sessionID := seedOpenIngestSession(t, ingA, node, stream, "u-never-projected", 400, 1000)
	if _, err := db.Exec(`
		UPDATE foghorn.ingest_sessions
		   SET projection_state='pending', started_at=NOW()-INTERVAL '10 minutes'
		 WHERE id=$1::uuid
	`, sessionID); err != nil {
		t.Fatalf("age pending session: %v", err)
	}

	retired, err := ReapNeverProjectedIngestSessions(ctx, 2*time.Minute, logging.NewLogger())
	if err != nil || retired != 1 {
		t.Fatalf("reap pending projection: retired=%d err=%v", retired, err)
	}
	if ended, reason := sessionEnded(t, sessionID); !ended || reason != "projection_timeout" {
		t.Fatalf("pending session state: ended=%v reason=%q", ended, reason)
	}
	var effects int
	if err := db.QueryRow(`
		SELECT COUNT(*) FROM foghorn.ingest_offline_effects
		 WHERE tenant_id=$1::uuid AND stream_internal_name=$2 AND source_generation=$3::uuid AND state='pending'
	`, ingA, stream, sessionID).Scan(&effects); err != nil {
		t.Fatalf("count pending-session cleanup effects: %v", err)
	}
	if effects != 1 {
		t.Fatalf("pending-session cleanup effects = %d, want 1", effects)
	}
}
