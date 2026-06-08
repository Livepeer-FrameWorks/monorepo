package grpc

import (
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	sharedpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/shared"
)

// TestDVRClusterPolicy pins the operator env surface that sets a per-cluster
// DVR ceiling. The contract dvrpolicy.Resolve depends on is: nil means "no
// cluster cap, tier ceilings stand", and a non-nil Cluster is returned only
// when at least one of the two knobs is positively set. Returning a zero-valued
// non-nil Cluster instead of nil would be indistinguishable downstream, but the
// nil/non-nil split is what the comment at the call site documents, so it's
// asserted here.
func TestDVRClusterPolicy(t *testing.T) {
	s := NewFoghornGRPCServer(nil, logging.NewLogger(), nil, nil, nil, nil, nil, nil)

	t.Run("both unset returns nil (tier ceilings stand)", func(t *testing.T) {
		t.Setenv("DVR_CLUSTER_MAX_WINDOW_SECONDS", "")
		t.Setenv("DVR_CLUSTER_MAX_ENTRIES", "")
		if got := s.dvrClusterPolicy(); got != nil {
			t.Fatalf("unset env must yield nil, got %+v", got)
		}
	})

	t.Run("window only", func(t *testing.T) {
		t.Setenv("DVR_CLUSTER_MAX_WINDOW_SECONDS", "600")
		t.Setenv("DVR_CLUSTER_MAX_ENTRIES", "")
		got := s.dvrClusterPolicy()
		if got == nil || got.MaxWindowSeconds != 600 || got.MaxEntries != 0 {
			t.Fatalf("window-only env = %+v, want {600,0}", got)
		}
	})

	t.Run("entries only", func(t *testing.T) {
		t.Setenv("DVR_CLUSTER_MAX_WINDOW_SECONDS", "")
		t.Setenv("DVR_CLUSTER_MAX_ENTRIES", "120")
		got := s.dvrClusterPolicy()
		if got == nil || got.MaxWindowSeconds != 0 || got.MaxEntries != 120 {
			t.Fatalf("entries-only env = %+v, want {0,120}", got)
		}
	})

	t.Run("both set", func(t *testing.T) {
		t.Setenv("DVR_CLUSTER_MAX_WINDOW_SECONDS", "3600")
		t.Setenv("DVR_CLUSTER_MAX_ENTRIES", "300")
		got := s.dvrClusterPolicy()
		if got == nil || got.MaxWindowSeconds != 3600 || got.MaxEntries != 300 {
			t.Fatalf("both env = %+v, want {3600,300}", got)
		}
	})
}

// TestResolveEffectiveDVRConfig verifies the adapter that maps a StartDVRRequest
// (tier policy from the wire + caller-requested window) and the cluster env
// policy into dvrpolicy.Resolve. The clamp/segment math itself is owned and
// tested by pkg/dvrpolicy; this test guards the wiring: that the tier proto is
// translated field-for-field, that the requested window flows in, and that the
// cluster env ceiling is actually consulted. A mapping bug here would let a tier
// or cluster cap silently not bite.
func TestResolveEffectiveDVRConfig(t *testing.T) {
	s := NewFoghornGRPCServer(nil, logging.NewLogger(), nil, nil, nil, nil, nil, nil)

	tierPolicy := &sharedpb.DVRPolicy{
		DefaultWindowSeconds:          1800,
		MaxWindowSeconds:              3600,
		DefaultSegmentDurationSeconds: 6,
		MaxEntries:                    600,
	}

	t.Run("request within tier max is honored", func(t *testing.T) {
		t.Setenv("DVR_CLUSTER_MAX_WINDOW_SECONDS", "")
		t.Setenv("DVR_CLUSTER_MAX_ENTRIES", "")
		req := &sharedpb.StartDVRRequest{DvrPolicy: tierPolicy, DvrWindowSeconds: i32(1200)}
		eff := s.resolveEffectiveDVRConfig(req)
		if eff.DVRWindowSeconds != 1200 {
			t.Fatalf("window = %d, want honored 1200", eff.DVRWindowSeconds)
		}
		if eff.UsedDefaultFallback {
			t.Fatal("a valid tier must not trip the misconfig fallback")
		}
	})

	t.Run("request above tier max is clamped to tier max", func(t *testing.T) {
		t.Setenv("DVR_CLUSTER_MAX_WINDOW_SECONDS", "")
		t.Setenv("DVR_CLUSTER_MAX_ENTRIES", "")
		req := &sharedpb.StartDVRRequest{DvrPolicy: tierPolicy, DvrWindowSeconds: i32(999999)}
		eff := s.resolveEffectiveDVRConfig(req)
		if eff.DVRWindowSeconds != 3600 {
			t.Fatalf("window = %d, want clamped to tier max 3600", eff.DVRWindowSeconds)
		}
	})

	t.Run("cluster env ceiling clamps below tier max", func(t *testing.T) {
		t.Setenv("DVR_CLUSTER_MAX_WINDOW_SECONDS", "600")
		t.Setenv("DVR_CLUSTER_MAX_ENTRIES", "")
		req := &sharedpb.StartDVRRequest{DvrPolicy: tierPolicy, DvrWindowSeconds: i32(3000)}
		eff := s.resolveEffectiveDVRConfig(req)
		if eff.DVRWindowSeconds != 600 {
			t.Fatalf("window = %d, want cluster-clamped 600", eff.DVRWindowSeconds)
		}
	})

	t.Run("missing tier policy trips the platform fallback", func(t *testing.T) {
		t.Setenv("DVR_CLUSTER_MAX_WINDOW_SECONDS", "")
		t.Setenv("DVR_CLUSTER_MAX_ENTRIES", "")
		// No DvrPolicy and no requested window: a live recording still has to
		// pick a window, so the resolver emits the 1h platform fallback.
		req := &sharedpb.StartDVRRequest{}
		eff := s.resolveEffectiveDVRConfig(req)
		if !eff.UsedDefaultFallback {
			t.Fatal("absent tier + no request must set UsedDefaultFallback")
		}
		if eff.DVRWindowSeconds != 3600 {
			t.Fatalf("fallback window = %d, want 3600", eff.DVRWindowSeconds)
		}
	})
}
