//go:build schema_verify

package control

import (
	"context"
	"testing"

	"frameworks/api_balancing/internal/state"
)

func TestSyncCompletePlacementRespectsDeletionWatermark_RealPG(t *testing.T) {
	conn := startRealPG(t)
	ctx := context.Background()
	const (
		hash     = "sync-delete-fence-proof"
		tenantID = "11111111-1111-1111-1111-111111111111"
		nodeID   = "node-1"
	)
	if _, err := conn.ExecContext(ctx, `
INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, tenant_id, status)
VALUES ($1, 'vod', $2::uuid, 'ready')`, hash, tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `
INSERT INTO foghorn.artifact_nodes (
    artifact_hash, node_id, is_complete, is_orphaned, role, inventory_reported_at_ms
) VALUES ($1, $2, true, false, 'cache', 100)`, hash, nodeID); err != nil {
		t.Fatal(err)
	}

	deleteTx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	deletionOutcome, err := DeleteNodeArtifactTx(ctx, deleteTx, hash, nodeID, 200)
	if err != nil || deletionOutcome != state.NodeArtifactDeletionApplied {
		_ = deleteTx.Rollback()
		t.Fatalf("delete outcome=%s err=%v", deletionOutcome, err)
	}
	if err := deleteTx.Commit(); err != nil {
		t.Fatal(err)
	}

	staleTx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	added, err := AddCachedNodeCopyTx(ctx, staleTx, hash, nodeID, "/tmp/stale", 10, 150)
	if err != nil || added {
		_ = staleTx.Rollback()
		t.Fatalf("stale sync placement applied=%v err=%v", added, err)
	}
	if err := staleTx.Commit(); err != nil {
		t.Fatal(err)
	}
	var present bool
	if err := conn.QueryRowContext(ctx, `SELECT EXISTS (
    SELECT 1 FROM foghorn.artifact_nodes WHERE artifact_hash=$1 AND node_id=$2
)`, hash, nodeID).Scan(&present); err != nil {
		t.Fatal(err)
	}
	if present {
		t.Fatal("sync completion older than the deletion watermark resurrected placement")
	}

	reacquireTx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	added, err = AddCachedNodeCopyTx(ctx, reacquireTx, hash, nodeID, "/tmp/reacquired", 10, 300)
	if err != nil || !added {
		_ = reacquireTx.Rollback()
		t.Fatalf("newer sync placement applied=%v err=%v", added, err)
	}
	if err := reacquireTx.Commit(); err != nil {
		t.Fatal(err)
	}
	var reportedAtMs int64
	if err := conn.QueryRowContext(ctx, `
SELECT inventory_reported_at_ms
FROM foghorn.artifact_nodes
WHERE artifact_hash=$1 AND node_id=$2`, hash, nodeID).Scan(&reportedAtMs); err != nil {
		t.Fatal(err)
	}
	if reportedAtMs != 300 {
		t.Fatalf("reacquired placement timestamp=%d, want 300", reportedAtMs)
	}
}

func TestDelayedSyncCannotMovePlacementClockBackward_RealPG(t *testing.T) {
	conn := startRealPG(t)
	ctx := context.Background()
	const (
		hash     = "sync-clock-monotonic-proof"
		tenantID = "11111111-1111-1111-1111-111111111111"
		nodeID   = "node-1"
	)
	if _, err := conn.ExecContext(ctx, `
INSERT INTO foghorn.artifacts (artifact_hash, artifact_type, tenant_id, status)
VALUES ($1, 'vod', $2::uuid, 'ready')`, hash, tenantID); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(ctx, `
INSERT INTO foghorn.artifact_nodes (
    artifact_hash, node_id, is_complete, is_orphaned, role, inventory_reported_at_ms
) VALUES ($1, $2, true, false, 'cache', 300)`, hash, nodeID); err != nil {
		t.Fatal(err)
	}

	lateSyncTx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	applied, err := AddCachedNodeCopyTx(ctx, lateSyncTx, hash, nodeID, "/tmp/late", 10, 100)
	if err != nil || !applied {
		_ = lateSyncTx.Rollback()
		t.Fatalf("late sync applied=%v err=%v", applied, err)
	}
	if err := lateSyncTx.Commit(); err != nil {
		t.Fatal(err)
	}

	var reportedAtMs int64
	if err := conn.QueryRowContext(ctx, `
SELECT inventory_reported_at_ms FROM foghorn.artifact_nodes
WHERE artifact_hash=$1 AND node_id=$2`, hash, nodeID).Scan(&reportedAtMs); err != nil {
		t.Fatal(err)
	}
	if reportedAtMs != 300 {
		t.Fatalf("delayed sync moved placement clock to %d, want 300", reportedAtMs)
	}
	var watermarkRows int
	if err := conn.QueryRowContext(ctx, `
SELECT count(*) FROM foghorn.artifact_node_deletion_watermark
WHERE artifact_hash=$1 AND node_id=$2`, hash, nodeID).Scan(&watermarkRows); err != nil {
		t.Fatal(err)
	}
	if watermarkRows != 0 {
		t.Fatalf("ordinary placement created %d deletion watermarks, want 0", watermarkRows)
	}

	deleteTx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := DeleteNodeArtifactTx(ctx, deleteTx, hash, nodeID, 200)
	if err != nil || outcome != state.NodeArtifactDeletionFenced {
		_ = deleteTx.Rollback()
		t.Fatalf("delete outcome=%s err=%v, want fenced", outcome, err)
	}
	if err := deleteTx.Commit(); err != nil {
		t.Fatal(err)
	}
}
