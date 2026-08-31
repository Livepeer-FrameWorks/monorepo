package state

import (
	"context"
	"encoding/json"
	"reflect"
	"sync"
	"testing"
	"time"

	ipcpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/ipc"
)

func TestApplyNodeLifecycleConcurrentWithRedisNodeChange(t *testing.T) {
	sm := NewStreamStateManager()
	t.Cleanup(sm.Shutdown)
	update := &ipcpb.NodeLifecycleUpdate{NodeId: "node-1", IsHealthy: true, DiskTotalBytes: 100, DiskUsedBytes: 25}
	payload, err := json.Marshal(&NodeState{NodeID: "node-1", IsHealthy: true})
	if err != nil {
		t.Fatal(err)
	}

	var group sync.WaitGroup
	group.Add(2)
	go func() {
		defer group.Done()
		for range 500 {
			if err := sm.ApplyNodeLifecycle(context.Background(), update); err != nil {
				t.Errorf("ApplyNodeLifecycle: %v", err)
				return
			}
		}
	}()
	go func() {
		defer group.Done()
		for range 500 {
			sm.applyRedisChange(StateChange{Entity: StateEntityNode, Operation: StateOpUpsert, NodeID: "node-1", Payload: payload})
		}
	}()
	group.Wait()
}

type blockingDeleteNodeRepo struct {
	rehydrateNodeRepo
	deleteStarted chan struct{}
	releaseDelete chan struct{}
	mu            sync.Mutex
	operations    []string
	startOnce     sync.Once
}

func (r *blockingDeleteNodeRepo) DeleteNodeOutputs(context.Context, string) error {
	r.startOnce.Do(func() { close(r.deleteStarted) })
	<-r.releaseDelete
	r.mu.Lock()
	r.operations = append(r.operations, "delete")
	r.mu.Unlock()
	return nil
}

func (r *blockingDeleteNodeRepo) UpsertNodeOutputs(context.Context, string, string, string) error {
	r.mu.Lock()
	r.operations = append(r.operations, "upsert")
	r.mu.Unlock()
	return nil
}

func TestNodeReconnectCannotBeOvertakenByInFlightEvictionDelete(t *testing.T) {
	sm := NewStreamStateManager()
	t.Cleanup(sm.Shutdown)
	repo := &blockingDeleteNodeRepo{
		deleteStarted: make(chan struct{}),
		releaseDelete: make(chan struct{}),
	}
	sm.nodeRepo = repo

	sm.SetNodeInfo("node-1", "https://old.example", true, nil, nil, "", `{"HLS":"old"}`, nil)
	sm.MarkNodeDisconnected("node-1")
	sm.mu.Lock()
	sm.nodes["node-1"].LastUpdate = time.Now().Add(-nodeRemovalThreshold - time.Second)
	sm.mu.Unlock()

	evictionDone := make(chan struct{})
	go func() {
		sm.checkStaleNodes()
		close(evictionDone)
	}()
	<-repo.deleteStarted

	reconnectDone := make(chan struct{})
	go func() {
		_ = sm.ApplyNodeLifecycle(context.Background(), &ipcpb.NodeLifecycleUpdate{
			NodeId: "node-1", BaseUrl: "https://new.example", IsHealthy: true, OutputsJson: `{"HLS":"new"}`,
		})
		close(reconnectDone)
	}()
	select {
	case <-reconnectDone:
		t.Fatal("reconnect persisted before the in-flight eviction delete completed")
	case <-time.After(20 * time.Millisecond):
	}
	close(repo.releaseDelete)
	<-evictionDone
	<-reconnectDone

	repo.mu.Lock()
	operations := append([]string(nil), repo.operations...)
	repo.mu.Unlock()
	if !reflect.DeepEqual(operations, []string{"delete", "upsert"}) {
		t.Fatalf("durable operation order = %v, want delete then reconnect upsert", operations)
	}
	if node := sm.GetNodeState("node-1"); node == nil || !node.IsHealthy || node.BaseURL != "https://new.example" {
		t.Fatalf("reconnected node was not retained: %+v", node)
	}
}
