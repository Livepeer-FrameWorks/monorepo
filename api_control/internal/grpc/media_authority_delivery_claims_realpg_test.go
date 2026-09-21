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
		if _, err := q.EnqueueMediaAuthorityDelivery(ctx, commodoredb.EnqueueMediaAuthorityDeliveryParams{AuthorityKind: "tenant", AuthorityID: fixture.id, AuthorityVersion: 1, CellID: fixture.cell, SignedEnvelope: []byte{},
			ShortLease: mediaAuthorityShortLease(uint32(fixture.schema), now, now.Add(fixture.lifetime))}); err != nil {
			t.Fatal(err)
		}
		if rows, err := q.UpsertCurrentMediaAuthority(ctx, commodoredb.UpsertCurrentMediaAuthorityParams{AuthorityKind: "tenant", AuthorityID: fixture.id, AuthorityVersion: 1}); err != nil || rows != 1 {
			t.Fatalf("set current authority %s: rows=%d err=%v", fixture.id, rows, err)
		}
	}
	// Ordinary deliveries are found and claimed one cell at a time. Only the cells
	// with an ordinary delivery waiting are listed.
	cells, err := q.ListMediaAuthorityDeliveryCells(ctx)
	if err != nil || len(cells) != 2 || cells[0] != "legacy" || cells[1] != "long" {
		t.Fatalf("cells with ordinary deliveries waiting = %v (err %v), want legacy and long", cells, err)
	}
	var ordinary []commodoredb.ClaimMediaAuthorityDeliveriesRow
	for _, cell := range cells {
		claimed, claimErr := q.ClaimMediaAuthorityDeliveries(ctx, commodoredb.ClaimMediaAuthorityDeliveriesParams{CellID: cell, LeaseMs: 60000, BatchSize: 10})
		if claimErr != nil {
			t.Fatal(claimErr)
		}
		for _, row := range claimed {
			if row.CellID != cell {
				t.Fatalf("a claim for cell %s took a delivery for %s", cell, row.CellID)
			}
		}
		ordinary = append(ordinary, claimed...)
	}
	if len(ordinary) != 2 {
		t.Fatalf("ordinary classification: %v", ordinary)
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
	insertDeadline := func(version int64) {
		t.Helper()
		now := time.Now().UTC()
		if err := q.InsertMediaAuthorityVersion(ctx, commodoredb.InsertMediaAuthorityVersionParams{
			AuthorityKind: "tenant", AuthorityID: "deadline-ordered", AuthorityVersion: version, PayloadSchemaVersion: 2,
			Payload: []byte{}, PayloadSha256: make([]byte, 32), SourceRevisions: []byte("[]"),
			IssuedAt: now, RefreshAfter: now, ValidUntil: now.Add(30 * time.Second),
		}); err != nil {
			t.Fatal(err)
		}
		if rows, err := q.UpsertCurrentMediaAuthority(ctx, commodoredb.UpsertCurrentMediaAuthorityParams{AuthorityKind: "tenant", AuthorityID: "deadline-ordered", AuthorityVersion: version}); err != nil || rows != 1 {
			t.Fatalf("set current deadline authority %d: rows=%d err=%v", version, rows, err)
		}
		if _, err := q.EnqueueMediaAuthorityDelivery(ctx, commodoredb.EnqueueMediaAuthorityDeliveryParams{AuthorityKind: "tenant", AuthorityID: "deadline-ordered", AuthorityVersion: version, CellID: "deadline-ordered", SignedEnvelope: []byte{}, ShortLease: true}); err != nil {
			t.Fatal(err)
		}
	}
	insertDeadline(1)
	deadlineRows := claim()
	if len(deadlineRows) != 1 || deadlineRows[0].AuthorityID != "deadline-ordered" || deadlineRows[0].AuthorityVersion != 1 {
		t.Fatalf("initial ordered deadline delivery = %v", deadlineRows)
	}
	insertDeadline(2)
	if _, err := q.SupersedeOlderMediaAuthorityDeliveries(ctx, commodoredb.SupersedeOlderMediaAuthorityDeliveriesParams{AuthorityKind: "tenant", AuthorityID: "deadline-ordered", AuthorityVersion: 2}); err != nil {
		t.Fatal(err)
	}
	if rows := claim(); len(rows) != 0 {
		t.Fatalf("deadline successor bypassed active predecessor: %v", rows)
	}
	if rows, err := q.MarkMediaAuthorityDeliveryAcknowledged(ctx, commodoredb.MarkMediaAuthorityDeliveryAcknowledgedParams{AuthorityKind: "tenant", AuthorityID: "deadline-ordered", AuthorityVersion: 1, CellID: "deadline-ordered"}); err != nil || rows != 1 {
		t.Fatalf("ack deadline predecessor: rows=%d err=%v", rows, err)
	}
	deadlineRows = claim()
	if len(deadlineRows) != 1 || deadlineRows[0].AuthorityID != "deadline-ordered" || deadlineRows[0].AuthorityVersion != 2 {
		t.Fatalf("deadline successor after predecessor acknowledgment = %v", deadlineRows)
	}
	if rows, err := q.MarkMediaAuthorityDeliveryAcknowledged(ctx, commodoredb.MarkMediaAuthorityDeliveryAcknowledgedParams{AuthorityKind: "tenant", AuthorityID: "deadline-ordered", AuthorityVersion: 2, CellID: "deadline-ordered"}); err != nil || rows != 1 {
		t.Fatalf("ack deadline successor: rows=%d err=%v", rows, err)
	}

	insertOrdinary := func(id string, version int64) {
		t.Helper()
		now := time.Now().UTC()
		if err := q.InsertMediaAuthorityVersion(ctx, commodoredb.InsertMediaAuthorityVersionParams{
			AuthorityKind: "tenant", AuthorityID: id, AuthorityVersion: version, PayloadSchemaVersion: 2,
			Payload: []byte{}, PayloadSha256: make([]byte, 32), SourceRevisions: []byte("[]"),
			IssuedAt: now, RefreshAfter: now, ValidUntil: now.Add(time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
		if rows, err := q.UpsertCurrentMediaAuthority(ctx, commodoredb.UpsertCurrentMediaAuthorityParams{AuthorityKind: "tenant", AuthorityID: id, AuthorityVersion: version}); err != nil || rows != 1 {
			t.Fatalf("set current authority %s/%d: rows=%d err=%v", id, version, rows, err)
		}
		if _, err := q.EnqueueMediaAuthorityDelivery(ctx, commodoredb.EnqueueMediaAuthorityDeliveryParams{AuthorityKind: "tenant", AuthorityID: id, AuthorityVersion: version, CellID: "ordered", SignedEnvelope: []byte{}}); err != nil {
			t.Fatal(err)
		}
	}
	claimOrdinary := func() []commodoredb.ClaimMediaAuthorityDeliveriesRow {
		t.Helper()
		rows, err := q.ClaimMediaAuthorityDeliveries(ctx, commodoredb.ClaimMediaAuthorityDeliveriesParams{CellID: "ordered", LeaseMs: 60000, BatchSize: 10})
		if err != nil {
			t.Fatal(err)
		}
		return rows
	}

	insertOrdinary("ordered-success", 1)
	rowsOrdinary := claimOrdinary()
	if len(rowsOrdinary) != 1 || rowsOrdinary[0].AuthorityID != "ordered-success" || rowsOrdinary[0].AuthorityVersion != 1 {
		t.Fatalf("initial ordered delivery = %v", rowsOrdinary)
	}
	insertOrdinary("ordered-success", 2)
	if _, err := q.SupersedeOlderMediaAuthorityDeliveries(ctx, commodoredb.SupersedeOlderMediaAuthorityDeliveriesParams{AuthorityKind: "tenant", AuthorityID: "ordered-success", AuthorityVersion: 2}); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := db.QueryRowContext(ctx, "SELECT status FROM commodore.media_authority_deliveries WHERE authority_kind='tenant' AND authority_id='ordered-success' AND authority_version=1 AND cell_id='ordered'").Scan(&status); err != nil || status != "delivering" {
		t.Fatalf("active predecessor status = %q, err=%v", status, err)
	}
	if rows := claimOrdinary(); len(rows) != 0 {
		t.Fatalf("successor bypassed active predecessor: %v", rows)
	}
	if rows, err := q.MarkMediaAuthorityDeliveryAcknowledged(ctx, commodoredb.MarkMediaAuthorityDeliveryAcknowledgedParams{AuthorityKind: "tenant", AuthorityID: "ordered-success", AuthorityVersion: 1, CellID: "ordered"}); err != nil || rows != 1 {
		t.Fatalf("ack predecessor: rows=%d err=%v", rows, err)
	}
	rowsOrdinary = claimOrdinary()
	if len(rowsOrdinary) != 1 || rowsOrdinary[0].AuthorityID != "ordered-success" || rowsOrdinary[0].AuthorityVersion != 2 {
		t.Fatalf("successor after predecessor acknowledgment = %v", rowsOrdinary)
	}
	if rows, err := q.MarkMediaAuthorityDeliveryAcknowledged(ctx, commodoredb.MarkMediaAuthorityDeliveryAcknowledgedParams{AuthorityKind: "tenant", AuthorityID: "ordered-success", AuthorityVersion: 2, CellID: "ordered"}); err != nil || rows != 1 {
		t.Fatalf("ack successor: rows=%d err=%v", rows, err)
	}

	insertOrdinary("ordered-failure", 1)
	rowsOrdinary = claimOrdinary()
	if len(rowsOrdinary) != 1 || rowsOrdinary[0].AuthorityID != "ordered-failure" || rowsOrdinary[0].AuthorityVersion != 1 {
		t.Fatalf("initial failure delivery = %v", rowsOrdinary)
	}
	insertOrdinary("ordered-failure", 2)
	if _, err := q.SupersedeOlderMediaAuthorityDeliveries(ctx, commodoredb.SupersedeOlderMediaAuthorityDeliveriesParams{AuthorityKind: "tenant", AuthorityID: "ordered-failure", AuthorityVersion: 2}); err != nil {
		t.Fatal(err)
	}
	if rows, err := q.RecordMediaAuthorityDeliveryFailure(ctx, commodoredb.RecordMediaAuthorityDeliveryFailureParams{
		NextAttemptAt: time.Now().Add(time.Minute), LastError: sql.NullString{String: "stale delivery", Valid: true},
		AuthorityKind: "tenant", AuthorityID: "ordered-failure", AuthorityVersion: 1, CellID: "ordered",
	}); err != nil || rows != 1 {
		t.Fatalf("settle obsolete failure: rows=%d err=%v", rows, err)
	}
	if err := db.QueryRowContext(ctx, "SELECT status FROM commodore.media_authority_deliveries WHERE authority_kind='tenant' AND authority_id='ordered-failure' AND authority_version=1 AND cell_id='ordered'").Scan(&status); err != nil || status != "superseded" {
		t.Fatalf("failed predecessor status = %q, err=%v", status, err)
	}
	rowsOrdinary = claimOrdinary()
	if len(rowsOrdinary) != 1 || rowsOrdinary[0].AuthorityID != "ordered-failure" || rowsOrdinary[0].AuthorityVersion != 2 {
		t.Fatalf("successor after predecessor failure = %v", rowsOrdinary)
	}
	if rows, err := q.MarkMediaAuthorityDeliveryAcknowledged(ctx, commodoredb.MarkMediaAuthorityDeliveryAcknowledgedParams{AuthorityKind: "tenant", AuthorityID: "ordered-failure", AuthorityVersion: 2, CellID: "ordered"}); err != nil || rows != 1 {
		t.Fatalf("ack failure successor: rows=%d err=%v", rows, err)
	}

	insertOrdinary("ordered-expired", 1)
	rowsOrdinary = claimOrdinary()
	if len(rowsOrdinary) != 1 || rowsOrdinary[0].AuthorityID != "ordered-expired" || rowsOrdinary[0].AuthorityVersion != 1 {
		t.Fatalf("initial expiry delivery = %v", rowsOrdinary)
	}
	insertOrdinary("ordered-expired", 2)
	if _, err := q.SupersedeOlderMediaAuthorityDeliveries(ctx, commodoredb.SupersedeOlderMediaAuthorityDeliveriesParams{AuthorityKind: "tenant", AuthorityID: "ordered-expired", AuthorityVersion: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE commodore.media_authority_deliveries SET lease_expires_at=NOW()-INTERVAL '1 second' WHERE authority_kind='tenant' AND authority_id='ordered-expired' AND authority_version=1 AND cell_id='ordered'"); err != nil {
		t.Fatal(err)
	}
	if rows, err := q.SupersedeExpiredObsoleteMediaAuthorityDeliveries(ctx, 100); err != nil || rows < 1 {
		t.Fatalf("settle expired obsolete deliveries: rows=%d err=%v", rows, err)
	}
	if err := db.QueryRowContext(ctx, "SELECT status FROM commodore.media_authority_deliveries WHERE authority_kind='tenant' AND authority_id='ordered-expired' AND authority_version=1 AND cell_id='ordered'").Scan(&status); err != nil || status != "superseded" {
		t.Fatalf("expired predecessor status = %q, err=%v", status, err)
	}
	rowsOrdinary = claimOrdinary()
	if len(rowsOrdinary) != 1 || rowsOrdinary[0].AuthorityID != "ordered-expired" || rowsOrdinary[0].AuthorityVersion != 2 {
		t.Fatalf("successor after predecessor lease expiry = %v", rowsOrdinary)
	}
}
