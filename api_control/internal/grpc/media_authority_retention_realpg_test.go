//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
)

func TestMediaAuthorityRetention_RealPG(t *testing.T) {
	testMediaAuthorityRetention(t, startCommodoreRealPG(t))
}

func TestMediaAuthorityRetention_RealYugabyte(t *testing.T) {
	testMediaAuthorityRetention(t, startPlacementDeliveryYugabyte(t, "authority_retention"))
}

func testMediaAuthorityRetention(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	now := time.Now().UTC()
	insertVersion := func(id string, validUntil time.Time) {
		t.Helper()
		if _, err := db.ExecContext(ctx, `
			INSERT INTO commodore.media_authority_versions
			    (authority_kind, authority_id, authority_version, payload_schema_version,
			     payload, payload_sha256, source_revisions, issued_at, refresh_after, valid_until)
			VALUES ('tenant', $1, 1, 2, ''::bytea, decode(repeat('00', 32), 'hex'), '[]'::jsonb,
			        $2 - INTERVAL '1 hour', $2 - INTERVAL '30 minutes', $2)
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
	stats, err := q.ListMediaAuthorityDeliveryStats(ctx)
	if err != nil || len(stats) != 1 || stats[0].AuthorityKind != "tenant" {
		t.Fatalf("current-first delivery stats after retention = %#v, %v", stats, err)
	}
}
