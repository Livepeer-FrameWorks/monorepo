package datafetcher

import (
	"context"
	"errors"
	"testing"
	"time"

	"frameworks/api_gateway/internal/loaders"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/cache"
)

func newTestCache() *cache.Cache {
	return cache.New(cache.Options{TTL: time.Minute, MaxEntries: 100}, cache.MetricsHooks{})
}

// buildKey is the cache/memo identity for a fetch. Distinct operations or key
// parts must never collide, or one resolver would serve another's data.
func TestBuildKey(t *testing.T) {
	df := New(Config{})
	tests := []struct {
		name string
		req  FetchRequest
		want string
	}{
		{"service+op only", FetchRequest{Service: ServiceCommodore, Operation: "GetStream"}, "commodore|GetStream"},
		{"with key parts", FetchRequest{Service: ServicePeriscope, Operation: "Status", KeyParts: []string{"t1", "s1"}}, "periscope|Status|t1|s1"},
		{"distinct parts don't merge", FetchRequest{Service: ServiceQuartermaster, Operation: "Op", KeyParts: []string{"a", "b"}}, "quartermaster|Op|a|b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := df.buildKey(tt.req); got != tt.want {
				t.Fatalf("buildKey = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFetch_RequiresLoader(t *testing.T) {
	df := New(Config{})
	_, err := df.Fetch(context.Background(), FetchRequest{Service: ServiceCommodore, Operation: "X"})
	if err == nil {
		t.Fatal("expected error when loader is nil")
	}
}

func TestFetch_CacheHitAvoidsSecondLoaderCall(t *testing.T) {
	df := New(Config{Caches: map[Service]*cache.Cache{ServiceCommodore: newTestCache()}})
	var calls int
	req := FetchRequest{
		Service:   ServiceCommodore,
		Operation: "GetStream",
		KeyParts:  []string{"s1"},
		Loader: func(context.Context) (any, error) {
			calls++
			return "v", nil
		},
	}

	for range 3 {
		v, err := df.Fetch(context.Background(), req)
		if err != nil || v != "v" {
			t.Fatalf("got (%v,%v), want (v,nil)", v, err)
		}
	}
	// First call populates the cache; the next two are served from it.
	if calls != 1 {
		t.Fatalf("loader called %d times, want 1 (cache hit)", calls)
	}
}

func TestFetch_SkipCacheBypassesCacheEveryTime(t *testing.T) {
	df := New(Config{Caches: map[Service]*cache.Cache{ServiceCommodore: newTestCache()}})
	var calls int
	req := FetchRequest{
		Service:   ServiceCommodore,
		Operation: "GetStream",
		KeyParts:  []string{"s1"},
		SkipCache: true,
		Loader: func(context.Context) (any, error) {
			calls++
			return "v", nil
		},
	}

	for range 3 {
		if _, err := df.Fetch(context.Background(), req); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 3 {
		t.Fatalf("loader called %d times, want 3 (SkipCache bypasses cache)", calls)
	}
}

func TestFetch_NoCacheConfiguredFallsThroughToLoader(t *testing.T) {
	// No cache registered for the service → every Fetch calls the loader.
	df := New(Config{})
	var calls int
	req := FetchRequest{
		Service:   ServiceCommodore,
		Operation: "GetStream",
		Loader: func(context.Context) (any, error) {
			calls++
			return "v", nil
		},
	}
	for range 2 {
		if _, err := df.Fetch(context.Background(), req); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatalf("loader called %d times, want 2 (no cache → no memo without loaders ctx)", calls)
	}
}

func TestFetch_NilCacheEntryIsDropped(t *testing.T) {
	// New() must drop nil cache entries so a configured-but-nil service does not
	// panic on Get; it should fall through to the loader instead.
	df := New(Config{Caches: map[Service]*cache.Cache{ServiceCommodore: nil}})
	var calls int
	req := FetchRequest{
		Service:   ServiceCommodore,
		Operation: "GetStream",
		Loader: func(context.Context) (any, error) {
			calls++
			return "v", nil
		},
	}
	if _, err := df.Fetch(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("loader called %d times, want 1", calls)
	}
}

func TestFetch_LoaderErrorPropagates(t *testing.T) {
	df := New(Config{Caches: map[Service]*cache.Cache{ServiceCommodore: newTestCache()}})
	sentinel := errors.New("downstream failed")
	req := FetchRequest{
		Service:   ServiceCommodore,
		Operation: "GetStream",
		Loader:    func(context.Context) (any, error) { return nil, sentinel },
	}
	_, err := df.Fetch(context.Background(), req)
	if !errors.Is(err, sentinel) {
		t.Fatalf("want sentinel error, got %v", err)
	}
}

func TestFetch_MemoDedupesWithinRequest(t *testing.T) {
	// With a request-scoped Memoizer in context (and no SkipMemo), repeated
	// fetches of the same key collapse to a single loader call — this is the
	// per-request N+1 guard layered above the cross-request cache.
	df := New(Config{})
	lds := &loaders.Loaders{Memo: loaders.NewMemoizer()}
	ctx := loaders.ContextWithLoaders(context.Background(), lds)
	var calls int
	req := FetchRequest{
		Service:   ServiceCommodore,
		Operation: "GetStream",
		KeyParts:  []string{"s1"},
		Loader: func(context.Context) (any, error) {
			calls++
			return "v", nil
		},
	}
	for range 3 {
		if _, err := df.Fetch(ctx, req); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("loader called %d times, want 1 (memo dedup)", calls)
	}

	// SkipMemo on the same context must bypass the memo and hit the loader.
	req.SkipMemo = true
	req.KeyParts = []string{"s2"}
	for range 2 {
		if _, err := df.Fetch(ctx, req); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 3 {
		t.Fatalf("loader called %d total, want 3 (2 more with SkipMemo)", calls)
	}
}
