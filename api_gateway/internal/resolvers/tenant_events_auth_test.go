package resolvers

import (
	"context"
	"errors"
	"testing"

	"frameworks/api_gateway/internal/middleware"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/events"
)

func TestTenantEventsRequireCategoryScope(t *testing.T) {
	ctx := context.WithValue(context.Background(), ctxkeys.KeyAuthType, "api_token")
	ctx = context.WithValue(ctx, ctxkeys.KeyPermissions, []string{"streams:read"})
	f, err := authorizeTenantEvents(ctx, TenantEventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range []string{"stream.live", "clip.ready", "upload.ready"} {
		if _, ok := f.Types[event]; !ok {
			t.Fatalf("missing permitted event %s", event)
		}
	}
	for _, event := range []string{"billing.invoice_paid", "api_token.created"} {
		if _, ok := f.Types[event]; ok {
			t.Fatalf("disclosed forbidden event %s", event)
		}
		if _, err := authorizeTenantEvents(ctx, TenantEventFilter{Types: map[string]struct{}{event: {}}}); !errors.Is(err, middleware.ErrForbidden) {
			t.Fatalf("explicit forbidden type %s: %v", event, err)
		}
	}
	ctx = context.WithValue(ctx, ctxkeys.KeyPermissions, []string{"analytics:read"})
	if _, err := authorizeTenantEvents(ctx, TenantEventFilter{}); !errors.Is(err, middleware.ErrForbidden) {
		t.Fatalf("analytics token subscribed: %v", err)
	}
}

func TestEveryPublicTenantEventHasReadScope(t *testing.T) {
	for _, spec := range events.Specs() {
		if spec.Public() && tenantEventScope(spec.Type) == "" {
			t.Errorf("public event %s lacks a read scope", spec.Type)
		}
	}
}
