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
