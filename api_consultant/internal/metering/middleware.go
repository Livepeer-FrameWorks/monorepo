package metering

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"time"

	"frameworks/api_consultant/internal/skipper"
	"frameworks/pkg/logging"
	pb "frameworks/pkg/proto"

	"github.com/gin-gonic/gin"
)

type AccessMiddlewareConfig struct {
	Purser            BillingClient
	RequiredTierLevel int
	RateLimiter       *RateLimiter
	Tracker           *UsageTracker
	Logger            logging.Logger
}

type BillingClient interface {
	GetBillingStatus(ctx context.Context, tenantID string) (*pb.BillingStatusResponse, error)
}

func AccessMiddleware(cfg AccessMiddlewareConfig) gin.HandlerFunc {
	billingRequired := cfg.RequiredTierLevel > 0
	requiredTier := cfg.RequiredTierLevel
	if billingRequired && requiredTier <= 0 {
		requiredTier = 1
	}

	return func(c *gin.Context) {
		tenantID := skipper.GetTenantID(c.Request.Context())
		if tenantID == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "tenant_id missing"})
			c.Abort()
			return
		}

		if billingRequired {
			if cfg.Purser == nil {
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": "billing service unavailable"})
				c.Abort()
				return
			}
			status, err := cfg.Purser.GetBillingStatus(c.Request.Context(), tenantID)
			if err != nil {
				if cfg.Logger != nil {
					cfg.Logger.WithError(err).WithField("tenant_id", tenantID).Warn("Failed to fetch billing status for Skipper")
				}
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": "billing status unavailable"})
				c.Abort()
				return
			}
			tier := status.GetTier()
			if tier == nil || int(tier.TierLevel) < requiredTier {
				message := "Skipper access requires a premium subscription tier. Please upgrade to continue."
				c.JSON(http.StatusForbidden, gin.H{"error": message})
				c.Abort()
				return
			}
		}

		if cfg.RateLimiter != nil {
			allowed, remaining, resetSeconds := cfg.RateLimiter.Allow(tenantID)
			if !allowed {
				c.JSON(http.StatusTooManyRequests, gin.H{
					"error":       "Skipper chat rate limit exceeded. Try again later.",
					"retry_after": resetSeconds,
				})
				c.Abort()
				return
			}
			c.Header("X-RateLimit-Remaining", strconv.Itoa(remaining))
			c.Header("X-RateLimit-Reset", strconv.Itoa(resetSeconds))
		}

		ctx := WithContext(c.Request.Context(), &Context{
			TenantID: tenantID,
			UserID:   skipper.GetUserID(c.Request.Context()),
			Tracker:  cfg.Tracker,
		})
		c.Request = c.Request.WithContext(ctx)
		c.Next()
	}
}

type RateLimiter struct {
	defaultLimit int
	overrides    map[string]int
	window       time.Duration
	mu           sync.Mutex
	usage        map[string]*rateUsage
}

type rateUsage struct {
	windowStart time.Time
	count       int
}

func NewRateLimiter(defaultLimit int, overrides map[string]int) *RateLimiter {
	if overrides == nil {
		overrides = map[string]int{}
	}
	return &RateLimiter{
		defaultLimit: defaultLimit,
		overrides:    overrides,
		window:       time.Hour,
		usage:        make(map[string]*rateUsage),
	}
}

func (rl *RateLimiter) Allow(tenantID string) (bool, int, int) {
	if rl == nil || tenantID == "" {
		return true, 0, 0
	}
	// overrides and defaultLimit are immutable after construction — safe to read without lock.
	limit := rl.defaultLimit
	if override, ok := rl.overrides[tenantID]; ok {
		limit = override
	}
	if limit <= 0 {
		return true, 0, 0
	}

	now := time.Now()

	rl.mu.Lock()
	defer rl.mu.Unlock()

	entry, ok := rl.usage[tenantID]
	if !ok || now.Sub(entry.windowStart) >= rl.window {
		entry = &rateUsage{windowStart: now, count: 0}
		rl.usage[tenantID] = entry
	}

	if entry.count >= limit {
		resetSeconds := int(entry.windowStart.Add(rl.window).Sub(now).Seconds())
		if resetSeconds < 0 {
			resetSeconds = 0
		}
		return false, 0, resetSeconds
	}

	entry.count++
	remaining := limit - entry.count
	resetSeconds := int(entry.windowStart.Add(rl.window).Sub(now).Seconds())
	if resetSeconds < 0 {
		resetSeconds = 0
	}
	return true, remaining, resetSeconds
}

func (rl *RateLimiter) Cleanup() {
	if rl == nil {
		return
	}
	rl.mu.Lock()
	defer rl.mu.Unlock()
	now := time.Now()
	for id, entry := range rl.usage {
		if now.Sub(entry.windowStart) >= 2*rl.window {
			delete(rl.usage, id)
		}
	}
}

func (rl *RateLimiter) StartCleanup(ctx context.Context) {
	if rl == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(rl.window)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				rl.Cleanup()
			}
		}
	}()
}
