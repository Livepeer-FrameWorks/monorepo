package grpc

import (
	"context"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
)

// approxDaysFromNow asserts a resolved retention timestamp lands ~days*24h in
// the future, tolerating test-execution slack.
func approxDaysFromNow(t *testing.T, got time.Time, days int32) {
	t.Helper()
	want := time.Now().UTC().Add(time.Duration(days) * 24 * time.Hour)
	if diff := got.Sub(want); diff > time.Minute || diff < -time.Minute {
		t.Fatalf("retention_until = %v, want ~%v (off by %v)", got, want, diff)
	}
}

// TestResolveArtifactInitialRetention_CommodoreTrusted covers the path where
// the upstream Commodore call already ran the full per-class retention cascade
// and handed Foghorn a concrete day count. Foghorn must trust it verbatim and
// NOT re-resolve against Purser: a positive count becomes that horizon, and a
// non-positive count (0 or negative) means "never auto-expire" → NULL
// retention_until (Valid=false). Getting the 0 case wrong would silently expire
// artifacts a tenant intended to keep forever.
func TestResolveArtifactInitialRetention_CommodoreTrusted(t *testing.T) {
	ctx := context.Background()
	log := logging.NewLogger()

	t.Run("positive commodore days sets that horizon", func(t *testing.T) {
		days := int32(45)
		// Purser is nil; the commodore-trusted branch must not touch it.
		got := resolveArtifactInitialRetention(ctx, nil, "tenant-1", &days, 30, log)
		if !got.Valid {
			t.Fatal("positive commodore days must yield a valid retention_until")
		}
		approxDaysFromNow(t, got.Time, 45)
	})

	t.Run("zero commodore days means keep forever (NULL)", func(t *testing.T) {
		zero := int32(0)
		got := resolveArtifactInitialRetention(ctx, nil, "tenant-1", &zero, 30, log)
		if got.Valid {
			t.Fatalf("zero commodore days must yield NULL retention_until, got %v", got.Time)
		}
	})

	t.Run("negative commodore days also means keep forever", func(t *testing.T) {
		neg := int32(-5)
		got := resolveArtifactInitialRetention(ctx, nil, "tenant-1", &neg, 30, log)
		if got.Valid {
			t.Fatalf("negative commodore days must yield NULL retention_until, got %v", got.Time)
		}
	})
}

// TestResolveArtifactInitialRetention_LocalFallback covers the direct-Foghorn
// path (commodoreDays == nil): no Commodore cascade ran, so Foghorn resolves
// locally. With a nil Purser client the tier cap is unavailable (cap = 0), so
// the system default governs: a positive default sets that horizon, and a
// non-positive default with no cap means "never expire" (NULL). This is the
// path internal retries / tests hit, and it must degrade safely rather than
// defaulting artifacts to immediate expiry.
func TestResolveArtifactInitialRetention_LocalFallback(t *testing.T) {
	ctx := context.Background()
	log := logging.NewLogger()

	t.Run("system default applies when no cap available", func(t *testing.T) {
		got := resolveArtifactInitialRetention(ctx, nil, "tenant-1", nil, 30, log)
		if !got.Valid {
			t.Fatal("positive system default must yield a valid retention_until")
		}
		approxDaysFromNow(t, got.Time, 30)
	})

	t.Run("no default and no cap means keep forever (NULL)", func(t *testing.T) {
		got := resolveArtifactInitialRetention(ctx, nil, "tenant-1", nil, 0, log)
		if got.Valid {
			t.Fatalf("no default + no cap must yield NULL retention_until, got %v", got.Time)
		}
	})

	t.Run("empty tenant skips purser lookup and uses system default", func(t *testing.T) {
		// tenantID == "" short-circuits the billing lookup guard; behaviour
		// must match the nil-purser fallback.
		got := resolveArtifactInitialRetention(ctx, nil, "", nil, 14, log)
		if !got.Valid {
			t.Fatal("empty tenant with positive default must still set a horizon")
		}
		approxDaysFromNow(t, got.Time, 14)
	})
}
