package state

import (
	"encoding/json"
	"testing"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func TestSetNodeArtifacts_CreatesNode(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	sm.SetNodeArtifacts("new-node", []*ipcpb.StoredArtifact{
		{ClipHash: "h1", FilePath: "/data/h1.mp4", SizeBytes: 100},
	}, ArtifactReportOrder{Fence: 1, Seq: 1})

	snap := sm.GetAllNodesSnapshot()
	found := false
	for _, n := range snap.Nodes {
		if n.NodeID == "new-node" {
			found = true
			if len(n.Artifacts) != 1 {
				t.Fatalf("expected 1 artifact, got %d", len(n.Artifacts))
			}
		}
	}
	if !found {
		t.Fatal("node not found after SetNodeArtifacts")
	}
}

// A stale (lower-revision) whole-node report must be dropped BEFORE it mutates in-memory state:
// acceptance precedes mutation. Report rev 100 installs h1; a delayed rev-90 report carrying h2
// must NOT replace it — the node keeps h1.
func TestSetNodeArtifacts_StaleReportDroppedBeforeMutation(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	sm.SetNodeArtifacts("node-1", []*ipcpb.StoredArtifact{{ClipHash: "h1", FilePath: "/d/h1.mp4"}}, ArtifactReportOrder{Fence: 1, Seq: 100})
	// Delayed older report (same connection fence, lower seq) with different content — must be rejected.
	sm.SetNodeArtifacts("node-1", []*ipcpb.StoredArtifact{{ClipHash: "h2", FilePath: "/d/h2.mp4"}}, ArtifactReportOrder{Fence: 1, Seq: 90})

	snap := sm.GetAllNodesSnapshot()
	for _, n := range snap.Nodes {
		if n.NodeID != "node-1" {
			continue
		}
		if len(n.Artifacts) != 1 || n.Artifacts[0].GetClipHash() != "h1" {
			t.Fatalf("stale report corrupted in-memory state: %+v", n.Artifacts)
		}
	}

	// A newer report (higher seq) is still accepted and replaces the content.
	sm.SetNodeArtifacts("node-1", []*ipcpb.StoredArtifact{{ClipHash: "h3", FilePath: "/d/h3.mp4"}}, ArtifactReportOrder{Fence: 1, Seq: 110})
	snap = sm.GetAllNodesSnapshot()
	for _, n := range snap.Nodes {
		if n.NodeID != "node-1" {
			continue
		}
		if len(n.Artifacts) != 1 || n.Artifacts[0].GetClipHash() != "h3" {
			t.Fatalf("newer report not applied: %+v", n.Artifacts)
		}
	}
}

// A reconnect (higher connection fence) supersedes the previous connection regardless of seq — the
// node's per-connection sequence restarts low but the fence ranks it strictly higher.
func TestSetNodeArtifacts_ReconnectFenceSupersedes(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	sm.SetNodeArtifacts("node-1", []*ipcpb.StoredArtifact{{ClipHash: "old", FilePath: "/d/old.mp4"}}, ArtifactReportOrder{Fence: 1, Seq: 500})
	// Reconnect (higher fence) with a LOW seq must still win.
	sm.SetNodeArtifacts("node-1", []*ipcpb.StoredArtifact{{ClipHash: "new", FilePath: "/d/new.mp4"}}, ArtifactReportOrder{Fence: 2, Seq: 1})

	snap := sm.GetAllNodesSnapshot()
	for _, n := range snap.Nodes {
		if n.NodeID != "node-1" {
			continue
		}
		if len(n.Artifacts) != 1 || n.Artifacts[0].GetClipHash() != "new" {
			t.Fatalf("reconnect fence not applied: %+v", n.Artifacts)
		}
	}

	// A delayed report from the OLD connection (lower fence) must be dropped even with a high seq.
	sm.SetNodeArtifacts("node-1", []*ipcpb.StoredArtifact{{ClipHash: "stale", FilePath: "/d/stale.mp4"}}, ArtifactReportOrder{Fence: 1, Seq: 999})
	snap = sm.GetAllNodesSnapshot()
	for _, n := range snap.Nodes {
		if n.NodeID != "node-1" {
			continue
		}
		if len(n.Artifacts) != 1 || n.Artifacts[0].GetClipHash() != "new" {
			t.Fatalf("delayed old-connection report corrupted state: %+v", n.Artifacts)
		}
	}
}

// A reconnect installs its ownership fence as the acceptance floor at REGISTRATION, before the new
// connection has sent any inventory report. A delayed report from the superseded old connection must
// therefore be rejected immediately — even though the new connection's first report has not arrived.
func TestRecordNodeArtifactFence_SupersedesBeforeFirstReport(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	// Old connection (fence 1) reported an inventory.
	sm.SetNodeArtifacts("node-1", []*ipcpb.StoredArtifact{{ClipHash: "old", FilePath: "/d/old.mp4"}}, ArtifactReportOrder{Fence: 1, Seq: 500})

	// Node reconnects: fence 2 is installed as the floor at registration. No report from it yet.
	// (redis store is nil in tests, so the takeover-marker publish is a no-op success.)
	if err := sm.RecordNodeArtifactFence("node-1", 2); err != nil {
		t.Fatalf("RecordNodeArtifactFence(2): %v", err)
	}

	// A delayed report from the OLD connection (fence 1) — even with a very high seq — must lose to the
	// new connection's registered fence, so it cannot resurrect stale inventory during the gap.
	sm.SetNodeArtifacts("node-1", []*ipcpb.StoredArtifact{{ClipHash: "stale", FilePath: "/d/stale.mp4"}}, ArtifactReportOrder{Fence: 1, Seq: 999})

	snap := sm.GetAllNodesSnapshot()
	for _, n := range snap.Nodes {
		if n.NodeID != "node-1" {
			continue
		}
		if len(n.Artifacts) != 1 || n.Artifacts[0].GetClipHash() != "old" {
			t.Fatalf("delayed old-connection report was not rejected against the registered fence: %+v", n.Artifacts)
		}
	}

	// The new connection's first report (fence 2) is accepted normally.
	sm.SetNodeArtifacts("node-1", []*ipcpb.StoredArtifact{{ClipHash: "fresh", FilePath: "/d/fresh.mp4"}}, ArtifactReportOrder{Fence: 2, Seq: 1})
	snap = sm.GetAllNodesSnapshot()
	for _, n := range snap.Nodes {
		if n.NodeID != "node-1" {
			continue
		}
		if len(n.Artifacts) != 1 || n.Artifacts[0].GetClipHash() != "fresh" {
			t.Fatalf("new connection's first report not applied: %+v", n.Artifacts)
		}
	}

	// RecordNodeArtifactFence never LOWERS the floor: a lower fence at re-registration is ignored.
	if err := sm.RecordNodeArtifactFence("node-1", 1); err != nil {
		t.Fatalf("RecordNodeArtifactFence(1): %v", err)
	}
	sm.SetNodeArtifacts("node-1", []*ipcpb.StoredArtifact{{ClipHash: "regressed", FilePath: "/d/regressed.mp4"}}, ArtifactReportOrder{Fence: 1, Seq: 1000})
	snap = sm.GetAllNodesSnapshot()
	for _, n := range snap.Nodes {
		if n.NodeID != "node-1" {
			continue
		}
		if len(n.Artifacts) != 1 || n.Artifacts[0].GetClipHash() != "fresh" {
			t.Fatalf("floor was lowered by a stale re-registration: %+v", n.Artifacts)
		}
	}
}

// A changelog artifact apply must be ordered by the report's (fence, seq), NOT by changelog append
// order — otherwise a stale old-owner snapshot appended after a handoff would win by entry ID.
func TestApplyRedisChange_ArtifactFenceSeqGate(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	// Prior applied state for the node at (fence 2, seq 5).
	sm.SetNodeArtifacts("node-1", []*ipcpb.StoredArtifact{{ClipHash: "cur", FilePath: "/d/cur.mp4"}}, ArtifactReportOrder{Fence: 2, Seq: 5})

	apply := func(hash string, fence, seq int64) {
		// The changelog payload is the report ENVELOPE (fence/seq at the top level, present even when
		// empty), not a bare artifact array.
		payload, err := json.Marshal(&NodeArtifactSnapshot{
			NodeID: "node-1", Fence: fence, Seq: seq,
			Artifacts: []*NodeArtifactState{{NodeID: "node-1", ClipHash: hash, FilePath: "/d/" + hash + ".mp4"}},
		})
		if err != nil {
			t.Fatal(err)
		}
		sm.applyRedisChange(StateChange{Entity: StateEntityArtifact, Operation: StateOpUpsert, NodeID: "node-1", Payload: payload})
	}
	hashOf := func() string {
		n := sm.GetNodeState("node-1")
		if n == nil || len(n.Artifacts) != 1 {
			t.Fatalf("expected exactly 1 artifact, got %+v", n)
		}
		return n.Artifacts[0].GetClipHash()
	}

	apply("stale-seq", 2, 3) // same fence, lower seq → rejected
	if h := hashOf(); h != "cur" {
		t.Fatalf("stale-seq apply was not rejected: %s", h)
	}
	apply("stale-fence", 1, 999) // lower fence, higher seq → rejected
	if h := hashOf(); h != "cur" {
		t.Fatalf("stale-fence apply was not rejected: %s", h)
	}
	apply("newer-seq", 2, 6) // same fence, higher seq → applied
	if h := hashOf(); h != "newer-seq" {
		t.Fatalf("newer-seq apply was rejected: %s", h)
	}
	apply("reconnect", 3, 1) // higher fence, lower seq → applied
	if h := hashOf(); h != "reconnect" {
		t.Fatalf("reconnect apply was rejected: %s", h)
	}
}

func TestSetNodeArtifacts_DeepCopiesSlice(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	original := []*ipcpb.StoredArtifact{
		{ClipHash: "h1", FilePath: "/data/h1.mp4"},
		{ClipHash: "h2", FilePath: "/data/h2.mp4"},
	}
	sm.SetNodeArtifacts("node-1", original, ArtifactReportOrder{Fence: 1, Seq: 1})

	// Appending to original slice should not affect stored state
	_ = append(original, &ipcpb.StoredArtifact{ClipHash: "h3"})

	snap := sm.GetAllNodesSnapshot()
	for _, n := range snap.Nodes {
		if n.NodeID == "node-1" {
			if len(n.Artifacts) != 2 {
				t.Fatalf("expected 2 artifacts (slice independent), got %d", len(n.Artifacts))
			}
		}
	}
}

func TestSetNodeArtifacts_EmptyClearsArtifacts(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	sm.SetNodeArtifacts("node-1", []*ipcpb.StoredArtifact{
		{ClipHash: "h1", FilePath: "/data/h1.mp4"},
	}, ArtifactReportOrder{Fence: 1, Seq: 1})
	sm.SetNodeArtifacts("node-1", nil, ArtifactReportOrder{Fence: 1, Seq: 2})

	snap := sm.GetAllNodesSnapshot()
	for _, n := range snap.Nodes {
		if n.NodeID == "node-1" {
			if len(n.Artifacts) != 0 {
				t.Fatalf("expected 0 artifacts after clear, got %d", len(n.Artifacts))
			}
		}
	}
}

func TestSetNodeArtifacts_MultipleSetsReplace(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	sm.SetNodeArtifacts("node-1", []*ipcpb.StoredArtifact{
		{ClipHash: "h1", FilePath: "/data/h1.mp4"},
		{ClipHash: "h2", FilePath: "/data/h2.mp4"},
	}, ArtifactReportOrder{Fence: 1, Seq: 1})
	sm.SetNodeArtifacts("node-1", []*ipcpb.StoredArtifact{
		{ClipHash: "h3", FilePath: "/data/h3.mp4"},
	}, ArtifactReportOrder{Fence: 1, Seq: 2})

	snap := sm.GetAllNodesSnapshot()
	for _, n := range snap.Nodes {
		if n.NodeID == "node-1" {
			if len(n.Artifacts) != 1 {
				t.Fatalf("expected 1 artifact after replace, got %d", len(n.Artifacts))
			}
			if n.Artifacts[0].ClipHash != "h3" {
				t.Fatalf("expected h3, got %s", n.Artifacts[0].ClipHash)
			}
		}
	}
}

func TestAddNodeArtifact_AddsNew(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	sm.SetNodeArtifacts("node-1", []*ipcpb.StoredArtifact{
		{ClipHash: "h1", FilePath: "/data/h1.mp4"},
	}, ArtifactReportOrder{Fence: 1, Seq: 1})
	sm.AddNodeArtifact("node-1", &ipcpb.StoredArtifact{
		ClipHash: "h2", FilePath: "/data/h2.mp4", SizeBytes: 200,
	})

	snap := sm.GetAllNodesSnapshot()
	for _, n := range snap.Nodes {
		if n.NodeID == "node-1" {
			if len(n.Artifacts) != 2 {
				t.Fatalf("expected 2 artifacts, got %d", len(n.Artifacts))
			}
		}
	}
}

func TestAddNodeArtifact_ReplacesExistingByHash(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	sm.SetNodeArtifacts("node-1", []*ipcpb.StoredArtifact{
		{ClipHash: "h1", FilePath: "/old/path.mp4", SizeBytes: 100},
	}, ArtifactReportOrder{Fence: 1, Seq: 1})
	sm.AddNodeArtifact("node-1", &ipcpb.StoredArtifact{
		ClipHash: "h1", FilePath: "/new/path.mkv", SizeBytes: 300, Format: "mkv",
	})

	snap := sm.GetAllNodesSnapshot()
	for _, n := range snap.Nodes {
		if n.NodeID == "node-1" {
			if len(n.Artifacts) != 1 {
				t.Fatalf("expected 1 artifact (replaced), got %d", len(n.Artifacts))
			}
			if n.Artifacts[0].FilePath != "/new/path.mkv" {
				t.Fatalf("expected updated path, got %s", n.Artifacts[0].FilePath)
			}
			if n.Artifacts[0].Format != "mkv" {
				t.Fatalf("expected format mkv, got %s", n.Artifacts[0].Format)
			}
		}
	}
}

func TestAddNodeArtifact_NilIsNoop(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	sm.AddNodeArtifact("node-1", nil) // should not panic
}

func TestAddNodeArtifact_CreatesNodeIfMissing(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	sm.AddNodeArtifact("new-node", &ipcpb.StoredArtifact{
		ClipHash: "h1", FilePath: "/data/h1.mp4",
	})

	snap := sm.GetAllNodesSnapshot()
	found := false
	for _, n := range snap.Nodes {
		if n.NodeID == "new-node" {
			found = true
			if len(n.Artifacts) != 1 {
				t.Fatalf("expected 1 artifact, got %d", len(n.Artifacts))
			}
		}
	}
	if !found {
		t.Fatal("node should have been created")
	}
}

func TestRemoveNodeArtifact_RemovesByHash(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	sm.SetNodeArtifacts("node-1", []*ipcpb.StoredArtifact{
		{ClipHash: "h1", FilePath: "/data/h1.mp4"},
		{ClipHash: "h2", FilePath: "/data/h2.mp4"},
	}, ArtifactReportOrder{Fence: 1, Seq: 1})
	sm.RemoveNodeArtifact("node-1", "h1")

	snap := sm.GetAllNodesSnapshot()
	for _, n := range snap.Nodes {
		if n.NodeID == "node-1" {
			if len(n.Artifacts) != 1 {
				t.Fatalf("expected 1 artifact after removal, got %d", len(n.Artifacts))
			}
			if n.Artifacts[0].ClipHash != "h2" {
				t.Fatalf("expected h2 to remain, got %s", n.Artifacts[0].ClipHash)
			}
		}
	}
}

func TestRemoveNodeArtifact_MissingHash_NoChange(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	sm.SetNodeArtifacts("node-1", []*ipcpb.StoredArtifact{
		{ClipHash: "h1", FilePath: "/data/h1.mp4"},
	}, ArtifactReportOrder{Fence: 1, Seq: 1})
	sm.RemoveNodeArtifact("node-1", "nonexistent")

	snap := sm.GetAllNodesSnapshot()
	for _, n := range snap.Nodes {
		if n.NodeID == "node-1" {
			if len(n.Artifacts) != 1 {
				t.Fatalf("expected 1 artifact unchanged, got %d", len(n.Artifacts))
			}
		}
	}
}

func TestRemoveNodeArtifact_MissingNode_NoPanic(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	sm.RemoveNodeArtifact("nonexistent-node", "h1") // should not panic
}

func TestFindNodeByArtifactInternalName_MatchesAfterPlus(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	sm.SetNodeArtifacts("node-1", []*ipcpb.StoredArtifact{
		{ClipHash: "h1", StreamName: "vod+my-internal-name", FilePath: "/data/h1.mp4"},
	}, ArtifactReportOrder{Fence: 1, Seq: 1})
	sm.SetNodeInfo("node-1", "http://host-1:8080", true, nil, nil, "", "", nil)

	host, artifact := sm.FindNodeByArtifactInternalName("my-internal-name")
	if host == "" || artifact == nil {
		t.Fatal("expected to find artifact by internal name")
	}
	if artifact.ClipHash != "h1" {
		t.Fatalf("expected h1, got %s", artifact.ClipHash)
	}
}

func TestFindNodeByArtifactInternalName_EmptyName(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	host, artifact := sm.FindNodeByArtifactInternalName("")
	if host != "" || artifact != nil {
		t.Fatal("expected nil for empty name")
	}
}

func TestFindNodeByArtifactInternalName_SkipsInactive(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	sm.SetNodeArtifacts("node-inactive", []*ipcpb.StoredArtifact{
		{ClipHash: "h1", StreamName: "vod+target", FilePath: "/data/h1.mp4"},
	}, ArtifactReportOrder{Fence: 1, Seq: 1})
	// Node exists but IsHealthy=false (default), so IsActive=false in snapshot

	host, artifact := sm.FindNodeByArtifactInternalName("target")
	if host != "" || artifact != nil {
		t.Fatal("should skip inactive nodes")
	}
}

// Score is an idleness scale (CPUScore = WeightCPU - load term), so the idler
// node has the HIGHER combined score and must win — same direction as the
// balancer's rate(). Both nodes get explicit metrics so neither rides the
// zero-value default (score 0 means saturated, not unknown-and-preferred).
func TestFindNodeByArtifactInternalName_PicksIdlestNode(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	sm.SetNodeArtifacts("node-busy", []*ipcpb.StoredArtifact{
		{ClipHash: "h1", StreamName: "vod+shared", FilePath: "/data/h1.mp4"},
	}, ArtifactReportOrder{Fence: 1, Seq: 1})
	sm.SetNodeArtifacts("node-idle", []*ipcpb.StoredArtifact{
		{ClipHash: "h1", StreamName: "vod+shared", FilePath: "/data/h1.mp4"},
	}, ArtifactReportOrder{Fence: 1, Seq: 1})

	sm.SetNodeInfo("node-busy", "http://host-busy:8080", true, nil, nil, "", "", nil)
	sm.SetNodeInfo("node-idle", "http://host-idle:8080", true, nil, nil, "", "", nil)

	setArtifactNodeCPU := func(nodeID string, cpu float64) {
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
	setArtifactNodeCPU("node-busy", 90) // CPUScore ≈ 50
	setArtifactNodeCPU("node-idle", 10) // CPUScore ≈ 450

	host, _ := sm.FindNodeByArtifactInternalName("shared")
	if host != "http://host-idle:8080" {
		t.Fatalf("expected host-idle (higher idleness score), got %s", host)
	}
}

// --- helper function tests ---

func TestInferArtifactType(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/recordings/dvr/abc123", "dvr"},
		{"/recordings/vod/upload.mp4", "vod"},
		{"/recordings/clips/clip.mp4", "clip"},
		{"/data/random.mp4", "clip"},
		{"", "clip"},
	}
	for _, tc := range tests {
		got := inferArtifactType(tc.path)
		if got != tc.want {
			t.Errorf("inferArtifactType(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}

func TestArtifactTypeToString(t *testing.T) {
	tests := []struct {
		input ipcpb.ArtifactEvent_ArtifactType
		want  string
	}{
		{ipcpb.ArtifactEvent_ARTIFACT_TYPE_CLIP, "clip"},
		{ipcpb.ArtifactEvent_ARTIFACT_TYPE_DVR, "dvr"},
		{ipcpb.ArtifactEvent_ARTIFACT_TYPE_VOD, "vod"},
		{ipcpb.ArtifactEvent_ARTIFACT_TYPE_UNSPECIFIED, ""},
	}
	for _, tc := range tests {
		got := artifactTypeToString(tc.input)
		if got != tc.want {
			t.Errorf("artifactTypeToString(%d) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestArtifactTypeFromString(t *testing.T) {
	tests := []struct {
		input string
		want  ipcpb.ArtifactEvent_ArtifactType
	}{
		{"clip", ipcpb.ArtifactEvent_ARTIFACT_TYPE_CLIP},
		{"dvr", ipcpb.ArtifactEvent_ARTIFACT_TYPE_DVR},
		{"vod", ipcpb.ArtifactEvent_ARTIFACT_TYPE_VOD},
		{"unknown", ipcpb.ArtifactEvent_ARTIFACT_TYPE_UNSPECIFIED},
		{"", ipcpb.ArtifactEvent_ARTIFACT_TYPE_UNSPECIFIED},
	}
	for _, tc := range tests {
		got := artifactTypeFromString(tc.input)
		if got != tc.want {
			t.Errorf("artifactTypeFromString(%q) = %d, want %d", tc.input, got, tc.want)
		}
	}
}
