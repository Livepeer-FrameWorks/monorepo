package control

import (
	"context"
	"testing"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

// A recording's replay index lives on its chapters, so the parent's storage
// lifecycle event never carries a finalized flag; a VOD keeps its own.
func TestArtifactStorageLifecycleEventFinalizedByType(t *testing.T) {
	base := artifactStorageStateLifecycle{
		artifactHash: "hash-1", tenantID: "tenant-1", syncStatus: "synced",
		synced: true, finalized: true,
	}

	dvrState := base
	dvrState.artifactType = "dvr"
	dvr, ok := buildArtifactStorageLifecycleEvent(context.Background(), dvrState).(*ipcpb.DVRLifecycleData)
	if !ok {
		t.Fatal("dvr state did not build a DVR lifecycle event")
	}
	if dvr.IsFinalized != nil {
		t.Fatalf("dvr lifecycle event carries isFinalized=%v, want unset", dvr.GetIsFinalized())
	}
	if !dvr.GetIsSynced() {
		t.Fatal("dvr lifecycle event lost isSynced")
	}

	vodState := base
	vodState.artifactType = "vod"
	vod, ok := buildArtifactStorageLifecycleEvent(context.Background(), vodState).(*ipcpb.VodLifecycleData)
	if !ok {
		t.Fatal("vod state did not build a VOD lifecycle event")
	}
	if vod.IsFinalized == nil || !vod.GetIsFinalized() {
		t.Fatalf("vod lifecycle event isFinalized=%v, want true", vod.IsFinalized)
	}
}
