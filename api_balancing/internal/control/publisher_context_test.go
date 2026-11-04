package control

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPublisherSourceWriteQueueHonorsCallerCancellation(t *testing.T) {
	registry := NewStreamRegistry(nil, "cluster-test", time.Minute)
	store, _, _ := newTestRedis(t)
	registry.redisStore = store
	lock := registry.sourceLocationLock("publisher")
	if err := lock.Acquire(t.Context(), 1); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := registry.publishUpsertSourceFencedContext(ctx, StreamEntry{InternalName: "publisher"})
		done <- err
	}()
	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("waiting publisher result: %v", err)
		}
		lock.Release(1)
	case <-time.After(150 * time.Millisecond):
		lock.Release(1)
		<-done
		t.Fatal("cancelled publisher kept waiting behind the source-write mutex")
	}
}
