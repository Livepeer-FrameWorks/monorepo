package state

import (
	"context"
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

	if err := store.SetConnOwner(ctx, "node-1", "inst-a", "10.0.0.1:9090"); err != nil {
		t.Fatalf("SetConnOwner: %v", err)
	}

	mr.FastForward(45 * time.Second)
	if err := store.RefreshConnOwner(ctx, "node-1"); err != nil {
		t.Fatalf("RefreshConnOwner: %v", err)
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
	err := store.RefreshConnOwner(context.Background(), "missing-node")
	if !errors.Is(err, ErrConnOwnerMissing) {
		t.Fatalf("expected ErrConnOwnerMissing, got %v", err)
	}
}

func TestDeleteConnOwnerIfMatch(t *testing.T) {
	store, _, _ := newRedisStateStore(t)
	ctx := context.Background()

	if err := store.SetConnOwner(ctx, "node-1", "inst-a", "10.0.0.1:9090"); err != nil {
		t.Fatalf("SetConnOwner: %v", err)
	}

	// Mismatched value: should not delete.
	deleted, err := store.DeleteConnOwnerIfMatch(ctx, "node-1", "inst-b", "10.0.0.2:9090")
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
	deleted, err = store.DeleteConnOwnerIfMatch(ctx, "node-1", "inst-a", "10.0.0.1:9090")
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

func TestPendingDVRStopConsumeIsSingleUse(t *testing.T) {
	store, mr, _ := newRedisStateStore(t)
	ctx := context.Background()

	if err := store.RegisterPendingDVRStop(ctx, "tenant+stream", time.Now()); err != nil {
		t.Fatalf("RegisterPendingDVRStop: %v", err)
	}

	consumed, err := store.ConsumePendingDVRStop(ctx, "tenant+stream")
	if err != nil {
		t.Fatalf("ConsumePendingDVRStop first: %v", err)
	}
	if !consumed {
		t.Fatal("expected first consume to find pending stop")
	}

	consumed, err = store.ConsumePendingDVRStop(ctx, "tenant+stream")
	if err != nil {
		t.Fatalf("ConsumePendingDVRStop second: %v", err)
	}
	if consumed {
		t.Fatal("expected pending stop to be single-use")
	}

	if mr.Exists(store.keyPendingDVRStop("tenant+stream")) {
		t.Fatal("expected pending stop key to be deleted after consume")
	}
}

func TestPendingDVRStopExpires(t *testing.T) {
	store, mr, _ := newRedisStateStore(t)
	ctx := context.Background()

	if err := store.RegisterPendingDVRStop(ctx, "tenant+stream", time.Now()); err != nil {
		t.Fatalf("RegisterPendingDVRStop: %v", err)
	}
	mr.FastForward(pendingDVRStopTTL + time.Second)

	consumed, err := store.ConsumePendingDVRStop(ctx, "tenant+stream")
	if err != nil {
		t.Fatalf("ConsumePendingDVRStop: %v", err)
	}
	if consumed {
		t.Fatal("expected expired pending stop to be absent")
	}
}

func TestConnOwnerRedisUnavailable(t *testing.T) {
	store, mr, client := newRedisStateStore(t)
	mr.Close()

	if err := store.SetConnOwner(context.Background(), "node-1", "inst-a", "10.0.0.1:9090"); err == nil {
		t.Fatal("expected SetConnOwner to fail when redis is unavailable")
	}
	if _, err := store.GetConnOwner(context.Background(), "node-1"); err == nil {
		t.Fatal("expected GetConnOwner to fail when redis is unavailable")
	}
	if err := store.RefreshConnOwner(context.Background(), "node-1"); err == nil {
		t.Fatal("expected RefreshConnOwner to fail when redis is unavailable")
	}

	_ = client.Close()
}
