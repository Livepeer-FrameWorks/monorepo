package handlers

import (
	"testing"
	"time"
)

// Foghorn advertises SIGHUP env reload. Settings frozen at construction would
// make that silently untrue for the ingest limits, so a changed environment
// must take effect on the next decision.
func TestIngestRateLimiterPicksUpEnvReload(t *testing.T) {
	prev := ingestLimiter
	ingestLimiter = newIngestRateLimiter()
	t.Cleanup(func() { ingestLimiter = prev })

	t.Setenv("INGEST_RESOLVE_BURST", "2")
	t.Setenv("INGEST_RESOLVE_RATE_PER_MIN", "1")

	now := time.Now()
	if allowed, _ := ingestLimiter.allow("198.51.100.1", now); !allowed {
		t.Fatal("first request within the reloaded burst must be allowed")
	}
	if allowed, _ := ingestLimiter.allow("198.51.100.1", now); !allowed {
		t.Fatal("second request within the reloaded burst must be allowed")
	}
	if allowed, _ := ingestLimiter.allow("198.51.100.1", now); allowed {
		t.Fatal("third request must exceed the reloaded burst of 2")
	}

	// Widening the limit mid-process must also apply.
	t.Setenv("INGEST_RESOLVE_BURST", "50")
	if allowed, _ := ingestLimiter.allow("198.51.100.2", now); !allowed {
		t.Fatal("a fresh caller must be allowed under the widened burst")
	}
	if ingestLimiter.burst != 50 {
		t.Fatalf("burst not reloaded: got %v want 50", ingestLimiter.burst)
	}
}

func TestIngestRateLimiterClampsExistingAllowanceWhenBurstShrinks(t *testing.T) {
	t.Setenv("INGEST_RESOLVE_BURST", "5")
	t.Setenv("INGEST_RESOLVE_RATE_PER_MIN", "1")
	limiter := newIngestRateLimiter()
	now := time.Now()
	if allowed, _ := limiter.allow("198.51.100.1", now); !allowed {
		t.Fatal("initial request must be allowed")
	}

	t.Setenv("INGEST_RESOLVE_BURST", "2")
	for i := 0; i < 2; i++ {
		if allowed, _ := limiter.allow("198.51.100.1", now); !allowed {
			t.Fatalf("request %d within the lowered allowance was refused", i+1)
		}
	}
	if allowed, _ := limiter.allow("198.51.100.1", now); allowed {
		t.Fatal("existing bucket retained tokens above the lowered burst")
	}
}

// The bucket map is capped so a flood of distinct source addresses cannot grow
// it without bound; eviction is least-recently-used.
func TestIngestRateLimiterEvictsAtCap(t *testing.T) {
	prev := ingestLimiter
	t.Setenv("INGEST_RESOLVE_MAX_BUCKETS", "3")
	ingestLimiter = newIngestRateLimiter()
	t.Cleanup(func() { ingestLimiter = prev })

	now := time.Now()
	for _, ip := range []string{"198.51.100.1", "198.51.100.2", "198.51.100.3", "198.51.100.4"} {
		ingestLimiter.allow(ip, now)
	}

	ingestLimiter.mu.Lock()
	size := len(ingestLimiter.buckets)
	_, oldestStillTracked := ingestLimiter.buckets["198.51.100.1"]
	ingestLimiter.mu.Unlock()

	if size > 3 {
		t.Fatalf("bucket map exceeded the cap: %d", size)
	}
	if oldestStillTracked {
		t.Error("least-recently-used bucket should have been evicted first")
	}
}

func TestIngestRateLimiterEvictsIdleBuckets(t *testing.T) {
	prev := ingestLimiter
	ingestLimiter = newIngestRateLimiter()
	t.Cleanup(func() { ingestLimiter = prev })

	start := time.Now()
	ingestLimiter.allow("198.51.100.1", start)
	ingestLimiter.evictIdle(start.Add(time.Hour), 10*time.Minute)

	ingestLimiter.mu.Lock()
	size := len(ingestLimiter.buckets)
	ingestLimiter.mu.Unlock()
	if size != 0 {
		t.Fatalf("idle bucket not evicted: %d remain", size)
	}
}
