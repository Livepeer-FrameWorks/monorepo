package state

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
	"github.com/sirupsen/logrus"
)

// A replica that starts while a stream is live rehydrates the stream's
// per-node instance from the write-through keys, so it knows the publisher is
// live and playable before that node reports again.
func TestRestartedReplicaRehydratesStreamInstances(t *testing.T) {
	mr := miniredis.RunT(t)
	logger := logrus.New()
	logger.SetLevel(logrus.WarnLevel)
	start := func(instanceID string) *StreamStateManager {
		sm := NewStreamStateManager()
		client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
		t.Cleanup(func() { _ = client.Close() })
		if err := sm.EnableRedisSync(context.Background(), NewRedisStateStore(client, "test-cluster"), instanceID, logger); err != nil {
			t.Fatalf("EnableRedisSync %s: %v", instanceID, err)
		}
		t.Cleanup(sm.Shutdown)
		return sm
	}

	running := start("replica-a")
	running.UpdateNodeStats("stream-1", "edge-1", 3, 1, 100, 200, false)
	if err := running.UpdateStreamFromBuffer("live+stream-1", "stream-1", "edge-1", "tenant-1", "FULL", ""); err != nil {
		t.Fatal(err)
	}

	restarted := start("replica-b")
	instance, found := restarted.GetStreamInstances("stream-1")["edge-1"]
	if !found {
		t.Fatalf("restarted replica has no instance of the live stream: %v", restarted.GetStreamInstances("stream-1"))
	}
	if instance.Status != "live" || instance.Inputs != 1 || !instance.Playable || instance.TenantID != "tenant-1" {
		t.Fatalf("rehydrated instance = %+v, want live, playable, one input, tenant-1", instance)
	}
	if _, stray := restarted.GetStreamInstances("")["edge-1"]; stray {
		t.Fatal("instance rehydrated under an empty stream name")
	}
}
