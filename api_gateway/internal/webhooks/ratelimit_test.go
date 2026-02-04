package webhooks

import (
	"testing"
	"time"
)

func TestNewWebhookRateLimiter(t *testing.T) {
	tests := []struct {
		name   string
		limit  int
		window time.Duration
		ttl    time.Duration
		want   WebhookRateLimiter
	}{
		{
			name:   "valid parameters",
			limit:  5,
			window: 2 * time.Second,
			ttl:    4 * time.Second,
			want: WebhookRateLimiter{
				limit:  5,
				window: 2 * time.Second,
				ttl:    4 * time.Second,
			},
		},
		{
			name:   "defaults for zero values",
			limit:  0,
			window: 0,
			ttl:    0,
			want: WebhookRateLimiter{
				limit:  1,
				window: time.Minute,
				ttl:    10 * time.Minute,
			},
		},
		{
			name:   "defaults for negative values",
			limit:  -3,
			window: -1 * time.Second,
			ttl:    -1 * time.Minute,
			want: WebhookRateLimiter{
				limit:  1,
				window: time.Minute,
				ttl:    10 * time.Minute,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rl := NewWebhookRateLimiter(tt.limit, tt.window, tt.ttl)

			if rl.limit != tt.want.limit {
				t.Fatalf("limit mismatch: got %d want %d", rl.limit, tt.want.limit)
			}
			if rl.window != tt.want.window {
				t.Fatalf("window mismatch: got %s want %s", rl.window, tt.want.window)
			}
			if rl.ttl != tt.want.ttl {
				t.Fatalf("ttl mismatch: got %s want %s", rl.ttl, tt.want.ttl)
			}
			if rl.buckets == nil {
				t.Fatal("expected buckets map to be initialized")
			}
		})
	}
}

func TestWebhookRateLimiterAllow(t *testing.T) {
	tests := []struct {
		name string
		run  func(t *testing.T)
	}{
		{
			name: "first request for new key allowed",
			run: func(t *testing.T) {
				rl := NewWebhookRateLimiter(1, time.Minute, time.Minute)
				if !rl.Allow("alpha") {
					t.Fatal("expected first request to be allowed")
				}
			},
		},
		{
			name: "requests within limit allowed",
			run: func(t *testing.T) {
				rl := NewWebhookRateLimiter(2, time.Minute, time.Minute)
				if !rl.Allow("beta") {
					t.Fatal("expected first request to be allowed")
				}
				if !rl.Allow("beta") {
					t.Fatal("expected second request to be allowed")
				}
			},
		},
		{
			name: "request exceeding limit denied",
			run: func(t *testing.T) {
				rl := NewWebhookRateLimiter(2, time.Minute, time.Minute)
				_ = rl.Allow("gamma")
				_ = rl.Allow("gamma")
				if rl.Allow("gamma") {
					t.Fatal("expected request exceeding limit to be denied")
				}
			},
		},
		{
			name: "empty key normalized to unknown",
			run: func(t *testing.T) {
				rl := NewWebhookRateLimiter(1, time.Minute, time.Minute)
				if !rl.Allow("") {
					t.Fatal("expected empty key to be allowed on first request")
				}
				if rl.Allow("unknown") {
					t.Fatal("expected unknown key to share bucket with empty key")
				}
			},
		},
		{
			name: "different keys have independent limits",
			run: func(t *testing.T) {
				rl := NewWebhookRateLimiter(1, time.Minute, time.Minute)
				if !rl.Allow("delta") {
					t.Fatal("expected first request for delta to be allowed")
				}
				if rl.Allow("delta") {
					t.Fatal("expected second request for delta to be denied")
				}
				if !rl.Allow("epsilon") {
					t.Fatal("expected first request for epsilon to be allowed")
				}
			},
		},
		{
			name: "window reset allows new requests",
			run: func(t *testing.T) {
				window := 10 * time.Millisecond
				rl := NewWebhookRateLimiter(1, window, time.Minute)
				if !rl.Allow("zeta") {
					t.Fatal("expected first request to be allowed")
				}
				if rl.Allow("zeta") {
					t.Fatal("expected second request to be denied within window")
				}
				time.Sleep(2 * window)
				if !rl.Allow("zeta") {
					t.Fatal("expected request to be allowed after window reset")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, tt.run)
	}
}
