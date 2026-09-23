package commodoredb

import (
	"context"
	"database/sql"
	"time"

	"github.com/lib/pq"
)

// The operator token listing reads metadata only: token_value holds the
// credential hash and is never selected here.
const adminAPITokenColumns = `
SELECT id::text, tenant_id::text, token_name, COALESCE(permissions, '{}'::text[]),
       CASE WHEN is_active AND (expires_at IS NULL OR expires_at > NOW()) THEN 'active' ELSE 'inactive' END::text AS status,
       last_used_at, expires_at, created_at
FROM commodore.api_tokens`

// $1 tenant filter (empty = every tenant), $2 unsupported-only, $3 accepted permissions.
const adminAPITokenFilter = `
WHERE ($1::text = '' OR tenant_id = NULLIF($1::text, '')::uuid)
  AND (NOT $2::boolean OR NOT (COALESCE(permissions, '{}'::text[]) <@ $3::text[]))`

const countAdminAPITokensSQL = `SELECT COUNT(*) FROM commodore.api_tokens` + adminAPITokenFilter

const listAdminAPITokensForwardSQL = adminAPITokenColumns + adminAPITokenFilter + `
ORDER BY created_at DESC, id DESC
LIMIT $4`

const listAdminAPITokensForwardAfterSQL = adminAPITokenColumns + adminAPITokenFilter + `
  AND (created_at, id) < ($5::timestamp, $6::uuid)
ORDER BY created_at DESC, id DESC
LIMIT $4`

const listAdminAPITokensBackwardSQL = adminAPITokenColumns + adminAPITokenFilter + `
ORDER BY created_at ASC, id ASC
LIMIT $4`

const listAdminAPITokensBackwardBeforeSQL = adminAPITokenColumns + adminAPITokenFilter + `
  AND (created_at, id) > ($5::timestamp, $6::uuid)
ORDER BY created_at ASC, id ASC
LIMIT $4`

// AdminAPITokenFilter selects tokens across tenants. An empty TenantID means
// every tenant; UnsupportedOnly keeps tokens holding a permission outside
// Accepted.
type AdminAPITokenFilter struct {
	TenantID        string
	UnsupportedOnly bool
	Accepted        []string
}

// AdminAPITokenPage is one keyset page. Backward pages are returned oldest
// first; the caller reverses them.
type AdminAPITokenPage struct {
	Backward   bool
	CursorTime *time.Time
	CursorID   string
	RowLimit   int32
}

type AdminAPITokenRow struct {
	ID          string
	TenantID    string
	TokenName   string
	Permissions pq.StringArray
	Status      string
	LastUsedAt  sql.NullTime
	ExpiresAt   sql.NullTime
	CreatedAt   sql.NullTime
}

func (f AdminAPITokenFilter) args() []any {
	accepted := f.Accepted
	if accepted == nil {
		accepted = []string{}
	}
	return []any{f.TenantID, f.UnsupportedOnly, pq.Array(accepted)}
}

func (q *Queries) CountAdminAPITokens(ctx context.Context, filter AdminAPITokenFilter) (int64, error) {
	var total int64
	err := q.db.QueryRowContext(ctx, countAdminAPITokensSQL, filter.args()...).Scan(&total)
	return total, err
}

func (q *Queries) ListAdminAPITokens(ctx context.Context, filter AdminAPITokenFilter, page AdminAPITokenPage) ([]AdminAPITokenRow, error) {
	args := append(filter.args(), page.RowLimit)
	query := listAdminAPITokensForwardSQL
	switch {
	case page.Backward && page.CursorTime != nil:
		query = listAdminAPITokensBackwardBeforeSQL
	case page.Backward:
		query = listAdminAPITokensBackwardSQL
	case page.CursorTime != nil:
		query = listAdminAPITokensForwardAfterSQL
	}
	if page.CursorTime != nil {
		args = append(args, *page.CursorTime, page.CursorID)
	}
	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AdminAPITokenRow
	for rows.Next() {
		var row AdminAPITokenRow
		if err := rows.Scan(&row.ID, &row.TenantID, &row.TokenName, &row.Permissions, &row.Status,
			&row.LastUsedAt, &row.ExpiresAt, &row.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}
