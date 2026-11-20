package state

import (
	"context"
	"encoding/json"
	"math"
	"testing"
)

func TestStreamInstanceInvalidInfoKeepsLastGoodState(t *testing.T) {
	manager := NewStreamStateManager()
	defer manager.Shutdown()
	store, _, client := newRedisStateStore(t)
	manager.redisStore = store
	key := store.keyStreamInstance("stream", "edge")
	manager.UpdateStreamInstanceInfo("stream", "edge", map[string]any{"ready": true})
	before, err := client.Get(context.Background(), key).Result()
	if err != nil || !json.Valid([]byte(before)) {
		t.Fatalf("initial durable state = %q, %v", before, err)
	}
	manager.UpdateStreamInstanceInfo("stream", "edge", map[string]any{"invalid": make(chan int)})
	after, err := client.Get(context.Background(), key).Result()
	if err != nil || after != before {
		t.Fatalf("unserializable update replaced durable state: before=%q after=%q err=%v", before, after, err)
	}
	manager.mu.RLock()
	_, invalid := manager.streamInstances["stream"]["edge"].RawDetails["invalid"]
	manager.mu.RUnlock()
	if invalid {
		t.Fatal("unserializable info poisoned future stream-instance updates")
	}
	manager.UpdateStreamInstanceInfo("stream", "edge", map[string]any{"ready": false})
	after, err = client.Get(context.Background(), key).Result()
	if err != nil || !json.Valid([]byte(after)) || after == before {
		t.Fatalf("valid update after refusal did not persist: %q, %v", after, err)
	}
}

func TestNodeDiskInvalidMetricsKeepLastDurableState(t *testing.T) {
	manager := NewStreamStateManager()
	defer manager.Shutdown()
	store, _, client := newRedisStateStore(t)
	manager.redisStore = store
	manager.UpdateNodeDiskUsage("edge", 1000, 500)
	before, err := client.Get(context.Background(), store.keyNode("edge")).Result()
	if err != nil {
		t.Fatal(err)
	}
	manager.mu.Lock()
	manager.nodes["edge"].UpSpeed = math.NaN()
	manager.mu.Unlock()
	manager.UpdateNodeDiskUsage("edge", 1000, 600)
	after, err := client.Get(context.Background(), store.keyNode("edge")).Result()
	if err != nil || before != after {
		t.Fatalf("invalid metric replaced durable JSON: before=%q after=%q err=%v", before, after, err)
	}
}

func TestNodeMutationsNeverReplaceDurableJSONWithInvalidMetadata(t *testing.T) {
	mutations := map[string]func(*StreamStateManager){
		"storage": func(sm *StreamStateManager) { sm.SetNodeStoragePaths("edge", "/srv", "bucket", "prefix") },
		"gpu":     func(sm *StreamStateManager) { sm.SetNodeGPUInfo("edge", "vendor", 1, 4096, "8.6") },
		"identity": func(sm *StreamStateManager) {
			sm.SetNodeConnectionInfo(context.Background(), "edge", "", "tenant", "cluster", nil)
		},
		"info": func(sm *StreamStateManager) {
			sm.SetNodeInfo("edge", "https://edge.example", true, nil, nil, "", "", nil)
		},
		"disconnect": func(sm *StreamStateManager) { sm.MarkNodeDisconnected("edge") },
		"metrics":    func(sm *StreamStateManager) { setMetrics(sm, "edge", math.NaN(), 0, 1000) },
		"mode": func(sm *StreamStateManager) {
			if err := sm.SetNodeOperationalMode(context.Background(), "edge", NodeModeDraining, "operator"); err != nil {
				t.Error(err)
			}
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			sm := NewStreamStateManager()
			defer sm.Shutdown()
			store, _, client := newRedisStateStore(t)
			sm.redisStore = store
			sm.UpdateNodeDiskUsage("edge", 1000, 500)
			before, err := client.Get(context.Background(), store.keyNode("edge")).Result()
			if err != nil {
				t.Fatal(err)
			}
			sm.mu.Lock()
			sm.nodes["edge"].UpSpeed = math.NaN()
			sm.mu.Unlock()
			mutate(sm)
			after, err := client.Get(context.Background(), store.keyNode("edge")).Result()
			if err != nil || before != after {
				t.Fatalf("invalid metadata replaced valid durable JSON: before=%q after=%q err=%v", before, after, err)
			}
		})
	}
}
