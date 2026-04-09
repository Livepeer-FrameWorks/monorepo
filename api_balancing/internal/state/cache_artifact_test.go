package state

import (
	"encoding/json"
	"testing"

	pb "frameworks/pkg/proto"
)

func TestApplyRedisChange_Artifact_Upsert(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	// Pre-create the node so applyRedisChange has a node to attach artifacts to
	sm.TouchNode("node-1", true)

	arts := []*NodeArtifactState{
		{NodeID: "node-1", ClipHash: "h1", FilePath: "/data/h1.mp4", SizeBytes: 100, StreamName: "vod+stream1", ArtifactType: "clip", Format: "mp4"},
	}
	payload, _ := json.Marshal(arts)

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
			if a.ArtifactType != pb.ArtifactEvent_ARTIFACT_TYPE_CLIP {
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

func TestApplyRedisChange_Artifact_Delete(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	sm.SetNodeArtifacts("node-1", []*pb.StoredArtifact{
		{ClipHash: "h1", FilePath: "/data/h1.mp4"},
	})

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

	sm.SetNodeArtifacts("node-1", []*pb.StoredArtifact{
		{ClipHash: "h1", FilePath: "/data/h1.mp4"},
	})

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
	payload, _ := json.Marshal(arts)

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
			if dvr.ArtifactType != pb.ArtifactEvent_ARTIFACT_TYPE_DVR {
				t.Fatalf("expected DVR type, got %d", dvr.ArtifactType)
			}
			vod := n.Artifacts[1]
			if vod.ArtifactType != pb.ArtifactEvent_ARTIFACT_TYPE_VOD {
				t.Fatalf("expected VOD type, got %d", vod.ArtifactType)
			}
			if vod.Format != "mkv" {
				t.Fatalf("expected format mkv, got %s", vod.Format)
			}
		}
	}
}
