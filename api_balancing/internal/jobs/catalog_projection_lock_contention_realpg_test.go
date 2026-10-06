//go:build schema_verify

package jobs

import (
	"context"
	"testing"
	"time"
)

// A delete RPC commits on one Foghorn replica and kicks that replica's reconciler, while another
// replica of the same cell holds the 'artifact_reconciler' advisory lock for a pass whose
// projection query already ran. The kicked replica loses the lock; the deletion must still reach
// the catalog within seconds of the peer releasing the lock, not on the next 5-minute fallback pass.
func TestTriggeredProjectionRetriesWhenPeerReplicaHoldsLock_RealPG(t *testing.T) {
	db := startRealPGForCleanup(t)
	ctx := context.Background()
	const tenant = "7a1e0000-0000-4000-8000-0000000000d1"
	const clipHash = "20261006120000deadbeefcafe0001"
	const origin = "staging-media-eu"

	peer, err := db.Conn(ctx)
	if err != nil {
		t.Fatalf("peer replica connection: %v", err)
	}
	defer func() { _ = peer.Close() }()
	if _, err := peer.ExecContext(ctx, `SELECT pg_advisory_lock(hashtext('artifact_reconciler'))`); err != nil {
		t.Fatalf("peer replica takes the reconciler lock: %v", err)
	}
	peerHolds := true
	releasePeer := func() {
		if !peerHolds {
			return
		}
		peerHolds = false
		if _, err := peer.ExecContext(ctx, `SELECT pg_advisory_unlock(hashtext('artifact_reconciler'))`); err != nil {
			t.Fatalf("peer replica releases the reconciler lock: %v", err)
		}
	}
	defer releasePeer()

	commodore := &mockCommodoreClient{}
	r := newTestReconciler(t, db, nil, commodore, nil)
	r.interval = 5 * time.Minute
	r.clusterID = origin
	r.lockRetryInterval = 200 * time.Millisecond
	r.wg.Add(1)
	go r.run()
	t.Cleanup(func() {
		close(r.stopCh)
		r.wg.Wait()
	})

	// The delete commits after the peer's pass already listed its projection batch.
	if _, err := db.ExecContext(ctx, `INSERT INTO foghorn.artifacts
		(artifact_hash, artifact_type, tenant_id, status, origin_cluster_id, storage_cluster_id)
		VALUES ($1, 'clip', $2::uuid, 'deleted', $3, $3)`, clipHash, tenant, origin); err != nil {
		t.Fatalf("seed deleted clip: %v", err)
	}
	r.Trigger()

	// Give the kicked pass time to run and lose the lock before the peer's pass ends.
	time.Sleep(time.Second)
	if n := deletionProjections(commodore, clipHash); n != 0 {
		t.Fatalf("no replica may project while the peer holds the lock, got %d deletion calls", n)
	}
	releasePeer()

	deadline := time.Now().Add(5 * time.Second)
	for deletionProjections(commodore, clipHash) == 0 {
		if time.Now().After(deadline) {
			t.Fatalf("deletion committed while a peer replica held the reconciler lock did not reach the catalog within 5s of the lock being released")
		}
		time.Sleep(50 * time.Millisecond)
	}

	var syncedRev, revision int64
	for {
		if err := db.QueryRowContext(ctx, `SELECT catalog_synced_rev, catalog_revision FROM foghorn.artifacts WHERE artifact_hash = $1`,
			clipHash).Scan(&syncedRev, &revision); err != nil {
			t.Fatalf("read watermark: %v", err)
		}
		if syncedRev == revision || time.Now().After(deadline) {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if syncedRev != revision {
		t.Fatalf("deletion watermark must cover the projected revision: synced=%d revision=%d", syncedRev, revision)
	}
}

func deletionProjections(m *mockCommodoreClient, hash string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, call := range m.snapshotCalls {
		if call.GetAssetKey() == hash && call.GetDeleted() {
			n++
		}
	}
	return n
}
