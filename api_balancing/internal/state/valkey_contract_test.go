//go:build schema_verify

package state

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockervalkey"
)

func TestRedisStateContracts_RealValkey(t *testing.T) {
	engine := dockervalkey.Start(t)
	ctx := context.Background()
	store := NewRedisStateStore(engine.Client, "state-contract")

	acquired, err := store.TryAcquireLease(ctx, "worker", "owner-a", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("acquire lease: acquired=%v err=%v", acquired, err)
	}
	if acquired, err = store.TryAcquireLease(ctx, "worker", "owner-b", time.Minute); err != nil || acquired {
		t.Fatalf("competing lease: acquired=%v err=%v", acquired, err)
	}
	if renewed, renewErr := store.RenewLease(ctx, "worker", "owner-b", time.Minute); renewErr != nil || renewed {
		t.Fatalf("non-owner renewed lease: renewed=%v err=%v", renewed, renewErr)
	}
	if err = store.ReleaseLease(ctx, "worker", "owner-b"); err != nil {
		t.Fatalf("non-owner release: %v", err)
	}
	if acquired, err = store.TryAcquireLease(ctx, "worker", "owner-b", time.Minute); err != nil || acquired {
		t.Fatalf("stale release removed lease: acquired=%v err=%v", acquired, err)
	}
	if err = store.ReleaseLease(ctx, "worker", "owner-a"); err != nil {
		t.Fatalf("owner release: %v", err)
	}

	if acquired, err = store.AcquireConnOwnerFenced(ctx, "node-1", "instance-a", "a:1", 10); err != nil || !acquired {
		t.Fatalf("acquire connection owner: acquired=%v err=%v", acquired, err)
	}
	if acquired, err = store.AcquireConnOwnerFenced(ctx, "node-1", "instance-b", "b:1", 9); err != nil || acquired {
		t.Fatalf("lower fence acquired: acquired=%v err=%v", acquired, err)
	}
	if acquired, err = store.AcquireConnOwnerFenced(ctx, "node-1", "instance-b", "b:1", 11); err != nil || !acquired {
		t.Fatalf("higher fence takeover: acquired=%v err=%v", acquired, err)
	}
	if err = store.RefreshConnOwnerFenced(ctx, "node-1", "instance-a", "a:1", 10); !errors.Is(err, ErrConnOwnerLost) {
		t.Fatalf("stale owner refresh error=%v, want ErrConnOwnerLost", err)
	}
	if deleted, deleteErr := store.DeleteConnOwnerIfMatch(ctx, "node-1", "instance-a", "a:1", 10); deleteErr != nil || deleted {
		t.Fatalf("stale owner delete: deleted=%v err=%v", deleted, deleteErr)
	}
	if deleted, deleteErr := store.DeleteConnOwnerIfMatch(ctx, "node-1", "instance-b", "b:1", 11); deleteErr != nil || !deleted {
		t.Fatalf("current owner delete: deleted=%v err=%v", deleted, deleteErr)
	}

	if guarded, guardErr := store.AcquireNodeReapGuard(ctx, "node-1", "reaper", time.Minute); guardErr != nil || !guarded {
		t.Fatalf("acquire reap guard: guarded=%v err=%v", guarded, guardErr)
	}
	if acquired, err = store.AcquireConnOwnerFenced(ctx, "node-1", "instance-c", "c:1", 12); !errors.Is(err, ErrNodeReaping) || acquired {
		t.Fatalf("owner acquired through reap guard: acquired=%v err=%v", acquired, err)
	}
	if err = store.ReleaseNodeReapGuard(ctx, "node-1", "wrong"); err != nil {
		t.Fatalf("non-owner reap release: %v", err)
	}
	if acquired, err = store.AcquireConnOwnerFenced(ctx, "node-1", "instance-c", "c:1", 12); !errors.Is(err, ErrNodeReaping) || acquired {
		t.Fatalf("wrong token removed reap guard: acquired=%v err=%v", acquired, err)
	}
	if err = store.ReleaseNodeReapGuard(ctx, "node-1", "reaper"); err != nil {
		t.Fatalf("owner reap release: %v", err)
	}

	envelope := []byte(`{"node_id":"node-1","fence":20,"seq":1,"ready":true,"artifacts":[]}`)
	change, err := json.Marshal(StateChange{InstanceID: "instance-c", Entity: StateEntityArtifact, Operation: StateOpUpsert, NodeID: "node-1", Payload: envelope})
	if err != nil {
		t.Fatal(err)
	}
	if applied, applyErr := store.SetNodeArtifactsFenced(ctx, "node-1", envelope, change, 20, 1); applyErr != nil || !applied {
		t.Fatalf("apply artifact snapshot: applied=%v err=%v", applied, applyErr)
	}
	if applied, applyErr := store.SetNodeArtifactsFenced(ctx, "node-1", []byte(`{"node_id":"node-1"}`), change, 20, 1); applyErr != nil || applied {
		t.Fatalf("equal watermark applied: applied=%v err=%v", applied, applyErr)
	}
	if applied, applyErr := store.SetNodeArtifactsFenced(ctx, "node-1", []byte(`{"node_id":"node-1"}`), change, 19, 99); applyErr != nil || applied {
		t.Fatalf("lower fence applied: applied=%v err=%v", applied, applyErr)
	}

	tail, err := store.ChangelogTail(ctx)
	if err != nil || tail == "0-0" {
		t.Fatalf("artifact write did not append changelog: tail=%q err=%v", tail, err)
	}
	engine.Restart(t)
	store = NewRedisStateStore(engine.Client, "state-contract")
	artifacts, err := store.GetAllNodeArtifacts()
	if err != nil || artifacts["node-1"] == nil || artifacts["node-1"].Fence != 20 || artifacts["node-1"].Seq != 1 {
		t.Fatalf("AOF restart lost fenced snapshot: artifacts=%+v err=%v", artifacts, err)
	}
	if got, tailErr := store.ChangelogTail(ctx); tailErr != nil || got != tail {
		t.Fatalf("AOF restart lost changelog: got=%q want=%q err=%v", got, tail, tailErr)
	}
}

func TestTenantCapacityContracts_RealValkey(t *testing.T) {
	engine := dockervalkey.Start(t)
	a := NewTenantCapacityManager()
	b := NewTenantCapacityManager()
	a.EnableRedisSync(engine.Client, "capacity-contract")
	b.EnableRedisSync(engine.Client, "capacity-contract")

	a.RegisterStream("tenant-1", "stream-a")
	b.RegisterStream("tenant-1", "stream-b")
	a.RegisterStream("tenant-1", "stream-a")
	if got := a.CountStreams("tenant-1"); got != 2 {
		t.Fatalf("cluster-wide stream count=%d, want 2", got)
	}
	a.RegisterViewer("tenant-1", "viewer-a")
	b.RegisterViewer("tenant-1", "viewer-b")
	if got := b.CountViewers("tenant-1"); got != 2 {
		t.Fatalf("cluster-wide viewer count=%d, want 2", got)
	}
	a.ReconcileStreams("tenant-1", []string{"stream-a"})
	if got := b.CountStreams("tenant-1"); got != 1 || !b.HasStream("tenant-1", "stream-a") {
		t.Fatalf("reconciled stream state count=%d has-a=%v", got, b.HasStream("tenant-1", "stream-a"))
	}
}
