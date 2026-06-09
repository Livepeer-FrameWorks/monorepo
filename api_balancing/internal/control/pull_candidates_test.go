package control

import (
	"context"
	"testing"

	"frameworks/api_balancing/internal/balancer"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/pullsource"
)

// filterPullCandidatesByClass enforces pull-source placement: a blocked class is
// refused outright; a public source with no allow-list passes through unchanged;
// otherwise it filters candidate nodes to the clusters the shared placement rules
// permit (private sources additionally require the per-cluster private-pull
// capability). Getting this wrong either routes a pull to a forbidden cluster or
// strands a legitimate one — so each arm is pinned. It's testable directly: the
// private-capability lookup is an injected callback, no Commodore needed.
func TestFilterPullCandidatesByClass(t *testing.T) {
	ctx := context.Background()
	nodes := []balancer.NodeWithScore{
		{NodeID: "n1", ClusterID: "c1"},
		{NodeID: "n2", ClusterID: "c1"},
	}
	allowAll := func(context.Context, string) bool { return true }
	denyAll := func(context.Context, string) bool { return false }

	t.Run("blocked class is refused", func(t *testing.T) {
		if _, err := filterPullCandidatesByClass(ctx, nodes, "live+x", "c1", pullsource.ClassBlocked, nil, allowAll); err == nil {
			t.Fatal("blocked class must error")
		}
	})

	t.Run("public with no allow-list passes through", func(t *testing.T) {
		out, err := filterPullCandidatesByClass(ctx, nodes, "live+x", "c1", pullsource.ClassPublic, nil, allowAll)
		if err != nil || len(out) != 2 {
			t.Fatalf("got (%d nodes, %v), want all 2 passed through", len(out), err)
		}
	})

	t.Run("private with capability is eligible", func(t *testing.T) {
		out, err := filterPullCandidatesByClass(ctx, nodes, "live+x", "c1", pullsource.ClassPrivate, []string{"c1"}, allowAll)
		if err != nil || len(out) != 2 {
			t.Fatalf("got (%d nodes, %v), want eligible cluster's nodes", len(out), err)
		}
	})

	t.Run("private without capability is rejected", func(t *testing.T) {
		if _, err := filterPullCandidatesByClass(ctx, nodes, "live+x", "c1", pullsource.ClassPrivate, []string{"c1"}, denyAll); err == nil {
			t.Fatal("private source with no private-pull capability must be rejected")
		}
	})

	t.Run("public with allow-list excluding the cluster strands it", func(t *testing.T) {
		if _, err := filterPullCandidatesByClass(ctx, nodes, "live+x", "c1", pullsource.ClassPublic, []string{"other"}, allowAll); err == nil {
			t.Fatal("nodes outside the allow-list must yield no eligible candidate")
		}
	})
}
