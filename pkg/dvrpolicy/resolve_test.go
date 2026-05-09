package dvrpolicy

import "testing"

func TestResolve(t *testing.T) {
	tiers := DefaultTiers()

	cases := []struct {
		name         string
		req          Request
		tier         Tier
		cluster      Cluster
		wantWindow   int
		wantSegment  int
		wantEntries  int
		wantFallback bool
	}{
		{
			name:        "free uses default when no request",
			req:         Request{},
			tier:        tiers["free"],
			wantWindow:  1800,
			wantSegment: 6,
			wantEntries: 300,
		},
		{
			name:        "free request 2h clamps to 1h max",
			req:         Request{DVRWindowSeconds: 2 * 3600},
			tier:        tiers["free"],
			wantWindow:  3600,
			wantSegment: 6,
			wantEntries: 600,
		},
		{
			name:        "developer 12h fits 7200 entries at 6s",
			req:         Request{DVRWindowSeconds: 12 * 3600},
			tier:        tiers["developer"],
			wantWindow:  12 * 3600,
			wantSegment: 6,
			wantEntries: 7200,
		},
		{
			name:        "production 24h scales to 12s segments",
			req:         Request{DVRWindowSeconds: 24 * 3600},
			tier:        tiers["production"],
			wantWindow:  24 * 3600,
			wantSegment: 12,
			wantEntries: 7200,
		},
		{
			name: "enterprise without cluster opt-in caps at tier max 24h with 24s segments",
			// Enterprise prefers 24s segments (DefaultSegmentDurationSeconds:24),
			// so even at 24h the tier default segment length wins over the
			// 12s baseline used by lower tiers.
			req:         Request{DVRWindowSeconds: 72 * 3600},
			tier:        tiers["enterprise"],
			wantWindow:  24 * 3600,
			wantSegment: 24,
			wantEntries: 3600,
		},
		{
			name:        "enterprise with cluster opt-in reaches 3d at 24s",
			req:         Request{DVRWindowSeconds: 72 * 3600},
			tier:        tiers["enterprise"],
			cluster:     Cluster{MaxWindowSeconds: 72 * 3600, MaxEntries: 10800},
			wantWindow:  72 * 3600,
			wantSegment: 24,
			wantEntries: 10800,
		},
		{
			name: "non-enterprise ignores cluster window extension",
			// Production cannot extend past tier max even if cluster says so;
			// AllowClusterExtension=false on production tier preset.
			req:         Request{DVRWindowSeconds: 72 * 3600},
			tier:        tiers["production"],
			cluster:     Cluster{MaxWindowSeconds: 72 * 3600},
			wantWindow:  24 * 3600,
			wantSegment: 12,
			wantEntries: 7200,
		},
		{
			name:        "platform absolute max overrides everything else",
			req:         Request{DVRWindowSeconds: 30 * 24 * 3600},
			tier:        tiers["enterprise"],
			cluster:     Cluster{MaxWindowSeconds: 30 * 24 * 3600, MaxEntries: 100000},
			wantWindow:  72 * 3600,
			wantSegment: 24,
			wantEntries: 10800,
		},
		{
			name: "max_entries clamp shrinks window to match",
			// Cluster restricts to 1000 entries; with developer's 6s segments
			// that's 6000s of window even if the request is 12h.
			req:         Request{DVRWindowSeconds: 12 * 3600},
			tier:        tiers["developer"],
			cluster:     Cluster{MaxEntries: 1000},
			wantWindow:  6000,
			wantSegment: 6,
			wantEntries: 1000,
		},
		{
			name:         "zero-everything tier falls back to 1h sane default",
			req:          Request{},
			tier:         Tier{},
			wantWindow:   3600,
			wantSegment:  6,
			wantEntries:  600,
			wantFallback: true,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Resolve(c.req, c.tier, c.cluster)
			if got.DVRWindowSeconds != c.wantWindow {
				t.Errorf("DVRWindowSeconds = %d, want %d", got.DVRWindowSeconds, c.wantWindow)
			}
			if got.SegmentDurationSeconds != c.wantSegment {
				t.Errorf("SegmentDurationSeconds = %d, want %d", got.SegmentDurationSeconds, c.wantSegment)
			}
			if got.MaxEntries != c.wantEntries {
				t.Errorf("MaxEntries = %d, want %d", got.MaxEntries, c.wantEntries)
			}
			if got.UsedDefaultFallback != c.wantFallback {
				t.Errorf("UsedDefaultFallback = %v, want %v", got.UsedDefaultFallback, c.wantFallback)
			}
		})
	}
}

func TestResolve_NeverExceedsPlatformMax(t *testing.T) {
	r := Resolve(
		Request{DVRWindowSeconds: 365 * 24 * 3600},
		Tier{
			DefaultWindowSeconds:          1 * 3600,
			MaxWindowSeconds:              365 * 24 * 3600,
			DefaultSegmentDurationSeconds: 60,
			MaxEntries:                    1_000_000,
			AllowClusterExtension:         true,
		},
		Cluster{MaxWindowSeconds: 365 * 24 * 3600, MaxEntries: 1_000_000},
	)
	if r.DVRWindowSeconds > PlatformAbsoluteMaxSeconds {
		t.Fatalf("window %d exceeds platform max %d", r.DVRWindowSeconds, PlatformAbsoluteMaxSeconds)
	}
}
