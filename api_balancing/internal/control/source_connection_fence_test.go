package control

import (
	"context"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

func TestLocalSourceConnectionFenceRequiresAuthenticatedCurrentIdentity(t *testing.T) {
	t.Cleanup(SetupTestRegistry("edge", &captureStream{}))
	c := registry.conns["edge"]
	c.canonicalID, c.clusterID, c.fence = "edge", "cluster", 9007199254740993
	if fence, ok := LocalSourceConnectionFence("edge", "cluster"); !ok || fence != c.fence {
		t.Fatalf("current registration missing or rounded: %d, %v", fence, ok)
	}
	for _, identity := range [][2]string{{"missing", "cluster"}, {"edge", "other"}, {"", "cluster"}, {"edge", ""}} {
		if _, ok := LocalSourceConnectionFence(identity[0], identity[1]); ok {
			t.Fatal("different identity used source connection")
		}
	}
	c.superseded.Store(true)
	if _, ok := LocalSourceConnectionFence("edge", "cluster"); ok {
		t.Fatal("superseded registration was current")
	}
	c.superseded.Store(false)
	c.fence = 0
	if _, ok := LocalSourceConnectionFence("edge", "cluster"); ok {
		t.Fatal("unfenced registration was current")
	}
}

func TestDestinationConnectionFenceUsesSharedOwnerWithoutLocalFallback(t *testing.T) {
	store, mr := newTestStore(t)
	setCommandRelay(t, buildRelay(t, store, "local", "local:9090", nil))
	t.Cleanup(SetupTestRegistry("edge", &captureStream{}))
	c := registry.conns["edge"]
	c.canonicalID, c.clusterID, c.fence = "edge", "cluster", 5
	ctx := context.Background()
	if _, err := DestinationConnectionFence(ctx, "edge", "cluster"); err == nil {
		t.Fatal("absent shared owner fell back to local registration")
	}
	const fence int64 = 9007199254740993
	if acquired, err := store.AcquireConnOwnerFenced(ctx, "edge", "peer", "peer:9090", fence); err != nil || !acquired {
		t.Fatalf("install peer ownership: %v", err)
	}
	if got, err := DestinationConnectionFence(ctx, "edge", "cluster"); err != nil || got != fence {
		t.Fatalf("peer ownership lost precision or locality independence: %d, %v", got, err)
	}
	mr.Close()
	if _, err := DestinationConnectionFence(ctx, "edge", "cluster"); err == nil {
		t.Fatal("unavailable shared owner fell back to local registration")
	}
	InitRelay(nil, "", "", nil, logging.NewLogger())
	if got, err := DestinationConnectionFence(ctx, "edge", "cluster"); err != nil || got != 5 {
		t.Fatalf("single-replica registration unavailable: %d, %v", got, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := DestinationConnectionFence(canceled, "edge", "cluster"); err == nil {
		t.Fatal("canceled ownership read succeeded")
	}
}
