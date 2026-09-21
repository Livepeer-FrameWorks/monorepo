//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/ctxkeys"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	commodorepb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/commodore"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestMediaAuthorityDeliveryQueue_RealPG(t *testing.T) {
	testMediaAuthorityDeliveryQueue(t, startCommodoreRealPG(t))
}

func TestMediaAuthorityDeliveryQueue_RealYugabyte(t *testing.T) {
	testMediaAuthorityDeliveryQueue(t, startPlacementDeliveryYugabyte(t, "authority_delivery_queue"))
}

// What a cell is sent, in what order, and when it is sent nothing: a cell that
// asks for a replay must not hold back a change behind its own catch-up, a
// version past its validity is never sent, and a cell that holds exactly what it
// acknowledged has nothing repeated.
func testMediaAuthorityDeliveryQueue(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	q := commodoredb.New(db)
	coreClock := func() time.Time {
		t.Helper()
		at, err := q.MediaAuthorityRecoveryTime(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return at
	}
	publish := func(id string, version int64, cell string, validity time.Duration) {
		t.Helper()
		now := time.Now().UTC()
		if err := q.InsertMediaAuthorityVersion(ctx, commodoredb.InsertMediaAuthorityVersionParams{
			AuthorityKind: "tenant", AuthorityID: id, AuthorityVersion: version, PayloadSchemaVersion: 1,
			Payload: []byte{}, PayloadSha256: make([]byte, 32), SourceRevisions: []byte("[]"),
			IssuedAt: now, RefreshAfter: now.Add(validity / 2), ValidUntil: now.Add(validity),
		}); err != nil {
			t.Fatal(err)
		}
		if rows, err := q.UpsertCurrentMediaAuthority(ctx, commodoredb.UpsertCurrentMediaAuthorityParams{AuthorityKind: "tenant", AuthorityID: id, AuthorityVersion: version}); err != nil || rows != 1 {
			t.Fatalf("set current %s/%d: rows=%d err=%v", id, version, rows, err)
		}
		if _, err := q.SupersedeOlderMediaAuthorityDeliveries(ctx, commodoredb.SupersedeOlderMediaAuthorityDeliveriesParams{AuthorityKind: "tenant", AuthorityID: id, AuthorityVersion: version}); err != nil {
			t.Fatal(err)
		}
		if _, err := q.EnqueueMediaAuthorityDelivery(ctx, commodoredb.EnqueueMediaAuthorityDeliveryParams{AuthorityKind: "tenant", AuthorityID: id, AuthorityVersion: version, CellID: cell, SignedEnvelope: []byte{}}); err != nil {
			t.Fatal(err)
		}
		if err := q.UpsertMediaAuthorityTarget(ctx, commodoredb.UpsertMediaAuthorityTargetParams{AuthorityKind: "tenant", AuthorityID: id, AuthorityVersion: version, CellID: cell}); err != nil {
			t.Fatal(err)
		}
	}
	claim := func(cell string, batch int32) []commodoredb.ClaimMediaAuthorityDeliveriesRow {
		t.Helper()
		rows, err := q.ClaimMediaAuthorityDeliveries(ctx, commodoredb.ClaimMediaAuthorityDeliveriesParams{CellID: cell, LeaseMs: 60000, BatchSize: batch})
		if err != nil {
			t.Fatal(err)
		}
		return rows
	}
	acknowledge := func(rows []commodoredb.ClaimMediaAuthorityDeliveriesRow) {
		t.Helper()
		for _, row := range rows {
			if settled, err := q.MarkMediaAuthorityDeliveryAcknowledged(ctx, commodoredb.MarkMediaAuthorityDeliveryAcknowledgedParams{
				AuthorityKind: row.AuthorityKind, AuthorityID: row.AuthorityID, AuthorityVersion: row.AuthorityVersion, CellID: row.CellID,
			}); err != nil || settled != 1 {
				t.Fatalf("acknowledge %s: rows=%d err=%v", row.AuthorityID, settled, err)
			}
			if err := q.UpsertMediaAuthorityDistribution(ctx, commodoredb.UpsertMediaAuthorityDistributionParams{
				AuthorityKind: row.AuthorityKind, AuthorityID: row.AuthorityID, AuthorityVersion: row.AuthorityVersion, CellID: row.CellID,
			}); err != nil {
				t.Fatal(err)
			}
		}
	}

	// The cell holds three authorities it acknowledged.
	for _, id := range []string{"held-1", "held-2", "held-3"} {
		publish(id, 1, "cell-a", time.Hour)
	}
	acknowledge(claim("cell-a", 10))

	// What is on record as acknowledged by the cell, summarized the way the cell
	// summarizes what it holds. The order the rows come back in does not matter.
	summarize := func() sharedauthority.HeldSummary {
		t.Helper()
		rows, err := q.ListAcknowledgedMediaAuthoritiesForCell(ctx, commodoredb.ListAcknowledgedMediaAuthoritiesForCellParams{CellID: "cell-a", AsOf: time.Now().UTC(), PageSize: sharedauthority.RecoveryPageSize})
		if err != nil {
			t.Fatal(err)
		}
		var summary sharedauthority.HeldSummary
		for _, row := range rows {
			summary.Add(row.AuthorityKind, row.AuthorityID, row.AuthorityVersion)
		}
		return summary
	}
	onRecord := summarize()
	var held sharedauthority.HeldSummary
	for _, id := range []string{"held-3", "held-1", "held-2"} {
		held.Add("tenant", id, 1)
	}
	if !onRecord.Matches(held.Count, held.Digest()) {
		t.Fatalf("a cell holding exactly what it acknowledged does not match the record (count %d vs %d)", held.Count, onRecord.Count)
	}
	var lost sharedauthority.HeldSummary
	lost.Add("tenant", "held-1", 1)
	if onRecord.Matches(lost.Count, lost.Digest()) {
		t.Fatal("a cell that lost two authorities matches the record")
	}

	// A replay is served after a fresh delivery to the same cell, however many
	// rows it re-opens and whenever they were created.
	if requeued, err := q.RequeueCurrentMediaAuthoritiesForCell(ctx, "cell-a"); err != nil || requeued != 3 {
		t.Fatalf("replay re-opened %d deliveries (err %v), want 3", requeued, err)
	}
	publish("changed", 1, "cell-a", time.Hour)
	first := claim("cell-a", 1)
	if len(first) != 1 || first[0].AuthorityID != "changed" {
		t.Fatalf("a change waited behind the cell's own replay: %+v", first)
	}
	acknowledge(first)
	acknowledge(claim("cell-a", 10))

	// A version that ran out before it could be delivered is not sent, is not
	// repeated, leaves the queue, and is no longer on record as held.
	publish("expiring", 1, "cell-a", time.Hour)
	if _, err := db.ExecContext(ctx, `
		UPDATE commodore.media_authority_versions
		SET issued_at = NOW() - INTERVAL '3 hours', refresh_after = NOW() - INTERVAL '2 hours', valid_until = NOW() - INTERVAL '1 hour'
		WHERE authority_id IN ('expiring', 'held-1')`); err != nil {
		t.Fatal(err)
	}
	if rows := claim("cell-a", 10); len(rows) != 0 {
		t.Fatalf("an expired version was claimed for delivery: %+v", rows)
	}
	if requeued, err := q.RequeueCurrentMediaAuthoritiesForCell(ctx, "cell-a"); err != nil || requeued != 3 {
		t.Fatalf("replay re-opened %d deliveries (err %v), want the 3 that are still valid", requeued, err)
	}
	if settled, err := q.SettleExpiredMediaAuthorityDeliveries(ctx, 100); err != nil || settled != 1 {
		t.Fatalf("settled %d expired deliveries (err %v), want 1", settled, err)
	}
	cells, err := q.ListMediaAuthorityDeliveryCells(ctx)
	if err != nil || len(cells) != 1 || cells[0] != "cell-a" {
		t.Fatalf("cells with deliveries waiting = %v (err %v)", cells, err)
	}
	acknowledge(claim("cell-a", 10))
	if cells, err = q.ListMediaAuthorityDeliveryCells(ctx); err != nil || len(cells) != 0 {
		t.Fatalf("cells with deliveries waiting after the queue drained = %v (err %v)", cells, err)
	}
	if got := summarize(); got.Count != 3 {
		t.Fatalf("%d authorities on record as held, want 3 (held-2, held-3, changed)", got.Count)
	}

	t.Run("publishing and delivering do not imply a restored cell", func(t *testing.T) {
		publish("advancing", 1, "advancing-cell", time.Hour)
		acknowledge(claim("advancing-cell", 10))
		publish("advancing", 2, "advancing-cell", time.Hour)
		server := &CommodoreServer{db: db}
		serviceCtx := context.WithValue(ctx, ctxkeys.KeyAuthType, "service")
		request := func(version int64) *commodorepb.RequestMediaAuthorityReplayResponse {
			t.Helper()
			var held sharedauthority.HeldSummary
			held.Add("tenant", "advancing", version)
			response, err := server.RequestMediaAuthorityReplay(serviceCtx, &commodorepb.RequestMediaAuthorityReplayRequest{
				ControlCellId: "advancing-cell", HeldCount: held.Count, HeldDigest: held.Digest(), AsOf: timestamppb.Now(),
			})
			if err != nil {
				t.Fatal(err)
			}
			return response
		}
		if response := request(1); !response.GetSummaryChecked() || !response.GetHeldMatches() || response.GetRequeuedCount() != 0 {
			t.Fatalf("published successor invalidated an acknowledged copy: %+v", response)
		}
		pending := claim("advancing-cell", 10)
		if response := request(2); response.GetSummaryChecked() || response.GetRequeuedCount() != 0 {
			t.Fatalf("apply before acknowledgement triggered full replay: %+v", response)
		}
		inventory := func(version int64, at time.Time) *commodorepb.RequestMediaAuthorityReplayResponse {
			t.Helper()
			response, err := server.RequestMediaAuthorityReplay(serviceCtx, &commodorepb.RequestMediaAuthorityReplayRequest{
				ControlCellId: "advancing-cell", InventoryPage: true, FinalPage: true, AsOf: timestamppb.Now(), AcknowledgedBefore: timestamppb.New(at),
				Held: []*commodorepb.HeldMediaAuthority{{AuthorityKind: "tenant", AuthorityId: "advancing", AuthorityVersion: version}},
			})
			if err != nil {
				t.Fatal(err)
			}
			return response
		}
		if response := inventory(2, coreClock()); !response.GetSummaryChecked() || !response.GetHeldMatches() || len(response.GetConfirmed()) != 1 {
			t.Fatalf("current copy ahead of ACK was not confirmed: %+v", response)
		}
		var resets int
		if err := db.QueryRowContext(ctx, "SELECT count(*) FROM commodore.media_authority_cell_ack_resets WHERE cell_id = 'advancing-cell'").Scan(&resets); err != nil || resets != 0 {
			t.Fatalf("ordinary delivery reset cell trust: %d, %v", resets, err)
		}
		beforeAck := coreClock()
		acknowledge(pending)
		if response := inventory(1, beforeAck); response.GetSummaryChecked() || response.GetRequeuedCount() != 0 {
			t.Fatalf("concurrent ACK must be inconclusive without replay: %+v", response)
		}
		var previous sharedauthority.HeldSummary
		previous.Add("tenant", "advancing", 1)
		racing, err := server.RequestMediaAuthorityReplay(serviceCtx, &commodorepb.RequestMediaAuthorityReplayRequest{
			ControlCellId: "advancing-cell", HeldCount: previous.Count, HeldDigest: previous.Digest(), AsOf: timestamppb.New(beforeAck),
		})
		if err != nil || racing.GetSummaryChecked() || racing.GetRequeuedCount() != 0 {
			t.Fatalf("acknowledgement after summary triggered restore: %+v, %v", racing, err)
		}
		if response := request(2); !response.GetSummaryChecked() || !response.GetHeldMatches() {
			t.Fatalf("settled summary did not converge: %+v", response)
		}
	})

	t.Run("unrelated backed off delivery cannot hide restore", func(t *testing.T) {
		publish("lost", 1, "busy-cell", time.Hour)
		acknowledge(claim("busy-cell", 10))
		publish("busy", 1, "busy-cell", time.Hour)
		if _, err := db.ExecContext(ctx, `UPDATE commodore.media_authority_deliveries SET next_attempt_at = NOW() + INTERVAL '1 hour' WHERE cell_id = 'busy-cell' AND authority_id = 'busy'`); err != nil {
			t.Fatal(err)
		}
		server := &CommodoreServer{db: db}
		response, err := server.RequestMediaAuthorityReplay(context.WithValue(ctx, ctxkeys.KeyAuthType, "service"), &commodorepb.RequestMediaAuthorityReplayRequest{
			ControlCellId: "busy-cell", InventoryPage: true, FinalPage: true, AsOf: timestamppb.Now(), AcknowledgedBefore: timestamppb.New(coreClock()),
		})
		if err != nil || !response.GetSummaryChecked() || response.GetHeldMatches() || response.GetRequeuedCount() == 0 {
			t.Fatalf("missing acknowledged authority hidden by unrelated pending work: %+v, %v", response, err)
		}
	})

	t.Run("a large missing range has a bounded comparison result", func(t *testing.T) {
		if _, err := db.ExecContext(ctx, `INSERT INTO commodore.media_authority_versions
			(authority_kind, authority_id, authority_version, payload_schema_version, payload, payload_sha256, issued_at, refresh_after, valid_until)
			SELECT 'tenant', 'missing-range-' || n::text, 1, 1, ''::bytea, decode(repeat('00',32),'hex'), NOW(), NOW()+INTERVAL '1 hour', NOW()+INTERVAL '2 hours'
			FROM generate_series(1, 1200) AS n`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO commodore.media_authority_distribution
			(authority_kind, authority_id, cell_id, highest_acknowledged_version, first_acknowledged_at, last_acknowledged_at)
			SELECT 'tenant', 'missing-range-' || n::text, 'empty-cell', 1, NOW(), NOW() FROM generate_series(1, 1200) AS n`); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO commodore.media_authority_deliveries
			(authority_kind, authority_id, authority_version, cell_id, signed_envelope, status)
			SELECT 'tenant', 'missing-range-' || n::text, 1, 'empty-cell', ''::bytea, 'acknowledged'
			FROM generate_series(1, 1200) AS n`); err != nil {
			t.Fatal(err)
		}
		rows, err := q.ReconcileMediaAuthorityPage(ctx, commodoredb.ReconcileMediaAuthorityPageParams{
			CellID: "empty-cell", Held: []byte("[]"), AsOf: coreClock(), AcknowledgedBefore: coreClock(), FinalPage: true,
		})
		if err != nil || len(rows) != 1 || !rows[0].Regressed {
			t.Fatalf("missing range was not aggregated: rows=%d err=%v", len(rows), err)
		}
	})

	t.Run("expired rejection remains actionable only while a usable copy remains", func(t *testing.T) {
		publish("rejected", 1, "rejected-cell", 24*time.Hour)
		acknowledge(claim("rejected-cell", 10))
		publish("rejected", 2, "rejected-cell", time.Hour)
		if _, err := db.ExecContext(ctx, `UPDATE commodore.media_authority_deliveries SET status = 'rejected' WHERE authority_id = 'rejected' AND authority_version = 2`); err != nil {
			t.Fatal(err)
		}
		assertRejected := func(want int64) {
			t.Helper()
			var actionable int64
			if err := db.QueryRowContext(ctx, "SELECT count(*) FROM commodore.media_authority_actionable_rejections").Scan(&actionable); err != nil || actionable != want {
				t.Fatalf("doctor count=%d want=%d err=%v", actionable, want, err)
			}
			stats, err := q.ListMediaAuthorityDeliveryStats(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var count int64
			for _, stat := range stats {
				count += stat.RejectedCount
			}
			if count != want {
				t.Fatalf("metric count=%d want=%d", count, want)
			}
		}
		assertRejected(1)
		for _, version := range []int{2, 1} {
			if _, err := db.ExecContext(ctx, `UPDATE commodore.media_authority_versions
				SET issued_at = NOW() - INTERVAL '3 hours', refresh_after = NOW() - INTERVAL '2 hours', valid_until = NOW() - INTERVAL '1 hour'
				WHERE authority_id = 'rejected' AND authority_version = $1`, version); err != nil {
				t.Fatal(err)
			}
			assertRejected(int64(version - 1))
		}
		// A trusted acknowledgement bounds older copies; a restore revokes that
		// bound, so the same still-valid predecessor becomes actionable again.
		if _, err := db.ExecContext(ctx, `UPDATE commodore.media_authority_versions
			SET issued_at = NOW(), refresh_after = NOW() + INTERVAL '1 hour', valid_until = NOW() + INTERVAL '1 day'
			WHERE authority_id = 'rejected' AND authority_version = 1`); err != nil {
			t.Fatal(err)
		}
		if err := q.UpsertMediaAuthorityDistribution(ctx, commodoredb.UpsertMediaAuthorityDistributionParams{
			AuthorityKind: "tenant", AuthorityID: "rejected", CellID: "rejected-cell", AuthorityVersion: 2,
		}); err != nil {
			t.Fatal(err)
		}
		assertRejected(0)
		if err := q.MarkMediaAuthorityCellAcknowledgementsUntrusted(ctx, "rejected-cell"); err != nil {
			t.Fatal(err)
		}
		assertRejected(1)
		var state string
		if err := db.QueryRowContext(ctx, "SELECT status FROM commodore.media_authority_deliveries WHERE authority_id = 'rejected' AND authority_version = 2").Scan(&state); err != nil || state != "rejected" {
			t.Fatalf("rejection audit was lost: %s, %v", state, err)
		}
	})
}
