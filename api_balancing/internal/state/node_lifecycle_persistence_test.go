package state

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

type lifecycleSnapshotRepository struct {
	rehydrateNodeRepo
	lifecycleJSON []byte
	component     string
}

func (r *lifecycleSnapshotRepository) UpsertNodeLifecycles(_ context.Context, updates []*ipcpb.NodeLifecycleUpdate) error {
	var err error
	r.lifecycleJSON, err = json.Marshal(updates[0])
	return err
}

func (r *lifecycleSnapshotRepository) UpsertNodeComponents(_ context.Context, updates []*ipcpb.NodeLifecycleUpdate) error {
	r.component = updates[0].GetComponentVersions()[0].GetVersion()
	return nil
}

func TestQueuedNodeLifecycleOwnsPersistenceSnapshot(t *testing.T) {
	repo := &lifecycleSnapshotRepository{}
	sm := NewStreamStateManager()
	t.Cleanup(sm.Shutdown)
	sm.nodeRepo = repo
	sm.nodeLifecycleBatchFlushInterval = time.Hour
	uuid := "before-uuid"
	update := &ipcpb.NodeLifecycleUpdate{
		NodeId: "node-1", NodeUuid: &uuid, BwLimit: 100,
		Artifacts:         []*ipcpb.StoredArtifact{{ClipHash: "before-artifact"}},
		ComponentVersions: []*ipcpb.EdgeComponentVersion{{Component: "helmsman", Version: "before-version"}},
		Capabilities:      &ipcpb.NodeCapabilities{Roles: []string{"ingest"}},
		Streams:           map[string]*ipcpb.StreamData{"live+stream": {Total: 2}},
	}
	sm.queueNodeLifecycleWrite(update)
	timer := sm.nodeLifecycleBatchTimer
	t.Cleanup(func() { timer.Stop() })

	// Analytics enrichment and artifact stripping may proceed after queuing.
	uuid = "after-uuid"
	update.BwLimit = 200
	update.Artifacts[0].ClipHash = "after-artifact"
	update.Artifacts = nil
	update.ComponentVersions[0].Version = "after-version"
	update.Capabilities.Roles[0] = "storage"
	update.Streams["live+stream"].Total = 4
	sm.flushNodeLifecycleBatch(nil)

	var saved ipcpb.NodeLifecycleUpdate
	if err := json.Unmarshal(repo.lifecycleJSON, &saved); err != nil {
		t.Fatal(err)
	}
	if len(saved.GetArtifacts()) != 1 || saved.GetArtifacts()[0].GetClipHash() != "before-artifact" {
		t.Fatalf("queued inventory changed after analytics stripped it: %s", repo.lifecycleJSON)
	}
	if saved.GetNodeUuid() != "before-uuid" || saved.GetBwLimit() != 100 ||
		saved.GetCapabilities().GetRoles()[0] != "ingest" || saved.GetStreams()["live+stream"].GetTotal() != 2 {
		t.Fatalf("queued node report was changed by its caller: %s", repo.lifecycleJSON)
	}
	if repo.component != "before-version" {
		t.Fatalf("queued component version = %q, want before-version", repo.component)
	}
}

type lifecycleEncodingRepository struct {
	rehydrateNodeRepo
	encodingStarted chan struct{}
}

func (r *lifecycleEncodingRepository) UpsertNodeLifecycles(_ context.Context, updates []*ipcpb.NodeLifecycleUpdate) error {
	close(r.encodingStarted)
	for range 1000 {
		if _, err := json.Marshal(updates[0]); err != nil {
			return err
		}
	}
	return nil
}

func TestQueuedNodeLifecycleEncodingDoesNotRaceAnalytics(t *testing.T) {
	repo := &lifecycleEncodingRepository{encodingStarted: make(chan struct{})}
	sm := NewStreamStateManager()
	t.Cleanup(sm.Shutdown)
	sm.nodeRepo = repo
	sm.nodeLifecycleBatchFlushInterval = time.Hour
	artifacts := []*ipcpb.StoredArtifact{{ClipHash: "artifact"}}
	update := &ipcpb.NodeLifecycleUpdate{NodeId: "node-1", Artifacts: artifacts}
	sm.queueNodeLifecycleWrite(update)
	timer := sm.nodeLifecycleBatchTimer
	t.Cleanup(func() { timer.Stop() })
	flushed := make(chan struct{})
	go func() {
		defer close(flushed)
		sm.flushNodeLifecycleBatch(nil)
	}()
	<-repo.encodingStarted
	for range 1000 {
		update.Artifacts = nil
		update.Artifacts = artifacts
	}
	<-flushed
}
