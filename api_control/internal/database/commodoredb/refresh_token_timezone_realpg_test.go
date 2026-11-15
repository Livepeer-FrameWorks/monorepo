//go:build schema_verify

package commodoredb

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func testLegacyRefreshUTC(t *testing.T, db *sql.DB) {
	t.Helper()
	ctx := context.Background()
	var originalZone string
	if err := db.QueryRowContext(ctx, "SHOW TIME ZONE").Scan(&originalZone); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if _, err := db.ExecContext(ctx, "SELECT set_config('TimeZone', $1, false)", originalZone); err != nil {
			t.Error(err)
		}
	}()
	q := New(db)
	for _, zone := range []string{"UTC", "Europe/Amsterdam", "America/New_York"} {
		for _, issuer := range []string{"login", "rotation", "session"} {
			t.Run(zone+"/"+issuer, func(t *testing.T) {
				if _, err := db.ExecContext(ctx, "SELECT set_config('TimeZone', $1, false)", zone); err != nil {
					t.Fatal(err)
				}
				tenant, user, hash := uuid.NewString(), uuid.NewString(), uuid.NewString()
				started := time.Now().UTC()
				expires := started.Add(2 * time.Minute)
				if zone == "UTC" {
					// Issuer clocks may carry a location independently of the database session.
					expires = expires.In(time.FixedZone("issuer", 7*60*60))
				}
				var err error
				switch issuer {
				case "login":
					err = q.InsertRefreshToken(ctx, InsertRefreshTokenParams{TenantID: tenant, UserID: user, TokenHash: hash, ExpiresAt: expires})
				case "rotation":
					_, err = q.InsertRotatedRefreshToken(ctx, InsertRotatedRefreshTokenParams{TenantID: tenant, UserID: user, TokenHash: hash, ExpiresAt: expires})
				case "session":
					_, err = q.InsertSessionRefreshToken(ctx, InsertSessionRefreshTokenParams{TenantID: tenant, UserID: user, TokenHash: hash, ExpiresAt: expires})
				}
				if err != nil {
					t.Fatal(err)
				}
				var id string
				var createdAt, expiresAt time.Time
				if err := db.QueryRowContext(ctx, `SELECT id, created_at AT TIME ZONE 'UTC', expires_at AT TIME ZONE 'UTC'
 FROM commodore.refresh_tokens WHERE tenant_id=$1 AND token_hash=$2`, tenant, hash).Scan(&id, &createdAt, &expiresAt); err != nil {
					t.Fatal(err)
				}
				if createdAt.Before(started.Add(-time.Second)) || createdAt.After(time.Now().UTC().Add(time.Second)) {
					t.Errorf("created_at is not UTC: %s (started %s)", createdAt, started)
				}
				if expiresAt.Sub(expires).Abs() > time.Microsecond {
					t.Errorf("expiry changed instant: got %s want %s", expiresAt, expires)
				}
				if row, err := q.LockRefreshTokenByHash(ctx, hash); err != nil || row.TenantID != tenant {
					t.Errorf("unexpired UTC credential was rejected: %v", err)
				}
				if owner, err := q.GetRefreshTokenOwnerByHash(ctx, hash); err != nil || owner.TenantID != tenant {
					t.Errorf("unexpired UTC credential owner was lost: %v", err)
				}
				if _, err := q.AdoptAuthSessionRefreshFamily(ctx, AdoptAuthSessionRefreshFamilyParams{TenantID: tenant, UserID: user, ID: id}); err != nil {
					t.Errorf("unexpired UTC family could not be adopted: %v", err)
				}
				if _, err := db.ExecContext(ctx, `UPDATE commodore.refresh_tokens SET expires_at=$3 WHERE tenant_id=$1 AND id=$2`, tenant, id, started.Add(-time.Minute)); err != nil {
					t.Fatal(err)
				}
				if _, err := q.LockRefreshTokenByHash(ctx, hash); !errors.Is(err, sql.ErrNoRows) {
					t.Errorf("expired UTC credential still resolves: %v", err)
				}
				if _, err := q.GetRefreshTokenOwnerByHash(ctx, hash); !errors.Is(err, sql.ErrNoRows) {
					t.Errorf("expired UTC credential owner still resolves: %v", err)
				}
				if err := q.RotateRefreshToken(ctx, RotateRefreshTokenParams{ID: id}); err != nil {
					t.Fatal(err)
				}
				var rotatedAt time.Time
				if err := db.QueryRowContext(ctx, `SELECT rotated_at AT TIME ZONE 'UTC' FROM commodore.refresh_tokens WHERE tenant_id=$1 AND id=$2`, tenant, id).Scan(&rotatedAt); err != nil {
					t.Fatal(err)
				}
				if rotatedAt.Before(started.Add(-time.Second)) || rotatedAt.After(time.Now().UTC().Add(time.Second)) {
					t.Errorf("rotated_at is not UTC: %s", rotatedAt)
				}
			})
		}
	}
}
