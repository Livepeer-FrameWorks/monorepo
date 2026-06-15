package database_test

import (
	"testing"

	"github.com/yugabyte/pgx/v5"
)

// TestMultiHostLoadBalanceDSNAcceptedByDriver proves the multi-host smart-driver
// DSN shape that provisioning renders (multiple contact points + load_balance +
// connect_timeout) is accepted by the exact yugabyte/pgx version's config parser
// and that the driver recognizes the params and resolves all contact points. This
// is an OFFLINE contract check (pgx.ParseConfig only); it does not open a
// connection. The live multi-host failover/balancing path is a staging drill.
func TestMultiHostLoadBalanceDSNAcceptedByDriver(t *testing.T) {
	dsn := "postgres://u:p@h1.internal:5433,h2.internal:5433,h3.internal:5433/db?sslmode=disable&load_balance=true&connect_timeout=5"

	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("yugabyte/pgx must accept the rendered multi-host load_balance DSN: %v", err)
	}
	if cfg.Host != "h1.internal" {
		t.Errorf("primary host = %q, want h1.internal", cfg.Host)
	}
	// Remaining contact points become fallbacks (1 primary + 2 fallbacks = 3).
	if len(cfg.Fallbacks) != 2 {
		t.Errorf("fallback hosts = %d, want 2 (total %d)", len(cfg.Fallbacks), 1+len(cfg.Fallbacks))
	}
	if cfg.ConnectTimeout == 0 {
		t.Errorf("connect_timeout was not parsed from DSN")
	}
}
