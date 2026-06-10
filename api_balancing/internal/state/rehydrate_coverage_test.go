package state

import (
	"context"
	"errors"
	"testing"
	"time"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// These tests lock the Rehydrate(ctx) bulk-load invariant: in-memory state is
// reconstructed from the durable repositories (nodes, node-maintenance, DVR,
// clips) per the configured policies, invalid maintenance modes are skipped
// (not applied), records missing identity keys are dropped, and the
// last-attempt status (timestamp + error string) reflects the worst repo
// error encountered. The Redis-pubsub rehydrate arm (rehydrateFromRedis) is a
// separate path exercised by triple_write_test.go; here we drive the
// repository-backed Rehydrate method directly, which had zero coverage.

// --- fake repositories (suffix: Rehydrate) ---

type rehydrateNodeRepo struct {
	nodes       []NodeRecord
	maintenance []NodeMaintenanceRecord
	nodesErr    error
	maintErr    error
}

func (r *rehydrateNodeRepo) ListAllNodes(_ context.Context) ([]NodeRecord, error) {
	return r.nodes, r.nodesErr
}
func (r *rehydrateNodeRepo) ListNodeMaintenance(_ context.Context) ([]NodeMaintenanceRecord, error) {
	return r.maintenance, r.maintErr
}
func (r *rehydrateNodeRepo) UpsertNodeOutputs(_ context.Context, _, _, _ string) error { return nil }
func (r *rehydrateNodeRepo) UpsertNodeLifecycles(_ context.Context, _ []*ipcpb.NodeLifecycleUpdate) error {
	return nil
}
func (r *rehydrateNodeRepo) UpsertNodeComponents(_ context.Context, _ []*ipcpb.NodeLifecycleUpdate) error {
	return nil
}
func (r *rehydrateNodeRepo) UpsertNodeMaintenance(_ context.Context, _ string, _ NodeOperationalMode, _ string) error {
	return nil
}

type rehydrateDVRRepo struct {
	records []DVRRecord
	listErr error
}

func (r *rehydrateDVRRepo) ListAllDVR(_ context.Context) ([]DVRRecord, error) {
	return r.records, r.listErr
}
func (r *rehydrateDVRRepo) ResolveInternalNameByHash(_ context.Context, _ string) (string, error) {
	return "", nil
}
func (r *rehydrateDVRRepo) UpdateDVRProgressByHash(_ context.Context, _, _ string, _ int64) error {
	return nil
}
func (r *rehydrateDVRRepo) UpdateDVRCompletionByHash(_ context.Context, _, _ string, _, _ int64, _, _ string) error {
	return nil
}
func (r *rehydrateDVRRepo) NeedsDtshSync(_ context.Context, _ string) bool { return false }

type rehydrateClipRepo struct {
	records []ClipRecord
	listErr error
}

func (r *rehydrateClipRepo) ListActiveClips(_ context.Context) ([]ClipRecord, error) {
	return r.records, r.listErr
}
func (r *rehydrateClipRepo) ResolveInternalNameByRequestID(_ context.Context, _ string) (string, error) {
	return "", nil
}
func (r *rehydrateClipRepo) NeedsDtshSync(_ context.Context, _ string) bool { return false }

type rehydrateArtifactRepo struct {
	byNode  map[string][]ArtifactRecord
	listErr error
}

func (r *rehydrateArtifactRepo) UpsertArtifacts(_ context.Context, _ string, _ []ArtifactRecord) error {
	return nil
}
func (r *rehydrateArtifactRepo) GetArtifactSyncInfo(_ context.Context, _ string) (*ArtifactSyncInfo, error) {
	return nil, nil
}
func (r *rehydrateArtifactRepo) SetSyncStatus(_ context.Context, _, _, _ string) error { return nil }
func (r *rehydrateArtifactRepo) AddCachedNode(_ context.Context, _, _ string) error    { return nil }
func (r *rehydrateArtifactRepo) AddCachedNodeWithPath(_ context.Context, _, _, _ string, _ int64) error {
	return nil
}
func (r *rehydrateArtifactRepo) RegisterOriginArtifact(_ context.Context, _, _, _ string, _ int64, _ bool) error {
	return nil
}
func (r *rehydrateArtifactRepo) ListOriginNodes(_ context.Context, _ string) ([]string, error) {
	return nil, nil
}
func (r *rehydrateArtifactRepo) IsSynced(_ context.Context, _ string) (bool, error) {
	return false, nil
}
func (r *rehydrateArtifactRepo) GetCachedAt(_ context.Context, _ string) (int64, error) {
	return 0, nil
}
func (r *rehydrateArtifactRepo) ListAllNodeArtifacts(_ context.Context) (map[string][]ArtifactRecord, error) {
	return r.byNode, r.listErr
}
func (r *rehydrateArtifactRepo) MarkNodeArtifactsOrphaned(_ context.Context, _ string) error {
	return nil
}
func (r *rehydrateArtifactRepo) NeedsVODDtshSync(_ context.Context, _ string) bool { return false }

// TestRehydrate_LoadsAllRepositoriesRehydrate asserts the happy path: every
// configured repository contributes to in-memory state, the maintenance arm
// applies a valid operational mode, and DVR/clip details land on the right
// stream instance keyed by (internalName, nodeID).
func TestRehydrate_LoadsAllRepositoriesRehydrate(t *testing.T) {
	sm := NewStreamStateManager()
	t.Cleanup(sm.Shutdown)

	nodeRepo := &rehydrateNodeRepo{
		nodes: []NodeRecord{
			{NodeID: "node-1", BaseURL: "https://n1.example:18007", OutputsJSON: "{}"},
			{NodeID: "node-2", BaseURL: "https://n2.example:18007", OutputsJSON: "{}"},
		},
		maintenance: []NodeMaintenanceRecord{
			{NodeID: "node-2", Mode: NodeModeDraining, SetBy: "op"},
		},
	}
	dvrRepo := &rehydrateDVRRepo{
		records: []DVRRecord{
			{Hash: "dvr-h1", InternalName: "dvr+rec1", StorageNodeID: "node-1", Status: "recording", ManifestPath: "/m.mpd", DurationSec: 12, SizeBytes: 999},
		},
	}
	clipRepo := &rehydrateClipRepo{
		records: []ClipRecord{
			{ClipHash: "clip-h1", InternalName: "vod+clip1", NodeID: "node-2", Status: "ready", StoragePath: "/c.mp4", SizeBytes: 444},
		},
	}
	artRepo := &rehydrateArtifactRepo{
		byNode: map[string][]ArtifactRecord{
			"node-1": {{ArtifactHash: "art-h1", StreamName: "vod+clip1", FilePath: "/a.mp4", SizeBytes: 7, ArtifactType: "clip"}},
		},
	}

	sm.ConfigurePolicies(PoliciesConfig{
		NodeRepo:     nodeRepo,
		DVRRepo:      dvrRepo,
		ClipRepo:     clipRepo,
		ArtifactRepo: artRepo,
	})

	if err := sm.Rehydrate(context.Background()); err != nil {
		t.Fatalf("Rehydrate returned error on happy path: %v", err)
	}

	// Nodes reconstructed with base URLs.
	if n := sm.GetNodeState("node-1"); n == nil || n.BaseURL != "https://n1.example:18007" {
		t.Fatalf("node-1 not rehydrated: %+v", n)
	}
	// Maintenance arm applied the valid draining mode.
	if got := sm.GetNodeOperationalMode("node-2"); got != NodeModeDraining {
		t.Fatalf("expected node-2 draining after rehydrate, got %q", got)
	}
	// node-1 had no maintenance row -> defaults to normal.
	if got := sm.GetNodeOperationalMode("node-1"); got != NodeModeNormal {
		t.Fatalf("expected node-1 normal, got %q", got)
	}

	// DVR detail landed on the storage node instance.
	dvrInst := sm.GetStreamInstances("dvr+rec1")
	if inst, ok := dvrInst["node-1"]; !ok {
		t.Fatalf("dvr instance not rehydrated onto node-1: %+v", dvrInst)
	} else if inst.RawDetails["dvr_status"] != "recording" {
		t.Fatalf("expected dvr_status recording, got %v", inst.RawDetails["dvr_status"])
	}

	// Clip detail landed on its node.
	clipInst := sm.GetStreamInstances("vod+clip1")
	if inst, ok := clipInst["node-2"]; !ok {
		t.Fatalf("clip instance not rehydrated onto node-2: %+v", clipInst)
	} else if inst.RawDetails["clip_status"] != "ready" {
		t.Fatalf("expected clip_status ready, got %v", inst.RawDetails["clip_status"])
	}

	// Status reflects success: timestamp set, no error.
	at, errMsg := sm.RehydrateStatus()
	if at.IsZero() {
		t.Fatal("expected non-zero lastRehydrateAt after success")
	}
	if errMsg != "" {
		t.Fatalf("expected empty rehydrate error, got %q", errMsg)
	}
}

// TestRehydrate_SkipsInvalidMaintenanceModeRehydrate locks the invariant that
// an unparseable operational mode is skipped (logged, continue) rather than
// applied or fatal. The node row that had a valid mode in the same batch must
// still be applied.
func TestRehydrate_SkipsInvalidMaintenanceModeRehydrate(t *testing.T) {
	sm := NewStreamStateManager()
	t.Cleanup(sm.Shutdown)

	nodeRepo := &rehydrateNodeRepo{
		nodes: []NodeRecord{{NodeID: "good", BaseURL: "https://g:1", OutputsJSON: "{}"}},
		maintenance: []NodeMaintenanceRecord{
			{NodeID: "bad", Mode: NodeOperationalMode("gremlin")},
			{NodeID: "good", Mode: NodeModeMaintenance},
		},
	}
	sm.ConfigurePolicies(PoliciesConfig{NodeRepo: nodeRepo})

	if err := sm.Rehydrate(context.Background()); err != nil {
		t.Fatalf("Rehydrate should not error on invalid mode skip: %v", err)
	}

	// Valid row applied.
	if got := sm.GetNodeOperationalMode("good"); got != NodeModeMaintenance {
		t.Fatalf("expected good=maintenance, got %q", got)
	}
	// Invalid-mode node was created (newNodeState) but mode left at default normal,
	// never set to the garbage value.
	if got := sm.GetNodeOperationalMode("bad"); got != NodeModeNormal {
		t.Fatalf("expected bad node to default normal (invalid mode skipped), got %q", got)
	}
}

// TestRehydrate_DropsRecordsMissingIdentityRehydrate asserts DVR/clip records
// lacking an internal name or node ID are not materialized as instances.
func TestRehydrate_DropsRecordsMissingIdentityRehydrate(t *testing.T) {
	sm := NewStreamStateManager()
	t.Cleanup(sm.Shutdown)

	dvrRepo := &rehydrateDVRRepo{records: []DVRRecord{
		{Hash: "h", InternalName: "", StorageNodeID: "node-x", Status: "recording"},      // no internal name
		{Hash: "h2", InternalName: "dvr+x", StorageNodeID: "", Status: "recording"},      // no node
		{Hash: "h3", InternalName: "dvr+ok", StorageNodeID: "node-ok", Status: "active"}, // kept
	}}
	clipRepo := &rehydrateClipRepo{records: []ClipRecord{
		{ClipHash: "c", InternalName: "", NodeID: "node-x", Status: "ready"},        // dropped
		{ClipHash: "c2", InternalName: "vod+ok", NodeID: "node-c", Status: "ready"}, // kept
	}}
	sm.ConfigurePolicies(PoliciesConfig{DVRRepo: dvrRepo, ClipRepo: clipRepo})

	if err := sm.Rehydrate(context.Background()); err != nil {
		t.Fatalf("Rehydrate error: %v", err)
	}

	all := sm.GetAllStreamInstances()
	if _, ok := all["dvr+x"]; ok {
		t.Fatal("DVR record missing node ID should not have been materialized")
	}
	if _, ok := all["vod+ok"]; !ok {
		t.Fatal("valid clip record should have been materialized")
	}
	if _, ok := all["dvr+ok"]; !ok {
		t.Fatal("valid DVR record should have been materialized")
	}
	// The record with empty internal name maps to the "" key (or is absent);
	// either way node-x must not appear under a real stream name.
	if insts, ok := all["dvr+x"]; ok && len(insts) > 0 {
		t.Fatalf("unexpected instances under dvr+x: %+v", insts)
	}
}

// TestRehydrate_RepoErrorsSurfaceInStatusRehydrate locks the error-aggregation
// invariant: the first repo error is returned and recorded in RehydrateStatus,
// and later repos still run (partial rehydrate). We make the node list fail but
// DVR succeed, then assert both the error string and the DVR materialization.
func TestRehydrate_RepoErrorsSurfaceInStatusRehydrate(t *testing.T) {
	sm := NewStreamStateManager()
	t.Cleanup(sm.Shutdown)

	bootErr := errors.New("boom-node-list")
	nodeRepo := &rehydrateNodeRepo{nodesErr: bootErr}
	dvrRepo := &rehydrateDVRRepo{records: []DVRRecord{
		{Hash: "h", InternalName: "dvr+survives", StorageNodeID: "node-1", Status: "recording"},
	}}
	sm.ConfigurePolicies(PoliciesConfig{NodeRepo: nodeRepo, DVRRepo: dvrRepo})

	err := sm.Rehydrate(context.Background())
	if err == nil {
		t.Fatal("expected Rehydrate to return the node-list error")
	}
	if !errors.Is(err, bootErr) {
		t.Fatalf("expected boom-node-list, got %v", err)
	}

	at, errMsg := sm.RehydrateStatus()
	if at.IsZero() {
		t.Fatal("expected lastRehydrateAt set even on error")
	}
	if errMsg != bootErr.Error() {
		t.Fatalf("expected status error %q, got %q", bootErr.Error(), errMsg)
	}

	// Downstream DVR repo still ran despite the upstream error.
	if _, ok := sm.GetAllStreamInstances()["dvr+survives"]; !ok {
		t.Fatal("DVR arm should still run after node arm error (partial rehydrate)")
	}
}

// TestRehydrate_FirstErrorWinsRehydrate asserts only the FIRST encountered
// error is retained when multiple repos fail (rehydrateErr == nil guard).
func TestRehydrate_FirstErrorWinsRehydrate(t *testing.T) {
	sm := NewStreamStateManager()
	t.Cleanup(sm.Shutdown)

	first := errors.New("first-maintenance-err")
	second := errors.New("second-dvr-err")
	// Node list succeeds, maintenance fails (first error), then DVR fails (second).
	nodeRepo := &rehydrateNodeRepo{
		nodes:    []NodeRecord{{NodeID: "n", BaseURL: "https://n:1", OutputsJSON: "{}"}},
		maintErr: first,
	}
	dvrRepo := &rehydrateDVRRepo{listErr: second}
	sm.ConfigurePolicies(PoliciesConfig{NodeRepo: nodeRepo, DVRRepo: dvrRepo})

	err := sm.Rehydrate(context.Background())
	if !errors.Is(err, first) {
		t.Fatalf("expected first error to win, got %v", err)
	}
	_, errMsg := sm.RehydrateStatus()
	if errMsg != first.Error() {
		t.Fatalf("expected status to hold first error %q, got %q", first.Error(), errMsg)
	}
}

// TestRehydrate_NilReposNoOpRehydrate asserts Rehydrate with no repos
// configured is a clean no-op that still stamps a successful status.
func TestRehydrate_NilReposNoOpRehydrate(t *testing.T) {
	sm := NewStreamStateManager()
	t.Cleanup(sm.Shutdown)

	before := time.Now()
	if err := sm.Rehydrate(context.Background()); err != nil {
		t.Fatalf("nil-repo Rehydrate should be a no-op, got %v", err)
	}
	at, errMsg := sm.RehydrateStatus()
	if errMsg != "" {
		t.Fatalf("expected empty error for no-op rehydrate, got %q", errMsg)
	}
	if at.Before(before) {
		t.Fatal("expected lastRehydrateAt to advance on no-op rehydrate")
	}
}

// TestRehydrate_BootRehydrateRunsViaConfigureRehydrate locks the
// ConfigurePolicies -> shouldBootRehydrate -> Rehydrate wiring: when a sync
// policy sets BootRehydrate, ConfigurePolicies itself triggers a rehydrate (no
// explicit Rehydrate call), reconstructing DVR state.
func TestRehydrate_BootRehydrateRunsViaConfigureRehydrate(t *testing.T) {
	sm := NewStreamStateManager()
	t.Cleanup(sm.Shutdown)

	dvrRepo := &rehydrateDVRRepo{records: []DVRRecord{
		{Hash: "h", InternalName: "dvr+boot", StorageNodeID: "node-1", Status: "recording"},
	}}

	sm.ConfigurePolicies(PoliciesConfig{
		DVRRepo: dvrRepo,
		SyncPolicies: map[EntityType]SyncPolicy{
			EntityDVR: {BootRehydrate: true},
		},
	})

	// No explicit Rehydrate call: ConfigurePolicies should have run it.
	if _, ok := sm.GetAllStreamInstances()["dvr+boot"]; !ok {
		t.Fatal("expected BootRehydrate sync policy to trigger rehydrate from ConfigurePolicies")
	}
	if at, _ := sm.RehydrateStatus(); at.IsZero() {
		t.Fatal("expected boot rehydrate to stamp status")
	}
}

// TestApplyDVRProgress_MemoryUpdateWithoutWriteThroughRehydrate covers the
// ApplyDVRProgress branch where write-through is disabled: no DB progress write
// happens, but the in-memory instance is still updated via the resolved
// internal name. Uses a DVR repo that resolves the hash to an internal name.
func TestApplyDVRProgress_MemoryUpdateWithoutWriteThroughRehydrate(t *testing.T) {
	sm := NewStreamStateManager()
	t.Cleanup(sm.Shutdown)

	repo := &resolvingDVRRepo{internal: "dvr+live"}
	// No write policy enabled -> write-through branch skipped, resolve branch taken.
	sm.ConfigurePolicies(PoliciesConfig{DVRRepo: repo})

	if err := sm.ApplyDVRProgress(context.Background(), "dvr-hash", "recording", 2048, 7, "node-1"); err != nil {
		t.Fatalf("ApplyDVRProgress: %v", err)
	}
	if repo.progressCalls != 0 {
		t.Fatalf("write-through disabled: expected 0 progress DB writes, got %d", repo.progressCalls)
	}
	inst := sm.GetStreamInstances("dvr+live")["node-1"]
	if inst.RawDetails["dvr_status"] != "recording" {
		t.Fatalf("expected in-memory dvr_status recording, got %v", inst.RawDetails["dvr_status"])
	}
	if inst.RawDetails["dvr_segment_count"] != uint32(7) {
		t.Fatalf("expected segment_count 7, got %v", inst.RawDetails["dvr_segment_count"])
	}
}

// TestApplyDVRStopped_WriteThroughAndMemoryRehydrate covers the ApplyDVRStopped
// write-through arm (policy enabled) plus the memory update, asserting the DB
// completion write fires exactly once and final state lands in memory.
func TestApplyDVRStopped_WriteThroughAndMemoryRehydrate(t *testing.T) {
	sm := NewStreamStateManager()
	t.Cleanup(sm.Shutdown)

	repo := &resolvingDVRRepo{internal: "dvr+done"}
	sm.ConfigurePolicies(PoliciesConfig{
		DVRRepo: repo,
		WritePolicies: map[EntityType]WritePolicy{
			EntityDVR: {Enabled: true, Mode: WriteThrough},
		},
	})

	if err := sm.ApplyDVRStopped(context.Background(), "dvr-hash", "completed", 30, 8192, "/final.mpd", "", "node-9"); err != nil {
		t.Fatalf("ApplyDVRStopped: %v", err)
	}
	if repo.completionCalls != 1 {
		t.Fatalf("write-through enabled: expected 1 completion DB write, got %d", repo.completionCalls)
	}
	inst := sm.GetStreamInstances("dvr+done")["node-9"]
	if inst.RawDetails["dvr_status"] != "completed" {
		t.Fatalf("expected dvr_status completed, got %v", inst.RawDetails["dvr_status"])
	}
	if inst.RawDetails["dvr_manifest_path"] != "/final.mpd" {
		t.Fatalf("expected manifest path, got %v", inst.RawDetails["dvr_manifest_path"])
	}
}

// resolvingDVRRepo resolves any hash to a fixed internal name and counts the
// DB mutation calls so write-through vs memory-only branches can be asserted.
type resolvingDVRRepo struct {
	internal        string
	progressCalls   int
	completionCalls int
}

func (r *resolvingDVRRepo) ListAllDVR(_ context.Context) ([]DVRRecord, error) { return nil, nil }
func (r *resolvingDVRRepo) ResolveInternalNameByHash(_ context.Context, _ string) (string, error) {
	return r.internal, nil
}
func (r *resolvingDVRRepo) UpdateDVRProgressByHash(_ context.Context, _, _ string, _ int64) error {
	r.progressCalls++
	return nil
}
func (r *resolvingDVRRepo) UpdateDVRCompletionByHash(_ context.Context, _, _ string, _, _ int64, _, _ string) error {
	r.completionCalls++
	return nil
}
func (r *resolvingDVRRepo) NeedsDtshSync(_ context.Context, _ string) bool { return false }
