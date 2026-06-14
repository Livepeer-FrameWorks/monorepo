package handlers

import (
	"strings"
	"sync"
	"time"
)

// playRewriteRecoveryTTL bounds how long a previously Foghorn-approved
// resolution may be replayed AFTER Foghorn becomes unreachable. It is
// deliberately short: a recovery hit bypasses Foghorn's per-viewer billing
// enforcement, viewer accounting, and analytics, and could briefly serve a
// stream whose owner was suspended in the meantime. Kept just long enough to
// bridge a Foghorn restart / transient control-stream drop, not to make the
// edge an authority. Reachable-Foghorn requests never touch this cache.
const playRewriteRecoveryTTL = 30 * time.Second

// playRewriteCacheMaxEntries caps the map so a churn of distinct playback IDs
// can't grow it unbounded; once exceeded, expired entries are swept on write.
const playRewriteCacheMaxEntries = 4096

type playRewriteCacheEntry struct {
	response string
	storedAt time.Time
}

var playRewriteCache = struct {
	sync.RWMutex
	entries map[string]playRewriteCacheEntry
}{
	entries: map[string]playRewriteCacheEntry{},
}

// rememberPlayRewrite records a successful Foghorn resolution so it can be
// replayed as a last resort if Foghorn later becomes unreachable.
func rememberPlayRewrite(requested, response string) {
	requested = strings.TrimSpace(requested)
	response = strings.TrimSpace(response)
	if requested == "" || response == "" {
		return
	}
	now := time.Now()
	playRewriteCache.Lock()
	if _, exists := playRewriteCache.entries[requested]; !exists {
		// Enforce a hard bound: sweep expired entries first, then, if a burst
		// of unique playback IDs still has us at capacity, evict the oldest so
		// the map can never grow past the cap.
		if len(playRewriteCache.entries) >= playRewriteCacheMaxEntries {
			for k, e := range playRewriteCache.entries {
				if now.Sub(e.storedAt) > playRewriteRecoveryTTL {
					delete(playRewriteCache.entries, k)
				}
			}
			for len(playRewriteCache.entries) >= playRewriteCacheMaxEntries {
				oldestKey, oldestAt := "", now
				for k, e := range playRewriteCache.entries {
					if oldestKey == "" || e.storedAt.Before(oldestAt) {
						oldestKey, oldestAt = k, e.storedAt
					}
				}
				if oldestKey == "" {
					break
				}
				delete(playRewriteCache.entries, oldestKey)
			}
		}
	}
	playRewriteCache.entries[requested] = playRewriteCacheEntry{
		response: response,
		storedAt: now,
	}
	playRewriteCache.Unlock()
}

// cachedPlayRewrite returns a previously Foghorn-approved resolution if one is
// still within the recovery window. Callers must only consult it on the
// Foghorn-unreachable path.
func cachedPlayRewrite(requested string) (string, bool) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return "", false
	}
	playRewriteCache.RLock()
	entry, ok := playRewriteCache.entries[requested]
	playRewriteCache.RUnlock()
	if !ok {
		return "", false
	}
	if time.Since(entry.storedAt) <= playRewriteRecoveryTTL {
		return entry.response, true
	}
	return "", false
}

func clearPlayRewriteCache() {
	playRewriteCache.Lock()
	playRewriteCache.entries = map[string]playRewriteCacheEntry{}
	playRewriteCache.Unlock()
}
