package handlers

import (
	"strconv"
	"testing"
)

// A burst of unique playback IDs within the recovery TTL (all fresh, none
// expired) must not grow the cache past its cap — the sweep-then-evict-oldest
// path keeps it hard-bounded.
func TestPlayRewriteCacheHardBound(t *testing.T) {
	clearPlayRewriteCache()
	t.Cleanup(clearPlayRewriteCache)

	for i := 0; i < playRewriteCacheMaxEntries+512; i++ {
		rememberPlayRewrite("stream-"+strconv.Itoa(i), "resp-"+strconv.Itoa(i))
	}

	playRewriteCache.RLock()
	n := len(playRewriteCache.entries)
	playRewriteCache.RUnlock()
	if n > playRewriteCacheMaxEntries {
		t.Fatalf("cache exceeded hard cap: %d > %d", n, playRewriteCacheMaxEntries)
	}
}
