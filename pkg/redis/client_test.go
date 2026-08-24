//go:build schema_verify

package redis_test

import (
	"context"
	"testing"

	pkgredis "github.com/Livepeer-FrameWorks/monorepo/pkg/redis"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockervalkey"
)

func TestNewUniversalClientSingle_RealValkey(t *testing.T) {
	engine := dockervalkey.Start(t)
	client, err := pkgredis.NewUniversalClient(context.Background(), pkgredis.Config{
		Mode: pkgredis.ModeSingle, Addrs: []string{engine.Addr},
	})
	if err != nil {
		t.Fatalf("connect through production constructor: %v", err)
	}
	defer func() { _ = client.Close() }()
	if err = client.Set(context.Background(), "topology-contract", "ok", 0).Err(); err != nil {
		t.Fatalf("write through production constructor: %v", err)
	}
	if clusterClient, clusterErr := pkgredis.NewUniversalClient(context.Background(), pkgredis.Config{
		Mode: pkgredis.ModeCluster, Addrs: []string{engine.Addr},
	}); clusterErr == nil {
		_ = clusterClient.Close()
		t.Fatal("cluster mode accepted a standalone Valkey node")
	}
}
