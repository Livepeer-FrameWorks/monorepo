//go:build schema_verify

package datamigrations

import (
	"database/sql"
	"testing"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/datamigrate"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"github.com/google/uuid"
)

func TestAuthSessionBackfillUsesUTC_RealPG(t *testing.T) {
	testAuthBackfillUTC(t, startAuthCapsMigrationPG(t))
}
func TestAuthSessionBackfillUsesUTC_RealYugabyte(t *testing.T) {
	db, ok := dockerpg.OpenSharedYugabyteBaseline(t, "commodore_backfill_utc", "commodore")
	if !ok {
		t.Skip("requires the shared Yugabyte contract fixture")
	}
	testAuthBackfillUTC(t, db)
}
func testAuthBackfillUTC(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := t.Context()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `SET TIME ZONE 'America/New_York'`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := db.ExecContext(ctx, `SET TIME ZONE 'UTC'`); err != nil {
			t.Error(err)
		}
	}()
	tenant, user, expired, live := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	if _, err := db.ExecContext(ctx, `INSERT INTO commodore.users(id,tenant_id,email,is_active,verified) VALUES($1,$2,'backfill-utc@example.test',true,true)`, user, tenant); err != nil {
		t.Fatal(err)
	}
	for _, token := range []struct {
		id    string
		hours int
	}{{expired, -1}, {live, 1}} {
		if _, err := db.ExecContext(ctx, `INSERT INTO commodore.refresh_tokens(id,tenant_id,user_id,token_hash,created_at,expires_at,revoked)
  VALUES($1,$2,$3,$4,(NOW() AT TIME ZONE 'UTC')-INTERVAL '2 hours',(NOW() AT TIME ZONE 'UTC')+$5*INTERVAL '1 hour',false)`, token.id, tenant, user, token.id, token.hours); err != nil {
			t.Fatal(err)
		}
	}
	preview, err := runAuthSessions(ctx, db, datamigrate.RunOptions{DryRun: true, BatchSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if preview.Scanned != 1 {
		t.Errorf("session timezone changed live-head preview: got %d, want 1", preview.Scanned)
	}
	progress, err := runAuthSessions(ctx, db, datamigrate.RunOptions{BatchSize: 10})
	if err != nil {
		t.Fatal(err)
	}
	if progress.Scanned != 1 {
		t.Errorf("session timezone adopted expired refresh proof: scanned=%d, want 1", progress.Scanned)
	}
	var untouched bool
	if err := db.QueryRowContext(ctx, `SELECT session_id IS NULL FROM commodore.refresh_tokens WHERE tenant_id=$1 AND user_id=$2 AND id=$3`, tenant, user, expired).Scan(&untouched); err != nil {
		t.Fatal(err)
	}
	if !untouched {
		t.Error("expired UTC refresh proof was adopted")
	}
	if err := verifyAuthSessions(ctx, db); err != nil {
		t.Errorf("expired UTC history blocked completion: %v", err)
	}
}
