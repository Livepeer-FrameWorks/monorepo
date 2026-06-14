package handlers

import (
	"strings"
	"sync"
	"time"
)

const (
	playRewriteBurstTTL    = 2 * time.Second
	playRewriteRecoveryTTL = 10 * time.Minute
)

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

func rememberPlayRewrite(requested, response string) {
	requested = strings.TrimSpace(requested)
	response = strings.TrimSpace(response)
	if requested == "" || response == "" {
		return
	}
	playRewriteCache.Lock()
	playRewriteCache.entries[requested] = playRewriteCacheEntry{
		response: response,
		storedAt: time.Now(),
	}
	playRewriteCache.Unlock()
}

func cachedPlayRewrite(requested string, recovery bool) (string, bool) {
	requested = strings.TrimSpace(requested)
	if requested == "" {
		return "", false
	}
	now := time.Now()
	playRewriteCache.RLock()
	entry, ok := playRewriteCache.entries[requested]
	playRewriteCache.RUnlock()
	if !ok {
		return "", false
	}
	age := now.Sub(entry.storedAt)
	if age <= playRewriteBurstTTL {
		return entry.response, true
	}
	if recovery && age <= playRewriteRecoveryTTL {
		return entry.response, true
	}
	return "", false
}

func clearPlayRewriteCache() {
	playRewriteCache.Lock()
	playRewriteCache.entries = map[string]playRewriteCacheEntry{}
	playRewriteCache.Unlock()
}
