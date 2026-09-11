package control

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCurrentInboundPullUsesDurableStateAcrossReplicas(t *testing.T) {
	store, client, _ := newTestRedis(t)
	t.Cleanup(func() { _ = client.Close() })
	writer := NewStreamRegistry(nil, "cluster-test", time.Minute)
	reader := NewStreamRegistry(nil, "cluster-test", time.Minute)
	writer.redisStore, reader.redisStore = store, store
	ctx := context.Background()
	first, err := writer.RecordInboundPull(ctx, "stream", testInbound("edge"))
	if err != nil {
		t.Fatal(err)
	}
	if _, found := reader.InboundPullForNode("stream", "edge"); found {
		t.Fatal("reader should have no local snapshot")
	}
	if got, found, readErr := reader.CurrentInboundPull(ctx, "live+stream", "edge"); readErr != nil || !found || got.AttemptID != first.AttemptID {
		t.Fatalf("new replica missed durable pull: %+v, %v, %v", got, found, readErr)
	}
	if marked, markErr := reader.MarkInboundDestinationObserved(ctx, "stream", "edge", first.AttemptID); markErr != nil || !marked {
		t.Fatalf("replica observation failed: %v", markErr)
	}
	if got, found, readErr := writer.CurrentInboundPull(ctx, "stream", "edge"); readErr != nil || !found || !got.DestinationObserved || got.AttemptID != first.AttemptID {
		t.Fatalf("observation was lost or revoked the source: %+v, %v", got, readErr)
	}
	if _, clearErr := reader.ClearInboundPull(ctx, "stream", "edge", first.AttemptID); clearErr != nil {
		t.Fatal(clearErr)
	}
	if _, found := writer.InboundPullForNode("stream", "edge"); !found {
		t.Fatal("writer should retain its lagging local snapshot")
	}
	if got, found, readErr := writer.CurrentInboundPull(ctx, "stream", "edge"); readErr != nil || found || !got.Cleared {
		t.Fatalf("stale replica reused cleared pull: %+v, %v, %v", got, found, readErr)
	}
}

func TestCurrentInboundPullNeverFallsBackOnMissingOrBrokenStorage(t *testing.T) {
	for _, scenario := range []string{"missing", "corrupt", "wrong-stream", "wrong-node", "unavailable", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			store, client, server := newTestRedis(t)
			t.Cleanup(func() { _ = client.Close() })
			registry := NewStreamRegistry(nil, "cluster-test", time.Minute)
			registry.redisStore = store
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if _, err := registry.RecordInboundPull(ctx, "stream", testInbound("edge")); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "missing":
				server.Del(store.keySource("stream"))
			case "corrupt":
				if err := server.Set(store.keySource("stream"), "{"); err != nil {
					t.Fatal(err)
				}
			case "wrong-stream":
				if err := server.Set(store.keySource("stream"), `{"InternalName":"other"}`); err != nil {
					t.Fatal(err)
				}
			case "wrong-node":
				if err := server.Set(store.keySource("stream"), `{"InternalName":"stream","Locations":{"cluster-test":{"InboundPulls":{"edge":{"DestNodeID":"other"}}}}}`); err != nil {
					t.Fatal(err)
				}
			case "unavailable":
				if err := client.Close(); err != nil {
					t.Fatal(err)
				}
			case "canceled":
				cancel()
			}
			got, found, err := registry.CurrentInboundPull(ctx, "stream", "edge")
			if found || (scenario != "missing" && err == nil) || (scenario == "canceled" && !errors.Is(err, context.Canceled)) {
				t.Fatalf("invalid state fell back to cached pull: %+v, %v, %v", got, found, err)
			}
		})
	}
}
