//go:build schema_verify

package mediaauthority

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	mediaauthoritypb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// Two Foghorn replicas of one cell share its database and nothing else: no
// changelog announcement reaches replica B. A tombstone, and a tenant change,
// applied on replica A are answered by B from the first read begun a second
// or more after the apply committed. Within that second B may still answer
// from the pair it read before.
func TestPairCache_TombstoneOnOtherReplicaVisibleWithin1s_RealPG(t *testing.T) {
	conn := startMediaAuthorityRealPG(t)
	_, trust, _ := storeFixture(t, "cell-a")
	ctx := context.Background()
	replicaA, err := NewStore(conn, "cell-a", trust)
	if err != nil {
		t.Fatal(err)
	}
	replicaB, err := NewStore(conn, "cell-a", trust)
	if err != nil {
		t.Fatal(err)
	}
	replicaA.now = func() time.Time { return storeFixtureNow.Add(time.Minute) }
	var clockB atomic.Int64
	clockB.Store(storeFixtureNow.Add(time.Minute).UnixNano())
	replicaB.now = func() time.Time { return time.Unix(0, clockB.Load()).UTC() }
	reads := prometheus.NewCounterVec(prometheus.CounterOpts{Name: "test_placement_pair_reads_total", Help: "test"}, []string{"result"})
	replicaB.SetPlacementPairReadMetric(reads)

	active, _ := signedArtifactFixture(t, mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE, 2)
	for _, envelope := range [][]byte{signedTenantAt(t, 7, storeFixtureNow, storeFixtureNow.Add(24*time.Hour)), active} {
		if _, err = replicaA.Apply(ctx, envelope); err != nil {
			t.Fatal(err)
		}
	}
	const tenantID = "10000000-0000-0000-0000-000000000001"
	authorityID := sharedauthority.ArtifactAuthorityID("20000000-0000-0000-0000-000000000001")
	readB := func() PlacementPair {
		t.Helper()
		pair, readErr := replicaB.Placement(ctx, tenantID, authorityID, "artifact-internal")
		if readErr != nil {
			t.Fatalf("replica B placement read: %v", readErr)
		}
		return pair
	}

	if got := readB(); got.Object.Authority.GetLifecycle() != mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_ACTIVE || got.Tenant.Version != 7 {
		t.Fatalf("replica B before the tombstone: %s tenant v%d", got.Object.Authority.GetLifecycle(), got.Tenant.Version)
	}
	readAt := time.Unix(0, clockB.Load())

	tombstone, _ := signedArtifactFixture(t, mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_TOMBSTONE, 3)
	if _, err = replicaA.Apply(ctx, tombstone); err != nil {
		t.Fatal(err)
	}
	clockB.Store(readAt.Add(placementPairCacheTTL - time.Millisecond).UnixNano())
	readB()
	if testutil.ToFloat64(reads.WithLabelValues("hit")) != 1 {
		t.Fatal("replica B did not answer from its kept pair inside the window; the test would not exercise the cache")
	}
	clockB.Store(readAt.Add(placementPairCacheTTL).UnixNano())
	if got := readB(); got.Object.Authority.GetLifecycle() != mediaauthoritypb.AuthorityLifecycle_AUTHORITY_LIFECYCLE_TOMBSTONE {
		t.Fatalf("replica B one second after replica A applied the tombstone: %s", got.Object.Authority.GetLifecycle())
	}

	tombstonedAt := time.Unix(0, clockB.Load())
	if _, err = replicaA.Apply(ctx, signedTenantAt(t, 8, storeFixtureNow, storeFixtureNow.Add(24*time.Hour))); err != nil {
		t.Fatal(err)
	}
	clockB.Store(tombstonedAt.Add(placementPairCacheTTL).UnixNano())
	if got := readB(); got.Tenant.Version != 8 {
		t.Fatalf("replica B one second after replica A applied tenant v8: v%d", got.Tenant.Version)
	}
}
