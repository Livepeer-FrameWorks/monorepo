package geoip

import (
	"context"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/cache"
)

// LookupCached is the cache-fronted entry point used on the hot routing path.
// Its contract is mostly about graceful degradation: a missing reader or a
// lookup that yields no geo data must collapse to nil rather than caching a
// bogus zero-value *GeoData. A zero-value Reader (no MMDB loaded) always
// returns nil from Lookup, which lets these branches be exercised without a
// database fixture.
func TestLookupCached(t *testing.T) {
	ctx := context.Background()
	newCache := func() *cache.Cache {
		return cache.New(cache.Options{
			TTL:         time.Minute,
			NegativeTTL: time.Minute,
			MaxEntries:  16,
		}, cache.MetricsHooks{})
	}

	t.Run("nil reader returns nil", func(t *testing.T) {
		if got := LookupCached(ctx, nil, newCache(), "8.8.8.8"); got != nil {
			t.Fatalf("expected nil for nil reader, got %+v", got)
		}
	})

	t.Run("nil cache delegates to reader", func(t *testing.T) {
		// Zero reader has no DB, so the direct Lookup path returns nil.
		// This proves the c == nil branch routes through reader.Lookup
		// rather than panicking on the absent cache.
		if got := LookupCached(ctx, &Reader{}, nil, "8.8.8.8"); got != nil {
			t.Fatalf("expected nil from db-less reader, got %+v", got)
		}
	})

	t.Run("cache miss with no geo data returns nil and does not cache a value", func(t *testing.T) {
		c := newCache()
		r := &Reader{}
		if got := LookupCached(ctx, r, c, "8.8.8.8"); got != nil {
			t.Fatalf("expected nil on cache miss, got %+v", got)
		}
		// A second call must also be nil: the loader returned ok=false, so no
		// positive *GeoData should have been promoted into the cache.
		if got := LookupCached(ctx, r, c, "8.8.8.8"); got != nil {
			t.Fatalf("expected nil on repeat lookup, got %+v", got)
		}
	})
}
