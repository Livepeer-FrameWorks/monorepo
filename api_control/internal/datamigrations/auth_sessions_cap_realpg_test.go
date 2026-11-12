//go:build schema_verify

package datamigrations

import (
	"context"
	"database/sql"
	"fmt"
	"os/exec"
	"testing"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	"frameworks/api_control/internal/sessionstore"
	dbsql "github.com/Livepeer-FrameWorks/monorepo/pkg/database/sql"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/datamigrate"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	_ "github.com/lib/pq"
)

func TestAuthSessionBackfillNormalizesCaps_RealPG(t *testing.T) {
	testAuthSessionBackfillCaps(t, startAuthCapsMigrationPG(t))
}
func TestAuthSessionBackfillNormalizesCaps_RealYugabyte(t *testing.T) {
	db, ok := dockerpg.OpenSharedYugabyteBaseline(t, "commodore_backfill_caps", "commodore")
	if !ok {
		t.Skip("requires the shared Yugabyte contract fixture")
	}
	testAuthSessionBackfillCaps(t, db)
}

func testAuthSessionBackfillCaps(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 180*time.Second)
	defer cancel()
	const tenant = "11000000-0000-4000-8000-00000000ca01"
	const user = "21000000-0000-4000-8000-00000000ca01"
	const other = "21000000-0000-4000-8000-00000000ca02"
	const oldest = "31000000-0000-4000-8000-00000000ca01"
	const otherSession = "31000000-0000-4000-8000-00000000ca02"
	const parent = "41000000-0000-4000-8000-00000000ca01"
	const head = "41000000-0000-4000-8000-00000000ca02"
	if _, err := db.ExecContext(ctx, `INSERT INTO commodore.users(id,tenant_id,email,verified,is_active) VALUES($1,$2,'cap-job-owner@example.test',true,true),($3,$2,'cap-job-other@example.test',true,true)`, user, tenant, other); err != nil {
		t.Fatal(err)
	}
	for _, owner := range []struct {
		id, session string
		rest        int
	}{{user, oldest, 249}, {other, otherSession, 9}} {
		if _, err := db.ExecContext(ctx, `INSERT INTO commodore.auth_sessions(id,tenant_id,user_id,auth_method,client_id,last_seen_at,idle_expires_at,absolute_expires_at)
VALUES($1,$2,$3,'unknown','unknown',NOW()-INTERVAL '2 hours',NOW()+INTERVAL '1 day',NOW()+INTERVAL '2 days')`, owner.session, tenant, owner.id); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO commodore.auth_sessions(tenant_id,user_id,auth_method,client_id,last_seen_at,idle_expires_at,absolute_expires_at)
SELECT $1,$2,'unknown','unknown',NOW()-INTERVAL '1 hour',NOW()+INTERVAL '1 day',NOW()+INTERVAL '2 days' FROM generate_series(1,$3)`, tenant, owner.id, owner.rest); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO commodore.refresh_tokens(id,tenant_id,user_id,session_id,token_hash,created_at,expires_at,revoked,replaced_by)
VALUES($1,$2,$3,$4,'cap-job-parent',NOW()-INTERVAL '2 hours',NOW()+INTERVAL '1 day',true,$5),
($5,$2,$3,NULL,'cap-job-head',NOW()-INTERVAL '1 hour',NOW()+INTERVAL '1 day',false,NULL)`, parent, tenant, user, oldest, head); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO commodore.refresh_tokens(tenant_id,user_id,session_id,token_hash,expires_at) VALUES($1,$2,$3,'cap-job-other',NOW()+INTERVAL '1 day')`, tenant, other, otherSession); err != nil {
		t.Fatal(err)
	}
	active := func(owner string) int {
		t.Helper()
		var count int
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM commodore.auth_sessions WHERE tenant_id=$1 AND user_id=$2 AND revoked_at IS NULL AND idle_expires_at>NOW()`, tenant, owner).Scan(&count); err != nil {
			t.Fatal(err)
		}
		return count
	}
	options := datamigrate.RunOptions{BatchSize: 10}
	checkedDryRun, done := false, false
	for step := 0; step < 40; step++ {
		before := active(user)
		progress, err := runAuthSessions(ctx, db, options)
		if err != nil {
			t.Fatal(err)
		}
		after := active(user)
		if before-after > 20 {
			t.Fatalf("backfill cap transaction exceeded 20 evictions: before=%d after=%d", before, after)
		}
		if active(other) != 10 {
			t.Fatal("backfill cap cleanup changed another owner")
		}
		if progress.Done {
			if after != 100 {
				t.Fatalf("backfill reported completion above the cap: active=%d", after)
			}
			done = true
			break
		}
		var pending bool
		if err = db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM commodore.refresh_tokens WHERE tenant_id=$1 AND user_id=$2 AND session_id IS NULL AND NOT revoked AND expires_at>NOW())`, tenant, user).Scan(&pending); err != nil {
			t.Fatal(err)
		}
		if !pending && after > 100 && !checkedDryRun {
			if err = verifyAuthSessions(ctx, db); err == nil {
				t.Error("backfill verification accepted an owner above the session cap")
			}
			tx, txErr := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
			if txErr != nil {
				t.Fatal(txErr)
			}
			dry, dryErr := runAuthSessions(ctx, tx, datamigrate.RunOptions{DryRun: true, BatchSize: 10})
			rollbackErr := tx.Rollback()
			if dryErr != nil || rollbackErr != nil || dry.Done || dry.Changed != 0 || active(user) != after {
				t.Errorf("read-only cap preview claimed completion or changed data: progress=%+v err=%v rollback=%v", dry, dryErr, rollbackErr)
			}
			checkedDryRun = true
		}
		options.Checkpoint = append([]byte(nil), progress.Checkpoint...)
	}
	if !done || !checkedDryRun {
		t.Fatalf("bounded backfill failed to complete or preview: done=%t preview=%t", done, checkedDryRun)
	}
	if err := verifyAuthSessions(ctx, db); err != nil {
		t.Fatal(err)
	}
	var named, global int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FILTER(WHERE event_type='auth_session.revoked'),count(*) FILTER(WHERE event_type='user.sessions_revoked') FROM commodore.domain_event_outbox WHERE tenant_id=$1`, tenant).Scan(&named, &global); err != nil || named != 150 || global != 0 {
		t.Fatalf("migration revocation scope: named=%d global=%d err=%v", named, global, err)
	}
	var oldFamily, otherFamily int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM commodore.refresh_tokens WHERE tenant_id=$1 AND user_id=$2 AND id IN ($3::uuid,$4::uuid)`, tenant, user, parent, head).Scan(&oldFamily); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM commodore.refresh_tokens WHERE tenant_id=$1 AND user_id=$2 AND token_hash='cap-job-other'`, tenant, other).Scan(&otherFamily); err != nil || oldFamily != 0 || otherFamily != 1 {
		t.Fatalf("mixed-version family cleanup: old=%d other=%d err=%v", oldFamily, otherFamily, err)
	}
	again, err := runAuthSessions(ctx, db, options)
	if err != nil || !again.Done || again.Changed != 0 {
		t.Fatalf("completed cap migration was not idempotent: %+v err=%v", again, err)
	}
}

func startAuthCapsMigrationPG(t *testing.T) *sql.DB {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not available")
	}
	name := fmt.Sprintf("fw-auth-cap-migration-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = dockerpg.CLI("rm", "-fv", name) })
	image, err := dockerpg.PostgresImage()
	if err != nil {
		t.Fatal(err)
	}
	if out, runErr := dockerpg.Run("run", "-d", "--name", name, "-P", "-e", "POSTGRES_PASSWORD=harness", image); runErr != nil {
		t.Fatalf("start PostgreSQL: %v: %s", runErr, out)
	}
	port, err := dockerpg.DiscoverPublishedHostPort(name, "5432/tcp")
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("postgres", fmt.Sprintf("postgres://postgres:harness@127.0.0.1:%s/postgres?sslmode=disable", port))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err = dockerpg.WaitReady(db, name); err != nil {
		t.Fatal(err)
	}
	schema, err := dbsql.Content.ReadFile("schema/commodore.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(t.Context(), string(schema)); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestSessionCapSkipsExpiredHistoryDurably_RealPG(t *testing.T) {
	testSessionCapExpiredHistory(t, startAuthCapsMigrationPG(t))
}

func TestSessionCapSkipsExpiredHistoryDurably_RealYugabyte(t *testing.T) {
	db, ok := dockerpg.OpenSharedYugabyteBaseline(t, "commodore_cap_history", "commodore")
	if !ok {
		t.Skip("requires the shared Yugabyte contract fixture")
	}
	testSessionCapExpiredHistory(t, db)
}

func testSessionCapExpiredHistory(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 90*time.Second)
	defer cancel()
	const tenant = "11000000-0000-4000-8000-00000000cb01"
	const user = "21000000-0000-4000-8000-00000000cb01"
	if _, err := db.ExecContext(ctx, `INSERT INTO commodore.users(id,tenant_id,email,is_active,verified) VALUES($1,$2,'cap-history@example.test',true,true)`, user, tenant); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range []struct {
		count   int
		expired bool
	}{{45, true}, {110, false}} {
		if _, err := db.ExecContext(ctx, `INSERT INTO commodore.auth_sessions(tenant_id,user_id,auth_method,client_id,created_at,last_seen_at,idle_expires_at,absolute_expires_at)
SELECT $1,$2,'unknown','unknown',NOW()-INTERVAL '3 days',
CASE WHEN $4::boolean THEN NOW()-INTERVAL '2 days' ELSE NOW()-INTERVAL '1 hour' END,
CASE WHEN $4::boolean THEN NOW()-INTERVAL '1 day' ELSE NOW()+INTERVAL '1 day' END,
NOW()+INTERVAL '2 days' FROM generate_series(1,$3)`, tenant, user, fixture.count, fixture.expired); err != nil {
			t.Fatal(err)
		}
	}
	finished := map[string]bool{}
	finish := func(_ context.Context, _ *sql.Tx, row commodoredb.CommodoreAuthSession) error {
		if !row.IdleExpiresAt.After(time.Now()) {
			return fmt.Errorf("expired session emitted an active revocation")
		}
		finished[row.ID] = true
		return nil
	}
	for step, wantExpired := range []int{20, 20, 5} {
		result, err := sessionstore.TrimCapBatch(ctx, db, tenant, user, 100, "", finish)
		if err != nil {
			t.Fatal(err)
		}
		wantRemoved := 0
		if step == 2 {
			wantRemoved = 10
		}
		if result.Expired != wantExpired || result.Removed != wantRemoved || result.Done != (step == 2) {
			t.Fatalf("step %d did not advance past expired candidates within budget: result=%+v", step, result)
		}
		if step == 0 {
			stopped, stop := context.WithCancel(ctx)
			stop()
			if _, stopErr := sessionstore.TrimCapBatch(stopped, db, tenant, user, 100, "", finish); stopErr == nil {
				t.Fatal("cancelled cleanup succeeded")
			}
		}
	}
	var terminal, live int
	var wrongDeadline int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FILTER(WHERE revoked_at IS NOT NULL),
count(*) FILTER(WHERE revoked_at IS NULL AND idle_expires_at>NOW()),
count(*) FILTER(WHERE idle_expires_at<=NOW() AND revoked_at IS DISTINCT FROM LEAST(idle_expires_at,absolute_expires_at))
FROM commodore.auth_sessions WHERE tenant_id=$1 AND user_id=$2`, tenant, user).Scan(&terminal, &live, &wrongDeadline); err != nil {
		t.Fatal(err)
	}
	if terminal != 55 || live != 100 || wrongDeadline != 0 || len(finished) != 10 {
		t.Fatalf("expired history or live cap is incorrect: terminal=%d live=%d wrongDeadline=%d activeRevocations=%d", terminal, live, wrongDeadline, len(finished))
	}
}
