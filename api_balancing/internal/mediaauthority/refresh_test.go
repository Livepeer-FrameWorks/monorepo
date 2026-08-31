package mediaauthority

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestSoftExpiryRefreshIsBackgroundAndCooldownBounded(t *testing.T) {
	store := &Store{now: func() time.Time { return storeFixtureNow }, refresh: &refreshCoordinator{}}
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var calls atomic.Int32
	store.SetRefreshRequester(func(context.Context) error {
		calls.Add(1)
		started <- struct{}{}
		<-release
		return nil
	})
	store.observeFreshness(FreshnessSoftExpired)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("background refresh did not start")
	}
	store.observeFreshness(FreshnessSoftExpired)
	if calls.Load() != 1 {
		t.Fatalf("refresh calls during cooldown = %d", calls.Load())
	}
	close(release)
	// Join any still-running singleflight call before advancing the fake
	// clock. Otherwise this test races the group cleanup performed after the
	// requester returns and can mistake coalescing for a cooldown failure.
	_, _, _ = store.refresh.group.Do("cell-replay", func() (any, error) { return nil, nil })
	store.now = func() time.Time { return storeFixtureNow.Add(localAuthorityRefreshCooldown + time.Second) }
	startedAgain := make(chan struct{}, 1)
	store.SetRefreshRequester(func(context.Context) error {
		calls.Add(1)
		startedAgain <- struct{}{}
		return nil
	})
	store.observeFreshness(FreshnessSoftExpired)
	select {
	case <-startedAgain:
	case <-time.After(time.Second):
		t.Fatal("refresh did not restart after cooldown")
	}
	if calls.Load() != 2 {
		t.Fatalf("refresh calls after cooldown = %d", calls.Load())
	}
}
