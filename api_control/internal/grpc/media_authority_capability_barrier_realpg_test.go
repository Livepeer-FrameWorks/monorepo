//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	foghornpb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/foghorn"
	"github.com/sirupsen/logrus"
)

type storedCellCapability struct {
	ready        bool
	replicas     int32
	firstReadyAt sql.NullTime
	attestedAt   time.Time
	updatedAt    time.Time
}

func readStoredCellCapability(ctx context.Context, t *testing.T, db *sql.DB, cellID string) storedCellCapability {
	t.Helper()
	var row storedCellCapability
	if err := db.QueryRowContext(ctx, `SELECT enforcement_ready, live_replicas, first_ready_at, attested_at, updated_at
		FROM commodore.media_cell_placement_capabilities WHERE cell_id = $1`, cellID).
		Scan(&row.ready, &row.replicas, &row.firstReadyAt, &row.attestedAt, &row.updatedAt); err != nil {
		t.Fatal(err)
	}
	return row
}

// An answer without an attestation (the cell's ledger read failed, or its
// ledger lists no live replica) says nothing about enforcement and keeps the
// stored readiness, including when it was first reached.
func TestRecordCapability_AbsentKeepsReadiness_RealPG(t *testing.T) {
	db := startCommodoreRealPG(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	server := &CommodoreServer{db: db, logger: logrus.New()}
	if err := server.recordCellPlacementCapability(ctx, "cell-a", readyCellAttestation()); err != nil {
		t.Fatal(err)
	}
	before := readStoredCellCapability(ctx, t, db, "cell-a")
	if !before.ready || !before.firstReadyAt.Valid {
		t.Fatalf("ready attestation not stored: %+v", before)
	}
	for name, absent := range map[string]*foghornpb.MediaCellPlacementCapability{
		"no attestation":    nil,
		"no live replicas":  {SupportedSchemaVersions: []uint32{1, 2}},
		"empty attestation": {},
	} {
		if err := server.recordCellPlacementCapability(ctx, "cell-a", absent); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		after := readStoredCellCapability(ctx, t, db, "cell-a")
		if !after.ready || !after.firstReadyAt.Valid || !after.firstReadyAt.Time.Equal(before.firstReadyAt.Time) {
			t.Fatalf("%s reset the stored readiness: before=%+v after=%+v", name, before, after)
		}
	}
	if ready, err := placementCellsReady(ctx, commodoredb.New(db), []string{"cell-a"}); err != nil || !ready {
		t.Fatalf("cell stopped counting as ready after an absent attestation: %t %v", ready, err)
	}
}

// Live replicas that explicitly attest they do not enforce placement withdraw
// the cell's readiness.
func TestRecordCapability_ExplicitNonEnforcingDowngrades_RealPG(t *testing.T) {
	db := startCommodoreRealPG(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	server := &CommodoreServer{db: db, logger: logrus.New()}
	if err := server.recordCellPlacementCapability(ctx, "cell-a", readyCellAttestation()); err != nil {
		t.Fatal(err)
	}
	notEnforcing := &foghornpb.MediaCellPlacementCapability{SupportedSchemaVersions: []uint32{1, 2}, LiveReplicas: 2}
	if err := server.recordCellPlacementCapability(ctx, "cell-a", notEnforcing); err != nil {
		t.Fatal(err)
	}
	after := readStoredCellCapability(ctx, t, db, "cell-a")
	if after.ready || after.firstReadyAt.Valid {
		t.Fatalf("explicit non-enforcing attestation kept readiness: %+v", after)
	}
	if ready, err := placementCellsReady(ctx, commodoredb.New(db), []string{"cell-a"}); err != nil || ready {
		t.Fatalf("withdrawn cell still counts as ready: %t %v", ready, err)
	}
}

// Every acknowledgement carries the cell's attestation. An unchanged one does
// not rewrite the row until five minutes have passed; a changed one always does.
func TestCapabilityAck_UnchangedNoWrite_RealPG(t *testing.T) {
	db := startCommodoreRealPG(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	server := &CommodoreServer{db: db, logger: logrus.New()}
	if err := server.recordCellPlacementCapability(ctx, "cell-a", readyCellAttestation()); err != nil {
		t.Fatal(err)
	}
	first := readStoredCellCapability(ctx, t, db, "cell-a")
	for range 3 {
		if err := server.recordCellPlacementCapability(ctx, "cell-a", readyCellAttestation()); err != nil {
			t.Fatal(err)
		}
	}
	if unchanged := readStoredCellCapability(ctx, t, db, "cell-a"); !unchanged.updatedAt.Equal(first.updatedAt) || !unchanged.attestedAt.Equal(first.attestedAt) {
		t.Fatalf("unchanged attestation rewrote the row: first=%+v now=%+v", first, unchanged)
	}

	if _, err := db.ExecContext(ctx, `UPDATE commodore.media_cell_placement_capabilities
		SET attested_at = NOW() - INTERVAL '6 minutes', updated_at = NOW() - INTERVAL '6 minutes' WHERE cell_id = 'cell-a'`); err != nil {
		t.Fatal(err)
	}
	stale := readStoredCellCapability(ctx, t, db, "cell-a")
	if err := server.recordCellPlacementCapability(ctx, "cell-a", readyCellAttestation()); err != nil {
		t.Fatal(err)
	}
	refreshed := readStoredCellCapability(ctx, t, db, "cell-a")
	if !refreshed.attestedAt.After(stale.attestedAt) || !refreshed.firstReadyAt.Time.Equal(first.firstReadyAt.Time) {
		t.Fatalf("stale attestation was not refreshed, or refresh moved first readiness: stale=%+v refreshed=%+v", stale, refreshed)
	}

	scaled := readyCellAttestation()
	scaled.LiveReplicas = 3
	if err := server.recordCellPlacementCapability(ctx, "cell-a", scaled); err != nil {
		t.Fatal(err)
	}
	if changed := readStoredCellCapability(ctx, t, db, "cell-a"); changed.replicas != 3 || !changed.ready {
		t.Fatalf("changed attestation was not written: %+v", changed)
	}
}
