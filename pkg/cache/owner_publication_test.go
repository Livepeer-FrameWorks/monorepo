package cache

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestOwnerPolicyIsCheckedAtPublication(t *testing.T) {
	var held atomic.Bool
	loading, publish := make(chan struct{}), make(chan struct{})
	cache := New(Options{
		SkipStoreValue: func(string, interface{}) bool { return held.Load() },
		ValueLifetime: func(string, interface{}) (time.Duration, time.Duration) {
			close(loading)
			<-publish
			return time.Minute, 0
		},
	}, MetricsHooks{})
	done := make(chan struct{})
	go func() { defer close(done); cache.SetDefault("resource:unknown-at-invalidation", "owner-evidence") }()
	<-loading
	held.Store(true)
	close(publish)
	<-done
	if _, found := cache.Peek("resource:unknown-at-invalidation"); found {
		t.Fatal("owner invalidation was checked before publication; stale evidence entered the cache")
	}
}
