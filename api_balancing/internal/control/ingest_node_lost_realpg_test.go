//go:build schema_verify

package control

import (
	"context"
	"testing"
	"time"

	"frameworks/api_balancing/internal/state"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	publicv1 "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/events/public/v1"
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

const (
	lostStreamID   = "7a1e0000-0000-4000-8000-0000000000bb"
	lostStreamID2  = "7a1e0000-0000-4000-8000-0000000000cc"
	lostPlaybackID = "pblostnode001"
)

// connOwnerStore wires the production conn_owner store (miniredis) and returns a function that gives
// a node a control connection on another replica.
func connOwnerStore(t *testing.T) (*state.RedisStateStore, func(nodeID string)) {
	t.Helper()
	store, _ := newTestStore(t)
	setCommandRelay(t, buildRelay(t, store, "inst-self", "10.0.0.1:9090", &mockRelayPool{}))
	var fence int64
	return store, func(nodeID string) {
		fence++
		acquired, err := store.AcquireConnOwnerFenced(context.Background(), nodeID, "inst-peer", "10.0.0.2:9090", fence)
		if err != nil || !acquired {
			t.Fatalf("connect %s: acquired=%v err=%v", nodeID, acquired, err)
		}
	}
}

// mintProjected admits a publisher through MintIngestSession (so stream.connected / stream.idle carry
// a stream ID) and marks its projection confirmed well before now.
func mintProjected(t *testing.T, node, stream, streamID string, pid int64, trigger string, startedMillis int64) string {
	t.Helper()
	id, outcome, err := MintIngestSession(context.Background(), IngestSessionRequest{
		TenantID: domainTenant, NodeID: node, InternalName: stream, StreamID: streamID, PlaybackID: lostPlaybackID,
		Protocol: publicv1.IngestProtocol_INGEST_PROTOCOL_RTMP, ConnectorPID: pid, TriggerUUID: trigger,
		StartedAtMillis: startedMillis, IngestClusterID: "cell-a",
	}, logging.NewLogger())
	if err != nil || outcome != IngestSessionActive {
		t.Fatalf("mint %s on %s: outcome=%v err=%v", trigger, node, outcome, err)
	}
	projectAged(t, id, 10*time.Minute)
	return id
}

func productionLostDeps() IngestNodeLostDeps {
	return IngestNodeLostDeps{Present: NodePresenceLookup, Guard: NodeRetireGuardLookup, Evidence: IngestLifeEvidenceLookup}
}

// A node with no control connection and no evidence of life keeps its sessions for the whole lost-node
// window, then each ends as node_lost through the offline path: stream.idle, an offline effect with
// teardown, and the stream slot freed. A connected node's session is never touched.
func TestLostNodeSessionsEndAfterWindow_RealPG(t *testing.T) {
	useRealPG(t)
	_, connectNode := connOwnerStore(t)
	ctx := context.Background()
	lg := logging.NewLogger()

	const lost, alive = "node-gone", "node-connected"
	const lostStream, aliveStream = "live+lost-window", "live+connected-window"
	lostGen := mintProjected(t, lost, lostStream, lostStreamID, 100, "u-lost", 1000)
	aliveGen := mintProjected(t, alive, aliveStream, lostStreamID2, 200, "u-alive", 1000)
	connectNode(alive)

	absence := make(IngestNodeAbsence)
	t0 := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	for _, at := range []time.Time{t0, t0.Add(time.Minute), t0.Add(IngestNodeLostAfter - time.Second)} {
		n, err := ReapLostNodeIngestSessionsOnce(ctx, productionLostDeps(), absence, at, lg)
		if err != nil || n != 0 {
			t.Fatalf("pass at %s: ended=%d err=%v, want nothing inside the window", at.Sub(t0), n, err)
		}
	}
	if ended, reason := sessionEnded(t, lostGen); ended {
		t.Fatalf("session ended inside the lost-node window (reason %q)", reason)
	}

	n, err := ReapLostNodeIngestSessionsOnce(ctx, productionLostDeps(), absence, t0.Add(IngestNodeLostAfter), lg)
	if err != nil || n != 1 {
		t.Fatalf("pass at the window: ended=%d err=%v, want 1", n, err)
	}
	if ended, reason := sessionEnded(t, lostGen); !ended || reason != IngestEndedNodeLost {
		t.Fatalf("lost node's session: ended=%v reason=%q, want %q", ended, reason, IngestEndedNodeLost)
	}
	if n := offlineEffectCount(t, domainTenant, lostStream); n != 1 {
		t.Fatalf("offline effects for the lost session = %d, want 1", n)
	}
	var teardown bool
	if err := db.QueryRowContext(ctx, `SELECT teardown_stream FROM foghorn.ingest_offline_effects WHERE source_generation=$1::uuid`, lostGen).Scan(&teardown); err != nil || !teardown {
		t.Fatalf("offline effect for the lost generation: teardown=%v err=%v", teardown, err)
	}
	if idle := domainRows(t, db, "stream.idle"); len(idle) != 1 || idle[0].aggregateID != lostStreamID {
		t.Fatalf("stream.idle rows = %+v, want one for the lost stream", idle)
	}
	if _, tracked := absence[lost]; tracked {
		t.Fatal("absence clock kept after the node's sessions ended")
	}
	if ended, _ := sessionEnded(t, aliveGen); ended {
		t.Fatal("a connected node's session was ended")
	}
	// The slot is free: the stream is admitted again on another node without a takeover.
	if _, outcome, err := MintIngestSession(ctx, IngestSessionRequest{
		TenantID: domainTenant, NodeID: "node-new", InternalName: lostStream, StreamID: lostStreamID, PlaybackID: lostPlaybackID,
		Protocol: publicv1.IngestProtocol_INGEST_PROTOCOL_RTMP, ConnectorPID: 300, TriggerUUID: "u-new", StartedAtMillis: 5000, IngestClusterID: "cell-a",
	}, lg); err != nil || outcome != IngestSessionActive {
		t.Fatalf("readmission after node_lost: outcome=%v err=%v", outcome, err)
	}
}

// A presence read that says absent is not enough: the retirement guard re-checks conn_owner
// atomically, so a node that registered again after the read keeps its sessions and its clock is
// cleared.
func TestLostNodePassYieldsToConcurrentRegistration_RealPG(t *testing.T) {
	useRealPG(t)
	_, connectNode := connOwnerStore(t)
	ctx := context.Background()
	lg := logging.NewLogger()

	const node, stream = "node-racing", "live+racing"
	gen := mintProjected(t, node, stream, lostStreamID, 100, "u-race", 1000)
	staleAbsent := func(context.Context, string) (bool, error) { return false, nil }
	deps := IngestNodeLostDeps{Present: staleAbsent, Guard: NodeRetireGuardLookup, Evidence: IngestLifeEvidenceLookup}

	absence := make(IngestNodeAbsence)
	t0 := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	if _, err := ReapLostNodeIngestSessionsOnce(ctx, deps, absence, t0, lg); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	connectNode(node)
	n, err := ReapLostNodeIngestSessionsOnce(ctx, deps, absence, t0.Add(IngestNodeLostAfter+time.Minute), lg)
	if err != nil || n != 0 {
		t.Fatalf("pass after re-registration: ended=%d err=%v, want 0", n, err)
	}
	if ended, reason := sessionEnded(t, gen); ended {
		t.Fatalf("session of a node that registered again was ended (reason %q)", reason)
	}
	if _, tracked := absence[node]; tracked {
		t.Fatal("absence clock kept for a node the guard found connected")
	}
}

// A connected edge serving a live pulled copy of the stream proves the source node still publishes:
// the clock restarts at every pass that sees it. Once the copy is gone, the full window runs again
// from that pass before the session ends. A copy on an edge without a control connection is not
// evidence.
func TestLostNodeEvidenceOfLifeRestartsClock_RealPG(t *testing.T) {
	useRealPG(t)
	_, connectNode := connOwnerStore(t)
	ctx := context.Background()
	lg := logging.NewLogger()

	const node, edge, stream = "node-partitioned", "node-edge-puller", "live+pulled-elsewhere"
	gen := mintProjected(t, node, stream, lostStreamID, 100, "u-pulled", 1000)
	manager := state.DefaultManager()
	manager.UpdateNodeStats("pulled-elsewhere", edge, 3, 1, 0, 0, true)
	t.Cleanup(func() { manager.ReconcileNodeStreamPresence(edge, map[string]struct{}{}) })

	absence := make(IngestNodeAbsence)
	t0 := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	// The edge has no control connection yet: its copy is not evidence and the clock starts.
	if _, err := ReapLostNodeIngestSessionsOnce(ctx, productionLostDeps(), absence, t0, lg); err != nil {
		t.Fatalf("first pass: %v", err)
	}
	if got := absence[node]; !got.Equal(t0) {
		t.Fatalf("absence clock = %v, want start at %v", got, t0)
	}

	connectNode(edge)
	for _, at := range []time.Time{t0.Add(4 * time.Minute), t0.Add(8 * time.Minute), t0.Add(12 * time.Minute)} {
		manager.UpdateNodeStats("pulled-elsewhere", edge, 3, 1, 0, 0, true)
		if n, err := ReapLostNodeIngestSessionsOnce(ctx, productionLostDeps(), absence, at, lg); err != nil || n != 0 {
			t.Fatalf("pass at %s with live pulled copy: ended=%d err=%v", at.Sub(t0), n, err)
		}
		if got := absence[node]; !got.Equal(at) {
			t.Fatalf("clock at %s = %v, want restarted at the pass", at.Sub(t0), got)
		}
	}
	if ended, reason := sessionEnded(t, gen); ended {
		t.Fatalf("session ended while its stream was pulled live from the node (reason %q)", reason)
	}

	// The copy stops: nothing ends until the whole window has run from the last evidence.
	manager.ReconcileNodeStreamPresence(edge, map[string]struct{}{})
	lastEvidence := t0.Add(12 * time.Minute)
	if n, err := ReapLostNodeIngestSessionsOnce(ctx, productionLostDeps(), absence, lastEvidence.Add(IngestNodeLostAfter-time.Second), lg); err != nil || n != 0 {
		t.Fatalf("pass just inside the window after evidence: ended=%d err=%v", n, err)
	}
	if n, err := ReapLostNodeIngestSessionsOnce(ctx, productionLostDeps(), absence, lastEvidence.Add(IngestNodeLostAfter), lg); err != nil || n != 1 {
		t.Fatalf("pass at the window after evidence: ended=%d err=%v, want 1", n, err)
	}
	if ended, reason := sessionEnded(t, gen); !ended || reason != IngestEndedNodeLost {
		t.Fatalf("session after evidence stopped: ended=%v reason=%q", ended, reason)
	}
}

// A node that returns after its session ended node_lost, still listing that generation, is told to
// stop exactly that generation even though no other publisher took the stream. A generation that
// ended on its own evidence with no successor is still left to the node.
func TestReturnAfterNodeLostDrainsExactGeneration_RealPG(t *testing.T) {
	useRealPG(t)
	ctx := context.Background()
	lg := logging.NewLogger()

	const node = "node-returns-late"
	const lostStream, closedStream = "live+returns-late", "live+closed-while-away"
	lostGen := mintProjected(t, node, lostStream, lostStreamID, 100, "u-lost-return", 1000)
	closedGen := mintProjected(t, node, closedStream, lostStreamID2, 101, "u-closed", 1000)
	if ok, err := RetireIngestSession(ctx, lostGen, domainTenant, lostStream, IngestEndedNodeLost, lg); err != nil || !ok {
		t.Fatalf("end as node_lost: ok=%v err=%v", ok, err)
	}
	if fin, err := FinalizeIngestSessionClose(ctx, domainTenant, node, 101, 5000, closedStream, lg); err != nil || fin.EndedSessionID != closedGen {
		t.Fatalf("close: %+v err=%v", fin, err)
	}

	cutoff, err := IngestRegistrationCutoff(ctx)
	if err != nil {
		t.Fatalf("cutoff: %v", err)
	}
	var drains []recordedDrain
	result, err := ReconcileNodeIngestSessions(ctx, node, []*ipcpb.LiveIngestGeneration{
		{RuntimeName: lostStream, Generation: lostGen, ConnectorPid: 100},
		{RuntimeName: closedStream, Generation: closedGen, ConnectorPid: 101},
	}, cutoff, recordDrains(&drains), nil, lg)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if result.Stopped != 1 || len(drains) != 1 {
		t.Fatalf("result=%+v drains=%+v, want exactly the node_lost generation stopped", result, drains)
	}
	got := drains[0]
	if got.nodeID != node || got.req.GetRuntimeName() != lostStream || got.req.GetSourceGeneration() != lostGen ||
		got.req.GetPriorOwnerSourceGeneration() != lostGen || got.req.GetReason() != "ingest_ownership_lost:node_lost" {
		t.Fatalf("drain = node %q %+v; want node %q, runtime %q fenced on %q", got.nodeID, got.req, node, lostStream, lostGen)
	}
	if idle := domainRows(t, db, "stream.idle"); len(idle) != 2 {
		t.Fatalf("stream.idle rows = %d, want one per ended session and none from the return", len(idle))
	}
}

type lateTrigger string

const (
	lateClose         lateTrigger = "PUSH_INPUT_CLOSE"
	lateStreamEnd     lateTrigger = "STREAM_END"
	lateRuntimeAbsent lateTrigger = "INGEST_RUNTIME_ABSENT"
)

type successorKind string

const (
	successorOtherNode successorKind = "successor on another node"
	successorSameNode  successorKind = "successor on the same node"
	successorNone      successorKind = "no successor (node_lost)"
)

// Triggers the old node delivers late for an ended generation — its close, its buffer's STREAM_END,
// its runtime-absence report — change nothing: the successor stays open, the ended generation keeps
// its reason, and no second stream.idle is emitted. Where a successor is open the offline backstop the
// processor runs for STREAM_END and runtime absence is suppressed. Each case calls the same control
// functions the trigger processor calls for that trigger.
func TestLateTriggersFromEndedGenerationAreNoOps_RealPG(t *testing.T) {
	for _, kind := range []successorKind{successorOtherNode, successorSameNode, successorNone} {
		for _, trigger := range []lateTrigger{lateClose, lateStreamEnd, lateRuntimeAbsent} {
			t.Run(string(kind)+"/"+string(trigger), func(t *testing.T) {
				useRealPG(t)
				connOwnerStore(t)
				prevRegistry := StreamRegistryInstance
				StreamRegistryInstance = NewStreamRegistry(nil, "cell-a", time.Minute)
				t.Cleanup(func() { StreamRegistryInstance = prevRegistry })
				ctx := context.Background()
				lg := logging.NewLogger()

				const oldNode, stream = "node-old", "live+late-trigger"
				const oldPID, oldStart = int64(100), int64(1000)
				oldGen := mintProjected(t, oldNode, stream, lostStreamID, oldPID, "u-old", oldStart)

				var successor, wantOldReason string
				var eventMillis int64
				switch kind {
				case successorOtherNode:
					// node-old has no conn_owner, so the new node takes the stream over.
					successor = mintProjected(t, "node-new", stream, lostStreamID, 200, "u-new", 3000)
					wantOldReason, eventMillis = IngestEndedSupersededByNewNode, 5000
				case successorSameNode:
					if ok, err := RetireIngestSession(ctx, oldGen, domainTenant, stream, IngestEndedNodeLost, lg); err != nil || !ok {
						t.Fatalf("end old generation: ok=%v err=%v", ok, err)
					}
					successor = mintProjected(t, oldNode, stream, lostStreamID, 101, "u-same", 3000)
					// The old buffer's end precedes the successor's admission; the old connection's
					// close carries the old PID whatever its time.
					wantOldReason, eventMillis = IngestEndedNodeLost, 2500
					if trigger == lateClose {
						eventMillis = 5000
					}
				case successorNone:
					if ok, err := RetireIngestSession(ctx, oldGen, domainTenant, stream, IngestEndedNodeLost, lg); err != nil || !ok {
						t.Fatalf("end old generation: ok=%v err=%v", ok, err)
					}
					wantOldReason, eventMillis = IngestEndedNodeLost, 5000
				}
				idleBefore := len(domainRows(t, db, "stream.idle"))
				effectsBefore := offlineEffectCount(t, domainTenant, stream)

				backstop := false
				switch trigger {
				case lateClose:
					fin, err := FinalizeIngestSessionClose(ctx, domainTenant, oldNode, oldPID, eventMillis, stream, lg)
					if err != nil || fin.EndedSessionID != "" {
						t.Fatalf("late close: %+v err=%v, want nothing finalized", fin, err)
					}
				case lateStreamEnd:
					n, err := EndIngestSessionsForStreamEnd(ctx, domainTenant, oldNode, stream, eventMillis, lg)
					if err != nil || n != 0 {
						t.Fatalf("late STREAM_END: ended=%d err=%v, want 0", n, err)
					}
					backstop = true
				case lateRuntimeAbsent:
					ended, err := EndExactMissingIngestSession(ctx, domainTenant, oldNode, stream, oldGen, oldPID, eventMillis, lg)
					if err != nil || ended {
						t.Fatalf("late runtime absence: ended=%v err=%v, want false", ended, err)
					}
					backstop = true
				}
				if backstop && successor != "" {
					proceed, _, err := FenceOfflineBackstop(ctx, StreamRegistryInstance, domainTenant, oldNode, stream, OfflineEffectIntent{
						SetNodeOffline: true, TeardownStream: true, BroadcastOffline: true,
					})
					if err != nil || proceed {
						t.Fatalf("offline backstop with an open successor: proceed=%v err=%v, want suppressed", proceed, err)
					}
					if n := offlineEffectCount(t, domainTenant, stream); n != effectsBefore {
						t.Fatalf("offline effects %d -> %d; an open successor must suppress the backstop", effectsBefore, n)
					}
				}

				if successor != "" {
					if ended, reason := sessionEnded(t, successor); ended {
						t.Fatalf("late %s ended the successor (reason %q)", trigger, reason)
					}
				}
				if ended, reason := sessionEnded(t, oldGen); !ended || reason != wantOldReason {
					t.Fatalf("old generation: ended=%v reason=%q, want %q", ended, reason, wantOldReason)
				}
				if idle := len(domainRows(t, db, "stream.idle")); idle != idleBefore {
					t.Fatalf("stream.idle rows %d -> %d; a late %s must not emit another", idleBefore, idle, trigger)
				}
				if trigger == lateClose || successor != "" {
					if n := offlineEffectCount(t, domainTenant, stream); n != effectsBefore {
						t.Fatalf("offline effects %d -> %d after a late %s", effectsBefore, n, trigger)
					}
				}
			})
		}
	}
}
