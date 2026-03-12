package state

import (
	"testing"

	pb "frameworks/pkg/proto"
)

func TestSetNodeArtifacts_CreatesNode(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	sm.SetNodeArtifacts("new-node", []*pb.StoredArtifact{
		{ClipHash: "h1", FilePath: "/data/h1.mp4", SizeBytes: 100},
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
		t.Fatal("node not found after SetNodeArtifacts")
	}
}

func TestSetNodeArtifacts_DeepCopiesSlice(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	original := []*pb.StoredArtifact{
		{ClipHash: "h1", FilePath: "/data/h1.mp4"},
		{ClipHash: "h2", FilePath: "/data/h2.mp4"},
	}
	sm.SetNodeArtifacts("node-1", original)

	// Appending to original slice should not affect stored state
	_ = append(original, &pb.StoredArtifact{ClipHash: "h3"})

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

	sm.SetNodeArtifacts("node-1", []*pb.StoredArtifact{
		{ClipHash: "h1", FilePath: "/data/h1.mp4"},
	})
	sm.SetNodeArtifacts("node-1", nil)

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

	sm.SetNodeArtifacts("node-1", []*pb.StoredArtifact{
		{ClipHash: "h1", FilePath: "/data/h1.mp4"},
		{ClipHash: "h2", FilePath: "/data/h2.mp4"},
	})
	sm.SetNodeArtifacts("node-1", []*pb.StoredArtifact{
		{ClipHash: "h3", FilePath: "/data/h3.mp4"},
	})

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

	sm.SetNodeArtifacts("node-1", []*pb.StoredArtifact{
		{ClipHash: "h1", FilePath: "/data/h1.mp4"},
	})
	sm.AddNodeArtifact("node-1", &pb.StoredArtifact{
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

	sm.SetNodeArtifacts("node-1", []*pb.StoredArtifact{
		{ClipHash: "h1", FilePath: "/old/path.mp4", SizeBytes: 100},
	})
	sm.AddNodeArtifact("node-1", &pb.StoredArtifact{
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

	sm.AddNodeArtifact("new-node", &pb.StoredArtifact{
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

	sm.SetNodeArtifacts("node-1", []*pb.StoredArtifact{
		{ClipHash: "h1", FilePath: "/data/h1.mp4"},
		{ClipHash: "h2", FilePath: "/data/h2.mp4"},
	})
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

	sm.SetNodeArtifacts("node-1", []*pb.StoredArtifact{
		{ClipHash: "h1", FilePath: "/data/h1.mp4"},
	})
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

	sm.SetNodeArtifacts("node-1", []*pb.StoredArtifact{
		{ClipHash: "h1", StreamName: "vod+my-internal-name", FilePath: "/data/h1.mp4"},
	})
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

	sm.SetNodeArtifacts("node-inactive", []*pb.StoredArtifact{
		{ClipHash: "h1", StreamName: "vod+target", FilePath: "/data/h1.mp4"},
	})
	// Node exists but IsHealthy=false (default), so IsActive=false in snapshot

	host, artifact := sm.FindNodeByArtifactInternalName("target")
	if host != "" || artifact != nil {
		t.Fatal("should skip inactive nodes")
	}
}

func TestFindNodeByArtifactInternalName_PicksLowestScore(t *testing.T) {
	sm := ResetDefaultManagerForTests()
	t.Cleanup(sm.Shutdown)

	sm.SetNodeArtifacts("node-high", []*pb.StoredArtifact{
		{ClipHash: "h1", StreamName: "vod+shared", FilePath: "/data/h1.mp4"},
	})
	sm.SetNodeArtifacts("node-low", []*pb.StoredArtifact{
		{ClipHash: "h1", StreamName: "vod+shared", FilePath: "/data/h1.mp4"},
	})

	sm.SetNodeInfo("node-high", "http://host-high:8080", true, nil, nil, "", "", nil)
	sm.SetNodeInfo("node-low", "http://host-low:8080", true, nil, nil, "", "", nil)

	// Give node-high a higher CPU score so node-low wins
	sm.UpdateNodeMetrics("node-high", struct {
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
		MaxTranscodes        int
		CurrentTranscodes    int
	}{CPU: 90})

	host, _ := sm.FindNodeByArtifactInternalName("shared")
	if host != "http://host-low:8080" {
		t.Fatalf("expected host-low (lower score), got %s", host)
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
		input pb.ArtifactEvent_ArtifactType
		want  string
	}{
		{pb.ArtifactEvent_ARTIFACT_TYPE_CLIP, "clip"},
		{pb.ArtifactEvent_ARTIFACT_TYPE_DVR, "dvr"},
		{pb.ArtifactEvent_ARTIFACT_TYPE_VOD, "vod"},
		{pb.ArtifactEvent_ARTIFACT_TYPE_UNSPECIFIED, ""},
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
		want  pb.ArtifactEvent_ArtifactType
	}{
		{"clip", pb.ArtifactEvent_ARTIFACT_TYPE_CLIP},
		{"dvr", pb.ArtifactEvent_ARTIFACT_TYPE_DVR},
		{"vod", pb.ArtifactEvent_ARTIFACT_TYPE_VOD},
		{"unknown", pb.ArtifactEvent_ARTIFACT_TYPE_UNSPECIFIED},
		{"", pb.ArtifactEvent_ARTIFACT_TYPE_UNSPECIFIED},
	}
	for _, tc := range tests {
		got := artifactTypeFromString(tc.input)
		if got != tc.want {
			t.Errorf("artifactTypeFromString(%q) = %d, want %d", tc.input, got, tc.want)
		}
	}
}
