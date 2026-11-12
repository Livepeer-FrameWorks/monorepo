package main

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand"
	"strings"
)

type authCapFixture struct{ name, owner string }

func authCapFixtureFor(id string) *authCapFixture {
	switch id {
	case "commodore:ListOldestAuthSessions", "commodore:ListOldestOtherAuthSessions", "commodore:CountAuthSessionsForIssuance", "commodore:ListAuthSessionCapOwners":
	default:
		return nil
	}
	rng := rand.New(rand.NewSource(int64(hashString("auth cap:" + id))))
	return &authCapFixture{name: strings.TrimPrefix(id, "commodore:"), owner: uuidFrom(rng)}
}

// Tenant and user share a UUID so every typed owner parameter binds the same
// populated identity, including unnamed positional arguments in generated SQL.
func (f *authCapFixture) parameter(typ string) string {
	if typ == "uuid" {
		if f.name == "ListAuthSessionCapOwners" {
			return "00000000-0000-0000-0000-000000000000"
		}
		return f.owner
	}
	if f.name == "CountAuthSessionsForIssuance" {
		return "120"
	}
	if f.name == "ListAuthSessionCapOwners" {
		return "100"
	}
	return "20"
}

func (f *authCapFixture) setup(ctx context.Context, conn *sql.Conn) error {
	if _, err := conn.ExecContext(ctx, `INSERT INTO commodore.users(id,tenant_id,email,is_active,verified) VALUES($1,$1,$1::text||'@cap-audit.test',true,true)`, f.owner); err != nil {
		return err
	}
	for _, fixture := range []struct {
		count   int
		expired bool
	}{{1000, true}, {250, false}} {
		if _, err := conn.ExecContext(ctx, `INSERT INTO commodore.auth_sessions(tenant_id,user_id,auth_method,client_id,created_at,last_seen_at,idle_expires_at,absolute_expires_at)
SELECT $1,$1,'unknown','unknown',NOW()-INTERVAL '3 days',
CASE WHEN $3::boolean THEN NOW()-INTERVAL '2 days' ELSE NOW()-INTERVAL '1 hour' END,
CASE WHEN $3::boolean THEN NOW()-INTERVAL '1 day' ELSE NOW()+INTERVAL '1 day' END,
NOW()+INTERVAL '2 days' FROM generate_series(1,$2)`, f.owner, fixture.count, fixture.expired); err != nil {
			return err
		}
	}
	// Excluding an actual oldest candidate exercises the protected-session filter.
	if f.name == "ListOldestOtherAuthSessions" {
		_, err := conn.ExecContext(ctx, `INSERT INTO commodore.auth_sessions(id,tenant_id,user_id,auth_method,client_id,created_at,last_seen_at,idle_expires_at,absolute_expires_at)
VALUES($1,$2,$2,'unknown','unknown',NOW()-INTERVAL '4 days',NOW()-INTERVAL '3 days',NOW()-INTERVAL '1 day',NOW()+INTERVAL '2 days')`, f.owner, f.owner)
		return err
	}
	return nil
}

func (f *authCapFixture) verify(ctx context.Context, conn *sql.Conn) error {
	var live int
	if err := conn.QueryRowContext(ctx, `SELECT count(*) FROM commodore.auth_sessions WHERE tenant_id=$1 AND user_id=$1 AND revoked_at IS NULL AND idle_expires_at>NOW()`, f.owner).Scan(&live); err != nil {
		return err
	}
	if live != 250 {
		return fmt.Errorf("cap audit must contain 250 live candidates, got %d", live)
	}
	return nil
}

var _ executionFixture = (*authCapFixture)(nil)
