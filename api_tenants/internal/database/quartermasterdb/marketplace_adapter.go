package quartermasterdb

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/lib/pq"
)

type MarketplaceClusterRow struct {
	ClusterID, ClusterName                     string
	ShortDescription                           sql.NullString
	Visibility                                 string
	RequiresApproval                           bool
	MaxConcurrentStreams, MaxConcurrentViewers int32
	OwnerName, SubscriptionStatus              sql.NullString
	IsSubscribed                               bool
	CreatedAt                                  time.Time
}

type MarketplacePageFilter struct {
	TenantID   string
	CursorTime *time.Time
	CursorID   string
	Backward   bool
	Limit      int
}

func (q *Queries) ListMarketplaceClustersPage(ctx context.Context, filter MarketplacePageFilter) ([]MarketplaceClusterRow, int32, error) {
	publicOnly := filter.TenantID == ""
	args := []any{}
	baseWhere := "WHERE c.is_active = true AND c.visibility = 'public'"
	accessJoin := ""
	if !publicOnly {
		args = append(args, filter.TenantID)
		baseWhere = "WHERE c.is_active = true AND (c.visibility = 'public' OR c.owner_tenant_id = $1 OR ((c.visibility = 'unlisted' OR c.visibility = 'private') AND a.id IS NOT NULL AND a.is_active = true))"
		accessJoin = "LEFT JOIN quartermaster.tenant_cluster_access a ON c.cluster_id = a.cluster_id AND a.tenant_id = $1"
	}
	var total int32
	if err := q.db.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM quartermaster.infrastructure_clusters c %s %s", accessJoin, baseWhere), args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	direction := "DESC"
	if filter.Backward {
		direction = "ASC"
	}
	where := baseWhere
	if filter.CursorTime != nil {
		op := "<"
		if filter.Backward {
			op = ">"
		}
		where += fmt.Sprintf(" AND (c.created_at, c.cluster_id) %s ($%d, $%d)", op, len(args)+1, len(args)+2)
		args = append(args, *filter.CursorTime, filter.CursorID)
	}
	subscriptionStatus, subscribed := "''", "false"
	if !publicOnly {
		subscriptionStatus = "COALESCE(a.subscription_status, '')"
		subscribed = "CASE WHEN a.id IS NOT NULL AND a.is_active THEN true ELSE false END"
	}
	query := fmt.Sprintf(`SELECT c.cluster_id, c.cluster_name, c.short_description, c.visibility, c.requires_approval,
		c.max_concurrent_streams, c.max_concurrent_viewers, t.name as owner_name, %s, %s, c.created_at
		FROM quartermaster.infrastructure_clusters c LEFT JOIN quartermaster.tenants t ON c.owner_tenant_id = t.id %s
		%s ORDER BY c.created_at %s, c.cluster_id %s LIMIT $%d`, subscriptionStatus, subscribed, accessJoin, where, direction, direction, len(args)+1)
	args = append(args, filter.Limit)
	rows, err := q.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var result []MarketplaceClusterRow
	for rows.Next() {
		var row MarketplaceClusterRow
		if err := rows.Scan(&row.ClusterID, &row.ClusterName, &row.ShortDescription, &row.Visibility, &row.RequiresApproval, &row.MaxConcurrentStreams, &row.MaxConcurrentViewers, &row.OwnerName, &row.SubscriptionStatus, &row.IsSubscribed, &row.CreatedAt); err != nil {
			return nil, 0, err
		}
		result = append(result, row)
	}
	return result, total, rows.Err()
}

func (q *Queries) GetMarketplaceCluster(ctx context.Context, clusterID, tenantID string) (MarketplaceClusterRow, error) {
	query := `
		SELECT c.cluster_id, c.cluster_name, c.short_description, c.visibility, c.requires_approval,
		       c.max_concurrent_streams, c.max_concurrent_viewers, t.name as owner_name,
		       '' as subscription_status, false as is_subscribed, c.created_at
		FROM quartermaster.infrastructure_clusters c
		LEFT JOIN quartermaster.tenants t ON c.owner_tenant_id = t.id
		WHERE c.cluster_id = $1 AND c.is_active = true AND c.visibility IN ('public', 'unlisted')
	`
	args := []any{clusterID}
	if tenantID != "" {
		query = `
			SELECT c.cluster_id, c.cluster_name, c.short_description, c.visibility, c.requires_approval,
			       c.max_concurrent_streams, c.max_concurrent_viewers, t.name as owner_name,
			       COALESCE(a.subscription_status, '') as subscription_status,
			       CASE WHEN a.id IS NOT NULL AND a.is_active THEN true ELSE false END as is_subscribed, c.created_at
			FROM quartermaster.infrastructure_clusters c
			LEFT JOIN quartermaster.tenants t ON c.owner_tenant_id = t.id
			LEFT JOIN quartermaster.tenant_cluster_access a ON c.cluster_id = a.cluster_id AND a.tenant_id = $2
			WHERE c.cluster_id = $1 AND c.is_active = true
			  AND (c.visibility IN ('public', 'unlisted') OR c.owner_tenant_id = $2 OR (c.visibility = 'private' AND a.id IS NOT NULL AND a.is_active = true))
		`
		args = append(args, tenantID)
	}
	var out MarketplaceClusterRow
	err := q.db.QueryRowContext(ctx, query, args...).Scan(&out.ClusterID, &out.ClusterName, &out.ShortDescription,
		&out.Visibility, &out.RequiresApproval, &out.MaxConcurrentStreams, &out.MaxConcurrentViewers,
		&out.OwnerName, &out.SubscriptionStatus, &out.IsSubscribed, &out.CreatedAt)
	return out, err
}

type MarketplaceOwnerRow struct {
	OwnerTenantID sql.NullString
	IsProvider    bool
}

func (q *Queries) GetMarketplaceOwner(ctx context.Context, clusterID, tenantID string) (MarketplaceOwnerRow, error) {
	var out MarketplaceOwnerRow
	err := q.db.QueryRowContext(ctx, `
		SELECT c.owner_tenant_id, COALESCE(t.is_provider, false) as is_provider
		FROM quartermaster.infrastructure_clusters c
		LEFT JOIN quartermaster.tenants t ON t.id = $2
		WHERE c.cluster_id = $1
	`, clusterID, tenantID).Scan(&out.OwnerTenantID, &out.IsProvider)
	return out, err
}

type UpdateMarketplaceParams struct {
	ClusterID                string
	Visibility, ClusterClass *string
	RequiresApproval         *bool
	ShortDescription         *string
}

func (q *Queries) UpdateMarketplace(ctx context.Context, arg UpdateMarketplaceParams) error {
	updates, args := []string{}, []any{}
	add := func(expression string, value any) {
		args = append(args, value)
		updates = append(updates, fmt.Sprintf(expression, len(args)))
	}
	if arg.Visibility != nil {
		add("visibility = $%d", *arg.Visibility)
	}
	if arg.ClusterClass != nil {
		add("cluster_class = CASE WHEN cluster_class = 'platform_official' THEN cluster_class ELSE $%d END", *arg.ClusterClass)
	}
	if arg.RequiresApproval != nil {
		add("requires_approval = $%d", *arg.RequiresApproval)
	}
	if arg.ShortDescription != nil {
		add("short_description = NULLIF($%d, '')", *arg.ShortDescription)
	}
	updates = append(updates, "updated_at = NOW()")
	args = append(args, arg.ClusterID)
	_, err := q.db.ExecContext(ctx, fmt.Sprintf("UPDATE quartermaster.infrastructure_clusters SET %s WHERE cluster_id = $%d", strings.Join(updates, ", "), len(args)), args...)
	return err
}

type ClusterMetadataRow struct {
	ClusterID, ClusterName                     string
	ShortDescription                           sql.NullString
	Visibility                                 string
	RequiresApproval                           bool
	OwnerName                                  sql.NullString
	MaxConcurrentStreams, MaxConcurrentViewers int32
	IsSubscribed                               bool
	SubscriptionStatus                         string
	IsPlatformOfficial                         bool
}

func (q *Queries) ListClusterMetadata(ctx context.Context, tenantID string, clusterIDs []string) ([]ClusterMetadataRow, error) {
	rows, err := q.db.QueryContext(ctx, `
		SELECT c.cluster_id, c.cluster_name, c.short_description, c.visibility, c.requires_approval, t.name AS owner_name,
		       c.max_concurrent_streams, c.max_concurrent_viewers, COALESCE(a.id IS NOT NULL, false) AS is_subscribed,
		       COALESCE(a.subscription_status, 'none') AS subscription_status, c.is_platform_official
		FROM quartermaster.infrastructure_clusters c
		LEFT JOIN quartermaster.tenants t ON c.owner_tenant_id = t.id
		LEFT JOIN quartermaster.tenant_cluster_access a ON c.cluster_id = a.cluster_id AND a.tenant_id = $1
		WHERE c.cluster_id = ANY($2) AND c.is_active = true
	`, tenantID, pq.Array(clusterIDs))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []ClusterMetadataRow
	for rows.Next() {
		var row ClusterMetadataRow
		if err := rows.Scan(&row.ClusterID, &row.ClusterName, &row.ShortDescription, &row.Visibility,
			&row.RequiresApproval, &row.OwnerName, &row.MaxConcurrentStreams, &row.MaxConcurrentViewers,
			&row.IsSubscribed, &row.SubscriptionStatus, &row.IsPlatformOfficial); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}
