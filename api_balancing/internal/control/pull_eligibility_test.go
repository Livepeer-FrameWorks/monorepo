package control

import (
	"context"
	"testing"

	"frameworks/api_balancing/internal/balancer"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
)

// filterPullCandidatesByEligibility narrows the candidate node set to those whose
// cluster may legitimately dial a managed pull source: a non-pull stream or a
// public source with no placement pin passes through untouched, while an
// allow-list that excludes a candidate's cluster strands it. This is the
// resolve-time guard against routing a pull to a cluster Commodore didn't sanction.
func TestFilterPullCandidatesByEligibility(t *testing.T) {
	ctx := context.Background()
	nodes := []balancer.NodeWithScore{{NodeID: "n1", ClusterID: "c1"}, {NodeID: "n2", ClusterID: "c1"}}
	deps := &PlaybackDependencies{LocalClusterID: "c1"}

	pullSrc := func(found bool, uri string, allowed []string) *fakeCommodoreInternal {
		return &fakeCommodoreInternal{
			pullSource: func(_ context.Context, _ *commodorepb.ResolvePullSourceByInternalNameRequest) (*commodorepb.ResolvePullSourceByInternalNameResponse, error) {
				return &commodorepb.ResolvePullSourceByInternalNameResponse{
					Found: found, Enabled: true, SourceUri: uri, AllowedClusterIds: allowed,
				}, nil
			},
		}
	}

	t.Run("empty node set passes through", func(t *testing.T) {
		startFakeCommodoreServer(t, &fakeCommodoreInternal{})
		out, err := filterPullCandidatesByEligibility(ctx, nil, "live+x", deps)
		if err != nil || len(out) != 0 {
			t.Fatalf("got (%v,%v)", out, err)
		}
	})

	t.Run("not a managed pull stream passes through", func(t *testing.T) {
		startFakeCommodoreServer(t, pullSrc(false, "", nil)) // Found=false
		out, err := filterPullCandidatesByEligibility(ctx, nodes, "live+x", deps)
		if err != nil || len(out) != 2 {
			t.Fatalf("non-pull stream should pass all nodes through, got (%d,%v)", len(out), err)
		}
	})

	t.Run("public source with no placement pin passes through", func(t *testing.T) {
		startFakeCommodoreServer(t, pullSrc(true, "https://cdn.example.com/live.m3u8", nil))
		out, err := filterPullCandidatesByEligibility(ctx, nodes, "live+x", deps)
		if err != nil || len(out) != 2 {
			t.Fatalf("public unpinned source should pass through, got (%d,%v)", len(out), err)
		}
	})

	t.Run("allow-list including the cluster keeps its nodes", func(t *testing.T) {
		startFakeCommodoreServer(t, pullSrc(true, "https://cdn.example.com/live.m3u8", []string{"c1"}))
		out, err := filterPullCandidatesByEligibility(ctx, nodes, "live+x", deps)
		if err != nil || len(out) != 2 {
			t.Fatalf("allow-listed cluster nodes should survive, got (%d,%v)", len(out), err)
		}
	})

	t.Run("allow-list excluding the cluster strands it", func(t *testing.T) {
		startFakeCommodoreServer(t, pullSrc(true, "https://cdn.example.com/live.m3u8", []string{"other"}))
		if _, err := filterPullCandidatesByEligibility(ctx, nodes, "live+x", deps); err == nil {
			t.Fatal("nodes outside the allow-list must be rejected, not silently routed")
		}
	})
}

// ClusterAllowsPrivatePulls fails closed: an empty cluster id (or, in tests, an
// unwired Quartermaster client) reports no private-pull capability rather than
// defaulting to permissive.
func TestClusterAllowsPrivatePulls_FailsClosed(t *testing.T) {
	if ClusterAllowsPrivatePulls(context.Background(), "") {
		t.Fatal("empty cluster id must not be granted private-pull capability")
	}
	if ClusterAllowsPrivatePulls(context.Background(), "c1") {
		t.Fatal("no Quartermaster client must fail closed (no capability)")
	}
}
