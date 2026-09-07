package main

import (
	"testing"

	"frameworks/api_balancing/internal/federation"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

func TestFederationPoolCoversBulkListBudget(t *testing.T) {
	cfg := federationFoghornPoolConfig("service-token", logging.NewLogger(), false, "/ca.pem", "foghorn.internal")
	if cfg.Timeout != federation.BulkListTimeout {
		t.Fatalf("federation pool timeout = %s, want bulk-list budget %s", cfg.Timeout, federation.BulkListTimeout)
	}
	if cfg.ServiceToken != "service-token" || cfg.CACertFile != "/ca.pem" || cfg.ServerName != "foghorn.internal" {
		t.Fatalf("federation pool lost production identity or TLS config: %#v", cfg)
	}
}
