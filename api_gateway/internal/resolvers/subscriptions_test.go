package resolvers

import (
	"context"
	"strings"
	"testing"

	"frameworks/pkg/logging"
)

func TestSubscriptionAuthRequired(t *testing.T) {
	resolver := &Resolver{
		Logger: logging.NewLogger(),
	}

	_, err := resolver.DoStreamUpdates(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "authentication required for stream subscriptions") {
		t.Fatalf("expected auth error for stream subscriptions, got %v", err)
	}

	_, err = resolver.DoAnalyticsUpdates(context.Background(), "")
	if err == nil || !strings.Contains(err.Error(), "authentication required for analytics subscriptions") {
		t.Fatalf("expected auth error for analytics subscriptions, got %v", err)
	}
}
