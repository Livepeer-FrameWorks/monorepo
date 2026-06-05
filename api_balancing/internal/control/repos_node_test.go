package control

import (
	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
	"testing"
)

func TestDedupeNodeLifecycleUpdatesKeepsLastPerNode(t *testing.T) {
	updates := []*ipcpb.NodeLifecycleUpdate{
		nil,
		{NodeId: ""},
		{NodeId: "edge-1", EventType: "old"},
		{NodeId: "edge-2", EventType: "only"},
		{NodeId: "edge-1", EventType: "new"},
	}

	got := dedupeNodeLifecycleUpdates(updates)
	if len(got) != 2 {
		t.Fatalf("expected 2 deduped updates, got %d", len(got))
	}
	if got[0].GetNodeId() != "edge-1" || got[0].GetEventType() != "new" {
		t.Fatalf("expected edge-1 last update first, got %#v", got[0])
	}
	if got[1].GetNodeId() != "edge-2" || got[1].GetEventType() != "only" {
		t.Fatalf("expected edge-2 update second, got %#v", got[1])
	}
}

func TestDedupeNodeComponentUpdatesKeepsLastPerNodeComponent(t *testing.T) {
	updates := []*ipcpb.NodeLifecycleUpdate{
		nil,
		{NodeId: ""},
		{
			NodeId: "edge-1",
			ComponentVersions: []*ipcpb.EdgeComponentVersion{
				nil,
				{Component: "", Version: "ignored"},
				{Component: "helmsman", Version: "v1"},
				{Component: "privateer", Version: "v1"},
			},
		},
		{
			NodeId: "edge-1",
			ComponentVersions: []*ipcpb.EdgeComponentVersion{
				{Component: "helmsman", Version: "v2"},
			},
		},
		{
			NodeId: "edge-2",
			ComponentVersions: []*ipcpb.EdgeComponentVersion{
				{Component: "helmsman", Version: "v3"},
			},
		},
	}

	got := dedupeNodeComponentUpdates(updates)
	if len(got) != 3 {
		t.Fatalf("expected 3 deduped component updates, got %d", len(got))
	}
	want := []nodeComponentUpdate{
		{nodeID: "edge-1", component: "helmsman", version: "v2"},
		{nodeID: "edge-1", component: "privateer", version: "v1"},
		{nodeID: "edge-2", component: "helmsman", version: "v3"},
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("entry %d = %#v, want %#v", i, got[i], want[i])
		}
	}
}
