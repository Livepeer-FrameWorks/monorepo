package capabilities

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

func TestSectionCacheServesFreshReloadsExpiredAndFallsBackToLastReading(t *testing.T) {
	clock := &fakeClock{now: time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)}
	cache := newSectionCache[int](SectionTTL, clock.Now)
	ctx := context.Background()
	calls := 0
	source := func(value int, err error) func(context.Context) (int, error) {
		return func(context.Context) (int, error) {
			calls++
			return value, err
		}
	}
	outage := errors.New("purser unavailable")

	if _, err := cache.Get(ctx, "tenant-a", source(0, outage)); !errors.Is(err, outage) {
		t.Fatalf("uncached outage error = %v, want the source error", err)
	}

	first, err := cache.Get(ctx, "tenant-a", source(7, nil))
	if err != nil || first.Value != 7 || first.Stale || !first.ObservedAt.Equal(clock.now) {
		t.Fatalf("first read = %+v, %v", first, err)
	}
	firstObserved := first.ObservedAt

	clock.now = clock.now.Add(SectionTTL - time.Second)
	hit, err := cache.Get(ctx, "tenant-a", source(8, nil))
	if err != nil || hit.Value != 7 || calls != 2 || !hit.ObservedAt.Equal(firstObserved) {
		t.Fatalf("fresh hit = %+v, %v, calls %d", hit, err, calls)
	}

	if other, otherErr := cache.Get(ctx, "tenant-b", source(0, outage)); !errors.Is(otherErr, outage) || other.Value != 0 {
		t.Fatalf("another tenant read tenant-a's section: %+v, %v", other, otherErr)
	}

	clock.now = clock.now.Add(2 * time.Second)
	stale, err := cache.Get(ctx, "tenant-a", source(0, outage))
	if err != nil || stale.Value != 7 || !stale.Stale || !stale.ObservedAt.Equal(firstObserved) {
		t.Fatalf("outage after expiry = %+v, %v; want last reading with its original observedAt", stale, err)
	}

	reloaded, err := cache.Get(ctx, "tenant-a", source(9, nil))
	if err != nil || reloaded.Value != 9 || reloaded.Stale || !reloaded.ObservedAt.Equal(clock.now) {
		t.Fatalf("reload after recovery = %+v, %v", reloaded, err)
	}
}

// A source that refuses the caller is an answer about access, not an outage:
// the cached reading is dropped instead of being served as stale, including
// to a later call that meets a plain outage.
func TestSectionCacheNeverServesAReadingAfterTheSourceDeniesAccess(t *testing.T) {
	for _, code := range []codes.Code{codes.PermissionDenied, codes.Unauthenticated} {
		t.Run(code.String(), func(t *testing.T) {
			clock := &fakeClock{now: time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC)}
			cache := newSectionCache[int](SectionTTL, clock.Now)
			ctx := context.Background()
			if _, err := cache.Get(ctx, "tenant-a", func(context.Context) (int, error) { return 7, nil }); err != nil {
				t.Fatal(err)
			}

			clock.now = clock.now.Add(SectionTTL + time.Second)
			denial := status.Error(code, "access denied")
			got, err := cache.Get(ctx, "tenant-a", func(context.Context) (int, error) { return 0, denial })
			if !errors.Is(err, denial) || got.Value != 0 || got.Stale {
				t.Fatalf("%s after a cached read = %+v, %v; want the denial and no cached value", code, got, err)
			}

			outage := errors.New("quartermaster unavailable")
			got, err = cache.Get(ctx, "tenant-a", func(context.Context) (int, error) { return 0, outage })
			if !errors.Is(err, outage) || got.Value != 0 || got.Stale {
				t.Fatalf("outage after a denial = %+v, %v; want the outage error, the denied reading must be gone", got, err)
			}
		})
	}
}
