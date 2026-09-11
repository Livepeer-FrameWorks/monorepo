//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
)

func TestMediaPlacementDeliveryClaims_RealPG(t *testing.T) {
	testMediaPlacementDeliveryClaims(t, startCommodoreRealPG(t))
}

func TestMediaPlacementDeliveryClaims_RealYugabyte(t *testing.T) {
	testMediaPlacementDeliveryClaims(t, startPlacementDeliveryYugabyte(t, "placement_claims"))
}

func testMediaPlacementDeliveryClaims(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	q := commodoredb.New(db)
	for _, fixture := range []struct {
		id, cell string
		schema   int32
		lifetime time.Duration
	}{
		{"short-a", "short", 2, 30 * time.Second},
		{"short-b", "short", 2, 30 * time.Second},
		{"boundary", "boundary", 2, time.Minute},
		{"long", "long", 2, time.Minute + time.Second},
		{"legacy", "legacy", 1, 30 * time.Second},
	} {
		now := time.Now().UTC()
		if err := q.InsertMediaAuthorityVersion(ctx, commodoredb.InsertMediaAuthorityVersionParams{AuthorityKind: "tenant", AuthorityID: fixture.id, AuthorityVersion: 1, PayloadSchemaVersion: fixture.schema, Payload: []byte{}, PayloadSha256: make([]byte, 32), SourceRevisions: []byte("[]"), IssuedAt: now, RefreshAfter: now, ValidUntil: now.Add(fixture.lifetime)}); err != nil {
			t.Fatal(err)
		}
		if _, err := q.EnqueueMediaAuthorityDelivery(ctx, commodoredb.EnqueueMediaAuthorityDeliveryParams{AuthorityKind: "tenant", AuthorityID: fixture.id, AuthorityVersion: 1, CellID: fixture.cell, SignedEnvelope: []byte{}}); err != nil {
			t.Fatal(err)
		}
	}
	ordinary, err := q.ClaimMediaAuthorityDeliveries(ctx, commodoredb.ClaimMediaAuthorityDeliveriesParams{LeaseMs: 60000, BatchSize: 10})
	if err != nil || len(ordinary) != 2 {
		t.Fatalf("ordinary classification: %v %v", ordinary, err)
	}
	for _, row := range ordinary {
		if row.AuthorityID != "long" && row.AuthorityID != "legacy" {
			t.Fatal("ordinary queue stole a short schema-2 lease")
		}
	}
	claim := func() []commodoredb.ClaimMediaAuthorityDeadlineDeliveryRow {
		t.Helper()
		rows, err := q.ClaimMediaAuthorityDeadlineDelivery(ctx, commodoredb.ClaimMediaAuthorityDeadlineDeliveryParams{LeaseMs: 60000, BatchSize: 10})
		if err != nil {
			t.Fatal(err)
		}
		return rows
	}
	rows := claim()
	seen := map[string]bool{}
	for _, row := range rows {
		seen[row.AuthorityID] = true
	}
	if len(rows) != 2 || !seen["short-a"] || !seen["boundary"] {
		t.Fatalf("deadline classification or per-cell heads: %v", rows)
	}
	if rows := claim(); len(rows) != 0 {
		t.Fatalf("active cell lease permitted another claim: %v", rows)
	}
	if _, err := db.ExecContext(ctx, "UPDATE commodore.media_authority_deliveries SET status='pending',lease_expires_at=NULL WHERE authority_kind='tenant' AND authority_id='short-a' AND authority_version=1 AND cell_id='short'"); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var locked string
	if err := tx.QueryRowContext(ctx, "SELECT authority_id FROM commodore.media_authority_deliveries WHERE authority_kind='tenant' AND authority_id='short-a' AND authority_version=1 AND cell_id='short' FOR UPDATE").Scan(&locked); err != nil {
		t.Fatal(err)
	}
	if rows := claim(); len(rows) != 0 {
		t.Fatalf("worker bypassed a locked cell head: %v", rows)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	rows = claim()
	if len(rows) != 1 || rows[0].AuthorityID != "short-a" {
		t.Fatalf("released cell head unavailable: %v", rows)
	}
	if _, err := q.MarkMediaAuthorityDeliveryAcknowledged(ctx, commodoredb.MarkMediaAuthorityDeliveryAcknowledgedParams{AuthorityKind: "tenant", AuthorityID: "short-a", AuthorityVersion: 1, CellID: "short"}); err != nil {
		t.Fatal(err)
	}
	rows = claim()
	if len(rows) != 1 || rows[0].AuthorityID != "short-b" || rows[0].Attempts != 1 {
		t.Fatalf("cell did not advance after acknowledgment: %v", rows)
	}
	if _, err := db.ExecContext(ctx, "UPDATE commodore.media_authority_deliveries SET lease_expires_at=NOW()-INTERVAL '1 second' WHERE authority_kind='tenant' AND authority_id='short-b' AND authority_version=1 AND cell_id='short'"); err != nil {
		t.Fatal(err)
	}
	rows = claim()
	if len(rows) != 1 || rows[0].AuthorityID != "short-b" || rows[0].Attempts != 2 {
		t.Fatalf("expired claim lease did not recover: %v", rows)
	}
	if _, err := q.SupersedeOlderMediaAuthorityDeliveries(ctx, commodoredb.SupersedeOlderMediaAuthorityDeliveriesParams{AuthorityKind: "tenant", AuthorityID: "short-b", AuthorityVersion: 2}); err != nil {
		t.Fatal(err)
	}
	if rows := claim(); len(rows) != 0 {
		t.Fatalf("superseded delivery remained claimable: %v", rows)
	}
}
