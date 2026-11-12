//go:build schema_verify

package commodoredb

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/google/uuid"
)

func TestAuthSessionsSchema_RealPG(t *testing.T) {
	testAuthSessionsSchema(t, startCommodoreQueryCatalogRealPG(t))
}

func TestAuthSessionsSchema_RealYugabyte(t *testing.T) {
	testAuthSessionsSchema(t, startCommodoreQueryCatalogRealYugabyte(t))
}

func testAuthSessionsSchema(t *testing.T, db *sql.DB) {
	t.Helper()
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `SET TIME ZONE 'Europe/Amsterdam'`); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM commodore.auth_sessions WHERE tenant_id = $1`, "10000000-0000-4000-8000-000000000081").Scan(&count); err != nil {
		t.Fatalf("first-class session storage unavailable: %v", err)
	}
	q := New(db)
	tenantID, otherTenantID, userID := uuid.NewString(), uuid.NewString(), uuid.NewString()
	create := func(tenant string) CommodoreAuthSession {
		session, err := q.InsertAuthSession(ctx, InsertAuthSessionParams{
			TenantID: tenant, UserID: userID, AuthMethod: "password", ClientID: "web",
			DeviceLabel: "Browser", CreatedIp: "203.0.113.1",
			IdleExpiresAt: time.Now().Add(time.Hour), AbsoluteExpiresAt: time.Now().Add(2 * time.Hour),
		})
		if err != nil {
			t.Fatal(err)
		}
		return session
	}
	current, other := create(tenantID), create(otherTenantID)
	if _, err := q.GetAuthSession(ctx, GetAuthSessionParams{TenantID: otherTenantID, UserID: userID, ID: current.ID}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("session crossed tenant boundary: %v", err)
	}
	if _, err := q.GetAuthSession(ctx, GetAuthSessionParams{TenantID: tenantID, UserID: uuid.NewString(), ID: current.ID}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("session crossed user boundary: %v", err)
	}
	listed, err := q.ListAuthSessions(ctx, ListAuthSessionsParams{TenantID: tenantID, UserID: userID, PageLimit: 10})
	if err != nil || len(listed) != 1 || listed[0].ID != current.ID {
		t.Fatalf("list=%v err=%v", listed, err)
	}
	if n, err := q.RenameAuthSession(ctx, RenameAuthSessionParams{TenantID: otherTenantID, UserID: userID, ID: current.ID, DeviceLabel: "unauthorized"}); err != nil || n != 0 {
		t.Fatalf("cross-tenant rename affected %d rows: %v", n, err)
	}
	touched, err := q.TouchAuthSession(ctx, TouchAuthSessionParams{TenantID: tenantID, UserID: userID, ID: current.ID, LastIp: "203.0.113.2"})
	if err != nil || !touched.IdleExpiresAt.Equal(current.AbsoluteExpiresAt) || touched.LastIp != "203.0.113.2" {
		t.Fatalf("idle timeout escaped absolute cap: %+v err=%v", touched, err)
	}
	_, err = q.InsertSessionRefreshToken(ctx, InsertSessionRefreshTokenParams{
		TenantID: otherTenantID, UserID: userID, SessionID: sql.NullString{String: current.ID, Valid: true},
		TokenHash: uuid.NewString(), ExpiresAt: time.Now().Add(time.Hour),
	})
	if err == nil {
		t.Fatal("refresh token accepted a session belonging to another tenant")
	}
	revoked, err := q.RevokeAuthSession(ctx, RevokeAuthSessionParams{
		TenantID: tenantID, UserID: userID, ID: current.ID, RevokeReason: sql.NullString{String: "user", Valid: true},
	})
	if err != nil || !revoked.RevokedAt.Valid {
		t.Fatalf("revoke=%+v err=%v", revoked, err)
	}
	if _, err := q.TouchAuthSession(ctx, TouchAuthSessionParams{TenantID: tenantID, UserID: userID, ID: current.ID}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("revoked session was renewed: %v", err)
	}
	untouched, err := q.GetAuthSession(ctx, GetAuthSessionParams{TenantID: otherTenantID, UserID: userID, ID: other.ID})
	if err != nil || untouched.RevokedAt.Valid {
		t.Fatalf("revocation affected another tenant: %+v %v", untouched, err)
	}
	cliSession, err := q.InsertAuthSession(ctx, InsertAuthSessionParams{
		TenantID: tenantID, UserID: userID, AuthMethod: "device_code", ClientID: "cli",
		IdleExpiresAt: time.Now().Add(time.Hour), AbsoluteExpiresAt: time.Now().Add(180 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	renewed, err := q.TouchAuthSession(ctx, TouchAuthSessionParams{TenantID: tenantID, UserID: userID, ID: cliSession.ID})
	if err != nil || time.Until(renewed.IdleExpiresAt) > 30*24*time.Hour || time.Until(renewed.IdleExpiresAt) < 30*24*time.Hour-5*time.Second {
		t.Fatalf("idle timeout is not a fixed 30-day duration: %+v err=%v", renewed, err)
	}
	testAuthSessionBackfill(t, db)
	testLegacyRefreshLookupAfterSessionRevocation(t, db)
	testRevocationWaitsForOldIssuerRotation(t, db)
	testAuthSessionHardMaximum(t, db)
}

func testAuthSessionHardMaximum(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, test := range []struct {
		client string
		days   int
	}{{"web", 90}, {"cli", 180}, {"tray-mac", 180}} {
		row, err := New(db).InsertAuthSession(context.Background(), InsertAuthSessionParams{
			TenantID: uuid.NewString(), UserID: uuid.NewString(), AuthMethod: "password", ClientID: test.client,
			IdleExpiresAt: time.Now().Add(31 * 24 * time.Hour), AbsoluteExpiresAt: time.Now().Add(time.Duration(test.days+1) * 24 * time.Hour),
		})
		if err != nil {
			t.Fatal(err)
		}
		if row.AbsoluteExpiresAt.Sub(row.CreatedAt) > time.Duration(test.days)*24*time.Hour || row.IdleExpiresAt.Sub(row.CreatedAt) > 30*24*time.Hour {
			t.Fatalf("%s exceeded hard duration despite an issuer clock ahead of database time: max=%s idle=%s", test.client, row.AbsoluteExpiresAt.Sub(row.CreatedAt), row.IdleExpiresAt.Sub(row.CreatedAt))
		}
	}
}

func testRevocationWaitsForOldIssuerRotation(t *testing.T, db *sql.DB) {
	t.Helper()
	db.SetMaxOpenConns(2)
	defer db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	q := New(db)
	tenantID, userID, rootHash, childHash := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	session, err := q.InsertAuthSession(ctx, InsertAuthSessionParams{
		TenantID: tenantID, UserID: userID, AuthMethod: "password", ClientID: "web",
		IdleExpiresAt: time.Now().Add(time.Hour), AbsoluteExpiresAt: time.Now().Add(2 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	rootID, err := q.InsertSessionRefreshToken(ctx, InsertSessionRefreshTokenParams{
		TenantID: tenantID, UserID: userID, SessionID: sql.NullString{String: session.ID, Valid: true}, TokenHash: rootHash, ExpiresAt: time.Now().Add(time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	oldTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer oldTx.Rollback()
	old := New(oldTx)
	if _, err := old.LockRefreshTokenByHash(ctx, rootHash); err != nil {
		t.Fatal(err)
	}
	childID, err := old.InsertRotatedRefreshToken(ctx, InsertRotatedRefreshTokenParams{TenantID: tenantID, UserID: userID, TokenHash: childHash, ExpiresAt: time.Now().Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	if err := old.RotateRefreshToken(ctx, RotateRefreshTokenParams{ID: rootID, ReplacedBy: sql.NullString{String: childID, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	started, finished := make(chan struct{}), make(chan error, 1)
	var once sync.Once
	go func() {
		finished <- database.WithRetryablePostgresTx(ctx, db, nil, func(tx *sql.Tx) error {
			revoke := New(tx)
			if _, revokeErr := revoke.RevokeAuthSession(ctx, RevokeAuthSessionParams{TenantID: tenantID, UserID: userID, ID: session.ID, RevokeReason: sql.NullString{String: "user", Valid: true}}); revokeErr != nil {
				return revokeErr
			}
			once.Do(func() { close(started) })
			if _, lockErr := revoke.LockAuthSessionRefreshTokens(ctx, LockAuthSessionRefreshTokensParams{TenantID: tenantID, UserID: userID, SessionID: sql.NullString{String: session.ID, Valid: true}}); lockErr != nil {
				return lockErr
			}
			return revoke.RevokeAuthSessionRefreshTokens(ctx, RevokeAuthSessionRefreshTokensParams{TenantID: tenantID, UserID: userID, SessionID: sql.NullString{String: session.ID, Valid: true}})
		})
	}()
	select {
	case <-started:
	case err := <-finished:
		t.Fatalf("revocation failed before waiting for old issuer: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	select {
	case err := <-finished:
		t.Fatalf("revocation did not wait for the in-flight old rotation: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := oldTx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	for _, hash := range []string{rootHash, childHash} {
		if _, err := q.LockRefreshTokenByHash(ctx, hash); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("old issuer's committed successor survived session revocation: %v", err)
		}
	}
}

func testLegacyRefreshLookupAfterSessionRevocation(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	q := New(db)
	tenantID, userID := uuid.NewString(), uuid.NewString()
	create := func() CommodoreAuthSession {
		row, err := q.InsertAuthSession(ctx, InsertAuthSessionParams{
			TenantID: tenantID, UserID: userID, AuthMethod: "password", ClientID: "web",
			IdleExpiresAt: time.Now().Add(time.Hour), AbsoluteExpiresAt: time.Now().Add(2 * time.Hour),
		})
		if err != nil {
			t.Fatal(err)
		}
		return row
	}
	ended, other := create(), create()
	root, child, preserved := uuid.NewString(), uuid.NewString(), uuid.NewString()
	_, err := db.ExecContext(ctx, `INSERT INTO commodore.refresh_tokens
        (id, tenant_id, user_id, session_id, token_hash, expires_at, revoked, replaced_by)
        VALUES ($1::uuid, $4, $5, $6, $1::text, NOW() + INTERVAL '1 hour', true, $2),
               ($2::uuid, $4, $5, NULL, $2::text, NOW() + INTERVAL '1 hour', false, NULL),
               ($3::uuid, $4, $5, $7, $3::text, NOW() + INTERVAL '1 hour', false, NULL)`, root, child, preserved, tenantID, userID, ended.ID, other.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.RevokeAuthSession(ctx, RevokeAuthSessionParams{TenantID: tenantID, UserID: userID, ID: ended.ID, RevokeReason: sql.NullString{String: "user", Valid: true}}); err != nil {
		t.Fatal(err)
	}
	if err := q.RevokeAuthSessionRefreshTokens(ctx, RevokeAuthSessionRefreshTokensParams{TenantID: tenantID, UserID: userID, SessionID: sql.NullString{String: ended.ID, Valid: true}}); err != nil {
		t.Fatal(err)
	}
	for _, hash := range []string{root, child} {
		if _, err := q.LockRefreshTokenByHash(ctx, hash); !errors.Is(err, sql.ErrNoRows) {
			t.Fatalf("legacy issuer can still find a revoked session's credential and trigger global theft handling: %v", err)
		}
	}
	if row, err := q.LockRefreshTokenByHash(ctx, preserved); err != nil || row.Revoked {
		t.Fatalf("ending one session changed the other device: %+v err=%v", row, err)
	}
}

func testAuthSessionBackfill(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	tenantID, userID, rootID, headID := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
	_, err := db.ExecContext(ctx, `INSERT INTO commodore.refresh_tokens
        (id, tenant_id, user_id, token_hash, created_at, expires_at, revoked, replaced_by)
        VALUES ($1::uuid, $3, $4, $1::text, TIMESTAMP '2020-08-01 00:00:00', NOW() + INTERVAL '20 days', true, $2),
               ($2::uuid, $3, $4, $2::text, NOW() - INTERVAL '1 day', NOW() + INTERVAL '29 days', false, NULL)`, rootID, headID, tenantID, userID)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		var scanned, changed int64
		if err := db.QueryRowContext(ctx, migrationSQLConstant(t, "auth_sessions.go", "AuthSessionsBatchSQL"), tenantID, 100).Scan(&scanned, &changed); err != nil {
			t.Fatalf("backfill: %v", err)
		}
	}
	var linked int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM commodore.refresh_tokens
        WHERE tenant_id = $1 AND user_id = $2 AND session_id = $3`, tenantID, userID, headID).Scan(&linked); err != nil || linked != 2 {
		t.Fatalf("chain backfill linked %d tokens, want 2: %v", linked, err)
	}
	session, err := New(db).GetAuthSession(ctx, GetAuthSessionParams{TenantID: tenantID, UserID: userID, ID: headID})
	if err != nil || session.ClientID != "unknown" || time.Since(session.CreatedAt) < 71*time.Hour || session.AbsoluteExpiresAt.Sub(session.CreatedAt) != 90*24*time.Hour {
		t.Fatalf("backfill lost original age or cap: %+v err=%v", session, err)
	}
	newHead := uuid.NewString()
	if _, err := db.ExecContext(ctx, `UPDATE commodore.refresh_tokens SET revoked = true, replaced_by = $4
        WHERE tenant_id = $1 AND user_id = $2 AND id = $3`, tenantID, userID, headID, newHead); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO commodore.refresh_tokens (id, tenant_id, user_id, token_hash, expires_at)
        VALUES ($1::uuid, $2, $3, $1::text, NOW() + INTERVAL '30 days')`, newHead, tenantID, userID); err != nil {
		t.Fatal(err)
	}
	var scanned, changed int64
	if err := db.QueryRowContext(ctx, migrationSQLConstant(t, "auth_sessions.go", "AuthSessionsBatchSQL"), tenantID, 100).Scan(&scanned, &changed); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM commodore.refresh_tokens
        WHERE tenant_id = $1 AND user_id = $2 AND session_id = $3`, tenantID, userID, headID).Scan(&linked); err != nil || linked != 3 {
		t.Fatalf("old issuer's next head forked its session: linked=%d err=%v", linked, err)
	}
}
