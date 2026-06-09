package dvrpolicy

import "testing"

func TestResolve_ClusterWindowClampsBelowTierMax(t *testing.T) {
	got := Resolve(
		Request{DVRWindowSeconds: 12 * 3600},
		Tier{
			DefaultWindowSeconds:          12 * 3600,
			MaxWindowSeconds:              12 * 3600,
			DefaultSegmentDurationSeconds: 6,
			MaxEntries:                    7200,
		},
		Cluster{MaxWindowSeconds: 1800},
	)
	if got.DVRWindowSeconds != 1800 {
		t.Errorf("DVRWindowSeconds = %d, want 1800 (cluster window is binding cap)", got.DVRWindowSeconds)
	}
	if got.MaxEntries != 300 {
		t.Errorf("MaxEntries = %d, want 300", got.MaxEntries)
	}
}

func TestResolve_ShortWindowKeepsTierSegment(t *testing.T) {
	got := Resolve(
		Request{DVRWindowSeconds: 3600},
		Tier{
			DefaultWindowSeconds:          3600,
			MaxWindowSeconds:              24 * 3600,
			DefaultSegmentDurationSeconds: 24,
			MaxEntries:                    10800,
		},
		Cluster{},
	)
	if got.SegmentDurationSeconds != 24 {
		t.Errorf("SegmentDurationSeconds = %d, want 24 (tier default wins for short window)", got.SegmentDurationSeconds)
	}
	if got.DVRWindowSeconds != 3600 {
		t.Errorf("DVRWindowSeconds = %d, want 3600", got.DVRWindowSeconds)
	}
	if got.MaxEntries != 150 {
		t.Errorf("MaxEntries = %d, want 150", got.MaxEntries)
	}
}

func TestResolve_LongWindowUsesTierSegmentOverDefaultFloor(t *testing.T) {
	got := Resolve(
		Request{DVRWindowSeconds: 72 * 3600},
		Tier{
			DefaultWindowSeconds:          72 * 3600,
			MaxWindowSeconds:              72 * 3600,
			DefaultSegmentDurationSeconds: 60,
			MaxEntries:                    1_000_000,
			AllowClusterExtension:         true,
		},
		Cluster{MaxWindowSeconds: 72 * 3600, MaxEntries: 1_000_000},
	)
	if got.SegmentDurationSeconds != 60 {
		t.Errorf("SegmentDurationSeconds = %d, want 60 (tier default exceeds 24s floor for >1d window)", got.SegmentDurationSeconds)
	}
	if got.DVRWindowSeconds != 72*3600 {
		t.Errorf("DVRWindowSeconds = %d, want %d", got.DVRWindowSeconds, 72*3600)
	}
	if got.MaxEntries != 4320 {
		t.Errorf("MaxEntries = %d, want 4320 (259200/60)", got.MaxEntries)
	}
}

func TestDefaultTiers_ExactPresets(t *testing.T) {
	tiers := DefaultTiers()
	cases := []struct {
		name        string
		wantDefault int
		wantMax     int
	}{
		{"free", 1800, 3600},
		{"supporter", 7200, 21600},
		{"developer", 14400, 43200},
		{"production", 14400, 86400},
		{"enterprise", 14400, 86400},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tier, ok := tiers[c.name]
			if !ok {
				t.Fatalf("missing tier %q", c.name)
			}
			if tier.DefaultWindowSeconds != c.wantDefault {
				t.Errorf("DefaultWindowSeconds = %d, want %d", tier.DefaultWindowSeconds, c.wantDefault)
			}
			if tier.MaxWindowSeconds != c.wantMax {
				t.Errorf("MaxWindowSeconds = %d, want %d", tier.MaxWindowSeconds, c.wantMax)
			}
		})
	}
}
