package graph

import (
	"context"
	"errors"
	"testing"

	"frameworks/api_gateway/internal/middleware"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
)

// An API token without analytics:read gets a FORBIDDEN field error for
// Stream.metrics before any analytics call; the resolver has no clients, so
// reaching Periscope would panic.
func TestStreamMetricsRequiresAnalyticsReadForAPITokens(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyTenantID, "tenant-1")
	ctx = context.WithValue(ctx, ctxkeys.KeyAuthType, "api_token")
	ctx = context.WithValue(ctx, ctxkeys.KeyPermissions, []string{"streams:read", "streams:write"})

	metrics, err := (&streamResolver{}).Metrics(ctx, &commodorepb.Stream{StreamId: "stream-1"})
	if metrics != nil || !errors.Is(err, middleware.ErrForbidden) {
		t.Fatalf("Metrics() = (%v, %v), want (nil, ErrForbidden)", metrics, err)
	}
}
