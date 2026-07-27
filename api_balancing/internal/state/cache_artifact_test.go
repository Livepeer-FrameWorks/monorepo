package state

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// cloneNodeStateLocked must produce a fully independent NodeState: mutating any reference field on the
// clone (geo pointers, Roles/Tags/ConfigStreams slices, ProcessingClasses map + its Ready slice, Artifacts)
// must not touch the original.
func TestCloneNodeStateLocked_Independent(t *testing.T) {
	lat, lon := 1.0, 2.0
	orig := &NodeState{
		NodeID:            "n",
		Latitude:          &lat,
		Longitude:         &lon,
		Roles:             []string{"edge"},
		Tags:              []string{"t1"},
		ConfigStreams:     []string{"s1"},
		ProcessingClasses: map[string]ClassCapacity{"video": {Total: 2, Used: 1, Ready: []string{"h264"}}},
		Artifacts:         []*ipcpb.StoredArtifact{{ClipHash: "h1", SizeBytes: 1}},
		Outputs:           map[string]any{"nested": map[string]any{"k": "v"}, "list": []any{"a"}},
	}
	c := cloneNodeStateLocked(orig)

	*c.Latitude = 99
	c.Roles[0] = "storage"
	c.Tags[0] = "t2"
	c.ConfigStreams[0] = "s2"
	pcv := c.ProcessingClasses["video"]
	pcv.Ready[0] = "hevc"
	c.ProcessingClasses["video2"] = ClassCapacity{}
	c.Artifacts[0].SizeBytes = 999
	c.Outputs["nested"].(map[string]any)["k"] = "mutated"
	c.Outputs["list"].([]any)[0] = "mutated"

	if *orig.Latitude != 1.0 {
		t.Fatal("Latitude pointer shared")
	}
	if orig.Roles[0] != "edge" || orig.Tags[0] != "t1" || orig.ConfigStreams[0] != "s1" {
		t.Fatal("slice backing array shared")
	}
	if orig.ProcessingClasses["video"].Ready[0] != "h264" {
		t.Fatal("ProcessingClasses.Ready slice shared")
	}
	if _, ok := orig.ProcessingClasses["video2"]; ok {
		t.Fatal("ProcessingClasses map shared")
	}
	if orig.Artifacts[0].GetSizeBytes() != 1 {
		t.Fatal("artifact pointee shared")
	}
	if orig.Outputs["nested"].(map[string]any)["k"] != "v" {
		t.Fatal("Outputs nested map shared")
	}
	if orig.Outputs["list"].([]any)[0] != "a" {
		t.Fatal("Outputs nested slice shared")
	}
}

// GetNodeState must return a fully independent snapshot: concurrent AddNodeArtifact point deltas (which
// replace elements in the live backing array) must not race a caller iterating the snapshot after the
// lock is released, and mutating a snapshot artifact must not affect the live node's copy. Run under -race.
func TestGetNodeState_SnapshotIsIndependent(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	sm.SetNodeInfo("node-1", "https://n1", true, nil, nil, "ams", "", nil)
	for i := 0; i < 32; i++ {
		sm.AddNodeArtifact("node-1", &ipcpb.StoredArtifact{ClipHash: fmt.Sprintf("h%d", i), SizeBytes: uint64(i)})
	}

	// Pointee independence: mutating a snapshot artifact must not change the live copy.
	snap := sm.GetNodeState("node-1")
	if snap == nil || len(snap.Artifacts) == 0 {
		t.Fatal("expected a populated snapshot")
	}
	snap.Artifacts[0].SizeBytes = 999999
	if live := sm.GetNodeState("node-1"); live.Artifacts[0].GetSizeBytes() == 999999 {
		t.Fatal("mutating the snapshot must not affect the live node state (pointees must be cloned)")
	}

	// Concurrency: iterate a snapshot while point deltas replace elements in the live backing array.
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		s := sm.GetNodeState("node-1")
		for iter := 0; iter < 200; iter++ {
			var total uint64
			for _, a := range s.Artifacts {
				total += a.GetSizeBytes()
			}
			_ = total
		}
	}()
	go func() {
		defer wg.Done()
		for iter := 0; iter < 200; iter++ {
			sm.AddNodeArtifact("node-1", &ipcpb.StoredArtifact{ClipHash: fmt.Sprintf("h%d", iter%32), SizeBytes: uint64(iter)})
		}
	}()
	wg.Wait()
}

func TestApplyRedisChange_Artifact_Upsert(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	// Pre-create the node so applyRedisChange has a node to attach artifacts to
	sm.TouchNode("node-1", true)

	arts := []*NodeArtifactState{
		{NodeID: "node-1", ClipHash: "h1", FilePath: "/data/h1.mp4", SizeBytes: 100, StreamName: "vod+stream1", ArtifactType: "clip", Format: "mp4"},
	}
	payload, _ := json.Marshal(&NodeArtifactSnapshot{NodeID: "node-1", Fence: 1, Seq: 1, Artifacts: arts})

	sm.applyRedisChange(StateChange{
		Entity:    StateEntityArtifact,
		Operation: StateOpUpsert,
		NodeID:    "node-1",
		Payload:   payload,
	})

	snap := sm.GetAllNodesSnapshot()
	found := false
	for _, n := range snap.Nodes {
		if n.NodeID == "node-1" {
			if len(n.Artifacts) != 1 {
				t.Fatalf("expected 1 artifact, got %d", len(n.Artifacts))
			}
			a := n.Artifacts[0]
			if a.ClipHash != "h1" {
				t.Fatalf("expected h1, got %s", a.ClipHash)
			}
			if a.StreamName != "vod+stream1" {
				t.Fatalf("expected StreamName, got %s", a.StreamName)
			}
			if a.ArtifactType != ipcpb.ArtifactEvent_ARTIFACT_TYPE_CLIP {
				t.Fatalf("expected CLIP type, got %d", a.ArtifactType)
			}
			if a.Format != "mp4" {
				t.Fatalf("expected format mp4, got %s", a.Format)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("node not found in snapshot")
	}
}

// A peer (non-owner) Foghorn applying a VERSIONED envelope with Ready=true must lift the
// artifact-readiness cordon locally, so the replicated node is routable for artifacts on the peer.
// Without replicated readiness the peer holds the inventory but permanently cordons the node.
func TestApplyRedisChange_Artifact_ReplicatesReadiness(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	// Active peer node with NO local versioned report yet → cordoned by default.
	sm.SetNodeInfo("node-1", "https://n1", true, nil, nil, "ams", "", nil)
	sm.TouchNode("node-1", true)
	sm.SetProbeVerified("node-1", true)
	if nodes := sm.FindNodesByArtifactHash("h1"); len(nodes) != 0 {
		t.Fatalf("expected node cordoned before a versioned envelope, got %d", len(nodes))
	}

	arts := []*NodeArtifactState{{NodeID: "node-1", ClipHash: "h1", FilePath: "/data/h1.mp4", ArtifactType: "clip"}}
	payload, _ := json.Marshal(&NodeArtifactSnapshot{NodeID: "node-1", Fence: 1, Seq: 1, Ready: true, Artifacts: arts})
	sm.applyRedisChange(StateChange{Entity: StateEntityArtifact, Operation: StateOpUpsert, NodeID: "node-1", Payload: payload})

	if nodes := sm.FindNodesByArtifactHash("h1"); len(nodes) != 1 || nodes[0].NodeID != "node-1" {
		t.Fatalf("expected the replicated node routable after a Ready envelope, got %+v", nodes)
	}
}

// The fenced artifact envelope is the SOLE owner of inventory + readiness. A generic (unfenced) node
// heartbeat snapshot carrying an empty/stale Artifacts list must NOT overwrite the fenced values.
func TestApplyRedisChange_Node_DoesNotClobberFencedInventory(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	sm.SetNodeInfo("node-1", "https://n1", true, nil, nil, "ams", "", nil)
	sm.TouchNode("node-1", true)
	sm.SetProbeVerified("node-1", true)

	// Fenced envelope establishes the authoritative inventory + readiness.
	arts := []*NodeArtifactState{{NodeID: "node-1", ClipHash: "h1", FilePath: "/data/h1.mp4", ArtifactType: "clip"}}
	envPayload, _ := json.Marshal(&NodeArtifactSnapshot{NodeID: "node-1", Fence: 1, Seq: 1, Ready: true, Artifacts: arts})
	sm.applyRedisChange(StateChange{Entity: StateEntityArtifact, Operation: StateOpUpsert, NodeID: "node-1", Payload: envPayload})
	if nodes := sm.FindNodesByArtifactHash("h1"); len(nodes) != 1 {
		t.Fatalf("precondition: expected routable after fenced envelope, got %d", len(nodes))
	}

	// A stale node heartbeat snapshot (active node, but NO artifacts + readiness=false) must be ignored
	// for the inventory/readiness fields — they are owned solely by the fenced envelope.
	nodePayload, _ := json.Marshal(&NodeState{NodeID: "node-1", BaseURL: "https://n1", IsHealthy: true, ProbeVerified: true, Artifacts: nil, ArtifactInventoryReady: false})
	sm.applyRedisChange(StateChange{Entity: StateEntityNode, Operation: StateOpUpsert, NodeID: "node-1", Payload: nodePayload})

	if nodes := sm.FindNodesByArtifactHash("h1"); len(nodes) != 1 || nodes[0].NodeID != "node-1" {
		t.Fatalf("unfenced node heartbeat must not clobber fenced inventory/readiness, got %+v", nodes)
	}
}

// A fenced TAKEOVER MARKER (seq=0, Ready=false) published on a reconnect must RE-ARM the readiness
// cordon on a peer that already applied the previous owner's Ready=true report — so the peer stops
// routing to the (now superseded) inventory until the new owner's first report (seq>=1) arrives.
func TestApplyRedisChange_Artifact_TakeoverMarkerReArmsCordon(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)
	sm.SetNodeInfo("node-1", "https://n1", true, nil, nil, "ams", "", nil)
	sm.TouchNode("node-1", true)
	sm.SetProbeVerified("node-1", true)

	apply := func(fence, seq int64, ready bool, arts []*NodeArtifactState) {
		payload, _ := json.Marshal(&NodeArtifactSnapshot{NodeID: "node-1", Fence: fence, Seq: seq, Ready: ready, Artifacts: arts})
		sm.applyRedisChange(StateChange{Entity: StateEntityArtifact, Operation: StateOpUpsert, NodeID: "node-1", Payload: payload})
	}
	arts := []*NodeArtifactState{{NodeID: "node-1", ClipHash: "h1", FilePath: "/d/h1.mp4", ArtifactType: "clip"}}

	// (1) Previous owner's versioned report → routable.
	apply(1, 1, true, arts)
	if n := sm.FindNodesByArtifactHash("h1"); len(n) != 1 {
		t.Fatalf("precondition: expected routable, got %d", len(n))
	}
	// (2) New owner's takeover marker (higher fence, seq=0, Ready=false) → cordoned.
	apply(2, 0, false, nil)
	if n := sm.FindNodesByArtifactHash("h1"); len(n) != 0 {
		t.Fatalf("takeover marker must re-arm the cordon, got %d routable", len(n))
	}
	// (3) A stale old-owner report (fence 1) after the marker must be ignored (marker's fence 2 wins).
	apply(1, 2, true, arts)
	if n := sm.FindNodesByArtifactHash("h1"); len(n) != 0 {
		t.Fatalf("stale lower-fence report must not lift the cordon, got %d routable", len(n))
	}
	// (4) New owner's first real report (fence 2, seq>=1, Ready=true) → routable again.
	apply(2, 1, true, arts)
	if n := sm.FindNodesByArtifactHash("h1"); len(n) != 1 {
		t.Fatalf("new owner's report must lift the cordon, got %d routable", len(n))
	}
}

func TestApplyRedisChange_Artifact_Delete(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	sm.SetNodeArtifacts("node-1", []*ipcpb.StoredArtifact{
		{ClipHash: "h1", FilePath: "/data/h1.mp4"},
	}, ArtifactReportOrder{Fence: 1, Seq: 1})

	sm.applyRedisChange(StateChange{
		Entity:    StateEntityArtifact,
		Operation: StateOpDelete,
		NodeID:    "node-1",
	})

	snap := sm.GetAllNodesSnapshot()
	for _, n := range snap.Nodes {
		if n.NodeID == "node-1" {
			if len(n.Artifacts) != 0 {
				t.Fatalf("expected 0 artifacts after delete, got %d", len(n.Artifacts))
			}
		}
	}
}

func TestApplyRedisChange_Artifact_BadJSON(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	sm.SetNodeArtifacts("node-1", []*ipcpb.StoredArtifact{
		{ClipHash: "h1", FilePath: "/data/h1.mp4"},
	}, ArtifactReportOrder{Fence: 1, Seq: 1})

	sm.applyRedisChange(StateChange{
		Entity:    StateEntityArtifact,
		Operation: StateOpUpsert,
		NodeID:    "node-1",
		Payload:   []byte("not valid json"),
	})

	// Should not have changed existing artifacts
	snap := sm.GetAllNodesSnapshot()
	for _, n := range snap.Nodes {
		if n.NodeID == "node-1" {
			if len(n.Artifacts) != 1 {
				t.Fatalf("expected 1 artifact unchanged, got %d", len(n.Artifacts))
			}
		}
	}
}

func TestApplyRedisChange_Artifact_MissingNode(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	arts := []*NodeArtifactState{
		{NodeID: "ghost", ClipHash: "h1", FilePath: "/data/h1.mp4"},
	}
	payload, _ := json.Marshal(arts)

	sm.applyRedisChange(StateChange{
		Entity:    StateEntityArtifact,
		Operation: StateOpUpsert,
		NodeID:    "ghost",
		Payload:   payload,
	})

	// No node exists for "ghost", so artifacts should be silently ignored
	snap := sm.GetAllNodesSnapshot()
	for _, n := range snap.Nodes {
		if n.NodeID == "ghost" {
			t.Fatal("should not have created a node from artifact upsert")
		}
	}
}

func TestApplyRedisChange_Artifact_PreservesAllFields(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	sm.TouchNode("node-1", true)

	arts := []*NodeArtifactState{
		{NodeID: "node-1", ClipHash: "dvr-h", FilePath: "/data/dvr/hash", SizeBytes: 5000, StreamName: "dvr+stream-dvr", ArtifactType: "dvr", Format: ""},
		{NodeID: "node-1", ClipHash: "vod-h", FilePath: "/data/vod/upload.mkv", SizeBytes: 8000, StreamName: "vod+stream-vod", ArtifactType: "vod", Format: "mkv"},
	}
	payload, _ := json.Marshal(&NodeArtifactSnapshot{NodeID: "node-1", Fence: 1, Seq: 1, Artifacts: arts})

	sm.applyRedisChange(StateChange{
		Entity:    StateEntityArtifact,
		Operation: StateOpUpsert,
		NodeID:    "node-1",
		Payload:   payload,
	})

	snap := sm.GetAllNodesSnapshot()
	for _, n := range snap.Nodes {
		if n.NodeID == "node-1" {
			if len(n.Artifacts) != 2 {
				t.Fatalf("expected 2 artifacts, got %d", len(n.Artifacts))
			}
			dvr := n.Artifacts[0]
			if dvr.ArtifactType != ipcpb.ArtifactEvent_ARTIFACT_TYPE_DVR {
				t.Fatalf("expected DVR type, got %d", dvr.ArtifactType)
			}
			vod := n.Artifacts[1]
			if vod.ArtifactType != ipcpb.ArtifactEvent_ARTIFACT_TYPE_VOD {
				t.Fatalf("expected VOD type, got %d", vod.ArtifactType)
			}
			if vod.Format != "mkv" {
				t.Fatalf("expected format mkv, got %s", vod.Format)
			}
		}
	}
}
