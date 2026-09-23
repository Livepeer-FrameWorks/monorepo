//go:build schema_verify

package commodoredb

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestAdminAPITokenListing_RealPG(t *testing.T) {
	db := startCommodoreQueryCatalogRealPG(t)
	ctx := context.Background()
	const (
		tenantA = "11111111-1111-1111-1111-111111111111"
		tenantB = "22222222-2222-2222-2222-222222222222"
		userID  = "33333333-3333-3333-3333-333333333333"
	)
	base := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	for i, token := range []struct {
		id, tenant, perms string
		active            bool
	}{
		{"a0000000-0000-0000-0000-000000000001", tenantA, "{streams:read}", true},
		{"a0000000-0000-0000-0000-000000000002", tenantA, "{read,streams:read}", true},
		{"b0000000-0000-0000-0000-000000000003", tenantB, "{write}", false},
		{"b0000000-0000-0000-0000-000000000004", tenantB, "{}", true},
	} {
		if _, err := db.ExecContext(ctx, `
			INSERT INTO commodore.api_tokens (id, tenant_id, user_id, token_value, token_name, permissions, is_active, created_at)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6::text[], $7, $8)
		`, token.id, token.tenant, userID, "hash-"+token.id, "token-"+token.id[:1], token.perms, token.active, base.Add(time.Duration(i)*time.Hour)); err != nil {
			t.Fatalf("seed token %s: %v", token.id, err)
		}
	}
	q := New(db)
	accepted := []string{"streams:read", "streams:write"}
	ids := func(rows []AdminAPITokenRow) string {
		out := make([]string, 0, len(rows))
		for _, row := range rows {
			out = append(out, row.ID[:1]+row.ID[len(row.ID)-1:])
		}
		return strings.Join(out, ",")
	}

	all := AdminAPITokenFilter{Accepted: accepted}
	if total, err := q.CountAdminAPITokens(ctx, all); err != nil || total != 4 {
		t.Fatalf("count all = %d, %v", total, err)
	}
	page1, err := q.ListAdminAPITokens(ctx, all, AdminAPITokenPage{RowLimit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(page1); got != "b4,b3" {
		t.Fatalf("first page = %s, want newest first across tenants", got)
	}
	cursor := page1[1].CreatedAt.Time
	page2, err := q.ListAdminAPITokens(ctx, all, AdminAPITokenPage{RowLimit: 5, CursorTime: &cursor, CursorID: page1[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(page2); got != "a2,a1" {
		t.Fatalf("second page = %s", got)
	}
	cursor = page2[0].CreatedAt.Time
	back, err := q.ListAdminAPITokens(ctx, all, AdminAPITokenPage{Backward: true, RowLimit: 5, CursorTime: &cursor, CursorID: page2[0].ID})
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(back); got != "b3,b4" {
		t.Fatalf("backward page = %s", got)
	}

	unsupported := AdminAPITokenFilter{UnsupportedOnly: true, Accepted: accepted}
	rows, err := q.ListAdminAPITokens(ctx, unsupported, AdminAPITokenPage{RowLimit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(rows); got != "b3,a2" {
		t.Fatalf("unsupported = %s, want the legacy-scope tokens only", got)
	}
	if rows[0].Status != "inactive" || rows[1].Status != "active" {
		t.Fatalf("statuses = %s/%s", rows[0].Status, rows[1].Status)
	}

	tenantOnly := AdminAPITokenFilter{TenantID: tenantA, UnsupportedOnly: true, Accepted: accepted}
	if total, err := q.CountAdminAPITokens(ctx, tenantOnly); err != nil || total != 1 {
		t.Fatalf("tenant unsupported count = %d, %v", total, err)
	}
	rows, err = q.ListAdminAPITokens(ctx, tenantOnly, AdminAPITokenPage{RowLimit: 10})
	if err != nil || ids(rows) != "a2" || rows[0].TenantID != tenantA {
		t.Fatalf("tenant unsupported rows = %s, %v", ids(rows), err)
	}
}
