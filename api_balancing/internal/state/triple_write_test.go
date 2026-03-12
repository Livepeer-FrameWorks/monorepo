package state

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	pb "frameworks/pkg/proto"

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
func (r *tripleWriteArtifactRepo) IsSynced(_ context.Context, _ string) (bool, error) {
	return false, nil
}
func (r *tripleWriteArtifactRepo) GetCachedAt(_ context.Context, _ string) (int64, error) {
	return 0, nil
}
func (r *tripleWriteArtifactRepo) ListAllNodeArtifacts(_ context.Context) (map[string][]ArtifactRecord, error) {
	return nil, nil
}
func (r *tripleWriteArtifactRepo) MarkNodeArtifactsOrphaned(ctx context.Context, nodeID string) error {
	r.mu.Lock()
	r.markOrphanedCalls = append(r.markOrphanedCalls, nodeID)
	r.mu.Unlock()
	if r.markOrphanedFn != nil {
		return r.markOrphanedFn(ctx, nodeID)
	}
	return nil
}
func (r *tripleWriteArtifactRepo) NeedsDtshSync(_ context.Context, _ string) bool {
	return r.needsDtshSyncResult
}
func (r *tripleWriteArtifactRepo) UpdateDVRProgressByHash(_ context.Context, _, _ string, _ int64) error {
	return nil
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

func TestTripleWrite_RoundTrip(t *testing.T) {
	sm, _, mr, _ := setupTripleWriteTest(t)

	artifacts := []*pb.StoredArtifact{
		{
			ClipHash:     "hash-1",
			FilePath:     "/data/clips/hash-1.mp4",
			SizeBytes:    1024,
			StreamName:   "vod+my-stream",
			ArtifactType: pb.ArtifactEvent_ARTIFACT_TYPE_CLIP,
			Format:       "mp4",
		},
	}
	sm.SetNodeArtifacts("node-1", artifacts)

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

	var states []*NodeArtifactState
	if err := json.Unmarshal([]byte(val), &states); err != nil {
		t.Fatalf("unmarshal Redis: %v", err)
	}
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

	sm.SetNodeArtifacts("node-1", []*pb.StoredArtifact{
		{
			ClipHash:     "hash-1",
			FilePath:     "/data/clips/hash-1.mp4",
			SizeBytes:    2048,
			StreamName:   "vod+stream-a",
			ArtifactType: pb.ArtifactEvent_ARTIFACT_TYPE_CLIP,
		},
	})

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
}

func TestTripleWrite_EmptyArtifactsOrphansDB(t *testing.T) {
	sm, _, mr, repo := setupTripleWriteTest(t)

	// First add something, then clear
	sm.SetNodeArtifacts("node-1", []*pb.StoredArtifact{
		{ClipHash: "hash-1", FilePath: "/data/h1.mp4"},
	})
	time.Sleep(50 * time.Millisecond)

	sm.SetNodeArtifacts("node-1", nil)
	time.Sleep(100 * time.Millisecond)

	// Verify Redis has empty array
	redisKey := "{test-cluster}:artifacts:node-1"
	val, err := mr.Get(redisKey)
	if err != nil {
		t.Fatalf("Redis GET: %v", err)
	}
	if val != "[]" && val != "null" {
		var states []*NodeArtifactState
		if err := json.Unmarshal([]byte(val), &states); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(states) != 0 {
			t.Fatalf("expected empty Redis array, got %d items", len(states))
		}
	}

	// Verify MarkNodeArtifactsOrphaned was called
	repo.mu.Lock()
	defer repo.mu.Unlock()
	if len(repo.markOrphanedCalls) == 0 {
		t.Fatal("expected MarkNodeArtifactsOrphaned to be called")
	}
	if repo.markOrphanedCalls[len(repo.markOrphanedCalls)-1] != "node-1" {
		t.Fatalf("expected orphan call for node-1, got %s", repo.markOrphanedCalls[len(repo.markOrphanedCalls)-1])
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

	smA.SetNodeArtifacts("node-1", []*pb.StoredArtifact{
		{
			ClipHash:     "hash-r1",
			FilePath:     "/data/r1.mp4",
			SizeBytes:    512,
			StreamName:   "live+rehydrate",
			ArtifactType: pb.ArtifactEvent_ARTIFACT_TYPE_VOD,
			Format:       "mkv",
		},
		{
			ClipHash:     "hash-r2",
			FilePath:     "/data/dvr/r2",
			SizeBytes:    4096,
			StreamName:   "dvr+recording",
			ArtifactType: pb.ArtifactEvent_ARTIFACT_TYPE_DVR,
		},
	})

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
					if a.ArtifactType != pb.ArtifactEvent_ARTIFACT_TYPE_VOD {
						t.Fatalf("expected VOD type, got %d", a.ArtifactType)
					}
					if a.StreamName != "live+rehydrate" {
						t.Fatalf("expected stream name, got %s", a.StreamName)
					}
				case "hash-r2":
					if a.ArtifactType != pb.ArtifactEvent_ARTIFACT_TYPE_DVR {
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
	smA.SetNodeArtifacts("node-1", []*pb.StoredArtifact{
		{
			ClipHash:     "hash-lossy",
			FilePath:     "/data/clip.mp4",
			SizeBytes:    1000,
			CreatedAt:    1700000000,
			HasDtsh:      true,
			AccessCount:  42,
			LastAccessed: 1700001000,
		},
	})

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
	smA.SetNodeArtifacts("node-1", []*pb.StoredArtifact{
		{
			ClipHash:     "pubsub-hash",
			FilePath:     "/data/pubsub.mp4",
			SizeBytes:    777,
			StreamName:   "vod+pubsub-test",
			ArtifactType: pb.ArtifactEvent_ARTIFACT_TYPE_CLIP,
			Format:       "mp4",
		},
	})

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

	sm.SetNodeArtifacts("node-1", []*pb.StoredArtifact{
		{ClipHash: "self-hash", FilePath: "/data/self.mp4", SizeBytes: 100},
	})

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
	sm.SetNodeArtifacts("node-1", []*pb.StoredArtifact{
		{ClipHash: "h1", FilePath: "/data/h1.mp4", SizeBytes: 100},
		{ClipHash: "h2", FilePath: "/data/h2.mp4", SizeBytes: 200},
	})

	// Add 3rd
	sm.AddNodeArtifact("node-1", &pb.StoredArtifact{
		ClipHash: "h3", FilePath: "/data/h3.mp4", SizeBytes: 300,
	})

	// Remove 1st
	sm.RemoveNodeArtifact("node-1", "h1")

	// Verify in-memory: should have h2 and h3
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

	// Verify Redis matches
	redisKey := "{test-cluster}:artifacts:node-1"
	val, err := mr.Get(redisKey)
	if err != nil {
		t.Fatalf("Redis GET: %v", err)
	}
	var states []*NodeArtifactState
	if err := json.Unmarshal([]byte(val), &states); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(states) != 2 {
		t.Fatalf("expected 2 Redis artifacts, got %d", len(states))
	}
	redisHashes := map[string]bool{}
	for _, s := range states {
		redisHashes[s.ClipHash] = true
	}
	if !redisHashes["h2"] || !redisHashes["h3"] {
		t.Fatalf("Redis expected h2 and h3, got %v", redisHashes)
	}
}

func TestTripleWrite_TypeInference(t *testing.T) {
	sm, _, mr, _ := setupTripleWriteTest(t)

	// Artifact with UNSPECIFIED type but DVR-like path
	sm.SetNodeArtifacts("node-1", []*pb.StoredArtifact{
		{
			ClipHash:     "dvr-inferred",
			FilePath:     "/recordings/dvr/somehash",
			SizeBytes:    5000,
			ArtifactType: pb.ArtifactEvent_ARTIFACT_TYPE_UNSPECIFIED,
		},
	})

	// Verify Redis stored inferred type
	redisKey := "{test-cluster}:artifacts:node-1"
	val, err := mr.Get(redisKey)
	if err != nil {
		t.Fatalf("Redis GET: %v", err)
	}
	var states []*NodeArtifactState
	if err := json.Unmarshal([]byte(val), &states); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(states) != 1 {
		t.Fatalf("expected 1, got %d", len(states))
	}
	if states[0].ArtifactType != "dvr" {
		t.Fatalf("expected inferred type 'dvr', got %q", states[0].ArtifactType)
	}
}
