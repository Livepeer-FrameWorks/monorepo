//go:build schema_verify

package foghorndb

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func TestControlReplicaCapabilityLedger_RealPG(t *testing.T) {
	verifyControlReplicaCapabilityLedger(t, startFoghornCatalogPostgres(t))
}

func TestControlReplicaCapabilityLedger_RealYugabyte(t *testing.T) {
	verifyControlReplicaCapabilityLedger(t, startFoghornCatalogYugabyte(t))
}

func verifyControlReplicaCapabilityLedger(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	q := New(db)
	read := func() ReadControlCellPlacementCapabilityRow {
		t.Helper()
		row, err := q.ReadControlCellPlacementCapability(ctx, 60)
		if err != nil {
			t.Fatal(err)
		}
		return row
	}
	if row := read(); row.LiveReplicas != 0 || row.AllEnforced || row.MinSchemaVersion != 0 {
		t.Fatalf("empty ledger attested: %+v", row)
	}
	for _, replica := range []struct {
		id       string
		enforced bool
	}{{"replica-a", true}, {"replica-b", false}} {
		if err := q.UpsertControlReplicaHeartbeat(ctx, UpsertControlReplicaHeartbeatParams{ReplicaID: replica.id, ReleaseVersion: "v0.3.0", PlacementSchemaVersion: 2, PlacementEnforced: replica.enforced}); err != nil {
			t.Fatal(err)
		}
	}
	if row := read(); row.LiveReplicas != 2 || row.AllEnforced || row.MinSchemaVersion != 2 {
		t.Fatalf("mixed ledger attested: %+v", row)
	}
	if err := q.UpsertControlReplicaHeartbeat(ctx, UpsertControlReplicaHeartbeatParams{ReplicaID: "replica-b", ReleaseVersion: "v0.3.0", PlacementSchemaVersion: 2, PlacementEnforced: true}); err != nil {
		t.Fatal(err)
	}
	if row := read(); row.LiveReplicas != 2 || !row.AllEnforced {
		t.Fatalf("fully enforcing ledger withheld attestation: %+v", row)
	}

	// The authority feature level is the minimum over live replicas. A replica
	// on an older release never writes the column, so it holds the cell at 0:
	// Commodore must not send a 30-day authority that replica would reject.
	if row := read(); row.MinAuthorityFeatureLevel != 0 {
		t.Fatalf("replicas that never reported a feature level attested %d", row.MinAuthorityFeatureLevel)
	}
	if err := q.UpsertControlReplicaHeartbeat(ctx, UpsertControlReplicaHeartbeatParams{ReplicaID: "replica-a", ReleaseVersion: "v0.3.11", PlacementSchemaVersion: 2, PlacementEnforced: true, AuthorityFeatureLevel: 1}); err != nil {
		t.Fatal(err)
	}
	if row := read(); row.MinAuthorityFeatureLevel != 0 {
		t.Fatalf("one upgraded replica of two attested level %d", row.MinAuthorityFeatureLevel)
	}
	if err := q.UpsertControlReplicaHeartbeat(ctx, UpsertControlReplicaHeartbeatParams{ReplicaID: "replica-b", ReleaseVersion: "v0.3.11", PlacementSchemaVersion: 2, PlacementEnforced: true, AuthorityFeatureLevel: 1}); err != nil {
		t.Fatal(err)
	}
	if row := read(); row.MinAuthorityFeatureLevel != 1 {
		t.Fatalf("fully upgraded cell attested level %d, want 1", row.MinAuthorityFeatureLevel)
	}
	if _, err := db.ExecContext(ctx, "UPDATE foghorn.control_replicas SET last_seen_at = NOW() - INTERVAL '2 hours', placement_enforced = FALSE WHERE replica_id = 'replica-b'"); err != nil {
		t.Fatal(err)
	}
	if row := read(); row.LiveReplicas != 1 || !row.AllEnforced {
		t.Fatalf("stale replica still counted: %+v", row)
	}
	if deleted, err := q.DeleteStaleControlReplicas(ctx, 3600); err != nil || deleted != 1 {
		t.Fatalf("stale prune: %d %v", deleted, err)
	}
	var remaining int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM foghorn.control_replicas").Scan(&remaining); err != nil || remaining != 1 {
		t.Fatalf("prune removed live replica: %d %v", remaining, err)
	}
}
