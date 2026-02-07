package metering

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"frameworks/api_consultant/internal/skipper"
	pb "frameworks/pkg/proto"

	"github.com/gin-gonic/gin"
)

type fakeBillingClient struct {
	status *pb.BillingStatusResponse
	err    error
}

func (f *fakeBillingClient) GetBillingStatus(ctx context.Context, tenantID string) (*pb.BillingStatusResponse, error) {
	_ = ctx
	_ = tenantID
	return f.status, f.err
}

func TestAccessMiddlewareRejectsNonPremium(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(skipper.WithTenantID(c.Request.Context(), "tenant-a"))
		c.Next()
	})

	router.Use(AccessMiddleware(AccessMiddlewareConfig{
		Purser: &fakeBillingClient{
			status: &pb.BillingStatusResponse{
				Tier: &pb.BillingTier{TierLevel: 1},
			},
		},
		RequiredTierLevel: 3,
	}))
	router.GET("/api/skipper/chat", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/skipper/chat", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rec.Code)
	}
}

func TestAccessMiddlewareRateLimits(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(skipper.WithTenantID(c.Request.Context(), "tenant-a"))
		c.Next()
	})

	router.Use(AccessMiddleware(AccessMiddlewareConfig{
		Purser: &fakeBillingClient{
			status: &pb.BillingStatusResponse{
				Tier: &pb.BillingTier{TierLevel: 3},
			},
		},
		RequiredTierLevel: 3,
		RateLimiter:       NewRateLimiter(1, nil),
	}))
	router.GET("/api/skipper/chat", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/skipper/chat", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", rec.Code)
	}
}

func TestAccessMiddlewareFailsClosedWhenPurserNilButTierRequired(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(skipper.WithTenantID(c.Request.Context(), "tenant-a"))
		c.Next()
	})

	router.Use(AccessMiddleware(AccessMiddlewareConfig{
		Purser:            nil,
		RequiredTierLevel: 3,
	}))
	router.GET("/api/skipper/chat", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/skipper/chat", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 when Purser nil but tier required, got %d", rec.Code)
	}
}

func TestAccessMiddlewareSkipsBillingWhenTierNotRequired(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Request = c.Request.WithContext(skipper.WithTenantID(c.Request.Context(), "tenant-a"))
		c.Next()
	})

	router.Use(AccessMiddleware(AccessMiddlewareConfig{
		Purser:            nil,
		RequiredTierLevel: 0,
	}))
	router.GET("/api/skipper/chat", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/api/skipper/chat", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 when billing disabled (tier=0, Purser=nil), got %d", rec.Code)
	}
}
