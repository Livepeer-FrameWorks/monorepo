package state

import (
	"context"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"testing"
	"time"
)

func TestStrictFreeResourceCountDoesNotInferZeroDuringRedisOutage(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: -1})
	defer client.Close()
	m := NewTenantCapacityManager()
	m.EnableRedisSync(client, "cell")
	server.Close()
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if count, err := m.CountViewersStrict(ctx, "tenant"); err == nil {
		t.Fatalf("HA outage became an eligible empty account: count=%d", count)
	}
}
