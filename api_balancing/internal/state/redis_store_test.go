package state

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
)

func newRedisStateStore(t *testing.T) (*RedisStateStore, *miniredis.Miniredis, goredis.UniversalClient) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return NewRedisStateStore(client, "test-cluster"), mr, client
}

func TestConnOwnerTTLAndRefresh(t *testing.T) {
	store, mr, _ := newRedisStateStore(t)
	ctx := context.Background()

	if ok, err := store.AcquireConnOwnerFenced(ctx, "node-1", "inst-a", "10.0.0.1:9090", 1); err != nil || !ok {
		t.Fatalf("AcquireConnOwnerFenced: ok=%v err=%v", ok, err)
	}

	mr.FastForward(45 * time.Second)
	if err := store.RefreshConnOwnerFenced(ctx, "node-1", "inst-a", "10.0.0.1:9090", 1); err != nil {
		t.Fatalf("RefreshConnOwnerFenced: %v", err)
	}

	mr.FastForward(30 * time.Second)
	owner, err := store.GetConnOwner(ctx, "node-1")
	if err != nil {
		t.Fatalf("GetConnOwner: %v", err)
	}
	if owner.InstanceID != "inst-a" {
		t.Fatalf("expected owner to survive refreshed TTL, got %+v", owner)
	}

	mr.FastForward(61 * time.Second)
	owner, err = store.GetConnOwner(ctx, "node-1")
	if err != nil {
		t.Fatalf("GetConnOwner after expiry: %v", err)
	}
	if owner.InstanceID != "" || owner.GRPCAddr != "" {
		t.Fatalf("expected conn owner to expire, got %+v", owner)
	}
}

func TestRefreshConnOwnerMissing(t *testing.T) {
	store, _, _ := newRedisStateStore(t)
	err := store.RefreshConnOwnerFenced(context.Background(), "missing-node", "inst-a", "10.0.0.1:9090", 1)
	if !errors.Is(err, ErrConnOwnerMissing) {
		t.Fatalf("expected ErrConnOwnerMissing, got %v", err)
	}
}

func TestDeleteConnOwnerIfMatch(t *testing.T) {
	store, _, _ := newRedisStateStore(t)
	ctx := context.Background()

	if ok, err := store.AcquireConnOwnerFenced(ctx, "node-1", "inst-a", "10.0.0.1:9090", 1); err != nil || !ok {
		t.Fatalf("AcquireConnOwnerFenced: ok=%v err=%v", ok, err)
	}

	// Mismatched value: should not delete.
	deleted, err := store.DeleteConnOwnerIfMatch(ctx, "node-1", "inst-b", "10.0.0.2:9090", 0)
	if err != nil {
		t.Fatalf("DeleteConnOwnerIfMatch mismatch: %v", err)
	}
	if deleted {
		t.Fatal("expected no deletion when value does not match")
	}
	owner, _ := store.GetConnOwner(ctx, "node-1")
	if owner.InstanceID != "inst-a" {
		t.Fatalf("owner should still be inst-a, got %+v", owner)
	}

	// Matching value: should delete.
	deleted, err = store.DeleteConnOwnerIfMatch(ctx, "node-1", "inst-a", "10.0.0.1:9090", 1)
	if err != nil {
		t.Fatalf("DeleteConnOwnerIfMatch match: %v", err)
	}
	if !deleted {
		t.Fatal("expected deletion when value matches")
	}
	owner, _ = store.GetConnOwner(ctx, "node-1")
	if owner.InstanceID != "" {
		t.Fatalf("expected empty owner after matched delete, got %+v", owner)
	}
}

func TestAcquireConnOwnerFenced_SupersedeAndLose(t *testing.T) {
	store, _, _ := newRedisStateStore(t)
	ctx := context.Background()

	// First connection (fence 5) acquires cleanly.
	ok, err := store.AcquireConnOwnerFenced(ctx, "node-1", "inst-a", "10.0.0.1:9090", 5)
	if err != nil || !ok {
		t.Fatalf("first acquire: ok=%v err=%v", ok, err)
	}

	// A lower fence (an old/delayed connection) must LOSE.
	ok, err = store.AcquireConnOwnerFenced(ctx, "node-1", "inst-b", "10.0.0.2:9090", 4)
	if err != nil {
		t.Fatalf("stale acquire err: %v", err)
	}
	if ok {
		t.Fatal("a lower fence must not acquire ownership")
	}
	owner, _ := store.GetConnOwner(ctx, "node-1")
	if owner.InstanceID != "inst-a" || owner.Fence != 5 {
		t.Fatalf("ownership must remain with the higher fence, got %+v", owner)
	}

	// An equal fence must also lose (strictly-higher required).
	ok, _ = store.AcquireConnOwnerFenced(ctx, "node-1", "inst-c", "10.0.0.3:9090", 5)
	if ok {
		t.Fatal("an equal fence must not acquire ownership")
	}

	// A strictly-higher fence (a reconnect) supersedes.
	ok, err = store.AcquireConnOwnerFenced(ctx, "node-1", "inst-d", "10.0.0.4:9090", 6)
	if err != nil || !ok {
		t.Fatalf("higher-fence acquire: ok=%v err=%v", ok, err)
	}
	owner, _ = store.GetConnOwner(ctx, "node-1")
	if owner.InstanceID != "inst-d" || owner.Fence != 6 {
		t.Fatalf("higher fence must take ownership, got %+v", owner)
	}
}

func TestNodeReapGuardFencesReconnect(t *testing.T) {
	store, _, _ := newRedisStateStore(t)
	ctx := context.Background()
	acquired, err := store.AcquireNodeReapGuard(ctx, "node-guarded", "reaper-token", time.Minute)
	if err != nil || !acquired {
		t.Fatalf("acquire reaper guard: acquired=%v err=%v", acquired, err)
	}
	if ok, err := store.AcquireConnOwnerFenced(ctx, "node-guarded", "inst-a", "10.0.0.1:9090", 1); ok || !errors.Is(err, ErrNodeReaping) {
		t.Fatalf("connection acquired through reaper guard: ok=%v err=%v", ok, err)
	}
	if err := store.ReleaseNodeReapGuard(ctx, "node-guarded", "reaper-token"); err != nil {
		t.Fatalf("release reaper guard: %v", err)
	}
	if ok, err := store.AcquireConnOwnerFenced(ctx, "node-guarded", "inst-a", "10.0.0.1:9090", 1); err != nil || !ok {
		t.Fatalf("connection did not acquire after guard release: ok=%v err=%v", ok, err)
	}
}

func TestRefreshConnOwnerFenced_RenewLostMissing(t *testing.T) {
	store, mr, _ := newRedisStateStore(t)
	ctx := context.Background()

	if ok, err := store.AcquireConnOwnerFenced(ctx, "node-1", "inst-a", "10.0.0.1:9090", 5); err != nil || !ok {
		t.Fatalf("acquire: ok=%v err=%v", ok, err)
	}

	// Same owner+fence renews.
	if err := store.RefreshConnOwnerFenced(ctx, "node-1", "inst-a", "10.0.0.1:9090", 5); err != nil {
		t.Fatalf("renew by owner: %v", err)
	}

	// A higher fence takes over; the old owner's refresh reports LOST.
	if ok, _ := store.AcquireConnOwnerFenced(ctx, "node-1", "inst-b", "10.0.0.2:9090", 6); !ok {
		t.Fatal("higher fence should have acquired")
	}
	if err := store.RefreshConnOwnerFenced(ctx, "node-1", "inst-a", "10.0.0.1:9090", 5); !errors.Is(err, ErrConnOwnerLost) {
		t.Fatalf("expected ErrConnOwnerLost for superseded owner, got %v", err)
	}

	// Expired key → ErrConnOwnerMissing.
	mr.FastForward(61 * time.Second)
	if err := store.RefreshConnOwnerFenced(ctx, "node-1", "inst-b", "10.0.0.2:9090", 6); !errors.Is(err, ErrConnOwnerMissing) {
		t.Fatalf("expected ErrConnOwnerMissing after expiry, got %v", err)
	}
}

func TestSetNodeArtifactsFenced_StaleNoop(t *testing.T) {
	store, _, _ := newRedisStateStore(t)
	ctx := context.Background()

	// The fenced write takes an envelope value + the changelog entry (both marshaled). Helper builds an
	// envelope with one artifact and its matching StateChange entry.
	write := func(fence, seq int64, hash string) (bool, error) {
		env := &NodeArtifactSnapshot{NodeID: "node-1", Fence: fence, Seq: seq,
			Artifacts: []*NodeArtifactState{{NodeID: "node-1", ClipHash: hash}}}
		envBytes, _ := json.Marshal(env)
		changeBytes, _ := json.Marshal(StateChange{Entity: StateEntityArtifact, NodeID: "node-1", Payload: envBytes})
		return store.SetNodeArtifactsFenced(ctx, "node-1", envBytes, changeBytes, fence, seq)
	}
	hashOf := func() string {
		arts, _ := store.GetAllNodeArtifacts()
		env := arts["node-1"]
		if env == nil || len(env.Artifacts) != 1 {
			t.Fatalf("expected exactly 1 artifact, got %+v", env)
		}
		return env.Artifacts[0].ClipHash
	}

	if applied, err := write(1, 10, "h10"); err != nil || !applied {
		t.Fatalf("initial write: applied=%v err=%v", applied, err)
	}
	// A stale write (same fence, lower seq) is a no-op and must NOT overwrite.
	if applied, err := write(1, 9, "h9"); err != nil || applied {
		t.Fatalf("stale write must be a no-op: applied=%v err=%v", applied, err)
	}
	if h := hashOf(); h != "h10" {
		t.Fatalf("stale write overwrote the newer inventory: %s", h)
	}
	// Newer seq applies; higher fence with a lower seq also applies (reconnect).
	if applied, _ := write(1, 11, "h11"); !applied {
		t.Fatal("newer seq should apply")
	}
	if applied, _ := write(2, 1, "h2"); !applied {
		t.Fatal("higher fence should apply even with a lower seq")
	}
	if h := hashOf(); h != "h2" {
		t.Fatalf("expected reconnect write to win, got %s", h)
	}
	// An unversioned write is rejected (no bypass).
	if applied, err := store.SetNodeArtifactsFenced(ctx, "node-1", []byte(`{}`), []byte(`{}`), 0, 0); applied || !errors.Is(err, ErrUnversionedArtifactWrite) {
		t.Fatalf("unversioned write must be rejected: applied=%v err=%v", applied, err)
	}
}

// The core empty-snapshot fix: an EMPTY authoritative report carries (fence, seq) and is ordered like
// any other — a stale empty cannot clear a newer inventory, and a newer empty legitimately clears it.
func TestSetNodeArtifactsFenced_EmptyEnvelopeOrdering(t *testing.T) {
	store, _, _ := newRedisStateStore(t)
	ctx := context.Background()
	write := func(fence, seq int64, artifacts []*NodeArtifactState) (bool, error) {
		env := &NodeArtifactSnapshot{NodeID: "node-1", Fence: fence, Seq: seq, Artifacts: artifacts}
		envBytes, _ := json.Marshal(env)
		changeBytes, _ := json.Marshal(StateChange{Entity: StateEntityArtifact, NodeID: "node-1", Payload: envBytes})
		return store.SetNodeArtifactsFenced(ctx, "node-1", envBytes, changeBytes, fence, seq)
	}

	if applied, _ := write(5, 10, []*NodeArtifactState{{NodeID: "node-1", ClipHash: "h1"}}); !applied {
		t.Fatal("initial non-empty write should apply")
	}
	// A STALE empty report (lower seq) must be a no-op — it must not clear the inventory.
	if applied, _ := write(5, 9, nil); applied {
		t.Fatal("stale empty report must not apply")
	}
	if arts, _ := store.GetAllNodeArtifacts(); arts["node-1"] == nil || len(arts["node-1"].Artifacts) != 1 {
		t.Fatal("stale empty report cleared a newer inventory")
	}
	// A NEWER empty report (higher fence) legitimately clears the inventory.
	if applied, _ := write(6, 1, nil); !applied {
		t.Fatal("newer empty report should apply")
	}
	if arts, _ := store.GetAllNodeArtifacts(); arts["node-1"] == nil || len(arts["node-1"].Artifacts) != 0 {
		t.Fatalf("newer empty report should have cleared the inventory: %+v", arts["node-1"])
	}
}

func TestRedisLeaseAcquireRenewRelease(t *testing.T) {
	store, mr, _ := newRedisStateStore(t)
	ctx := context.Background()
	ttl := 15 * time.Second

	acquired, err := store.TryAcquireLease(ctx, "qm_reporter", "inst-a", ttl)
	if err != nil {
		t.Fatalf("TryAcquireLease inst-a: %v", err)
	}
	if !acquired {
		t.Fatal("expected inst-a to acquire lease")
	}

	acquired, err = store.TryAcquireLease(ctx, "qm_reporter", "inst-b", ttl)
	if err != nil {
		t.Fatalf("TryAcquireLease inst-b: %v", err)
	}
	if acquired {
		t.Fatal("expected inst-b not to acquire held lease")
	}

	mr.FastForward(10 * time.Second)
	renewed, err := store.RenewLease(ctx, "qm_reporter", "inst-a", ttl)
	if err != nil {
		t.Fatalf("RenewLease inst-a: %v", err)
	}
	if !renewed {
		t.Fatal("expected inst-a to renew lease")
	}

	mr.FastForward(10 * time.Second)
	acquired, err = store.TryAcquireLease(ctx, "qm_reporter", "inst-b", ttl)
	if err != nil {
		t.Fatalf("TryAcquireLease inst-b after renew: %v", err)
	}
	if acquired {
		t.Fatal("expected renewed lease to remain held by inst-a")
	}

	if releaseErr := store.ReleaseLease(ctx, "qm_reporter", "inst-b"); releaseErr != nil {
		t.Fatalf("ReleaseLease non-owner: %v", releaseErr)
	}
	acquired, err = store.TryAcquireLease(ctx, "qm_reporter", "inst-b", ttl)
	if err != nil {
		t.Fatalf("TryAcquireLease inst-b after stale release: %v", err)
	}
	if acquired {
		t.Fatal("expected non-owner release not to clear lease")
	}

	if releaseErr := store.ReleaseLease(ctx, "qm_reporter", "inst-a"); releaseErr != nil {
		t.Fatalf("ReleaseLease owner: %v", releaseErr)
	}
	acquired, err = store.TryAcquireLease(ctx, "qm_reporter", "inst-b", ttl)
	if err != nil {
		t.Fatalf("TryAcquireLease inst-b after owner release: %v", err)
	}
	if !acquired {
		t.Fatal("expected inst-b to acquire released lease")
	}
}

func TestConnOwnerRedisUnavailable(t *testing.T) {
	store, mr, client := newRedisStateStore(t)
	mr.Close()

	if _, err := store.AcquireConnOwnerFenced(context.Background(), "node-1", "inst-a", "10.0.0.1:9090", 1); err == nil {
		t.Fatal("expected AcquireConnOwnerFenced to fail when redis is unavailable")
	}
	if _, err := store.GetConnOwner(context.Background(), "node-1"); err == nil {
		t.Fatal("expected GetConnOwner to fail when redis is unavailable")
	}
	if err := store.RefreshConnOwnerFenced(context.Background(), "node-1", "inst-a", "10.0.0.1:9090", 1); err == nil {
		t.Fatal("expected RefreshConnOwnerFenced to fail when redis is unavailable")
	}

	_ = client.Close()
}
