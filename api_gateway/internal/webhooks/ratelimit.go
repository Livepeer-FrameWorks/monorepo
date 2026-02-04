package webhooks

import (
	"sync"
	"time"
)

type rateBucket struct {
	windowStart time.Time
	count       int
	lastSeen    time.Time
}

// WebhookRateLimiter provides a simple per-key fixed-window rate limiter.
type WebhookRateLimiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	ttl     time.Duration
	buckets map[string]*rateBucket
}

// NewWebhookRateLimiter creates a per-key fixed-window limiter.
func NewWebhookRateLimiter(limit int, window, ttl time.Duration) *WebhookRateLimiter {
	if limit <= 0 {
		limit = 1
	}
	if window <= 0 {
		window = time.Minute
	}
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	return &WebhookRateLimiter{
		limit:   limit,
		window:  window,
		ttl:     ttl,
		buckets: make(map[string]*rateBucket),
	}
}

// Allow returns true if the request is permitted for the key.
func (rl *WebhookRateLimiter) Allow(key string) bool {
	now := time.Now()
	if key == "" {
		key = "unknown"
	}

	rl.mu.Lock()
	defer rl.mu.Unlock()

	for k, bucket := range rl.buckets {
		if now.Sub(bucket.lastSeen) > rl.ttl {
			delete(rl.buckets, k)
		}
	}

	bucket, ok := rl.buckets[key]
	if !ok {
		rl.buckets[key] = &rateBucket{windowStart: now, count: 1, lastSeen: now}
		return true
	}

	bucket.lastSeen = now
	if now.Sub(bucket.windowStart) >= rl.window {
		bucket.windowStart = now
		bucket.count = 1
		return true
	}

	if bucket.count >= rl.limit {
		return false
	}

	bucket.count++
	return true
}
