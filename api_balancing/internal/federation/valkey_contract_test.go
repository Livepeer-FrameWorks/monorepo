//go:build schema_verify

package federation

import (
	"context"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockervalkey"
)

func TestFederationCacheContracts_RealValkey(t *testing.T) {
	engine := dockervalkey.Start(t)
	ctx := context.Background()
	cache := NewRemoteEdgeCache(engine.Client, "federation-contract", logging.NewLogger())

	if !cache.TryAcquireLeaderLease(ctx, "peering", "instance-a") {
		t.Fatal("first leader did not acquire lease")
	}
	if cache.TryAcquireLeaderLease(ctx, "peering", "instance-b") || cache.RenewLeaderLease(ctx, "peering", "instance-b") {
		t.Fatal("non-owner acquired or renewed leader lease")
	}
	cache.ReleaseLeaderLease(ctx, "peering", "instance-b")
	if cache.TryAcquireLeaderLease(ctx, "peering", "instance-b") {
		t.Fatal("non-owner release removed leader lease")
	}
	cache.ReleaseLeaderLease(ctx, "peering", "instance-a")
	if !cache.TryAcquireLeaderLease(ctx, "peering", "instance-b") {
		t.Fatal("new leader did not acquire released lease")
	}

	if !cache.TryAcquireOriginPullLock(ctx, "tenant+stream", "pull-a") {
		t.Fatal("first origin pull did not acquire lock")
	}
	cache.ReleaseOriginPullLock(ctx, "tenant+stream", "pull-b")
	if cache.TryAcquireOriginPullLock(ctx, "tenant+stream", "pull-b") {
		t.Fatal("non-owner release removed origin-pull lock")
	}
	cache.ReleaseOriginPullLock(ctx, "tenant+stream", "pull-a")

	entry := func(cluster string, revision int64) *RemoteLiveStreamEntry {
		return &RemoteLiveStreamEntry{ClusterID: cluster, TenantID: "tenant-a", SourceRevision: revision, UpdatedAt: revision}
	}
	if applied, err := cache.ApplyRemoteStreamLifecycle(ctx, "tenant-a", "live+one", entry("cell-a", 20), true); err != nil || !applied {
		t.Fatalf("apply live revision: applied=%v err=%v", applied, err)
	}
	if applied, err := cache.ApplyRemoteStreamLifecycle(ctx, "tenant-a", "live+one", entry("cell-a", 19), false); err != nil || applied {
		t.Fatalf("stale offline revision applied=%v err=%v", applied, err)
	}
	if applied, err := cache.ApplyRemoteStreamLifecycle(ctx, "tenant-a", "live+one", entry("cell-a", 20), false); err != nil || !applied {
		t.Fatalf("equal offline did not dominate live: applied=%v err=%v", applied, err)
	}
	if applied, err := cache.ApplyRemoteStreamLifecycle(ctx, "tenant-a", "live+one", entry("cell-a", 20), true); err != nil || applied {
		t.Fatalf("equal live resurrected offline marker: applied=%v err=%v", applied, err)
	}
	if applied, err := cache.ApplyRemoteStreamLifecycle(ctx, "tenant-a", "live+one", entry("cell-b", 1), true); err != nil || !applied {
		t.Fatalf("independent origin revision was cross-ordered: applied=%v err=%v", applied, err)
	}

	ended := StreamPeerMembership{
		StreamName: "live+membership", TenantID: "tenant-a", SourceGeneration: "generation-a", SourceRevision: 30,
		Peers: []StreamPeerTarget{{ClusterID: "cell-b", Addr: "cell-b:18019"}},
	}
	if current, err := cache.SetStreamPeerMembership(ctx, ended); err != nil || !current {
		t.Fatalf("set membership: current=%v err=%v", current, err)
	}
	if current, err := cache.EndStreamPeerMembership(ctx, ended); err != nil || !current {
		t.Fatalf("end membership: current=%v err=%v", current, err)
	}
	records, _, err := cache.ScanEndedStreamPeerMemberships(ctx, 0, 100)
	if err != nil || len(records) != 1 {
		t.Fatalf("scan ended memberships: records=%+v err=%v", records, err)
	}
	successor := ended
	successor.SourceGeneration = "generation-b"
	successor.SourceRevision = 31
	if current, setErr := cache.SetStreamPeerMembership(ctx, successor); setErr != nil || !current {
		t.Fatalf("set successor membership: current=%v err=%v", current, setErr)
	}
	if purged, purgeErr := cache.PurgeEndedStreamPeerMemberships(ctx, records); purgeErr != nil || purged != 0 {
		t.Fatalf("stale tombstone purge removed successor: purged=%d err=%v", purged, purgeErr)
	}
	memberships, err := cache.LoadAllStreamPeerMemberships(ctx)
	if err != nil || !memberships[successor.StreamName].Active || memberships[successor.StreamName].SourceGeneration != "generation-b" {
		t.Fatalf("successor membership lost: memberships=%+v err=%v", memberships, err)
	}

	engine.Restart(t)
	cache = NewRemoteEdgeCache(engine.Client, "federation-contract", logging.NewLogger())
	memberships, err = cache.LoadAllStreamPeerMemberships(ctx)
	if err != nil || !memberships[successor.StreamName].Active {
		t.Fatalf("container replacement lost membership: memberships=%+v err=%v", memberships, err)
	}
}
