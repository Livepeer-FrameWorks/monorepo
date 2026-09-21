//go:build schema_verify

package grpc

import (
	"context"
	"crypto/ed25519"
	"database/sql"
	"slices"
	"testing"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/logging"
	sharedauthority "github.com/Livepeer-FrameWorks/monorepo/pkg/mediaauthority"
	mediapb "github.com/Livepeer-FrameWorks/monorepo/pkg/proto/media_authority"
	"google.golang.org/protobuf/proto"
)

func TestMediaAuthorityRetention_RealPG(t *testing.T) {
	testMediaAuthorityRetention(t, startCommodoreRealPG(t))
}

func TestMediaAuthorityRetention_RealYugabyte(t *testing.T) {
	testMediaAuthorityRetention(t, startPlacementDeliveryYugabyte(t, "authority_retention"))
}

func testMediaAuthorityRetention(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	now := time.Now().UTC()
	insertVersion := func(id string, validUntil time.Time) {
		t.Helper()
		if _, err := db.ExecContext(ctx, `
			INSERT INTO commodore.media_authority_versions
			    (authority_kind, authority_id, authority_version, payload_schema_version,
			     payload, payload_sha256, source_revisions, issued_at, refresh_after, valid_until)
			VALUES ('tenant', $1, 1, 2, ''::bytea, decode(repeat('00', 32), 'hex'), '[]'::jsonb,
			        $2::timestamptz - INTERVAL '1 hour', $2::timestamptz - INTERVAL '30 minutes', $2::timestamptz)
		`, id, validUntil); err != nil {
			t.Fatalf("insert authority %s: %v", id, err)
		}
	}
	insertDelivery := func(id, status string) {
		t.Helper()
		if _, err := db.ExecContext(ctx, `
			INSERT INTO commodore.media_authority_deliveries
			    (authority_kind, authority_id, authority_version, cell_id, signed_envelope, status)
			VALUES ('tenant', $1, 1, 'cell-a', ''::bytea, $2)
		`, id, status); err != nil {
			t.Fatalf("insert delivery %s: %v", id, err)
		}
	}
	old := now.Add(-mediaAuthorityHistoryRetention - time.Hour)
	recent := now.Add(-mediaAuthorityHistoryRetention + time.Hour)
	insertVersion("old-terminal", old)
	insertDelivery("old-terminal", "acknowledged")
	insertVersion("old-current", old)
	insertDelivery("old-current", "acknowledged")
	insertVersion("old-pending", old)
	insertDelivery("old-pending", "pending")
	insertVersion("old-orphan", old)
	insertVersion("recent-terminal", recent)
	insertDelivery("recent-terminal", "superseded")
	if _, err := db.ExecContext(ctx, `
		INSERT INTO commodore.media_authority_current(authority_kind, authority_id, authority_version)
		VALUES ('tenant', 'old-current', 1)
	`); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		id        string
		completed time.Time
	}{
		{"old-inbox", now.Add(-mediaAuthorityInboxRetention - time.Hour)},
		{"recent-inbox", now.Add(-mediaAuthorityInboxRetention + time.Hour)},
	} {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO commodore.media_authority_refresh_inbox
			    (source_service, source_event_id, tenant_id, reason, status, completed_at)
			VALUES ('commodore', $1, '10000000-0000-0000-0000-000000000001', 'test', 'completed', $2)
		`, fixture.id, fixture.completed); err != nil {
			t.Fatal(err)
		}
	}

	server := &CommodoreServer{db: db}
	if err := server.sweepMediaAuthorityRetention(ctx, now); err != nil {
		t.Fatal(err)
	}
	for _, check := range []struct {
		id   string
		want int
	}{
		{"old-terminal", 0},
		{"old-orphan", 0},
		{"old-current", 1},
		{"old-pending", 1},
		{"recent-terminal", 1},
	} {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM commodore.media_authority_versions WHERE authority_id = $1`, check.id).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != check.want {
			t.Errorf("authority %s rows = %d, want %d", check.id, count, check.want)
		}
	}
	for _, check := range []struct {
		id   string
		want int
	}{{"old-inbox", 0}, {"recent-inbox", 1}} {
		var count int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM commodore.media_authority_refresh_inbox WHERE source_event_id = $1`, check.id).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != check.want {
			t.Errorf("refresh inbox %s rows = %d, want %d", check.id, count, check.want)
		}
	}
	q := commodoredb.New(db)
	// The backlog gauges read only deliveries that are not settled. What retention
	// left behind is settled, so there is nothing to report, and the query must
	// still run against the retained history.
	stats, err := q.ListMediaAuthorityDeliveryStats(ctx)
	if err != nil {
		t.Fatalf("delivery stats after retention: %v", err)
	}
	for _, row := range stats {
		if row.PendingCount != 0 || row.RejectedCount != 0 {
			t.Fatalf("settled deliveries reported as backlog after retention: %#v", row)
		}
	}
	t.Run("removed cell corrections end without resetting version fences", func(t *testing.T) {
		s := &CommodoreServer{db: db, logger: logging.NewLogger(), mediaAuthorityKeyID: "retention", mediaAuthorityPrivateKey: ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))}
		object := useStreamPayload("retire-cell")
		id := "live_stream:" + object.GetLiveStream().GetStreamId()
		revisions := []*mediapb.AuthoritySourceRevision{{Service: "commodore", Revision: "retire-cell"}}
		issued := time.Now().UTC().Truncate(time.Second)
		originalUntil := issued.Add(time.Hour)
		publish := func(cells []string, until time.Time) {
			t.Helper()
			if err := s.persistMediaObjectAuthority(ctx, id, object, cells, revisions, issued, until); err != nil {
				t.Fatal(err)
			}
		}
		publish([]string{"cell-a", "cell-b"}, originalUntil)
		publish([]string{"cell-a", "cell-b"}, issued.Add(30*time.Minute))
		if err := q.UpsertMediaAuthorityDistribution(ctx, commodoredb.UpsertMediaAuthorityDistributionParams{AuthorityKind: "media_object", AuthorityID: id, CellID: "cell-b", AuthorityVersion: 2}); err != nil {
			t.Fatal(err)
		}
		publish([]string{"cell-a"}, issued.Add(24*time.Hour))
		for round := range 3 {
			object.OriginClusterId = []string{"origin-a", "origin-b", "origin-c"}[round]
			publish([]string{"cell-a"}, issued.Add(24*time.Hour))
			var horizon time.Time
			if err := db.QueryRowContext(ctx, `SELECT correction_until FROM commodore.media_authority_targets WHERE authority_kind = 'media_object' AND authority_id = $1 AND cell_id = 'cell-b'`, id).Scan(&horizon); err != nil {
				t.Fatal(err)
			}
			if !horizon.Equal(originalUntil) {
				t.Fatalf("retirement horizon extended or lost restored predecessor: %s, want %s", horizon, originalUntil)
			}
			current, err := q.GetCurrentMediaAuthorityPublication(ctx, commodoredb.GetCurrentMediaAuthorityPublicationParams{AuthorityKind: "media_object", AuthorityID: id})
			if err != nil || !current.ValidUntil.Equal(issued.Add(24*time.Hour)) {
				t.Fatalf("retirement shortened healthy recipient validity: %+v %v", current, err)
			}
			for _, cell := range []string{"cell-a", "cell-b"} {
				var encoded []byte
				var ceiling sql.NullTime
				if err := db.QueryRowContext(ctx, `SELECT signed_envelope, correction_until FROM commodore.media_authority_deliveries WHERE authority_kind='media_object' AND authority_id=$1 AND authority_version=$2 AND cell_id=$3`, id, current.AuthorityVersion, cell).Scan(&encoded, &ceiling); err != nil {
					t.Fatal(err)
				}
				signed := &mediapb.SignedAuthorityEnvelope{}
				if err := proto.Unmarshal(encoded, signed); err != nil {
					t.Fatal(err)
				}
				if _, err := sharedauthority.Verify(signed, sharedauthority.TrustSet{"retention": s.mediaAuthorityPrivateKey.Public().(ed25519.PublicKey)}, cell, time.Now()); err != nil {
					t.Fatal(err)
				}
				want := current.ValidUntil
				if cell == "cell-b" {
					want = originalUntil
				}
				if !signed.Envelope.ValidUntil.AsTime().Equal(want) || ceiling.Valid != (cell == "cell-b") || (ceiling.Valid && !ceiling.Time.Equal(want)) {
					t.Fatalf("%s envelope and stored ceiling disagree: %v %v", cell, signed.Envelope.ValidUntil, ceiling)
				}
			}
			active, err := q.ListActiveMediaAuthorityCells(ctx, commodoredb.ListActiveMediaAuthorityCellsParams{AuthorityKind: "media_object", AuthorityID: id})
			if err != nil || !slices.Equal(active, []string{"cell-a"}) {
				t.Fatalf("correction cell promoted to active: %v %v", active, err)
			}
		}
		// The sweep's cutoff is advanced past retirement while publication still
		// holds its target snapshot, forcing the prune/publish interleaving.
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		defer tx.Rollback()
		locked := commodoredb.New(tx)
		if _, err := lockMediaAuthorityTargetHorizons(ctx, locked, "media_object", id); err != nil {
			t.Fatal(err)
		}
		pruned, err := q.DeleteRetiredMediaAuthorityTargets(ctx, commodoredb.DeleteRetiredMediaAuthorityTargetsParams{ExpiredBefore: sql.NullTime{Time: originalUntil.Add(time.Second), Valid: true}, BatchSize: 100})
		if err != nil || pruned != 0 {
			t.Fatalf("sweep deleted publication's correction recipient: %d %v", pruned, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}

		// Regrant with the same delivery-cell set must still publish a full lease.
		publish([]string{"cell-a", "cell-b"}, issued.Add(24*time.Hour))
		var regranted sql.NullTime
		if err := db.QueryRowContext(ctx, `SELECT d.correction_until FROM commodore.media_authority_deliveries d JOIN commodore.media_authority_current c USING (authority_kind,authority_id,authority_version) WHERE d.authority_kind='media_object' AND d.authority_id=$1 AND d.cell_id='cell-b'`, id).Scan(&regranted); err != nil || regranted.Valid {
			t.Fatalf("regrant reused capped correction: %v %v", regranted, err)
		}
		publish([]string{"cell-a"}, issued.Add(24*time.Hour))
		// Advance only this fixture beyond every possible copy's validity.
		if _, err := db.ExecContext(ctx, `UPDATE commodore.media_authority_versions SET issued_at = issued_at - INTERVAL '2 days', refresh_after = refresh_after - INTERVAL '2 days', valid_until = valid_until - INTERVAL '2 days' WHERE authority_kind = 'media_object' AND authority_id = $1`, id); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `UPDATE commodore.media_authority_targets SET correction_until = correction_until - INTERVAL '2 days' WHERE authority_kind = 'media_object' AND authority_id = $1`, id); err != nil {
			t.Fatal(err)
		}
		publish([]string{"cell-a"}, issued.Add(time.Hour))
		if err := s.sweepMediaAuthorityRetention(ctx, time.Now()); err != nil {
			t.Fatal(err)
		}
		prior, err := q.ListMediaAuthorityPriorCells(ctx, commodoredb.ListMediaAuthorityPriorCellsParams{AuthorityKind: "media_object", AuthorityID: id})
		if err != nil || !slices.Equal(prior, []string{"cell-a"}) {
			t.Fatalf("expired retired target remains: %v %v", prior, err)
		}
		var targets, floors int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM commodore.media_authority_targets WHERE authority_kind = 'media_object' AND authority_id = $1 AND cell_id = 'cell-b'`, id).Scan(&targets); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM commodore.media_authority_distribution WHERE authority_kind = 'media_object' AND authority_id = $1 AND cell_id = 'cell-b'`, id).Scan(&floors); err != nil {
			t.Fatal(err)
		}
		if targets != 0 || floors != 1 {
			t.Fatalf("target not pruned or recovery fence discarded: targets=%d floors=%d", targets, floors)
		}
		publish([]string{"cell-a", "cell-b"}, issued.Add(2*time.Hour))
		active, err := q.ListActiveMediaAuthorityCells(ctx, commodoredb.ListActiveMediaAuthorityCellsParams{AuthorityKind: "media_object", AuthorityID: id})
		if err != nil || !slices.Equal(active, []string{"cell-a", "cell-b"}) {
			t.Fatalf("explicit regrant could not reactivate cell: %v %v", active, err)
		}
	})
	t.Run("retirement cannot pin tenant renewal", func(t *testing.T) {
		s := &CommodoreServer{db: db, logger: logging.NewLogger(), mediaAuthorityKeyID: "renewal", mediaAuthorityPrivateKey: ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))}
		tenant := obligationTenantPayload()
		tenant.TenantId = "10000000-0000-4000-8000-000000000099"
		issued := time.Now().UTC().Add(-12 * time.Hour).Truncate(time.Second)
		revisions := []*mediapb.AuthoritySourceRevision{{Service: "quartermaster", Revision: "retirement"}}
		for _, publication := range []struct {
			cells         []string
			issued, until time.Time
		}{
			{[]string{"renew-a", "renew-b"}, issued, issued.Add(13 * time.Hour)},
			{[]string{"renew-a"}, issued, issued.Add(24 * time.Hour)},
			{[]string{"renew-a"}, issued.Add(12 * time.Hour), issued.Add(36 * time.Hour)},
		} {
			if err := s.persistTenantAuthority(ctx, tenant, publication.cells, revisions, publication.issued, publication.until); err != nil {
				t.Fatal(err)
			}
		}
		current, err := q.GetCurrentMediaAuthorityPublication(ctx, commodoredb.GetCurrentMediaAuthorityPublicationParams{AuthorityKind: "tenant", AuthorityID: tenant.TenantId})
		if err != nil || current.AuthorityVersion != 3 || !current.ValidUntil.Equal(issued.Add(36*time.Hour)) {
			t.Fatalf("healthy tenant lease was not renewed: %+v %v", current, err)
		}
		if err := s.persistTenantAuthority(ctx, tenant, []string{"renew-a", "renew-b"}, revisions, issued.Add(12*time.Hour), current.ValidUntil); err != nil {
			t.Fatal(err)
		}
		var ceiling sql.NullTime
		if err := db.QueryRowContext(ctx, `SELECT d.correction_until FROM commodore.media_authority_deliveries d JOIN commodore.media_authority_current c USING (authority_kind,authority_id,authority_version) WHERE d.authority_kind='tenant' AND d.authority_id=$1 AND d.cell_id='renew-b'`, tenant.TenantId).Scan(&ceiling); err != nil || ceiling.Valid {
			t.Fatalf("tenant regrant retained the correction ceiling: %v %v", ceiling, err)
		}
	})
	t.Run("expired correction is not replayed or reported missing", func(t *testing.T) {
		s := &CommodoreServer{db: db, logger: logging.NewLogger(), mediaAuthorityKeyID: "expiry", mediaAuthorityPrivateKey: ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))}
		object := useStreamPayload("expiry-cell")
		object.GetLiveStream().StreamId = "10000000-0000-4000-8000-000000000098"
		id := "live_stream:" + object.GetLiveStream().GetStreamId()
		issued := time.Now().UTC().Truncate(time.Second)
		revisions := []*mediapb.AuthoritySourceRevision{{Service: "commodore", Revision: "expiry"}}
		for _, cells := range [][]string{{"expiry-a", "expiry-b"}, {"expiry-a"}} {
			if err := s.persistMediaObjectAuthority(ctx, id, object, cells, revisions, issued, issued.Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
		}
		// Advance only the correction delivery clock; the version and healthy
		// cell still have a full hour, and the earlier ACK may have been lost.
		if _, err := db.ExecContext(ctx, `UPDATE commodore.media_authority_deliveries SET correction_until=NOW()-INTERVAL '1 second', status='acknowledged' WHERE authority_id=$1 AND cell_id='expiry-b'`, id); err != nil {
			t.Fatal(err)
		}
		if err := q.UpsertMediaAuthorityDistribution(ctx, commodoredb.UpsertMediaAuthorityDistributionParams{AuthorityKind: "media_object", AuthorityID: id, CellID: "expiry-b", AuthorityVersion: 1}); err != nil {
			t.Fatal(err)
		}
		got, err := q.ListCurrentMediaAuthorityEnvelopesForCell(ctx, commodoredb.ListCurrentMediaAuthorityEnvelopesForCellParams{CellID: "expiry-b", ObjectAuthorityID: id})
		if err != nil || len(got) != 0 {
			t.Fatalf("fetch returned expired correction: %v %v", got, err)
		}
		replayed, err := q.RequeueCurrentMediaAuthoritiesForCell(ctx, "expiry-b")
		if err != nil || replayed != 0 {
			t.Fatalf("replay queued expired correction: %d %v", replayed, err)
		}
		page, err := q.ReconcileMediaAuthorityPage(ctx, commodoredb.ReconcileMediaAuthorityPageParams{CellID: "expiry-b", Held: []byte(`[]`), FinalPage: true, AsOf: time.Now(), AcknowledgedBefore: time.Now().Add(time.Minute)})
		if err != nil {
			t.Fatal(err)
		}
		for _, row := range page {
			if row.Regressed || row.Raced {
				t.Fatalf("expired correction generated recovery churn: %+v", row)
			}
		}
		if _, err := db.ExecContext(ctx, `UPDATE commodore.media_authority_deliveries SET status='pending' WHERE authority_id=$1 AND cell_id='expiry-b'`, id); err != nil {
			t.Fatal(err)
		}
		claims, err := q.ClaimMediaAuthorityDeliveries(ctx, commodoredb.ClaimMediaAuthorityDeliveriesParams{CellID: "expiry-b", BatchSize: 10, LeaseMs: 1000})
		if err != nil || len(claims) != 0 {
			t.Fatalf("claimed expired correction: %v %v", claims, err)
		}
		if _, err := q.SettleExpiredMediaAuthorityDeliveries(ctx, 100); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `UPDATE commodore.media_authority_deliveries SET status='rejected' WHERE authority_id=$1 AND cell_id='expiry-b'`, id); err != nil {
			t.Fatal(err)
		}
		var actionable int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM commodore.media_authority_actionable_rejections WHERE authority_id=$1`, id).Scan(&actionable); err != nil || actionable != 0 {
			t.Fatalf("expired correction still pages: %d %v", actionable, err)
		}
	})
}
