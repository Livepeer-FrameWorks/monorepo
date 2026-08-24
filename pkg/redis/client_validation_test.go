package redis_test

import (
	"context"
	"testing"

	pkgredis "github.com/Livepeer-FrameWorks/monorepo/pkg/redis"
)

func TestNewUniversalClientRequiresExplicitTopology(t *testing.T) {
	ctx := context.Background()
	invalid := []pkgredis.Config{
		{Addrs: []string{"127.0.0.1:6379"}},
		{Mode: "unknown", Addrs: []string{"127.0.0.1:6379"}},
		{Mode: pkgredis.ModeSingle, Addrs: []string{"a:1", "b:2"}},
		{Mode: pkgredis.ModeSentinel, Addrs: []string{"a:1"}},
		{Mode: pkgredis.ModeCluster, Addrs: []string{"a:1"}, DB: 1},
	}
	for _, cfg := range invalid {
		if client, err := pkgredis.NewUniversalClient(ctx, cfg); err == nil {
			_ = client.Close()
			t.Fatalf("configuration %+v was accepted", cfg)
		}
	}
}
