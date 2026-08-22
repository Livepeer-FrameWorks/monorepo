package quartermasterdb

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

type CreateBootstrapTokenParams struct {
	ID, Name, TokenHash, TokenPrefix, Kind string
	TenantID, ClusterID, ExpectedIP        *string
	Metadata                               any
	UsageLimit                             *int32
	ExpiresAt                              time.Time
}

func (q *Queries) CreateBootstrapToken(ctx context.Context, arg CreateBootstrapTokenParams) error {
	_, err := q.db.ExecContext(ctx, `
		INSERT INTO quartermaster.bootstrap_tokens (id, name, token_hash, token_prefix, kind, tenant_id, cluster_id, expected_ip, metadata, usage_limit, usage_count, expires_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, COALESCE($9::jsonb, '{}'::jsonb), $10, 0, $11, NOW())
	`, arg.ID, arg.Name, arg.TokenHash, arg.TokenPrefix, arg.Kind, arg.TenantID, arg.ClusterID, arg.ExpectedIP, arg.Metadata, arg.UsageLimit, arg.ExpiresAt)
	return err
}

type BootstrapTokenFilter struct {
	Kind, TenantID *string
	CursorTime     *time.Time
	CursorID       string
	Backward       bool
	Limit          int
}

type BootstrapTokenRow struct {
	ID, Name, TokenPrefix, Kind     string
	TenantID, ClusterID, ExpectedIP sql.NullString
	UsageLimit                      sql.NullInt32
	UsageCount                      int32
	ExpiresAt                       time.Time
	UsedAt                          sql.NullTime
	CreatedBy                       sql.NullString
	CreatedAt                       time.Time
}

func (q *Queries) ListBootstrapTokens(ctx context.Context, filter BootstrapTokenFilter) ([]BootstrapTokenRow, error) {
	where := "WHERE 1=1"
	args := []any{}
	if filter.Kind != nil {
		args = append(args, *filter.Kind)
		where += fmt.Sprintf(" AND kind = $%d", len(args))
	}
	if filter.TenantID != nil {
		args = append(args, *filter.TenantID)
		where += fmt.Sprintf(" AND tenant_id = $%d", len(args))
	}
	if filter.CursorTime != nil {
		op := "<"
		if filter.Backward {
			op = ">"
		}
		where += fmt.Sprintf(" AND (created_at, id) %s ($%d, $%d)", op, len(args)+1, len(args)+2)
		args = append(args, *filter.CursorTime, filter.CursorID)
	}
	direction := "DESC"
	if filter.Backward {
		direction = "ASC"
	}
	query := fmt.Sprintf(`
		SELECT id, name, token_prefix, kind, tenant_id, cluster_id, expected_ip, usage_limit, usage_count, expires_at, used_at, created_by, created_at
		FROM quartermaster.bootstrap_tokens
		%s
		ORDER BY created_at %s, id %s
		LIMIT $%d
	`, where, direction, direction, len(args)+1)
	args = append(args, filter.Limit)
	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []BootstrapTokenRow{}
	for rows.Next() {
		var row BootstrapTokenRow
		if err := rows.Scan(&row.ID, &row.Name, &row.TokenPrefix, &row.Kind, &row.TenantID, &row.ClusterID,
			&row.ExpectedIP, &row.UsageLimit, &row.UsageCount, &row.ExpiresAt, &row.UsedAt, &row.CreatedBy, &row.CreatedAt); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}

func (q *Queries) RevokeBootstrapToken(ctx context.Context, tokenID string) (int64, error) {
	result, err := q.db.ExecContext(ctx, `DELETE FROM quartermaster.bootstrap_tokens WHERE id = $1`, tokenID)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

type ValidatedBootstrapTokenRow struct {
	Kind                            string
	TenantID, ClusterID, ExpectedIP sql.NullString
	ExpiresAt                       time.Time
	UsageLimit                      sql.NullInt32
	UsageCount                      int32
	UsedAt                          sql.NullTime
	Metadata                        []byte
}

func (q *Queries) GetBootstrapTokenForValidation(ctx context.Context, tokenHash string) (ValidatedBootstrapTokenRow, error) {
	row := q.db.QueryRowContext(ctx, `
		SELECT kind, tenant_id, cluster_id, expected_ip::text, expires_at, usage_limit, usage_count, used_at, COALESCE(metadata, '{}'::jsonb)
		FROM quartermaster.bootstrap_tokens
		WHERE token_hash = $1
	`, tokenHash)
	var out ValidatedBootstrapTokenRow
	err := row.Scan(&out.Kind, &out.TenantID, &out.ClusterID, &out.ExpectedIP, &out.ExpiresAt, &out.UsageLimit, &out.UsageCount, &out.UsedAt, &out.Metadata)
	return out, err
}

func (q *Queries) ConsumeBootstrapToken(ctx context.Context, tokenHash string) (int64, error) {
	result, err := q.db.ExecContext(ctx, `
		UPDATE quartermaster.bootstrap_tokens
		SET usage_count = usage_count + 1, used_at = NOW()
		WHERE token_hash = $1
		  AND expires_at > NOW()
		  AND (
			(usage_limit IS NULL AND used_at IS NULL) OR
			(usage_limit IS NOT NULL AND usage_count < usage_limit)
		  )
	`, tokenHash)
	if err != nil {
		return 0, err
	}
	return result.RowsAffected()
}

func (q *Queries) GetClusterPublicRootDomain(ctx context.Context, clusterID string) (string, error) {
	var rootDomain string
	err := q.db.QueryRowContext(ctx, `
		SELECT COALESCE(NULLIF(c.base_url, ''), NULLIF(control.base_url, ''), '')
		FROM quartermaster.infrastructure_clusters c
		LEFT JOIN quartermaster.infrastructure_clusters control
		  ON control.cluster_id = c.control_cell_id AND control.is_active = true
		WHERE c.cluster_id = $1 AND c.is_active = true
	`, clusterID).Scan(&rootDomain)
	return rootDomain, err
}

func (q *Queries) GetClusterInternalFoghornGRPC(ctx context.Context, clusterID string) (string, error) {
	var addr string
	err := q.db.QueryRowContext(ctx, `
		SELECT si.advertise_host || ':' || si.port
		FROM quartermaster.service_instances si
		JOIN quartermaster.service_cluster_assignments sca ON sca.service_instance_id = si.id
		JOIN quartermaster.services svc ON svc.service_id = si.service_id
		WHERE sca.cluster_id = $1
		  AND sca.is_active = true
		  AND si.status = 'running'
		  AND si.health_status = 'healthy'
		  AND si.protocol = 'grpc'
		  AND (si.metadata->>'foghorn_listener' = 'internal_control' OR si.port = 18019 OR si.metadata->>'foghorn_listener' = 'control')
		  AND svc.type = 'foghorn'
		ORDER BY CASE WHEN si.metadata->>'foghorn_listener' = 'internal_control' THEN 0 WHEN si.port = 18019 THEN 1 WHEN si.metadata->>'foghorn_listener' = 'control' THEN 2 ELSE 3 END, si.updated_at DESC, si.id ASC
		LIMIT 1
	`, clusterID).Scan(&addr)
	return strings.TrimSpace(addr), err
}
