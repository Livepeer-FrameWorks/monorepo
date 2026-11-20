//go:build schema_verify

package state

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/capacity"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockervalkey"
	goredis "github.com/redis/go-redis/v9"
)

func TestRedisStateContracts_RealValkey(t *testing.T) {
	engine := dockervalkey.Start(t)
	ctx := context.Background()
	store := NewRedisStateStore(engine.Client, "state-contract")

	t.Run("bandwidth reservations", func(t *testing.T) {
		now := time.Now()
		link := capacity.Link{Budget: 20000000000000000}
		for _, tc := range []struct {
			id      string
			bytes   uint64
			allowed bool
		}{
			{"above", link.Usable() + 1, false}, {"exact", link.Usable(), true}, {"overflow", 1, false},
		} {
			result, err := store.mutateBandwidth(ctx, "reserve", tc.id, "capacity-node", "replica-a", bandwidthReservation{Bytes: tc.bytes, Direction: BandwidthEgress, Stream: "stream", State: "PENDING", ExpiresMS: now.Add(30 * time.Second).UnixMilli()}, link, 0, now)
			if err != nil || result.Allowed != tc.allowed {
				t.Fatalf("%s admission=%v err=%v", tc.id, result.Allowed, err)
			}
		}
		if result, err := store.mutateBandwidth(ctx, "activate", "exact", "capacity-node", "replica-b", bandwidthReservation{}, link, 0, now); err != nil || !result.Allowed {
			t.Fatalf("cross-replica activation=%+v %v", result, err)
		}
		if result, err := store.mutateBandwidth(ctx, "release_pending", "exact", "capacity-node", "replica-a", bandwidthReservation{}, link, 0, now); err != nil || result.Sums.Egress != link.Usable() {
			t.Fatalf("stale pending release=%+v %v", result, err)
		}
		if result, err := store.mutateBandwidth(ctx, "settle", "", "capacity-node", "replica-b", bandwidthReservation{}, link, now.Add(-time.Second).UnixMilli(), now); err != nil || result.Sums.Egress != link.Usable() {
			t.Fatalf("predating window=%+v %v", result, err)
		}
		if result, err := store.mutateBandwidth(ctx, "settle", "", "capacity-node", "replica-b", bandwidthReservation{}, link, now.Add(time.Second).UnixMilli(), now.Add(12*time.Second)); err != nil || result.Sums.Egress != 0 {
			t.Fatalf("complete window=%+v %v", result, err)
		}
	})

	t.Run("node audience census", func(t *testing.T) {
		env := NodeStreamCountsSnapshot{NodeID: "audience-node", Fence: 5, Seq: 10, Streams: map[string]uint32{"stream": 2}}
		write := func(snapshot NodeStreamCountsSnapshot) bool {
			payload, err := json.Marshal(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			change, err := json.Marshal(StateChange{Entity: StateEntityNodeStreamCounts, NodeID: snapshot.NodeID, Operation: StateOpUpsert, Payload: payload})
			if err != nil {
				t.Fatal(err)
			}
			accepted, err := store.setNodeStreamCountsFenced(ctx, snapshot, payload, change)
			if err != nil {
				t.Fatal(err)
			}
			return accepted
		}
		if !write(env) {
			t.Fatal("first census rejected")
		}
		before, err := engine.Client.XLen(ctx, store.changelog.Key()).Result()
		if err != nil {
			t.Fatal(err)
		}
		stale := env
		stale.Fence = 4
		stale.Seq = 99
		stale.Streams = map[string]uint32{"stream": 50}
		if write(stale) {
			t.Fatal("old owner replaced audience")
		}
		after, err := engine.Client.XLen(ctx, store.changelog.Key()).Result()
		if err != nil || before != after {
			t.Fatalf("stale census appended changelog: %d -> %d (%v)", before, after, err)
		}
		restored, err := store.getAllNodeStreamCounts()
		if err != nil {
			t.Fatal(err)
		}
		if got := restored[env.NodeID]; got == nil || got.Fence != 5 || got.Seq != 10 || got.Streams["stream"] != 2 {
			t.Fatalf("stored census: %+v", got)
		}
		if watermark, err := engine.Client.Get(ctx, store.keyNodeStreamCountsWatermark(env.NodeID)).Result(); err != nil || watermark != "5-10" {
			t.Fatalf("watermark %q: %v", watermark, err)
		}
		if err := store.deleteNodeStreamCounts(env.NodeID); err != nil {
			t.Fatal(err)
		}
		if present, err := engine.Client.Exists(ctx, store.keyNodeStreamCounts(env.NodeID), store.keyNodeStreamCountsWatermark(env.NodeID)).Result(); err != nil || present != 0 {
			t.Fatalf("evicted census keys=%d: %v", present, err)
		}
	})

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

	const highFence = int64(4503599627370497)
	envelope := []byte(`{"node_id":"node-1","fence":4503599627370497,"seq":1,"ready":true,"artifacts":[]}`)
	change, err := json.Marshal(StateChange{InstanceID: "instance-c", Entity: StateEntityArtifact, Operation: StateOpUpsert, NodeID: "node-1", Payload: envelope})
	if err != nil {
		t.Fatal(err)
	}
	if applied, applyErr := store.SetNodeArtifactsFenced(ctx, "node-1", envelope, change, highFence, 1); applyErr != nil || !applied {
		t.Fatalf("apply artifact snapshot: applied=%v err=%v", applied, applyErr)
	}
	if watermark, getErr := store.client.Get(ctx, store.keyArtifactWatermark("node-1")).Result(); getErr != nil || watermark != "4503599627370497-1" {
		t.Fatalf("artifact watermark = %q err=%v, want exact high-range decimal", watermark, getErr)
	}
	envelope = []byte(`{"node_id":"node-1","fence":4503599627370497,"seq":2,"ready":true,"artifacts":[]}`)
	change, err = json.Marshal(StateChange{InstanceID: "instance-c", Entity: StateEntityArtifact, Operation: StateOpUpsert, NodeID: "node-1", Payload: envelope})
	if err != nil {
		t.Fatal(err)
	}
	if applied, applyErr := store.SetNodeArtifactsFenced(ctx, "node-1", envelope, change, highFence, 2); applyErr != nil || !applied {
		t.Fatalf("higher sequence on high fence: applied=%v err=%v", applied, applyErr)
	}
	if applied, applyErr := store.SetNodeArtifactsFenced(ctx, "node-1", []byte(`{"node_id":"node-1"}`), change, highFence, 2); applyErr != nil || applied {
		t.Fatalf("equal watermark applied: applied=%v err=%v", applied, applyErr)
	}
	if applied, applyErr := store.SetNodeArtifactsFenced(ctx, "node-1", []byte(`{"node_id":"node-1"}`), change, highFence-1, 99); applyErr != nil || applied {
		t.Fatalf("lower fence applied: applied=%v err=%v", applied, applyErr)
	}

	tail, err := store.ChangelogTail(ctx)
	if err != nil || tail == "0-0" {
		t.Fatalf("artifact write did not append changelog: tail=%q err=%v", tail, err)
	}
	engine.Restart(t)
	store = NewRedisStateStore(engine.Client, "state-contract")
	if watermark, getErr := store.client.Get(ctx, store.keyArtifactWatermark("node-1")).Result(); getErr != nil || watermark != "4503599627370497-2" {
		t.Fatalf("AOF restart artifact watermark=%q err=%v, want exact high-range decimal", watermark, getErr)
	}
	artifacts, err := store.GetAllNodeArtifacts()
	if err != nil || artifacts["node-1"] == nil || artifacts["node-1"].Fence != highFence || artifacts["node-1"].Seq != 2 {
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

	if allowed, _, _, err := a.TryRegisterViewer("tenant-1", "node-a", "session-a", "viewer-a", 2); err != nil || !allowed {
		t.Fatalf("reserve viewer-a: allowed=%v err=%v", allowed, err)
	}
	if allowed, _, _, err := b.TryRegisterViewer("tenant-1", "node-b", "session-b", "viewer-b", 2); err != nil || !allowed {
		t.Fatalf("reserve viewer-b: allowed=%v err=%v", allowed, err)
	}
	if got := b.CountViewers("tenant-1"); got != 2 {
		t.Fatalf("cluster-wide viewer count=%d, want 2", got)
	}
	// A fresh manager has no process-local virtual-viewer correlation. USER_END
	// must still resolve the shared session mapping and release the right member.
	restarted := NewTenantCapacityManager()
	restarted.EnableRedisSync(engine.Client, "capacity-contract")
	if capacityID, released, count, err := restarted.ReleaseViewerSession("tenant-1", "node-a", "session-a"); err != nil || capacityID != "viewer-a" || !released || count != 1 {
		t.Fatalf("cross-replica viewer release = capacity=%q released=%v count=%d err=%v", capacityID, released, count, err)
	}
}

func TestTenantCapacityPlacementDeadline_RealValkey(t *testing.T) {
	engine := dockervalkey.Start(t)
	ctx := context.Background()
	m := NewTenantCapacityManager()
	m.EnableRedisSync(engine.Client, "placement-deadline")
	if allowed, _, count, err := m.TryRegisterViewerBefore(ctx, "tenant", "node", "session", "viewer", 1, time.Now().Add(20*time.Second)); !allowed || count != 1 || err != nil {
		t.Fatalf("valid placement reservation failed: %v %d %v", allowed, count, err)
	}
	keys := m.viewerKeys("tenant")
	before := make([]string, len(keys))
	for i, key := range keys {
		var err error
		before[i], err = engine.Client.Dump(ctx, key).Result()
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, session := range []string{"session", "new-session"} {
		now := time.Now()
		// Execute the actual writer with already-expired authority to exercise
		// the server clock guard independently of the Go preflight check.
		result, err := reserveTenantViewer.Run(ctx, engine.Client, keys, viewerSessionField("node", session), "viewer", 1,
			now.UnixMilli(), now.Add(tenantViewerCapacityLease).UnixMilli(), now.Add(tenantViewerCorrelationRetention).UnixMilli(),
			(tenantViewerCapacityLease + tenantViewerCorrelationRetention).Milliseconds(), now.Add(-time.Second).UnixMilli()).Result()
		if err != nil {
			t.Fatal(err)
		}
		values, err := redisInts(result, 3)
		if err != nil || values[0] != -1 {
			t.Fatalf("expired server-side admission accepted: %v %v", values, err)
		}
		for i, key := range keys {
			after, err := engine.Client.Dump(ctx, key).Result()
			if err != nil || after != before[i] {
				t.Fatalf("expired admission mutated %s: %v", key, err)
			}
		}
	}
}

func TestTenantCapacityRenewRespectsCap_RealValkey(t *testing.T) {
	engine := dockervalkey.Start(t)
	m := NewTenantCapacityManager()
	m.EnableRedisSync(engine.Client, "capacity-renew-cap")
	now := time.Now().Truncate(time.Millisecond)
	m.now = func() time.Time { return now }
	if allowed, _, _, err := m.TryRegisterViewer("tenant-cap", "node-a", "session-a", "viewer-a", 1); err != nil || !allowed {
		t.Fatalf("reserve viewer-a: allowed=%v err=%v", allowed, err)
	}
	now = now.Add(tenantViewerCapacityLease + time.Second)
	if allowed, _, _, err := m.TryRegisterViewer("tenant-cap", "node-b", "session-b", "viewer-b", 1); err != nil || !allowed {
		t.Fatalf("reserve viewer-b: allowed=%v err=%v", allowed, err)
	}
	if err := m.RenewViewerSession("tenant-cap", "node-a", "session-a", 1); err != nil {
		t.Fatalf("renew viewer-a: %v", err)
	}
	if got := m.CountViewers("tenant-cap"); got != 1 {
		t.Fatalf("viewer count after late renew = %d, want cap 1", got)
	}
	if m.HasViewer("tenant-cap", "viewer-a") {
		t.Fatal("late inventory renew reactivated viewer-a above the cap")
	}
}

func TestTenantCapacityBoundedCleanupCannotEraseReactivatedViewer_RealValkey(t *testing.T) {
	engine := dockervalkey.Start(t)
	m := NewTenantCapacityManager()
	m.EnableRedisSync(engine.Client, "capacity-refcount-contract")
	now := time.Now().Truncate(time.Millisecond)
	m.now = func() time.Time { return now }

	// One more than a Lua cleanup batch leaves a stale correlation behind the
	// reactivation call. All sessions refer to the same logical viewer.
	for i := range 129 {
		sessionID := fmt.Sprintf("stale-%03d", i)
		if allowed, _, _, err := m.TryRegisterViewer("tenant-bounded", "node-a", sessionID, "viewer-a", 1); err != nil || !allowed {
			t.Fatalf("seed stale session %d: allowed=%v err=%v", i, allowed, err)
		}
	}
	now = now.Add(tenantViewerCapacityLease + time.Second)
	if allowed, _, count, err := m.TryRegisterViewer("tenant-bounded", "node-a", "live", "viewer-a", 1); err != nil || !allowed || count != 1 {
		t.Fatalf("reactivate viewer-a: allowed=%v count=%d err=%v", allowed, count, err)
	}
	// This reservation runs the next bounded cleanup batch. Its final stale
	// decrement must leave viewer-a's live reference intact and enforce the cap.
	if allowed, _, count, err := m.TryRegisterViewer("tenant-bounded", "node-b", "competitor", "viewer-b", 1); err != nil || allowed || count != 1 {
		t.Fatalf("competitor after stale cleanup: allowed=%v count=%d err=%v", allowed, count, err)
	}
}

func TestTenantCapacityTargetReconciledBeyondBoundedCleanup_RealValkey(t *testing.T) {
	engine := dockervalkey.Start(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Millisecond)

	for _, operation := range []string{"reserve", "renew", "release"} {
		t.Run(operation, func(t *testing.T) {
			m := NewTenantCapacityManager()
			clusterID := "capacity-target-" + operation
			m.EnableRedisSync(engine.Client, clusterID)
			m.now = func() time.Time { return now }
			tenantID := "tenant-target"
			keys := m.viewerKeys(tenantID)
			expiredAt := now.Add(-time.Second).UnixMilli()
			retainedUntil := now.Add(time.Hour).UnixMilli()
			targetField := viewerSessionField("node-z", "zzzz-target")
			targetCapacity := "viewer-target"
			pipe := engine.Client.TxPipeline()
			for i := range 129 {
				field := viewerSessionField("node-a", fmt.Sprintf("decoy-%03d", i))
				capacity := fmt.Sprintf("viewer-decoy-%03d", i)
				pipe.ZAdd(ctx, keys[0], goredis.Z{Score: float64(expiredAt), Member: capacity})
				pipe.ZAdd(ctx, keys[1], goredis.Z{Score: float64(expiredAt), Member: field})
				pipe.HSet(ctx, keys[2], field, capacity)
				pipe.HSet(ctx, keys[3], capacity, 1)
				pipe.ZAdd(ctx, keys[4], goredis.Z{Score: float64(retainedUntil), Member: field})
			}
			pipe.ZAdd(ctx, keys[0], goredis.Z{Score: float64(expiredAt), Member: targetCapacity})
			pipe.ZAdd(ctx, keys[1], goredis.Z{Score: float64(expiredAt), Member: targetField})
			pipe.HSet(ctx, keys[2], targetField, targetCapacity)
			pipe.HSet(ctx, keys[3], targetCapacity, 1)
			pipe.ZAdd(ctx, keys[4], goredis.Z{Score: float64(retainedUntil), Member: targetField})
			if _, err := pipe.Exec(ctx); err != nil {
				t.Fatal(err)
			}

			switch operation {
			case "reserve":
				allowed, _, _, err := m.TryRegisterViewer(tenantID, "node-z", "zzzz-target", "viewer-replacement", 1000)
				if err != nil || !allowed {
					t.Fatalf("replace expired target: allowed=%v err=%v", allowed, err)
				}
			case "renew":
				if err := m.RenewViewerSession(tenantID, "node-z", "zzzz-target", 1000); err != nil {
					t.Fatal(err)
				}
			case "release":
				if _, _, _, err := m.ReleaseViewerSession(tenantID, "node-z", "zzzz-target"); err != nil {
					t.Fatal(err)
				}
			}

			refs, err := engine.Client.HGet(ctx, keys[3], targetCapacity).Int64()
			if operation == "renew" {
				if err != nil || refs != 1 {
					t.Fatalf("renewed target refs=%d err=%v, want one exact reference", refs, err)
				}
			} else if !errors.Is(err, goredis.Nil) {
				t.Fatalf("retired target reference remains: refs=%d err=%v", refs, err)
			}
		})
	}
}
