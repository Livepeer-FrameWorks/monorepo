//go:build schema_verify

package grpc

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"frameworks/api_control/internal/database/commodoredb"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/database"
	"github.com/Livepeer-FrameWorks/monorepo/pkg/testutil/dockerpg"
	"github.com/google/uuid"
)

func TestAuthSessionRevocationClock_RealPG(t *testing.T) {
	testAuthSessionRevocationClock(t, startCommodoreRealPG(t))
}

func TestAuthSessionRevocationClock_RealYugabyte(t *testing.T) {
	db, ok := dockerpg.OpenSharedYugabyteBaseline(t, "commodore_revocation_clock", "commodore")
	if !ok {
		t.Skip("requires the shared Yugabyte contract fixture")
	}
	testAuthSessionRevocationClock(t, db)
}

func testAuthSessionRevocationClock(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, mode := range []string{"single", "others", "all"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			tenant, user, current, target := uuid.NewString(), uuid.NewString(), uuid.NewString(), uuid.NewString()
			if _, err := db.ExecContext(ctx, `INSERT INTO commodore.users(id,tenant_id,is_active) VALUES($1,$2,true)`, user, tenant); err != nil {
				t.Fatal(err)
			}
			for _, sid := range []string{current, target} {
				if _, err := db.ExecContext(ctx, `INSERT INTO commodore.auth_sessions(id,tenant_id,user_id,auth_method,client_id,idle_expires_at,absolute_expires_at)
VALUES($1,$2,$3,'wallet','web',NOW()+INTERVAL '1 hour',NOW()+INTERVAL '2 hours')`, sid, tenant, user); err != nil {
					t.Fatal(err)
				}
			}
			issuer, err := db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if rollbackErr := issuer.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
					t.Errorf("release issuer lock: %v", rollbackErr)
				}
			}()
			if _, err = commodoredb.New(issuer).LockAuthSessionUser(ctx, commodoredb.LockAuthSessionUserParams{TenantID: tenant, ID: user}); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				done <- database.WithRetryablePostgresTx(ctx, db, nil, func(tx *sql.Tx) error {
					queries := commodoredb.New(tx)
					if _, lockErr := queries.LockAuthSessionUser(ctx, commodoredb.LockAuthSessionUserParams{TenantID: tenant, ID: user}); lockErr != nil {
						return lockErr
					}
					reason := sql.NullString{String: "user", Valid: true}
					var revokeErr error
					switch mode {
					case "single":
						_, revokeErr = queries.RevokeAuthSession(ctx, commodoredb.RevokeAuthSessionParams{TenantID: tenant, UserID: user, ID: target, RevokeReason: reason})
					case "others":
						_, revokeErr = queries.RevokeOtherAuthSessions(ctx, commodoredb.RevokeOtherAuthSessionsParams{TenantID: tenant, UserID: user, ID: current, RevokeReason: reason})
					case "all":
						_, revokeErr = queries.RevokeAllAuthSessions(ctx, commodoredb.RevokeAllAuthSessionsParams{TenantID: tenant, UserID: user, RevokeReason: reason})
					}
					return revokeErr
				})
			}()
			for {
				var waiting bool
				if err = db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity
WHERE datname=current_database() AND pid<>pg_backend_pid() AND state='active' AND query LIKE '-- name: LockAuthSessionUser%')`).Scan(&waiting); err != nil {
					t.Fatal(err)
				}
				if waiting {
					break
				}
				select {
				case revokeErr := <-done:
					t.Fatalf("revoker did not wait for issuer: %v", revokeErr)
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				case <-time.After(10 * time.Millisecond):
				}
			}
			var issuedAt time.Time
			if err = issuer.QueryRowContext(ctx, `SELECT clock_timestamp()`).Scan(&issuedAt); err != nil {
				t.Fatal(err)
			}
			if err = issuer.Commit(); err != nil {
				t.Fatal(err)
			}
			if err = <-done; err != nil {
				t.Fatal(err)
			}
			// As the snapshot's lookback boundary advances past issuance, a later
			// revocation must remain discoverable through the JWT validity window.
			rows, err := commodoredb.New(db).ListAuthSessionRevocationsSince(ctx, sql.NullTime{Time: issuedAt, Valid: true})
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, row := range rows {
				if row.TenantID == tenant && row.UserID == user && row.ID == target {
					found = true
				}
			}
			if !found {
				t.Fatal("snapshot boundary after issuance lost a named revocation committed after the issuer released its lock")
			}
		})
	}
}
