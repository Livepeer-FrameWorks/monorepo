package clusterurls

import (
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	quartermasterpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/quartermaster"
)

func TestChandlerBase(t *testing.T) {
	t.Run("override_takes_precedence", func(t *testing.T) {
		t.Setenv("CHANDLER_BASE_URL", "http://localhost:18090/")
		r := NewResolver(nil, logging.NewLogger())
		// Even an unknown cluster resolves to the override in single-node mode.
		if got := r.ChandlerBase("anything"); got != "http://localhost:18090" {
			t.Fatalf("ChandlerBase = %q, want trimmed override", got)
		}
		// Empty cluster id is fine when the override is set.
		if got := r.ChandlerBase(""); got != "http://localhost:18090" {
			t.Fatalf("ChandlerBase(empty) = %q, want override", got)
		}
	})

	t.Run("snapshot_lookup_with_trim", func(t *testing.T) {
		t.Setenv("CHANDLER_BASE_URL", "")
		r := NewResolver(nil, logging.NewLogger())
		snap := map[string]string{"media-1": "https://chandler.media-1.example"}
		r.snapshot.Store(&snap)

		if got := r.ChandlerBase("  media-1  "); got != "https://chandler.media-1.example" {
			t.Errorf("ChandlerBase(whitespace) = %q, want trimmed-key hit", got)
		}
		if got := r.ChandlerBase("unknown"); got != "" {
			t.Errorf("ChandlerBase(unknown) = %q, want empty", got)
		}
		if got := r.ChandlerBase(""); got != "" {
			t.Errorf("ChandlerBase(empty) = %q, want empty", got)
		}
	})
}

func TestNewResolverInitialState(t *testing.T) {
	t.Run("trims_env_override", func(t *testing.T) {
		t.Setenv("CHANDLER_BASE_URL", "  http://host:1/  ")
		r := NewResolver(nil, logging.NewLogger())
		if r.publicChandlerBase != "http://host:1" {
			t.Errorf("publicChandlerBase = %q, want trimmed", r.publicChandlerBase)
		}
	})

	t.Run("unset_env_leaves_empty_snapshot", func(t *testing.T) {
		t.Setenv("CHANDLER_BASE_URL", "")
		r := NewResolver(nil, logging.NewLogger())
		if r.publicChandlerBase != "" {
			t.Errorf("publicChandlerBase = %q, want empty", r.publicChandlerBase)
		}
		snap := r.snapshot.Load()
		if snap == nil || len(*snap) != 0 {
			t.Errorf("initial snapshot = %v, want empty non-nil map", snap)
		}
	})
}

func TestChandlerBaseForEarlyReturns(t *testing.T) {
	cases := []struct {
		name    string
		cluster *quartermasterpb.InfrastructureCluster
	}{
		{"empty_base_url", &quartermasterpb.InfrastructureCluster{ClusterId: "c1", ClusterName: "C1"}},
		{"empty_everything", &quartermasterpb.InfrastructureCluster{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := chandlerBaseFor(tc.cluster); got != "" {
				t.Errorf("chandlerBaseFor = %q, want empty", got)
			}
		})
	}
}

func TestBuildThumbnailAssetsGuards(t *testing.T) {
	t.Setenv("CHANDLER_BASE_URL", "")
	r := NewResolver(nil, logging.NewLogger())

	if got := r.BuildThumbnailAssets("media-1", ""); got != nil {
		t.Errorf("empty asset key = %v, want nil", got)
	}
	// Cluster unknown to the (empty) snapshot and no override → nil.
	if got := r.BuildThumbnailAssets("unknown", "asset-1"); got != nil {
		t.Errorf("unknown cluster = %v, want nil", got)
	}

	// Stored base with a trailing slash must not produce a double slash.
	snap := map[string]string{"media-1": "https://chandler.media-1.example/"}
	r.snapshot.Store(&snap)
	got := r.BuildThumbnailAssets("media-1", "asset-1")
	if got == nil {
		t.Fatal("expected assets")
	}
	if got.GetPosterUrl() != "https://chandler.media-1.example/assets/asset-1/poster.jpg" {
		t.Errorf("poster URL = %q (trailing-slash not normalized)", got.GetPosterUrl())
	}
	if got.GetAssetKey() != "asset-1" {
		t.Errorf("asset key = %q, want asset-1", got.GetAssetKey())
	}
}

// Start must be safe and idempotent with a nil Quartermaster client: refresh
// short-circuits, the snapshot stays empty, and a second Start is a no-op.
func TestStartNilQMIsSafeAndIdempotent(t *testing.T) {
	t.Setenv("CHANDLER_BASE_URL", "")
	r := NewResolver(nil, logging.NewLogger())
	ctx := t.Context() // cancelled automatically at test cleanup, stopping the goroutine

	r.Start(ctx, 0) // interval 0 → defaults internally; nil qm → empty snapshot
	r.Start(ctx, 0) // startOnce makes this a no-op

	if got := r.ChandlerBase("any"); got != "" {
		t.Errorf("ChandlerBase = %q, want empty after nil-qm start", got)
	}
	if err := r.refresh(ctx); err != nil {
		t.Errorf("refresh with nil qm returned error: %v", err)
	}
}
