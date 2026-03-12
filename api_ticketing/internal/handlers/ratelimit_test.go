package handlers

import (
	"testing"
	"time"
)

func TestNewWebhookRateLimiter_Defaults(t *testing.T) {
	rl := NewWebhookRateLimiter(0, 0, 0)
	if rl.limit != 1 {
		t.Fatalf("expected limit 1 (clamped), got %d", rl.limit)
	}
	if rl.window != time.Minute {
		t.Fatalf("expected 1m window, got %v", rl.window)
	}
	if rl.ttl != 10*time.Minute {
		t.Fatalf("expected 10m ttl, got %v", rl.ttl)
	}
}

func TestAllow_UnderLimit(t *testing.T) {
	rl := NewWebhookRateLimiter(5, time.Minute, time.Hour)
	for range 5 {
		if !rl.Allow("ip-1") {
			t.Fatal("should allow under limit")
		}
	}
}

func TestAllow_AtLimit(t *testing.T) {
	rl := NewWebhookRateLimiter(3, time.Minute, time.Hour)
	for range 3 {
		rl.Allow("ip-1")
	}
	if rl.Allow("ip-1") {
		t.Fatal("should reject at limit")
	}
}

func TestAllow_DifferentKeys(t *testing.T) {
	rl := NewWebhookRateLimiter(1, time.Minute, time.Hour)
	if !rl.Allow("ip-1") {
		t.Fatal("ip-1 should be allowed")
	}
	if rl.Allow("ip-1") {
		t.Fatal("ip-1 should be rejected (at limit)")
	}
	if !rl.Allow("ip-2") {
		t.Fatal("ip-2 should be allowed (independent key)")
	}
}

func TestAllow_WindowReset(t *testing.T) {
	rl := NewWebhookRateLimiter(1, 1*time.Millisecond, time.Hour)
	if !rl.Allow("ip-1") {
		t.Fatal("first should be allowed")
	}
	if rl.Allow("ip-1") {
		t.Fatal("second should be rejected")
	}
	time.Sleep(5 * time.Millisecond)
	if !rl.Allow("ip-1") {
		t.Fatal("should allow after window reset")
	}
}

func TestAllow_EmptyKey(t *testing.T) {
	rl := NewWebhookRateLimiter(1, time.Minute, time.Hour)
	if !rl.Allow("") {
		t.Fatal("empty key should be treated as 'unknown' and allowed")
	}
	if rl.Allow("") {
		t.Fatal("second empty key should be rejected")
	}
}

func TestAllow_TTLCleanup(t *testing.T) {
	rl := NewWebhookRateLimiter(10, time.Minute, 1*time.Millisecond)
	rl.Allow("old-ip")
	time.Sleep(5 * time.Millisecond)
	// Trigger cleanup by calling Allow with a different key
	rl.Allow("new-ip")

	rl.mu.Lock()
	_, exists := rl.buckets["old-ip"]
	rl.mu.Unlock()
	if exists {
		t.Fatal("old bucket should have been cleaned up")
	}
}
