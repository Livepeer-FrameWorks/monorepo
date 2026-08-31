package state

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/sirupsen/logrus"
)

type tripleWriteArtifactRepo struct {
	mu                  sync.Mutex
	upsertCalls         []tripleWriteUpsert
	markOrphanedCalls   []string
	upsertArtifactsFn   func(ctx context.Context, nodeID string, records []ArtifactRecord) error
	markOrphanedFn      func(ctx context.Context, nodeID string) error
	needsDtshSyncResult bool
}

type tripleWriteUpsert struct {
	NodeID  string
	Records []ArtifactRecord
}

func (r *tripleWriteArtifactRepo) UpsertArtifacts(ctx context.Context, nodeID string, records []ArtifactRecord) error {
	r.mu.Lock()
	r.upsertCalls = append(r.upsertCalls, tripleWriteUpsert{nodeID, records})
	r.mu.Unlock()
	if r.upsertArtifactsFn != nil {
		return r.upsertArtifactsFn(ctx, nodeID, records)
	}
	return nil
}

func (r *tripleWriteArtifactRepo) GetArtifactSyncInfo(_ context.Context, _ string) (*ArtifactSyncInfo, error) {
	return nil, nil
}
func (r *tripleWriteArtifactRepo) SetSyncStatus(_ context.Context, _, _, _ string) error { return nil }
func (r *tripleWriteArtifactRepo) AddCachedNode(_ context.Context, _, _ string) error    { return nil }
func (r *tripleWriteArtifactRepo) AddCachedNodeWithPath(_ context.Context, _, _, _ string, _ int64) error {
	return nil
}
func (r *tripleWriteArtifactRepo) RegisterOriginArtifact(_ context.Context, _, _, _ string, _ int64, _ bool) error {
	return nil
}
func (r *tripleWriteArtifactRepo) ListOriginNodes(_ context.Context, _ string) ([]string, error) {
	return nil, nil
}
func (r *tripleWriteArtifactRepo) IsSynced(_ context.Context, _ string) (bool, error) {
	return false, nil
}
func (r *tripleWriteArtifactRepo) GetCachedAt(_ context.Context, _ string) (int64, error) {
	return 0, nil
}
func (r *tripleWriteArtifactRepo) ListAllNodeArtifacts(_ context.Context) (map[string][]ArtifactRecord, error) {
	return nil, nil
}
func (r *tripleWriteArtifactRepo) MarkNodeArtifactsOrphaned(ctx context.Context, nodeID string, _ int64, _ int64, _ int64) error {
	r.mu.Lock()
	r.markOrphanedCalls = append(r.markOrphanedCalls, nodeID)
	r.mu.Unlock()
	if r.markOrphanedFn != nil {
		return r.markOrphanedFn(ctx, nodeID)
	}
	return nil
}
func (r *tripleWriteArtifactRepo) DeleteNodeArtifact(_ context.Context, _, _ string, _ int64) (NodeArtifactDeletionOutcome, error) {
	return NodeArtifactDeletionApplied, nil
}
func (r *tripleWriteArtifactRepo) ReconcileNodeCopies(_ context.Context) (int, error) {
	return 0, nil
}
func (r *tripleWriteArtifactRepo) RefreshNodeCopy(_ context.Context, _, _ string) error {
	return nil
}
func (r *tripleWriteArtifactRepo) RegisterDVRRecordingOrigin(_ context.Context, _, _, _ string) error {
	return nil
}
func (r *tripleWriteArtifactRepo) NeedsDtshSync(_ context.Context, _ string) bool {
	return r.needsDtshSyncResult
}
func (r *tripleWriteArtifactRepo) NeedsVODDtshSync(_ context.Context, _ string) bool {
	return r.needsDtshSyncResult
}
func (r *tripleWriteArtifactRepo) UpdateDVRProgressByHash(_ context.Context, _, _ string, _ int64, _ uint32, _ string) (bool, string, error) {
	return true, "recording", nil
}
func (r *tripleWriteArtifactRepo) UpdateDVRCompletionByHash(_ context.Context, _, _ string, _, _ int64, _, _ string) error {
	return nil
}

func setupTripleWriteTest(t *testing.T) (*StreamStateManager, *RedisStateStore, *miniredis.Miniredis, *tripleWriteArtifactRepo) {
	t.Helper()

	sm := ResetDefaultManagerForTests()
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	store := NewRedisStateStore(client, "test-cluster")
	repo := &tripleWriteArtifactRepo{}

	logger := logrus.New()
	logger.SetLevel(logrus.WarnLevel)

	if err := sm.EnableRedisSync(context.Background(), store, "instance-a", logger); err != nil {
		t.Fatalf("EnableRedisSync: %v", err)
	}
	sm.ConfigurePolicies(PoliciesConfig{ArtifactRepo: repo})

	t.Cleanup(func() {
		sm.Shutdown()
	})

	return sm, store, mr, repo
}

// A whole-node report that passes THIS instance's local acceptance gate but LOSES the cross-instance
// fenced CAS (a strictly-higher fence already owns the node on a peer) must NOT lift the local artifact
// readiness cordon: the node stays unroutable for artifacts and the loss is surfaced. Otherwise a
// superseded replica would serve from a stale local inventory it no longer authoritatively owns.
func TestSetNodeArtifacts_LostFenceKeepsNodeCordoned(t *testing.T) {
	sm, store, mr, _ := setupTripleWriteTest(t)

	// A peer instance already advanced the shared Redis (fence, seq) watermark to a HIGHER fence. Seed the
	// watermark key ALONE (no envelope/changelog), so the CAS below loses without the peer's Ready=true
	// snapshot being replicated back into this manager — isolating the local losing-report decision.
	if err := mr.Set(store.keyArtifactWatermark("node-1"), "5-1"); err != nil {
		t.Fatalf("seed higher-fence watermark: %v", err)
	}

	// This instance receives a report from an OLDER connection (fence 2). It passes the local accept gate
	// (no prior local report) but must lose the cross-instance CAS.
	err := sm.SetNodeArtifacts("node-1", []*ipcpb.StoredArtifact{{ClipHash: "stale", FilePath: "/d/stale.mp4"}},
		ArtifactReportOrder{Fence: 2, Seq: 1})
	if !errors.Is(err, ErrArtifactInventorySuperseded) {
		t.Fatalf("expected ErrArtifactInventorySuperseded, got %v", err)
	}

	snap := sm.GetAllNodesSnapshot()
	found := false
	for _, n := range snap.Nodes {
		if n.NodeID == "node-1" {
			found = true
			if n.ArtifactInventoryReady {
				t.Fatal("losing-fence report must NOT lift the artifact readiness cordon")
			}
		}
	}
	if !found {
		t.Fatal("node-1 not found in snapshot")
	}
}

// The winning path: a report that wins the fenced CAS lifts the readiness cordon so the node becomes
// routable for artifacts and no supersession error is returned.
func TestSetNodeArtifacts_WonFenceLiftsCordon(t *testing.T) {
	sm, _, _, _ := setupTripleWriteTest(t)

	if err := sm.SetNodeArtifacts("node-1", []*ipcpb.StoredArtifact{{ClipHash: "h1", FilePath: "/d/h1.mp4"}},
		ArtifactReportOrder{Fence: 1, Seq: 1}); err != nil {
		t.Fatalf("winning report should not error: %v", err)
	}

	snap := sm.GetAllNodesSnapshot()
	for _, n := range snap.Nodes {
		if n.NodeID == "node-1" && !n.ArtifactInventoryReady {
			t.Fatal("winning report must lift the artifact readiness cordon")
		}
	}
}

// A sidecar-reported INCOMPLETE scan (lost mount / read error) re-arms the artifact-routing cordon:
// ArtifactInventoryReady drops so vanished copies stop being routable, while the last-good inventory is
// RETAINED for observability. A STALE (not-newer) incomplete report must NOT re-cordon a node that a
// newer complete report already made ready.
func TestCordonNodeArtifactsIncomplete_ReArmsCordonRetainsInventory(t *testing.T) {
	sm, _, _, _ := setupTripleWriteTest(t)

	// Complete report at (1,1): node ready with an inventory.
	if err := sm.SetNodeArtifacts("node-1", []*ipcpb.StoredArtifact{{ClipHash: "h1", FilePath: "/d/h1.mp4"}},
		ArtifactReportOrder{Fence: 1, Seq: 1}); err != nil {
		t.Fatalf("initial report: %v", err)
	}

	// Newer incomplete report at (1,2): cordon, but keep the last-good inventory.
	if err := sm.CordonNodeArtifactsIncomplete("node-1", ArtifactReportOrder{Fence: 1, Seq: 2}); err != nil {
		t.Fatalf("incomplete cordon at a newer order must apply: %v", err)
	}
	for _, n := range sm.GetAllNodesSnapshot().Nodes {
		if n.NodeID == "node-1" {
			if n.ArtifactInventoryReady {
				t.Fatal("incomplete scan must re-arm the cordon (ArtifactInventoryReady=false)")
			}
			if len(n.Artifacts) != 1 {
				t.Fatalf("last-good inventory must be retained for observability, got %d artifacts", len(n.Artifacts))
			}
		}
	}

	// A newer complete report at (1,3) lifts the cordon again.
	if err := sm.SetNodeArtifacts("node-1", []*ipcpb.StoredArtifact{{ClipHash: "h1", FilePath: "/d/h1.mp4"}},
		ArtifactReportOrder{Fence: 1, Seq: 3}); err != nil {
		t.Fatalf("recovery report: %v", err)
	}
	// A STALE incomplete report at (1,1) is not newer than the accepted (1,3): it must NOT re-cordon the
	// recovered node, and it is a DROP (not an apply), so it must surface as superseded — not a false
	// success that would make the caller log the node as freshly cordoned.
	if err := sm.CordonNodeArtifactsIncomplete("node-1", ArtifactReportOrder{Fence: 1, Seq: 1}); !errors.Is(err, ErrArtifactInventorySuperseded) {
		t.Fatalf("stale incomplete cordon must return ErrArtifactInventorySuperseded, got: %v", err)
	}
	for _, n := range sm.GetAllNodesSnapshot().Nodes {
		if n.NodeID == "node-1" && !n.ArtifactInventoryReady {
			t.Fatal("a stale incomplete report must not re-cordon a node a newer report already made ready")
		}
	}
}

// Positive control for the marker path: with a Redis tier and no higher incumbent fence, publishing
// the fenced takeover marker WINS its CAS and returns nil, and this owner's first versioned report then
// lifts the readiness cordon. Guards the applied==false rejection from firing on a normal registration.
func TestRecordNodeArtifactFence_WinsMarkerCASWithRedis(t *testing.T) {
	sm, _, _, _ := setupTripleWriteTest(t)

	if err := sm.RecordNodeArtifactFence("node-1", 3); err != nil {
		t.Fatalf("uncontested takeover marker must win its CAS: %v", err)
	}

	if err := sm.SetNodeArtifacts("node-1", []*ipcpb.StoredArtifact{{ClipHash: "h1", FilePath: "/d/h1.mp4"}},
		ArtifactReportOrder{Fence: 3, Seq: 1}); err != nil {
		t.Fatalf("first versioned report after a won marker must apply: %v", err)
	}

	snap := sm.GetAllNodesSnapshot()
	found := false
	for _, n := range snap.Nodes {
		if n.NodeID == "node-1" {
			found = true
			if !n.ArtifactInventoryReady {
				t.Fatal("won marker + first report must lift the artifact readiness cordon")
			}
		}
	}
	if !found {
		t.Fatal("node-1 not found in snapshot")
	}
}

// A genuine failure of the authoritative Redis write (here: Redis unreachable) must return an error,
// NOT success: the report never became durable, so the node stays cordoned and no downstream side
// effect runs (DB placement persist skipped). Otherwise the caller would notify peers / persist
// telemetry for state that never landed in Redis. This is distinct from a valid CAS loss (which
// returns ErrArtifactInventorySuperseded).
func TestSetNodeArtifacts_RedisWriteFailurePropagatesError(t *testing.T) {
	sm, _, mr, repo := setupTripleWriteTest(t)

	// Kill the Redis backend so the fenced CAS write genuinely fails (connection refused).
	mr.Close()

	err := sm.SetNodeArtifacts("node-1", []*ipcpb.StoredArtifact{{ClipHash: "h1", FilePath: "/d/h1.mp4"}},
		ArtifactReportOrder{Fence: 1, Seq: 1})
	if err == nil {
		t.Fatal("expected an error when the authoritative Redis write fails")
	}
	if errors.Is(err, ErrArtifactInventorySuperseded) {
		t.Fatalf("a Redis write failure must NOT be reported as a supersession: %v", err)
	}

	// The node stays cordoned: a non-durable report may never lift readiness.
	snap := sm.GetAllNodesSnapshot()
	for _, n := range snap.Nodes {
		if n.NodeID == "node-1" && n.ArtifactInventoryReady {
			t.Fatal("a report whose Redis write failed must NOT lift the artifact readiness cordon")
		}
	}

	// No downstream side effect: the function returns before spawning the DB placement upsert, so a
	// report that never became authoritative in Redis does not persist telemetry.
	repo.mu.Lock()
	upserts := len(repo.upsertCalls)
	repo.mu.Unlock()
	if upserts != 0 {
		t.Fatalf("expected no DB placement upsert after a failed Redis write, got %d", upserts)
	}
}

// A report that LOSES the fenced cross-instance CAS (superseded by a strictly-higher-fence owner on a
// peer) must run NO downstream side effect: no durable placement upsert AND no .dtsh trigger. Both are
// scheduled only AFTER the superseded early-return, so a losing snapshot can never persist placement or
// schedule a command from state it no longer authoritatively owns. The node also stays cordoned.
func TestSetNodeArtifacts_LostFenceSkipsDownstreamSideEffects(t *testing.T) {
	sm, store, mr, repo := setupTripleWriteTest(t)
	repo.needsDtshSyncResult = true // a VOD .dtsh WOULD be eligible if the trigger ran

	var dtshCalls int32
	SetDtshSyncHandler(func(_, _, _, _ string) { atomic.AddInt32(&dtshCalls, 1) })
	t.Cleanup(func() { SetDtshSyncHandler(nil) })

	// A peer already advanced the shared watermark to a strictly-higher fence. Seed the watermark key
	// alone so the CAS below loses without replicating the peer's Ready snapshot back into this manager.
	if err := mr.Set(store.keyArtifactWatermark("node-1"), "9-1"); err != nil {
		t.Fatalf("seed higher-fence watermark: %v", err)
	}

	// This instance's report passes the local accept gate (no prior local report) but LOSES the
	// cross-instance CAS. The artifact carries a .dtsh so, absent the fix, the trigger would fire from
	// this losing snapshot.
	err := sm.SetNodeArtifacts("node-1", []*ipcpb.StoredArtifact{
		{ClipHash: "vod-1", FilePath: "/data/vod/vod-1.mp4", ArtifactType: ipcpb.ArtifactEvent_ARTIFACT_TYPE_VOD, HasDtsh: true},
	}, ArtifactReportOrder{Fence: 3, Seq: 1})
	if !errors.Is(err, ErrArtifactInventorySuperseded) {
		t.Fatalf("expected ErrArtifactInventorySuperseded, got %v", err)
	}

	// Give any (erroneously) scheduled goroutine time to run; with the fix, none is scheduled.
	time.Sleep(100 * time.Millisecond)

	repo.mu.Lock()
	upserts := len(repo.upsertCalls)
	orphans := len(repo.markOrphanedCalls)
	repo.mu.Unlock()
	if upserts != 0 {
		t.Fatalf("superseded report must not persist placement: got %d upsert(s)", upserts)
	}
	if orphans != 0 {
		t.Fatalf("superseded report must not orphan placement: got %d orphan call(s)", orphans)
	}
	if n := atomic.LoadInt32(&dtshCalls); n != 0 {
		t.Fatalf("superseded report must not schedule a .dtsh command: got %d", n)
	}

	// The node stays cordoned (inventory not ready) on the CAS-loss path.
	snap := sm.GetAllNodesSnapshot()
	for _, n := range snap.Nodes {
		if n.NodeID == "node-1" && n.ArtifactInventoryReady {
			t.Fatal("superseded report must NOT lift the artifact readiness cordon")
		}
	}
}

// A report that is LOCALLY OVERTAKEN (a newer report already published in THIS instance) must be treated
// as superseded exactly like a lost cross-instance CAS: it returns ErrArtifactInventorySuperseded and runs
// NO downstream side effect (no durable placement upsert, no .dtsh trigger, no caller notification). This
// simulates the stalled-A/newer-B interleaving — B publishes while A is stalled between accept and publish
// — by pre-advancing the published order past A while leaving A's accept gate open.
func TestSetNodeArtifacts_LocalOvertakeSkipsDownstreamSideEffects(t *testing.T) {
	sm, _, _, repo := setupTripleWriteTest(t)
	repo.needsDtshSyncResult = true // a VOD .dtsh WOULD be eligible if the trigger ran

	var dtshCalls int32
	SetDtshSyncHandler(func(_, _, _, _ string) { atomic.AddInt32(&dtshCalls, 1) })
	t.Cleanup(func() { SetDtshSyncHandler(nil) })

	// A newer report (fence 1, seq 2) already PUBLISHED in this instance.
	sm.mu.Lock()
	sm.lastPublishedArtifactOrder["node-1"] = incOrder{fence: 1, seq: 2}
	sm.mu.Unlock()

	// The stalled older report (fence 1, seq 1) passes the accept gate but is locally overtaken at publish.
	err := sm.SetNodeArtifacts("node-1", []*ipcpb.StoredArtifact{
		{ClipHash: "vod-1", FilePath: "/data/vod/vod-1.mp4", ArtifactType: ipcpb.ArtifactEvent_ARTIFACT_TYPE_VOD, HasDtsh: true},
	}, ArtifactReportOrder{Fence: 1, Seq: 1})
	if !errors.Is(err, ErrArtifactInventorySuperseded) {
		t.Fatalf("expected ErrArtifactInventorySuperseded for a locally-overtaken report, got %v", err)
	}

	time.Sleep(100 * time.Millisecond)
	repo.mu.Lock()
	upserts := len(repo.upsertCalls)
	repo.mu.Unlock()
	if upserts != 0 {
		t.Fatalf("overtaken report must not persist placement: got %d upsert(s)", upserts)
	}
	if n := atomic.LoadInt32(&dtshCalls); n != 0 {
		t.Fatalf("overtaken report must not schedule a .dtsh command: got %d", n)
	}
}

func TestTripleWrite_RoundTrip(t *testing.T) {
	sm, _, mr, _ := setupTripleWriteTest(t)

	artifacts := []*ipcpb.StoredArtifact{
		{
			ClipHash:     "hash-1",
			FilePath:     "/data/clips/hash-1.mp4",
			SizeBytes:    1024,
			StreamName:   "vod+my-stream",
			ArtifactType: ipcpb.ArtifactEvent_ARTIFACT_TYPE_CLIP,
			Format:       "mp4",
		},
	}
	sm.SetNodeArtifacts("node-1", artifacts, ArtifactReportOrder{Fence: 1, Seq: 1})

	// Verify in-memory
	snap := sm.GetAllNodesSnapshot()
	var found bool
	for _, n := range snap.Nodes {
		if n.NodeID == "node-1" {
			found = true
			if len(n.Artifacts) != 1 {
				t.Fatalf("expected 1 artifact, got %d", len(n.Artifacts))
			}
			a := n.Artifacts[0]
			if a.ClipHash != "hash-1" {
				t.Fatalf("expected hash-1, got %s", a.ClipHash)
			}
			if a.Format != "mp4" {
				t.Fatalf("expected mp4 format, got %s", a.Format)
			}
			if a.StreamName != "vod+my-stream" {
				t.Fatalf("expected stream name, got %s", a.StreamName)
			}
		}
	}
	if !found {
		t.Fatal("node not found in snapshot")
	}

	// Verify Redis
	redisKey := "{test-cluster}:artifacts:node-1"
	val, err := mr.Get(redisKey)
	if err != nil {
		t.Fatalf("Redis GET: %v", err)
	}

	// Redis now stores the report envelope {node_id, fence, seq, artifacts}.
	var env NodeArtifactSnapshot
	if err := json.Unmarshal([]byte(val), &env); err != nil {
		t.Fatalf("unmarshal Redis: %v", err)
	}
	states := env.Artifacts
	if len(states) != 1 {
		t.Fatalf("expected 1 Redis artifact, got %d", len(states))
	}
	s := states[0]
	if s.ClipHash != "hash-1" || s.FilePath != "/data/clips/hash-1.mp4" || s.SizeBytes != 1024 {
		t.Fatalf("Redis data mismatch: %+v", s)
	}
	if s.StreamName != "vod+my-stream" || s.ArtifactType != "clip" || s.Format != "mp4" {
		t.Fatalf("Redis metadata mismatch: StreamName=%s ArtifactType=%s Format=%s", s.StreamName, s.ArtifactType, s.Format)
	}
}

func TestTripleWrite_PostgresReceivesRecords(t *testing.T) {
	sm, _, _, repo := setupTripleWriteTest(t)

	sm.SetNodeArtifacts("node-1", []*ipcpb.StoredArtifact{
		{
			ClipHash:     "hash-1",
			FilePath:     "/data/clips/hash-1.mp4",
			SizeBytes:    2048,
			StreamName:   "vod+stream-a",
			ArtifactType: ipcpb.ArtifactEvent_ARTIFACT_TYPE_CLIP,
		},
	}, ArtifactReportOrder{Fence: 1, Seq: 1, ReportedAtMs: 1234})

	// Wait for async goroutine
	time.Sleep(100 * time.Millisecond)

	repo.mu.Lock()
	defer repo.mu.Unlock()

	if len(repo.upsertCalls) != 1 {
		t.Fatalf("expected 1 UpsertArtifacts call, got %d", len(repo.upsertCalls))
	}
	call := repo.upsertCalls[0]
	if call.NodeID != "node-1" {
		t.Fatalf("expected nodeID node-1, got %s", call.NodeID)
	}
	if len(call.Records) != 1 {
		t.Fatalf("expected 1 record, got %d", len(call.Records))
	}
	rec := call.Records[0]
	if rec.ArtifactHash != "hash-1" {
		t.Fatalf("expected hash-1, got %s", rec.ArtifactHash)
	}
	if rec.ArtifactType != "clip" {
		t.Fatalf("expected clip, got %s", rec.ArtifactType)
	}
	if rec.SizeBytes != 2048 {
		t.Fatalf("expected 2048, got %d", rec.SizeBytes)
	}
	if rec.ReportedAtMs != 1234 {
		t.Fatalf("snapshot capture time = %d, want 1234", rec.ReportedAtMs)
	}
}

// An empty report is applied to state (the envelope carries an empty inventory) but does NOT trigger a
// scan-driven negative diff: MarkNodeArtifactsOrphaned is NOT called on an empty report. The node's stale
// rows are reconciled by the stale sweep instead.
func TestTripleWrite_EmptyArtifactsDefersNegativeDiff(t *testing.T) {
	sm, _, mr, repo := setupTripleWriteTest(t)

	// First add something, then clear
	sm.SetNodeArtifacts("node-1", []*ipcpb.StoredArtifact{
		{ClipHash: "hash-1", FilePath: "/data/h1.mp4"},
	}, ArtifactReportOrder{Fence: 1, Seq: 1})
	time.Sleep(50 * time.Millisecond)

	sm.SetNodeArtifacts("node-1", nil, ArtifactReportOrder{Fence: 1, Seq: 2})
	time.Sleep(100 * time.Millisecond)

	// Verify the Redis envelope carries an empty inventory (the node reported nothing).
	redisKey := "{test-cluster}:artifacts:node-1"
	val, err := mr.Get(redisKey)
	if err != nil {
		t.Fatalf("Redis GET: %v", err)
	}
	var env NodeArtifactSnapshot
	if err := json.Unmarshal([]byte(val), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(env.Artifacts) != 0 {
		t.Fatalf("expected empty artifacts in envelope, got %d items", len(env.Artifacts))
	}

	// MarkNodeArtifactsOrphaned must NOT be called on an empty report (negative diff disabled).
	repo.mu.Lock()
	defer repo.mu.Unlock()
	if len(repo.markOrphanedCalls) != 0 {
		t.Fatalf("empty report must NOT trigger MarkNodeArtifactsOrphaned (negative diff disabled), got %d calls", len(repo.markOrphanedCalls))
	}
}

func TestTripleWrite_Rehydration(t *testing.T) {
	// Instance A writes artifacts
	smA := ResetDefaultManagerForTests()
	mr := miniredis.RunT(t)
	clientA := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = clientA.Close() })

	storeA := NewRedisStateStore(clientA, "test-cluster")
	logger := logrus.New()
	logger.SetLevel(logrus.WarnLevel)

	if err := smA.EnableRedisSync(context.Background(), storeA, "instance-a", logger); err != nil {
		t.Fatalf("EnableRedisSync A: %v", err)
	}

	smA.SetNodeArtifacts("node-1", []*ipcpb.StoredArtifact{
		{
			ClipHash:     "hash-r1",
			FilePath:     "/data/r1.mp4",
			SizeBytes:    512,
			StreamName:   "live+rehydrate",
			ArtifactType: ipcpb.ArtifactEvent_ARTIFACT_TYPE_VOD,
			Format:       "mkv",
		},
		{
			ClipHash:     "hash-r2",
			FilePath:     "/data/dvr/r2",
			SizeBytes:    4096,
			StreamName:   "dvr+recording",
			ArtifactType: ipcpb.ArtifactEvent_ARTIFACT_TYPE_DVR,
		},
	}, ArtifactReportOrder{Fence: 1, Seq: 1})

	smA.Shutdown()

	// Instance B: fresh SM, rehydrate from Redis
	smB := NewStreamStateManager()
	clientB := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = clientB.Close() })
	storeB := NewRedisStateStore(clientB, "test-cluster")

	// Must create the node first so rehydration can attach artifacts
	smB.TouchNode("node-1", true)

	if err := smB.EnableRedisSync(context.Background(), storeB, "instance-b", logger); err != nil {
		t.Fatalf("EnableRedisSync B: %v", err)
	}
	t.Cleanup(smB.Shutdown)

	snap := smB.GetAllNodesSnapshot()
	var found bool
	for _, n := range snap.Nodes {
		if n.NodeID == "node-1" {
			found = true
			if len(n.Artifacts) != 2 {
				t.Fatalf("expected 2 artifacts after rehydration, got %d", len(n.Artifacts))
			}
			for _, a := range n.Artifacts {
				switch a.ClipHash {
				case "hash-r1":
					if a.Format != "mkv" {
						t.Fatalf("expected mkv, got %s", a.Format)
					}
					if a.ArtifactType != ipcpb.ArtifactEvent_ARTIFACT_TYPE_VOD {
						t.Fatalf("expected VOD type, got %d", a.ArtifactType)
					}
					if a.StreamName != "live+rehydrate" {
						t.Fatalf("expected stream name, got %s", a.StreamName)
					}
				case "hash-r2":
					if a.ArtifactType != ipcpb.ArtifactEvent_ARTIFACT_TYPE_DVR {
						t.Fatalf("expected DVR type, got %d", a.ArtifactType)
					}
				default:
					t.Fatalf("unexpected artifact hash: %s", a.ClipHash)
				}
			}
		}
	}
	if !found {
		t.Fatal("node not found after rehydration")
	}
}

func TestTripleWrite_RehydrationLossy(t *testing.T) {
	smA := ResetDefaultManagerForTests()
	mr := miniredis.RunT(t)
	clientA := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = clientA.Close() })

	storeA := NewRedisStateStore(clientA, "test-cluster")
	logger := logrus.New()
	logger.SetLevel(logrus.WarnLevel)

	if err := smA.EnableRedisSync(context.Background(), storeA, "instance-a", logger); err != nil {
		t.Fatalf("EnableRedisSync: %v", err)
	}

	// Set artifact with fields that don't survive Redis round-trip
	smA.SetNodeArtifacts("node-1", []*ipcpb.StoredArtifact{
		{
			ClipHash:     "hash-lossy",
			FilePath:     "/data/clip.mp4",
			SizeBytes:    1000,
			CreatedAt:    1700000000,
			HasDtsh:      true,
			AccessCount:  42,
			LastAccessed: 1700001000,
		},
	}, ArtifactReportOrder{Fence: 1, Seq: 1})

	smA.Shutdown()

	// Rehydrate into fresh SM
	smB := NewStreamStateManager()
	clientB := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = clientB.Close() })
	storeB := NewRedisStateStore(clientB, "test-cluster")

	smB.TouchNode("node-1", true)
	if err := smB.EnableRedisSync(context.Background(), storeB, "instance-b", logger); err != nil {
		t.Fatalf("EnableRedisSync: %v", err)
	}
	t.Cleanup(smB.Shutdown)

	snap := smB.GetAllNodesSnapshot()
	for _, n := range snap.Nodes {
		if n.NodeID == "node-1" {
			if len(n.Artifacts) != 1 {
				t.Fatalf("expected 1 artifact, got %d", len(n.Artifacts))
			}
			a := n.Artifacts[0]
			// These fields are NOT stored in Redis (NodeArtifactState doesn't have them)
			if a.CreatedAt != 0 {
				t.Fatalf("expected CreatedAt=0 after rehydration, got %d", a.CreatedAt)
			}
			if a.HasDtsh != false {
				t.Fatal("expected HasDtsh=false after rehydration")
			}
			if a.AccessCount != 0 {
				t.Fatalf("expected AccessCount=0 after rehydration, got %d", a.AccessCount)
			}
			if a.LastAccessed != 0 {
				t.Fatalf("expected LastAccessed=0 after rehydration, got %d", a.LastAccessed)
			}
			// These fields ARE stored in Redis
			if a.ClipHash != "hash-lossy" {
				t.Fatalf("expected hash-lossy, got %s", a.ClipHash)
			}
			if a.SizeBytes != 1000 {
				t.Fatalf("expected 1000, got %d", a.SizeBytes)
			}
			return
		}
	}
	t.Fatal("node not found")
}

func TestTripleWrite_PubSubCrossInstance(t *testing.T) {
	mr := miniredis.RunT(t)
	logger := logrus.New()
	logger.SetLevel(logrus.WarnLevel)

	// Instance A
	smA := NewStreamStateManager()
	clientA := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = clientA.Close() })
	storeA := NewRedisStateStore(clientA, "test-cluster")
	if err := smA.EnableRedisSync(context.Background(), storeA, "instance-a", logger); err != nil {
		t.Fatalf("EnableRedisSync A: %v", err)
	}

	// Instance B
	smB := NewStreamStateManager()
	clientB := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = clientB.Close() })
	storeB := NewRedisStateStore(clientB, "test-cluster")

	// Pre-create node in B so PubSub update has a target
	smB.TouchNode("node-1", true)

	if err := smB.EnableRedisSync(context.Background(), storeB, "instance-b", logger); err != nil {
		t.Fatalf("EnableRedisSync B: %v", err)
	}

	t.Cleanup(smA.Shutdown)
	t.Cleanup(smB.Shutdown)

	// Give PubSub subscription time to establish
	time.Sleep(50 * time.Millisecond)

	// A writes artifacts
	smA.SetNodeArtifacts("node-1", []*ipcpb.StoredArtifact{
		{
			ClipHash:     "pubsub-hash",
			FilePath:     "/data/pubsub.mp4",
			SizeBytes:    777,
			StreamName:   "vod+pubsub-test",
			ArtifactType: ipcpb.ArtifactEvent_ARTIFACT_TYPE_CLIP,
			Format:       "mp4",
		},
	}, ArtifactReportOrder{Fence: 1, Seq: 1})

	// Poll B's state (async PubSub delivery)
	var foundInB bool
	for i := 0; i < 50; i++ {
		time.Sleep(50 * time.Millisecond)
		snap := smB.GetAllNodesSnapshot()
		for _, n := range snap.Nodes {
			if n.NodeID == "node-1" && len(n.Artifacts) == 1 {
				a := n.Artifacts[0]
				if a.ClipHash == "pubsub-hash" && a.Format == "mp4" && a.StreamName == "vod+pubsub-test" {
					foundInB = true
				}
			}
		}
		if foundInB {
			break
		}
	}

	if !foundInB {
		t.Fatal("Instance B did not receive artifacts via PubSub within timeout")
	}
}

func TestTripleWrite_PubSubSelfFilter(t *testing.T) {
	sm, _, _, _ := setupTripleWriteTest(t)

	// Give subscription time to establish
	time.Sleep(50 * time.Millisecond)

	sm.SetNodeArtifacts("node-1", []*ipcpb.StoredArtifact{
		{ClipHash: "self-hash", FilePath: "/data/self.mp4", SizeBytes: 100},
	}, ArtifactReportOrder{Fence: 1, Seq: 1})

	// Wait for any PubSub roundtrip
	time.Sleep(200 * time.Millisecond)

	// Verify in-memory has exactly 1 artifact (not duplicated by self-delivery)
	snap := sm.GetAllNodesSnapshot()
	for _, n := range snap.Nodes {
		if n.NodeID == "node-1" {
			if len(n.Artifacts) != 1 {
				t.Fatalf("expected 1 artifact (self-filter should prevent double-write), got %d", len(n.Artifacts))
			}
			return
		}
	}
	t.Fatal("node not found")
}

func TestTripleWrite_AddRemoveCycle(t *testing.T) {
	sm, _, mr, _ := setupTripleWriteTest(t)

	// Set 2 artifacts
	sm.SetNodeArtifacts("node-1", []*ipcpb.StoredArtifact{
		{ClipHash: "h1", FilePath: "/data/h1.mp4", SizeBytes: 100},
		{ClipHash: "h2", FilePath: "/data/h2.mp4", SizeBytes: 200},
	}, ArtifactReportOrder{Fence: 1, Seq: 1})

	// Add 3rd
	sm.AddNodeArtifact("node-1", &ipcpb.StoredArtifact{
		ClipHash: "h3", FilePath: "/data/h3.mp4", SizeBytes: 300,
	})

	// Remove 1st
	sm.RemoveNodeArtifact("node-1", "h1")

	// Verify in-memory reflects the point deltas: h2 and h3 (h1 removed, h3 added).
	snap := sm.GetAllNodesSnapshot()
	for _, n := range snap.Nodes {
		if n.NodeID == "node-1" {
			if len(n.Artifacts) != 2 {
				t.Fatalf("expected 2 artifacts after add+remove, got %d", len(n.Artifacts))
			}
			hashes := map[string]bool{}
			for _, a := range n.Artifacts {
				hashes[a.ClipHash] = true
			}
			if !hashes["h2"] || !hashes["h3"] {
				t.Fatalf("expected h2 and h3, got %v", hashes)
			}
			if hashes["h1"] {
				t.Fatal("h1 should have been removed")
			}
		}
	}

	// AddNodeArtifact/RemoveNodeArtifact are IN-MEMORY point deltas: they must NOT touch Redis. Redis
	// still holds the last AUTHORITATIVE versioned report (h1, h2). Cross-instance convergence for a
	// server-authored copy is the next versioned sidecar report / the fenced HA snapshot, not the
	// best-effort local refresh — asserting Redis is unchanged locks in that boundary.
	redisKey := "{test-cluster}:artifacts:node-1"
	val, err := mr.Get(redisKey)
	if err != nil {
		t.Fatalf("Redis GET: %v", err)
	}
	var env NodeArtifactSnapshot
	if err := json.Unmarshal([]byte(val), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	states := env.Artifacts
	if len(states) != 2 {
		t.Fatalf("expected 2 Redis artifacts (last authoritative report), got %d", len(states))
	}
	redisHashes := map[string]bool{}
	for _, s := range states {
		redisHashes[s.ClipHash] = true
	}
	if !redisHashes["h1"] || !redisHashes["h2"] {
		t.Fatalf("Redis should still hold the last authoritative report (h1, h2), got %v", redisHashes)
	}
}

func TestTripleWrite_TypeInference(t *testing.T) {
	sm, _, mr, _ := setupTripleWriteTest(t)

	// Artifact with UNSPECIFIED type but DVR-like path
	sm.SetNodeArtifacts("node-1", []*ipcpb.StoredArtifact{
		{
			ClipHash:     "dvr-inferred",
			FilePath:     "/recordings/dvr/somehash",
			SizeBytes:    5000,
			ArtifactType: ipcpb.ArtifactEvent_ARTIFACT_TYPE_UNSPECIFIED,
		},
	}, ArtifactReportOrder{Fence: 1, Seq: 1})

	// Verify Redis stored inferred type
	redisKey := "{test-cluster}:artifacts:node-1"
	val, err := mr.Get(redisKey)
	if err != nil {
		t.Fatalf("Redis GET: %v", err)
	}
	var env NodeArtifactSnapshot
	if err := json.Unmarshal([]byte(val), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	states := env.Artifacts
	if len(states) != 1 {
		t.Fatalf("expected 1, got %d", len(states))
	}
	if states[0].ArtifactType != "dvr" {
		t.Fatalf("expected inferred type 'dvr', got %q", states[0].ArtifactType)
	}
}
