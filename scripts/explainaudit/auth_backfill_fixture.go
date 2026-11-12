package main

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"strings"
)

type authBackfillFixture struct {
	name, tenant, user, otherTenant, otherUser string
	heads                                      []string
	root                                       string
}

func authBackfillFixtureFor(id string) *authBackfillFixture {
	if !strings.HasPrefix(id, "commodore:hw:runAuthSessions") && id != "commodore:hw:verifyAuthSessions" {
		return nil
	}
	rng := rand.New(rand.NewSource(int64(hashString("auth backfill:" + id))))
	return &authBackfillFixture{name: strings.TrimPrefix(id, "commodore:hw:"), tenant: uuidFrom(rng), user: uuidFrom(rng),
		heads: []string{uuidFrom(rng), uuidFrom(rng), uuidFrom(rng)}, root: uuidFrom(rng), otherTenant: uuidFrom(rng), otherUser: uuidFrom(rng)}
}

// The baseline seed supplies the retained assigned-token population. Add live
// unassigned heads so an empty partial index cannot make a broken probe pass.
func (f *authBackfillFixture) setup(ctx context.Context, conn *sql.Conn) error {
	for _, id := range f.heads {
		if _, err := conn.ExecContext(ctx, `INSERT INTO commodore.refresh_tokens
            (id,tenant_id,user_id,token_hash,created_at,expires_at,revoked)
            VALUES ($1,$2,$3,$4,NOW()-INTERVAL '1 day',NOW()+INTERVAL '10 days',false)`, id, f.tenant, f.user, id); err != nil {
			return err
		}
	}
	_, err := conn.ExecContext(ctx, `INSERT INTO commodore.refresh_tokens
        (id,tenant_id,user_id,token_hash,created_at,expires_at,revoked,replaced_by)
        VALUES ($1,$2,$3,$4,NOW()-INTERVAL '2 days',NOW()+INTERVAL '9 days',true,$5)`, f.root, f.tenant, f.user, f.root, f.heads[0])
	if err != nil {
		return err
	}
	// Pending heads span owners; expired unassigned history must be skipped by
	// the expiry range before LIMIT, including within the selected owner.
	if _, err = conn.ExecContext(ctx, `INSERT INTO commodore.refresh_tokens
 (id,tenant_id,user_id,token_hash,created_at,expires_at,revoked)
 SELECT gen_random_uuid(), $1::uuid, $2::uuid, gen_random_uuid()::text,
 NOW()-INTERVAL '2 days', NOW()+INTERVAL '20 days'+n*INTERVAL '1 second',false
 FROM generate_series(1,1000) AS n`, f.otherTenant, f.otherUser); err != nil {
		return err
	}
	if _, err = conn.ExecContext(ctx, `INSERT INTO commodore.refresh_tokens
 (id,tenant_id,user_id,token_hash,created_at,expires_at,revoked)
 SELECT gen_random_uuid(), $1::uuid, $2::uuid, gen_random_uuid()::text,
 NOW()-INTERVAL '2 days', NOW()-INTERVAL '1 day',false
 FROM generate_series(1,1000)`, f.tenant, f.user); err != nil {
		return err
	}
	_, err = conn.ExecContext(ctx, `ANALYZE commodore.refresh_tokens`)
	return err
}

func (f *authBackfillFixture) verify(ctx context.Context, conn *sql.Conn) error {
	if f.name != "runAuthSessions#3" {
		return nil
	}
	var linked int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM commodore.refresh_tokens
        WHERE tenant_id=$1 AND user_id=$2 AND session_id IS NOT NULL`, f.tenant, f.user).Scan(&linked); err != nil {
		return err
	}
	if linked != 4 {
		return fmt.Errorf("backfill audit did not execute the live chain: linked=%d, want 4", linked)
	}
	return nil
}

func (f *authBackfillFixture) parameter(typ string) string {
	if typ == "uuid" {
		return f.tenant
	}
	return "100"
}

var _ executionFixture = (*authBackfillFixture)(nil)
