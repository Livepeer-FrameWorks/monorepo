package state

import (
	"context"
	"errors"
	"testing"
	"time"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// metricsForCPU sets only the CPU on a node via the anonymous-struct
// UpdateNodeMetrics signature so the cached CPUScore/RAMScore are recomputed.
// Lower CPU => higher idleness score (same direction as the balancer rate()).
func metricsForCPU(sm *StreamStateManager, nodeID string, cpu float64) {
	sm.UpdateNodeMetrics(nodeID, struct {
		CPU                  float64
		RAMMax               float64
		RAMCurrent           float64
		UpSpeed              float64
		DownSpeed            float64
		BWLimit              float64
		CapIngest            bool
		CapEdge              bool
		CapStorage           bool
		CapProcessing        bool
		Roles                []string
		StorageCapacityBytes uint64
		StorageUsedBytes     uint64
		ProcessingClasses    map[string]ClassCapacity
	}{CPU: cpu})
}

// RemoveStream must purge both the union summary AND every per-node instance
// row; a half-cleared stream leaves the balancer pointing at a dead origin.
func TestRemoveStream_ClearsStreamAndAllInstances(t *testing.T) {
	sm := NewStreamStateManager()
	t.Cleanup(sm.Shutdown)

	internalName := "internal-remove"
	// Two nodes carry instance rows for the same stream.
	sm.UpdateNodeStats(internalName, "node-a", 1, 1, 0, 0, false)
	sm.UpdateNodeStats(internalName, "node-b", 1, 1, 0, 0, false)

	if sm.GetStreamState(internalName) == nil {
		t.Fatal("expected stream state before removal")
	}
	if got := sm.GetStreamInstances(internalName); len(got) != 2 {
		t.Fatalf("expected 2 instances before removal, got %d", len(got))
	}

	sm.RemoveStream(internalName)

	if sm.GetStreamState(internalName) != nil {
		t.Fatal("expected stream summary to be cleared")
	}
	if got := sm.GetStreamInstances(internalName); len(got) != 0 {
		t.Fatalf("expected all instances cleared, got %d", len(got))
	}
	// Underlying map entry must be gone, not just empty.
	sm.mu.RLock()
	_, streamOK := sm.streams[internalName]
	_, instOK := sm.streamInstances[internalName]
	sm.mu.RUnlock()
	if streamOK || instOK {
		t.Fatalf("expected map entries deleted: streams=%v instances=%v", streamOK, instOK)
	}
}

// GetClusterSnapshot must hand the caller a DEEP COPY: mutating the returned
// streams (incl. their Tracks slice) and nodes (incl. their Outputs map) must
// NOT corrupt internal state. The balancer decision tree mutates these copies.
func TestGetClusterSnapshot_DeepCopyIsolation(t *testing.T) {
	sm := NewStreamStateManager()
	t.Cleanup(sm.Shutdown)

	internalName := "internal-snap"
	// Seed a stream with a track, and a node with an Outputs map.
	details := `{"video1": {"codec": "H264", "kbits": 1500, "fpks": 30000, "width": 1920, "height": 1080}}`
	if err := sm.UpdateStreamFromBuffer("snap-stream", internalName, "node-snap", "tenant-snap", "FULL", details); err != nil {
		t.Fatalf("UpdateStreamFromBuffer: %v", err)
	}
	sm.SetNodeInfo("node-snap", "http://host-snap:8080", true, nil, nil, "us-east", `{"HLS": "/hls/$"}`, nil)

	streams, nodes := sm.GetClusterSnapshot()
	if len(streams) != 1 {
		t.Fatalf("expected 1 stream in snapshot, got %d", len(streams))
	}

	// Capture internal truth for comparison.
	internalStream := sm.GetStreamState(internalName)
	origTrackCount := len(internalStream.Tracks)
	origBitrate := internalStream.Tracks[0].Bitrate

	// Mutate the snapshot copy aggressively.
	streams[0].Status = "CORRUPTED"
	streams[0].TenantID = "evil-tenant"
	if len(streams[0].Tracks) > 0 {
		streams[0].Tracks[0].Bitrate = 99999
	}

	var nodeCopy *NodeState
	for _, n := range nodes {
		if n.NodeID == "node-snap" {
			nodeCopy = n
		}
	}
	if nodeCopy == nil {
		t.Fatal("expected node-snap in snapshot")
	}
	nodeCopy.BaseURL = "http://evil"
	if nodeCopy.Outputs != nil {
		nodeCopy.Outputs["HLS"] = "/evil"
	}

	// Internal state must be untouched.
	internalStream = sm.GetStreamState(internalName)
	if internalStream.Status == "CORRUPTED" {
		t.Fatal("snapshot mutation leaked into internal stream status")
	}
	if internalStream.TenantID != "tenant-snap" {
		t.Fatalf("snapshot mutation leaked into internal tenant: %q", internalStream.TenantID)
	}
	if len(internalStream.Tracks) != origTrackCount {
		t.Fatalf("track count changed: %d != %d", len(internalStream.Tracks), origTrackCount)
	}
	if internalStream.Tracks[0].Bitrate != origBitrate {
		t.Fatalf("track Bitrate mutated through snapshot copy: got %d want %d", internalStream.Tracks[0].Bitrate, origBitrate)
	}

	internalNode := sm.GetNodeState("node-snap")
	if internalNode.BaseURL == "http://evil" {
		t.Fatal("snapshot mutation leaked into internal node BaseURL")
	}
	if internalNode.Outputs != nil && internalNode.Outputs["HLS"] == "/evil" {
		t.Fatal("snapshot mutation leaked into internal node Outputs map")
	}
}

// CleanupStaleStreams must evict only streams whose LastUpdate predates the
// age window, keeping fresh ones — this is the age-window eviction invariant.
func TestCleanupStaleStreams_AgeWindowEviction(t *testing.T) {
	sm := NewStreamStateManager()
	t.Cleanup(sm.Shutdown)

	staleName := "internal-stale"
	freshName := "internal-fresh"
	sm.UpdateNodeStats(staleName, "node-stale", 1, 1, 0, 0, false)
	sm.UpdateNodeStats(freshName, "node-fresh", 1, 1, 0, 0, false)

	// Backdate the stale stream's LastUpdate well past the window.
	sm.mu.Lock()
	sm.streams[staleName].LastUpdate = time.Now().Add(-10 * time.Minute)
	// Keep fresh stream's LastUpdate at now (UpdateNodeStats set it).
	sm.mu.Unlock()

	sm.CleanupStaleStreams(5 * time.Minute)

	if sm.GetStreamState(staleName) != nil {
		t.Fatal("expected stale stream to be evicted")
	}
	if got := sm.GetStreamInstances(staleName); len(got) != 0 {
		t.Fatalf("expected stale stream instances cleared, got %d", len(got))
	}
	if sm.GetStreamState(freshName) == nil {
		t.Fatal("expected fresh stream to survive eviction")
	}
	if got := sm.GetStreamInstances(freshName); len(got) != 1 {
		t.Fatalf("expected fresh stream instances retained, got %d", len(got))
	}
}

// FindNodesByArtifactHash returns ALL active nodes hosting the hash, each with
// its combined idleness score; inactive nodes are skipped, and nodes without
// the hash are excluded. This list feeds the balancer's artifact routing.
func TestFindNodesByArtifactHash_ScoresAndActiveFiltering(t *testing.T) {
	sm := NewStreamStateManager()
	t.Cleanup(sm.Shutdown)

	hash := "clip-hash-xyz"

	// node-idle and node-busy both have the artifact and are healthy/active.
	sm.SetNodeArtifacts("node-idle", []*ipcpb.StoredArtifact{{ClipHash: hash, FilePath: "/d/x.mp4"}})
	sm.SetNodeArtifacts("node-busy", []*ipcpb.StoredArtifact{{ClipHash: hash, FilePath: "/d/x.mp4"}})
	sm.SetNodeInfo("node-idle", "http://host-idle:8080", true, nil, nil, "", "", nil)
	sm.SetNodeInfo("node-busy", "http://host-busy:8080", true, nil, nil, "", "", nil)
	metricsForCPU(sm, "node-idle", 10) // higher idleness score
	metricsForCPU(sm, "node-busy", 90) // lower idleness score

	// node-other is active but does NOT host the hash.
	sm.SetNodeArtifacts("node-other", []*ipcpb.StoredArtifact{{ClipHash: "different", FilePath: "/d/o.mp4"}})
	sm.SetNodeInfo("node-other", "http://host-other:8080", true, nil, nil, "", "", nil)
	metricsForCPU(sm, "node-other", 10)

	// node-inactive hosts the hash but is unhealthy (IsActive=false), must be skipped.
	sm.SetNodeArtifacts("node-inactive", []*ipcpb.StoredArtifact{{ClipHash: hash, FilePath: "/d/x.mp4"}})
	// SetNodeInfo with isHealthy=false would still leave it stale; just don't make it healthy.
	sm.SetNodeInfo("node-inactive", "http://host-inactive:8080", false, nil, nil, "", "", nil)

	results := sm.FindNodesByArtifactHash(hash)

	got := make(map[string]ArtifactNodeInfo, len(results))
	for _, r := range results {
		got[r.NodeID] = r
	}
	if _, ok := got["node-other"]; ok {
		t.Fatal("node without the hash must not appear")
	}
	if _, ok := got["node-inactive"]; ok {
		t.Fatal("inactive node must be skipped")
	}
	idle, idleOK := got["node-idle"]
	busy, busyOK := got["node-busy"]
	if !idleOK || !busyOK {
		t.Fatalf("expected both active hosting nodes, got %#v", got)
	}
	if idle.Host != "http://host-idle:8080" {
		t.Fatalf("expected idle host URL, got %q", idle.Host)
	}
	if idle.Artifact == nil || idle.Artifact.GetClipHash() != hash {
		t.Fatalf("expected artifact carried on result, got %#v", idle.Artifact)
	}
	// Idleness ordering invariant: the idler node scores strictly higher.
	if idle.Score <= busy.Score {
		t.Fatalf("expected idle score (%d) > busy score (%d)", idle.Score, busy.Score)
	}
}

// UpdateStreamInstanceInfo merges new fields into RawDetails without dropping
// previously-set keys — the field-merge invariant DVR progress relies on.
func TestUpdateStreamInstanceInfo_FieldMerge(t *testing.T) {
	sm := NewStreamStateManager()
	t.Cleanup(sm.Shutdown)

	internalName := "internal-merge"
	nodeID := "node-merge"

	sm.UpdateStreamInstanceInfo(internalName, nodeID, map[string]any{"a": 1, "b": "two"})
	sm.UpdateStreamInstanceInfo(internalName, nodeID, map[string]any{"b": "TWO", "c": true})

	instances := sm.GetStreamInstances(internalName)
	inst, ok := instances[nodeID]
	if !ok {
		t.Fatal("expected instance after UpdateStreamInstanceInfo")
	}
	if inst.RawDetails["a"] != 1 {
		t.Fatalf("expected first-write key 'a' to survive merge, got %#v", inst.RawDetails["a"])
	}
	if inst.RawDetails["b"] != "TWO" {
		t.Fatalf("expected key 'b' overwritten to TWO, got %#v", inst.RawDetails["b"])
	}
	if inst.RawDetails["c"] != true {
		t.Fatalf("expected new key 'c' merged in, got %#v", inst.RawDetails["c"])
	}
}

// fakeDVRRepo resolves a hash to a fixed internal name so ApplyDVRProgress /
// ApplyDVRStopped exercise their stream-instance persistence branch. The
// progress/completion write-throughs are gated off (no write policy) so the
// repo only needs to answer the resolver.
type fakeDVRRepo struct {
	internalName string
	resolveErr   error
}

func (f *fakeDVRRepo) ListAllDVR(_ context.Context) ([]DVRRecord, error) { return nil, nil }
func (f *fakeDVRRepo) ResolveInternalNameByHash(_ context.Context, _ string) (string, error) {
	return f.internalName, f.resolveErr
}
func (f *fakeDVRRepo) UpdateDVRProgressByHash(_ context.Context, _, _ string, _ int64) error {
	return nil
}
func (f *fakeDVRRepo) UpdateDVRCompletionByHash(_ context.Context, _, _ string, _, _ int64, _, _ string) error {
	return nil
}
func (f *fakeDVRRepo) NeedsDtshSync(_ context.Context, _ string) bool { return false }

// ApplyDVRProgress must resolve the DVR hash to its internal stream name and
// stamp the live DVR progress fields onto that node's stream instance.
func TestApplyDVRProgress_PersistsInstanceFields(t *testing.T) {
	sm := NewStreamStateManager()
	t.Cleanup(sm.Shutdown)

	internalName := "internal-dvr-progress"
	nodeID := "node-dvr"
	sm.ConfigurePolicies(PoliciesConfig{DVRRepo: &fakeDVRRepo{internalName: internalName}})

	if err := sm.ApplyDVRProgress(context.Background(), "dvr-hash-1", "recording", 4096, 7, nodeID); err != nil {
		t.Fatalf("ApplyDVRProgress: %v", err)
	}

	inst, ok := sm.GetStreamInstances(internalName)[nodeID]
	if !ok {
		t.Fatal("expected stream instance after ApplyDVRProgress")
	}
	if inst.RawDetails["dvr_status"] != "recording" {
		t.Fatalf("dvr_status = %#v, want recording", inst.RawDetails["dvr_status"])
	}
	if inst.RawDetails["dvr_segment_count"] != uint32(7) {
		t.Fatalf("dvr_segment_count = %#v, want 7", inst.RawDetails["dvr_segment_count"])
	}
	if inst.RawDetails["dvr_size_bytes"] != uint64(4096) {
		t.Fatalf("dvr_size_bytes = %#v, want 4096", inst.RawDetails["dvr_size_bytes"])
	}
}

// When the DVR hash does not resolve to an internal name, ApplyDVRProgress must
// NOT create a phantom stream instance.
func TestApplyDVRProgress_UnresolvedHashIsNoop(t *testing.T) {
	sm := NewStreamStateManager()
	t.Cleanup(sm.Shutdown)

	sm.ConfigurePolicies(PoliciesConfig{DVRRepo: &fakeDVRRepo{resolveErr: errors.New("not found")}})

	if err := sm.ApplyDVRProgress(context.Background(), "dvr-hash-missing", "recording", 1, 1, "node-x"); err != nil {
		t.Fatalf("ApplyDVRProgress: %v", err)
	}
	// No internal name resolved => no instance created anywhere.
	sm.mu.RLock()
	count := len(sm.streamInstances)
	sm.mu.RUnlock()
	if count != 0 {
		t.Fatalf("expected no stream instances for unresolved hash, got %d", count)
	}
}

// ApplyDVRStopped must stamp the final DVR completion fields (status, manifest
// path, duration) onto the resolved stream instance.
func TestApplyDVRStopped_PersistsCompletionFields(t *testing.T) {
	sm := NewStreamStateManager()
	t.Cleanup(sm.Shutdown)

	internalName := "internal-dvr-stopped"
	nodeID := "node-dvr-stop"
	sm.ConfigurePolicies(PoliciesConfig{DVRRepo: &fakeDVRRepo{internalName: internalName}})

	if err := sm.ApplyDVRStopped(context.Background(), "dvr-hash-2", "completed", 123, 8192, "/recordings/dvr/x.m3u8", "", nodeID); err != nil {
		t.Fatalf("ApplyDVRStopped: %v", err)
	}

	inst, ok := sm.GetStreamInstances(internalName)[nodeID]
	if !ok {
		t.Fatal("expected stream instance after ApplyDVRStopped")
	}
	if inst.RawDetails["dvr_status"] != "completed" {
		t.Fatalf("dvr_status = %#v, want completed", inst.RawDetails["dvr_status"])
	}
	if inst.RawDetails["dvr_manifest_path"] != "/recordings/dvr/x.m3u8" {
		t.Fatalf("dvr_manifest_path = %#v", inst.RawDetails["dvr_manifest_path"])
	}
	if inst.RawDetails["dvr_duration_seconds"] != int64(123) {
		t.Fatalf("dvr_duration_seconds = %#v, want 123", inst.RawDetails["dvr_duration_seconds"])
	}
}

// Redis round-trip: SetStream -> GetAllStreams -> DeleteStream must preserve
// the stream identity/fields and then evict it. Backs the HA state sync.
func TestRedisStreamRoundTrip(t *testing.T) {
	store, _, _ := newRedisStateStore(t)

	internalName := "internal-redis-stream"
	in := &StreamState{
		InternalName: internalName,
		StreamName:   "redis-stream",
		TenantID:     "tenant-redis",
		Status:       "live",
		BufferState:  "FULL",
	}
	if err := store.SetStream(internalName, in); err != nil {
		t.Fatalf("SetStream: %v", err)
	}

	all, err := store.GetAllStreams()
	if err != nil {
		t.Fatalf("GetAllStreams: %v", err)
	}
	got, ok := all[internalName]
	if !ok {
		t.Fatalf("expected stream %q in GetAllStreams, got keys %#v", internalName, all)
	}
	if got.TenantID != "tenant-redis" || got.Status != "live" || got.BufferState != "FULL" {
		t.Fatalf("round-trip mismatch: %+v", got)
	}

	if delErr := store.DeleteStream(internalName); delErr != nil {
		t.Fatalf("DeleteStream: %v", delErr)
	}
	all, err = store.GetAllStreams()
	if err != nil {
		t.Fatalf("GetAllStreams after delete: %v", err)
	}
	if _, ok := all[internalName]; ok {
		t.Fatal("expected stream to be deleted from Redis")
	}
}

// Redis round-trip for per-node instance rows: SetStreamInstance ->
// GetAllStreamInstances (keyed by internalName then nodeID) -> Delete. The
// instance record's (InternalName, NodeID) envelope must reconstruct correctly.
func TestRedisStreamInstanceRoundTrip(t *testing.T) {
	store, _, _ := newRedisStateStore(t)

	internalName := "internal-redis-inst"
	nodeID := "node-redis-inst"
	in := &StreamInstanceState{
		NodeID:      nodeID,
		TenantID:    "tenant-redis",
		Status:      "live",
		BufferState: "FULL",
		Inputs:      1,
	}
	if err := store.SetStreamInstance(internalName, nodeID, in); err != nil {
		t.Fatalf("SetStreamInstance: %v", err)
	}

	all, err := store.GetAllStreamInstances()
	if err != nil {
		t.Fatalf("GetAllStreamInstances: %v", err)
	}
	byNode, ok := all[internalName]
	if !ok {
		t.Fatalf("expected instances for %q, got %#v", internalName, all)
	}
	got, ok := byNode[nodeID]
	if !ok {
		t.Fatalf("expected instance for node %q, got %#v", nodeID, byNode)
	}
	if got.Status != "live" || got.TenantID != "tenant-redis" || got.Inputs != 1 {
		t.Fatalf("instance round-trip mismatch: %+v", got)
	}

	if delErr := store.DeleteStreamInstance(internalName, nodeID); delErr != nil {
		t.Fatalf("DeleteStreamInstance: %v", delErr)
	}
	all, err = store.GetAllStreamInstances()
	if err != nil {
		t.Fatalf("GetAllStreamInstances after delete: %v", err)
	}
	if _, ok := all[internalName]; ok {
		t.Fatal("expected instance to be deleted from Redis")
	}
}
